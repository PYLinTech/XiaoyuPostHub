package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/blobstore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/filestore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/randomtoken"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
	"github.com/PYLinTech/XiaoyuPostHub/backend/upload"
)

// hardUploadLimit 是单文件系统硬上限的兜底默认值（可由系统设置覆盖）。
const hardUploadLimit = int64(100 << 30)

// maxPendingUploadSessions 限制单用户进行中的上传会话数量：会话期间不产生资源，
// 若不设上限可无限占用临时盘（见 validateUploadQuota 的在途预扣）。
const maxPendingUploadSessions = 64

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
		// 同名冲突预检：error 动作直接拒绝、overwrite 动作拒绝覆盖被拉黑文件，
		// 避免用户传完全部分片才在合并阶段失败。auto_rename 不在 init 冻结名字：
		// 名字在合并时由服务端在事务内解析（并发同名各自拿到唯一名字；"重挂同一
		// 文件"也能命中已有会话，不会因自己的会话占用原名而新建第二个会话）。
		if req.ConflictAction == "error" || req.ConflictAction == "overwrite" {
			existing, lookupErr := deps.ResourceRepo.FindActiveChildFile(r.Context(), u.ID, parentID, req.Filename)
			if lookupErr == nil {
				if req.ConflictAction == "error" {
					writeBusinessError(w, http.StatusConflict, "同一目录下已存在同名文件")
					return
				}
				if existing.AdminBlocked {
					writeBusinessError(w, http.StatusForbidden, "文件已被管理员限制，无法覆盖")
					return
				}
			} else if !errors.Is(lookupErr, resource.ErrNotFound) {
				writeBusinessError(w, http.StatusInternalServerError, "读取同名文件失败")
				return
			}
		}
		storageCredit, err := overwriteStorageCredit(r, deps, u.ID, parentID, req.Filename, req.ConflictAction)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取存储配额失败")
			return
		}
		if err := validateUploadQuota(r, deps, u.ID, req.Size, storageCredit, "", true); err != nil {
			writeBusinessError(w, uploadQuotaStatus(err), err.Error())
			return
		}
		settings, err := deps.SystemSettings.Get(r.Context())
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取分片配置失败")
			return
		}
		// 加密策略开启但部署未配置 KEK：在初始化阶段直接拒绝，避免用户传完
		// 全部分片才在合并阶段失败。
		if settings.EncryptNewFiles && !deps.Blobs.EncryptionAvailable() {
			writeBusinessError(w, http.StatusServiceUnavailable, "已开启新文件加密，但服务器未配置加密密钥，请联系管理员")
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
		if errors.Is(err, upload.ErrInvalidState) {
			// 已存在同名上传任务且大小不一致：可理解的冲突，而非服务器故障。
			writeBusinessError(w, http.StatusConflict, "已存在同名上传任务，请先完成或取消后重试")
			return
		}
		if err != nil {
			log.Printf("创建上传任务失败：%v", err)
			writeBusinessError(w, http.StatusInternalServerError, "创建上传任务失败")
			return
		}

		// 全平台按内容哈希复用物理对象（引用计数复用，不复制文件、不生成新
		// 存储键）；被管理员限制的内容不可复用；复用遵循当前加密策略
		// （加密只复用加密对象，明文只复用明文对象）。
		// 秒传复用范围由系统开关决定：默认全平台去重（省存储），管理员可改为
		// "仅本人"（crossUserDedupe=false），避免他人凭已知哈希+大小领取私有内容。
		allowCrossUserDedupe := true
		if knobs, knobErr := deps.SystemSettings.GetKnobs(r.Context()); knobErr == nil {
			allowCrossUserDedupe = knobs.CrossUserDedupe
		}
		if reusable, findErr := deps.Blobs.FindReusable(r.Context(), req.SHA256, req.Size, settings.EncryptNewFiles, u.ID, allowCrossUserDedupe); findErr == nil {
			if availableErr := deps.Blobs.VerifyAvailable(r.Context(), reusable); availableErr == nil {
				// 秒传同样要过配额：初始化阶段的检查发生在会话创建之前，多个并发
				// 秒传会读到同一份配额快照后各自提交，从而突破存储上限。提交资源
				// 前用"排除本会话"的口径复检一次，与分片上传的 complete 复检对齐。
				if quotaErr := validateUploadQuota(r, deps, u.ID, req.Size, storageCredit, session.ID, true); quotaErr != nil {
					_ = deps.UploadRepo.SetStatus(r.Context(), u.ID, session.ID, "failed", "秒传配额校验未通过")
					writeBusinessError(w, uploadQuotaStatus(quotaErr), quotaErr.Error())
					return
				}
				item, instantErr := saveUploadedResource(r, deps, u.ID, parentID, req.Filename, reusable.ID, req.Size, req.SHA256, req.MimeType, req.ConflictAction, session.BatchID, session.ID)
				if instantErr != nil {
					_ = deps.UploadRepo.SetStatus(r.Context(), u.ID, session.ID, "failed", "秒传失败")
					writeResourceMutationError(w, instantErr)
					return
				}
				session, _ = deps.UploadRepo.GetOwned(r.Context(), u.ID, session.ID)
				// 秒传不产生分片文件：清理此前可能残留的分片目录。completed 会话
				// 不参与过期清理，不在这里回收就会永久残留。
				_ = deps.FileStore.RemoveUploadSession(r.Context(), session.ID)
				writeJSON(w, http.StatusCreated, map[string]any{"status": "ok", "instant": true, "resumed": resumed, "task": session, "resource": item})
				return
			}
		}
		// 秒传未命中、即将进入分片上传：存储目标不可用时立即明确拒绝，避免用户
		// 传完全部分片才在合并阶段失败（需管理员在「存储管理」中修复配置）。
		if !deps.Blobs.DefaultBackendAvailable() {
			writeBusinessError(w, http.StatusServiceUnavailable, "服务器未配置默认存储后端，请联系管理员")
			return
		}
		if _, backendErr := resolveUploadBackend(r.Context(), deps, u.ID); backendErr != nil {
			if errors.Is(backendErr, errGroupBackendDisabled) {
				writeBusinessError(w, http.StatusServiceUnavailable, "所属用户组的存储后端已停用，请联系管理员")
				return
			}
			writeBusinessError(w, http.StatusInternalServerError, "解析用户组存储绑定失败")
			return
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
		if action == "cancel" {
			// 取消同时清除分片记录（否则取消后 resume 会跳过重传导致永久卡死）；
			// 已完成/正在合并的任务拒绝取消。
			if err := deps.UploadRepo.CancelOwned(r.Context(), u.ID, parts[0]); err != nil {
				if errors.Is(err, upload.ErrInvalidState) {
					writeBusinessError(w, http.StatusConflict, "已完成或正在合并的上传任务不能取消")
					return
				}
				writeBusinessError(w, http.StatusNotFound, "上传任务不存在")
				return
			}
			_ = deps.FileStore.RemoveUploadSession(r.Context(), parts[0])
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
			return
		}
		status := map[string]string{"pause": "paused", "resume": "queued"}[action]
		if status == "" {
			writeBusinessError(w, http.StatusBadRequest, "上传任务操作无效")
			return
		}
		item, err := deps.UploadRepo.GetOwned(r.Context(), u.ID, parts[0])
		if err != nil {
			writeBusinessError(w, http.StatusNotFound, "上传任务不存在")
			return
		}
		if item.Status == "completing" || item.Status == "completed" {
			writeBusinessError(w, http.StatusConflict, "上传任务状态无效")
			return
		}
		if err := deps.UploadRepo.SetUserActionStatus(r.Context(), u.ID, parts[0], status); err != nil {
			if errors.Is(err, upload.ErrInvalidState) {
				writeBusinessError(w, http.StatusConflict, "上传任务状态无效")
				return
			}
			writeBusinessError(w, http.StatusNotFound, "上传任务不存在")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	}
}

func handleUploadChunk(w http.ResponseWriter, r *http.Request, deps Deps, ownerID int64, sessionID, rawIndex string) {
	if !requireUploadPermission(w, r, deps, ownerID) {
		return
	}
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
	// 末片按实际剩余长度校验（空文件的唯一分片即末片：期望 0 字节）。
	expectedSize := int64(session.ChunkSize)
	if index == session.TotalChunks-1 {
		expectedSize = session.TotalSize - int64(index)*int64(session.ChunkSize)
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(session.ChunkSize)+1)
	relativePath, checksum, size, err := deps.FileStore.WriteUploadChunk(r.Context(), sessionID, index, r.Body, int64(session.ChunkSize))
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		switch {
		case errors.Is(err, filestore.ErrChunkTooLarge), errors.As(err, &maxBytesErr):
			writeBusinessError(w, http.StatusRequestEntityTooLarge, "上传分片超过允许大小")
		case errors.Is(err, filestore.ErrUnsafeStorageKey):
			writeBusinessError(w, http.StatusBadRequest, "上传任务标识无效")
		default:
			// 磁盘/IO 故障不能伪装成"请求过大"，否则排障方向会被带偏。
			log.Printf("保存上传分片失败（会话=%s 分片=%d）：%v", sessionID, index, err)
			writeBusinessError(w, http.StatusInternalServerError, "保存上传分片失败")
		}
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
	if !requireUploadPermission(w, r, deps, ownerID) {
		return
	}
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
	// 分片按序惰性打开并流式合并写入物理对象：Create 边写边算 SHA-256 并比对，
	// 同一时刻只占用一个分片文件描述符（此前一次性打开全部分片，大文件会撞
	// ulimit 上限并被误报为"分片缺失"）。
	for expectedIndex, chunk := range chunks {
		if chunk.Index != int32(expectedIndex) {
			failUploadTask(r, deps, ownerID, sessionID, "分片序列不完整")
			writeBusinessError(w, http.StatusConflict, "上传分片尚未完成")
			return
		}
	}
	parts := &partSequenceReader{ctx: r.Context(), store: deps.FileStore, chunks: chunks}
	defer parts.Close()
	storageCredit, err := overwriteStorageCredit(r, deps, ownerID, session.ParentID, session.Filename, session.ConflictAction)
	if err != nil {
		failUploadTask(r, deps, ownerID, sessionID, "读取存储配额失败")
		writeBusinessError(w, http.StatusInternalServerError, "读取存储配额失败")
		return
	}
	if err := validateUploadQuota(r, deps, ownerID, session.TotalSize, storageCredit, sessionID, true); err != nil {
		failUploadTask(r, deps, ownerID, sessionID, err.Error())
		writeBusinessError(w, uploadQuotaStatus(err), err.Error())
		return
	}
	settings, err := deps.SystemSettings.Get(r.Context())
	if err != nil {
		failUploadTask(r, deps, ownerID, sessionID, "读取加密配置失败")
		writeBusinessError(w, http.StatusInternalServerError, "读取加密配置失败")
		return
	}
	if settings.EncryptNewFiles && !deps.Blobs.EncryptionAvailable() {
		failUploadTask(r, deps, ownerID, sessionID, "服务器未配置加密密钥")
		writeBusinessError(w, http.StatusServiceUnavailable, "已开启新文件加密，但服务器未配置加密密钥，请联系管理员")
		return
	}
	// 强制分片：即便配置异常（0/非法），也按默认粒度分片，不再回落到单对象。
	chunkSize := settings.StorageChunkSizeBytes
	if !systemsetting.ValidStorageChunkSize(chunkSize) {
		chunkSize = systemsetting.DefaultStorageChunkSizeBytes
	}
	// 用户组存储绑定：组内用户的新对象写入组绑定的后端；未绑定用全局默认。
	// 绑定后端被停用时明确失败——静默回退会把文件写到与「组 = 存储」约定不符
	// 的位置，破坏后续按组迁移的范围。
	backendID, resolveErr := resolveUploadBackend(r.Context(), deps, ownerID)
	if resolveErr != nil {
		if errors.Is(resolveErr, errGroupBackendDisabled) {
			failUploadTask(r, deps, ownerID, sessionID, "所属用户组的存储后端已停用")
			writeBusinessError(w, http.StatusServiceUnavailable, "所属用户组的存储后端已停用，请联系管理员")
			return
		}
		failUploadTask(r, deps, ownerID, sessionID, "解析用户组存储绑定失败")
		writeBusinessError(w, http.StatusInternalServerError, "解析用户组存储绑定失败")
		return
	}
	blob, err := deps.Blobs.Create(r.Context(), parts, session.TotalSize, session.ExpectedSHA256,
		blobstore.Options{BackendID: backendID, Encrypt: settings.EncryptNewFiles, ChunkSize: chunkSize})
	if err != nil {
		if errors.Is(err, errUploadPartMissing) {
			failUploadTask(r, deps, ownerID, sessionID, "分片文件缺失")
			writeBusinessError(w, http.StatusConflict, "上传分片文件缺失，请取消该任务后重新上传")
			return
		}
		failUploadTask(r, deps, ownerID, sessionID, "完整文件校验失败")
		if errors.Is(err, blobstore.ErrChecksumMismatch) {
			writeBusinessError(w, http.StatusUnprocessableEntity, "完整文件校验失败")
			return
		}
		if errors.Is(err, blobstore.ErrEncryptionUnavailable) {
			writeBusinessError(w, http.StatusServiceUnavailable, "已开启新文件加密，但服务器未配置加密密钥，请联系管理员")
			return
		}
		// 未分类的存储错误必须留日志：客户端只看到泛化提示，排障依赖服务端记录。
		log.Printf("合并上传分片失败（会话=%s 大小=%d）：%v", sessionID, session.TotalSize, err)
		writeBusinessError(w, http.StatusInternalServerError, "合并上传分片失败")
		return
	}
	item, err := saveUploadedResource(r, deps, ownerID, session.ParentID, session.Filename, blob.ID, session.TotalSize, blob.SHA256, session.MimeType, session.ConflictAction, session.BatchID, session.ID)
	if err != nil {
		// 资源保存失败的新对象没有任何引用：降引用为 orphaned，交由管理员清理。
		_ = deps.Blobs.Release(r.Context(), blob.ID)
		failUploadTask(r, deps, ownerID, sessionID, "保存资源失败")
		writeResourceMutationError(w, err)
		return
	}
	_ = deps.FileStore.RemoveUploadSession(r.Context(), sessionID)
	writeJSON(w, http.StatusCreated, map[string]any{"status": "ok", "resource": item})
}

// partSequenceReader 按索引顺序流式读取分片文件：每个分片读完即关闭，避免一次
// 性打开全部分片（数千分片的大文件会耗尽文件描述符）。分片缺失/长度不足以
// errUploadPartMissing 包装返回，调用方据此给出"分片缺失"而非"校验失败"。
type partSequenceReader struct {
	ctx     context.Context
	store   *filestore.Store
	chunks  []upload.Chunk
	index   int
	current *os.File
	remain  int64
}

var errUploadPartMissing = errors.New("上传分片文件缺失")

func (p *partSequenceReader) Read(buffer []byte) (int, error) {
	for {
		if p.current == nil {
			if p.index >= len(p.chunks) {
				return 0, io.EOF
			}
			chunk := p.chunks[p.index]
			path, err := p.store.UploadChunkPath(p.ctx, chunk.RelativePath)
			if err != nil {
				return 0, err
			}
			file, err := os.Open(path)
			if err != nil {
				return 0, fmt.Errorf("%w: %v", errUploadPartMissing, err)
			}
			p.current = file
			p.remain = int64(chunk.SizeBytes)
		}
		if p.remain <= 0 {
			_ = p.current.Close()
			p.current = nil
			p.index++
			continue
		}
		limit := int64(len(buffer))
		if limit > p.remain {
			limit = p.remain
		}
		n, err := p.current.Read(buffer[:limit])
		p.remain -= int64(n)
		if err == io.EOF && p.remain > 0 {
			return n, fmt.Errorf("%w: 分片 %d 长度不足", errUploadPartMissing, p.index)
		}
		return n, err
	}
}

func (p *partSequenceReader) Close() error {
	if p.current != nil {
		return p.current.Close()
	}
	return nil
}

// requireUploadPermission 复核上传权限：会话创建后管理员可能撤销 upload 权限，
// 而会话最长可续期 7 天，分片与合并入口必须按当前权限复查。
func requireUploadPermission(w http.ResponseWriter, r *http.Request, deps Deps, ownerID int64) bool {
	owner, err := deps.UserRepo.GetByID(r.Context(), ownerID)
	if err != nil {
		writeBusinessError(w, http.StatusUnauthorized, "登录状态已失效，请重新登录")
		return false
	}
	if !owner.HasPermission(permission.Upload) {
		writeBusinessError(w, http.StatusForbidden, "没有上传权限")
		return false
	}
	return true
}

func saveUploadedResource(r *http.Request, deps Deps, ownerID int64, parentID *string, filename, blobID string, size int64, checksum, mimeType, conflictAction, uploadTaskID, uploadSessionID string) (resource.Resource, error) {
	settings, err := deps.AdminRepo.GetReviewSettings(r.Context())
	if err != nil {
		return resource.Resource{}, err
	}
	return deps.ResourceRepo.SaveUploadedFile(
		r.Context(), ownerID, parentID, filename, blobID, size, checksum,
		mimeType, conflictAction, settings.UploadRequiresReview,
		uploadTaskID, uploadSessionID,
	)
}

func overwriteStorageCredit(r *http.Request, deps Deps, ownerID int64, parentID *string, filename, conflictAction string) (int64, error) {
	if conflictAction != "overwrite" {
		return 0, nil
	}
	return deps.ResourceRepo.ExistingChildFileSize(r.Context(), ownerID, parentID, filename)
}

func validateUploadQuota(r *http.Request, deps Deps, ownerID, size, storageCredit int64, excludeSessionID string, includeStorage bool) error {
	profile, err := deps.QuotaRepo.GetEffectiveQuotaByUser(r.Context(), ownerID)
	if err != nil {
		return fmt.Errorf("读取上传配额失败")
	}
	hardLimit := hardUploadLimit
	if knobs, knobErr := deps.SystemSettings.GetKnobs(r.Context()); knobErr == nil && knobs.UploadMaxFileBytes > 0 {
		hardLimit = knobs.UploadMaxFileBytes
	}
	if size > hardLimit || (profile.SingleFileBytesLimit.Valid && size > profile.SingleFileBytesLimit.Int64) {
		return errQuotaSingleFile
	}
	// 超管豁免配额（保留 hardUploadLimit 硬上限）：否则把 default_user 配额调小
	// 会连带锁死超管自身。
	if quotaExempt(r.Context(), deps, ownerID) {
		return nil
	}
	dailyCount, dailyBytes, err := deps.ResourceRepo.UploadUsageSince(r.Context(), ownerID, startOfLocalDay(time.Now()))
	if err != nil {
		return fmt.Errorf("读取每日上传用量失败")
	}
	if profile.DailyUploadCountLimit.Valid && dailyCount >= profile.DailyUploadCountLimit.Int64 {
		return errQuotaDailyCount
	}
	if profile.DailyUploadBytesLimit.Valid && dailyBytes+size > profile.DailyUploadBytesLimit.Int64 {
		return errQuotaDailyBytes
	}
	// 在途预扣：进行中会话的声明字节同样占用磁盘，必须计入存储配额并限制并发
	// 会话数——否则反复建会话、传分片但永不 complete 就能绕过全部配额写满磁盘。
	pendingBytes, pendingCount, err := deps.UploadRepo.PendingUploadBytes(r.Context(), ownerID, excludeSessionID)
	if err != nil {
		return fmt.Errorf("读取在途上传用量失败")
	}
	if pendingCount >= maxPendingUploadSessions {
		return errQuotaActiveSessions
	}
	if includeStorage && profile.StorageBytesLimit.Valid {
		current, err := deps.ResourceRepo.TotalFileBytesByOwner(r.Context(), ownerID)
		if err != nil {
			return fmt.Errorf("读取存储配额失败")
		}
		if current+size-storageCredit+pendingBytes > profile.StorageBytesLimit.Int64 {
			return errQuotaStorage
		}
	}
	return nil
}

// 上传配额类错误：用哨兵错误分派 HTTP 状态码，而不是匹配错误文案——文案调整
// 不应静默改变状态码。文案本身即面向用户的提示。
var (
	errQuotaDailyCount     = errors.New("已达到每日上传次数限制")
	errQuotaDailyBytes     = errors.New("已达到每日上传流量限制")
	errQuotaStorage        = errors.New("存储空间不足")
	errQuotaSingleFile     = errors.New("文件超过单文件大小限制")
	errQuotaActiveSessions = errors.New("进行中的上传任务过多，请先完成或取消部分任务")
)

func uploadQuotaStatus(err error) int {
	switch {
	case errors.Is(err, errQuotaDailyCount), errors.Is(err, errQuotaActiveSessions):
		return http.StatusTooManyRequests
	case errors.Is(err, errQuotaDailyBytes),
		errors.Is(err, errQuotaStorage),
		errors.Is(err, errQuotaSingleFile):
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusInternalServerError
	}
}

// startOfLocalDay 返回服务器本地时区的当天零点：每日上传配额按自然日统计
// （滚动 24 小时窗口会把跨天的用量混在一起，与用户的"今天额度"直觉不符）。
func startOfLocalDay(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

// errGroupBackendDisabled 表示用户所属用户组绑定的存储后端已被停用。
var errGroupBackendDisabled = errors.New("所属用户组的存储后端已停用")

// resolveUploadBackend 解析本次上传应写入的后端 id：用户所属组绑定的后端优先，
// 未绑定返回 0（由存储服务回落全局默认后端）。
//
// 绑定后端被停用时返回 errGroupBackendDisabled：不静默回退到其它后端——静默
// 改写落盘位置会破坏「组 = 存储」的可预期性，也会让后续按组迁移的范围失真。
// 初始化与合并两个阶段共用，保证"传分片之前"就能拒绝不可用的目标。
func resolveUploadBackend(ctx context.Context, deps Deps, userID int64) (int64, error) {
	binding, err := deps.GroupRepo.EffectiveStorageBackend(ctx, userID)
	if err != nil {
		return 0, err
	}
	if binding.BackendID == 0 {
		return 0, nil
	}
	if !binding.Enabled {
		return 0, errGroupBackendDisabled
	}
	return binding.BackendID, nil
}

func failUploadTask(r *http.Request, deps Deps, ownerID int64, sessionID, message string) {
	_ = deps.UploadRepo.SetStatus(r.Context(), ownerID, sessionID, "failed", message)
}
