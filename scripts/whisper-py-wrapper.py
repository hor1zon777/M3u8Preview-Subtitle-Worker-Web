#!/usr/bin/env python3
"""
whisper-cli CLI 兼容 wrapper —— 底层用 faster-whisper。

接受 whisper.cpp CLI 参数子集，翻译成 faster-whisper API 调用。
Go backend (internal/asr/whisper.go) 下发的所有参数必须兼容。

用法：
  whisper-cli -m <model> -f <wav> -l <lang> -osrt -of <basename> [-pp] [-pc] [-ng]
              [-mc <int>] [--prompt <str>]
              [--vad [--vad-threshold <v> --vad-min-speech-duration-ms <ms> ...]]

  <model> 支持三种形式：
    - HF model id（如 large-v3、Systran/faster-whisper-large-v3）
    - CT2 本地目录
    - ggml-<name>.bin 路径 → 自动提取 <name> 作为 HF id
"""

import argparse, os, re, sys
from pathlib import Path

# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def parse_args():
    p = argparse.ArgumentParser(allow_abbrev=False)
    p.add_argument('-m', dest='model_path', required=True,
                   help='HF model id / CT2 dir / ggml-<name>.bin path')
    p.add_argument('-f', dest='wav', default='',
                   help='16kHz mono PCM WAV')
    p.add_argument('-l', dest='lang', default='auto')
    p.add_argument('-osrt', action='store_true', default=True)
    p.add_argument('-of', dest='of', default='',
                   help='output .srt basename（无后缀）')
    p.add_argument('-pp', action='store_true', default=False)
    p.add_argument('-pc', action='store_true', default=False)
    p.add_argument('-ng', dest='no_gpu', action='store_true', default=False)
    p.add_argument('-mc', dest='max_context', type=int, default=-1)
    p.add_argument('--prompt', dest='prompt', default='')
    p.add_argument('--download-only', dest='download_only', action='store_true', default=False,
                   help='仅下载模型到 HF cache，不做 ASR')

    # VAD flags
    p.add_argument('--vad', action='store_true', default=False)
    p.add_argument('--vad-model', default='')
    p.add_argument('--vad-threshold', type=float, default=0.5)
    p.add_argument('--vad-min-speech-duration-ms', type=int, default=250)
    p.add_argument('--vad-min-silence-duration-ms', type=int, default=100)
    p.add_argument('--vad-max-speech-duration-s', type=float, default=0)
    p.add_argument('--vad-speech-pad-ms', type=int, default=30)
    p.add_argument('--vad-samples-overlap', type=float, default=0.1)

    return p.parse_args()


# ---------------------------------------------------------------------------
# model path → HF id
# ---------------------------------------------------------------------------

def resolve_model(raw: str) -> str:
    """
    /data/models/ggml-large-v3.bin → large-v3
    /data/models/ggml-large-v3-turbo.bin → large-v3-turbo
    Systran/faster-whisper-large-v3 → 原样
    large-v3 → 原样
    mobiuslabsgmbh--faster-whisper-large-v3-turbo → mobiuslabsgmbh/faster-whisper-large-v3-turbo
    /path/to/ct2_model/ → 原样（CT2 本地目录）
    """
    name = Path(raw).stem          # /a/b/ggml-large-v3.bin → ggml-large-v3
    name = re.sub(r'^ggml-', '', name)
    # HF cache 目录格式: org--model → org/model
    if '/' not in name and '--' in name:
        name = name.replace('--', '/', 1)
    return name


# ---------------------------------------------------------------------------
# SRT time formatting
# ---------------------------------------------------------------------------

