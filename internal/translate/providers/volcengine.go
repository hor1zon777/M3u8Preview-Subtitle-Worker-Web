// Package providers — 火山引擎机器翻译 TranslateText（V4 签名）。
//
// 接入：
//   - POST https://open.volcengineapi.com/?Action=TranslateText&Version=2020-06-01
//   - Region: cn-north-1, Service: translate
//   - 签名版本 HMAC-SHA256，Volcengine 自有派生密钥链
//   - body: {TargetLanguage, TextList:[...], SourceLanguage?}
//   - response: {TranslationList:[{Translation, ...}], ResponseMetadata:{...}, ResponseMetadata: { Error:{Code,Message} }}
package providers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/config"
)

type volcRequestBody struct {
	TargetLanguage string   `json:"TargetLanguage"`
	SourceLanguage string   `json:"SourceLanguage,omitempty"`
	TextList       []string `json:"TextList"`
}

type volcResponseBody struct {
	TranslationList []struct {
		Translation         string `json:"Translation"`
		DetectedSourceLanguage string `json:"DetectedSourceLanguage,omitempty"`
	} `json:"TranslationList"`
	ResponseMetadata struct {
		Error *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error,omitempty"`
	} `json:"ResponseMetadata"`
}

// Volcengine 调用火山翻译。p.APIKey = AccessKeyID，p.APISecret = SecretKey。
func Volcengine(ctx context.Context, texts []string, p config.Provider, src, tgt string) ([]string, error) {
	if p.APIKey == "" || p.APISecret == "" {
		return nil, fmt.Errorf("volc apiKey 或 apiSecret 未配置")
	}
	if tgt == "" {
		return nil, fmt.Errorf("volc targetLanguage 未指定")
	}
	body := volcRequestBody{TargetLanguage: tgt, SourceLanguage: src, TextList: texts}
	rawBody, _ := json.Marshal(body)

	host := "open.volcengineapi.com"
	region := "cn-north-1"
	service := "translate"

	query := "Action=TranslateText&Version=2020-06-01"
	endpoint := "https://" + host + "/?" + query

	// 签名所需头
	now := time.Now().UTC()
	xDate := now.Format("20060102T150405Z")
	shortDate := now.Format("20060102")
	payloadHash := sha256Hex(rawBody)

	headers := map[string]string{
		"Host":               host,
		"Content-Type":       "application/json; charset=utf-8",
		"X-Date":             xDate,
		"X-Content-Sha256":   payloadHash,
	}

	canonicalHeaders, signedHeaders := buildVolcCanonicalHeaders(headers)
	canonicalRequest := strings.Join([]string{
		"POST",
		"/",
		query,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	credentialScope := shortDate + "/" + region + "/" + service + "/request"
	stringToSign := strings.Join([]string{
		"HMAC-SHA256",
		xDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	// 派生 SigningKey
	kDate := hmacSHA256([]byte(p.APISecret), shortDate)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	authz := fmt.Sprintf("HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		p.APIKey, credentialScope, signedHeaders, signature)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(rawBody))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", authz)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("volc request: %w", err)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("volc HTTP %d: %s", resp.StatusCode, truncate(string(bodyBytes), 500))
	}
	var parsed volcResponseBody
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return nil, fmt.Errorf("volc decode: %w (body=%s)", err, truncate(string(bodyBytes), 200))
	}
	if parsed.ResponseMetadata.Error != nil && parsed.ResponseMetadata.Error.Message != "" {
		return nil, fmt.Errorf("volc %s: %s", parsed.ResponseMetadata.Error.Code, parsed.ResponseMetadata.Error.Message)
	}
	out := make([]string, 0, len(parsed.TranslationList))
	for _, t := range parsed.TranslationList {
		out = append(out, t.Translation)
	}
	return out, nil
}

func buildVolcCanonicalHeaders(headers map[string]string) (canonical, signed string) {
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

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}
