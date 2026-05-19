// Package worker — 定时心跳。
//
// 每 settings.heartbeatIntervalSec 秒，给所有 currentJobs 上报一次 heartbeat。
// 单次心跳失败不致命；下次再试。410 Gone 则把任务从 currentJobs 移除。
package worker

import (
	"context"
	"time"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/broker"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/logger"
)

// Heartbeater 定时心跳协程。
type Heartbeater struct {
	state    *State
	client   *broker.Client
	workerID string
	interval time.Duration
	stop     chan struct{}
	stopped  chan struct{}
}

// NewHeartbeater 构造。intervalSec 会被自动 clamp 到 [5, staleThreshold/2]。
func NewHeartbeater(state *State, client *broker.Client, workerID string, intervalSec int, staleSec int) *Heartbeater {
	if intervalSec < 5 {
		intervalSec = 5
	}
	if intervalSec > 300 {
		intervalSec = 300
	}
	// 安全上限：不超过 stale 阈值一半
	safe := time.Duration(intervalSec) * time.Second
	half := time.Duration(staleSec) * time.Second / 2
	if half >= 5*time.Second && safe > half {
		safe = half
	}
	return &Heartbeater{
		state:    state,
		client:   client,
		workerID: workerID,
		interval: safe,
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

// Start 起 goroutine 跑心跳循环。
func (h *Heartbeater) Start() {
	go h.loop()
	logger.Info("[worker:hb] heartbeat started (interval=%v)", h.interval)
}

// Stop 停止心跳循环。同步等待。
func (h *Heartbeater) Stop() {
	close(h.stop)
	<-h.stopped
	logger.Info("[worker:hb] heartbeat stopped")
}

func (h *Heartbeater) loop() {
	defer close(h.stopped)
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-ticker.C:
			h.tick()
		}
	}
}

func (h *Heartbeater) tick() {
	snap := h.state.Snapshot()
	logger.Debug("[worker:hb] tick: jobs=%d", len(snap.CurrentJobs))
	for _, j := range snap.CurrentJobs {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := h.client.Heartbeat(ctx, j.JobID, h.workerID, j.Stage, j.Progress)
		cancel()
		if err == nil {
			continue
		}
		if err == broker.ErrJobLost {
			logger.Warn("[worker:hb] job %s lost (410), removing from current", j.JobID)
			h.state.RemoveJob(j.JobID)
		} else {
			logger.Warn("[worker:hb] heartbeat failed for %s: %v", j.JobID, err)
		}
	}
}
