// cmd/worker/main.go — 进程入口。
//
// 责任：
//   - 解析 env / flag
//   - 加载 config
//   - 启动 worker engine + web server
//   - SIGTERM/SIGINT 优雅停止
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/config"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/logger"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/translate"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/web"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/worker"
)

var (
	flagAddr     = flag.String("addr", envOr("MWS_ADDR", ":8089"), "HTTP 监听地址（默认 :8089）")
	flagWorkRoot = flag.String("work-root", envOr("MWS_WORK_ROOT", ""), "worker 临时工作目录（默认 $TMPDIR/m3u8-subtitle-worker）")
)

func main() {
	flag.Parse()

	cfgDir, dataDir := config.ResolveDirs()
	logger.Info("config dir: %s", cfgDir)
	logger.Info("data dir:   %s", dataDir)

	store, err := config.New(cfgDir, dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	cfg := store.Get()
	logger.SetDebug(cfg.Settings.Debug)
	logger.Info("workerId=%s workerName=%q", cfg.Worker.WorkerID, cfg.Worker.WorkerName)
	logger.Info("modelsPath=%s whisperCli=%s ffmpeg=%s",
		cfg.Settings.ModelsPath, cfg.Settings.WhisperCliPath, cfg.Settings.FFmpegPath)
	logger.Debug("system settings: useCuda=%v useVAD=%v vadThreshold=%.2f minSpeech=%dms minSilence=%dms maxSpeech=%dms speechPad=%dms samplesOverlap=%.2f",
		cfg.Settings.UseCuda, cfg.Settings.UseVAD, cfg.Settings.VadThreshold,
		cfg.Settings.VadMinSpeechDuration, cfg.Settings.VadMinSilenceDuration,
		cfg.Settings.VadMaxSpeechDuration, cfg.Settings.VadSpeechPad, cfg.Settings.VadSamplesOverlap)
	logger.Debug("worker settings: pollInterval=%ds heartbeat=%ds errorBackoff=%ds verifyTls=%v whisperModel=%s sourceLang=%s targetLang=%s translateProvider=%q localMaxConcurrent=%d",
		cfg.Worker.PollIntervalSec, cfg.Worker.HeartbeatIntervalSec, cfg.Worker.ErrorBackoffSec,
		cfg.Worker.VerifyTLS, cfg.Worker.WhisperModel, cfg.Worker.SourceLanguage, cfg.Worker.TargetLanguage,
		cfg.Worker.TranslateProviderID, cfg.Worker.LocalMaxConcurrentTasks)

	workRoot := *flagWorkRoot
	if workRoot == "" {
		workRoot = filepath.Join(os.TempDir(), "m3u8-subtitle-worker")
	}

	// 装配
	state := worker.NewState(filepath.Join(dataDir, "history.json"))
	trMgr := translate.NewManager(store)
	// 把 trMgr.Run 适配成 worker.TranslateFunc（参数末位 progress func 类型不同别名）
	translateAdapter := func(ctx context.Context, srtPath, providerID, src, tgt string, onProgress func(percent int)) (string, error) {
		var p translate.ProgressFn
		if onProgress != nil {
			p = translate.ProgressFn(onProgress)
		}
		return trMgr.Run(ctx, srtPath, providerID, src, tgt, p)
	}
	engine := worker.NewEngine(store, state, translateAdapter, workRoot)

	if cfg.Settings.WebToken != "" {
		logger.Info("[web] bearer token auth enabled (configured in settings)")
	} else {
		logger.Info("[web] no token configured — open access; set token in Settings page")
	}
	srv := web.New(*flagAddr, store, engine, state, trMgr)

	// 如果上次保存的 enabled=true，则自动启 worker
	if cfg.Worker.Enabled {
		go func() {
			startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := engine.Start(startCtx); err != nil {
				logger.Error("[worker] auto-start failed: %v", err)
			}
		}()
	} else {
		logger.Info("[worker] not enabled in config, idle on startup")
	}

	// 启 HTTP server（阻塞）
	errCh := make(chan error, 1)
	go func() {
		if err := srv.Start(); err != nil {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		logger.Info("shutdown signal received: %s", sig)
	case err := <-errCh:
		logger.Error("[web] server error: %v", err)
	}

	// 优雅关闭
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	engine.Stop(shutdownCtx)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("[web] shutdown error: %v", err)
	}
	logger.Info("shutdown complete")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
