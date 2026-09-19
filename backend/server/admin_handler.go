package server

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/PYLinTech/XiaoyuPostHub/backend/admin"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
	"github.com/jackc/pgx/v5/pgtype"
)

type adminSystemConfigRequest struct {
	SiteName                     string `json:"siteName"`
	StoragePath                  string `json:"storagePath"`
	InvitationCodeLength         int16  `json:"invitationCodeLength"`
	InvitationCodeCaseSensitive  bool   `json:"invitationCodeCaseSensitive"`
	InvitationCodeIncludeLetters bool   `json:"invitationCodeIncludeLetters"`
	InvitationCodeIncludeNumbers bool   `json:"invitationCodeIncludeNumbers"`
	ShareCodeLength              int16  `json:"shareCodeLength"`
	ShareCodeCaseSensitive       bool   `json:"shareCodeCaseSensitive"`
	ShareCodeIncludeLetters      bool   `json:"shareCodeIncludeLetters"`
	ShareCodeIncludeNumbers      bool   `json:"shareCodeIncludeNumbers"`
	PickupCodeLength             int16  `json:"pickupCodeLength"`
	PickupCodeCaseSensitive      bool   `json:"pickupCodeCaseSensitive"`
	PickupCodeIncludeLetters     bool   `json:"pickupCodeIncludeLetters"`
	PickupCodeIncludeNumbers     bool   `json:"pickupCodeIncludeNumbers"`
	PickupMaxLifetimeSeconds     *int64 `json:"pickupMaxLifetimeSeconds"`
	LoginTOTPEnabled             bool   `json:"loginTOTPEnabled"`
	UploadRequiresReview         bool   `json:"uploadRequiresReview"`
	CustomShareRequiresReview    bool   `json:"customShareRequiresReview"`
	UploadChunkSizeBytes         int32  `json:"uploadChunkSizeBytes"`
	UploadTaskChunkConcurrency   int16  `json:"uploadTaskChunkConcurrency"`
	UploadUserTaskConcurrency    int16  `json:"uploadUserTaskConcurrency"`
	TrashRetentionDays           int16  `json:"trashRetentionDays"`
	EncryptNewFiles              bool   `json:"encryptNewFiles"`
	ShareRetrievalMode           string `json:"shareRetrievalMode"`
	StorageChunkSizeBytes        int32  `json:"storageChunkSizeBytes"`
	// 302 取数不可用时是否自动降级本机中转（默认开启）。
	RedirectFallback bool `json:"redirectFallback"`
	// 允许创建/修改"永久有效"的取件码分享（管理端开关）。
	PickupAllowPermanent bool `json:"pickupAllowPermanent"`
	// 邀请码有效期（天，0 = 永久）。
	InvitationValidDays int32 `json:"invitationValidDays"`
	// 单文件系统硬上限（字节）。
	UploadMaxFileBytes int64 `json:"uploadMaxFileBytes"`
	// CrossUserDedupe 秒传是否允许跨用户复用物理对象。缺省（未传）= false，即
	// 收紧为"仅复用本人对象"——对隐私是更安全的方向，不会因漏传字段而放宽。
	CrossUserDedupe bool `json:"crossUserDedupe"`
}

