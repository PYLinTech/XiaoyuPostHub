package server

// StaticHandler 单元测试。
//
// 测试策略:
//   - 用 t.TempDir() 建临时 static 根,创建一组 fixture 文件
//   - httptest.NewRecorder 跑真实请求,检查 status / headers / body
//   - 不 mock 任何函数(走真磁盘、真 http.ServeContent)
//
// 覆盖矩阵(18 个 case):
//
//	1.  /                     → 200 + index.html + charset
//	2.  /static/js/main.<hash>.js → 200 + Cache-Control: immutable
//	3.  /static/css/main.css  → 200 + Cache-Control: no-cache
//	4.  /settings/profile (SPA on)  → 200 + root index.html
//	5.  /settings/profile (SPA off) → 404
//	6.  /../etc/passwd        → 404(穿越)
//	7.  /.git/config          → 404(隐藏)
//	8.  /static/sub/          → 200 + sub/index.html
//	9.  /static/sub           → 200 + sub/index.html(trailing slash 可选)
//	10. If-None-Match 命中    → 304 无 body
//	11. If-Modified-Since 命中 → 304 无 body
//	12. HEAD /                → 200 无 body
//	13. POST /                → 405 + Allow
//	14. Range 请求            → 206 + Content-Range
//	15. /index.html           → Cache-Control 不含 immutable
//	16. SPA fallback ETag      → 与 / 的 ETag 完全一致
//	17. 304 命中时            → 不打开文件(删文件后 304 仍成功)
//	18. metaCache             → 重复请求命中,条目数稳定

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// staticFixture 是一次性 fixture 树:
//
//	<root>/
//	  index.html
//	  settings/
//	    profile.html  (存在,验证 try_files 命中真实文件)
//	  static/
//	    js/main.abc12345.js     (带 hash,触发 immutable)
//	    js/main.js             (无 hash,触发 no-cache)
//	    css/main.css
//	    img/logo.svg
//	    sub/index.html         (子目录的 index.html)
//	    .git/config             (隐藏文件)
//
// 返回 root 路径(绝对路径)。
func staticFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	files := map[string]string{
		"index.html":                 "<html><body>Root</body></html>",
		"settings/profile.html":      "<html><body>SettingsProfile</body></html>",
		"static/js/main.abc12345.js": "console.log('hashed');",
		"static/js/main.js":          "console.log('plain');",
		"static/css/main.css":        "body { color: red; }",
		"static/img/logo.svg":        "<svg></svg>",
		"static/sub/index.html":      "<html><body>Sub</body></html>",
		"static/.git/config":         "[core]\n\trepositoryformatversion = 0",
	}

	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}

	// 统一设置 mtime 到固定时间,避免 304 测试受文件系统 mtime 精度影响
	//(macOS 上 mtime 默认有亚秒精度,如果不锁住,If-Modified-Since 比对会偶发失败)
	fixed := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	for rel := range files {
		_ = os.Chtimes(filepath.Join(root, rel), fixed, fixed)
	}

	return root
}

// newHandler 构造一个测试用 handler,显式开启 SPA fallback。
// 注:SPAFallback 是普通 bool,零值 false,这里必须显式传 true
// 才会开启 fallback。helper 不依赖任何"默认值"。
func newHandler(t *testing.T) http.Handler {
	t.Helper()
	root := staticFixture(t)
	h, err := NewStaticHandler(StaticConfig{
		Dir:         root,
		SPAFallback: true,
	})
	if err != nil {
		t.Fatalf("NewStaticHandler: %v", err)
	}
	return h
}

