package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

const (
	siteIconRelativePath   = "custom/site-icon"
	customHomeRelativePath = "custom/homepage.html"
)

func siteConfigHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		settings, err := deps.SystemSettings.Get(r.Context())
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取站点配置失败")
			return
		}
		iconURL := currentSiteIconURL(settings.StoragePath)
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{
			"status":                   "ok",
			"siteName":                 settings.SiteName,
			"siteIconUrl":              iconURL,
			"pickupMaxLifetimeSeconds": nullableInt64(settings.PickupMaxLifetimeSeconds),
			"pickupCodeLength":         settings.PickupLength,
		})
	}
}

// homePageHandler 只接管精确的根路径；其他前端路由仍交给 React SPA。
func homePageHandler(deps Deps, fallback http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			fallback.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if deps.SystemSettings == nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		settings, err := deps.SystemSettings.Get(r.Context())
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		filePath := customHomepagePath(settings.StoragePath)
		file, err := os.Open(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || info.IsDir() {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, "homepage.html", info.ModTime(), file)
	})
}

func siteIconHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		settings, err := deps.SystemSettings.Get(r.Context())
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取站点配置失败")
			return
		}
		path := siteIconPath(settings.StoragePath)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				http.NotFound(w, r)
				return
			}
			writeBusinessError(w, http.StatusInternalServerError, "读取站点图标失败")
			return
		}
		contentType, ok := siteIconContentType(data)
		if !ok {
			writeBusinessError(w, http.StatusUnsupportedMediaType, "站点图标格式无效")
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
	}
}

func handleAdminSiteIcon(w http.ResponseWriter, r *http.Request, deps Deps, u user.User) {
	settings, err := deps.SystemSettings.Get(r.Context())
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取存储配置失败")
		return
	}
	path := siteIconPath(settings.StoragePath)
	if r.Method == http.MethodDelete {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			writeBusinessError(w, http.StatusInternalServerError, "移除站点图标失败")
			return
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), u.ID, u.Username, "site_icon.delete", "system_settings", "站点图标", map[string]any{}, net.ParseIP(clientIP(r)))
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "siteIconUrl": ""})
		return
	}
	if r.Method != http.MethodPost {
		writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeBusinessError(w, http.StatusBadRequest, "图标上传请求无效")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, _, err := r.FormFile("icon")
	if err != nil {
		writeBusinessError(w, http.StatusBadRequest, "请选择站点图标")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil || len(data) == 0 {
		writeBusinessError(w, http.StatusBadRequest, "站点图标不能为空")
		return
	}
	if _, ok := siteIconContentType(data); !ok {
		writeBusinessError(w, http.StatusUnsupportedMediaType, "仅支持 SVG、PNG、JPEG 和 WebP 图标")
		return
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "创建自定义资源目录失败")
		return
	}
	tmp, err := os.CreateTemp(dir, ".site-icon-*")
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "保存站点图标失败")
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0o640); err == nil {
		_, err = tmp.Write(data)
	}
	closeErr := tmp.Close()
	if err != nil || closeErr != nil {
		writeBusinessError(w, http.StatusInternalServerError, "保存站点图标失败")
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "替换站点图标失败")
		return
	}
	info, _ := os.Stat(path)
	iconURL := "/api/site-icon"
	if info != nil {
		iconURL = fmt.Sprintf("/api/site-icon?v=%d", info.ModTime().UnixNano())
	}
	_ = deps.AdminRepo.WriteAudit(r.Context(), u.ID, u.Username, "site_icon.update", "system_settings", "站点图标", map[string]any{"path": siteIconRelativePath}, net.ParseIP(clientIP(r)))
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "siteIconUrl": iconURL})
}

func handleAdminHomepage(w http.ResponseWriter, r *http.Request, deps Deps, u user.User) {
	settings, err := deps.SystemSettings.Get(r.Context())
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取存储配置失败")
		return
	}
	path := customHomepagePath(settings.StoragePath)
	if r.Method == http.MethodGet {
		data, readErr := os.ReadFile(path)
		if readErr != nil && !os.IsNotExist(readErr) {
			writeBusinessError(w, http.StatusInternalServerError, "读取自定义首页失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok", "customHomepageConfigured": readErr == nil, "html": string(data),
		})
		return
	}
	if r.Method == http.MethodDelete {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			writeBusinessError(w, http.StatusInternalServerError, "移除自定义首页失败")
			return
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), u.ID, u.Username, "homepage.delete", "system_settings", "自定义首页", map[string]any{}, net.ParseIP(clientIP(r)))
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "customHomepageConfigured": false})
		return
	}
	if r.Method != http.MethodPost {
		writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeBusinessError(w, http.StatusUnsupportedMediaType, "自定义首页只接受 HTML 文本")
		return
	}
	var req struct {
		HTML string `json:"html"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.HTML) == "" {
		writeBusinessError(w, http.StatusBadRequest, "首页 HTML 不能为空")
		return
	}
	if err := writeCustomFile(path, []byte(req.HTML), ".homepage-*"); err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "保存自定义首页失败")
		return
	}
	_ = deps.AdminRepo.WriteAudit(r.Context(), u.ID, u.Username, "homepage.update", "system_settings", "自定义首页", map[string]any{"path": customHomeRelativePath, "source": "editor"}, net.ParseIP(clientIP(r)))
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "customHomepageConfigured": true})
}

func customHomepagePath(storagePath string) string {
	return filepath.Join(filepath.Clean(storagePath), filepath.FromSlash(customHomeRelativePath))
}

func customHomepageConfigured(storagePath string) bool {
	info, err := os.Stat(customHomepagePath(storagePath))
	return err == nil && !info.IsDir()
}

func writeCustomFile(path string, data []byte, pattern string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0o640); err == nil {
		_, err = tmp.Write(data)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func siteIconPath(storagePath string) string {
	return filepath.Join(filepath.Clean(storagePath), filepath.FromSlash(siteIconRelativePath))
}

func currentSiteIconURL(storagePath string) string {
	info, err := os.Stat(siteIconPath(storagePath))
	if err != nil || info.IsDir() {
		return ""
	}
	return fmt.Sprintf("/api/site-icon?v=%d", info.ModTime().UnixNano())
}

func siteIconContentType(data []byte) (string, bool) {
	trimmed := bytes.TrimSpace(data)
	lower := bytes.ToLower(trimmed)
	if bytes.HasPrefix(lower, []byte("<svg")) || (bytes.HasPrefix(lower, []byte("<?xml")) && bytes.Contains(lower, []byte("<svg"))) {
		unsafe := []string{"<script", "javascript:", "onload=", "onerror="}
		text := string(lower)
		for _, marker := range unsafe {
			if strings.Contains(text, marker) {
				return "", false
			}
		}
		return "image/svg+xml; charset=utf-8", true
	}
	contentType := http.DetectContentType(data)
	switch contentType {
	case "image/png", "image/jpeg", "image/webp":
		return contentType, true
	default:
		return "", false
	}
}
