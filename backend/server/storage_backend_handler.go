package server

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/PYLinTech/XiaoyuPostHub/backend/admin"
	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
	"github.com/jackc/pgx/v5/pgconn"
)

// 存储后端管理（S4）：管理员在此登记本机 / 对象存储 / 第三方网盘后端。
//
// 设计约束：
//   - 敏感凭据（123 云盘 client_secret、对象存储 AK/SK）只存在于 .env；本接口读写
//     storage_backends.settings 中的参数（根目录 ID、直链开关、直链鉴权密钥）。
//     其中**直链鉴权密钥不回传浏览器**（读接口只返回"是否已配置"，编辑时留空表示
//     沿用原值），避免签名密钥随管理页面外泄；
//   - 保存后立即调用 blobstore.Reload，使配置生效（无需重启容器）；
//   - 停用后端不会删除对象元数据：已有对象仍按原后端读取，只是不再作为上传
//     目标。物理清理由管理员的存储维护任务负责。
type storageBackendRequest struct {
	ID        int64          `json:"id"`
	Name      string         `json:"name"`
	Kind      string         `json:"kind"`
	Settings  map[string]any `json:"settings"`
	IsEnabled bool           `json:"isEnabled"`
	IsDefault bool           `json:"isDefault"`
}

func handleAdminStorageBackends(w http.ResponseWriter, r *http.Request, deps Deps, u user.User) {
	switch r.Method {
	case http.MethodGet:
		listStorageBackends(w, r, deps)
	case http.MethodPut:
		saveStorageBackend(w, r, deps, u)
	default:
		writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func listStorageBackends(w http.ResponseWriter, r *http.Request, deps Deps) {
	backends, err := deps.AdminRepo.ListStorageBackends(r.Context())
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取存储后端失败")
		return
	}
	usage, err := deps.AdminRepo.StorageUsageByBackend(r.Context())
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取存储用量失败")
		return
	}
	loaded := deps.Blobs.LoadedBackends()
	items := make([]map[string]any, 0, len(backends))
	for _, backend := range backends {
		// 直链鉴权密钥是敏感配置：只回传"是否已配置"，不回传密钥本身（编辑时留空
		// 即沿用原值），避免签名密钥随管理页面外泄。
		settings := backend.Settings
		if backend.Kind == "pan123" {
			settings = make(map[string]any, len(backend.Settings)+1)
			for key, value := range backend.Settings {
				if key == "direct_link_auth_key" {
					continue
				}
				settings[key] = value
			}
			settings["direct_link_auth_key_set"] = stringSetting(backend.Settings, "direct_link_auth_key") != ""
		}
		items = append(items, map[string]any{
			"id": backend.ID, "name": backend.Name, "kind": backend.Kind,
			"settings": settings, "isEnabled": backend.IsEnabled,
			"isDefault": backend.IsDefault,
			// ready=false 表示该后端当前未被加载（配置不完整或凭据缺失）；
			// 与是否启用无关——停用的后端仍会加载以支撑存量对象的读取与迁移。
			"ready":     loaded[backend.ID],
			"usedBytes": usage[backend.ID],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "items": items,
		"supportedKinds":              []string{"local", "pan123", "s3"},
		"pan123CredentialsConfigured": deps.Blobs.Pan123Configured(),
		"s3CredentialsConfigured":     deps.Blobs.S3Configured(),
	})
}

// storageTaskRequest 是维护任务接口的请求体。
//
// 三种动作：
//   - scan：创建扫描任务，只产出"命中对象清单"，不改动任何数据；
//   - apply：按某个扫描任务的结果清单创建执行任务（SourceTaskID 必填）；
//   - run：直接执行（仅迁移——它由管理员选定原位置与新位置后立即开始）。
type storageTaskRequest struct {
	Action       string `json:"action"`
	Type         string `json:"type"`
	SourceTaskID int64  `json:"sourceTaskId"`
	// BackendID 为扫描范围（0=全部后端）；Source/TargetBackendID 为迁移的原/新位置。
	BackendID       int64 `json:"backendId"`
	SourceBackendID int64 `json:"sourceBackendId"`
	TargetBackendID int64 `json:"targetBackendId"`
	ChunkSize       int64 `json:"chunkSize"`
}

// handleAdminStorageTasks 存储维护任务：任务列表、扫描/执行、结果清单。
func handleAdminStorageTasks(w http.ResponseWriter, r *http.Request, deps Deps, u user.User, path string) {
	// 结果清单：GET /api/admin/storage-tasks/{id}/items
	if id, ok := strings.CutPrefix(path, "storage-tasks/"); ok && strings.HasSuffix(id, "/items") {
		if r.Method != http.MethodGet {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		taskID, err := strconv.ParseInt(strings.TrimSuffix(id, "/items"), 10, 64)
		if err != nil || taskID <= 0 {
			writeBusinessError(w, http.StatusBadRequest, "任务编号无效")
			return
		}
		items, err := deps.AdminRepo.ListStorageTaskItems(r.Context(), taskID, 200)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取任务结果失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "items": items})
		return
	}
	switch r.Method {
	case http.MethodGet:
		tasks, err := deps.AdminRepo.ListStorageTasks(r.Context(), 50)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取存储任务失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "items": tasks})
	case http.MethodPost:
		var req storageTaskRequest
		if err := decodeSmallJSON(w, r, &req); err != nil {
			writeBusinessError(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		var (
			task     admin.StorageTask
			auditKey string
		)
		switch req.Action {
		case "scan":
			kind := strings.TrimSpace(req.Type)
			taskType, ok := admin.ScanKindToTaskType(kind)
			if !ok {
				writeBusinessError(w, http.StatusBadRequest, "不支持的扫描类型")
				return
			}
			if taskType == "encrypt" || taskType == "decrypt" {
				if !deps.Blobs.EncryptionAvailable() {
					writeBusinessError(w, http.StatusBadRequest, "未配置加密密钥（XPH_ENCRYPTION_KEYS），无法加密或解密")
					return
				}
			}
			if taskType == "chunk" {
				// 先做 int64 范围检查再转 int32，避免超大值溢出成合法值。
				if req.ChunkSize <= 0 || req.ChunkSize > 1<<30 || !systemsetting.ValidStorageChunkSize(int32(req.ChunkSize)) {
					writeBusinessError(w, http.StatusBadRequest, "分片大小必须是 4MiB 的整数倍（4MiB~1GiB）")
					return
				}
			}
			if req.BackendID > 0 {
				backends, listErr := deps.AdminRepo.ListStorageBackends(r.Context())
				if listErr != nil {
					writeBusinessError(w, http.StatusInternalServerError, "读取存储后端失败")
					return
				}
				if _, found := findStorageBackend(backends, req.BackendID); !found {
					writeBusinessError(w, http.StatusBadRequest, "存储后端不存在")
					return
				}
			}
			created, err := deps.AdminRepo.CreateScanTask(r.Context(), kind, req.BackendID, req.ChunkSize, u.ID)
			if err != nil {
				log.Printf("创建扫描任务失败：%v", err)
				writeBusinessError(w, http.StatusInternalServerError, "创建扫描任务失败")
				return
			}
			task, auditKey = created, kind
		case "apply":
			if req.SourceTaskID <= 0 {
				writeBusinessError(w, http.StatusBadRequest, "请选择要执行的扫描任务")
				return
			}
			created, err := deps.AdminRepo.CreateTaskFromScan(r.Context(), req.SourceTaskID, u.ID)
			if err != nil {
				// 空结果、非扫描任务等都属于可预期的业务拒绝，直接回显原因。
				writeBusinessError(w, http.StatusBadRequest, err.Error())
				return
			}
			task, auditKey = created, "from_scan#"+strconv.FormatInt(req.SourceTaskID, 10)
		case "run", "":
			// 直接执行：目前只有迁移（孤立文件/加密/解密/分片都要求先扫描）。
			if req.Type != "migrate" {
				writeBusinessError(w, http.StatusBadRequest, "该操作需要先扫描，再按扫描结果执行")
				return
			}
			if req.TargetBackendID <= 0 {
				writeBusinessError(w, http.StatusBadRequest, "请选择新位置（目标存储后端）")
				return
			}
			// 新位置必须"已加载且启用"：停用的后端不能承接迁移写入。
			if !deps.Blobs.BackendEnabled(req.TargetBackendID) {
				writeBusinessError(w, http.StatusBadRequest, "目标存储后端当前未就绪或已停用")
				return
			}
			params := map[string]any{"target_backend_id": req.TargetBackendID}
			if req.SourceBackendID > 0 {
				params["source_backend_id"] = req.SourceBackendID
			}
			created, err := deps.AdminRepo.CreateStorageTask(r.Context(), "migrate", params, u.ID)
			if err != nil {
				log.Printf("创建迁移任务失败：%v", err)
				writeBusinessError(w, http.StatusInternalServerError, "创建迁移任务失败")
				return
			}
			task, auditKey = created, "migrate"
		default:
			writeBusinessError(w, http.StatusBadRequest, "不支持的维护操作")
			return
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), u.ID, u.Username, "storage_task.create",
			"storage_tasks", auditKey, map[string]any{
				"id": task.ID, "type": task.Type, "phase": task.Phase,
				"sourceTaskId": task.SourceTaskID, "params": task.Params, "totalItems": task.TotalItems,
			}, net.ParseIP(clientIP(r)))
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "task": task})
	default:
		writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// findStorageBackend 在存储后端清单里按 id 查找。
