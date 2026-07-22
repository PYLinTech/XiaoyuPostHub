// 黑盒测试：通过 HTTP 接口验证 server 包对外行为，不访问未导出符号。
package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PYLinTech/XiaoyuPostHub/backend/server"
	"github.com/PYLinTech/XiaoyuPostHub/backend/session"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

// newTestServer 构造一个完整 router + 含 index.html 的 static 目录。
//
// 黑盒测试只覆盖路由分流与错误页，用非 nil 空 Repo 满足构造期依赖校验。
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>home</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := server.NewRouter(dir, server.Deps{
		UserRepo:     &user.Repo{},
		SessionRepo:  &session.Repo{},
		CookieSecure: true,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return httptest.NewServer(h)
}

// --- NewRouter：路由分流 ---

func TestNewRouter_RootRedirectsToLoginWithoutCustomHomepage(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 302", resp.StatusCode)
	}
	if location := resp.Header.Get("Location"); location != "/login" {
		t.Errorf("Location = %q, want /login", location)
	}
}

// TestNewRouter_StaticMissingFallsBackToIndex 验证 SPA fallback:
// GET /settings/profile(前端路由)→ 200 + 根 index.html,React Router 接管。
// SPA 部署核心契约:后端只兜底首页,不区分"路径是否存在"。
func TestNewRouter_StaticMissingFallsBackToIndex(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/settings/profile")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (SPA fallback)", resp.StatusCode)
	}
	if string(body) != "<h1>home</h1>" {
		t.Errorf("body = %q, want root index.html", string(body))
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type = %q, want text/html", resp.Header.Get("Content-Type"))
	}
}

func TestNewRouter_APIHealth(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Errorf("body = %q", string(body))
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestNewRouter_APIUnknownReturnsJSON(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/unknown")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if !strings.Contains(string(body), `"status":"error"`) {
		t.Errorf("body doesn't look like JSON API error: %q", string(body[:min(80, len(body))]))
	}
}

// --- WithErrorPage：覆盖任意 handler 的错误状态 ---

func TestWithErrorPage_ReplacesBodyForCustomErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/forbidden", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	})
	mux.HandleFunc("/bad-gateway", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream dead", http.StatusBadGateway)
	})
	mux.HandleFunc("/teapot", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Tea", "Earl Grey")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("original body that should be discarded"))
	})

	srv := httptest.NewServer(server.WithErrorPage(mux))
	defer srv.Close()

	cases := []struct {
		path       string
		wantStatus int
	}{
		{"/forbidden", http.StatusForbidden},
		{"/bad-gateway", http.StatusBadGateway},
		{"/teapot", http.StatusTeapot},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if !strings.HasPrefix(string(body), "<!DOCTYPE html>") && !strings.HasPrefix(string(body), "<!doctype html>") {
				t.Errorf("body doesn't look like embedded 404 page")
			}
		})
	}
}

func TestWithErrorPage_PanicBecomes500With404Page(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/panic", func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	mux.HandleFunc("/panic-after-403", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		panic("kaboom after 403")
	})

	srv := httptest.NewServer(server.WithErrorPage(mux))
	defer srv.Close()

	t.Run("plain panic", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/panic")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", resp.StatusCode)
		}
		if !strings.HasPrefix(string(body), "<!DOCTYPE html>") && !strings.HasPrefix(string(body), "<!doctype html>") {
			t.Errorf("body doesn't look like embedded 404 page")
		}
	})

	t.Run("panic after error status keeps status", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/panic-after-403")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
		if !strings.HasPrefix(string(body), "<!DOCTYPE html>") && !strings.HasPrefix(string(body), "<!doctype html>") {
			t.Errorf("body doesn't look like embedded 404 page")
		}
	})
}

// --- 大文件下载场景（黑盒关键验证） ---

func TestWithErrorPage_SuccessBodyUntouched(t *testing.T) {
	const chunk = "abcdefghij"
	wantBody := strings.Repeat(chunk, 1000)

	mux := http.NewServeMux()
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 1000; i++ {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
	})

	srv := httptest.NewServer(server.WithErrorPage(mux))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/big")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if string(body) != wantBody {
		t.Errorf("body length = %d, want %d", len(body), len(wantBody))
	}
	if resp.Header.Get("X-Test") != "yes" {
		t.Errorf("X-Test header lost: %q", resp.Header.Get("X-Test"))
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/octet-stream") {
		t.Errorf("Content-Type = %q", resp.Header.Get("Content-Type"))
	}
}

func TestWithErrorPage_FlushPassthroughOnSuccess(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/stream", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("ok"))
	})

	srv := httptest.NewServer(server.WithErrorPage(mux))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q, want 'ok'", string(body))
	}
	if resp.Header.Get("X-Test") != "yes" {
		t.Errorf("header lost")
	}
}

func TestWithErrorPage_ErrorBodyDiscardedDuringStream(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/stream-err", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		for i := 0; i < 1000; i++ {
			_, _ = w.Write([]byte("should be discarded "))
		}
	})

	srv := httptest.NewServer(server.WithErrorPage(mux))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/stream-err")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
	if !strings.HasPrefix(string(body), "<!DOCTYPE html>") && !strings.HasPrefix(string(body), "<!doctype html>") {
		t.Errorf("body doesn't look like embedded 404 page (got %d bytes)", len(body))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
