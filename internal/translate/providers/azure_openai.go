// Package providers — Azure OpenAI Service。
//
// 与原 azureOpenai.ts 行为一致：
//   - apiUrl 形如 https://<resource>.openai.azure.com/openai/deployments/<deployment>/chat/completions?api-version=...
//   - 头 api-key: <apiKey>
//   - body 与 OpenAI 兼容（response_format json_object）
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/config"
)

// AzureOpenAI 翻译。apiUrl 必须是完整的 chat/completions endpoint URL（含 api-version）。
func AzureOpenAI(ctx context.Context, texts []string, p config.Provider, src, tgt string) ([]string, error) {
	if p.APIKey == "" {
		return nil, fmt.Errorf("azureopenai apiKey missing")
	}
	if p.APIURL == "" {
		return nil, fmt.Errorf("azureopenai apiUrl missing")
	}
	apiURL := strings.TrimSpace(p.APIURL)
	if !strings.Contains(apiURL, "chat/completions") {
		apiURL = strings.TrimRight(apiURL, "/") + "/chat/completions"
	}

	out := make([]string, len(texts))
	for i, t := range texts {
		systemPrompt := renderVars(firstNonEmpty(p.SystemPrompt, defaultSystemPrompt), src, tgt)
		req := chatCompletionsRequest{
			Model:       firstNonEmpty(p.ModelName, ""), // Azure 不用 model
			Messages:    []chatMessage{{Role: "system", Content: systemPrompt}, {Role: "user", Content: t}},
			Temperature: 0.3,
			Stream:      false,
		}
		switch strings.ToLower(p.StructuredOutput) {
		case "json_schema", "json_object":
			req.ResponseFormat = map[string]any{"type": "json_object"}
		}
		raw, _ := json.Marshal(req)
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		hr.Header.Set("api-key", p.APIKey)
		hr.Header.Set("Content-Type", "application/json")
		applyCustomHeaders(hr, p.CustomParameters)
		resp, err := httpClient.Do(hr)
		if err != nil {
			return nil, fmt.Errorf("azureopenai request: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("azureopenai HTTP %d: %s", resp.StatusCode, truncate(string(body), 500))
		}
		var parsed chatCompletionsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("azureopenai decode: %w", err)
		}
		if len(parsed.Choices) == 0 {
			return nil, fmt.Errorf("azureopenai: empty choices")
		}
		out[i] = parsed.Choices[0].Message.Content
	}
	return out, nil
}
