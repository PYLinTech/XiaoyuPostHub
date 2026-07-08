// Package server 提供 XiaoyuPostHub 后端的 HTTP 路由与错误处理能力。
//
// 本文件实现 nginx 简化版静态文件服务:
//
//   - try_files 链式查找:真实文件 → 目录+index.html → SPA fallback
//   - SPA fallback 让 React Router 接管 history 路由
//   - 基于相对路径/size/mtime 的 ETag + Last-Modified + 304 协商
//   - hash 文件 → 30 天 immutable;其他 → no-cache
//   - 路径穿越 / 隐藏文件防护
//   - 仅 GET / HEAD,其他 405
//   - 进程级元信息永久缓存(见 staticFileMeta)
//
// 仅用 stdlib,启动期 fail-fast,不抢 API 路由。
package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// StaticConfig 是 NewStaticHandler 的配置项。
type StaticConfig struct {
	// Dir 是静态文件根目录,必须存在且是目录。
	Dir string

	// IndexFile 是目录请求与 SPA fallback 使用的索引文件名。
	// 留空时使用 "index.html"。
	IndexFile string

	// SPAFallback 控制找不到文件时是否回退到 IndexFile。
	// NewRouter 传 true;直接调用 NewStaticHandler 时由调用方显式决定。
	// 关闭后,所有未命中真实文件的请求返回 404。
	SPAFallback bool

	// ImmutableMaxAge 是命中 ImmutableRegex 的资源缓存秒数。
	// 留空时使用 2592000(30 天)。
	ImmutableMaxAge int

	// ImmutableRegex 命中后写 Cache-Control: immutable。
	// 留空时匹配 webpack/vite 通用 hash 模式:<name>.<hex{6,}>.ext。
	ImmutableRegex *regexp.Regexp

	// DefaultMaxAge 是其他资源的 max-age(秒)。
	// 留空时为 0(no-cache,每次 revalidate 走 304 协商)。
	DefaultMaxAge int
}

// 默认值常量。新增字段时务必同步更新 applyDefaults 与本块。
const (
	defaultIndexFile       = "index.html"
	defaultImmutableMaxAge = 30 * 24 * 60 * 60
)

// defaultImmutableRegex 匹配 webpack/vite 通用 hash 文件名模式:
// main.abc12345.js / chunk.def67890.css / font.123456.woff2。
// index.html 与 api.json 不命中(避免误判)。
var defaultImmutableRegex = regexp.MustCompile(
	`\.[a-f0-9]{6,}\.(js|css|mjs|woff2?|ttf|otf|eot|png|jpe?g|gif|svg|webp|ico|mp4|webm)$`,
)

// staticFileMeta 是进程级缓存的静态文件元信息。
//
// 只缓存元信息(ETag / Cache-Control / Content-Type / mtime / 相对路径),
// 不缓存文件内容——文件内容仍由 os.Open + http.ServeContent 读取,
// 实际页缓存由操作系统 page cache 承担。
//
// 为什么可以永久缓存:本项目前端是预构建产物,后端运行期间静态文件不会被
// 原地替换;前端重新构建 → 重新部署 → 进程重启,缓存自然失效。
// 不需要 TTL / watcher / fsnotify。
type staticFileMeta struct {
	rel          string
	etag         string
	modTime      time.Time
	cacheControl string
	contentType  string
}

// staticHandler 是 NewStaticHandler 返回的私有类型。
type staticHandler struct {
	dir             string
	indexFile       string
	spaFallback     bool
	immutableMaxAge int
	defaultMaxAge   int
	immutableRe     *regexp.Regexp

	// metaCache 进程级永久缓存。key 为文件绝对路径,value 为 staticFileMeta。
	// 不需要失效机制,见 staticFileMeta 注释。
	metaCache sync.Map
}

