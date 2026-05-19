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
	Source     string // "huggingface" | "hf-mirror"（仅 whisper.cpp 模式）
	Engine     string // "whisper-cli" | "faster-whisper"
	Wrapper    string // faster-whisper 模式：wrapper 路径（默认 whisper-cli 即 PATH 中的 trampoline）
	http       *http.Client
}

// globalDownloads 防止并发下载同 model：值为 nil 占位。
var globalDownloads sync.Map

// NewDownloader 构造。Source 默认 hf-mirror（境内网络更快）。
// engine 为 "faster-whisper" 时走 Python wrapper 下载。
func NewDownloader(modelsPath, source, engine, wrapper string) *Downloader {
	if source != "huggingface" {
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
func (d *Downloader) downloadFasterWhisper(ctx context.Context, model string, onProgress ProgressFn) error {
	cmd := exec.CommandContext(ctx, d.Wrapper, "--download-only", "-m", model)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("wrapper stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("wrapper start: %w", err)
	}
	// scan stderr for progress
	buf := make([]byte, 4096)
	for {
		n, err := stderr.Read(buf)
		if n > 0 {
			s := string(buf[:n])
			if strings.Contains(s, "downloading model") && onProgress != nil {
				onProgress(1) // 开始
			}
			if strings.Contains(s, "progress = 100%") && onProgress != nil {
				onProgress(100)
			}
		}
		if err != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("wrapper download failed: %w", err)
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
