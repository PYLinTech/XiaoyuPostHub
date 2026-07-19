package server

import (
	_ "embed"
	"log"
	"net/http"
)

// notFoundHTML 是 404 错误页内容，源文件 server/static/404.html
// 在编译期通过 //go:embed 嵌入二进制，运行时无需读取磁盘文件。
//
//go:embed static/404.html
var notFoundHTML []byte

// WithErrorPage 把所有 4xx/5xx 响应统一替换为内置 404 页内容，
// 状态码保留原始值（便于诊断）。成功路径完全透传，不 buffer body，
// 支持大文件下载与流式响应。
func WithErrorPage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		iw := &interceptWriter{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("handler panic 已恢复：%v", rec)
				if iw.status == 0 {
					iw.status = http.StatusInternalServerError
				}
			}
			if iw.status >= 400 {
				iw.writeNotFound()
			}
		}()
		next.ServeHTTP(iw, r)
	})
}

// interceptWriter 包装 ResponseWriter：
//
//   - 成功路径（status < 400）：完全透传，Write/Flush/Header 不变。
//   - 错误路径（status >= 400）：丢弃所有 Write，最后由 writeNotFound 注入 404 页。
type interceptWriter struct {
	http.ResponseWriter
	status       int
	written      bool
	fallbackDone bool
}

func (w *interceptWriter) WriteHeader(code int) {
	if w.written {
		return
	}
	w.status = code
	w.written = true
	if code < 400 {
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *interceptWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	if w.status >= 400 {
		return len(b), nil // 丢弃错误响应的 body
	}
	return w.ResponseWriter.Write(b)
}

func (w *interceptWriter) Flush() {
	if w.status >= 400 {
		return
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *interceptWriter) writeNotFound() {
	if w.fallbackDone {
		return
	}
	w.fallbackDone = true
	if w.status == 0 {
		w.status = http.StatusInternalServerError
	}
	h := w.ResponseWriter.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Del("Content-Length")
	w.ResponseWriter.WriteHeader(w.status)
	_, _ = w.ResponseWriter.Write(notFoundHTML)
}