// NewStaticHandler 构造静态文件 http.Handler。
//
// 启动期校验(失败返回 error):
//  1. Dir 必须存在且是目录
//  2. IndexFile 必须存在(SPA fallback 需要)
//
// 注意:SPAFallback 是普通 bool,零值 false——区分"未传"与"显式 false"做不到。
// 调用方必须显式赋值。NewRouter 传 true,其他场景由调用方决定。
func NewStaticHandler(cfg StaticConfig) (http.Handler, error) {
	applyDefaults(&cfg)

	dir, err := filepath.Abs(cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("static: 解析 Dir 失败: %w", err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("static: Dir %q 不存在: %w", dir, err)
	}
	if !dirInfo.IsDir() {
		return nil, fmt.Errorf("static: Dir %q 不是目录", dir)
	}

	// IndexFile 存在性校验(SPA fallback 必需)
	indexPath := filepath.Join(dir, cfg.IndexFile)
	if _, err := os.Stat(indexPath); err != nil {
		return nil, fmt.Errorf("static: IndexFile %q 不存在(SPA fallback 必需): %w",
			indexPath, err)
	}

	h := &staticHandler{
		dir:             dir,
		indexFile:       cfg.IndexFile,
		spaFallback:     cfg.SPAFallback,
		immutableMaxAge: cfg.ImmutableMaxAge,
		defaultMaxAge:   cfg.DefaultMaxAge,
		immutableRe:     cfg.ImmutableRegex,
	}
	return h, nil
}

// applyDefaults 为零值字段填默认值。SPAFallback 是 bool 零值 false,
// 无法区分"未传"与"显式 false",不在本函数处理,调用方必须显式赋值。
func applyDefaults(cfg *StaticConfig) {
	if cfg.IndexFile == "" {
		cfg.IndexFile = defaultIndexFile
	}
	if cfg.ImmutableMaxAge == 0 {
		cfg.ImmutableMaxAge = defaultImmutableMaxAge
	}
	if cfg.ImmutableRegex == nil {
		cfg.ImmutableRegex = defaultImmutableRegex
	}
	// DefaultMaxAge 零值 = 0,正是目标值(no-cache),不需要默认值。
}

// ---------- 核心 handler ----------

// ServeHTTP 流水线:
//  1. method check(非 GET/HEAD → 405)
//  2. URL 归一化 + 路径穿越 / 隐藏段检测
//  3. try_files:文件 → 目录+index.html
//  4. SPA fallback → 根 index.html
//  5. serveFile:头 + 304 协商 + http.ServeContent
func (h *staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	cleanURL := path.Clean(r.URL.Path)
	if cleanURL == "/" {
		cleanURL = ""
	}

	if h.isPathTraversal(cleanURL) || h.hasHiddenSegment(cleanURL) {
		http.NotFound(w, r)
		return
	}

	full, found := h.tryFiles(cleanURL)
	if !found && h.spaFallback {
		full = filepath.Join(h.dir, h.indexFile)
		// metaCache 已知存在则跳过 stat,让"删文件后 304"场景下整条流水线
		// 不调 os.Stat(只走 metaFor + 304 return)
		if _, ok := h.metaCache.Load(full); ok {
			found = true
		} else if info, err := os.Stat(full); err == nil && !info.IsDir() {
			found = true
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	h.serveFile(w, r, full)
}

// tryFiles 按 nginx try_files 语义查找:文件 → 目录+index.html。
// metaCache 命中跳过 os.Stat(性能 + 让 304 路径不调 stat)。
func (h *staticHandler) tryFiles(cleanURL string) (full string, found bool) {
	abs := filepath.Join(h.dir, cleanURL)
	if _, ok := h.metaCache.Load(abs); ok {
		return abs, true
	}

	info, err := os.Stat(abs)
	if err != nil {
		return "", false
	}
	if !info.IsDir() {
		return abs, true
	}
	idx := filepath.Join(abs, h.indexFile)
	if _, ok := h.metaCache.Load(idx); ok {
		return idx, true
	}
	if info2, err := os.Stat(idx); err == nil && !info2.IsDir() {
		return idx, true
	}
	return "", false
}

// serveFile 关键不变量:
//
//   - full 必须是**最终实际返回**的文件绝对路径(SPA fallback 时是 index.html),
//     因此 ETag 与 Cache-Control 与直接请求 / 或 /index.html 完全一致。
//   - 304 路径不调 os.Open(metaFor → set headers → WriteHeader(304) → return;
//     200 路径才走 os.Open + http.ServeContent)。
func (h *staticHandler) serveFile(w http.ResponseWriter, r *http.Request, full string) {
	meta, err := h.metaFor(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("ETag", meta.etag)
	w.Header().Set("Last-Modified", meta.modTime.UTC().Format(http.TimeFormat))
	w.Header().Set("Cache-Control", meta.cacheControl)
	w.Header().Set("Content-Type", meta.contentType)

	if notModified(r, meta.etag, meta.modTime) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	http.ServeContent(w, r, filepath.Base(full), meta.modTime, f)
}

// ---------- 元信息缓存 ----------

// metaFor 拿文件的缓存元信息;首次计算并 LoadOrStore,后续命中。
// 失败(目录/不存在)不写入缓存。LoadOrStore 保证并发安全 + ETag 稳定。
func (h *staticHandler) metaFor(full string) (staticFileMeta, error) {
	if v, ok := h.metaCache.Load(full); ok {
		return v.(staticFileMeta), nil
	}

	info, err := os.Stat(full)
	if err != nil {
		return staticFileMeta{}, err
	}
	if info.IsDir() {
		return staticFileMeta{}, fmt.Errorf("static: %q is a directory", full)
	}

	rel := h.relativePath(full)
	cacheControl := cacheControlNoCache(h.defaultMaxAge)
	if h.immutableRe.MatchString(rel) {
		cacheControl = fmt.Sprintf("public, max-age=%d, immutable", h.immutableMaxAge)
	}

	meta := staticFileMeta{
		rel:          rel,
		etag:         etagFor(rel, info.Size(), info.ModTime()),
		modTime:      info.ModTime(),
		cacheControl: cacheControl,
		contentType:  contentTypeFor(filepath.Ext(full)),
	}

	actual, _ := h.metaCache.LoadOrStore(full, meta)
	return actual.(staticFileMeta), nil
}

// relativePath 把绝对路径转换为静态根目录内的相对路径。
// 部署目录变化不影响 ETag;反斜杠被 ToSlash 规范化,跨平台一致。
// 兜底 basename:防御性,理论上 filepath.Join 后必在 dir 内。
func (h *staticHandler) relativePath(full string) string {
	rel, err := filepath.Rel(h.dir, full)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return filepath.Base(full)
	}
	return filepath.ToSlash(rel)
}

// ---------- 工具函数 ----------

// isPathTraversal 检测 cleanURL 在拼接到 dir 后是否逃出 dir 边界
// (filepath.Clean + HasPrefix,比字符串 Contains("..") 黑名单精确)。
func (h *staticHandler) isPathTraversal(cleanURL string) bool {
	abs := filepath.Clean(filepath.Join(h.dir, cleanURL))
	root := filepath.Clean(h.dir)
	if abs == root {
		return false
	}
	return !strings.HasPrefix(abs, root+string(os.PathSeparator))
}

// hasHiddenSegment 检测 cleanURL 中是否有任一路径段以 "." 开头且长度 > 1。
// 例:/.git/config 命中,/. 单独段不命中(path 语法)。
func (h *staticHandler) hasHiddenSegment(cleanURL string) bool {
	for _, seg := range strings.Split(cleanURL, "/") {
		if len(seg) > 1 && strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

// etagFor 基于相对路径 + size + mtime 生成 ETag,**不**读完整文件内容。
// 格式 "v1|<relPath>|<size>|<mtime_unix_nano>"(v1 是算法版本前缀,
// 未来 bump 防碰撞;mtime 用 UTC 纳秒避免本地时区影响)。
func etagFor(relPath string, size int64, mtime time.Time) string {
	h := sha256.New()
	fmt.Fprintf(h, "v1|%s|%d|%d", filepath.ToSlash(relPath), size, mtime.UTC().UnixNano())
	sum := h.Sum(nil)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// matchAnyETag 解析 If-None-Match,支持多值、弱 ETag 前缀 W/、通配符 *。
func matchAnyETag(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, raw := range strings.Split(header, ",") {
		e := strings.TrimSpace(raw)
		if e == "*" {
			return true
		}
		e = strings.TrimPrefix(e, "W/")
		if e == etag {
			return true
		}
	}
	return false
}

// notModified 按 RFC 7232 协商:If-None-Match 优先于 If-Modified-Since。
func notModified(r *http.Request, etag string, mtime time.Time) bool {
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		return matchAnyETag(inm, etag)
	}
	if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		t, err := http.ParseTime(ims)
		if err != nil {
			return false
		}
		// Last-Modified 精度 1s,文件 mtime 截断到秒再比
		return mtime.Truncate(time.Second).Unix() <= t.Truncate(time.Second).Unix()
	}
	return false
}

// contentTypeFor 在 stdlib mime.TypeByExtension 基础上:
// 未知扩展名兜底 application/octet-stream;text/* 与 application/javascript 加 charset=utf-8。
func contentTypeFor(ext string) string {
	if ext == "" {
		return "application/octet-stream"
	}
	ct := mime.TypeByExtension(ext)
	if ct == "" {
		return "application/octet-stream"
	}
	if (strings.HasPrefix(ct, "text/") || ct == "application/javascript") &&
		!strings.Contains(ct, "charset=") {
		ct += "; charset=utf-8"
	}
	return ct
}

// cacheControlNoCache:0 / 负数 → no-cache,正数 → public, max-age=<n>。
func cacheControlNoCache(maxAge int) string {
	if maxAge <= 0 {
		return "no-cache"
	}
	return fmt.Sprintf("public, max-age=%d", maxAge)
}