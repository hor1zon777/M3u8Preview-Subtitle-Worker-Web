// Package asr — whisper-py-wrapper 常驻服务模式客户端 + 进程管理。
//
// 与 wrapper.py --serve 配合使用：
//
//   - Server.Ensure(modelID, noGpu) 拉起 wrapper 子进程，加载模型一次；
//     如已运行且配置一致 → no-op；如配置变了 → 重启
//   - Server.Transcribe(opts) 通过 Unix Domain Socket 提交 ASR 请求，
//     流式接收 progress / log / done / error 事件
//   - Server.Stop() 优雅退出
//
// 协议（newline-delimited JSON）见 wrapper.py 顶部注释。
package asr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/logger"
)

// TranscribeRequest 服务端 ASR 请求参数（JSON 编码后通过 socket 提交）。
type TranscribeRequest struct {
	ID         string         `json:"id"`
	Wav        string         `json:"wav"`
	Of         string         `json:"of"` // .srt basename
	Lang       string         `json:"lang"`
	Prompt     string         `json:"prompt,omitempty"`
	MaxContext int            `json:"max_context,omitempty"`
	Vad        bool           `json:"vad,omitempty"`
	VadParams  map[string]any `json:"vad_params,omitempty"`
}

// TranscribeResponse 成功完成时的 done 事件。
type TranscribeResponse struct {
	SrtPath  string  `json:"srt_path"`
	Segments int     `json:"segments"`
	Language string  `json:"language"`
	Duration float64 `json:"duration"`
}

// Server 管理 wrapper 服务子进程。包级单例 GlobalServer。
type Server struct {
	mu         sync.Mutex
	cmd        *exec.Cmd
	cliPath    string
	socketPath string
	modelID    string
	noGpu      bool
	ready      atomic.Bool
}

// GlobalServer 包级单例。lifecycle 由 worker.Engine 触发。
var GlobalServer = &Server{}

// resolveSocketPath 选择临时目录里的 socket 路径。
func resolveSocketPath() string {
	dir := os.TempDir()
	return filepath.Join(dir, "mws-whisper.sock")
}

// Ensure 启动或确认 wrapper 服务进程在跑且使用指定的模型。
// 如当前未启动 → 启动；如当前 model/noGpu 与请求一致 → no-op；不一致 → 重启。
func (s *Server) Ensure(ctx context.Context, cliPath, modelID string, noGpu bool) error {
	if runtime.GOOS == "windows" {
		// Windows 下 unix socket 支持有限，wrapper Server 仅在 Linux/macOS 启用；
		// 调用方在 Ensure 失败时应 fallback 到 fork-per-job 模式。
		return errors.New("server mode disabled on windows")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cmd != nil && s.modelID == modelID && s.noGpu == noGpu && s.ready.Load() {
		return nil
	}
	if s.cmd != nil {
		s.stopLocked()
	}
	s.cliPath = cliPath
	s.socketPath = resolveSocketPath()
	return s.startLocked(ctx, modelID, noGpu)
}

func (s *Server) startLocked(ctx context.Context, modelID string, noGpu bool) error {
	args := []string{"--serve", "--socket", s.socketPath, "-m", modelID}
	if noGpu {
		args = append(args, "-ng")
	}
	logger.Info("[asr:server] starting wrapper service: %s %v", s.cliPath, args)
	cmd := exec.Command(s.cliPath, args...)
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start wrapper service: %w (path=%s)", err, s.cliPath)
	}

	// stderr 持续转发到 logger.Debug（用 [asr:server] 前缀）
	go forwardLog(stderr, "[asr:server]")

	// 等待 stdout 上的 READY 行（model loading 可能比较慢，给 5 分钟兜底）
	readyCh := make(chan error, 1)
	rdr := bufio.NewReader(stdout)
	go func() {
		for {
			line, err := rdr.ReadString('\n')
			if err != nil {
				readyCh <- fmt.Errorf("read READY: %w", err)
				return
			}
			line = trimEOL(line)
			if line == "READY" {
				readyCh <- nil
				// 之后 stdout 上不会再有内容；继续 drain 防止子进程 stdout 阻塞
				go drainReader(rdr)
				return
			}
			// 其他行也输出到 logger，便于发现异常
			logger.Debug("[asr:server:stdout] %s", line)
		}
	}()

	select {
	case err := <-readyCh:
		if err != nil {
			_ = cmd.Process.Kill()
			return fmt.Errorf("wrapper service did not become ready: %w", err)
		}
	case <-time.After(5 * time.Minute):
		_ = cmd.Process.Kill()
		return errors.New("wrapper service did not output READY within 5 min")
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return ctx.Err()
	}

	s.cmd = cmd
	s.modelID = modelID
	s.noGpu = noGpu
	s.ready.Store(true)

	// 起一个 watcher：进程退出时清理状态，下次 Ensure 会重新拉起
	go func() {
		_ = cmd.Wait()
		s.mu.Lock()
		if s.cmd == cmd {
			s.cmd = nil
			s.ready.Store(false)
			_ = os.Remove(s.socketPath)
			logger.Warn("[asr:server] wrapper service exited; will restart on next ASR job")
		}
		s.mu.Unlock()
	}()

	logger.Info("[asr:server] wrapper service ready (model=%s noGpu=%v socket=%s)",
		modelID, noGpu, s.socketPath)
	return nil
}

