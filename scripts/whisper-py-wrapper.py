#!/usr/bin/env python3
"""
whisper-cli CLI 兼容 wrapper —— 底层用 faster-whisper。

两种工作模式：

1. CLI（默认）— 一次性进程，跑完单个 job 退出。
     whisper-cli -m <model> -f <wav> -l <lang> -osrt -of <basename> ...

2. SERVE — 常驻服务进程，模型加载一次后通过 Unix Domain Socket 持续服务。
     whisper-cli --serve --socket /tmp/mws-whisper.sock -m <model> [-ng]
   启动完成后向 stdout 输出 "READY" 一行，stderr 持续输出诊断日志。
   客户端协议（每行一条 JSON）：
     请求:  {"id":"<job_id>", "wav":"...", "of":"...", "lang":"ja",
             "prompt":"", "max_context":-1, "vad":false, "vad_params":{...}}
     响应（流式，多条）:
       {"id":..., "event":"log",      "msg":"..."}
       {"id":..., "event":"info",     "language":"ja", "duration":8142.0, "vad":false}
       {"id":..., "event":"progress", "pct":42}
       {"id":..., "event":"done",     "srt_path":"...", "segments":292}
       {"id":..., "event":"error",    "msg":"..."}
     另: {"event":"ping"} → {"event":"pong"} 用于健康检查。

  <model> 支持三种形式：
    - HF model id（如 large-v3、Systran/faster-whisper-large-v3）
    - CT2 本地目录
    - ggml-<name>.bin 路径 → 自动提取 <name> 作为 HF id
"""

import argparse, json, os, re, socket, sys
from collections import Counter
from pathlib import Path

# ---------------------------------------------------------------------------
# CLI args
# ---------------------------------------------------------------------------

def parse_args():
    p = argparse.ArgumentParser(allow_abbrev=False)
    # 模式选择
    p.add_argument('--serve', action='store_true', default=False,
                   help='以常驻服务模式运行，监听 Unix Socket')
    p.add_argument('--socket', dest='socket', default='/tmp/mws-whisper.sock',
                   help='Unix Socket 路径（仅 --serve 模式）')
    p.add_argument('--download-only', dest='download_only', action='store_true', default=False,
                   help='仅下载模型到 HF cache，不做 ASR')

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

    # VAD flags（仅 CLI 模式使用；serve 模式 VAD 由 JSON 请求传入）
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
# helpers shared by CLI + serve
# ---------------------------------------------------------------------------

def resolve_model(raw: str) -> str:
    """
    /data/models/ggml-large-v3.bin → large-v3
    Systran/faster-whisper-large-v3 → 原样
    mobiuslabsgmbh--faster-whisper-large-v3-turbo → mobiuslabsgmbh/faster-whisper-large-v3-turbo
    /path/to/ct2_model/ → 原样（CT2 本地目录）
    """
    name = Path(raw).stem
    name = re.sub(r'^ggml-', '', name)
    if '/' not in name and '--' in name:
        name = name.replace('--', '/', 1)
    return name