func nullableInt64(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

type adminInvitationIssueRequest struct {
	TargetType string `json:"targetType"`
	TargetID   int64  `json:"targetId"`
	Quantity   int    `json:"quantity"`
}

type adminInvitationSettingsRequest struct {
	RegistrationRequiresInvitation bool `json:"registrationRequiresInvitation"`
}

type adminCreateGroupRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type adminGroupMembersRequest struct {
	UserIDs []int64 `json:"userIds"`
}

type adminUserGroupsRequest struct {
	GroupIDs []int64 `json:"groupIds"`
}

// adminResetPasswordRequest 是解密后的重设密码载荷（外层是加密信封）。
type adminResetPasswordRequest struct {
	Password string `json:"password"`
}

type adminUserDisabledRequest struct {
	Disabled bool `json:"disabled"`
}

type adminQuotaRequest struct {
	Name                    string `json:"name"`
	Description             string `json:"description"`
	StorageBytesLimit       *int64 `json:"storageBytesLimit"`
	SingleFileBytesLimit    *int64 `json:"singleFileBytesLimit"`
	DailyUploadBytesLimit   *int64 `json:"dailyUploadBytesLimit"`
	DailyUploadCountLimit   *int64 `json:"dailyUploadCountLimit"`
	DailyDownloadBytesLimit *int64 `json:"dailyDownloadBytesLimit"`
	DailyDownloadCountLimit *int64 `json:"dailyDownloadCountLimit"`
	ActiveShareCountLimit   *int64 `json:"activeShareCountLimit"`
	ActiveDirectLinkLimit   *int64 `json:"activeDirectLinkLimit"`
}

type adminGroupPermissionsRequest struct {
	Permissions []string `json:"permissions"`
}

type adminGroupQuotaRequest struct {
	QuotaProfileID *int64 `json:"quotaProfileId"`
	Priority       int32  `json:"priority"`
}

func adminHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := requireManagementUser(w, r, deps)
		if !ok {
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/admin/")
		if path == "users" || strings.HasPrefix(path, "users/") {
			required := permission.ManageUsers
			if path == "users" && r.Method == http.MethodGet {
				if !requireAnyAdminPermission(w, u, permission.ManageUsers, permission.ManageUserGroups) {
					return
				}
			} else if strings.HasSuffix(path, "/groups") {
				required = permission.ManageUserGroups
				if !requireAdminPermission(w, u, required) {
					return
				}
			} else if !requireAdminPermission(w, u, required) {
				return
			}
			handleAdminUsers(w, r, deps, u, path)
			return
		}
		if path == "user-groups" || strings.HasPrefix(path, "user-groups/") {
			if !requireAdminPermission(w, u, permission.ManageUserGroups) {
				return
			}
			handleAdminUserGroups(w, r, deps, u, path)
			return
		}
		if path == "access" || strings.HasPrefix(path, "access/") {
			// 系统管理员也要能进入 access 页面（用户组存储绑定属于 ManageSystem）；
			// 各子分支内部仍各自校验所需权限，这里只做门禁放行。
			if !requireAnyAdminPermission(w, u, permission.ManagePermissions, permission.ManageQuotas, permission.ManageSystem) {
				return
			}
			handleAdminAccess(w, r, deps, u, path)
			return
		}
		if path == "shares" || strings.HasPrefix(path, "shares/") {
			// 分享与取件码管理：可随时改期（含永久）、启停、删除，并一键释放失效
			// 取件码腾出码空间。
			if !requireAdminPermission(w, u, permission.ReviewShares) {
				return
			}
			handleAdminShares(w, r, deps, u, path)
			return
		}
		if path == "uploads" || strings.HasPrefix(path, "uploads/") {
			// 在途上传任务：占用临时盘，管理员可查看并直接取消（无需等过期）。
			if !requireAdminPermission(w, u, permission.ManageSystem) {
				return
			}
			handleAdminUploads(w, r, deps, u, path)
			return
		}
		if path == "invitations" || strings.HasPrefix(path, "invitations/") {
			if !requireAdminPermission(w, u, permission.ManageInvitations) {
				return
			}
			handleAdminInvitations(w, r, deps, u, path)
			return
		}
		if path == "reviews/files" || strings.HasPrefix(path, "reviews/files/") {
			if !requireAdminPermission(w, u, permission.ReviewFiles) {
				return
			}
			handleAdminReviews(w, r, deps, u, path)
			return
		}
		if path == "reviews/shares" || strings.HasPrefix(path, "reviews/shares/") {
			if !requireAdminPermission(w, u, permission.ReviewShares) {
				return
			}
			handleAdminReviews(w, r, deps, u, path)
			return
		}
		// 存储维护任务：任务列表 / 扫描 / 执行 / 结果清单（storage-tasks/{id}/items）。
		if path == "storage-tasks" || strings.HasPrefix(path, "storage-tasks/") {
			if !requireAdminPermission(w, u, permission.ManageSystem) {
				return
			}
			handleAdminStorageTasks(w, r, deps, u, path)
			return
		}
		switch path {
		case "overview":
			if !requireAdminPermission(w, u, permission.ViewAdminOverview) {
				return
			}
			if r.Method != http.MethodGet {
				writeBusinessError(w, 405, "method not allowed")
				return
			}
			// 磁盘统计用部署级配置的宿主路径（默认 "/"），不依赖可改的存储路径，
			// 也不会因为本地存储目录异常而让整个概览接口失败。
			data, err := deps.AdminRepo.GetOverview(r.Context(), deps.HostDiskPath)
			if err != nil {
				writeBusinessError(w, 500, "读取实时概览失败")
				return
			}
			if !data.StorageDiskAvailable {
				log.Printf("实时概览：读取宿主磁盘信息失败 path=%s", deps.HostDiskPath)
			}
			writeJSON(w, 200, map[string]any{"status": "ok", "data": data})
		case "audit":
			if !requireAdminPermission(w, u, permission.ReadAuditLog) {
				return
			}
			if r.Method != http.MethodGet {
				writeBusinessError(w, 405, "method not allowed")
				return
			}
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			items, err := deps.AdminRepo.ListAudit(r.Context(), limit)
			if err != nil {
				writeBusinessError(w, 500, "读取审计日志失败")
				return
			}
			writeJSON(w, 200, map[string]any{"status": "ok", "items": items})
		case "system-config":
			if !requireAdminPermission(w, u, permission.ManageSystem) {
				return
			}
			handleAdminSystemConfig(w, r, deps, u)
		case "system-config/upload-test":
			if !requireAdminPermission(w, u, permission.ManageSystem) {
				return
			}
			handleAdminUploadTest(w, r)
		case "storage-backends":
			if !requireAdminPermission(w, u, permission.ManageSystem) {
				return
			}
			handleAdminStorageBackends(w, r, deps, u)
		case "site-icon":
			if !requireAdminPermission(w, u, permission.ManageSystem) {
				return
			}
			handleAdminSiteIcon(w, r, deps, u)
		case "homepage":
			if !requireAdminPermission(w, u, permission.ManageSystem) {
				return
			}
			handleAdminHomepage(w, r, deps, u)
		default:
			writeBusinessError(w, 404, "管理接口不存在")
		}
	}
}