// Stop 优雅停止服务。
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

func (s *Server) stopLocked() {
	if s.cmd == nil {
		return
	}
	logger.Info("[asr:server] stopping wrapper service ...")
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_ = s.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = s.cmd.Process.Kill()
			<-done
		}
	}
	_ = os.Remove(s.socketPath)
	s.cmd = nil
	s.ready.Store(false)
	logger.Info("[asr:server] wrapper service stopped")
}

// IsReady 当前是否已就绪可接收 ASR 请求。
func (s *Server) IsReady() bool { return s.ready.Load() }

// Transcribe 通过 socket 提交 ASR 请求；阻塞直到 done / error。
func (s *Server) Transcribe(
	ctx context.Context,
	req TranscribeRequest,
	onProgress ProgressFn,
) (*TranscribeResponse, error) {
	if !s.ready.Load() {
		return nil, errors.New("asr server not ready")
	}
	conn, err := net.DialTimeout("unix", s.socketPath, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial wrapper socket: %w", err)
	}
	defer conn.Close()

	// 把 ctx 的 cancel 绑到 conn 上（直接 close 解锁 reader）
	stopCtxWatch := make(chan struct{})
	defer close(stopCtxWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-stopCtxWatch:
		}
	}()

	// 发请求
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}
	logger.Debug("[asr:server] request submitted: id=%s wav=%s lang=%s vad=%v",
		req.ID, req.Wav, req.Lang, req.Vad)

	rdr := bufio.NewReader(conn)
	for {
		line, err := rdr.ReadString('\n')
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("read event: %w", err)
		}
		line = trimEOL(line)
		if line == "" {
			continue
		}
		var ev struct {
			ID        string  `json:"id"`
			Event     string  `json:"event"`
			Msg       string  `json:"msg"`
			Pct       int     `json:"pct"`
			SrtPath   string  `json:"srt_path"`
			Segments  int     `json:"segments"`
			Language  string  `json:"language"`
			Duration  float64 `json:"duration"`
			Traceback string  `json:"traceback"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			logger.Debug("[asr:server] unparseable event line: %s", line)
			continue
		}
		switch ev.Event {
		case "log":
			logger.Debug("[asr:server:log] %s", ev.Msg)
		case "info":
			logger.Debug("[asr:server:info] %s", line)
		case "progress":
			if onProgress != nil {
				onProgress(ev.Pct)
			}
		case "done":
			return &TranscribeResponse{
				SrtPath:  ev.SrtPath,
				Segments: ev.Segments,
				Language: ev.Language,
				Duration: ev.Duration,
			}, nil
		case "error":
			if ev.Traceback != "" {
				logger.Debug("[asr:server:error:traceback] %s", ev.Traceback)
			}
			return nil, fmt.Errorf("wrapper: %s", ev.Msg)
		default:
			logger.Debug("[asr:server] unknown event: %s", line)
		}
	}
}

// --- helpers ---

func forwardLog(r io.Reader, prefix string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		logger.Debug("%s %s", prefix, line)
	}
}

func drainReader(r *bufio.Reader) {
	buf := make([]byte, 4096)
	for {
		if _, err := r.Read(buf); err != nil {
			return
		}
	}
}

func trimEOL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