def fmt_ts(seconds: float) -> str:
    h = int(seconds // 3600)
    m = int(seconds % 3600 // 60)
    s = seconds % 60
    return f"{h:02d}:{m:02d}:{s:06.3f}".replace('.', ',')


def log(msg: str):
    """诊断日志统一前缀，便于 Go 端 stderr 抓取归类。"""
    print(f"[wrapper] {msg}", file=sys.stderr, flush=True)


def detect_cuda():
    try:
        import ctranslate2  # noqa
        cuda_count = ctranslate2.get_cuda_device_count()
        if cuda_count > 0:
            return True, f"ctranslate2 reports {cuda_count} CUDA device(s)"
        return False, "ctranslate2.get_cuda_device_count() == 0 (no CUDA runtime / driver)"
    except Exception as e:
        return False, f"ctranslate2 probe error: {e}"


def resolve_device(no_gpu_flag: bool):
    if no_gpu_flag:
        return 'cpu', 'int8', '-ng requested by caller'
    ok, detail = detect_cuda()
    if ok:
        return 'cuda', 'float16', detail
    log(f"WARN: CUDA unavailable, falling back to CPU: {detail}")
    return 'cpu', 'int8', f'CUDA unavailable, fallback ({detail})'


def clamp_vad_params(vad_params):
    """
    防呆 1：vad-threshold >=0.9 视为异常严格，clamp 到 0.6 避免空输出。
    防呆 2：max_speech_duration_s 未设置 / 过大 时强制兜底 30 秒，避免 silero
            把超长连续语音段（>60s）整段交给 Whisper，导致 Whisper 在长段里
            大量判 no_speech 只输出 1 句的"沉默幻觉"。
    """
    if not vad_params:
        vad_params = {}
    vad_params = dict(vad_params)
    threshold = vad_params.get('threshold', 0.5)
    if threshold >= 0.9:
        log(f"WARN: vad-threshold={threshold} too strict, clamping to 0.6")
        vad_params['threshold'] = 0.6
    max_s = vad_params.get('max_speech_duration_s')
    if max_s is None or max_s <= 0 or max_s > 60:
        # silero 默认 inf（永不切长段）；给 30s 兜底
        if max_s is not None and max_s > 60:
            log(f"WARN: max_speech_duration_s={max_s}s too long, clamping to 30s "
                f"(避免 silero 把长段整段丢给 Whisper)")
        vad_params['max_speech_duration_s'] = 30.0
    return vad_params


def do_transcribe(model, wav, of, lang_in, prompt, max_ctx, vad, vad_params,
                  on_progress=None, on_log=None):
    """
    单次 transcribe + 自动 fallback。返回 (seg_count, info, srt_path)。

    防 Whisper 幻觉策略：
      - condition_on_previous_text=False  关闭跨段上下文（OpenAI Whisper paper §3.7
        推荐的最有效防幻觉手段；否则一个错的短语会反复传染）
      - 显式 temperature fallback 列表，让 hallucination 触发时模型自动重试
      - 若结果中同一短语出现频率 > 30% 且没启用 VAD → 自动用 faster-whisper 默认
        VAD 参数重跑一次（VAD 会跳过静音段，是消除幻觉的根本手段）

      on_progress(pct: int) - 每整数百分点触发
      on_log(msg: str)      - 阶段性诊断
    """
    def emit_log(msg):
        if on_log:
            on_log(msg)
        else:
            log(msg)

    lang = None if lang_in in ('', 'auto') else lang_in

    def run(use_vad, vp):
        srt_path = f"{of}.srt"
        segments, info = model.transcribe(
            wav,
            language=lang,
            initial_prompt=prompt or None,
            vad_filter=use_vad,
            vad_parameters=vp if use_vad else None,
            # ----------------- 防幻觉关键参数 -----------------
            # 关闭跨段上下文 — 单条错误不会传染后续
            condition_on_previous_text=False,
            # 默认 fallback temperature；hallucination 触发时模型重试
            temperature=[0.0, 0.2, 0.4, 0.6, 0.8, 1.0],
            # 压缩比 > 2.4 视为 hallucination
            compression_ratio_threshold=2.4,
            # 平均 log-prob < -1.0 视为低置信度
            log_prob_threshold=-1.0,
            # 静音概率 > 0.8 才跳过（默认 0.6 在 BGM/喘息混合段会把大量
            # 真实语音判成 no_speech 跳过，导致 long segment 只输出 1 句的"沉默幻觉"）
            no_speech_threshold=0.8,
            # ----------------- 段切分精度关键参数 -----------------
            # 启用 word-level 时间戳（cross-attention 重对齐）。
            # 关闭时 segment 时间戳来自模型自己的 <|t|> token，遇到不确定段
            # 会"懒人地"匀分时间戳，输出"每 2 秒整一段"这种反自然结果。
            # 开启后 segment 边界基于真实 word 时间戳，更贴合说话节奏；
            # 代价：推理时间 +30% 左右。
            word_timestamps=True,
            **({} if max_ctx <= 0 else {'max_initial_prompt_ctx': max_ctx}),
        )
        emit_log(f"detected lang={info.language} prob={info.language_probability:.2f} "
                 f"duration={info.duration:.1f}s vad={use_vad}")
        total = float(info.duration) or 1.0
        seg_count = 0
        last_pct = -1
        text_counter = Counter()
        # 段时间戳异常检测：若前 N 段出现"恰好等距 + 共享毫秒尾数"模式，
        # 通常意味着模型在不确定段输出了整数秒匀分时间戳。开启 word_timestamps
        # 后理论上不应再出现，仍保留诊断 log 以便回归确认。
        first_segments_dump = []
        with open(srt_path, 'w', encoding='utf-8') as f:
            for i, seg in enumerate(segments, 1):
                text = seg.text.strip()
                if not text:
                    continue
                seg_count += 1
                text_counter[text] += 1
                f.write(f"{i}\n{fmt_ts(seg.start)} --> {fmt_ts(seg.end)}\n{text}\n\n")
                if seg_count <= 30:
                    first_segments_dump.append(
                        f"  seg[{seg_count:3d}] {seg.start:7.3f}s -> {seg.end:7.3f}s "
                        f"(len={seg.end - seg.start:5.3f}s) {text[:40]!r}"
                    )
                if on_progress:
                    pct = min(99, int(seg.end / total * 100))
                    if pct != last_pct:
                        last_pct = pct
                        on_progress(pct)
        if first_segments_dump:
            emit_log(f"first {len(first_segments_dump)} segments (vad={use_vad}):\n"
                     + "\n".join(first_segments_dump))
        return seg_count, info, srt_path, text_counter

    seg_count, info, srt_path, text_counter = run(vad, vad_params)
    actual_vad = vad  # 实际最后一次跑用的 VAD 状态（VAD 0 段 fallback 后会改成 False）
    if seg_count == 0 and vad:
        emit_log(f"WARN: VAD produced 0 segments (threshold="
                 f"{vad_params.get('threshold') if vad_params else 'n/a'}); "
                 f"retrying without VAD to recover")
        seg_count, info, srt_path, text_counter = run(False, None)
        actual_vad = False
        if seg_count > 0:
            emit_log(f"recovered {seg_count} segments without VAD — "
                     f"建议在设置中降低 vadThreshold 或关闭 VAD")

    # --- 幻觉检测 ---
    if seg_count >= 5 and text_counter:
        top_text, top_count = text_counter.most_common(1)[0]
        ratio = top_count / seg_count
        if top_count >= 5 and ratio > 0.3:
            emit_log(f"WARN: hallucination suspected — top phrase '{top_text[:50]}' "
                     f"appeared {top_count} times ({ratio*100:.0f}% of {seg_count} segments)")
            if not actual_vad:
                # 当前实际没在用 VAD → 尝试用 faster-whisper 默认 VAD 救一下
                emit_log("retrying with default-VAD to suppress hallucination ...")
                retry_count, retry_info, retry_path, retry_counter = run(True, None)
                if retry_count > 0:
                    rt_top_text, rt_top_count = retry_counter.most_common(1)[0]
                    rt_ratio = rt_top_count / retry_count
                    if rt_top_count < 5 or rt_ratio <= 0.3:
                        emit_log(f"recovered {retry_count} segments with default VAD "
                                 f"(top phrase ratio dropped from {ratio*100:.0f}% to {rt_ratio*100:.0f}%)")
                        seg_count, info, srt_path = retry_count, retry_info, retry_path
                    else:
                        emit_log(f"VAD retry still shows hallucination "
                                 f"(top {rt_top_count}/{retry_count} = {rt_ratio*100:.0f}%) — "
                                 f"音频可能本身有大量静音或 BGM，建议检查输入")
                        seg_count, info, srt_path = retry_count, retry_info, retry_path
            else:
                # 已经在 VAD 模式下仍有幻觉 → 单纯 warn，提示用户检查音频
                emit_log("已启用 VAD 仍出现高频重复短语 — 音频可能有较多 BGM/纯音乐段，"
                         "或视频末尾片头片尾类台词重复出现。"
                         "可尝试：1) 调高 vad-threshold 至 0.5-0.7；"
                         "2) 调高 min-silence-duration-ms 至 1000-2000；"
                         "3) 检查源音频是否有效")

    return seg_count, info, srt_path


# ---------------------------------------------------------------------------
# CLI mode (one-shot, original behavior)
# ---------------------------------------------------------------------------

def cli_main(a):
    model_id = resolve_model(a.model_path)
    device, compute_type, dev_reason = resolve_device(a.no_gpu)
    log(f"model_id={model_id} device={device} compute_type={compute_type} ({dev_reason})")

    if a.download_only:
        from faster_whisper import WhisperModel
        print(f"downloading model {model_id} to HF cache ...", file=sys.stderr, flush=True)
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

    from faster_whisper import WhisperModel
    log(f"loading model {model_id} ...")
    try:
        model = WhisperModel(model_id, device=device, compute_type=compute_type)
    except Exception as e:
        log(f"ERROR: WhisperModel init failed: {e!r}")
        raise
    log("model loaded")

    vad_params = None
    if a.vad:
        vad_params = dict(
            threshold=a.vad_threshold,
            min_speech_duration_ms=a.vad_min_speech_duration_ms,
            min_silence_duration_ms=a.vad_min_silence_duration_ms,
            speech_pad_ms=a.vad_speech_pad_ms,
        )
        if a.vad_max_speech_duration_s > 0:
            vad_params['max_speech_duration_s'] = a.vad_max_speech_duration_s
        vad_params = clamp_vad_params(vad_params)
        log(f"VAD enabled: {vad_params}")
    else:
        log("VAD disabled")

    log(f"transcribe start lang={a.lang} prompt_len={len(a.prompt)} max_ctx={a.max_context}")

    def on_progress(pct):
        if a.pp:
            print(f"whisper_print_progress_callback: progress = {pct}%",
                  file=sys.stderr, flush=True)

    seg_count, info, srt_path = do_transcribe(
        model, a.wav, a.of, a.lang, a.prompt, a.max_context, a.vad, vad_params,
        on_progress=on_progress)

    if a.pp:
        print("whisper_print_progress_callback: progress = 100%",
              file=sys.stderr, flush=True)

    log(f"transcribe done: {seg_count} segments written to {srt_path}")

    if seg_count == 0 or os.path.getsize(srt_path) == 0:
        reason = ["已尝试 with-VAD 与 without-VAD 两种模式，均产出 0 段"]
        if info.duration < 1.0:
            reason.append(f"audio duration only {info.duration:.2f}s (可能音频解码异常)")
        if device == 'cpu':
            reason.append("running on CPU — 性能可能不足以在合理时间内出结果")
        log(f"ERROR: whisper output SRT is empty. duration={info.duration:.1f}s "
            f"detected_lang={info.language} reason={'; '.join(reason)}")
        sys.exit(1)


# ---------------------------------------------------------------------------
# Serve mode (persistent server, IPC over Unix Socket)
# ---------------------------------------------------------------------------

def serve_main(a):
    model_id = resolve_model(a.model_path)
    device, compute_type, dev_reason = resolve_device(a.no_gpu)
    log(f"[serve] model_id={model_id} device={device} compute_type={compute_type} ({dev_reason})")

    from faster_whisper import WhisperModel
    log(f"[serve] loading model {model_id} ...")
    try:
        model = WhisperModel(model_id, device=device, compute_type=compute_type)
    except Exception as e:
        log(f"[serve] ERROR: WhisperModel init failed: {e!r}")
        sys.exit(1)
    log("[serve] model loaded")

    socket_path = a.socket
    try:
        if os.path.exists(socket_path):
            os.remove(socket_path)
    except OSError as e:
        log(f"[serve] WARN: cannot remove stale socket {socket_path}: {e}")

    srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    try:
        srv.bind(socket_path)
        os.chmod(socket_path, 0o600)
        srv.listen(4)
    except Exception as e:
        log(f"[serve] ERROR: cannot bind socket {socket_path}: {e}")
        sys.exit(1)

    # 告知 Go 端就绪：READY 走 stdout，stderr 留给诊断
    print("READY", flush=True)
    sys.stdout.flush()
    log(f"[serve] listening on {socket_path}")

    while True:
        try:
            conn, _ = srv.accept()
        except (KeyboardInterrupt, SystemExit):
            log("[serve] shutting down")
            break
        except Exception as e:
            log(f"[serve] accept error: {e}")
            continue
        try:
            handle_client(conn, model)
        except Exception as e:
            log(f"[serve] client handler error: {e}")
        finally:
            try:
                conn.close()
            except Exception:
                pass


def handle_client(conn, model):
    f_in = conn.makefile('r', encoding='utf-8')
    f_out = conn.makefile('w', encoding='utf-8')

    def send(obj):
        f_out.write(json.dumps(obj, ensure_ascii=False) + '\n')
        f_out.flush()

    line = f_in.readline()
    if not line:
        return
    try:
        req = json.loads(line)
    except Exception as e:
        send({'event': 'error', 'msg': f'invalid request json: {e}'})
        return

    # 健康检查
    if req.get('event') == 'ping':
        send({'event': 'pong'})
        return

    job_id = req.get('id', '')
    wav = req.get('wav', '')
    of = req.get('of', '')
    if not wav or not of:
        send({'id': job_id, 'event': 'error', 'msg': 'wav and of required'})
        return

    lang_in = req.get('lang', 'auto')
    prompt = req.get('prompt', '')
    max_ctx = int(req.get('max_context', -1))
    vad = bool(req.get('vad', False))
    vad_params = req.get('vad_params') if vad else None
    if isinstance(vad_params, dict):
        vad_params = clamp_vad_params(vad_params)

    def on_log(msg):
        send({'id': job_id, 'event': 'log', 'msg': msg})

    def on_progress(pct):
        send({'id': job_id, 'event': 'progress', 'pct': int(pct)})

    on_log(f"transcribe start lang={lang_in} prompt_len={len(prompt)} vad={vad}")
    try:
        seg_count, info, srt_path = do_transcribe(
            model, wav, of, lang_in, prompt, max_ctx, vad, vad_params,
            on_progress=on_progress, on_log=on_log)
    except Exception as e:
        import traceback
        send({'id': job_id, 'event': 'error',
              'msg': f'transcribe exception: {e}',
              'traceback': traceback.format_exc()})
        return

    send({'id': job_id, 'event': 'progress', 'pct': 100})
    if seg_count == 0:
        send({'id': job_id, 'event': 'error',
              'msg': f'whisper output SRT is empty. duration={info.duration:.1f}s '
                     f'lang={info.language}'})
        return
    send({'id': job_id, 'event': 'done',
          'srt_path': srt_path, 'segments': seg_count,
          'language': info.language, 'duration': float(info.duration)})


# ---------------------------------------------------------------------------
# entrypoint
# ---------------------------------------------------------------------------

def main():
    a = parse_args()
    if a.serve:
        serve_main(a)
    else:
        cli_main(a)


if __name__ == '__main__':
    main()