func handleAdminAccess(w http.ResponseWriter, r *http.Request, deps Deps, actor user.User, path string) {
	if path == "access" && r.Method == http.MethodGet {
		// 配额方案只返回给有权管理配额/系统的管理员：其余角色（如仅管理权限的
		// 管理员）不需要看到限额明细。
		var quotas []admin.QuotaItem
		if actor.IsSuperAdmin() || actor.HasPermission(permission.ManageQuotas) || actor.HasPermission(permission.ManageSystem) {
			listed, listErr := deps.AdminRepo.ListQuotaProfiles(r.Context())
			if listErr != nil {
				writeBusinessError(w, http.StatusInternalServerError, "读取配额方案失败")
				return
			}
			quotas = listed
		}
		groups, err := deps.AdminRepo.ListAccessGroups(r.Context())
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取用户组配置失败")
			return
		}
		definitions := make([]map[string]string, 0, len(permission.Definitions))
		for _, definition := range permission.Definitions {
			definitions = append(definitions, map[string]string{
				"code":          definition.Code,
				"description":   definition.Description,
				"descriptionEn": definition.DescriptionEN,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok", "quotas": quotas, "groups": groups,
			"availablePermissions": definitions,
		})
		return
	}
	if path == "access/quotas" && r.Method == http.MethodPost {
		if !requireAdminPermission(w, actor, permission.ManageQuotas) {
			return
		}
		var req adminQuotaRequest
		if !decodeQuotaRequest(w, r, &req) {
			return
		}
		item, err := deps.QuotaRepo.CreateQuotaProfile(r.Context(), strings.TrimSpace(strings.ToLower(req.Name)), strings.TrimSpace(req.Description),
			req.StorageBytesLimit, req.SingleFileBytesLimit, req.DailyUploadBytesLimit,
			req.DailyUploadCountLimit, req.DailyDownloadBytesLimit, req.DailyDownloadCountLimit,
			req.ActiveShareCountLimit, req.ActiveDirectLinkLimit)
		if err != nil {
			writeBusinessError(w, http.StatusBadRequest, "配额方案名称重复或格式无效")
			return
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "quota.create", "quota_profile", item.Name, map[string]any{"id": item.ID}, net.ParseIP(clientIP(r)))
		writeJSON(w, http.StatusCreated, map[string]any{"status": "ok"})
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "access/"), "/")
	if len(parts) == 2 && parts[0] == "quotas" {
		if !requireAdminPermission(w, actor, permission.ManageQuotas) {
			return
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || id < 1 {
			writeBusinessError(w, http.StatusBadRequest, "配额方案编号无效")
			return
		}
		if r.Method == http.MethodPut {
			var req adminQuotaRequest
			if !decodeQuotaRequest(w, r, &req) {
				return
			}
			if err := deps.QuotaRepo.UpdateQuotaProfile(r.Context(), id, strings.TrimSpace(req.Description),
				req.StorageBytesLimit, req.SingleFileBytesLimit, req.DailyUploadBytesLimit,
				req.DailyUploadCountLimit, req.DailyDownloadBytesLimit, req.DailyDownloadCountLimit,
				req.ActiveShareCountLimit, req.ActiveDirectLinkLimit); err != nil {
				writeBusinessError(w, http.StatusBadRequest, "更新配额方案失败")
				return
			}
			_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "quota.update", "quota_profile", strconv.FormatInt(id, 10), map[string]any{}, net.ParseIP(clientIP(r)))
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
			return
		}
		if r.Method == http.MethodDelete {
			if err := deps.QuotaRepo.DeleteQuotaProfile(r.Context(), id); err != nil {
				writeBusinessError(w, http.StatusConflict, "系统配额方案不可删除，或方案不存在")
				return
			}
			_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "quota.delete", "quota_profile", strconv.FormatInt(id, 10), map[string]any{}, net.ParseIP(clientIP(r)))
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
			return
		}
	}
	if len(parts) == 3 && parts[0] == "groups" && r.Method == http.MethodPut {
		groupID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || groupID < 1 {
			writeBusinessError(w, http.StatusBadRequest, "用户组编号无效")
			return
		}
		switch parts[2] {
		case "permissions":
			if !requireAdminPermission(w, actor, permission.ManagePermissions) {
				return
			}
			var req adminGroupPermissionsRequest
			if err := decodeSmallJSON(w, r, &req); err != nil || len(req.Permissions) > len(permission.All) {
				writeBusinessError(w, http.StatusBadRequest, "权限配置无效")
				return
			}
			unique := make(map[string]struct{}, len(req.Permissions))
			codes := make([]string, 0, len(req.Permissions))
			for _, code := range req.Permissions {
				if !permission.IsValid(code) {
					writeBusinessError(w, http.StatusBadRequest, "包含未知权限")
					return
				}
				// 不能授予操作者自身不具备的能力：否则持有 ManagePermissions
				// 的管理员可以给自己所在组加上 manage_system 等更高权限（提权）。
				if !actor.IsSuperAdmin() && !actor.HasPermission(code) {
					writeBusinessError(w, http.StatusForbidden, "不能授予超出自身权限范围的能力")
					return
				}
				if _, exists := unique[code]; !exists {
					unique[code] = struct{}{}
					codes = append(codes, code)
				}
			}
			// 系统用户组必须保留登录权限：default_user 是全站兜底组，移除 login
			// 会让仅依赖该组的用户（可能包括操作者本人）立即无法登录。
			if info, infoErr := deps.GroupRepo.GetByID(r.Context(), groupID); infoErr != nil {
				writeBusinessError(w, http.StatusBadRequest, "用户组不存在")
				return
			} else if info.IsSystem && !containsCode(codes, permission.Login) {
				writeBusinessError(w, http.StatusBadRequest, "系统用户组必须保留登录权限")
				return
			}
			if err := deps.AdminRepo.SetGroupPermissions(r.Context(), groupID, codes); err != nil {
				writeBusinessError(w, http.StatusBadRequest, "保存用户组权限失败")
				return
			}
			_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "group.permissions.update", "user_group", strconv.FormatInt(groupID, 10), map[string]any{"permissions": codes}, net.ParseIP(clientIP(r)))
		case "quota":
			if !requireAdminPermission(w, actor, permission.ManageQuotas) {
				return
			}
			var req adminGroupQuotaRequest
			if err := decodeSmallJSON(w, r, &req); err != nil || req.QuotaProfileID == nil {
				writeBusinessError(w, http.StatusBadRequest, "用户组配额参数无效")
				return
			}
			if _, err := deps.QuotaRepo.GetByID(r.Context(), *req.QuotaProfileID); err != nil {
				writeBusinessError(w, http.StatusBadRequest, "配额方案不存在")
				return
			}
			if err := deps.GroupRepo.UpdateGroupQuotaProfile(r.Context(), groupID, *req.QuotaProfileID); err != nil {
				writeBusinessError(w, http.StatusBadRequest, "保存用户组配额失败")
				return
			}
			if err := deps.GroupRepo.UpdateGroupPriority(r.Context(), groupID, req.Priority); err != nil {
				writeBusinessError(w, http.StatusBadRequest, "保存用户组优先级失败")
				return
			}
			_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "group.quota.update", "user_group", strconv.FormatInt(groupID, 10), map[string]any{"quotaProfileId": req.QuotaProfileID, "priority": req.Priority}, net.ParseIP(clientIP(r)))
		case "storage":
			if !requireAdminPermission(w, actor, permission.ManageSystem) {
				return
			}
			var req struct {
				StorageBackendID *int64 `json:"storageBackendId"`
			}
			if err := decodeSmallJSON(w, r, &req); err != nil {
				writeBusinessError(w, http.StatusBadRequest, "存储绑定参数无效")
				return
			}
			backends, err := deps.AdminRepo.ListStorageBackends(r.Context())
			if err != nil {
				writeBusinessError(w, http.StatusInternalServerError, "读取存储后端失败")
				return
			}
			// 组必须存在；旧绑定用于判定是否需要迁移。
			groupInfo, err := deps.GroupRepo.GetByID(r.Context(), groupID)
			if err != nil {
				writeBusinessError(w, http.StatusBadRequest, "用户组不存在")
				return
			}
			oldBackendID := int64(0)
			if groupInfo.StorageBackendID.Valid {
				oldBackendID = groupInfo.StorageBackendID.Int64
			} else {
				// 未绑定不等于"没有存储位置"：此时对象写在全局默认后端上。旧位置
				// 取默认后端，否则"解绑 → 绑定非默认后端"会漏掉迁移，对象的实际
				// 位置与新绑定不一致。
				oldBackendID = defaultStorageBackendID(backends)
			}
			targetBackendID := int64(0)
			if req.StorageBackendID != nil {
				// 绑定目标必须"已加载且启用"：DB 里启用但凭据缺失/未就绪的后端
				// 会接住上传请求却在合并阶段失败，必须提前拒绝。
				if _, ok := findStorageBackend(backends, *req.StorageBackendID); !ok {
					writeBusinessError(w, http.StatusBadRequest, "存储后端不存在")
					return
				}
				if !deps.Blobs.BackendEnabled(*req.StorageBackendID) {
					writeBusinessError(w, http.StatusBadRequest, "存储后端当前不可用（未启用或未就绪）")
					return
				}
				targetBackendID = *req.StorageBackendID
			} else {
				// 解绑：回退全局默认后端，迁移目标即它。
				targetBackendID = defaultStorageBackendID(backends)
			}
			if err := deps.GroupRepo.UpdateGroupStorageBackend(r.Context(), groupID, req.StorageBackendID); err != nil {
				writeBusinessError(w, http.StatusBadRequest, "保存用户组存储绑定失败")
				return
			}
			// 绑定变化且有旧值时，把该组存量对象迁到新目标：先取消先前排队、
			// 目标可能已过期的同类任务，避免对象被搬到不再对应该组的后端。
			var migrationTaskID any // int64 或 nil（JSON null）
			migrationError := ""
			if oldBackendID != 0 && targetBackendID != 0 && targetBackendID != oldBackendID {
				if _, cancelErr := deps.AdminRepo.CancelQueuedScopedMigrations(r.Context(), groupID); cancelErr != nil {
					log.Printf("取消用户组 %d 的排队迁移任务失败：%v", groupID, cancelErr)
				}
				task, createErr := deps.AdminRepo.CreateStorageTask(r.Context(), "migrate", map[string]any{
					"target_backend_id": targetBackendID,
					"scope_group_id":    groupID,
				}, actor.ID)
				if createErr != nil {
					// 绑定已保存但迁移未创建：明确回报给前端，由管理员手动发起。
					log.Printf("用户组 %d 切换存储绑定后创建迁移任务失败：%v", groupID, createErr)
					migrationError = "自动迁移任务创建失败，请在存储任务页手动发起迁移"
				} else {
					migrationTaskID = task.ID
				}
			}
			_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "group.storage.update", "user_group", strconv.FormatInt(groupID, 10), map[string]any{
				"oldBackendId": oldBackendID, "storageBackendId": targetBackendID, "migrationTaskId": migrationTaskID,
			}, net.ParseIP(clientIP(r)))
			writeJSON(w, http.StatusOK, map[string]any{
				"status":           "ok",
				"migrationStarted": migrationTaskID != nil,
				"migrationTaskId":  migrationTaskID,
				"migrationError":   migrationError,
			})
			return
		default:
			writeBusinessError(w, http.StatusNotFound, "权限与配额接口不存在")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		return
	}
	writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func decodeQuotaRequest(w http.ResponseWriter, r *http.Request, req *adminQuotaRequest) bool {
	if err := decodeSmallJSON(w, r, req); err != nil || len(req.Name) > 32 || len(req.Description) > 500 {
		writeBusinessError(w, http.StatusBadRequest, "配额方案参数无效")
		return false
	}
	if req.Name != "" {
		name := strings.TrimSpace(strings.ToLower(req.Name))
		if len(name) < 2 {
			writeBusinessError(w, http.StatusBadRequest, "配额方案名称无效")
			return false
		}
	}
	limits := []*int64{req.StorageBytesLimit, req.SingleFileBytesLimit, req.DailyUploadBytesLimit,
		req.DailyUploadCountLimit, req.DailyDownloadBytesLimit, req.DailyDownloadCountLimit,
		req.ActiveShareCountLimit, req.ActiveDirectLinkLimit}
	for _, limit := range limits {
		if limit != nil && *limit < 0 {
			writeBusinessError(w, http.StatusBadRequest, "配额不能为负数")
			return false
		}
	}
	return true
}

