package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// flushRecorder 包装 httptest.ResponseRecorder，统计 Flush 调用次数。
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushCount int
}

func (f *flushRecorder) Flush() {
	f.flushCount++
	f.ResponseRecorder.Flush()
}

// --- interceptWriter 边界条件（白盒） ---

func TestInterceptWriter_DefaultsTo200(t *testing.T) {
	w := httptest.NewRecorder()
	iw := &interceptWriter{ResponseWriter: w}

	n, err := iw.Write([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("Write returned n = %d, want 5", n)
	}
	if w.Code != http.StatusOK {
		t.Errorf("default status = %d, want 200", w.Code)
	}
	if w.Body.String() != "hello" {
		t.Errorf("body = %q, want 'hello'", w.Body.String())
	}
}

func TestInterceptWriter_DiscardBodyOnError(t *testing.T) {
	w := httptest.NewRecorder()
	iw := &interceptWriter{ResponseWriter: w}

	iw.WriteHeader(http.StatusInternalServerError)
	n, err := iw.Write([]byte("this should be discarded"))
	if err != nil {
		t.Fatal(err)
	}
	if n != len("this should be discarded") {
		t.Errorf("Write returned n = %d, want %d", n, len("this should be discarded"))
	}
	if w.Body.Len() != 0 {
		t.Errorf("body should be empty before writeNotFound, got %d bytes", w.Body.Len())
	}

	iw.writeNotFound()
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if w.Body.String() != string(notFoundHTML) {
		t.Errorf("body mismatch")
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type = %q, want text/html", w.Header().Get("Content-Type"))
	}
}

// TestInterceptWriter_PreservesFirstStatus 重复调用 WriteHeader 时只采纳第一次。
func TestInterceptWriter_PreservesFirstStatus(t *testing.T) {
	w := httptest.NewRecorder()
	iw := &interceptWriter{ResponseWriter: w}

	iw.WriteHeader(http.StatusForbidden)
	iw.WriteHeader(http.StatusInternalServerError) // 应被忽略
	iw.writeNotFound()

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (first WriteHeader 应保留)", w.Code)
	}
}

func TestInterceptWriter_FlushOnlyOnSuccess(t *testing.T) {
	// 错误状态：Flush 不应触达底层
	wErr := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	iwErr := &interceptWriter{ResponseWriter: wErr}
	iwErr.WriteHeader(http.StatusInternalServerError)
	iwErr.Flush()
	if wErr.flushCount != 0 {
		t.Errorf("Flush called %d times on error path, want 0", wErr.flushCount)
	}

	// 成功状态：Flush 应透传
	wOK := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	iwOK := &interceptWriter{ResponseWriter: wOK}
	iwOK.Flush()
	if wOK.flushCount != 1 {
		t.Errorf("Flush called %d times on success path, want 1", wOK.flushCount)
	}
}

func TestInterceptWriter_WriteNotFoundIdempotent(t *testing.T) {
	w := httptest.NewRecorder()
	iw := &interceptWriter{ResponseWriter: w}
	iw.WriteHeader(http.StatusNotFound)
	iw.writeNotFound()
	firstLen := w.Body.Len()
	iw.writeNotFound() // 重复调用应无副作用
	if w.Body.Len() != firstLen {
		t.Errorf("body grew on repeated writeNotFound: %d -> %d", firstLen, w.Body.Len())
	}
}

// --- NewStaticHandler traversal 拒绝(端到端) ---
// httptest.NewRequest 会把 .. 规范化掉;SPA 关闭后,/etc/passwd 不存在 → 404。

func TestNewStaticHandler_RejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	// NewStaticHandler 启动期校验 IndexFile 必须存在,先放个 dummy
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	h, err := NewStaticHandler(StaticConfig{
		Dir:         dir,
		SPAFallback: false, // 关闭 fallback,让 /etc/passwd 拿到 404
	})
	if err != nil {
		t.Fatalf("NewStaticHandler: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/../etc/passwd", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("traversal: status = %d, want 404", w.Code)
	}
}

// --- embed 完整性 sanity check（白盒） ---

func TestNotFoundHTML_Embedded(t *testing.T) {
	if len(notFoundHTML) == 0 {
		t.Fatal("notFoundHTML is empty — //go:embed failed")
	}
	head := string(notFoundHTML[:min(80, len(notFoundHTML))])
	if !strings.Contains(head, "<html") && !strings.Contains(head, "<!DOCTYPE") {
		t.Errorf("notFoundHTML doesn't look like HTML (head=%q)", head)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}