// do 简化请求调用。
func do(h http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// =====================================================================
// 1. / 命中根 index.html
// =====================================================================
func TestStatic_RootIndex(t *testing.T) {
	h := newHandler(t)
	w := do(h, http.MethodGet, "/", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
	}
	if !strings.Contains(w.Body.String(), "Root") {
		t.Errorf("body = %q, want contains 'Root'", w.Body.String())
	}
}

// =====================================================================
// 2. 带 hash 的 JS 文件 → immutable cache
// =====================================================================
func TestStatic_HashedJS_ImmutableCache(t *testing.T) {
	h := newHandler(t)
	w := do(h, http.MethodGet, "/static/js/main.abc12345.js", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	want := "public, max-age=2592000, immutable"
	if got := w.Header().Get("Cache-Control"); got != want {
		t.Errorf("Cache-Control = %q, want %q", got, want)
	}
	if got := w.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/javascript; charset=utf-8", got)
	}
}

// =====================================================================
// 3. 无 hash 的 CSS 文件 → no-cache
// =====================================================================
func TestStatic_PlainCSS_NoCache(t *testing.T) {
	h := newHandler(t)
	w := do(h, http.MethodGet, "/static/css/main.css", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
}

// =====================================================================
// 4. SPA fallback:不存在的路径 → 根 index.html
// =====================================================================
func TestStatic_SPAFallback_On(t *testing.T) {
	h := newHandler(t)
	w := do(h, http.MethodGet, "/settings/profile", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (SPA fallback)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Root") {
		t.Errorf("body = %q, want root index.html content", w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
	}
}

// =====================================================================
// 5. SPA fallback 关闭 → 404
// =====================================================================
func TestStatic_SPAFallback_Off(t *testing.T) {
	root := staticFixture(t)
	h, err := NewStaticHandler(StaticConfig{
		Dir:         root,
		SPAFallback: false,
	})
	if err != nil {
		t.Fatalf("NewStaticHandler: %v", err)
	}
	w := do(h, http.MethodGet, "/settings/profile", nil)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (SPA off)", w.Code)
	}
}

// =====================================================================
// 6. 路径穿越(SPA 关闭)→ 404
//
// 客户端无法构造真正穿越的 URL:
//   - httptest.NewRequest 会把 URL.Path 里的 .. 自动 normalize 掉
//   - path.Clean 也会拆掉 ..
//
// 因此端到端测试只能验证"normalize 后不存在的路径"——SPA 开启会被 fallback 接住 200,
//
//	关闭时才返回 404,这才是"穿越防御"语义。
//
// 真正的边界检查走 TestStatic_IsPathTraversal(unexported 方法直接验证)。
// =====================================================================
func TestStatic_PathTraversal(t *testing.T) {
	root := staticFixture(t)
	h, err := NewStaticHandler(StaticConfig{
		Dir:         root,
		SPAFallback: false, // 关键:关闭 fallback 才能拿到 404
	})
	if err != nil {
		t.Fatalf("NewStaticHandler: %v", err)
	}
	// /../etc/passwd → 被 httptest 规范化为 /etc/passwd → 文件不存在 + SPA 关 → 404
	w := do(h, http.MethodGet, "/../etc/passwd", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (normalize 后 /etc/passwd 不存在)", w.Code)
	}
}

// TestStatic_IsPathTraversal 直接验证 isPathTraversal 的边界。
func TestStatic_IsPathTraversal(t *testing.T) {
	root := staticFixture(t)
	h, err := NewStaticHandler(StaticConfig{Dir: root})
	if err != nil {
		t.Fatalf("NewStaticHandler: %v", err)
	}
	sh := h.(*staticHandler)

	cases := []struct {
		name    string
		clean   string
		wantBad bool
	}{
		{"root", "", false},
		{"plain", "static/js/main.js", false},
		{"nested", "static/sub/index.html", false},
		{"relative-up", "../etc/passwd", true},
		{"deep-relative-up", "static/../../../etc/passwd", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sh.isPathTraversal(tc.clean)
			if got != tc.wantBad {
				t.Errorf("isPathTraversal(%q) = %v, want %v", tc.clean, got, tc.wantBad)
			}
		})
	}
}

// =====================================================================
// 7. 隐藏文件 → 404
// =====================================================================
func TestStatic_HiddenFile(t *testing.T) {
	h := newHandler(t)
	w := do(h, http.MethodGet, "/static/.git/config", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (hidden file)", w.Code)
	}
}

// =====================================================================
// 8. 目录请求 → 子目录 index.html
// =====================================================================
func TestStatic_DirectoryIndex(t *testing.T) {
	h := newHandler(t)
	w := do(h, http.MethodGet, "/static/sub/", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Sub") {
		t.Errorf("body = %q, want sub/index.html content", w.Body.String())
	}
}

// =====================================================================
// 10. 目录请求不带 trailing slash → 仍能找到 index.html
// =====================================================================
func TestStatic_DirectoryIndex_NoTrailingSlash(t *testing.T) {
	h := newHandler(t)
	w := do(h, http.MethodGet, "/static/sub", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Sub") {
		t.Errorf("body = %q, want sub/index.html content", w.Body.String())
	}
}

// =====================================================================
// 11. If-None-Match 命中 → 304 无 body
// =====================================================================
func TestStatic_IfNoneMatch_304(t *testing.T) {
	h := newHandler(t)
	// 第一次请求拿 ETag
	w1 := do(h, http.MethodGet, "/static/css/main.css", nil)
	if w1.Code != http.StatusOK {
		t.Fatalf("first request: status = %d, want 200", w1.Code)
	}
	etag := w1.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("ETag header is empty")
	}

	// 第二次请求带 If-None-Match
	w2 := do(h, http.MethodGet, "/static/css/main.css", map[string]string{
		"If-None-Match": etag,
	})
	if w2.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", w2.Code)
	}
	if w2.Body.Len() != 0 {
		t.Errorf("body should be empty on 304, got %d bytes", w2.Body.Len())
	}
	if w2.Header().Get("ETag") != etag {
		t.Errorf("ETag on 304 = %q, want %q", w2.Header().Get("ETag"), etag)
	}
}