func handleAdminUsers(w http.ResponseWriter, r *http.Request, deps Deps, actor user.User, path string) {
	if path == "users" {
		if r.Method != http.MethodGet {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		items, err := deps.AdminRepo.ListUsers(r.Context())
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取用户列表失败")
			return
		}
		groups, err := deps.AdminRepo.ListUserGroups(r.Context())
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取用户组失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok", "items": items, "groups": groups,
			"superAdminUsername": user.EnvSuperAdminName(),
		})
		return
	}

	parts := strings.Split(strings.TrimPrefix(path, "users/"), "/")
	if len(parts) < 1 || len(parts) > 2 {
		writeBusinessError(w, http.StatusNotFound, "用户管理接口不存在")
		return
	}
	userID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || userID < 1 {
		writeBusinessError(w, http.StatusBadRequest, "用户编号无效")
		return
	}
	targetName, err := deps.AdminRepo.GetUsername(r.Context(), userID)
	if errors.Is(err, admin.ErrUserNotFound) {
		writeBusinessError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取目标用户失败")
		return
	}
	if targetName == user.EnvSuperAdminName() {
		writeBusinessError(w, http.StatusForbidden, "系统超级管理员不能通过管理面板修改")
		return
	}
	if userID == actor.ID && (r.Method == http.MethodDelete || (len(parts) == 2 && parts[1] == "disabled")) {
		writeBusinessError(w, http.StatusConflict, "不能删除或禁用当前登录账户")
		return
	}
	// 非超级管理员不得操作"权限超出自身范围"的账户：否则持有 ManageUsers 的管理员
	// 可以重置更高权限管理员的密码（重设会连带清空其全部会话）后登录该账号，或直接
	// 禁用/删除该管理员——与用户组、邀请码已有的越级防护保持一致。
	if r.Method != http.MethodGet {
		if status, msg := ensureActorCoversUser(r.Context(), deps, actor, userID); msg != "" {
			writeBusinessError(w, status, msg)
			return
		}
	}

	if len(parts) == 1 && r.Method == http.MethodDelete {
		uploadSessionIDs := []string{}
		if deps.UploadRepo != nil {
			ids, listErr := deps.UploadRepo.ListIDsOwned(r.Context(), userID)
			if listErr != nil {
				log.Printf("读取待删除用户 %d 的上传会话失败: %v", userID, listErr)
			} else {
				uploadSessionIDs = ids
			}
		}
		username, blobIDs, err := deps.AdminRepo.DeleteUser(r.Context(), userID, actor.Username)
		if errors.Is(err, admin.ErrUserNotFound) {
			writeBusinessError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "删除用户失败")
			return
		}
		// 物理对象只降引用：归零后标记 orphaned，由管理员清理工具处理。
		for _, blobID := range blobIDs {
			if deps.Blobs != nil {
				if err := deps.Blobs.Release(r.Context(), blobID); err != nil {
					log.Printf("释放已删除用户 %d 的对象 %q 失败: %v", userID, blobID, err)
				}
			}
		}
		for _, sessionID := range uploadSessionIDs {
			if deps.FileStore != nil {
				if err := deps.FileStore.RemoveUploadSession(r.Context(), sessionID); err != nil {
					log.Printf("清理已删除用户 %d 的上传分片失败 id=%s: %v", userID, sessionID, err)
				}
			}
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "user.delete", "user", username, map[string]any{}, net.ParseIP(clientIP(r)))
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		return
	}
	if len(parts) != 2 || r.Method != http.MethodPut {
		writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	switch parts[1] {
	case "groups":
		var req adminUserGroupsRequest
		if err := decodeSmallJSON(w, r, &req); err != nil || len(req.GroupIDs) > 100 {
			writeBusinessError(w, http.StatusBadRequest, "用户组参数无效")
			return
		}
		// 成员归属与"下发组权限"受同一约束：非超级管理员只能把用户分配到
		// 权限不超出自身的组，且不能修改自己的归属——否则持有 ManageUserGroups
		// 的管理员可以把自己加入高权限组实现提权。
		if status, msg := ensureGroupsWithinActor(r.Context(), deps, actor, userID, req.GroupIDs); msg != "" {
			writeBusinessError(w, status, msg)
			return
		}
		username, err := deps.AdminRepo.SetUserGroups(r.Context(), userID, req.GroupIDs)
		if errors.Is(err, admin.ErrGroupNotFound) || errors.Is(err, admin.ErrUserWithoutGroup) ||
			errors.Is(err, admin.ErrGroupGuestNotAssignable) {
			writeBusinessError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "更新用户组失败")
			return
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "user.groups.update", "user", username, map[string]any{"groupIds": req.GroupIDs}, net.ParseIP(clientIP(r)))
	case "password":
		var req adminResetPasswordRequest
		if !openSealedPayload(w, r, deps, &req) {
			return
		}
		if err := user.ValidateNewPassword(req.Password); err != nil {
			writeBusinessError(w, http.StatusBadRequest, passwordPolicyMessage(err))
			return
		}
		hash, err := user.HashPassword(req.Password)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "生成密码摘要失败")
			return
		}
		username, err := deps.AdminRepo.ResetUserPassword(r.Context(), userID, hash)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "重设密码失败")
			return
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "user.password.reset", "user", username, map[string]any{}, net.ParseIP(clientIP(r)))
	case "disabled":
		var req adminUserDisabledRequest
		if err := decodeSmallJSON(w, r, &req); err != nil {
			writeBusinessError(w, http.StatusBadRequest, "用户状态参数无效")
			return
		}
		username, err := deps.AdminRepo.SetUserDisabled(r.Context(), userID, req.Disabled)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "更新用户状态失败")
			return
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "user.status.update", "user", username, map[string]any{"disabled": req.Disabled}, net.ParseIP(clientIP(r)))
	default:
		writeBusinessError(w, http.StatusNotFound, "用户管理接口不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func handleAdminUserGroups(w http.ResponseWriter, r *http.Request, deps Deps, actor user.User, path string) {
	if path == "user-groups" {
		if r.Method != http.MethodPost {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req adminCreateGroupRequest
		if err := decodeSmallJSON(w, r, &req); err != nil || len(req.Name) > 32 || len(req.Description) > 500 {
			writeBusinessError(w, http.StatusBadRequest, "用户组参数无效")
			return
		}
		item, err := deps.AdminRepo.CreateUserGroup(r.Context(), req.Name, req.Description)
		if errors.Is(err, admin.ErrGroupNameExists) || errors.Is(err, admin.ErrGroupInput) {
			writeBusinessError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "新增用户组失败")
			return
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "user_group.create", "user_group", item.Name, map[string]any{"id": item.ID}, net.ParseIP(clientIP(r)))
		writeJSON(w, http.StatusCreated, map[string]any{"status": "ok", "group": item})
		return
	}

	parts := strings.Split(strings.TrimPrefix(path, "user-groups/"), "/")
	if len(parts) < 1 || len(parts) > 2 {
		writeBusinessError(w, http.StatusNotFound, "用户组管理接口不存在")
		return
	}
	groupID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || groupID < 1 {
		writeBusinessError(w, http.StatusBadRequest, "用户组编号无效")
		return
	}
	if len(parts) == 2 && parts[1] == "members" && r.Method == http.MethodPut {
		var req adminGroupMembersRequest
		if err := decodeSmallJSON(w, r, &req); err != nil || len(req.UserIDs) > 10000 {
			writeBusinessError(w, http.StatusBadRequest, "用户组成员参数无效")
			return
		}
		// 不能管理"权限超出自身"的组成员：否则可借高权限组提权。
		if status, msg := ensureActorCoversGroup(r.Context(), deps, actor, groupID); msg != "" {
			writeBusinessError(w, status, msg)
			return
		}
		name, err := deps.AdminRepo.SetUserGroupMembers(r.Context(), groupID, req.UserIDs, user.EnvSuperAdminName())
		if errors.Is(err, admin.ErrGroupNotFound) || errors.Is(err, admin.ErrUserNotFound) ||
			errors.Is(err, admin.ErrUserWithoutGroup) || errors.Is(err, admin.ErrGroupGuestNotAssignable) {
			writeBusinessError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "配置用户组成员失败")
			return
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "user_group.members.update", "user_group", name, map[string]any{"userIds": req.UserIDs}, net.ParseIP(clientIP(r)))
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		return
	}
	if len(parts) != 1 {
		writeBusinessError(w, http.StatusNotFound, "用户组管理接口不存在")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var req adminCreateGroupRequest
		if err := decodeSmallJSON(w, r, &req); err != nil || len(req.Name) > 32 || len(req.Description) > 500 {
			writeBusinessError(w, http.StatusBadRequest, "用户组参数无效")
			return
		}
		item, err := deps.AdminRepo.UpdateUserGroup(r.Context(), groupID, req.Name, req.Description)
		if errors.Is(err, admin.ErrGroupNotFound) {
			writeBusinessError(w, http.StatusNotFound, err.Error())
			return
		}
		if errors.Is(err, admin.ErrGroupIsSystem) {
			writeBusinessError(w, http.StatusForbidden, err.Error())
			return
		}
		if errors.Is(err, admin.ErrGroupNameExists) || errors.Is(err, admin.ErrGroupInput) {
			writeBusinessError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "更新用户组失败")
			return
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "user_group.update", "user_group", item.Name, map[string]any{"id": item.ID}, net.ParseIP(clientIP(r)))
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "group": item})
	case http.MethodDelete:
		name, err := deps.AdminRepo.DeleteUserGroup(r.Context(), groupID)
		if errors.Is(err, admin.ErrGroupNotFound) {
			writeBusinessError(w, http.StatusNotFound, err.Error())
			return
		}
		if errors.Is(err, admin.ErrGroupIsSystem) {
			writeBusinessError(w, http.StatusForbidden, err.Error())
			return
		}
		if errors.Is(err, admin.ErrGroupSoleMembers) {
			writeBusinessError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "删除用户组失败")
			return
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "user_group.delete", "user_group", name, map[string]any{"id": groupID}, net.ParseIP(clientIP(r)))
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	default:
		writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// groupPermissionIndex 读取全部用户组及其权限码，供"权限子集"校验复用。
