package server

import (
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
	"github.com/jackc/pgx/v5/pgconn"
)

type folderRequest struct {
	Name     string  `json:"name"`
	ParentID *string `json:"parentId"`
}

type ownedDownloadRequest struct {
	ResourceIDs []string `json:"resourceIds"`
}

type renameResourceRequest struct {
	Name string `json:"name"`
}

func folderHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		u, ok := requireUser(w, r, deps)
		if !ok {
			return
		}
		if !u.HasPermission(permission.Upload) {
			writeBusinessError(w, http.StatusForbidden, "没有创建文件夹权限")
			return
		}
		var req folderRequest
		if err := decodeJSON(w, r, &req); err != nil {
			writeBusinessError(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		item, err := deps.ResourceRepo.CreateFolder(r.Context(), u.ID, normalizeID(req.ParentID), req.Name)
		if err != nil {
			writeResourceMutationError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"status": "ok", "resource": item})
	}
}

func resourceListHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			serveOwnedResourcesDownload(w, r, deps)
			return
		}
		if r.Method != http.MethodGet {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		u, ok := requireUser(w, r, deps)
		if !ok {
			return
		}
		var parentID *string
		if raw := strings.TrimSpace(r.URL.Query().Get("parentId")); raw != "" {
			parentID = &raw
			parent, err := deps.ResourceRepo.GetOwned(r.Context(), u.ID, raw)
			if err != nil || parent.Kind != resource.KindFolder {
				writeBusinessError(w, http.StatusNotFound, "文件夹不存在")
				return
			}
		}
		items, err := deps.ResourceRepo.ListChildren(r.Context(), u.ID, parentID)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取目录失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "items": items})
	}
}

