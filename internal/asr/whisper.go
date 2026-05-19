// Package asr — whisper 子进程封装。
//
// 支持两种后端（自动检测）：
//
//   - whisper-cli（whisper.cpp 原版二进制）：-m 传 ggml-<name>.bin 绝对路径
//   - whisper-py-wrapper（faster-whisper Python 脚本）：-m 传模型名（HF id）
//
// 自动检测逻辑：WhisperCliPath 指向的文件如果首行是 #! 脚本，则按 Python wrapper
// 模式处理（跳过 ggml 文件存在检查，模型名直接传给 wrapper）。
//
// 行为对齐原 SmartSub generateSubtitleWithBuiltinWhisper：
//   - 输入 16kHz mono PCM WAV
//   - 输出 SRT 文件
//   - 支持 VAD（silero v6.2.0，由后端内部处理）
//   - 支持 prompt / max_context
//   - 进度从 stderr "progress = N%" 解析
//   - 校验 SRT 文件存在且非空
package asr

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/config"
)

// WhisperOptions 单次 whisper 运行的参数。
type WhisperOptions struct {
	WavPath        string
	Model          string // 模型名：ggml 模式 = "large-v3" → ggml-large-v3.bin；wrapper 模式 = HF id
	SourceLanguage string // "auto" 让 whisper 自动检测
	Prompt         string
	MaxContext     int // -1 表示不传
}

// ProgressFn 进度回调（0-100 整数）。
type ProgressFn func(percent int)

// WhisperRunner 封装 whisper 子进程。
type WhisperRunner struct {
	Settings     config.SystemSettings
	isPythonWrap *bool // 缓存检测结果：true = Python 脚本，false = 原生二进制
}

func (w *WhisperRunner) detectEngine() bool {
	if w.isPythonWrap != nil {
		return *w.isPythonWrap
	}
	cli := w.Settings.WhisperCliPath
	if cli == "" {
		cli = "whisper-cli"
	}
	// 如果路径在 $PATH 中，尝试解析
	if !strings.ContainsAny(cli, "/\\") {
		p, err := exec.LookPath(cli)
		if err == nil {
			cli = p
		}
	}
	is := isScriptFile(cli)
	w.isPythonWrap = &is
	return is
}

// isScriptFile 检查文件是否以 #! 开头（shell/Python wrapper）。
func isScriptFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var head [2]byte
	n, _ := f.Read(head[:])
	return n == 2 && head[0] == '#' && head[1] == '!'
}

