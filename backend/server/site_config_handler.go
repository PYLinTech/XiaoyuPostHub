package server

import (
	"bytes"
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
	siteIconRelativePath = "custom/site-icon"
	maxSiteIconBytes     = 2 << 20
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
			"status":      "ok",
			"siteName":    settings.SiteName,
			"siteIconUrl": iconURL,
		})
	}
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

	r.Body = http.MaxBytesReader(w, r.Body, maxSiteIconBytes+(1<<20))
	if err := r.ParseMultipartForm(maxSiteIconBytes + (1 << 20)); err != nil {
		writeBusinessError(w, http.StatusBadRequest, "图标上传请求无效或文件过大")
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
	data, err := io.ReadAll(io.LimitReader(file, maxSiteIconBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxSiteIconBytes {
		writeBusinessError(w, http.StatusBadRequest, "站点图标不能为空且不能超过 2MB")
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
