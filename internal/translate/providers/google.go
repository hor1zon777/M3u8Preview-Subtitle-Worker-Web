// Package providers — Google Translation API v2。
//
// POST https://translation.googleapis.com/language/translate/v2?key={apiKey}
// body: {q: [...], source, target, format:'text'}
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/config"
)

type googleRequest struct {
	Q      []string `json:"q"`
	Source string   `json:"source,omitempty"`
	Target string   `json:"target"`
	Format string   `json:"format"`
}

type googleResponse struct {
	Data struct {
		Translations []struct {
			TranslatedText string `json:"translatedText"`
		} `json:"translations"`
	} `json:"data"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func Google(ctx context.Context, texts []string, p config.Provider, src, tgt string) ([]string, error) {
	if p.APIKey == "" {
		return nil, fmt.Errorf("google apiKey missing")
	}
	apiBase := firstNonEmpty(p.APIURL, "https://translation.googleapis.com/language/translate/v2")
	u, err := url.Parse(apiBase)
	if err != nil {
		return nil, fmt.Errorf("google parse url: %w", err)
	}
	q := u.Query()
	q.Set("key", p.APIKey)
	u.RawQuery = q.Encode()

	body := googleRequest{Q: texts, Source: src, Target: tgt, Format: "text"}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google request: %w", err)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var parsed googleResponse
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return nil, fmt.Errorf("google decode: %w (body=%s)", err, truncate(string(bodyBytes), 200))
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("google %d: %s", parsed.Error.Code, parsed.Error.Message)
	}
	out := make([]string, 0, len(parsed.Data.Translations))
	for _, t := range parsed.Data.Translations {
		out = append(out, t.TranslatedText)
	}
	return out, nil
}