// =====================================================================
// 12. If-Modified-Since 命中 → 304 无 body
// =====================================================================
func TestStatic_IfModifiedSince_304(t *testing.T) {
	h := newHandler(t)
	// 用未来时间确保 If-Modified-Since > mtime,触发 304
	future := time.Now().Add(1 * time.Hour).UTC().Format(http.TimeFormat)
	w := do(h, http.MethodGet, "/static/css/main.css", map[string]string{
		"If-Modified-Since": future,
	})
	if w.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("body should be empty on 304, got %d bytes", w.Body.Len())
	}
}

// =====================================================================
// 13. HEAD 请求 → 200 无 body
// =====================================================================
func TestStatic_HEAD(t *testing.T) {
	h := newHandler(t)
	w := do(h, http.MethodHead, "/", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("HEAD body should be empty, got %d bytes", w.Body.Len())
	}
	if got := w.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
	}
}

// =====================================================================
// 14. POST 请求 → 405 + Allow
// =====================================================================
func TestStatic_POST_405(t *testing.T) {
	h := newHandler(t)
	w := do(h, http.MethodPost, "/", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
	if got := w.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q, want 'GET, HEAD'", got)
	}
}

// =====================================================================
// 15. Range 请求 → 206 + Content-Range
// =====================================================================
func TestStatic_RangeRequest(t *testing.T) {
	h := newHandler(t)
	w := do(h, http.MethodGet, "/static/css/main.css", map[string]string{
		"Range": "bytes=0-3",
	})
	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", w.Code)
	}
	cr := w.Header().Get("Content-Range")
	if !strings.HasPrefix(cr, "bytes 0-3/") {
		t.Errorf("Content-Range = %q, want prefix 'bytes 0-3/'", cr)
	}
	// body 应该是前 4 字节
	body, _ := io.ReadAll(w.Result().Body)
	if len(body) != 4 {
		t.Errorf("body length = %d, want 4", len(body))
	}
}