// Run 调用 whisper 子进程。成功后返回生成的 SRT 文件绝对路径（位于 workDir）。
func (w *WhisperRunner) Run(ctx context.Context, workDir string, opts WhisperOptions, onProgress ProgressFn) (string, error) {
	if opts.Model == "" {
		opts.Model = "large-v3"
	}
	useWrapper := w.detectEngine()
	var modelArg string
	if useWrapper {
		modelArg = opts.Model // 直接传 HF id，wrapper 负责解析
	} else {
		modelPath := filepath.Join(w.Settings.ModelsPath, "ggml-"+opts.Model+".bin")
		if _, err := os.Stat(modelPath); err != nil {
			return "", fmt.Errorf("cannot find model %s (ggml-"+opts.Model+".bin not found in modelsPath=%s): %w",
				opts.Model, w.Settings.ModelsPath, err)
		}
		modelArg = modelPath
	}

	srtBase := filepath.Join(workDir, "subtitle.source")
	srtPath := srtBase + ".srt"

	args := []string{
		"-m", modelArg,
		"-f", opts.WavPath,
		"-l", normLang(opts.SourceLanguage),
		"-osrt",
		"-of", srtBase,
	}
	if useWrapper {
		args = append(args, "-pp") // 要进度输出
	}
	if useWrapper {
		// wrapper 在 $PATH 中的 "whisper-cli" 需要传完整路径，因为 exec.LookPath
		// 在 cmd.Start 里也会做，但对 trampoline 脚本正常
	}
	// GPU：whisper-cli 默认开 GPU；只在 useCuda=false 时显式关
	if !w.Settings.UseCuda {
		args = append(args, "-ng")
	}
	if opts.Prompt != "" {
		args = append(args, "--prompt", opts.Prompt)
	}
	if opts.MaxContext > 0 {
		args = append(args, "-mc", strconv.Itoa(opts.MaxContext))
	}
	if w.Settings.UseVAD {
		vadModel := filepath.Join(w.Settings.AssetsPath, "ggml-silero-v6.2.0.bin")
		args = append(args,
			"--vad",
			"--vad-model", vadModel,
			"--vad-threshold", strconv.FormatFloat(w.Settings.VadThreshold, 'f', 2, 64),
			"--vad-min-speech-duration-ms", strconv.Itoa(w.Settings.VadMinSpeechDuration),
			"--vad-min-silence-duration-ms", strconv.Itoa(w.Settings.VadMinSilenceDuration),
			"--vad-speech-pad-ms", strconv.Itoa(w.Settings.VadSpeechPad),
			"--vad-samples-overlap", strconv.FormatFloat(w.Settings.VadSamplesOverlap, 'f', 2, 64),
		)
		if w.Settings.VadMaxSpeechDuration > 0 {
			args = append(args, "--vad-max-speech-duration-s",
				strconv.FormatFloat(float64(w.Settings.VadMaxSpeechDuration)/1000.0, 'f', 2, 64))
		}
	}

	cmd := exec.CommandContext(ctx, w.Settings.WhisperCliPath, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("whisper stderr pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("whisper stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("whisper start: %w (path=%s)", err, w.Settings.WhisperCliPath)
	}

	go drain(stdout)
	stderrTail := newRing(8192)
	scanStderrProgress(stderr, onProgress, stderrTail)

	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("whisper exit: %w; stderr: %s", err, stderrTail.String())
	}

	st, err := os.Stat(srtPath)
	if err != nil {
		return "", fmt.Errorf("whisper 未生成 SRT 文件: %w", err)
	}
	if st.Size() == 0 {
		msg := "whisper 输出 SRT 为空。"
		if useWrapper {
			msg += " 常见原因：模型首次下载失败 / VAD 过滤掉全部语音 / 音频静音。"
		} else {
			msg += " 常见原因：VAD 阈值过严过滤掉全部语音段 / 模型文件未下载或路径错误 / CUDA 配置异常 / 音频本身静音。"
		}
		return "", fmt.Errorf(msg)
	}
	return srtPath, nil
}

var progressRe = regexp.MustCompile(`progress\s*=\s*(\d+)\s*%`)

func scanStderrProgress(r io.Reader, onProgress ProgressFn, tail *ring) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*64), 1024*1024)
	lastPct := -1
	for scanner.Scan() {
		line := scanner.Text()
		tail.Write([]byte(line + "\n"))
		if onProgress == nil {
			continue
		}
		if m := progressRe.FindStringSubmatch(line); len(m) == 2 {
			pct, err := strconv.Atoi(strings.TrimSpace(m[1]))
			if err != nil {
				continue
			}
			if pct != lastPct {
				lastPct = pct
				onProgress(pct)
			}
		}
	}
}

func drain(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		if _, err := r.Read(buf); err != nil {
			return
		}
	}
}

func normLang(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "auto" {
		return "auto"
	}
	return s
}

// ring 简单环形缓冲，用于截取 stderr 末尾 N 字节作为错误上下文。
type ring struct {
	buf []byte
	cap int
}

func newRing(c int) *ring { return &ring{cap: c} }
func (r *ring) Write(p []byte) (int, error) {
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.cap {
		r.buf = r.buf[len(r.buf)-r.cap:]
	}
	return len(p), nil
}
func (r *ring) String() string { return string(r.buf) }
