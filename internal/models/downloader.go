// Package models — 模型下载器。
//
// 支持两种后端下载路径：
//
//   whisper.cpp（原生二进制）：下载 ggml-<name>.bin → ModelsPath
//   faster-whisper（Python wrapper）：调 wrapper --download-only → HF cache
//
// URL 模板（whisper.cpp）：
//   - huggingface.co: https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-<name>.bin
//   - hf-mirror.com:  https://hf-mirror.com/ggerganov/whisper.cpp/resolve/main/ggml-<name>.bin
//
// Concurrent 下载：同一 model 同时只允许一个任务（globalDownloads sync.Map 去重）。
//
// 进度通过 ProgressFn 推送（0-100 整数；完成后推 100）。
package models

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProgressFn 0-100 进度。下载完成时调用一次 100。
type ProgressFn func(percent int)

// Downloader 模型下载器。
type Downloader struct {
	ModelsPath string
	Source     string // "huggingface" | "hf-mirror" | "hf-cdn"（whisper.cpp 和 faster-whisper 共用）
	Engine     string // "whisper-cli" | "faster-whisper"
	Wrapper    string // faster-whisper 模式：wrapper 路径（默认 whisper-cli 即 PATH 中的 trampoline）
	http       *http.Client
}

// globalDownloads 防止并发下载同 model：值为 nil 占位。
var globalDownloads sync.Map

// NewDownloader 构造。Source 默认 hf-mirror（境内网络更快）。
// engine 为 "faster-whisper" 时走 Python wrapper 下载。
func NewDownloader(modelsPath, source, engine, wrapper string) *Downloader {
	switch source {
	case "huggingface", "hf-cdn":
		// ok
	default:
		source = "hf-mirror"
	}
	if wrapper == "" {
		wrapper = "whisper-cli"
	}
	if engine == "" {
		engine = "whisper-cli"
	}
	return &Downloader{
		ModelsPath: modelsPath,
		Source:     source,
		Engine:     engine,
		Wrapper:    wrapper,
		http: &http.Client{
			Timeout: 30 * time.Minute,
		},
	}
}

// Download 启动一次下载。同名重复调用返回 error。
func (d *Downloader) Download(ctx context.Context, model string, onProgress ProgressFn) error {
	if model == "" {
		return fmt.Errorf("model name empty")
	}
	if _, dup := globalDownloads.LoadOrStore(model, struct{}{}); dup {
		return fmt.Errorf("model %s 正在下载中", model)
	}
	defer globalDownloads.Delete(model)

	if d.Engine == "faster-whisper" {
		return d.downloadFasterWhisper(ctx, model, onProgress)
	}
	return d.downloadWhisperCpp(ctx, model, onProgress)
}

// downloadFasterWhisper 调 Python wrapper --download-only，模型进 HF cache。
// 通过 HF_ENDPOINT 环境变量控制镜像源。
func (d *Downloader) downloadFasterWhisper(ctx context.Context, model string, onProgress ProgressFn) error {
	cmd := exec.CommandContext(ctx, d.Wrapper, "--download-only", "-m", model)
	// 设置 HF 镜像
	cmd.Env = os.Environ()
	switch d.Source {
	case "hf-mirror":
		cmd.Env = append(cmd.Env, "HF_ENDPOINT=https://hf-mirror.com")
	case "hf-cdn":
		cmd.Env = append(cmd.Env, "HF_ENDPOINT=https://hf-cdn.sufy.com")
	}
	var stderrBuf strings.Builder
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("wrapper stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("wrapper start: %w", err)
	}
	// 读 stderr 直到 EOF。huggingface_hub 的 tqdm 进度条用 \r 刷新，
	// Go 端必须按 \r/\n 切分才能抓到 45% 这类中间值。
	buf := make([]byte, 64*1024)
	remain := ""
	for {
		n, err := stderr.Read(buf)
		if n > 0 {
			s := remain + string(buf[:n])
			stderrBuf.WriteString(s)
			// 按 \r 或 \n 切分行，tqdm 用 \r 原地刷新
			lines := strings.FieldsFunc(s, func(r rune) bool { return r == '\r' || r == '\n' })
			// 最后一段可能不完整（下一次 Read 继续拼接）
			remain = ""
			if len(lines) > 0 && !strings.HasSuffix(s, "\n") && !strings.HasSuffix(s, "\r") {
				remain = lines[len(lines)-1]
				lines = lines[:len(lines)-1]
			}
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				// tqdm: "Fetching 5 files:  45%|████▌     | 697M/1.55G ..."
				// wrapper: "whisper_print_progress_callback: progress = 100%"
				if pct := extractDownloadPercent(line); pct >= 0 && onProgress != nil {
					onProgress(pct)
				}
			}
		}
		if err != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		tail := stderrBuf.String()
		if len(tail) > 1000 {
			tail = tail[len(tail)-1000:]
		}
		return fmt.Errorf("wrapper download failed: %w\nstderr: %s", err, tail)
	}
	if onProgress != nil {
		onProgress(100)
	}
	return nil
}

// downloadWhisperCpp 下载 ggml-*.bin 到 ModelsPath。
func (d *Downloader) downloadWhisperCpp(ctx context.Context, model string, onProgress ProgressFn) error {

	if err := os.MkdirAll(d.ModelsPath, 0o755); err != nil {
		return fmt.Errorf("mkdir modelsPath: %w", err)
	}

	host := "hf-mirror.com"
	if d.Source == "huggingface" {
		host = "huggingface.co"
	} else if d.Source == "hf-cdn" {
		host = "hf-cdn.sufy.com"
	}
	url := fmt.Sprintf("https://%s/ggerganov/whisper.cpp/resolve/main/ggml-%s.bin", host, model)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download %s HTTP %d", url, resp.StatusCode)
	}

	total := resp.ContentLength
	dst := filepath.Join(d.ModelsPath, "ggml-"+model+".bin")
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	pr := &progressReader{rd: resp.Body, total: total, onProgress: onProgress}
	_, err = io.Copy(f, pr)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("download body: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	if onProgress != nil {
		onProgress(100)
	}
	return nil
}

// IsDownloading 该 model 当前是否在下载。
func IsDownloading(model string) bool {
	_, ok := globalDownloads.Load(model)
	return ok
}

// progressReader 包装 io.Reader 计算累计字节并推送进度。
type progressReader struct {
	rd         io.Reader
	total      int64
	read       atomic.Int64
	lastReport int
	onProgress ProgressFn
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.rd.Read(buf)
	if n > 0 {
		now := p.read.Add(int64(n))
		if p.onProgress != nil && p.total > 0 {
			pct := int(now * 100 / p.total)
			if pct < 0 {
				pct = 0
			}
			if pct > 99 {
				pct = 99
			}
			// 每 1% 步进推一次
			if pct > p.lastReport {
				p.lastReport = pct
				p.onProgress(pct)
			}
		}
	}
	return n, err
}

// tqdmPercent 匹配 huggingface_hub tqdm 输出：  " 45%|██..."
// 也匹配 wrapper 的 "progress = 100%"
var tqdmPercent = regexp.MustCompile(`(\d+)%`)

// extractDownloadPercent 从一行 stderr 中提取百分比。无匹配返回 -1。
func extractDownloadPercent(line string) int {
	// 优先匹配 tqdm 行（"Fetching N files:" 开头的行有百分比）
	if !strings.Contains(line, "%") {
		return -1
	}
	m := tqdmPercent.FindStringSubmatch(line)
	if len(m) < 2 {
		return -1
	}
	pct, err := strconv.Atoi(m[1])
	if err != nil {
		return -1
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}
