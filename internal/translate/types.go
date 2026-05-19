// Package translate — 共享类型与常量。
package translate

import (
	"context"
	"errors"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/asr"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/config"
)

// Subtitle 与 asr.Subtitle 同义，供翻译模块独立使用。
type Subtitle = asr.Subtitle

// Provider 翻译服务条目；与 config.Provider 同语义。
type Provider = config.Provider

// TranslationResult 单条翻译结果。
type TranslationResult struct {
	ID            string `json:"id"`
	StartEndTime  string `json:"startEndTime"`
	SourceContent string `json:"sourceContent"`
	TargetContent string `json:"targetContent"`
}

// TranslatorFunc 各 provider 实现的统一接口。
//
//   - texts：批次原文（每条字幕内容已合并为单行字符串）
//   - p：provider 配置
//   - src/tgt：语言码（已 convertLanguageCode 映射）
//   - 返回：每条原文对应的译文（顺序对齐）
type TranslatorFunc func(ctx context.Context, texts []string, p Provider, src, tgt string) ([]string, error)

// FormData 翻译表单配置（与 TS IFormData 对齐）。
type FormData struct {
	TranslateContent         string `json:"translateContent"`         // onlyTranslate / sourceAndTranslate / translateAndSource
	TargetSrtSaveOption      string `json:"targetSrtSaveOption"`      // fileName / customFileName
	CustomTargetSrtFileName  string `json:"customTargetSrtFileName"`
	SourceLanguage           string `json:"sourceLanguage"`
	TargetLanguage           string `json:"targetLanguage"`
	TranslateRetryTimes      int    `json:"translateRetryTimes"`
}

// ProgressFn 翻译进度回调（0-100）。
type ProgressFn func(percent int)

// Errors
var (
	ErrUnknownProviderType = errors.New("unknown translation provider type")
	ErrEmptyTranslation    = errors.New("empty translation result")
)