func groupPermissionIndex(ctx context.Context, deps Deps) (map[int64][]string, error) {
	groups, err := deps.AdminRepo.ListAccessGroups(ctx)
	if err != nil {
		return nil, err
	}
	index := make(map[int64][]string, len(groups))
	for _, group := range groups {
		index[group.ID] = group.Permissions
	}
	return index, nil
}

// ensureGroupsWithinActor 校验操作者有权把目标用户分配到给定用户组。
// 非超级管理员只能操作"权限不超出自身"的组，且不能修改自己的归属——否则
// 持有 ManageUserGroups 的管理员可借成员关系自提权。返回 (HTTP 状态码, 文案)，
// 无错误时文案为空。
func ensureGroupsWithinActor(ctx context.Context, deps Deps, actor user.User, targetUserID int64, groupIDs []int64) (int, string) {
	if len(groupIDs) == 0 {
		return http.StatusBadRequest, "至少保留一个用户组"
	}
	if actor.IsSuperAdmin() {
		return 0, ""
	}
	if targetUserID == actor.ID {
		return http.StatusForbidden, "不能修改自己的用户组归属"
	}
	index, err := groupPermissionIndex(ctx, deps)
	if err != nil {
		return http.StatusInternalServerError, "读取用户组失败"
	}
	seen := make(map[int64]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID < 1 {
			return http.StatusBadRequest, "用户组参数无效"
		}
		if _, exists := seen[groupID]; exists {
			continue
		}
		seen[groupID] = struct{}{}
		perms, exists := index[groupID]
		if !exists {
			return http.StatusBadRequest, "用户组不存在"
		}
		for _, code := range perms {
			if !actor.HasPermission(code) {
				return http.StatusForbidden, "不能把用户分配到权限超出自身范围的用户组"
			}
		}
	}
	return 0, ""
}

