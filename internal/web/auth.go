// Package web — Bearer Token 认证中间件。
//
// Token 持久化在 config.SystemSettings.WebToken（在设置页编辑）。
// 空 token = 不强制认证（首次启动允许直接访问，便于用户在设置页配置 token）。
//
// 免认证的端点：
//   - /api/auth/status — 让前端判断"需不需要登录"
//   - /api/auth/login  — 校验 token 是否正确（供登录页使用）
package web

import (
	"net/http"
	"strings"
)

// currentToken 当前生效的 token，从 store 动态读取。
func (s *Server) currentToken() string {
	return strings.TrimSpace(s.store.GetSettings().WebToken)
}

// authMiddleware 校验 Authorization: Bearer <token>。
// store 中 webToken 为空 → 全部放行；非空 → 必须匹配。
//
// WebSocket 端点支持额外用 ?token=xxx 透传（浏览器原生 WS 不支持自定义 header）。
func (s *Server) authMiddleware() func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 免认证端点（让登录页可以校验）
			if r.URL.Path == "/api/auth/status" || r.URL.Path == "/api/auth/login" {
				next.ServeHTTP(w, r)
				return
			}
			token := s.currentToken()
			if token == "" {
				next.ServeHTTP(w, r)
				return
			}
			// WS：允许 ?token=
			if strings.HasSuffix(r.URL.Path, "/ws/status") {
				if r.URL.Query().Get("token") == token {
					next.ServeHTTP(w, r)
					return
				}
			}
			auth := r.Header.Get("Authorization")
			if strings.HasPrefix(auth, "Bearer ") && strings.TrimSpace(auth[7:]) == token {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
}