// serveOwnedResourcesDownload 将所有者选择的单个文件直接返回；多文件或文件夹
// 统一映射到临时虚拟目录后打包，原资源的位置和层级不会被修改。
func serveOwnedResourcesDownload(w http.ResponseWriter, r *http.Request, deps Deps) {
	u, ok := requireUser(w, r, deps)
	if !ok {
		return
	}
	if !u.HasPermission(permission.Download) {
		writeBusinessError(w, http.StatusForbidden, "没有下载资源权限")
		return
	}
	var req ownedDownloadRequest
	if err := decodeSmallJSON(w, r, &req); err != nil {
		writeBusinessError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	resourceIDs := normalizeResourceIDs(req.ResourceIDs)
	if len(resourceIDs) == 0 || len(resourceIDs) > 100 {
		writeBusinessError(w, http.StatusBadRequest, "请选择 1 至 100 项内容")
		return
	}
	items := make([]resource.Resource, 0, len(resourceIDs))
	for _, resourceID := range resourceIDs {
		item, err := deps.ResourceRepo.GetOwned(r.Context(), u.ID, resourceID)
		if err != nil {
			writeBusinessError(w, http.StatusNotFound, "部分资源不存在")
			return
		}
		if !resourceTreeApproved(w, r, deps, item) {
			return
		}
		items = append(items, item)
	}
	if len(items) == 1 && items[0].Kind == resource.KindFile {
		serveOwnedFile(w, r, deps, items[0])
		return
	}
	virtualRoot := resource.Resource{
		ID: fmt.Sprintf("owner-download-%d", time.Now().UnixNano()), OwnerUserID: u.ID,
		Kind: resource.KindFolder, Name: "下载文件", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	tree, err := buildResourceSelectionTree(r, deps, items, virtualRoot)
	if err != nil {
		writeDownloadPreparationError(w, err)
		return
	}
	path, size, err := deps.FileStore.BuildZip(r.Context(), tree)
	if path != "" {
		defer os.Remove(path) //nolint:errcheck
	}
	if err != nil {
		writeDownloadPreparationError(w, err)
		return
	}
	serveDownload(w, r, path, size, tree[0].Name+".zip", "application/zip")
}

func resourceItemHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pathParts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/resources/"), "/"), "/")
		if len(pathParts) == 2 && pathParts[1] == "preview" {
			serveResourcePreview(w, r, deps, pathParts[0])
			return
		}
		if len(pathParts) != 1 {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		u, ok := requireUser(w, r, deps)
		if !ok {
			return
		}
		id := strings.TrimSpace(pathParts[0])
		if id == "" {
			writeBusinessError(w, http.StatusBadRequest, "资源编号无效")
			return
		}
		if r.Method == http.MethodPut {
			if !u.HasPermission(permission.Rename) {
				writeBusinessError(w, http.StatusForbidden, "没有重命名资源权限")
				return
			}
			var req renameResourceRequest
			if err := decodeSmallJSON(w, r, &req); err != nil {
				writeBusinessError(w, http.StatusBadRequest, "请求格式错误")
				return
			}
			item, err := deps.ResourceRepo.RenameOwned(r.Context(), u.ID, id, req.Name)
			if errors.Is(err, resource.ErrNotFound) {
				writeBusinessError(w, http.StatusNotFound, "资源不存在")
				return
			}
			if err != nil {
				writeResourceMutationError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "resource": item})
			return
		}
		if r.Method != http.MethodDelete {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !u.HasPermission(permission.DeleteOwn) {
			writeBusinessError(w, http.StatusForbidden, "没有删除资源权限")
			return
		}
		err := deps.ResourceRepo.MoveToTrashOwned(r.Context(), u.ID, id)
		if errors.Is(err, resource.ErrNotFound) || errors.Is(err, resource.ErrOwnerMismatch) {
			writeBusinessError(w, http.StatusNotFound, "资源不存在")
			return
		}
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "删除资源失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	}
}

func requireApprovedFile(w http.ResponseWriter, r *http.Request, deps Deps, resourceID string) bool {
	approved, err := deps.AdminRepo.IsFileApproved(r.Context(), resourceID)
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取文件审核状态失败")
		return false
	}
	if !approved {
		writeBusinessError(w, http.StatusForbidden, "文件正在审核或未通过审核")
		return false
	}
	return true
}

func serveOwnedFile(w http.ResponseWriter, r *http.Request, deps Deps, item resource.Resource) {
	path, err := deps.FileStore.ValidateFile(r.Context(), item)
	if err != nil {
		log.Printf("下载文件校验失败 id=%s: %v", item.ID, err)
		writeBusinessError(w, http.StatusUnprocessableEntity, "文件完整性校验失败")
		return
	}
	contentType := ""
	if item.MimeType != nil {
		contentType = strings.TrimSpace(*item.MimeType)
	}
	if contentType == "" {
		contentType = mime.TypeByExtension(strings.ToLower(filepath.Ext(item.Name)))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	serveDownload(w, r, path, item.SizeBytes, item.Name, contentType)
}

// serveResourcePreview 只向资源所有者返回文件内容。每次读取前都会重新计算
// SHA-256 并核对数据库中的大小和校验码；ServeContent 同时提供 PDF 等预览器
// 需要的 HEAD、Range 与 206 响应。
func serveResourcePreview(w http.ResponseWriter, r *http.Request, deps Deps, id string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u, ok := requireUser(w, r, deps)
	if !ok {
		return
	}
	if !u.HasPermission(permission.Preview) {
		writeBusinessError(w, http.StatusForbidden, "没有预览资源权限")
		return
	}
	if strings.TrimSpace(id) == "" {
		writeBusinessError(w, http.StatusBadRequest, "资源编号无效")
		return
	}
	item, err := deps.ResourceRepo.GetOwned(r.Context(), u.ID, id)
	if errors.Is(err, resource.ErrNotFound) || errors.Is(err, resource.ErrOwnerMismatch) || item.Kind != resource.KindFile {
		writeBusinessError(w, http.StatusNotFound, "文件不存在")
		return
	}
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取文件失败")
		return
	}
	if !requireApprovedFile(w, r, deps, item.ID) {
		return
	}
	serveResourceFilePreview(w, r, deps, item)
}

func serveResourceFilePreview(w http.ResponseWriter, r *http.Request, deps Deps, item resource.Resource) {
	filePath, err := deps.FileStore.ValidateFile(r.Context(), item)
	if err != nil {
		log.Printf("预览文件校验失败 id=%s: %v", item.ID, err)
		writeBusinessError(w, http.StatusUnprocessableEntity, "文件完整性校验失败")
		return
	}
	file, err := os.Open(filePath)
	if err != nil {
		writeBusinessError(w, http.StatusNotFound, "文件不存在")
		return
	}
	defer file.Close()

	contentType := ""
	if item.MimeType != nil {
		contentType = strings.TrimSpace(*item.MimeType)
	}
	if contentType == "" {
		contentType = mime.TypeByExtension(strings.ToLower(filepath.Ext(item.Name)))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": item.Name}))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, item.Name, item.UpdatedAt, file)
}

func normalizeID(id *string) *string {
	if id == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*id)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func writeResourceMutationError(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, resource.ErrInvalidName):
		writeBusinessError(w, http.StatusBadRequest, "名称不合法")
	case errors.Is(err, resource.ErrNotFound), errors.Is(err, resource.ErrNotFolder), errors.Is(err, resource.ErrOwnerMismatch):
		writeBusinessError(w, http.StatusNotFound, "父文件夹不存在")
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		writeBusinessError(w, http.StatusConflict, "同一目录下已存在同名资源")
	default:
		log.Printf("写入资源失败：%v", err)
		writeBusinessError(w, http.StatusInternalServerError, "保存资源失败")
	}
}

func decodeSmallJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return decodeJSONBody(w, r, dst, 1<<20)
}
