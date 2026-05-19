// Package worker — long-poll claim loop.
//
// 与原 TS poller.ts 行为一致：
//   - long-poll claim（waitSec=25）：服务端 hold 至有任务才返回
//   - 多维并发限流 state.CanClaimNew
//   - claim 失败指数退避 [base, 60s]
//   - Stop() 优雅停止：先 deregister，再等所有 in-flight 完成（或超时）
package worker

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/broker"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/logger"
)

// claimWaitSec long-poll 服务端 hold 时长。0 退回短轮询。
const claimWaitSec = 25

// SubtitleCapability worker 上报给服务端的 capability。subtitle worker 固定。
var SubtitleCapability = []string{"asr_subtitle"}

// Poller poll 循环。
type Poller struct {
	state    *State
	client   *broker.Client
	workerID string
	runner   *Runner
	settings func() (pollIntervalSec, errorBackoffSec int)

	ctx    context.Context
	cancel context.CancelFunc

	inflightWG sync.WaitGroup
	stopped    chan struct{}
}

// NewPoller 构造。
func NewPoller(state *State, client *broker.Client, workerID string, runner *Runner,
	getSettings func() (int, int)) *Poller {
	ctx, cancel := context.WithCancel(context.Background())
	return &Poller{
		state:    state,
		client:   client,
		workerID: workerID,
		runner:   runner,
		settings: getSettings,
		ctx:      ctx,
		cancel:   cancel,
		stopped:  make(chan struct{}),
	}
}

// Start 起 goroutine 跑 poll 循环。
func (p *Poller) Start() {
	p.state.SetPollingActive(true)
	go p.loop()
	logger.Info("[worker:poll] started")
}

// Stop 优雅停止。timeoutMs 内未完成的 in-flight 任务被放弃（服务端 stale recovery 兜底）。
func (p *Poller) Stop(timeoutMs int) {
	p.cancel()

	// 调 deregister 让服务端立刻回滚本 worker 的任务（neutral 路径，attempt 不增）
	dCtx, dCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := p.client.Deregister(dCtx, p.workerID); err != nil {
		logger.Warn("[worker:poll] deregister failed (server will reclaim by stale recovery): %v", err)
	} else {
		logger.Info("[worker:poll] deregistered worker=%s", p.workerID)
	}
	dCancel()

	// 等 in-flight 任务完成或超时
	doneCh := make(chan struct{})
	go func() {
		p.inflightWG.Wait()
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(time.Duration(timeoutMs) * time.Millisecond):
		logger.Warn("[worker:poll] stop timeout after %dms, abandoning", timeoutMs)
	}

	<-p.stopped
	p.state.SetPollingActive(false)
	logger.Info("[worker:poll] stopped")
}

func (p *Poller) loop() {
	defer close(p.stopped)

	pollSec, backoffSec := p.settings()
	baseInterval := time.Duration(max(1, pollSec)) * time.Second
	backoff := time.Duration(max(1, backoffSec)) * time.Second
	maxBackoff := 60 * time.Second
	logger.Debug("[worker:poll] loop start (pollInterval=%s baseBackoff=%s maxBackoff=%s waitSec=%d)",
		baseInterval, backoff, maxBackoff, claimWaitSec)

	// 限流：concurrency saturated 状态每 30s 才输出一次，避免淹没日志
	var lastSatLog time.Time

	for {
		if p.ctx.Err() != nil {
			return
		}
		if !p.state.CanClaimNew() {
			if time.Since(lastSatLog) > 30*time.Second {
				logger.Debug("[worker:poll] cannot claim new (concurrency saturated), polling paused")
				lastSatLog = time.Now()
			}
			if sleepCtx(p.ctx, time.Second) != nil {
				return
			}
			continue
		}
		lastSatLog = time.Time{}

		// long-poll claim
		logger.Debug("[worker:poll] claim start (waitSec=%d)", claimWaitSec)
		claimStart := time.Now()
		claimCtx, claimCancel := context.WithCancel(p.ctx)
		job, err := p.client.Claim(claimCtx, p.workerID, claimWaitSec)
		claimCancel()
		logger.Debug("[worker:poll] claim returned (took=%s, err=%v, job=%v)",
			time.Since(claimStart), err, job != nil)
		if err != nil {
			if p.ctx.Err() != nil {
				return
			}
			logger.Warn("[worker:poll] claim error: %v", err)
			logger.Debug("[worker:poll] backing off for %s", backoff)
			if sleepCtx(p.ctx, backoff) != nil {
				return
			}
			backoff = time.Duration(math.Min(float64(maxBackoff), float64(backoff)*1.7))
			continue
		}
		backoff = time.Duration(max(1, backoffSec)) * time.Second

		if job == nil {
			logger.Debug("[worker:poll] no job; sleeping baseInterval=%s", baseInterval)
			if sleepCtx(p.ctx, baseInterval) != nil {
				return
			}
			continue
		}

		logger.Info("[worker:poll] claimed job %s (media=%s, stage=%s%s)",
			job.JobID, job.MediaID, job.Stage,
			func() string {
				if job.Attempt > 0 && job.MaxAttempts > 0 {
					return ", attempt=" + itoa(job.Attempt) + "/" + itoa(job.MaxAttempts)
				}
				return ""
			}())
		logger.Debug("[worker:poll] job detail: audioUrl=%s size=%d sha=%s… sourceLang=%s targetLang=%s",
			job.AudioArtifactURL, job.AudioArtifactSize, truncStr(job.AudioArtifactSha256, 12),
			job.SourceLang, job.TargetLang)

		// fire-and-forget runJob
		p.inflightWG.Add(1)
		go func(j *broker.ClaimedJob) {
			defer p.inflightWG.Done()
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("[worker:poll] runJob %s panic: %v", j.JobID, rec)
				}
			}()
			if err := p.runner.Run(p.ctx, j); err != nil {
				logger.Warn("[worker:poll] runJob %s ended with error: %v", j.JobID, err)
			}
		}(job)

		// 短喘息防止 burst
		if sleepCtx(p.ctx, 200*time.Millisecond) != nil {
			return
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func itoa(n int) string {
	// 小型内联 strconv.Itoa，避免无谓 import
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func truncStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
