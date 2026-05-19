// Package providers — OpenAI 兼容（覆盖 openai / deepseek / DeerAPI / Gemini / qwen / siliconflow）。
//
// 与原 TS service/openai.ts 行为一致：
//   - POST {apiUrl}/chat/completions
//   - Bearer apiKey
//   - body: {model, messages: [{system}, {user}], temperature: 0.3, stream:false, response_format?}
//   - structuredOutput: disabled / json_object / json_schema
//
// v2：structured output 三级自动回退。
//   很多兼容厂商不支持 json_schema（DeepSeek / SiliconFlow / 部分中转 API）。
//   当 API 返回 400 + "response_format" 时自动退一级：json_schema → json_object → disabled。
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/config"
	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/logger"
)

// 与 TS DefaultSystemPrompt 对齐。
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
	EnableThinking *bool          `json:"enable_thinking,omitempty"`
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
		req := chatCompletionsRequest{
			Model: firstNonEmpty(p.ModelName, "gpt-4o-mini"),
			Messages: []chatMessage{
				{Role: "system", Content: systemPrompt},
				{Role: "user", Content: t},
			},
			Temperature: 0.3,
			Stream:      false,
		}
		// qwen / dashscope：禁用思考
		if strings.Contains(strings.ToLower(p.Type), "qwen") ||
			strings.Contains(strings.ToLower(apiURL), "dashscope") {
			no := false
			req.EnableThinking = &no
		}

		// 三级回退：json_schema → json_object → disabled
		so := strings.ToLower(p.StructuredOutput)
		fallback := buildFallback(so)
		content, err := chatWithFallback(ctx, apiURL, p.APIKey, p.CustomParameters, req, fallback)
		if err != nil {
			return nil, err
		}
		out[i] = content
	}
	return out, nil
}

// buildFallback 把前端选项转成回退链。
func buildFallback(so string) []string {
	switch so {
	case "json_schema":
		return []string{"json_schema", "json_object", ""}
	case "json_object":
		return []string{"json_object", ""}
	default:
		return []string{""}
	}
}

// buildResponseFormat 按级别构造 response_format。空字符串 = 不传。
func buildResponseFormat(level string) map[string]any {
	switch level {
	case "json_schema":
		return map[string]any{
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
		return map[string]any{"type": "json_object"}
	default:
		return nil
	}
}

// chatWithFallback 逐级尝试 fallback 链：
//   先 json_schema → API 报 "response_format" 400 则退到 json_object →
//   仍不行则退到 disabled（不传 response_format）。
//   其他错误（401/403/429/超时/模型不存在）不重试，直接返回。
func chatWithFallback(
	ctx context.Context,
	apiURL, apiKey string,
	customParams map[string]any,
	req chatCompletionsRequest,
	fallback []string,
) (string, error) {
	for _, step := range fallback {
		req.ResponseFormat = buildResponseFormat(step)
		raw, _ := json.Marshal(req)
		stepLabel := step
		if stepLabel == "" {
			stepLabel = "disabled"
		}
		logger.Debug("[translate:openai] POST %s model=%s response_format=%s msgCount=%d userLen=%d",
			apiURL, req.Model, stepLabel, len(req.Messages), userMsgLen(req.Messages))
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(raw))
		if err != nil {
			return "", err
		}
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		httpReq.Header.Set("Content-Type", "application/json")
		applyCustomHeaders(httpReq, customParams)

		start := time.Now()
		resp, err := httpClient.Do(httpReq)
		if err != nil {
			logger.Debug("[translate:openai] request error: %v", err)
			return "", fmt.Errorf("openai request: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		logger.Debug("[translate:openai] → HTTP %d (took=%s, bytes=%d, response_format=%s)",
			resp.StatusCode, time.Since(start), len(body), stepLabel)

		// 仅 response_format 相关 400 才退到下一级
		if resp.StatusCode == 400 && strings.Contains(string(body), "response_format") {
			logger.Debug("[translate:openai] response_format=%s rejected, falling back to next level", stepLabel)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", fmt.Errorf("openai HTTP %d: %s", resp.StatusCode, truncate(string(body), 500))
		}
		var parsed chatCompletionsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return "", fmt.Errorf("openai decode: %w (body=%s)", err, truncate(string(body), 200))
		}
		if parsed.Error != nil && parsed.Error.Message != "" {
			return "", fmt.Errorf("openai error: %s", parsed.Error.Message)
		}
		if len(parsed.Choices) == 0 {
			return "", fmt.Errorf("openai: empty choices")
		}
		content := parsed.Choices[0].Message.Content
		logger.Debug("[translate:openai] choices[0].message.content len=%d", len(content))
		return content, nil
	}
	return "", fmt.Errorf("openai: all response_format fallbacks exhausted")
}

func userMsgLen(messages []chatMessage) int {
	for _, m := range messages {
		if m.Role == "user" {
			return len(m.Content)
		}
	}
	return 0
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
