// Package web — SPA 静态文件嵌入。
//
// 前端 Next.js 构建产物（pnpm build → web/out）拷贝到 internal/web/dist 后
// 由 go:embed 嵌入二进制。
//
// 未配置前端构建时 dist 为空 —— 此时 SPA fallback 返回 503 + 提示文案，让用户
// 知道需要先跑 `scripts/build.sh` 构建前端。
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// spaHandler 提供 SPA：
//   - 优先匹配 dist 里的实际文件
//   - 目录路径 → 自动 serve 它的 index.html（兼容 Next.js trailingSlash: true 模式生成的
//     models/index.html、providers/index.html 等子路由）
//   - 都找不到 → fallback 到根 index.html（让 SPA 客户端路由处理）
//   - dist 不存在 → 503 + 友好提示
//
// 注意：直接 `fs.Stat(sub, "models/")` 在 embed.FS 上会返回 ErrNotExist，
// 必须把 trailing slash 去掉先 stat 目录，再 stat 它的 index.html。
func (s *Server) spaHandler() http.HandlerFunc {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "frontend dist not embedded; run scripts/build.sh first", http.StatusServiceUnavailable)
		}
	}
	fileServer := http.FileServer(http.FS(sub))
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		// 把 leading/trailing slash 去掉拿到 embed 内部相对路径
		p := strings.Trim(r.URL.Path, "/")
		if p == "" {
			fileServer.ServeHTTP(w, r) // 根 → 自动 serve index.html
			return
		}
		// 1) 直接是文件
		if info, err := fs.Stat(sub, p); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		// 2) 目录路径 → 它的 index.html（Next.js trailingSlash 模式）。
		//    不能让 http.FileServer 走 /<path>/index.html —— 它会自动 301 回 /<path>/ 死循环。
		//    直接读字节写 response。
		if data, err := fs.ReadFile(sub, p+"/index.html"); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
		// 3) 兜底回根 index.html（SPA 客户端路由，避免硬刷子路由 404）
		if data, err := fs.ReadFile(sub, "index.html"); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
		http.NotFound(w, r)
	}
}
