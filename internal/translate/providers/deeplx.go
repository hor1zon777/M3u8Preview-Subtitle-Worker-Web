// Package providers — DeepLX 自部署翻译。
//
// POST {apiUrl} body {text, source_lang, target_lang}；响应 {data: "...", alternatives: [...]}
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

type deeplxRequest struct {
	Text       string `json:"text"`
	SourceLang string `json:"source_lang"`
	TargetLang string `json:"target_lang"`
}

type deeplxResponse struct {
	Code         int      `json:"code"`
	Data         string   `json:"data"`
	Alternatives []string `json:"alternatives,omitempty"`
	Message      string   `json:"message,omitempty"`
}

func DeepLX(ctx context.Context, texts []string, p config.Provider, src, tgt string) ([]string, error) {
	if p.APIURL == "" {
		return nil, fmt.Errorf("deeplx apiUrl missing")
	}
	out := make([]string, len(texts))
	for i, t := range texts {
		body := deeplxRequest{
			Text:       t,
			SourceLang: firstNonEmpty(strings.ToUpper(src), "EN"),
			TargetLang: firstNonEmpty(strings.ToUpper(tgt), "ZH"),
		}
		raw, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.APIURL, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("deeplx request: %w", err)
		}
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var parsed deeplxResponse
		if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
			return nil, fmt.Errorf("deeplx decode: %w (body=%s)", err, truncate(string(bodyBytes), 200))
		}
		if parsed.Code >= 400 || (parsed.Code == 0 && resp.StatusCode >= 400) {
			return nil, fmt.Errorf("deeplx HTTP %d: %s", resp.StatusCode, parsed.Message)
		}
		out[i] = firstNonEmpty(parsed.Data, firstAlt(parsed.Alternatives))
	}
	return out, nil
}

func firstAlt(alts []string) string {
	if len(alts) == 0 {
		return ""
	}
	return alts[0]
}
