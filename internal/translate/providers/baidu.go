// Package providers — 百度通用翻译。
//
// 与 baidu.ts 行为一致：
//   - POST https://fanyi-api.baidu.com/api/trans/vip/translate (form-urlencoded)
//   - sign = md5(appid + q + salt + key)
//   - appid=APIKey, key=APISecret
//   - q 是 batch 用 \n 拼起来
//   - trans_result[i].dst 是译文
package providers

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hor1zon777/m3u8-preview-subtitle-worker-web/internal/config"
)

type baiduResponse struct {
	From        string `json:"from"`
	To          string `json:"to"`
	TransResult []struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	} `json:"trans_result"`
	ErrorCode string `json:"error_code,omitempty"`
	ErrorMsg  string `json:"error_msg,omitempty"`
}

func Baidu(ctx context.Context, texts []string, p config.Provider, src, tgt string) ([]string, error) {
	if p.APIKey == "" || p.APISecret == "" {
		return nil, fmt.Errorf("baidu apiKey 或 apiSecret 未配置")
	}
	q := strings.Join(texts, "\n")
	salt := strconv.FormatInt(time.Now().UnixMilli(), 10)
	sign := md5Hex(p.APIKey + q + salt + p.APISecret)

	from := firstNonEmpty(src, "auto")
	to := firstNonEmpty(tgt, "zh")

	form := url.Values{}
	form.Set("q", q)
	form.Set("from", from)
	form.Set("to", to)
	form.Set("appid", p.APIKey)
	form.Set("salt", salt)
	form.Set("sign", sign)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://fanyi-api.baidu.com/api/trans/vip/translate",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("baidu request: %w", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var parsed baiduResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("baidu decode: %w (body=%s)", err, truncate(string(body), 200))
	}
	if parsed.ErrorCode != "" {
		return nil, fmt.Errorf("baidu error %s: %s", parsed.ErrorCode, parsed.ErrorMsg)
	}
	out := make([]string, 0, len(parsed.TransResult))
	for _, r := range parsed.TransResult {
		out = append(out, r.Dst)
	}
	if len(out) != len(texts) {
		// 部分情况下 trans_result 数量与按 \n 切分的不一致：原样兜底
		// 注意：上层 api_batch 会检测 count 不匹配并报错
		return out, nil
	}
	return out, nil
}

func md5Hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}
