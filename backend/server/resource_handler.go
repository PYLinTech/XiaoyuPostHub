package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/filestore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
	"github.com/jackc/pgx/v5/pgconn"
)

const hardUploadLimit = int64(100 << 30) // 配额不限时仍保留 100 GiB 单请求安全上限。

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

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
	resourceIDs := normalizeResourceIDs(req.ResourceIDs, "")
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
		tree, err := deps.ResourceRepo.DeleteOwned(r.Context(), u.ID, id)
		if errors.Is(err, resource.ErrNotFound) || errors.Is(err, resource.ErrOwnerMismatch) {
			writeBusinessError(w, http.StatusNotFound, "资源不存在")
			return
		}
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "删除资源失败")
			return
		}
		for _, item := range tree {
			if item.Kind == resource.KindFile && item.StorageKey != nil {
				if err := deps.FileStore.Remove(r.Context(), *item.StorageKey); err != nil {
					log.Printf("清理资源文件失败 id=%s: %v", item.ID, err)
				}
			}
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

func uploadHandler(deps Deps) http.HandlerFunc {
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
			writeBusinessError(w, http.StatusForbidden, "没有上传权限")
			return
		}

		quotaProfile, err := deps.QuotaRepo.GetEffectiveQuotaByUser(r.Context(), u.ID)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取上传配额失败")
			return
		}
		dailyCount, dailyBytes, err := deps.ResourceRepo.UploadUsageSince(r.Context(), u.ID, time.Now().Add(-24*time.Hour))
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取每日上传用量失败")
			return
		}
		if quotaProfile.DailyUploadCountLimit.Valid && dailyCount >= quotaProfile.DailyUploadCountLimit.Int64 {
			writeBusinessError(w, http.StatusTooManyRequests, "已达到每日上传次数限制")
			return
		}
		maxFileBytes := hardUploadLimit
		if quotaProfile.SingleFileBytesLimit.Valid && quotaProfile.SingleFileBytesLimit.Int64 < maxFileBytes {
			maxFileBytes = quotaProfile.SingleFileBytesLimit.Int64
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFileBytes+(2<<20))
		reader, err := r.MultipartReader()
		if err != nil {
			writeBusinessError(w, http.StatusBadRequest, "必须使用 multipart/form-data 上传")
			return
		}

		expectedChecksum := strings.ToLower(strings.TrimSpace(r.Header.Get("X-File-SHA256")))
		var parentID *string
		var overrideName, originalName, mimeType string
		var tempPath string
		var sizeBytes int64
		var streamChecksum string
		cleanupTemp := func() {
			if tempPath != "" {
				_ = os.Remove(tempPath)
			}
		}
		defer cleanupTemp()

		for {
			part, nextErr := reader.NextPart()
			if errors.Is(nextErr, io.EOF) {
				break
			}
			if nextErr != nil {
				writeBusinessError(w, http.StatusBadRequest, "读取上传内容失败")
				return
			}
			field := part.FormName()
			if part.FileName() == "" {
				value, readErr := io.ReadAll(io.LimitReader(part, 4097))
				_ = part.Close()
				if readErr != nil || len(value) > 4096 {
					writeBusinessError(w, http.StatusBadRequest, "上传字段过长")
					return
				}
				switch field {
				case "checksum":
					expectedChecksum = strings.ToLower(strings.TrimSpace(string(value)))
				case "parentId":
					if id := strings.TrimSpace(string(value)); id != "" {
						parentID = &id
					}
				case "name":
					overrideName = strings.TrimSpace(string(value))
				}
				continue
			}
			if field != "file" || tempPath != "" {
				_ = part.Close()
				writeBusinessError(w, http.StatusBadRequest, "一次只能上传一个 file 字段")
				return
			}
			originalName = part.FileName()
			mimeType = part.Header.Get("Content-Type")
			temp, createErr := deps.FileStore.NewTemp(r.Context(), "upload-*")
			if createErr != nil {
				writeBusinessError(w, http.StatusInternalServerError, "创建上传文件失败")
				return
			}
			tempPath = temp.Name()
			hash := sha256.New()
			n, copyErr := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(part, maxFileBytes+1))
			_ = part.Close()
			if copyErr == nil {
				copyErr = temp.Sync()
			}
			closeErr := temp.Close()
			if copyErr != nil || closeErr != nil {
				writeBusinessError(w, http.StatusInternalServerError, "保存上传文件失败")
				return
			}
			if n > maxFileBytes {
				writeBusinessError(w, http.StatusRequestEntityTooLarge, "文件超过单文件大小限制")
				return
			}
			sizeBytes = n
			streamChecksum = hex.EncodeToString(hash.Sum(nil))
		}

		if tempPath == "" {
			writeBusinessError(w, http.StatusBadRequest, "缺少 file 字段")
			return
		}
		if !sha256Pattern.MatchString(expectedChecksum) {
			writeBusinessError(w, http.StatusBadRequest, "缺少合法的 SHA-256 校验码")
			return
		}
		if streamChecksum != expectedChecksum {
			writeBusinessError(w, http.StatusUnprocessableEntity, "前端校验码与上传内容不一致")
			return
		}
		// 文件已保存并 fsync 后重新从磁盘完整读取，完成第二次独立校验。
		diskChecksum, diskSize, err := filestore.ChecksumFile(tempPath)
		if err != nil || diskChecksum != expectedChecksum || diskSize != sizeBytes {
			writeBusinessError(w, http.StatusUnprocessableEntity, "后端落盘校验失败")
			return
		}

		if quotaProfile.StorageBytesLimit.Valid {
			currentBytes, totalErr := deps.ResourceRepo.TotalFileBytesByOwner(r.Context(), u.ID)
			if totalErr != nil {
				writeBusinessError(w, http.StatusInternalServerError, "读取存储配额失败")
				return
			}
			if currentBytes+sizeBytes > quotaProfile.StorageBytesLimit.Int64 {
				writeBusinessError(w, http.StatusRequestEntityTooLarge, "存储空间不足")
				return
			}
		}
		if quotaProfile.DailyUploadBytesLimit.Valid && dailyBytes+sizeBytes > quotaProfile.DailyUploadBytesLimit.Int64 {
			writeBusinessError(w, http.StatusRequestEntityTooLarge, "已达到每日上传流量限制")
			return
		}

		name := originalName
		if overrideName != "" {
			name = overrideName
		}
		if _, err := resource.ValidateName(name); err != nil {
			writeBusinessError(w, http.StatusBadRequest, "文件名不合法")
			return
		}
		storageKey, err := resource.StorageKey()
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "生成存储键失败")
			return
		}
		finalPath, err := deps.FileStore.Commit(r.Context(), tempPath, storageKey)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "提交上传文件失败")
			return
		}
		tempPath = ""
		item, err := deps.ResourceRepo.CreateFile(
			r.Context(), u.ID, normalizeID(parentID), name, storageKey,
			sizeBytes, diskChecksum, mimeType,
		)
		if err != nil {
			_ = os.Remove(finalPath)
			writeResourceMutationError(w, err)
			return
		}
		reviewStatus := "approved"
		settings, err := deps.AdminRepo.GetReviewSettings(r.Context())
		if err != nil {
			_, _ = deps.ResourceRepo.DeleteOwned(r.Context(), u.ID, item.ID)
			_ = os.Remove(finalPath)
			writeBusinessError(w, http.StatusInternalServerError, "读取文件审核配置失败")
			return
		}
		if settings.UploadRequiresReview {
			if err := deps.AdminRepo.MarkFilePending(r.Context(), item.ID); err != nil {
				_, _ = deps.ResourceRepo.DeleteOwned(r.Context(), u.ID, item.ID)
				_ = os.Remove(finalPath)
				writeBusinessError(w, http.StatusInternalServerError, "提交文件审核失败")
				return
			}
			reviewStatus = "pending"
		}
		writeJSON(w, http.StatusCreated, map[string]any{"status": "ok", "resource": item, "reviewStatus": reviewStatus})
	}
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