// ensureActorCoversUser 校验操作者对目标账户的权限覆盖：非超级管理员只能改动
// "权限不超出自身"的账户（按其当前用户组判定）。缺少该判定时，仅持 ManageUsers
// 的管理员可重置/禁用/删除权限更高的管理员，实现从 ManageUsers 到完全管理权限的
// 提权。返回 (HTTP 状态码, 文案)，无错误时文案为空。
func ensureActorCoversUser(ctx context.Context, deps Deps, actor user.User, targetUserID int64) (int, string) {
	if actor.IsSuperAdmin() || targetUserID == actor.ID {
		return 0, ""
	}
	groupIDs, err := deps.AdminRepo.GetUserGroupIDs(ctx, targetUserID)
	if err != nil {
		return http.StatusInternalServerError, "读取目标账户用户组失败"
	}
	if len(groupIDs) == 0 {
		// 无组账户没有管理权限，普通管理员可以处理。
		return 0, ""
	}
	index, err := groupPermissionIndex(ctx, deps)
	if err != nil {
		return http.StatusInternalServerError, "读取用户组失败"
	}
	for _, groupID := range groupIDs {
		for _, code := range index[groupID] {
			// 只比较"管理类"权限：普通用户的业务权限（上传/分享/直链等）不构成
			// 更高的管理权限，否则仅持 ManageUsers 的"用户支持"管理员将无法管理
			// 任何普通用户（其业务权限必然不在自己名下）。
			if !containsCode(permission.Admin, code) {
				continue
			}
			if !actor.HasPermission(code) {
				return http.StatusForbidden, "不能操作权限超出自身范围的账户"
			}
		}
	}
	return 0, ""
}

// ensureActorCoversGroup 校验操作者对目标用户组的权限覆盖：非超级管理员只能
// 管理"权限不超出自身"的组成员与邀请码，防止借高权限组提权。
func ensureActorCoversGroup(ctx context.Context, deps Deps, actor user.User, groupID int64) (int, string) {
	if actor.IsSuperAdmin() {
		return 0, ""
	}
	index, err := groupPermissionIndex(ctx, deps)
	if err != nil {
		return http.StatusInternalServerError, "读取用户组失败"
	}
	perms, exists := index[groupID]
	if !exists {
		return http.StatusBadRequest, "用户组不存在"
	}
	for _, code := range perms {
		if !actor.HasPermission(code) {
			return http.StatusForbidden, "不能操作权限超出自身范围的用户组"
		}
	}
	return 0, ""
}

// containsCode 判断权限码集合是否包含指定码。
func containsCode(codes []string, target string) bool {
	for _, code := range codes {
		if code == target {
			return true
		}
	}
	return false
}

