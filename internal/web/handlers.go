// Package web — REST handlers。
//
// 与前端 lib/api.ts 调用对齐：响应统一用 {ok, data?, message?} envelope。
// 错误用 200 + ok:false 是为了前端可以一律读 body 不需要分支 status code（与原 IPC 行为一致）。
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/config"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/logger"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/models"
)

// envelope 与前端 ApiResponse 对齐。
//
// Data 字段**不能用 omitempty**：当后端返回空数组 `[]` 时，omitempty 会让 JSON
// 完全省略 data 字段，前端拿到 undefined 再 `.map(...)` 直接 crash。
type envelope[T any] struct {
	OK      bool   `json:"ok"`
	Data    T      `json:"data"`
	Message string `json:"message,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOK[T any](w http.ResponseWriter, v T) {
	writeJSON(w, http.StatusOK, envelope[T]{OK: true, Data: v})
}

func writeFail(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusOK, envelope[any]{OK: false, Message: msg})
}

// --- 通用 ---

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, map[string]any{"status": "ok"})
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, map[string]any{
		"version": "0.1.0",
		"go":      runtime.Version(),
		"os":      runtime.GOOS,
		"arch":    runtime.GOARCH,
	})
}

// SystemInfo 与前端 ISystemInfo 对齐。
type SystemInfo struct {
	ModelsInstalled    []string `json:"modelsInstalled"`
	DownloadingModels  []string `json:"downloadingModels"`
	ModelsPath         string   `json:"modelsPath"`
	TotalMemoryGB      int      `json:"totalMemoryGB,omitempty"`
	WhisperCliPath     string   `json:"whisperCliPath"`
	WhisperCliFound    bool     `json:"whisperCliFound"`
	WhisperEngine      string   `json:"whisperEngine"` // "whisper-cli" / "faster-whisper" / "unknown"
	FFmpegPath         string   `json:"ffmpegPath"`
	FFmpegFound        bool     `json:"ffmpegFound"`
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, _ *http.Request) {
	settings := s.store.GetSettings()
	installer := &models.Installer{ModelsPath: settings.ModelsPath}
	installed, _ := installer.List()
	if installed == nil {
		installed = []string{} // 避免 nil → JSON null → 前端 .map 崩
	}
	// 已下载中：扫描 catalog 看哪些在下载
	downloading := []string{}
	for _, m := range models.AllModels() {
		if models.IsDownloading(m.Name) {
			downloading = append(downloading, m.Name)
		}
	}
	engine := "unknown"
	cliFound := isExecutable(settings.WhisperCliPath)
	if cliFound {
		if isScriptFile(settings.WhisperCliPath) {
			engine = "faster-whisper"
		} else {
			engine = "whisper-cli"
		}
	}
	info := SystemInfo{
		ModelsInstalled:   installed,
		DownloadingModels: downloading,
		ModelsPath:        settings.ModelsPath,
		WhisperCliPath:    settings.WhisperCliPath,
		WhisperCliFound:   cliFound,
		WhisperEngine:     engine,
		FFmpegPath:        settings.FFmpegPath,
		FFmpegFound:       isExecutable(settings.FFmpegPath),
	}
	writeOK(w, info)
}

func (s *Server) handleLogs(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, logger.Snapshot())
}

// --- settings ---

func (s *Server) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, s.store.GetSettings())
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var in config.SystemSettings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeFail(w, "decode: "+err.Error())
		return
	}
	if err := s.store.Update(func(c *config.Config) {
		c.Settings = in
		// 不允许置空路径
		def := config.DefaultSystemSettings("")
		if c.Settings.WhisperCliPath == "" {
			c.Settings.WhisperCliPath = def.WhisperCliPath
		}
		if c.Settings.FFmpegPath == "" {
			c.Settings.FFmpegPath = def.FFmpegPath
		}
	}); err != nil {
		writeFail(w, err.Error())
		return
	}
	writeOK(w, s.store.GetSettings())
}

// --- providers ---

func (s *Server) handleListProviders(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, s.store.GetProviders())
}

func (s *Server) handlePutProviders(w http.ResponseWriter, r *http.Request) {
	var in []config.Provider
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeFail(w, "decode: "+err.Error())
		return
	}
	if err := s.store.Update(func(c *config.Config) {
		c.TranslationProviders = in
	}); err != nil {
		writeFail(w, err.Error())
		return
	}
	writeOK(w, s.store.GetProviders())
}

// handleTestProvider 用 provider 跑 "Hello China" → 目标语言 一次。
func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		SourceLanguage string `json:"sourceLanguage"`
		TargetLanguage string `json:"targetLanguage"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.SourceLanguage == "" {
		body.SourceLanguage = "en"
	}
	if body.TargetLanguage == "" {
		body.TargetLanguage = "zh"
	}
	// 借用 manager 的查找 + registry：调一条假 SRT
	tmpPath, cleanup, err := writeTempSrt("1\n00:00:01,000 --> 00:00:04,000\nHello China\n")
	if err != nil {
		writeFail(w, err.Error())
		return
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out, err := s.translate.Run(ctx, tmpPath, id, body.SourceLanguage, body.TargetLanguage, nil)
	if err != nil {
		writeFail(w, err.Error())
		return
	}
	// 读译文 SRT 取第一条
	raw, err := os.ReadFile(out)
	if err != nil {
		writeFail(w, err.Error())
		return
	}
	first := firstSrtContent(string(raw))
	writeOK(w, map[string]any{"translation": first})
}

// --- worker ---

func (s *Server) handleGetWorkerSettings(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, s.store.GetWorker())
}

