// Package providers — 阿里云机器翻译 GetBatchTranslate（ACS3 V3 签名）。
//
// 接入：
//   - GET/POST https://mt.aliyuncs.com/
//   - Action=GetBatchTranslate, Version=2018-10-12
//   - 签名 V3 HMAC-SHA256
//   - sourceText 用 JSON.stringify({"0":"...", "1":"..."}) 形式批量
//   - response: { Data: { TranslatedList: [{ index, translated, code, errorMsg }, ...] } }
package providers

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/config"
)

type aliyunResponseBody struct {
	Body struct {
		Code int `json:"Code"`
		Data struct {
			TranslatedList []struct {
				Index      string `json:"index"`
				Translated string `json:"translated"`
				Code       string `json:"code"`
				ErrorMsg   string `json:"errorMsg"`
			} `json:"TranslatedList"`
		} `json:"Data"`
		Message string `json:"Message"`
	} `json:"body"`
	// 顶层备用结构
	Code int `json:"Code"`
	Data struct {
		TranslatedList []struct {
			Index      string `json:"index"`
			Translated string `json:"translated"`
		} `json:"TranslatedList"`
	} `json:"Data"`
	Message string `json:"Message"`
}

// Aliyun 调用阿里云批量翻译。p.APIKey = AccessKeyID，p.APISecret = AccessKeySecret。
func Aliyun(ctx context.Context, texts []string, p config.Provider, src, tgt string) ([]string, error) {
	if p.APIKey == "" || p.APISecret == "" {
		return nil, fmt.Errorf("aliyun apiKey 或 apiSecret 未配置")
	}
	if tgt == "" {
		return nil, fmt.Errorf("aliyun targetLanguage 未指定")
	}
	host := firstNonEmpty(p.Endpoint, "mt.aliyuncs.com")
	endpoint := "https://" + host + "/"

	// 构造 sourceText：{"0":"text0","1":"text1",...}
	indexed := make(map[string]string, len(texts))
	for i, t := range texts {
		indexed[strconv.Itoa(i)] = t
	}
	sourceTextJSON, _ := json.Marshal(indexed)

	form := url.Values{}
	form.Set("FormatType", "text")
	form.Set("SourceLanguage", firstNonEmpty(src, "auto"))
	form.Set("TargetLanguage", tgt)
	form.Set("Scene", "general")
	form.Set("ApiType", "translate_standard")
	form.Set("SourceText", string(sourceTextJSON))

	bodyStr := form.Encode()
	bodyBytes := []byte(bodyStr)
	bodyHash := sha256Hex(bodyBytes)

	now := time.Now().UTC()
	xDate := now.Format("2006-01-02T15:04:05Z")
	nonce := strconv.FormatInt(time.Now().UnixNano(), 10)

	headers := map[string]string{
		"host":                  host,
		"x-acs-action":          "GetBatchTranslate",
		"x-acs-version":         "2018-10-12",
		"x-acs-date":            xDate,
		"x-acs-signature-nonce": nonce,
		"x-acs-content-sha256":  bodyHash,
		"content-type":          "application/x-www-form-urlencoded",
	}

	canonicalHeaders, signedHeaders := buildAcs3CanonicalHeaders(headers)
	canonicalRequest := strings.Join([]string{
		"POST",
		"/",
		"", // canonical query string（无）
		canonicalHeaders,
		signedHeaders,
		bodyHash,
	}, "\n")

	stringToSign := "ACS3-HMAC-SHA256\n" + sha256Hex([]byte(canonicalRequest))
	signature := hex.EncodeToString(hmacSHA256([]byte(p.APISecret), stringToSign))

	authz := fmt.Sprintf("ACS3-HMAC-SHA256 Credential=%s,SignedHeaders=%s,Signature=%s",
		p.APIKey, signedHeaders, signature)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", authz)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("aliyun request: %w", err)
	}
	respBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("aliyun HTTP %d: %s", resp.StatusCode, truncate(string(respBytes), 500))
	}
	var parsed aliyunResponseBody
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return nil, fmt.Errorf("aliyun decode: %w (body=%s)", err, truncate(string(respBytes), 200))
	}
	list := parsed.Body.Data.TranslatedList
	if len(list) == 0 {
		list = nil
		for _, t := range parsed.Data.TranslatedList {
			list = append(list, struct {
				Index      string `json:"index"`
				Translated string `json:"translated"`
				Code       string `json:"code"`
				ErrorMsg   string `json:"errorMsg"`
			}{Index: t.Index, Translated: t.Translated})
		}
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("aliyun: empty translated list (code=%d, msg=%s)", parsed.Body.Code, parsed.Body.Message)
	}
	// 按 index 排序回原顺序
	sort.SliceStable(list, func(i, j int) bool {
		ai, _ := strconv.Atoi(list[i].Index)
		bj, _ := strconv.Atoi(list[j].Index)
		return ai < bj
	})
	out := make([]string, 0, len(list))
	for _, l := range list {
		out = append(out, l.Translated)
	}
	return out, nil
}

func buildAcs3CanonicalHeaders(headers map[string]string) (canonical, signed string) {
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var cb, sb strings.Builder
	for i, k := range keys {
		lk := strings.ToLower(k)
		cb.WriteString(lk + ":" + strings.TrimSpace(headers[k]) + "\n")
		if i > 0 {
			sb.WriteString(";")
		}
		sb.WriteString(lk)
	}
	return cb.String(), sb.String()
}
