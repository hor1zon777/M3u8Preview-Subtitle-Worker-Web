// Package providers — Ollama local LLM。
//
// 与原 ollama.ts 一致：POST {apiUrl} 默认 chat 端点；body {model, messages, stream:false, format:'json'}。
// 兼容 /generate → /chat 自动改路径。
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

type ollamaRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Format   string        `json:"format,omitempty"`
}

type ollamaResponse struct {
	Message chatMessage `json:"message"`
	Error   string      `json:"error,omitempty"`
}

func Ollama(ctx context.Context, texts []string, p config.Provider, src, tgt string) ([]string, error) {
	apiURL := strings.TrimRight(firstNonEmpty(p.APIURL, "http://localhost:11434/api/chat"), "/")
	// 兼容旧 /generate
	apiURL = strings.Replace(apiURL, "/api/generate", "/api/chat", 1)
	if !strings.Contains(apiURL, "/api/chat") {
		apiURL = apiURL + "/api/chat"
	}

	out := make([]string, len(texts))
	for i, t := range texts {
		systemPrompt := renderVars(firstNonEmpty(p.SystemPrompt, defaultSystemPrompt), src, tgt)
		req := ollamaRequest{
			Model:    firstNonEmpty(p.ModelName, "llama3"),
			Messages: []chatMessage{{Role: "system", Content: systemPrompt}, {Role: "user", Content: t}},
			Stream:   false,
		}
		if p.UseJsonMode == nil || *p.UseJsonMode {
			req.Format = "json"
		}
		raw, _ := json.Marshal(req)
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		hr.Header.Set("Content-Type", "application/json")
		applyCustomHeaders(hr, p.CustomParameters)
		resp, err := httpClient.Do(hr)
		if err != nil {
			return nil, fmt.Errorf("ollama request: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("ollama HTTP %d: %s", resp.StatusCode, truncate(string(body), 500))
		}
		var parsed ollamaResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("ollama decode: %w (body=%s)", err, truncate(string(body), 200))
		}
		if parsed.Error != "" {
			return nil, fmt.Errorf("ollama: %s", parsed.Error)
		}
		out[i] = parsed.Message.Content
	}
	return out, nil
}