func (s *Server) handlePutWorkerSettings(w http.ResponseWriter, r *http.Request) {
	var in config.WorkerSettings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeFail(w, "decode: "+err.Error())
		return
	}
	if err := s.store.Update(func(c *config.Config) {
		// 不允许置空 workerId / workerName（防误改）
		if in.WorkerID == "" {
			in.WorkerID = c.Worker.WorkerID
		}
		if in.WorkerName == "" {
			in.WorkerName = c.Worker.WorkerName
		}
		c.Worker = in
	}); err != nil {
		writeFail(w, err.Error())
		return
	}
	// 热更新
	if err := s.engine.SettingsChanged(r.Context()); err != nil {
		// 这里失败可以容忍：让用户看到 message
		writeJSON(w, http.StatusOK, envelope[config.WorkerSettings]{
			OK: true, Data: s.store.GetWorker(),
			Message: "saved, but engine hot-reload failed: " + err.Error(),
		})
		return
	}
	writeOK(w, s.store.GetWorker())
}

func (s *Server) handleGetWorkerStatus(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, s.engine.Status())
}

func (s *Server) handleWorkerStart(w http.ResponseWriter, r *http.Request) {
	// 先把 enabled 置 true 再 start，便于刷新失败时回滚
	if err := s.store.Update(func(c *config.Config) {
		c.Worker.Enabled = true
	}); err != nil {
		writeFail(w, err.Error())
		return
	}
	if err := s.engine.Start(r.Context()); err != nil {
		// 回滚 enabled
		_ = s.store.Update(func(c *config.Config) {
			c.Worker.Enabled = false
		})
		writeFail(w, err.Error())
		return
	}
	writeOK(w, map[string]any{"started": true})
}

func (s *Server) handleWorkerStop(w http.ResponseWriter, r *http.Request) {
	s.engine.Stop(r.Context())
	_ = s.store.Update(func(c *config.Config) {
		c.Worker.Enabled = false
	})
	writeOK(w, map[string]any{"stopped": true})
}

func (s *Server) handleWorkerTest(w http.ResponseWriter, r *http.Request) {
	ok, msg := s.engine.TestConnection(r.Context())
	writeOK(w, map[string]any{"ok": ok, "message": msg})
}

// --- models ---