func handleAdminInvitations(w http.ResponseWriter, r *http.Request, deps Deps, u user.User, path string) {
	if path == "invitations" && r.Method == http.MethodGet {
		data, err := deps.AdminRepo.GetInvitationDashboard(r.Context())
		if err != nil {
			writeBusinessError(w, 500, "读取邀请码配置失败")
			return
		}
		writeJSON(w, 200, map[string]any{"status": "ok", "data": data})
		return
	}
	if path == "invitations" && r.Method == http.MethodPost {
		var req adminInvitationIssueRequest
		if err := decodeSmallJSON(w, r, &req); err != nil {
			writeBusinessError(w, 400, "请求格式错误")
			return
		}
		// 组邀请码同样受"权限子集"约束：否则持有 ManageInvitations 的管理员
		// 可以给高权限组发码、再注册新账号实现提权。
		if req.TargetType == "group" {
			if status, msg := ensureActorCoversGroup(r.Context(), deps, u, req.TargetID); msg != "" {
				writeBusinessError(w, status, msg)
				return
			}
		}
		// 邀请码有效期由系统配置决定（0 = 永久），管理员可随时调整。
		knobs, knobErr := deps.SystemSettings.GetKnobs(r.Context())
		if knobErr != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取系统配置失败")
			return
		}
		messageID, err := deps.AdminRepo.IssueInvitationCodes(r.Context(), u.ID, req.TargetType, req.TargetID, req.Quantity, knobs.InvitationValidDays)
		if errors.Is(err, admin.ErrInvitationTargetInvalid) || errors.Is(err, admin.ErrInvitationQuantity) {
			writeBusinessError(w, 400, err.Error())
			return
		}
		if err != nil {
			writeBusinessError(w, 500, "生成邀请码失败")
			return
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), u.ID, u.Username, "invitation.issue", req.TargetType, strconv.FormatInt(req.TargetID, 10), map[string]any{"quantity": req.Quantity}, net.ParseIP(clientIP(r)))
		writeJSON(w, 200, map[string]any{"status": "ok", "quantity": req.Quantity, "messageId": messageID})
		return
	}
	if path == "invitations/settings" && r.Method == http.MethodPut {
		var req adminInvitationSettingsRequest
		if err := decodeSmallJSON(w, r, &req); err != nil {
			writeBusinessError(w, 400, "请求格式错误")
			return
		}
		if err := deps.AdminRepo.SetRegistrationRequiresInvitation(r.Context(), req.RegistrationRequiresInvitation); err != nil {
			writeBusinessError(w, 500, "更新注册策略失败")
			return
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), u.ID, u.Username, "registration.invitation_requirement.update", "system_settings", "注册邀请码策略", map[string]any{"required": req.RegistrationRequiresInvitation}, net.ParseIP(clientIP(r)))
		writeJSON(w, 200, map[string]any{"status": "ok", "registrationRequiresInvitation": req.RegistrationRequiresInvitation})
		return
	}
	if strings.HasPrefix(path, "invitations/") && r.Method == http.MethodDelete {
		id, err := strconv.ParseInt(strings.TrimPrefix(path, "invitations/"), 10, 64)
		if err != nil || id < 1 {
			writeBusinessError(w, 400, "邀请码编号无效")
			return
		}
		if err := deps.AdminRepo.RevokeInvitation(r.Context(), id); errors.Is(err, admin.ErrInvitationNotAvailable) {
			writeBusinessError(w, 409, err.Error())
			return
		} else if err != nil {
			writeBusinessError(w, 500, "作废邀请码失败")
			return
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), u.ID, u.Username, "invitation.revoke", "invitation_code", strconv.FormatInt(id, 10), map[string]any{}, net.ParseIP(clientIP(r)))
		writeJSON(w, 200, map[string]any{"status": "ok"})
		return
	}
	writeBusinessError(w, 405, "method not allowed")
}

func requireManagementUser(w http.ResponseWriter, r *http.Request, deps Deps) (user.User, bool) {
	u, ok := requireUser(w, r, deps)
	if !ok {
		return user.User{}, false
	}
	if !hasManagementPermission(u) {
		writeBusinessError(w, http.StatusForbidden, "没有管理权限")
		return user.User{}, false
	}
	return u, true
}

func hasManagementPermission(u user.User) bool {
	if u.IsSuperAdmin() {
		return true
	}
	for _, code := range permission.Admin {
		if u.HasPermission(code) {
			return true
		}
	}
	return false
}

func requireAnyAdminPermission(w http.ResponseWriter, u user.User, codes ...string) bool {
	for _, code := range codes {
		if u.HasPermission(code) {
			return true
		}
	}
	writeBusinessError(w, http.StatusForbidden, "没有对应的管理权限")
	return false
}

func requireAdminPermission(w http.ResponseWriter, u user.User, code string) bool {
	if u.HasPermission(code) {
		return true
	}
	writeBusinessError(w, http.StatusForbidden, "没有对应的管理权限")
	return false
}

func handleAdminSystemConfig(w http.ResponseWriter, r *http.Request, deps Deps, u user.User) {
	if r.Method == http.MethodGet {
		settings, err := deps.SystemSettings.Get(r.Context())
		if err != nil {
			writeBusinessError(w, 500, "读取系统配置失败")
			return
		}
		allowed, required, err := deps.AdminRepo.ListTOTPPolicyGroups(r.Context())
		if err != nil {
			writeBusinessError(w, 500, "读取动态令牌用户组失败")
			return
		}
		knobs, knobErr := deps.SystemSettings.GetKnobs(r.Context())
		if knobErr != nil {
			writeBusinessError(w, 500, "读取系统配置失败")
			return
		}
		writeJSON(w, 200, systemConfigResponse(settings, knobs, allowed, required, deps.Blobs.EncryptionAvailable()))
		return
	}
	if r.Method != http.MethodPut {
		writeBusinessError(w, 405, "method not allowed")
		return
	}
	var req adminSystemConfigRequest
	if err := decodeSmallJSON(w, r, &req); err != nil {
		writeBusinessError(w, 400, "请求格式错误")
		return
	}
	// 开启新文件加密前必须已配置 KEK，否则之后所有上传都会失败。
	if req.EncryptNewFiles && !deps.Blobs.EncryptionAvailable() {
		writeBusinessError(w, 400, "未配置加密密钥（XPH_ENCRYPTION_KEYS），无法开启新文件加密")
		return
	}
	settings, err := deps.SystemSettings.UpdateAll(r.Context(), systemsetting.Config{
		SiteName: req.SiteName, StoragePath: req.StoragePath,
		InvitationLength: req.InvitationCodeLength, InvitationCaseSensitive: req.InvitationCodeCaseSensitive,
		InvitationIncludeLetters: req.InvitationCodeIncludeLetters, InvitationIncludeNumbers: req.InvitationCodeIncludeNumbers,
		ShareLength: req.ShareCodeLength, ShareCaseSensitive: req.ShareCodeCaseSensitive,
		ShareIncludeLetters: req.ShareCodeIncludeLetters, ShareIncludeNumbers: req.ShareCodeIncludeNumbers,
		PickupLength: req.PickupCodeLength, PickupCaseSensitive: req.PickupCodeCaseSensitive,
		PickupIncludeLetters: req.PickupCodeIncludeLetters, PickupIncludeNumbers: req.PickupCodeIncludeNumbers,
		PickupMaxLifetimeSeconds: req.PickupMaxLifetimeSeconds,
		LoginTOTPEnabled:         req.LoginTOTPEnabled,
		UploadRequiresReview:     req.UploadRequiresReview, CustomShareRequiresReview: req.CustomShareRequiresReview,
		UploadChunkSizeBytes:       req.UploadChunkSizeBytes,
		UploadTaskChunkConcurrency: req.UploadTaskChunkConcurrency,
		UploadUserTaskConcurrency:  req.UploadUserTaskConcurrency,
		TrashRetentionDays:         req.TrashRetentionDays,
		EncryptNewFiles:            req.EncryptNewFiles,
		ShareRetrievalMode:         req.ShareRetrievalMode,
		StorageChunkSizeBytes:      req.StorageChunkSizeBytes,
		PickupAllowPermanent:       req.PickupAllowPermanent,
		CrossUserDedupe:            req.CrossUserDedupe,
		InvitationValidDays:        req.InvitationValidDays,
		UploadMaxFileBytes:         req.UploadMaxFileBytes,
		RedirectFallback:           req.RedirectFallback,
	})
	if errors.Is(err, systemsetting.ErrSiteNameBlank) || errors.Is(err, systemsetting.ErrStoragePathInvalid) || errors.Is(err, systemsetting.ErrUploadChunkSize) || errors.Is(err, systemsetting.ErrUploadConcurrency) || errors.Is(err, systemsetting.ErrTrashRetention) || errors.Is(err, systemsetting.ErrPickupLifetime) || errors.Is(err, systemsetting.ErrInvitationValidity) || errors.Is(err, systemsetting.ErrUploadMaxFileBytes) {
		writeBusinessError(w, 400, err.Error())
		return
	}
	if errors.Is(err, systemsetting.ErrRetrievalMode) {
		writeBusinessError(w, 400, "分享取数方式无效")
		return
	}
	if errors.Is(err, systemsetting.ErrStorageChunkSize) {
		writeBusinessError(w, 400, err.Error())
		return
	}
	if errors.Is(err, systemsetting.ErrRandomCodeInvalid) {
		writeBusinessError(w, 400, "随机码位数必须在 4 到 64 之间，且至少包含字母或数字")
		return
	}
	if err != nil {
		writeBusinessError(w, 500, "更新系统配置失败")
		return
	}
	ip := net.ParseIP(clientIP(r))
	_ = deps.AdminRepo.WriteAudit(r.Context(), u.ID, u.Username, "system_config.update", "system_settings", "全局系统配置", map[string]any{"siteName": settings.SiteName, "storagePath": settings.StoragePath, "invitationCodeLength": settings.InvitationLength, "shareCodeLength": settings.ShareLength, "uploadRequiresReview": settings.UploadRequiresReview, "customShareRequiresReview": settings.CustomShareRequiresReview, "uploadChunkSizeBytes": settings.UploadChunkSizeBytes, "uploadTaskChunkConcurrency": settings.UploadTaskChunkConcurrency, "uploadUserTaskConcurrency": settings.UploadUserTaskConcurrency, "trashRetentionDays": settings.TrashRetentionDays, "encryptNewFiles": settings.EncryptNewFiles, "shareRetrievalMode": settings.ShareRetrievalMode, "redirectFallback": req.RedirectFallback}, ip)
	allowed, required, err := deps.AdminRepo.ListTOTPPolicyGroups(r.Context())
	if err != nil {
		writeBusinessError(w, 500, "读取动态令牌用户组失败")
		return
	}
	knobs, knobErr := deps.SystemSettings.GetKnobs(r.Context())
	if knobErr != nil {
		writeBusinessError(w, 500, "读取系统配置失败")
		return
	}
	writeJSON(w, 200, systemConfigResponse(settings, knobs, allowed, required, deps.Blobs.EncryptionAvailable()))
}

