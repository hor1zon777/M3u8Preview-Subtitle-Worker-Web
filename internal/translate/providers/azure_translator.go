// Package providers — Azure Cognitive Translator。
//
// POST https://api.cognitive.microsofttranslator.com/translate?api-version=3.0&from&to
// 头: Ocp-Apim-Subscription-Key, Ocp-Apim-Subscription-Region
// body: [{text}, ...]
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/config"
)

type azureTransItem struct {
	Text string `json:"text"`
}

type azureTransResponse struct {
	Translations []struct {
		Text string `json:"text"`
	} `json:"translations"`
}

type azureTransError struct {
	Error struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// AzureTranslator 接 Azure Cognitive Services Translator。
func AzureTranslator(ctx context.Context, texts []string, p config.Provider, src, tgt string) ([]string, error) {
	if p.APIKey == "" {
		return nil, fmt.Errorf("azure apiKey missing")
	}
	endpoint := firstNonEmpty(p.APIURL, "https://api.cognitive.microsofttranslator.com")
	u, err := url.Parse(strings.TrimRight(endpoint, "/") + "/translate")
	if err != nil {
		return nil, fmt.Errorf("azure parse url: %w", err)
	}
	q := u.Query()
	q.Set("api-version", "3.0")
	if src != "" {
		q.Set("from", src)
	}
	q.Set("to", firstNonEmpty(tgt, "zh-Hans"))
	u.RawQuery = q.Encode()

	items := make([]azureTransItem, len(texts))
	for i, t := range texts {
		items[i] = azureTransItem{Text: t}
	}
	raw, _ := json.Marshal(items)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Ocp-Apim-Subscription-Key", p.APIKey)
	if p.APISecret != "" {
		req.Header.Set("Ocp-Apim-Subscription-Region", p.APISecret)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("azure request: %w", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e azureTransError
		_ = json.Unmarshal(body, &e)
		if e.Error.Message != "" {
			return nil, fmt.Errorf("azure HTTP %d: %s", resp.StatusCode, e.Error.Message)
		}
		return nil, fmt.Errorf("azure HTTP %d: %s", resp.StatusCode, truncate(string(body), 500))
	}
	var parsed []azureTransResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("azure decode: %w", err)
	}
	out := make([]string, 0, len(parsed))
	for _, r := range parsed {
		if len(r.Translations) > 0 {
			out = append(out, r.Translations[0].Text)
		} else {
			out = append(out, "")
		}
	}
	return out, nil
}
