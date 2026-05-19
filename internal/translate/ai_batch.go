// Package translate — AI 批量翻译路径。
//
// 与原 TS ai.ts 行为一致：
//   - batchSize 个字幕 → 拼成 JSON 对象（id→content） → renderTemplate(prompt) → translator
//   - 响应 strip <think>...</think> 然后从 ```json``` 块提取
//   - 三重 JSON 回退：strict → 宽松修复 → 错误
//   - 按 subtitle.id 映射到 TranslationResult
//
// 并发：runBatchesConcurrent（provider.Concurrency / RequestInterval 节流）。
package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/logger"
)

// HandleAIBatch 跑完所有 AI 批次。
//
//   - subs: 全部字幕
//   - p: provider 配置
//   - src/tgt: 已映射的语言码
//   - translator: provider 对应的 TranslatorFunc
//   - batchSize: <=0 时回退到 DefaultBatchSize.AI
//   - onProgress: 0-100 进度回调
//   - onBatchResult: 每批次结果回调（用于落盘）
//   - maxRetries: 单批失败重试次数
func HandleAIBatch(
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
		batchSize = DefaultBatchSize.AI
	}
	if p.Concurrency <= 0 {
		p.Concurrency = 1
	}
	totalBatches := (len(subs) + batchSize - 1) / batchSize
	if totalBatches == 0 {
		return nil, nil
	}

	var (
		mu       sync.Mutex
		results  = make([]TranslationResult, 0, len(subs))
		done     int
		// 统计计数器：success = 真正得到译文；placeholder = 整批失败被占位；empty = 译文为空字符串
		successCount     int
		placeholderCount int
		emptyCount       int
		progress         = func() {
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
	logger.Debug("[translate:ai] total=%d batches=%d batchSize=%d concurrency=%d intervalMs=%d retries=%d",
		len(subs), totalBatches, batchSize, p.Concurrency, intervalMs, maxRetries)

	RunBatchesConcurrent(ctx, p.Concurrency, intervalMs, totalBatches, func(ctx context.Context, idx int) error {
		start := idx * batchSize
		end := start + batchSize
		if end > len(subs) {
			end = len(subs)
		}
		batch := subs[start:end]

		// 拼成 JSON: { "1": "...", "2": "..." }
		idMap := make(map[string]string, len(batch))
		for _, s := range batch {
			idMap[s.ID] = strings.Join(s.Content, "\n")
		}
		jsonContent, _ := json.MarshalIndent(idMap, "", "  ")

		// 系统 prompt 渲染
		userPrompt := renderTemplateString(firstNonEmpty(p.Prompt, DefaultUserPrompt), map[string]string{
			"sourceLanguage": src,
			"targetLanguage": tgt,
			"content":        string(jsonContent),
		})
		_ = userPrompt // translator 自行决定如何使用；下面把 userPrompt 作为唯一文本传入

		logger.Debug("[translate:ai] batch %d/%d start: rows=%d userPromptLen=%d",
			idx+1, totalBatches, len(batch), len(userPrompt))

		var lastErr error
		for attempt := 0; attempt <= maxRetries; attempt++ {
			texts, err := translator(ctx, []string{userPrompt}, p, src, tgt)
			if err != nil {
				lastErr = err
				logger.Debug("[translate:ai] batch %d attempt %d translator error: %v", idx+1, attempt, err)
				if isConfigurationError(err) {
					mu.Lock()
					placeholderCount += len(batch)
					mu.Unlock()
					return placeholderResults(batch, "[翻译失败: 配置错误]", &mu, &results, &done, progress, onBatchResult)
				}
				// 指数（线性）退避：1s * attempt
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
				continue
			}
			if len(texts) == 0 {
				lastErr = ErrEmptyTranslation
				logger.Debug("[translate:ai] batch %d attempt %d: empty translator response", idx+1, attempt)
				continue
			}
			raw := texts[0]
			logger.Debug("[translate:ai] batch %d attempt %d rawLen=%d", idx+1, attempt, len(raw))
			parsed, perr := parseAIBatchResponse(raw)
			if perr != nil {
				lastErr = fmt.Errorf("parse ai response: %w; raw=%s", perr, truncate(raw, 500))
				logger.Warn("[translate:ai] batch %d/%d parse failed (attempt %d): %v",
					idx+1, totalBatches, attempt, perr)
				continue
			}
			batchResults := mapAIResultsToSubtitles(batch, parsed)
			// 统计本批成功数（target 非空 = 真翻译；空 = 模型漏了某些 id）
			batchSuccess, batchEmpty := 0, 0
			for _, r := range batchResults {
				if strings.TrimSpace(r.TargetContent) == "" {
					batchEmpty++
				} else {
					batchSuccess++
				}
			}
			mu.Lock()
			results = append(results, batchResults...)
			done += len(batch)
			successCount += batchSuccess
			emptyCount += batchEmpty
			mu.Unlock()
			logger.Debug("[translate:ai] batch %d/%d done: parsed=%d success=%d empty=%d",
				idx+1, totalBatches, len(parsed), batchSuccess, batchEmpty)
			progress()
			if onBatchResult != nil {
				if err := onBatchResult(batchResults); err != nil {
					return err
				}
			}
			return nil
		}
		// 重试用完仍失败：占位
		logger.Warn("[translate:ai] batch %d/%d exhausted retries → placeholder filled (%d rows)",
			idx+1, totalBatches, len(batch))
		mu.Lock()
		placeholderCount += len(batch)
		mu.Unlock()
		return placeholderResults(batch, "[翻译失败: "+errString(lastErr)+"]", &mu, &results, &done, progress, onBatchResult)
	})

	// 按 subtitle id 顺序排序结果
	sort.SliceStable(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	if onProgress != nil {
		onProgress(100)
	}
	logger.Info("[translate:ai] summary: total=%d success=%d empty=%d placeholder=%d (provider=%s model=%s)",
		len(subs), successCount, emptyCount, placeholderCount, p.Name, p.ModelName)
	return results, nil
}

// parseAIBatchResponse 三重 JSON 回退。返回 id→target 映射。
func parseAIBatchResponse(raw string) (map[string]string, error) {
	// 1. strip <think>...</think>
	cleaned := ThinkTagRegex.ReplaceAllString(raw, "")
	// 2. 优先抽 ```json ... ``` 块
	if m := JSONContentRegex.FindStringSubmatch(cleaned); len(m) == 2 {
		cleaned = m[1]
	}
	// 3. 尝试 strict
	cleaned = strings.TrimSpace(cleaned)
	var out map[string]string
	if err := json.Unmarshal([]byte(cleaned), &out); err == nil {
		return out, nil
	}
	// 4. 尝试 relaxed：移除尾随逗号 + 智能截取 JSON 区段
	relaxed := relaxJSON(cleaned)
	if err := json.Unmarshal([]byte(relaxed), &out); err == nil {
		return out, nil
	}
	// 5. 失败
	return nil, fmt.Errorf("无法解析 JSON（前 200 字符）：%s", truncate(cleaned, 200))
}

// relaxJSON 简易 JSON 修复：
//   - 截取第一个 { 到最后一个 } 之间内容
//   - 移除尾随逗号 ,}  / ,]
func relaxJSON(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		s = s[start : end+1]
	}
	// 移除尾随逗号
	s = strings.ReplaceAll(s, ",}", "}")
	s = strings.ReplaceAll(s, ",]", "]")
	// 处理换行后多余的逗号
	s = strings.ReplaceAll(s, ",\n}", "\n}")
	s = strings.ReplaceAll(s, ",\n]", "\n]")
	return s
}

// mapAIResultsToSubtitles 按 id 优先、idx 兜底，把 AI 返回映射回 Subtitle。
func mapAIResultsToSubtitles(batch []Subtitle, parsed map[string]string) []TranslationResult {
	out := make([]TranslationResult, 0, len(batch))
	keys := make([]string, 0, len(parsed))
	for k := range parsed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, s := range batch {
		target := ""
		if v, ok := parsed[s.ID]; ok {
			target = v
		} else if i < len(keys) {
			target = parsed[keys[i]]
		}
		out = append(out, TranslationResult{
			ID:            s.ID,
			StartEndTime:  s.StartEndTime,
			SourceContent: strings.Join(s.Content, "\n"),
			TargetContent: target,
		})
	}
	return out
}

// placeholderResults 用占位填充失败批次，调用 onBatchResult 后返回 nil（保持流程不 abort）。
func placeholderResults(
	batch []Subtitle,
	placeholder string,
	mu *sync.Mutex,
	results *[]TranslationResult,
	done *int,
	progress func(),
	onBatchResult func([]TranslationResult) error,
) error {
	br := make([]TranslationResult, 0, len(batch))
	for _, s := range batch {
		br = append(br, TranslationResult{
			ID:            s.ID,
			StartEndTime:  s.StartEndTime,
			SourceContent: strings.Join(s.Content, "\n"),
			TargetContent: placeholder,
		})
	}
	mu.Lock()
	*results = append(*results, br...)
	*done += len(batch)
	mu.Unlock()
	progress()
	if onBatchResult != nil {
		return onBatchResult(br)
	}
	return nil
}

// --- 内部辅助 ---

func renderTemplateString(tmpl string, vars map[string]string) string {
	out := tmpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "${"+k+"}", v)
	}
	return out
}

func isConfigurationError(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "api key") ||
		strings.Contains(low, "apikey") ||
		strings.Contains(low, "missing key") ||
		strings.Contains(low, "invalid key") ||
		strings.Contains(low, "401") ||
		strings.Contains(low, "403") ||
		strings.Contains(low, "unauthorized") ||
		strings.Contains(low, "forbidden")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
