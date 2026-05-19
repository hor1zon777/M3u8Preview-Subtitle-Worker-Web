// Package providers — OpenAI 兼容（覆盖 openai / deepseek / DeerAPI / Gemini / qwen / siliconflow）。
//
// 与原 TS service/openai.ts 行为一致：
//   - POST {apiUrl}/chat/completions
//   - Bearer apiKey
//   - body: {model, messages: [{system}, {user}], temperature: 0.3, stream:false, response_format?}
//   - structuredOutput: disabled / json_object / json_schema
//
// 实现策略：
//   - texts 数组单元素时（AI 批模式：完整 JSON prompt）→ messages=[system, user]
//   - texts 多元素时（API 模式不会走 isAi=true 这条路径）→ fallback：每条独立翻译
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

// DefaultUserPrompt / DefaultSystemPrompt 内容固定（与 translate.DefaultSystemPrompt 同步）。
const defaultSystemPrompt = `你是一位专业字幕翻译专家。请将给定的 ${sourceLanguage} 字幕翻译成 ${targetLanguage}，遵守以下要求：

1. 严格保持原始字幕的编号顺序与时间轴对应关系
2. 译文应自然流畅，符合目标语言的表达习惯
3. 保留原文中的语气词、感叹号、问号等情感符号
4. 对人名、专有名词、品牌名采用通行译法（如不确定则音译）
5. 在保持原意基础上适度本地化，避免逐字直译
6. 输出格式必须是 JSON 对象，键为字幕 ID，值为翻译后的内容；不要输出额外说明文字。`

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionsRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	Temperature    float64        `json:"temperature"`
	Stream         bool           `json:"stream"`
	ResponseFormat map[string]any `json:"response_format,omitempty"`
	// qwen / dashscope 需要的字段
	EnableThinking *bool `json:"enable_thinking,omitempty"`
}

type chatCompletionsResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error,omitempty"`
}

// OpenAI 是 TranslatorFunc 类型。
func OpenAI(ctx context.Context, texts []string, p config.Provider, src, tgt string) ([]string, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if p.APIKey == "" {
		return nil, fmt.Errorf("provider %s: apiKey missing", p.Name)
	}
	apiURL := strings.TrimRight(firstNonEmpty(p.APIURL, "https://api.openai.com/v1"), "/")
	if !strings.HasSuffix(apiURL, "/chat/completions") {
		apiURL = apiURL + "/chat/completions"
	}

	out := make([]string, len(texts))
	for i, t := range texts {
		systemPrompt := renderVars(firstNonEmpty(p.SystemPrompt, defaultSystemPrompt), src, tgt)
		// 渲染 user prompt 模板（${content}）：t 是已经渲染好的内容
		userContent := t
		req := chatCompletionsRequest{
			Model: firstNonEmpty(p.ModelName, "gpt-4o-mini"),
			Messages: []chatMessage{
				{Role: "system", Content: systemPrompt},
				{Role: "user", Content: userContent},
			},
			Temperature: 0.3,
			Stream:      false,
		}
		// structured output 支持
		switch strings.ToLower(p.StructuredOutput) {
		case "json_schema":
			req.ResponseFormat = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name": "translations",
					"schema": map[string]any{
						"type":                 "object",
						"additionalProperties": map[string]string{"type": "string"},
					},
				},
			}
		case "json_object":
			req.ResponseFormat = map[string]any{"type": "json_object"}
		}
		// qwen / dashscope：禁用思考
		if strings.Contains(strings.ToLower(p.Type), "qwen") ||
			strings.Contains(strings.ToLower(apiURL), "dashscope") {
			no := false
			req.EnableThinking = &no
		}

		raw, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
		httpReq.Header.Set("Content-Type", "application/json")
		applyCustomHeaders(httpReq, p.CustomParameters)

		resp, err := httpClient.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("openai request: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("openai HTTP %d: %s", resp.StatusCode, truncate(string(body), 500))
		}
		var parsed chatCompletionsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("openai decode: %w", err)
		}
		if parsed.Error != nil && parsed.Error.Message != "" {
			return nil, fmt.Errorf("openai error: %s", parsed.Error.Message)
		}
		if len(parsed.Choices) == 0 {
			return nil, fmt.Errorf("openai: empty choices")
		}
		out[i] = parsed.Choices[0].Message.Content
	}
	return out, nil
}

func renderVars(template, src, tgt string) string {
	out := template
	out = strings.ReplaceAll(out, "${sourceLanguage}", src)
	out = strings.ReplaceAll(out, "${targetLanguage}", tgt)
	return out
}

func applyCustomHeaders(req *http.Request, cp map[string]any) {
	if cp == nil {
		return
	}
	headers, ok := cp["headers"].(map[string]any)
	if !ok {
		return
	}
	for k, v := range headers {
		if s, ok := v.(string); ok {
			req.Header.Set(k, s)
		}
	}
}
