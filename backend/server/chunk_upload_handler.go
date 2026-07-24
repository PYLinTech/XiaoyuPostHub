package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/randomtoken"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
)

const hardUploadLimit = int64(100 << 30)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var uploadBatchPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type initUploadRequest struct {
	BatchID        string  `json:"batchId"`
	Filename       string  `json:"filename"`
	ParentID       *string `json:"parentId"`
	Size           int64   `json:"size"`
	MimeType       string  `json:"mimeType"`
	SHA256         string  `json:"sha256"`
	ConflictAction string  `json:"conflictAction"`
}

type uploadConflictsRequest struct {
	ParentID *string  `json:"parentId"`
	Files    []string `json:"files"`
}

type uploadActionRequest struct {
	Action string `json:"action"`
}

func uploadConfigHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if _, ok := requireUser(w, r, deps); !ok {
			return
		}
		settings, err := deps.SystemSettings.Get(r.Context())
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取分片配置失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok", "chunkSizeBytes": settings.UploadChunkSizeBytes,
			"taskChunkConcurrency": settings.UploadTaskChunkConcurrency,
			"userTaskConcurrency":  settings.UploadUserTaskConcurrency,
		})
	}
}

func uploadConflictsHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := requireUser(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodPost {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !u.HasPermission(permission.Upload) {
			writeBusinessError(w, http.StatusForbidden, "没有上传权限")
			return
		}
		var req uploadConflictsRequest
		if err := decodeSmallJSON(w, r, &req); err != nil || len(req.Files) > 1000 {
			writeBusinessError(w, http.StatusBadRequest, "文件信息无效")
			return
		}
		parentID := normalizeID(req.ParentID)
		if parentID != nil {
			parent, err := deps.ResourceRepo.GetOwned(r.Context(), u.ID, *parentID)
			if err != nil || parent.Kind != resource.KindFolder {
				writeBusinessError(w, http.StatusNotFound, "父文件夹不存在")
				return
			}
		}
		for _, name := range req.Files {
			if _, err := resource.ValidateName(name); err != nil {
				writeBusinessError(w, http.StatusBadRequest, "文件信息无效")
				return
			}
		}
		existing, err := deps.ResourceRepo.ExistingChildNames(r.Context(), u.ID, parentID, req.Files)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "检查同名文件失败")
			return
		}
		seen := make(map[string]bool)
		conflicts := make([]map[string]any, 0)
		for index, name := range req.Files {
			if existing[name] || seen[name] {
				conflicts = append(conflicts, map[string]any{"index": index, "filename": name})
			}
			seen[name] = true
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "conflicts": conflicts})
	}
}

func uploadSessionsHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := requireUser(w, r, deps)
		if !ok {
			return
		}
		if r.Method == http.MethodGet {
			items, err := deps.UploadRepo.ListOwned(r.Context(), u.ID)
			if err != nil {
				writeBusinessError(w, http.StatusInternalServerError, "读取上传任务失败")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "items": items})
			return
		}
		if r.Method != http.MethodPost {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !u.HasPermission(permission.Upload) {
			writeBusinessError(w, http.StatusForbidden, "没有上传权限")
			return
		}
		var req initUploadRequest
		if err := decodeSmallJSON(w, r, &req); err != nil {
			writeBusinessError(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		req.Filename = strings.TrimSpace(req.Filename)
		req.BatchID = strings.TrimSpace(req.BatchID)
		req.SHA256 = strings.ToLower(strings.TrimSpace(req.SHA256))
		req.ConflictAction = strings.TrimSpace(req.ConflictAction)
		if req.ConflictAction == "" {
			req.ConflictAction = "error"
		}
		if _, err := resource.ValidateName(req.Filename); err != nil || req.Size < 0 || !sha256Pattern.MatchString(req.SHA256) || (req.BatchID != "" && !uploadBatchPattern.MatchString(req.BatchID)) {
			writeBusinessError(w, http.StatusBadRequest, "文件信息无效")
			return
		}
		if req.ConflictAction != "error" && req.ConflictAction != "overwrite" && req.ConflictAction != "auto_rename" {
			writeBusinessError(w, http.StatusBadRequest, "同名文件处理方式无效")
			return
		}
		parentID := normalizeID(req.ParentID)
		if parentID != nil {
			parent, err := deps.ResourceRepo.GetOwned(r.Context(), u.ID, *parentID)
			if err != nil || parent.Kind != resource.KindFolder {
				writeBusinessError(w, http.StatusNotFound, "父文件夹不存在")
				return
			}
		}
		if req.ConflictAction == "auto_rename" {
			availableName, nameErr := deps.ResourceRepo.AvailableChildName(r.Context(), u.ID, parentID, req.Filename)
			if nameErr != nil {
				writeBusinessError(w, http.StatusConflict, "无法生成可用文件名")
				return
			}
			req.Filename = availableName
		}
		storageCredit, err := overwriteStorageCredit(r, deps, u.ID, parentID, req.Filename, req.ConflictAction)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取存储配额失败")
			return
		}
		if err := validateUploadQuota(r, deps, u.ID, req.Size, storageCredit, true); err != nil {
			writeBusinessError(w, uploadQuotaStatus(err), err.Error())
			return
		}
		settings, err := deps.SystemSettings.Get(r.Context())
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取分片配置失败")
			return
		}
		chunkSize := settings.UploadChunkSizeBytes
		totalChunks := int32(1)
		if req.Size > 0 {
			totalChunks = int32((req.Size + int64(chunkSize) - 1) / int64(chunkSize))
		}
		if req.BatchID == "" {
			req.BatchID, _ = randomtoken.New(18)
		}
		session, resumed, err := deps.UploadRepo.CreateOrResume(r.Context(), u.ID, req.BatchID, parentID, req.Filename, req.Size, chunkSize, totalChunks, strings.TrimSpace(req.MimeType), req.SHA256, req.ConflictAction)
		if err != nil {
			writeBusinessError(w, http.StatusConflict, "无法创建上传任务")
			return
		}

		// 全平台按内容哈希复用已落盘文件；响应只包含当前用户新建的资源记录。
		source, sourceErr := deps.ResourceRepo.FindFileByChecksum(r.Context(), req.SHA256, req.Size)
		if sourceErr == nil && source.StorageKey != nil {
			if _, err := deps.FileStore.ValidateFile(r.Context(), source); err == nil {
				item, oldStorageKey, instantErr := cloneUploadedResource(r, deps, u.ID, parentID, req.Filename, req.MimeType, req.Size, req.SHA256, *source.StorageKey, session.BatchID, session.ID, req.ConflictAction)
				if instantErr != nil {
					_ = deps.UploadRepo.SetStatus(r.Context(), u.ID, session.ID, "failed", "秒传失败")
					writeResourceMutationError(w, instantErr)
					return
				}
				if oldStorageKey != nil {
					_ = deps.FileStore.Remove(r.Context(), *oldStorageKey)
				}
				session, _ = deps.UploadRepo.GetOwned(r.Context(), u.ID, session.ID)
				writeJSON(w, http.StatusCreated, map[string]any{"status": "ok", "instant": true, "resumed": resumed, "task": session, "resource": item})
				return
			}
		}
		writeJSON(w, http.StatusCreated, map[string]any{"status": "ok", "instant": false, "resumed": resumed, "task": session})
	}
}

func uploadSessionItemHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := requireUser(w, r, deps)
		if !ok {
			return
		}
		parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/uploads/"), "/"), "/")
		if len(parts) == 3 && parts[1] == "chunks" {
			handleUploadChunk(w, r, deps, u.ID, parts[0], parts[2])
			return
		}
		if len(parts) == 2 && parts[1] == "complete" {
			handleUploadComplete(w, r, deps, u.ID, parts[0])
			return
		}
		if len(parts) != 1 || parts[0] == "" {
			writeBusinessError(w, http.StatusNotFound, "上传任务不存在")
			return
		}
		if r.Method == http.MethodGet {
			item, err := deps.UploadRepo.GetOwned(r.Context(), u.ID, parts[0])
			if err != nil {
				writeBusinessError(w, http.StatusNotFound, "上传任务不存在")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "task": item})
			return
		}
		if r.Method != http.MethodPatch && r.Method != http.MethodDelete {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		action := "cancel"
		if r.Method == http.MethodPatch {
			var req uploadActionRequest
			if err := decodeSmallJSON(w, r, &req); err != nil {
				writeBusinessError(w, http.StatusBadRequest, "请求格式错误")
				return
			}
			action = req.Action
		}
		if action == "move_up" || action == "move_down" {
			offset := 1
			if action == "move_up" {
				offset = -1
			}
			if err := deps.UploadRepo.MoveOwned(r.Context(), u.ID, parts[0], offset); err != nil {
				writeBusinessError(w, http.StatusConflict, "调整上传队列失败")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
			return
		}
		status := map[string]string{"pause": "paused", "resume": "queued", "cancel": "canceled"}[action]
		if status == "" {
			writeBusinessError(w, http.StatusBadRequest, "上传任务操作无效")
			return
		}
		item, err := deps.UploadRepo.GetOwned(r.Context(), u.ID, parts[0])
		if err != nil {
			writeBusinessError(w, http.StatusNotFound, "上传任务不存在")
			return
		}
		if item.Status == "completing" || (item.Status == "completed" && status != "canceled") {
			writeBusinessError(w, http.StatusConflict, "上传任务状态无效")
			return
		}
		if err := deps.UploadRepo.SetStatus(r.Context(), u.ID, parts[0], status, ""); err != nil {
			writeBusinessError(w, http.StatusNotFound, "上传任务不存在")
			return
		}
		if status == "canceled" {
			_ = deps.FileStore.RemoveUploadSession(r.Context(), parts[0])
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	}
}

func handleUploadChunk(w http.ResponseWriter, r *http.Request, deps Deps, ownerID int64, sessionID, rawIndex string) {
	if r.Method != http.MethodPut {
		writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	indexValue, err := strconv.ParseInt(rawIndex, 10, 32)
	if err != nil || indexValue < 0 {
		writeBusinessError(w, http.StatusBadRequest, "分片编号无效")
		return
	}
	session, err := deps.UploadRepo.GetOwned(r.Context(), ownerID, sessionID)
	if err != nil {
		writeBusinessError(w, http.StatusNotFound, "上传任务不存在")
		return
	}
	index := int32(indexValue)
	if index >= session.TotalChunks || (session.Status != "queued" && session.Status != "uploading") {
		writeBusinessError(w, http.StatusConflict, "上传任务状态无效")
		return
	}
	for _, receivedIndex := range session.ReceivedChunks {
		if receivedIndex == index {
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "chunkIndex": index, "alreadyReceived": true})
			return
		}
	}
	expectedSize := int64(session.ChunkSize)
	if index == session.TotalChunks-1 {
		expectedSize = session.TotalSize - int64(index)*int64(session.ChunkSize)
	}
	if session.TotalSize == 0 {
		expectedSize = 0
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(session.ChunkSize)+1)
	relativePath, checksum, size, err := deps.FileStore.WriteUploadChunk(r.Context(), sessionID, index, r.Body, int64(session.ChunkSize))
	if err != nil {
		writeBusinessError(w, http.StatusRequestEntityTooLarge, "保存上传分片失败")
		return
	}
	expectedChecksum := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Chunk-SHA256")))
	if int64(size) != expectedSize || !sha256Pattern.MatchString(expectedChecksum) || checksum != expectedChecksum {
		writeBusinessError(w, http.StatusUnprocessableEntity, "上传分片校验失败")
		return
	}
	if err := deps.UploadRepo.RecordChunk(r.Context(), ownerID, sessionID, index, size, checksum, relativePath); err != nil {
		writeBusinessError(w, http.StatusConflict, "上传任务状态无效")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "chunkIndex": index})
}

