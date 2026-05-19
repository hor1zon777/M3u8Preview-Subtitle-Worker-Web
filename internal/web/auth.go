// Package web — 可选 Bearer Token 认证中间件。
package web

import (
	"net/http"
	"strings"
)

// authMiddleware 在 authToken 不为空时校验 Authorization: Bearer <token>。
// 空 token = 关闭认证（LAN 部署常用）。
//
// WebSocket 端点支持额外用 ?token=xxx 透传（浏览器原生 WS 不支持自定义 header）。
func (s *Server) authMiddleware() func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.authToken == "" {
				next.ServeHTTP(w, r)
				return
			}
			// WS：允许 ?token=
			if strings.HasSuffix(r.URL.Path, "/ws/status") {
				if r.URL.Query().Get("token") == s.authToken {
					next.ServeHTTP(w, r)
					return
				}
			}
			auth := r.Header.Get("Authorization")
			if strings.HasPrefix(auth, "Bearer ") && strings.TrimSpace(auth[7:]) == s.authToken {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
}
