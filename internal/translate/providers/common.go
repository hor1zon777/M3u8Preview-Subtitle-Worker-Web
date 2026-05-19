// Package providers — 共享 HTTP client + 工具函数。
package providers

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// httpClient 所有 provider 共用的 HTTP 客户端。
// timeout 120s 覆盖大批次翻译；TLS 校验默认开启（生产合规）。
var httpClient = &http.Client{
	Timeout: 120 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{},
	},
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