func handleUploadComplete(w http.ResponseWriter, r *http.Request, deps Deps, ownerID int64, sessionID string) {
	if r.Method != http.MethodPost {
		writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, err := deps.UploadRepo.GetOwned(r.Context(), ownerID, sessionID)
	if err != nil {
		writeBusinessError(w, http.StatusNotFound, "上传任务不存在")
		return
	}
	if session.Status == "completed" && session.ResourceID != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "resourceId": *session.ResourceID})
		return
	}
	chunks, err := deps.UploadRepo.ListChunks(r.Context(), ownerID, sessionID)
	if err != nil || len(chunks) != int(session.TotalChunks) {
		writeBusinessError(w, http.StatusConflict, "上传分片尚未完成")
		return
	}
	if err := deps.UploadRepo.ClaimCompleting(r.Context(), ownerID, sessionID); err != nil {
		writeBusinessError(w, http.StatusConflict, "上传任务正在合并")
		return
	}
	temp, err := deps.FileStore.NewTemp(r.Context(), "assembled-upload-*")
	if err != nil {
		failUploadTask(r, deps, ownerID, sessionID, "创建合并文件失败")
		writeBusinessError(w, http.StatusInternalServerError, "创建合并文件失败")
		return
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	hash := sha256.New()
	var assembledSize int64
	for expectedIndex, chunk := range chunks {
		if chunk.Index != int32(expectedIndex) {
			failUploadTask(r, deps, ownerID, sessionID, "分片序列不完整")
			writeBusinessError(w, http.StatusConflict, "上传分片尚未完成")
			return
		}
		path, pathErr := deps.FileStore.UploadChunkPath(r.Context(), chunk.RelativePath)
		if pathErr != nil {
			failUploadTask(r, deps, ownerID, sessionID, "读取分片失败")
			writeBusinessError(w, http.StatusInternalServerError, "读取上传分片失败")
			return
		}
		part, openErr := os.Open(path)
		if openErr != nil {
			failUploadTask(r, deps, ownerID, sessionID, "分片文件缺失")
			writeBusinessError(w, http.StatusConflict, "上传分片文件缺失")
			return
		}
		n, copyErr := io.Copy(io.MultiWriter(temp, hash), part)
		_ = part.Close()
		if copyErr != nil || n != int64(chunk.SizeBytes) {
			failUploadTask(r, deps, ownerID, sessionID, "合并分片失败")
			writeBusinessError(w, http.StatusInternalServerError, "合并上传分片失败")
			return
		}
		assembledSize += n
	}
	checksum := hex.EncodeToString(hash.Sum(nil))
	if assembledSize != session.TotalSize || checksum != session.ExpectedSHA256 {
		failUploadTask(r, deps, ownerID, sessionID, "完整文件校验失败")
		writeBusinessError(w, http.StatusUnprocessableEntity, "完整文件校验失败")
		return
	}
	if err := temp.Sync(); err != nil || temp.Close() != nil {
		failUploadTask(r, deps, ownerID, sessionID, "合并文件落盘失败")
		writeBusinessError(w, http.StatusInternalServerError, "合并文件落盘失败")
		return
	}
	storageCredit, err := overwriteStorageCredit(r, deps, ownerID, session.ParentID, session.Filename, session.ConflictAction)
	if err != nil {
		failUploadTask(r, deps, ownerID, sessionID, "读取存储配额失败")
		writeBusinessError(w, http.StatusInternalServerError, "读取存储配额失败")
		return
	}
	if err := validateUploadQuota(r, deps, ownerID, session.TotalSize, storageCredit, true); err != nil {
		failUploadTask(r, deps, ownerID, sessionID, err.Error())
		writeBusinessError(w, uploadQuotaStatus(err), err.Error())
		return
	}
	storageKey, err := resource.StorageKey()
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "生成存储键失败")
		return
	}
	finalPath, err := deps.FileStore.Commit(r.Context(), tempPath, storageKey)
	if err != nil {
		failUploadTask(r, deps, ownerID, sessionID, "提交上传文件失败")
		writeBusinessError(w, http.StatusInternalServerError, "提交上传文件失败")
		return
	}
	committed = true
	item, oldStorageKey, err := saveUploadedResource(r, deps, ownerID, session.ParentID, session.Filename, storageKey, session.TotalSize, checksum, session.MimeType, session.ConflictAction, session.BatchID, session.ID)
	if err != nil {
		_ = os.Remove(finalPath)
		failUploadTask(r, deps, ownerID, sessionID, "保存资源失败")
		writeResourceMutationError(w, err)
		return
	}
	if oldStorageKey != nil {
		_ = deps.FileStore.Remove(r.Context(), *oldStorageKey)
	}
	_ = deps.FileStore.RemoveUploadSession(r.Context(), sessionID)
	writeJSON(w, http.StatusCreated, map[string]any{"status": "ok", "resource": item})
}

