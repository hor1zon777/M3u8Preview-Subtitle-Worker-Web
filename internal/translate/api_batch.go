// Package translate — API 模式批量翻译。
//
// 与原 TS api.ts 行为一致：
//   - batchSize 个字幕 → 每条 content 用 \n 拼起来 → translator([]string) 返回 []string
//   - 返回数量与原文不符 → 报错
//   - 并发 + 节流
package translate

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// HandleAPIBatch 跑完所有 API 批次。
func HandleAPIBatch(
	ctx context.Context,
	subs []Subtitle,
	p Provider,
	src, tgt string,
	translator TranslatorFunc,
	batchSize int,
	onProgress ProgressFn,
	onBatchResult func([]TranslationResult) error,
	maxRetries int,
) ([]TranslationResult, error) {
	if batchSize <= 0 {
		batchSize = DefaultBatchSize.API
	}
	if p.Concurrency <= 0 {
		p.Concurrency = 1
	}
	totalBatches := (len(subs) + batchSize - 1) / batchSize
	if totalBatches == 0 {
		return nil, nil
	}

	var (
		mu      sync.Mutex
		results = make([]TranslationResult, 0, len(subs))
		done    int
		progress = func() {
			if onProgress == nil {
				return
			}
			pct := done * 100 / max(1, len(subs))
			if pct > 100 {
				pct = 100
			}
			onProgress(pct)
		}
	)
	if onProgress != nil {
		onProgress(0)
	}
	intervalMs := int(p.RequestInterval * 1000)

	RunBatchesConcurrent(ctx, p.Concurrency, intervalMs, totalBatches, func(ctx context.Context, idx int) error {
		start := idx * batchSize
		end := start + batchSize
		if end > len(subs) {
			end = len(subs)
		}
		batch := subs[start:end]

		texts := make([]string, len(batch))
		for i, s := range batch {
			texts[i] = strings.Join(s.Content, "\n")
		}

		var (
			translated []string
			lastErr    error
		)
		for attempt := 0; attempt <= maxRetries; attempt++ {
			t, err := translator(ctx, texts, p, src, tgt)
			if err != nil {
				lastErr = err
				if isConfigurationError(err) {
					return placeholderResults(batch, "[翻译失败: 配置错误]", &mu, &results, &done, progress, onBatchResult)
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
				continue
			}
			translated = t
			lastErr = nil
			break
		}
		if lastErr != nil {
			return placeholderResults(batch, "[翻译失败: "+errString(lastErr)+"]", &mu, &results, &done, progress, onBatchResult)
		}
		if len(translated) != len(batch) {
			return fmt.Errorf("Translation result count mismatch: source=%d, translated=%d", len(batch), len(translated))
		}
		br := make([]TranslationResult, 0, len(batch))
		for i, s := range batch {
			br = append(br, TranslationResult{
				ID:            s.ID,
				StartEndTime:  s.StartEndTime,
				SourceContent: texts[i],
				TargetContent: translated[i],
			})
		}
		mu.Lock()
		results = append(results, br...)
		done += len(batch)
		mu.Unlock()
		progress()
		if onBatchResult != nil {
			return onBatchResult(br)
		}
		return nil
	})

	if onProgress != nil {
		onProgress(100)
	}
	return results, nil
}