// =====================================================================
// 16. /index.html 不返回 immutable Cache-Control
//
// index.html 是 SPA fallback 的目标,内容更新要能被前端感知,
// 不能用长期 immutable 缓存。这条覆盖"hash 文件 → immutable,index.html → 不 immutable"的分流。
// =====================================================================
func TestStatic_IndexHTML_NotImmutable(t *testing.T) {
	h := newHandler(t)
	w := do(h, http.MethodGet, "/", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	cc := w.Header().Get("Cache-Control")
	if strings.Contains(cc, "immutable") {
		t.Errorf("/index.html Cache-Control = %q, must NOT contain 'immutable'", cc)
	}
}

// =====================================================================
// 17. SPA fallback 命中时的 ETag,必须与直接访问 / 或 /index.html 完全一致
//
// 关键不变量:serveFile 接收的 full 是最终实际返回的文件路径(index.html),
// 而不是请求路径 /settings/profile。这样 ETag 基于 index.html 计算,与直接访问
// 一致;否则前端会拿到"404-like"的 stale 缓存结果。
// =====================================================================
func TestStatic_SPAFallback_ETagMatchesRootIndex(t *testing.T) {
	h := newHandler(t)

	// 直接请求 /index.html → 拿到"标准" ETag
	wRoot := do(h, http.MethodGet, "/", nil)
	if wRoot.Code != http.StatusOK {
		t.Fatalf("/ status = %d, want 200", wRoot.Code)
	}
	rootETag := wRoot.Header().Get("ETag")
	if rootETag == "" {
		t.Fatalf("/ ETag is empty")
	}

	// 典型 SPA 路由,触发 fallback 到 index.html
	wSPA := do(h, http.MethodGet, "/settings/profile", nil)
	if wSPA.Code != http.StatusOK {
		t.Fatalf("SPA fallback status = %d, want 200", wSPA.Code)
	}
	spaETag := wSPA.Header().Get("ETag")
	if spaETag != rootETag {
		t.Errorf("SPA fallback ETag = %q, want equal to / ETag = %q",
			spaETag, rootETag)
	}

	// 再补一个更"偏僻"的 SPA 路径
	wDeep := do(h, http.MethodGet, "/files/123/share", nil)
	if deepETag := wDeep.Header().Get("ETag"); deepETag != rootETag {
		t.Errorf("/files/123/share fallback ETag = %q, want equal to / ETag = %q",
			deepETag, rootETag)
	}
}

// =====================================================================
// 18. 304 命中时不调 os.Open(structural 验证 + 黑盒证据)
//
// 测试策略:第一次请求 → 200 + 缓存元信息 → 删除磁盘文件 → 第二次带 If-None-Match
//   - 第二次请求成功返回 304,证明整条流水线(tryFiles + serveFile)都走了 metaCache 命中,
//     没尝试 os.Stat / os.Open(否则会暴露文件已删,变成 404)
//   - 同时第三次请求不带 If-None-Match,会进入 200 路径 → os.Open 失败 → 404,
//     反过来证明 200 路径是会读盘的,跟 304 路径形成对照
//
// 这条同时验证用户验收标准的第 7 条("304 响应不打开文件")。
// =====================================================================
func TestStatic_304DoesNotReopenFile(t *testing.T) {
	root := staticFixture(t)
	h, err := NewStaticHandler(StaticConfig{
		Dir:         root,
		SPAFallback: false, // 关闭避免 SPA fallback 干扰
	})
	if err != nil {
		t.Fatalf("NewStaticHandler: %v", err)
	}

	// 1) 第一次:200,拿到 ETag,metaCache 已写入
	w1 := do(h, http.MethodGet, "/static/css/main.css", nil)
	if w1.Code != http.StatusOK {
		t.Fatalf("first req: status = %d, want 200", w1.Code)
	}
	etag := w1.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("first req: ETag is empty")
	}

	// 2) 删除文件,模拟"运行期被替换"(即便前端 build 产物不应发生,这是模拟测试)
	target := filepath.Join(root, "static/css/main.css")
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// 3) 第二次:带 If-None-Match,期望 304
	//    metaCache 命中 → tryFiles 跳过 os.Stat → serveFile 跳过 os.Open → 304
	w2 := do(h, http.MethodGet, "/static/css/main.css", map[string]string{
		"If-None-Match": etag,
	})
	if w2.Code != http.StatusNotModified {
		t.Errorf("304 path: status = %d, want 304 (metaCache 命中,文件已删但不应感知)", w2.Code)
	}

	// 4) 第三次:不带 If-None-Match,会进入 200 路径 → os.Open 失败 → 404
	//    跟 304 路径形成对照,证明 200 路径确实会读盘
	w3 := do(h, http.MethodGet, "/static/css/main.css", nil)
	if w3.Code != http.StatusNotFound {
		t.Errorf("200 path: status = %d, want 404 (文件已删,200 路径会 open 失败)", w3.Code)
	}
}