func findStorageBackend(backends []admin.StorageBackend, id int64) (admin.StorageBackend, bool) {
	for _, backend := range backends {
		if backend.ID == id {
			return backend, true
		}
	}
	return admin.StorageBackend{}, false
}

// defaultStorageBackendID 返回当前「启用 + 默认」的后端 id；不存在时返回 0。
func defaultStorageBackendID(backends []admin.StorageBackend) int64 {
	for _, backend := range backends {
		if backend.IsDefault && backend.IsEnabled {
			return backend.ID
		}
	}
	return 0
}

// boolSetting 从 JSON settings 中读取布尔值（兼容 true/false 与字符串形式）。
func boolSetting(settings map[string]any, key string) bool {
	switch value := settings[key].(type) {
	case bool:
		return value
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		return err == nil && parsed
	}
	return false
}

// existingPan123AuthKey 读取已保存的直链鉴权密钥（仅用于"编辑时留空 = 沿用原值"）。
// 该密钥不在读接口中回传，因此服务端必须自己取回。
func existingPan123AuthKey(ctx context.Context, deps Deps, backendID int64) (string, bool) {
	if backendID <= 0 {
		return "", false
	}
	backends, err := deps.AdminRepo.ListStorageBackends(ctx)
	if err != nil {
		return "", false
	}
	for _, backend := range backends {
		if backend.ID == backendID && backend.Kind == "pan123" {
			key := stringSetting(backend.Settings, "direct_link_auth_key")
			return key, key != ""
		}
	}
	return "", false
}