def fmt_ts(seconds: float) -> str:
    h = int(seconds // 3600)
    m = int(seconds % 3600 // 60)
    s = seconds % 60
    return f"{h:02d}:{m:02d}:{s:06.3f}".replace('.', ',')


# ---------------------------------------------------------------------------
# diagnostics
# ---------------------------------------------------------------------------

def log(msg: str):
    """统一前缀日志，便于 Go 端 stderr 抓取归类。"""
    print(f"[wrapper] {msg}", file=sys.stderr, flush=True)


def detect_cuda():
    """探测 CUDA / cuDNN 可用性，返回 (ok: bool, detail: str)。"""
    try:
        import ctranslate2  # noqa
        cuda_count = ctranslate2.get_cuda_device_count()
        if cuda_count > 0:
            return True, f"ctranslate2 reports {cuda_count} CUDA device(s)"
        return False, "ctranslate2.get_cuda_device_count() == 0 (no CUDA runtime / driver)"
    except Exception as e:
        return False, f"ctranslate2 probe error: {e}"


def resolve_device(no_gpu_flag: bool):
    """
    解析最终 device + compute_type。
    返回 (device, compute_type, reason)。
    """
    if no_gpu_flag:
        return 'cpu', 'int8', '-ng requested by caller'
    ok, detail = detect_cuda()
    if ok:
        return 'cuda', 'float16', detail
    log(f"WARN: CUDA unavailable, falling back to CPU: {detail}")
    return 'cpu', 'int8', f'CUDA unavailable, fallback ({detail})'


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------

def main():
    a = parse_args()
    model_id = resolve_model(a.model_path)

    device, compute_type, dev_reason = resolve_device(a.no_gpu)
    log(f"model_id={model_id} device={device} compute_type={compute_type} ({dev_reason})")

    # --- download-only mode ---
    if a.download_only:
        from faster_whisper import WhisperModel
        print(f"downloading model {model_id} to HF cache ...", file=sys.stderr, flush=True)
        # WhisperModel 构造时自动下载；huggingface_hub 的 tqdm 进度条输出到 stderr
        WhisperModel(model_id, device=device, compute_type=compute_type)
        print("", file=sys.stderr, flush=True)
        print("whisper_print_progress_callback: progress = 100%", file=sys.stderr, flush=True)
        sys.exit(0)

    if not a.wav:
        log("ERROR: -f <wav> is required (except --download-only)")
        sys.exit(2)
    if not a.of:
        log("ERROR: -of <basename> is required (except --download-only)")
        sys.exit(2)

    # --- progress 回调 ---
    last_pct = [-1]

    def on_progress(seg_end_sec: float, total_sec: float):
        if total_sec <= 0:
            return
        pct = min(99, int(seg_end_sec / total_sec * 100))
        if pct != last_pct[0]:
            last_pct[0] = pct
            print(f"whisper_print_progress_callback: progress = {pct}%",
                  file=sys.stderr, flush=True)

    # --- faster-whisper ---
    from faster_whisper import WhisperModel

    log(f"loading model {model_id} ...")
    try:
        model = WhisperModel(model_id, device=device, compute_type=compute_type)
    except Exception as e:
        # 常见：CUDA 库缺失但 device='cuda' / 模型仓库 404 / 磁盘满
        log(f"ERROR: WhisperModel init failed: {e!r}")
        raise
    log("model loaded")

    # --- VAD ---
    vad_params = None
    if a.vad:
        # threshold 越大越严格，>0.9 几乎肯定全部过滤；这里 clamp 并 warn
        threshold = a.vad_threshold
        if threshold >= 0.9:
            log(f"WARN: vad-threshold={threshold} too strict, clamping to 0.6 to avoid empty output")
            threshold = 0.6
        vad_params = dict(
            threshold=threshold,
            min_speech_duration_ms=a.vad_min_speech_duration_ms,
            min_silence_duration_ms=a.vad_min_silence_duration_ms,
            speech_pad_ms=a.vad_speech_pad_ms,
        )
        if a.vad_max_speech_duration_s > 0:
            vad_params['max_speech_duration_s'] = a.vad_max_speech_duration_s
        log(f"VAD enabled: {vad_params}")
    else:
        log("VAD disabled")

    # --- transcribe ---
    lang = None if a.lang in ('', 'auto') else a.lang
    log(f"transcribe start lang={lang or 'auto'} prompt_len={len(a.prompt)} max_ctx={a.max_context}")
    segments, info = model.transcribe(
        a.wav,
        language=lang,
        initial_prompt=a.prompt or None,
        vad_filter=a.vad,
        vad_parameters=vad_params,
        **({} if a.max_context <= 0 else {'max_initial_prompt_ctx': a.max_context}),
    )
    log(f"detected lang={info.language} prob={info.language_probability:.2f} duration={info.duration:.1f}s")

    # --- write SRT ---
    srt_path = f"{a.of}.srt"
    total = info.duration or 1.0
    seg_count = 0
    with open(srt_path, 'w', encoding='utf-8') as f:
        for i, seg in enumerate(segments, 1):
            text = seg.text.strip()
            if not text:
                continue
            seg_count += 1
            f.write(f"{i}\n{fmt_ts(seg.start)} --> {fmt_ts(seg.end)}\n{text}\n\n")
            if a.pp:
                on_progress(seg.end, total)

    if a.pp:
        print("whisper_print_progress_callback: progress = 100%",
              file=sys.stderr, flush=True)

    log(f"transcribe done: {seg_count} segments written to {srt_path}")

    # 与 whisper-cli 行为对齐：SRT 为空的 → exit 非 0，并给出诊断
    if seg_count == 0 or os.path.getsize(srt_path) == 0:
        reason = []
        if a.vad:
            reason.append(f"VAD active (threshold={vad_params.get('threshold') if vad_params else 'n/a'}); "
                          "考虑降低 threshold 或关闭 VAD 重试")
        if info.duration < 1.0:
            reason.append(f"audio duration only {info.duration:.2f}s (可能音频解码异常)")
        if device == 'cpu':
            reason.append("running on CPU — 性能可能不足以在合理时间内出结果")
        reason_str = "; ".join(reason) or "unknown (try disabling VAD or lowering threshold)"
        log(f"ERROR: whisper output SRT is empty. duration={info.duration:.1f}s "
            f"detected_lang={info.language} reason={reason_str}")
        sys.exit(1)


if __name__ == '__main__':
    main()
