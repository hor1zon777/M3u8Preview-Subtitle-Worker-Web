// Package web — HTTP + WebSocket server。
//
// 端点：
//
//	GET  /api/health
//	GET  /api/version
//	GET  /api/system/info
//	GET  /api/logs
//	GET  /api/settings
//	PUT  /api/settings
//	GET  /api/providers
//	PUT  /api/providers
//	POST /api/providers/{id}/test
//	GET  /api/worker/settings
//	PUT  /api/worker/settings
//	GET  /api/worker/status
//	POST /api/worker/start
//	POST /api/worker/stop
//	POST /api/worker/test
//	GET  /api/models                 模型清单 + 已安装
//	POST /api/models/{name}/download
//	DELETE /api/models/{name}
//	POST /api/models/import          multipart name + file
//	GET  /api/ws/status              WebSocket 推送
//
// 静态前端：未匹配 /api/* 的请求落到 SPA（go:embed dist/*；fallback index.html）。
package web

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/config"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/logger"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/models"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/translate"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/worker"
)

// Server 装配整个 HTTP 服务。
type Server struct {
	store     *config.Store
	engine    *worker.Engine
	state     *worker.State
	translate *translate.Manager
	dl        *models.Downloader
	installer *models.Installer
	authToken string
	httpSrv   *http.Server
}

// New 构造。authToken 为空表示不强制认证。
func New(addr string, authToken string,
	store *config.Store, engine *worker.Engine, state *worker.State,
	tr *translate.Manager) *Server {
	settings := store.GetSettings()
	engStr := "whisper-cli"
	if isScriptFile(settings.WhisperCliPath) {
		engStr = "faster-whisper"
	}
	s := &Server{
		store:     store,
		engine:    engine,
		state:     state,
		translate: tr,
		dl:        models.NewDownloader(settings.ModelsPath, "hf-mirror", engStr, settings.WhisperCliPath),
		installer: &models.Installer{ModelsPath: settings.ModelsPath},
		authToken: authToken,
	}
	s.httpSrv = &http.Server{
		Addr:              addr,
		Handler:           s.router(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// Start 起 HTTP server，阻塞直到 ListenAndServe 返回。
func (s *Server) Start() error {
	logger.Info("[web] listening on %s", s.httpSrv.Addr)
	if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown 优雅停止。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Timeout(10 * time.Minute)) // 模型下载用到长连接
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Route("/api", func(r chi.Router) {
		r.Use(s.authMiddleware())

		r.Get("/health", s.handleHealth)
		r.Get("/version", s.handleVersion)
		r.Get("/system/info", s.handleSystemInfo)
		r.Get("/logs", s.handleLogs)

		r.Get("/settings", s.handleGetSettings)
		r.Put("/settings", s.handlePutSettings)

		r.Get("/providers", s.handleListProviders)
		r.Put("/providers", s.handlePutProviders)
		r.Post("/providers/{id}/test", s.handleTestProvider)

		r.Get("/worker/settings", s.handleGetWorkerSettings)
		r.Put("/worker/settings", s.handlePutWorkerSettings)
		r.Get("/worker/status", s.handleGetWorkerStatus)
		r.Post("/worker/start", s.handleWorkerStart)
		r.Post("/worker/stop", s.handleWorkerStop)
		r.Post("/worker/test", s.handleWorkerTest)

		r.Get("/models", s.handleListModels)
		r.Post("/models/{name}/download", s.handleDownloadModel)
		r.Delete("/models/{name}", s.handleDeleteModel)
		r.Post("/models/import", s.handleImportModel)

		r.Get("/ws/status", s.handleWS) // ws upgrade
	})

	// SPA fallback
	r.NotFound(s.spaHandler())
	r.Get("/*", s.spaHandler())
	return r
}
