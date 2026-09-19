package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/blobstore"
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

// serveOwnedResourcesDownload 生成文件下载计划：前端逐文件取数（解密、合并、
// 打包均在浏览器完成）。单文件同样按「分片列表」处理，不存在单分片特殊路径。
//
// 取数方式与分享页一致：302 优先且可用时逐片直连第三方；否则按降级开关决定
// 本机中转（密文 + 密钥信封，或服务器解密兜底）或按下载失败处理。
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
	settings, err := deps.SystemSettings.Get(r.Context())
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取下载策略失败")
		return
	}
	knobs, knobErr := deps.SystemSettings.GetKnobs(r.Context())
	if knobErr != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取下载策略失败")
		return
	}
	singleFile := len(items) == 1 && items[0].Kind == resource.KindFile
	virtualRoot := resource.Resource{
		ID: fmt.Sprintf("owner-download-%d", time.Now().UnixNano()), OwnerUserID: u.ID,
		Kind: resource.KindFolder, Name: "下载文件", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	tree, err := buildResourceSelectionTree(r, deps, items, virtualRoot)
	if err != nil {
		writeDownloadPreparationError(w, err)
		return
	}
	blobByID := make(map[string]blobstore.Blob, len(tree))
	blobs := make([]blobstore.Blob, 0, len(tree))
	var totalBytes int64
	for _, entry := range tree {
		if entry.Resource.Kind != resource.KindFile {
			continue
		}
		blob, blobErr := resourceBlob(r.Context(), deps, entry.Resource)
		if blobErr != nil {
			writeDownloadPreparationError(w, blobErr)
			return
		}
		blobByID[entry.Resource.ID] = blob
		blobs = append(blobs, blob)
		totalBytes += entry.Resource.SizeBytes
	}
	if len(blobs) == 0 {
		writeBusinessError(w, http.StatusUnprocessableEntity, "所选内容不可下载")
		return
	}
	// 多文件/文件夹固定 302 优先，只有 302 不可用且开启自动降级时才回落本机中转。
	source, sourceErr := resolveDeliverySource(r, deps, settings, knobs, blobs, !singleFile)
	if sourceErr != nil {
		writeDeliveryFailure(w)
		return
	}
	const streamBase = "/api/resources/"
	planItems := make([]deliveryItem, 0, len(tree))
	for _, entry := range tree {
		if entry.Resource.Kind == resource.KindFolder {
			planItems = append(planItems, deliveryItem{
				Kind: resource.KindFolder, ResourceID: entry.ID, Name: entry.Name,
				RelativePath: filepath.ToSlash(entry.RelativePath),
			})
			continue
		}
		blob := blobByID[entry.Resource.ID]
		streamURL, partURL := "", ""
		if source == deliverySourceProxy {
			streamURL = streamBase + entry.ID + "/content"
		} else {
			partURL = streamBase + entry.ID + "/parts/"
		}
		planItem, itemErr := buildDeliveryItem(r, deps, entry.Resource, blob, streamURL, partURL)
		if itemErr != nil {
			log.Printf("构建下载计划失败 resource=%s：%v", entry.Resource.ID, itemErr)
			writeBusinessError(w, http.StatusInternalServerError, "准备下载失败")
			return
		}
		planItem.RelativePath = filepath.ToSlash(entry.RelativePath)
		planItems = append(planItems, planItem)
	}
	// 下载方向配额：计划创建即视为该次下载发生（登录用户按账号，超管豁免）。
	if err := guardPlannedDownload(r, deps, totalBytes, "owned"); err != nil {
		writeDownloadQuotaFailure(w, err)
		return
	}
	response := map[string]any{
		"status": "ok", "dataSource": source, "totalBytes": totalBytes, "items": planItems,
	}
	if !singleFile {
		response["archiveName"] = tree[0].Name + ".zip"
	}
	writeJSON(w, http.StatusOK, response)
}

func resourceItemHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pathParts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/resources/"), "/"), "/")
		if len(pathParts) == 2 && pathParts[1] == "preview" {
			serveResourcePreview(w, r, deps, pathParts[0])
			return
		}
		if len(pathParts) == 2 && pathParts[1] == "content" {
			serveOwnedFileContent(w, r, deps, pathParts[0])
			return
		}
		if len(pathParts) == 3 && pathParts[1] == "parts" {
			serveOwnedFilePart(w, r, deps, pathParts[0], pathParts[2])
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

// serveOwnedFileContent 输出单个文件内容（文件页本机中转取数）：加密对象且请求方
// 具备前端解密能力时下发密文 + 密钥信封，否则服务器解密兜底输出明文。
func serveOwnedFileContent(w http.ResponseWriter, r *http.Request, deps Deps, id string) {
	u, ok := requireUser(w, r, deps)
	if !ok {
		return
	}
	if !u.HasPermission(permission.Download) {
		writeBusinessError(w, http.StatusForbidden, "没有下载资源权限")
		return
	}
	item, err := deps.ResourceRepo.GetOwned(r.Context(), u.ID, strings.TrimSpace(id))
	if err != nil || item.Kind != resource.KindFile {
		writeBusinessError(w, http.StatusNotFound, "文件不存在")
		return
	}
	if !requireApprovedFile(w, r, deps, item.ID) {
		return
	}
	blob, blobErr := resourceBlob(r.Context(), deps, item)
	if blobErr != nil {
		writeDownloadPreparationError(w, blobErr)
		return
	}
	if _, ok := deliverItemStream(w, r, deps, blob, item.Name, blobContentType(item),
		"attachment", blobstore.PresignForShare); !ok {
		return
	}
}

// serveOwnedFilePart 为文件页 302 取数逐片发放第三方直链（前端按需逐片请求）。
func serveOwnedFilePart(w http.ResponseWriter, r *http.Request, deps Deps, id, rawIndex string) {
	u, ok := requireUser(w, r, deps)
	if !ok {
		return
	}
	if !u.HasPermission(permission.Download) {
		writeBusinessError(w, http.StatusForbidden, "没有下载资源权限")
		return
	}
	index, err := strconv.ParseInt(rawIndex, 10, 32)
	if err != nil || index < 0 {
		writeBusinessError(w, http.StatusNotFound, "下载地址不存在")
		return
	}
	item, err := deps.ResourceRepo.GetOwned(r.Context(), u.ID, strings.TrimSpace(id))
	if err != nil || item.Kind != resource.KindFile {
		writeBusinessError(w, http.StatusNotFound, "文件不存在")
		return
	}
	if !requireApprovedFile(w, r, deps, item.ID) {
		return
	}
	blob, blobErr := resourceBlob(r.Context(), deps, item)
	if blobErr != nil {
		writeDownloadPreparationError(w, blobErr)
		return
	}
	url, presignErr := presignItemPart(r, deps, blob, int32(index), blobstore.PresignForShare)
	if presignErr != nil {
		if errors.Is(presignErr, errDeliveryUnavailable) {
			writeDeliveryFailure(w)
			return
		}
		log.Printf("准备直链失败 resource=%s：%v", item.ID, presignErr)
		writeBusinessError(w, http.StatusInternalServerError, "准备下载失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "url": url})
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
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	// 与下载共用交付策略：加密 + 服务器实时解密关时输出密文并下发解密元数据
	// （前端读取响应头解密后交给预览器）。
	serveFileContent(w, r, deps, item, "inline")
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
	case errors.Is(err, resource.ErrNameConflict):
		writeBusinessError(w, http.StatusConflict, "同一目录下已存在同名资源")
	case errors.Is(err, resource.ErrAdminBlocked):
		writeBusinessError(w, http.StatusForbidden, "文件已被管理员限制，无法覆盖")
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
