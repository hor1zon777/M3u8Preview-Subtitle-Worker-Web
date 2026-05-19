// Package worker — 生命周期编排。
//
// 把 broker.Client / state / heartbeat / poller / runner 拼装起来。
// 对外暴露 Engine：Start / Stop / Status / Test。
//
// Engine 是单例：worker 同一时刻只能有一份运行实例。
package worker

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/asr"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/broker"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/config"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/logger"
)

// Engine worker 进程级单例。
type Engine struct {
	mu         sync.Mutex
	store      *config.Store
	state      *State
	translate  TranslateFunc
	workRoot   string

	client    *broker.Client
	heartbeat *Heartbeater
	poller    *Poller

	// 当前注册时使用的字段，用于热更新判断
	activeBaseURL   string
	activeToken     string
	activeVerifyTLS bool
}

// NewEngine 构造。translate 为 nil 表示当前不支持翻译（Phase 2 启动时 = nil；Phase 3 注入）。
func NewEngine(store *config.Store, state *State, translate TranslateFunc, workRoot string) *Engine {
	return &Engine{
		store:     store,
		state:     state,
		translate: translate,
		workRoot:  workRoot,
	}
}

// SetTranslator 后期注入翻译函数。
func (e *Engine) SetTranslator(t TranslateFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.translate = t
}

// Start 注册到服务端并起 poll / heartbeat。已运行则 no-op。
func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client != nil {
		logger.Info("[worker] start: already running, ignored")
		return nil
	}
	ws := e.store.GetWorker()
	if ws.BaseURL == "" || ws.Token == "" {
		return errors.New("请先在 Worker 设置页填 server URL 和 token")
	}

	client, err := broker.New(ws.BaseURL, ws.Token, ws.VerifyTLS)
	if err != nil {
		return err
	}
	// ping 失败不阻塞 register
	if err := client.Ping(ctx); err != nil {
		logger.Warn("[worker] ping failed (将继续尝试注册): %v", err)
	}

	resp, err := client.Register(ctx, broker.RegisterRequest{
		WorkerID:     ws.WorkerID,
		Name:         ws.WorkerName,
		Version:      "0.1.0",
		GPU:          fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH),
		Capabilities: SubtitleCapability,
	})
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}

	e.state.SetStaleThreshold(resp.WorkerStaleThreshold)
	if resp.MaxConcurrentTasks > 0 {
		e.state.SetServerMaxConcurrent(resp.MaxConcurrentTasks)
	} else {
		e.state.SetServerMaxConcurrent(1)
	}
	if ws.LocalMaxConcurrentTasks > 0 {
		e.state.SetLocalMaxConcurrent(ws.LocalMaxConcurrentTasks)
	}
	e.state.SetRegistered(true)
	logger.Info("[worker:poll] registered as %s (stale=%ds, maxConcurrent=%d, accepted=%v)",
		ws.WorkerID, resp.WorkerStaleThreshold, e.state.Snapshot().MaxConcurrentTasks, resp.AcceptedCapabilities)

	e.client = client
	e.activeBaseURL = ws.BaseURL
	e.activeToken = ws.Token
	e.activeVerifyTLS = ws.VerifyTLS

	// 心跳
	staleSec := resp.WorkerStaleThreshold
	if staleSec < 15 {
		staleSec = 60
	}
	e.heartbeat = NewHeartbeater(e.state, client, ws.WorkerID, ws.HeartbeatIntervalSec, staleSec)
	e.heartbeat.Start()

	// runner + poller
	runner := NewRunner(RunnerDeps{
		State:       e.state,
		Client:      client,
		WorkerID:    ws.WorkerID,
		GetSettings: e.store.GetSettings,
		GetWorker:   e.store.GetWorker,
		Translate:   e.translate,
		WorkRoot:    e.workRoot,
	})
	e.poller = NewPoller(e.state, client, ws.WorkerID, runner, func() (int, int) {
		w := e.store.GetWorker()
		return w.PollIntervalSec, w.ErrorBackoffSec
	})
	e.poller.Start()
	return nil
}

// Stop 优雅停止。等 in-flight ≤ 30s。
func (e *Engine) Stop(_ context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client == nil {
		// 即使 engine 未 Start 过，也要确保 asr server 关闭
		asr.GlobalServer.Stop()
		return
	}
	if e.poller != nil {
		e.poller.Stop(30_000)
		e.poller = nil
	}
	if e.heartbeat != nil {
		e.heartbeat.Stop()
		e.heartbeat = nil
	}
	e.state.SetRegistered(false)
	e.client = nil
	e.activeBaseURL = ""
	e.activeToken = ""
	// poller 已停 → 没有新 ASR 进来 → 安全关 server 释放 GPU 显存
	asr.GlobalServer.Stop()
	logger.Info("[worker] stopped")
}

// Status 当前运行时状态。
func (e *Engine) Status() RuntimeStatus {
	snap := e.state.Snapshot()
	snap.WorkerID = e.store.GetWorker().WorkerID
	return snap
}

// TestConnection 验证 baseUrl / token 是否能 ping 通。不修改 engine 状态。
func (e *Engine) TestConnection(ctx context.Context) (ok bool, message string) {
	ws := e.store.GetWorker()
	if ws.BaseURL == "" || ws.Token == "" {
		return false, "请先填 server URL 和 token"
	}
	client, err := broker.New(ws.BaseURL, ws.Token, ws.VerifyTLS)
	if err != nil {
		return false, err.Error()
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx); err != nil {
		return false, err.Error()
	}
	return true, "ok"
}

// SettingsChanged 在 settings 改动后由 web handler 调用。
// 当 baseUrl/token/verifyTls 改变 → 重启 worker；只是其它字段改 → 应用 localMax + 不重启。
func (e *Engine) SettingsChanged(ctx context.Context) error {
	e.mu.Lock()
	ws := e.store.GetWorker()
	e.state.SetLocalMaxConcurrent(ws.LocalMaxConcurrentTasks)
	running := e.client != nil
	needRestart := running &&
		(e.activeBaseURL != ws.BaseURL ||
			e.activeToken != ws.Token ||
			e.activeVerifyTLS != ws.VerifyTLS)
	e.mu.Unlock()

	if !running && ws.Enabled {
		return e.Start(ctx)
	}
	if running && !ws.Enabled {
		e.Stop(ctx)
		return nil
	}
	if needRestart {
		logger.Info("[worker] settings changed, restarting...")
		e.Stop(ctx)
		return e.Start(ctx)
	}
	return nil
}
