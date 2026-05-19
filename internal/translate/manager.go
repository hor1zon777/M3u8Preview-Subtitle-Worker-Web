// Package translate — 主入口：从 SRT 翻译到 SRT。
//
// 与原 TS translate(...) 函数对齐：
//   - 解析源 SRT
//   - 按 provider.isAi 走 AI 或 API 路径
//   - 用 contentTemplate 渲染输出 SRT
//   - tempTranslatedPath 写"纯译文"SRT（无源文，校对编辑器用；本工作版可省略）
package translate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/asr"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/config"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/logger"
)

// Manager 持有 store 引用，把 provider id 解析为 TranslateFunc 调用。
type Manager struct {
	store    *config.Store
	registry map[string]TranslatorFunc
}

// NewManager 构造。registry 在初始化时通过 RegisterTranslator 注册。
func NewManager(store *config.Store) *Manager {
	return &Manager{
		store:    store,
		registry: defaultRegistry(),
	}
}

// Register 添加/覆盖一个 provider type 的 translator 实现。
func (m *Manager) Register(providerType string, fn TranslatorFunc) {
	m.registry[strings.ToLower(providerType)] = fn
}

// Run 是 worker.TranslateFunc 接口的实现。
//
// 流程：
//  1. 按 providerID 查 store 取 provider 配置
//  2. 读源 SRT 解析为 Subtitle 数组
//  3. provider.isAi ? HandleAIBatch : HandleAPIBatch
//  4. 按 formData.translateContent 渲染输出 SRT（默认 onlyTranslate）
//  5. 写到 workDir/<srcBase>.<tgtLang>.srt
//
// 失败时 onProgress 不再回调；返回 error 让上层 fail。
func (m *Manager) Run(ctx context.Context, srtPath, providerID, src, tgt string, onProgress ProgressFn) (string, error) {
	provider, err := m.findProvider(providerID)
	if err != nil {
		return "", err
	}
	translator, ok := m.registry[strings.ToLower(provider.Type)]
	if !ok {
		return "", fmt.Errorf("unknown translation provider type: %s", provider.Type)
	}

	logger.Debug("[translate] reading SRT: %s", srtPath)
	subs, err := asr.ReadSRT(srtPath)
	if err != nil {
		return "", fmt.Errorf("read source srt: %w", err)
	}
	if len(subs) == 0 {
		return "", fmt.Errorf("source srt 为空，无法翻译")
	}

	// 语言码映射
	mappedSrc := ConvertLanguageCode(src, provider.Type)
	mappedTgt := ConvertLanguageCode(tgt, provider.Type)
	logger.Debug("[translate] lang map: %s→%s mapped to %s→%s (provider=%s)",
		src, tgt, mappedSrc, mappedTgt, provider.Type)

	maxRetries := 2
	batchSize := provider.BatchSize
	logger.Info("[translate] start: provider=%s type=%s isAi=%v %s→%s subs=%d batchSize=%d concurrency=%d",
		provider.Name, provider.Type, provider.IsAi, mappedSrc, mappedTgt, len(subs), batchSize, provider.Concurrency)

	start := time.Now()
	var results []TranslationResult
	if provider.IsAi {
		results, err = HandleAIBatch(ctx, subs, *provider, mappedSrc, mappedTgt, translator, batchSize, onProgress, nil, maxRetries)
	} else {
		results, err = HandleAPIBatch(ctx, subs, *provider, mappedSrc, mappedTgt, translator, batchSize, onProgress, nil, maxRetries)
	}
	logger.Debug("[translate] batch done in %s err=%v results=%d", time.Since(start), err, len(results))
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "", fmt.Errorf("translate returned 0 results")
	}

	// 渲染输出 SRT
	workDir := filepath.Dir(srtPath)
	srcBase := strings.TrimSuffix(filepath.Base(srtPath), filepath.Ext(srtPath))
	outName := srcBase
	if tgt != "" {
		outName = srcBase + "." + tgt
	}
	outPath := filepath.Join(workDir, outName+".srt")
	if err := writeTranslatedSRT(outPath, results, "onlyTranslate"); err != nil {
		return "", err
	}
	logger.Info("[translate] done: out=%s", outPath)
	return outPath, nil
}

func (m *Manager) findProvider(id string) (*Provider, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("translate provider id 为空")
	}
	for _, p := range m.store.GetProviders() {
		if p.ID == id {
			cp := p
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("找不到 translate provider id=%s", id)
}

// writeTranslatedSRT 按 template 写 SRT 文件。
func writeTranslatedSRT(path string, results []TranslationResult, template string) error {
	sort.SliceStable(results, func(i, j int) bool { return results[i].ID < results[j].ID })

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create translated srt: %w", err)
	}
	defer f.Close()
	for _, r := range results {
		body := RenderContent(template, r.SourceContent, r.TargetContent)
		fmt.Fprintf(f, "%s\n%s\n%s", r.ID, r.StartEndTime, body)
	}
	return nil
}

// max int helper (Go 1.21+ 有 max，但保留兼容 ≥1.21)
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