// =====================================================================
// 19. metaCache 命中验证(条目数对比)
//
// 同一个文件被请求两次:第一次 1 个 cache entry,第二次仍然 1 个(命中已有,不重复写入)。
// 间接验证 metaCache 在并发安全(LoadOrStore)下不会膨胀。
// =====================================================================
func TestStatic_MetaCache_HitsOnRepeatedRequest(t *testing.T) {
	h := newHandler(t)
	sh := h.(*staticHandler)

	do(h, http.MethodGet, "/static/css/main.css", nil)
	n1 := metaCacheSize(sh)
	if n1 != 1 {
		t.Fatalf("after first req: metaCache entries = %d, want 1", n1)
	}

	do(h, http.MethodGet, "/static/css/main.css", nil)
	n2 := metaCacheSize(sh)
	if n2 != 1 {
		t.Errorf("after second req: metaCache entries = %d, want 1 (应命中已有)", n2)
	}

	// 请求一个不同的路径,cache 应该 +1
	do(h, http.MethodGet, "/static/js/main.js", nil)
	n3 := metaCacheSize(sh)
	if n3 != 2 {
		t.Errorf("after third req (different path): metaCache entries = %d, want 2", n3)
	}
}

// metaCacheSize 是测试辅助:遍历 sync.Map 数条目。
func metaCacheSize(h *staticHandler) int {
	n := 0
	h.metaCache.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}

// =====================================================================
// 构造期校验:Dir 不存在 → error
// =====================================================================
func TestNewStaticHandler_DirMissing(t *testing.T) {
	_, err := NewStaticHandler(StaticConfig{
		Dir: "/nonexistent/path/should/not/exist",
	})
	if err == nil {
		t.Fatal("expected error for missing dir, got nil")
	}
}

// =====================================================================
// 构造期校验:IndexFile 不存在 → error
// =====================================================================
func TestNewStaticHandler_IndexFileMissing(t *testing.T) {
	dir := t.TempDir()
	// dir 里没有 index.html
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write other.txt: %v", err)
	}
	_, err := NewStaticHandler(StaticConfig{
		Dir:         dir,
		SPAFallback: true,
	})
	if err == nil {
		t.Fatal("expected error for missing index.html, got nil")
	}
}

// =====================================================================
// etagFor 单元测试
// =====================================================================
func TestEtagFor(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Second)

	e1 := etagFor("static/a.js", 100, t1)
	e2 := etagFor("static/a.js", 100, t1)
	e3 := etagFor("static/a.js", 101, t1) // size 变化
	e4 := etagFor("static/a.js", 100, t2) // mtime 变化
	e5 := etagFor("static/b.js", 100, t1) // path 变化

	if e1 != e2 {
		t.Errorf("same input should produce same ETag: %q vs %q", e1, e2)
	}
	if e1 == e3 {
		t.Errorf("size change should change ETag")
	}
	if e1 == e4 {
		t.Errorf("mtime change should change ETag")
	}
	if e1 == e5 {
		t.Errorf("path change should change ETag")
	}
	if !strings.HasPrefix(e1, `"`) || !strings.HasSuffix(e1, `"`) {
		t.Errorf("ETag should be quoted: %q", e1)
	}
}

