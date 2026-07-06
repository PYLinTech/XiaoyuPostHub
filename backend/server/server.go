// Package server 提供 XiaoyuPostHub 后端的 HTTP 路由与错误处理能力。
package server

import (
	"net/http"
	"strings"
)

// NewRouter 构造分流路由：
//
//	/api/*  → API handler（后端业务入口）
//	其余    → staticDir 下的静态文件
//
// 所有响应再经 WithErrorPage 包裹，错误状态统一替换为内置 404 页。
func NewRouter(staticDir string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", APIHandler())
	mux.Handle("/", StaticHandler(staticDir))
	return WithErrorPage(mux)
}

// StaticHandler 提供静态文件服务；拒绝包含 .. 的路径以防穿越。
func StaticHandler(staticDir string) http.Handler {
	fs := http.FileServer(http.Dir(staticDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "..") {
			http.NotFound(w, r)
			return
		}
		fs.ServeHTTP(w, r)
	})
}

// APIHandler 注册后端 API。当前仅挂 /api/health 用于存活探测；
// 后续真实接口在这里挂载。
func APIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	return mux
}