func systemConfigResponse(settings sqlcgen.SystemSetting, knobs systemsetting.Knobs, allowedGroups, requiredGroups []string, encryptionConfigured bool) map[string]any {
	return map[string]any{
		"status": "ok", "siteName": settings.SiteName, "siteIconUrl": currentSiteIconURL(settings.StoragePath),
		"customHomepageConfigured": customHomepageConfigured(settings.StoragePath),
		"storagePath":              settings.StoragePath,
		"invitationCodeLength":     settings.InvitationLength, "invitationCodeCaseSensitive": settings.InvitationCaseSensitive,
		"invitationCodeIncludeLetters": settings.InvitationIncludeLetters, "invitationCodeIncludeNumbers": settings.InvitationIncludeNumbers,
		"shareCodeLength": settings.ShareLength, "shareCodeCaseSensitive": settings.ShareCaseSensitive,
		"shareCodeIncludeLetters": settings.ShareIncludeLetters, "shareCodeIncludeNumbers": settings.ShareIncludeNumbers,
		"pickupCodeLength": settings.PickupLength, "pickupCodeCaseSensitive": settings.PickupCaseSensitive,
		"pickupCodeIncludeLetters": settings.PickupIncludeLetters, "pickupCodeIncludeNumbers": settings.PickupIncludeNumbers,
		"pickupMaxLifetimeSeconds": nullableInt64(settings.PickupMaxLifetimeSeconds),
		"pickupAllowPermanent":     knobs.PickupAllowPermanent,
		"crossUserDedupe":          knobs.CrossUserDedupe,
		"invitationValidDays":      knobs.InvitationValidDays,
		"uploadMaxFileBytes":       knobs.UploadMaxFileBytes,
		"loginTOTPEnabled":         settings.LoginTotpEnabled,
		"loginTOTPAllowedGroups":   allowedGroups,
		"loginTOTPRequiredGroups":  requiredGroups,
		"uploadRequiresReview":     settings.UploadRequiresReview, "customShareRequiresReview": settings.CustomShareRequiresReview,
		"uploadChunkSizeBytes":       settings.UploadChunkSizeBytes,
		"uploadTaskChunkConcurrency": settings.UploadTaskChunkConcurrency,
		"uploadUserTaskConcurrency":  settings.UploadUserTaskConcurrency,
		"trashRetentionDays":         settings.TrashRetentionDays,
		"encryptNewFiles":            settings.EncryptNewFiles,
		"shareRetrievalMode":         settings.ShareRetrievalMode,
		"storageChunkSizeBytes":      settings.StorageChunkSizeBytes,
		"redirectFallback":           knobs.RedirectFallback,
		// encryptionConfigured 告诉管理界面当前部署是否配置了 KEK（未配置时
		// 不允许开启新文件加密）。
		"encryptionConfigured": encryptionConfigured,
	}
}

// handleAdminUploadTest 接收与候选分片等大的原始请求体，用于验证反向代理限制。
func handleAdminUploadTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	expected, err := strconv.ParseInt(r.URL.Query().Get("sizeBytes"), 10, 64)
	if err != nil || expected < 1<<20 || expected > 64<<20 {
		writeBusinessError(w, http.StatusBadRequest, "分片测试大小必须在 1M 到 64M 之间")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, expected+1)
	n, err := io.Copy(io.Discard, r.Body)
	if err != nil || n != expected {
		writeBusinessError(w, http.StatusBadRequest, "分片测试请求大小不匹配")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "receivedBytes": n})
}
