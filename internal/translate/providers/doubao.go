// Package providers — 豆包 /api/v3/responses。
//
// 与 doubao.ts 一致：
//   - POST https://ark.cn-beijing.volces.com/api/v3/responses（可由 apiUrl 覆盖）
//   - Bearer apiKey
//   - body {model, input:[{role:user, content:[{type:input_text, text, translation_options}]}]}
//   - 单条 text 一次请求；无 batch
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/config"
)

type doubaoContent struct {
	Type               string                 `json:"type"`
	Text               string                 `json:"text"`
	TranslationOptions *doubaoTransOptions    `json:"translation_options,omitempty"`
}

type doubaoTransOptions struct {
	SourceLanguage string `json:"source_language,omitempty"`
	TargetLanguage string `json:"target_language,omitempty"`
}

type doubaoInput struct {
	Role    string          `json:"role"`
	Content []doubaoContent `json:"content"`
}

type doubaoRequest struct {
	Model string         `json:"model"`
	Input []doubaoInput  `json:"input"`
}

type doubaoOutput struct {
	Type    string         `json:"type"`
	Content []doubaoContent `json:"content"`
}

type doubaoResponse struct {
	Output []doubaoOutput `json:"output"`
	Error  *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

func Doubao(ctx context.Context, texts []string, p config.Provider, src, tgt string) ([]string, error) {
	if p.APIKey == "" {
		return nil, fmt.Errorf("doubao apiKey missing")
	}
	apiURL := firstNonEmpty(p.APIURL, "https://ark.cn-beijing.volces.com/api/v3/responses")
	out := make([]string, len(texts))
	for i, t := range texts {
		opts := doubaoTransOptions{}
		if src != "" {
			opts.SourceLanguage = src
		}
		if tgt != "" {
			opts.TargetLanguage = tgt
		}
		req := doubaoRequest{
			Model: firstNonEmpty(p.ModelName, "doubao-seed-translation-250915"),
			Input: []doubaoInput{{
				Role: "user",
				Content: []doubaoContent{{
					Type:               "input_text",
					Text:               t,
					TranslationOptions: &opts,
				}},
			}},
		}
		raw, _ := json.Marshal(req)
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		hr.Header.Set("Authorization", "Bearer "+p.APIKey)
		hr.Header.Set("Content-Type", "application/json")
		resp, err := httpClient.Do(hr)
		if err != nil {
			return nil, fmt.Errorf("doubao request: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("doubao HTTP %d: %s", resp.StatusCode, truncate(string(body), 500))
		}
		var parsed doubaoResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("doubao decode: %w", err)
		}
		if parsed.Error != nil && parsed.Error.Message != "" {
			return nil, fmt.Errorf("doubao %s: %s", parsed.Error.Code, parsed.Error.Message)
		}
		// 拼接所有 output_text
		var sb bytes.Buffer
		for _, o := range parsed.Output {
			for _, c := range o.Content {
				if c.Type == "output_text" {
					sb.WriteString(c.Text)
				}
			}
		}
		out[i] = sb.String()
	}
	return out, nil
}
