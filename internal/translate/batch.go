// Package translate — work-stealing 批次并发执行器。
//
// 与原 TS runBatchesConcurrent 行为一致：
//   - concurrency 个 worker，共享一个 cursor 抢批次（work-stealing）
//   - 每批 submit 间隔 startIntervalMs（第一批免）—— 节流外部 API
//   - 批次失败 → 上层 retry；这里只 log 不重试
package translate

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/logger"
)

// RunBatchesConcurrent 并发跑 totalBatches 个批次。
//
//	concurrency: worker 数；< 1 视为 1，>32 视为 32
//	startIntervalMs: 各 worker 之间相邻 submit 的最小间隔（节流）
//	runBatch(ctx, idx) 实际批次处理函数，return error 仅日志，不 abort 全局
//	totalBatches: 批次总数
func RunBatchesConcurrent(
	ctx context.Context,
	concurrency int,
	startIntervalMs int,
	totalBatches int,
	runBatch func(ctx context.Context, idx int) error,
) {
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 32 {
		concurrency = 32
	}
	if totalBatches <= 0 {
		return
	}
	var cursor int64 = 0
	var lastSubmitAt atomic.Int64
	var mu sync.Mutex // 串行 throttle 计算
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				idx := atomic.AddInt64(&cursor, 1) - 1
				if int(idx) >= totalBatches {
					return
				}
				if startIntervalMs > 0 {
					mu.Lock()
					now := time.Now().UnixMilli()
					last := lastSubmitAt.Load()
					wait := int64(startIntervalMs) - (now - last)
					if last > 0 && wait > 0 {
						mu.Unlock()
						select {
						case <-ctx.Done():
							return
						case <-time.After(time.Duration(wait) * time.Millisecond):
						}
					} else {
						mu.Unlock()
					}
					lastSubmitAt.Store(time.Now().UnixMilli())
				}
				if err := runBatch(ctx, int(idx)); err != nil {
					logger.Warn("[translate:batch] batch %d failed: %v", idx, err)
				}
			}
		}()
	}
	wg.Wait()
}