func cloneUploadedResource(r *http.Request, deps Deps, ownerID int64, parentID *string, filename, mimeType string, size int64, checksum, sourceStorageKey, uploadTaskID, uploadSessionID, conflictAction string) (resource.Resource, *string, error) {
	storageKey, err := resource.StorageKey()
	if err != nil {
		return resource.Resource{}, nil, err
	}
	path, err := deps.FileStore.CloneFile(r.Context(), sourceStorageKey, storageKey)
	if err != nil {
		return resource.Resource{}, nil, err
	}
	item, oldStorageKey, err := saveUploadedResource(r, deps, ownerID, parentID, filename, storageKey, size, checksum, mimeType, conflictAction, uploadTaskID, uploadSessionID)
	if err != nil {
		_ = os.Remove(path)
		return resource.Resource{}, nil, err
	}
	return item, oldStorageKey, nil
}

func saveUploadedResource(r *http.Request, deps Deps, ownerID int64, parentID *string, filename, storageKey string, size int64, checksum, mimeType, conflictAction, uploadTaskID, uploadSessionID string) (resource.Resource, *string, error) {
	settings, err := deps.AdminRepo.GetReviewSettings(r.Context())
	if err != nil {
		return resource.Resource{}, nil, err
	}
	return deps.ResourceRepo.SaveUploadedFile(
		r.Context(), ownerID, parentID, filename, storageKey, size, checksum,
		mimeType, conflictAction == "overwrite", settings.UploadRequiresReview,
		uploadTaskID, uploadSessionID,
	)
}

func overwriteStorageCredit(r *http.Request, deps Deps, ownerID int64, parentID *string, filename, conflictAction string) (int64, error) {
	if conflictAction != "overwrite" {
		return 0, nil
	}
	return deps.ResourceRepo.ExistingChildFileSize(r.Context(), ownerID, parentID, filename)
}

func validateUploadQuota(r *http.Request, deps Deps, ownerID, size, storageCredit int64, includeStorage bool) error {
	profile, err := deps.QuotaRepo.GetEffectiveQuotaByUser(r.Context(), ownerID)
	if err != nil {
		return fmt.Errorf("读取上传配额失败")
	}
	if size > hardUploadLimit || (profile.SingleFileBytesLimit.Valid && size > profile.SingleFileBytesLimit.Int64) {
		return fmt.Errorf("文件超过单文件大小限制")
	}
	dailyCount, dailyBytes, err := deps.ResourceRepo.UploadUsageSince(r.Context(), ownerID, time.Now().Add(-24*time.Hour))
	if err != nil {
		return fmt.Errorf("读取每日上传用量失败")
	}
	if profile.DailyUploadCountLimit.Valid && dailyCount >= profile.DailyUploadCountLimit.Int64 {
		return fmt.Errorf("已达到每日上传次数限制")
	}
	if profile.DailyUploadBytesLimit.Valid && dailyBytes+size > profile.DailyUploadBytesLimit.Int64 {
		return fmt.Errorf("已达到每日上传流量限制")
	}
	if includeStorage && profile.StorageBytesLimit.Valid {
		current, err := deps.ResourceRepo.TotalFileBytesByOwner(r.Context(), ownerID)
		if err != nil {
			return fmt.Errorf("读取存储配额失败")
		}
		if current+size-storageCredit > profile.StorageBytesLimit.Int64 {
			return fmt.Errorf("存储空间不足")
		}
	}
	return nil
}

func uploadQuotaStatus(err error) int {
	if strings.Contains(err.Error(), "次数") {
		return http.StatusTooManyRequests
	}
	if strings.Contains(err.Error(), "限制") || strings.Contains(err.Error(), "空间") {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusInternalServerError
}

func failUploadTask(r *http.Request, deps Deps, ownerID int64, sessionID, message string) {
	_ = deps.UploadRepo.SetStatus(r.Context(), ownerID, sessionID, "failed", message)
}