func (s *Server) handleListModels(w http.ResponseWriter, _ *http.Request) {
	settings := s.store.GetSettings()
	inst := &models.Installer{ModelsPath: settings.ModelsPath}
	installed, _ := inst.List()
	if installed == nil {
		installed = []string{}
	}
	writeOK(w, map[string]any{
		"catalog":    models.Catalog,
		"installed":  installed,
		"modelsPath": settings.ModelsPath,
	})
}

func (s *Server) handleDownloadModel(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if models.FindModel(name) == nil {
		writeFail(w, "unknown model: "+name)
		return
	}
	if models.IsDownloading(name) {
		writeFail(w, "已在下载中")
		return
	}
	settings := s.store.GetSettings()
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "hf-mirror"
	}
	engine := "whisper-cli"
	wrapper := settings.WhisperCliPath
	if isScriptFile(settings.WhisperCliPath) {
		engine = "faster-whisper"
	}
	dl := models.NewDownloader(settings.ModelsPath, source, engine, wrapper)
	// 异步下载，通过 WS 推送进度
	go func() {
		bg, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := dl.Download(bg, name, func(pct int) {
			s.broadcastModelProgress(name, pct, "")
		})
		if err != nil {
			logger.Error("[web] download %s failed: %v", name, err)
			s.broadcastModelProgress(name, 0, err.Error())
			return
		}
		s.broadcastModelProgress(name, 100, "")
	}()
	writeOK(w, map[string]any{"started": true, "name": name})
}

func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	settings := s.store.GetSettings()
	inst := &models.Installer{ModelsPath: settings.ModelsPath}
	if err := inst.Delete(name); err != nil {
		writeFail(w, err.Error())
		return
	}
	writeOK(w, map[string]any{"deleted": name})
}

func (s *Server) handleImportModel(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeFail(w, "parse multipart: "+err.Error())
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		writeFail(w, "缺少 name 字段")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeFail(w, "缺少 file 字段")
		return
	}
	defer file.Close()
	settings := s.store.GetSettings()
	inst := &models.Installer{ModelsPath: settings.ModelsPath}
	if err := inst.Import(name, file); err != nil {
		writeFail(w, err.Error())
		return
	}
	writeOK(w, map[string]any{"imported": name})
}

// --- helpers ---

func isExecutable(path string) bool {
	if path == "" {
		return false
	}
	if strings.ContainsAny(path, "/\\") {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return true
		}
		return false
	}
	// 没有路径分隔符 → 默认认为在 $PATH（运行时跑起来会真验证）
	return true
}

// isScriptFile 检测文件是否以 #! 开头（Python/shell wrapper）。
// 用于区分 whisper-cli 原生二进制 和 faster-whisper Python wrapper。
func isScriptFile(path string) bool {
	if !strings.ContainsAny(path, "/\\") {
		p, err := exec.LookPath(path)
		if err != nil {
			return false
		}
		path = p
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var head [2]byte
	n, _ := f.Read(head[:])
	return n == 2 && head[0] == '#' && head[1] == '!'
}

// writeTempSrt 写一份临时 SRT 用于翻译测试。返回路径 + cleanup。
func writeTempSrt(content string) (string, func(), error) {
	dir := filepath.Join(os.TempDir(), "m3u8-subtitle-worker-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", func() {}, err
	}
	path := filepath.Join(dir, "test-"+uuid.NewString()+".srt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", func() {}, err
	}
	return path, func() { _ = os.RemoveAll(filepath.Dir(path)) }, nil
}

// 占位包级别引用避免无 io / fmt 误删
var _ = io.EOF
var _ = fmt.Sprintf

// firstSrtContent 抽出 SRT 文件第一段的 content 行（去除编号 / 时间戳）。
func firstSrtContent(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, l := range lines {
		if strings.Contains(l, "-->") && i+1 < len(lines) {
			// 从 i+1 开始找首段
			var seg []string
			for j := i + 1; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == "" {
					break
				}
				seg = append(seg, lines[j])
			}
			return strings.Join(seg, "\n")
		}
	}
	return ""
}