// stringSetting 从 JSON settings 中读取字符串值。
func stringSetting(settings map[string]any, key string) string {
	raw, ok := settings[key]
	if !ok {
		return ""
	}
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func saveStorageBackend(w http.ResponseWriter, r *http.Request, deps Deps, u user.User) {
	var req storageBackendRequest
	if err := decodeSmallJSON(w, r, &req); err != nil {
		writeBusinessError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len([]rune(req.Name)) > 64 {
		writeBusinessError(w, http.StatusBadRequest, "存储后端名称不能为空且不超过 64 字")
		return
	}
	switch req.Kind {
	case "local":
		// 本机后端无需额外参数。
	case "pan123":
		if !deps.Blobs.Pan123Configured() {
			writeBusinessError(w, http.StatusBadRequest, "未配置 123 云盘应用凭据（XPH_PAN123_CLIENT_ID / XPH_PAN123_CLIENT_SECRET）")
			return
		}
		parentID, ok := numericSetting(req.Settings, "parent_file_id")
		if !ok || parentID <= 0 {
			writeBusinessError(w, http.StatusBadRequest, "123 云盘后端必须填写存储根目录文件夹 ID（parent_file_id）")
			return
		}
		// 鉴权密钥不回传浏览器：编辑时留空表示沿用已保存的密钥（服务端自己取回）。
		if stringSetting(req.Settings, "direct_link_auth_key") == "" {
			if existing, found := existingPan123AuthKey(r.Context(), deps, req.ID); found {
				req.Settings["direct_link_auth_key"] = existing
			}
		}
		if boolSetting(req.Settings, "direct_link_auth") && stringSetting(req.Settings, "direct_link_auth_key") == "" {
			writeBusinessError(w, http.StatusBadRequest, "启用直链鉴权必须填写鉴权密钥（在 123 云盘直链管理的「鉴权管理」中开启并复制密钥）")
			return
		}
	case "s3":
		if !deps.Blobs.S3Configured() {
			writeBusinessError(w, http.StatusBadRequest, "未配置对象存储访问凭据（XPH_S3_ACCESS_KEY_ID / XPH_S3_SECRET_ACCESS_KEY）")
			return
		}
		endpoint := stringSetting(req.Settings, "endpoint")
		if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			writeBusinessError(w, http.StatusBadRequest, "对象存储 endpoint 必须是 http(s) 地址")
			return
		}
		if stringSetting(req.Settings, "bucket") == "" {
			writeBusinessError(w, http.StatusBadRequest, "对象存储必须填写 bucket")
			return
		}
	default:
		writeBusinessError(w, http.StatusBadRequest, "暂不支持该存储后端类型")
		return
	}
	if len(req.Settings) == 0 {
		req.Settings = map[string]any{}
	}
	// 保存前基于「模拟保存后的后端集合」做完整性校验（校验不通过绝不落库，
	// 否则会留下无默认后端的坏状态，重启后所有新上传都会失败）：
	//   - 必须仍存在「启用 + 默认」的后端（新上传的写入目标）；
	//   - 已有对象的后端禁止修改类型（定位符语义会变：路径 ↔ S3 key ↔ fileID）。
	existing, listErr := deps.AdminRepo.ListStorageBackends(r.Context())
	if listErr != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取存储后端失败")
		return
	}
	hasDefaultAfterSave := false
	for _, item := range existing {
		if item.ID == req.ID {
			if item.Kind != req.Kind {
				hasObjects, objectErr := deps.AdminRepo.StorageBackendHasObjects(r.Context(), req.ID)
				if objectErr != nil {
					// 查询失败时不能当作"没有对象"放行：类型变更会让存量对象
					// 的定位符语义失效，必须拒绝保存。
					writeBusinessError(w, http.StatusInternalServerError, "校验该后端是否已存放对象失败")
					return
				}
				if hasObjects {
					writeBusinessError(w, http.StatusBadRequest, "该后端已存放对象，不能修改存储类型")
					return
				}
			}
			continue // 该条目将被本次保存覆盖，以请求值为准
		}
		if item.IsEnabled && item.IsDefault {
			hasDefaultAfterSave = true
		}
	}
	if req.IsEnabled && req.IsDefault {
		hasDefaultAfterSave = true
	}
	if !hasDefaultAfterSave {
		writeBusinessError(w, http.StatusBadRequest, "必须保留一个启用的默认存储后端（新上传的写入目标）")
		return
	}
	saved, err := deps.AdminRepo.SaveStorageBackend(r.Context(), admin.StorageBackend{
		ID: req.ID, Name: req.Name, Kind: req.Kind, Settings: req.Settings,
		IsEnabled: req.IsEnabled, IsDefault: req.IsDefault,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if strings.Contains(pgErr.ConstraintName, "single_default") {
				writeBusinessError(w, http.StatusBadRequest, "默认存储后端只能有一个")
				return
			}
			writeBusinessError(w, http.StatusBadRequest, "存储后端名称已存在")
			return
		}
		writeBusinessError(w, http.StatusInternalServerError, "保存存储后端失败")
		return
	}
	// 立即重新加载后端，使配置生效；加载失败只影响新后端可用性，不阻断保存。
	if err := deps.Blobs.Reload(r.Context()); err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "存储后端已保存，但重新加载失败，请重启服务")
		return
	}
	// 审计不记录 settings 原文：其中虽只应含非敏感参数，但接口不校验键集合，
	// 避免管理员误填密钥类信息后被审计日志回显。
	_ = deps.AdminRepo.WriteAudit(r.Context(), u.ID, u.Username, "storage_backend.save",
		"storage_backends", saved.Name, map[string]any{
			"id": saved.ID, "name": saved.Name, "kind": saved.Kind,
			"isEnabled": saved.IsEnabled, "isDefault": saved.IsDefault,
		}, net.ParseIP(clientIP(r)))
	listStorageBackends(w, r, deps)
}

// numericSetting 从 JSON settings 中读取数值（前端可能传 number 或字符串）。
func numericSetting(settings map[string]any, key string) (int64, bool) {
	raw, ok := settings[key]
	if !ok {
		return 0, false
	}
	switch value := raw.(type) {
	case float64:
		return int64(value), true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}