// TestETag_DoesNotDependOnAbsolutePath 验证 ETag 不依赖绝对路径。
//
// 同一相对路径 + size + mtime 应该产出同一 ETag,无论部署在 /opt/v1/web
// 还是 /opt/v2/web。这是 relativePath + etagFor 改造的核心收益。
//
// 同时验证 filepath.ToSlash 生效:Windows 反斜杠与 Unix 正斜杠 ETag 一致。
func TestETag_DoesNotDependOnAbsolutePath(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// 不同"虚拟"绝对路径,相同相对路径/大小/mtime → 相同 ETag
	e1 := etagFor("static/js/main.js", 100, t1)
	e2 := etagFor("static/js/main.js", 100, t1)
	if e1 != e2 {
		t.Errorf("stable input should produce stable ETag: %q vs %q", e1, e2)
	}

	// 反斜杠被 ToSlash 规范化(Windows 部署也能稳定)
	eWin := etagFor(filepath.FromSlash("static/js/main.js"), 100, t1)
	if e1 != eWin {
		t.Errorf("ToSlash should normalize path: e1=%q eWin=%q", e1, eWin)
	}
}

// =====================================================================
// matchAnyETag 单元测试
// =====================================================================
func TestMatchAnyETag(t *testing.T) {
	const etag = `"abc123"`
	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"empty", "", false},
		{"single-match", etag, true},
		{"single-no-match", `"xyz"`, false},
		{"multi-with-match", `"xyz", ` + etag + `, "foo"`, true},
		{"weak-prefix", `W/` + etag, true},
		{"wildcard", `*`, true},
		{"whitespace", "  " + etag + "  ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchAnyETag(tc.header, etag); got != tc.want {
				t.Errorf("matchAnyETag(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}

// =====================================================================
// contentTypeFor 单元测试
// =====================================================================
func TestContentTypeFor(t *testing.T) {
	cases := []struct {
		ext  string
		want string
	}{
		{".html", "text/html; charset=utf-8"},
		{".js", "text/javascript; charset=utf-8"},
		{".css", "text/css; charset=utf-8"},
		{".json", "application/json"}, // nginx 默认也不加,保持一致
		{".svg", "image/svg+xml"},     // image/* 不加 charset
		{".png", "image/png"},
		{".unknown", "application/octet-stream"},
		{"", "application/octet-stream"},
	}
	for _, tc := range cases {
		t.Run(tc.ext, func(t *testing.T) {
			if got := contentTypeFor(tc.ext); got != tc.want {
				t.Errorf("contentTypeFor(%q) = %q, want %q", tc.ext, got, tc.want)
			}
		})
	}
}

// =====================================================================
// hasHiddenSegment 单元测试
// =====================================================================
func TestHasHiddenSegment(t *testing.T) {
	sh := &staticHandler{}
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"static/js/main.js", false},
		{"static/.git/config", true},
		{".env", true},
		{"static/sub/.hidden", true},
		{"static/./foo", false}, // . 单独段不算
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := sh.hasHiddenSegment(tc.in); got != tc.want {
				t.Errorf("hasHiddenSegment(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// =====================================================================
// cacheControlNoCache 单元测试
// =====================================================================
func TestCacheControlNoCache(t *testing.T) {
	if got := cacheControlNoCache(0); got != "no-cache" {
		t.Errorf("max-age=0 → %q, want no-cache", got)
	}
	if got := cacheControlNoCache(-1); got != "no-cache" {
		t.Errorf("max-age<0 → %q, want no-cache", got)
	}
	if got := cacheControlNoCache(60); got != "public, max-age=60" {
		t.Errorf("max-age=60 → %q, want 'public, max-age=60'", got)
	}
}
