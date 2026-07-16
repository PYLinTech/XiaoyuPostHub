package server

import (
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/PYLinTech/XiaoyuPostHub/backend/admin"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

type adminSystemConfigRequest struct {
	SiteName                     string `json:"siteName"`
	StoragePath                  string `json:"storagePath"`
	FolderPackMode               string `json:"folderPackMode"`
	ShareDeliveryMode            string `json:"shareDeliveryMode"`
	InvitationCodeLength         int16  `json:"invitationCodeLength"`
	InvitationCodeCaseSensitive  bool   `json:"invitationCodeCaseSensitive"`
	InvitationCodeIncludeLetters bool   `json:"invitationCodeIncludeLetters"`
	InvitationCodeIncludeNumbers bool   `json:"invitationCodeIncludeNumbers"`
	ShareCodeLength              int16  `json:"shareCodeLength"`
	ShareCodeCaseSensitive       bool   `json:"shareCodeCaseSensitive"`
	ShareCodeIncludeLetters      bool   `json:"shareCodeIncludeLetters"`
	ShareCodeIncludeNumbers      bool   `json:"shareCodeIncludeNumbers"`
	UploadRequiresReview         bool   `json:"uploadRequiresReview"`
	CustomShareRequiresReview    bool   `json:"customShareRequiresReview"`
}

type adminReviewRequest struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
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

type adminUserGroupsRequest struct {
	GroupIDs []int64 `json:"groupIds"`
}

type adminResetPasswordRequest struct {
	Password string `json:"password"`
}

type adminUserDisabledRequest struct {
	Disabled bool `json:"disabled"`
}

type adminQuotaRequest struct {
	Name                  string `json:"name"`
	Description           string `json:"description"`
	StorageBytesLimit     *int64 `json:"storageBytesLimit"`
	SingleFileBytesLimit  *int64 `json:"singleFileBytesLimit"`
	DailyUploadBytesLimit *int64 `json:"dailyUploadBytesLimit"`
	DailyUploadCountLimit *int64 `json:"dailyUploadCountLimit"`
	ActiveShareCountLimit *int64 `json:"activeShareCountLimit"`
	ActiveDirectLinkLimit *int64 `json:"activeDirectLinkLimit"`
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
			if !requireAdminPermission(w, u, permission.ManageUsers) {
				return
			}
			handleAdminUsers(w, r, deps, u, path)
			return
		}
		if path == "user-groups" {
			if !requireAdminPermission(w, u, permission.ManageUsers) {
				return
			}
			handleAdminUserGroups(w, r, deps, u)
			return
		}
		if path == "access" || strings.HasPrefix(path, "access/") {
			if !requireAdminPermission(w, u, permission.ManageRoles) {
				return
			}
			handleAdminAccess(w, r, deps, u, path)
			return
		}
		if path == "invitations" || strings.HasPrefix(path, "invitations/") {
			if !requireAdminPermission(w, u, permission.ManageRoles) {
				return
			}
			handleAdminInvitations(w, r, deps, u, path)
			return
		}
		if path == "reviews/files" || strings.HasPrefix(path, "reviews/files/") || path == "reviews/shares" || strings.HasPrefix(path, "reviews/shares/") {
			if !requireAdminPermission(w, u, permission.ReadAuditLog) {
				return
			}
			handleAdminReviews(w, r, deps, u, path)
			return
		}
		switch path {
		case "overview":
			if r.Method != http.MethodGet {
				writeBusinessError(w, 405, "method not allowed")
				return
			}
			if deps.FileStore == nil {
				writeBusinessError(w, 503, "文件存储尚未初始化")
				return
			}
			storagePath, err := deps.FileStore.Root(r.Context())
			if err != nil {
				writeBusinessError(w, 500, "读取存储目录失败")
				return
			}
			data, err := deps.AdminRepo.GetOverview(r.Context(), storagePath)
			if err != nil {
				writeBusinessError(w, 500, "读取实时概览失败")
				return
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
			if !requireSuperAdmin(w, u) {
				return
			}
			handleAdminSystemConfig(w, r, deps, u)
		case "site-icon":
			if !requireSuperAdmin(w, u) {
				return
			}
			handleAdminSiteIcon(w, r, deps, u)
		default:
			writeBusinessError(w, 404, "管理接口不存在")
		}
	}
}

func handleAdminAccess(w http.ResponseWriter, r *http.Request, deps Deps, actor user.User, path string) {
	if path == "access" && r.Method == http.MethodGet {
		quotas, err := deps.AdminRepo.ListQuotaProfiles(r.Context())
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取配额方案失败")
			return
		}
		groups, err := deps.AdminRepo.ListAccessGroups(r.Context())
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取用户组配置失败")
			return
		}
		definitions := make([]map[string]string, 0, len(permission.Definitions))
		for _, definition := range permission.Definitions {
			definitions = append(definitions, map[string]string{
				"code": definition.Code, "description": definition.Description,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok", "quotas": quotas, "groups": groups,
			"availablePermissions": definitions,
		})
		return
	}
	if path == "access/quotas" && r.Method == http.MethodPost {
		var req adminQuotaRequest
		if !decodeQuotaRequest(w, r, &req) {
			return
		}
		item, err := deps.QuotaRepo.CreateQuotaProfile(r.Context(), strings.TrimSpace(strings.ToLower(req.Name)), strings.TrimSpace(req.Description),
			req.StorageBytesLimit, req.SingleFileBytesLimit, req.DailyUploadBytesLimit,
			req.DailyUploadCountLimit, req.ActiveShareCountLimit, req.ActiveDirectLinkLimit)
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
				req.DailyUploadCountLimit, req.ActiveShareCountLimit, req.ActiveDirectLinkLimit); err != nil {
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
				if _, exists := unique[code]; !exists {
					unique[code] = struct{}{}
					codes = append(codes, code)
				}
			}
			if err := deps.AdminRepo.SetGroupPermissions(r.Context(), groupID, codes); err != nil {
				writeBusinessError(w, http.StatusBadRequest, "保存用户组权限失败")
				return
			}
			_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "group.permissions.update", "user_group", strconv.FormatInt(groupID, 10), map[string]any{"permissions": codes}, net.ParseIP(clientIP(r)))
		case "quota":
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
		req.DailyUploadCountLimit, req.ActiveShareCountLimit, req.ActiveDirectLinkLimit}
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

	if len(parts) == 1 && r.Method == http.MethodDelete {
		username, storageKeys, err := deps.AdminRepo.DeleteUser(r.Context(), userID)
		if errors.Is(err, admin.ErrUserNotFound) {
			writeBusinessError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "删除用户失败")
			return
		}
		for _, storageKey := range storageKeys {
			if deps.FileStore != nil {
				if err := deps.FileStore.Remove(r.Context(), storageKey); err != nil {
					log.Printf("清理已删除用户 %d 的文件 %q 失败: %v", userID, storageKey, err)
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
		username, err := deps.AdminRepo.SetUserGroups(r.Context(), userID, req.GroupIDs)
		if errors.Is(err, admin.ErrGroupNotFound) {
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
		if err := decodeSmallJSON(w, r, &req); err != nil || len(req.Password) < 8 || len(req.Password) > 1024 {
			writeBusinessError(w, http.StatusBadRequest, "密码长度应为 8 至 1024 个字符")
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

func handleAdminUserGroups(w http.ResponseWriter, r *http.Request, deps Deps, actor user.User) {
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
		messageID, err := deps.AdminRepo.IssueInvitationCodes(r.Context(), u.ID, req.TargetType, req.TargetID, req.Quantity)
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
	return u.IsSuperAdmin() ||
		u.HasPermission(permission.ManageUsers) ||
		u.HasPermission(permission.ReadAuditLog) ||
		u.HasPermission(permission.ManageRoles)
}

func requireAdminPermission(w http.ResponseWriter, u user.User, code string) bool {
	if u.HasPermission(code) {
		return true
	}
	writeBusinessError(w, http.StatusForbidden, "没有对应的管理权限")
	return false
}

func requireSuperAdmin(w http.ResponseWriter, u user.User) bool {
	if u.IsSuperAdmin() {
		return true
	}
	writeBusinessError(w, http.StatusForbidden, "仅系统超级管理员可访问")
	return false
}

func handleAdminSystemConfig(w http.ResponseWriter, r *http.Request, deps Deps, u user.User) {
	if r.Method == http.MethodGet {
		settings, err := deps.SystemSettings.Get(r.Context())
		if err != nil {
			writeBusinessError(w, 500, "读取系统配置失败")
			return
		}
		writeJSON(w, 200, systemConfigResponse(settings))
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
	settings, err := deps.SystemSettings.UpdateAll(r.Context(), systemsetting.Config{
		SiteName: req.SiteName, StoragePath: req.StoragePath,
		FolderPackMode: req.FolderPackMode, ShareDeliveryMode: req.ShareDeliveryMode,
		InvitationLength: req.InvitationCodeLength, InvitationCaseSensitive: req.InvitationCodeCaseSensitive,
		InvitationIncludeLetters: req.InvitationCodeIncludeLetters, InvitationIncludeNumbers: req.InvitationCodeIncludeNumbers,
		ShareLength: req.ShareCodeLength, ShareCaseSensitive: req.ShareCodeCaseSensitive,
		ShareIncludeLetters: req.ShareCodeIncludeLetters, ShareIncludeNumbers: req.ShareCodeIncludeNumbers,
		UploadRequiresReview: req.UploadRequiresReview, CustomShareRequiresReview: req.CustomShareRequiresReview,
	})
	if errors.Is(err, systemsetting.ErrSiteNameBlank) || errors.Is(err, systemsetting.ErrStoragePathInvalid) || errors.Is(err, systemsetting.ErrDownloadMode) {
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
	_ = deps.AdminRepo.WriteAudit(r.Context(), u.ID, u.Username, "system_config.update", "system_settings", "全局系统配置", map[string]any{"siteName": settings.SiteName, "storagePath": settings.StoragePath, "folderPackMode": settings.FolderPackMode, "shareDeliveryMode": settings.ShareDeliveryMode, "invitationCodeLength": settings.InvitationLength, "shareCodeLength": settings.ShareLength, "uploadRequiresReview": settings.UploadRequiresReview, "customShareRequiresReview": settings.CustomShareRequiresReview}, ip)
	writeJSON(w, 200, systemConfigResponse(settings))
}

func systemConfigResponse(settings sqlcgen.SystemSetting) map[string]any {
	return map[string]any{
		"status": "ok", "siteName": settings.SiteName, "siteIconUrl": currentSiteIconURL(settings.StoragePath),
		"storagePath": settings.StoragePath, "folderPackMode": settings.FolderPackMode, "shareDeliveryMode": settings.ShareDeliveryMode,
		"invitationCodeLength": settings.InvitationLength, "invitationCodeCaseSensitive": settings.InvitationCaseSensitive,
		"invitationCodeIncludeLetters": settings.InvitationIncludeLetters, "invitationCodeIncludeNumbers": settings.InvitationIncludeNumbers,
		"shareCodeLength": settings.ShareLength, "shareCodeCaseSensitive": settings.ShareCaseSensitive,
		"shareCodeIncludeLetters": settings.ShareIncludeLetters, "shareCodeIncludeNumbers": settings.ShareIncludeNumbers,
		"uploadRequiresReview": settings.UploadRequiresReview, "customShareRequiresReview": settings.CustomShareRequiresReview,
	}
}

func handleAdminReviews(w http.ResponseWriter, r *http.Request, deps Deps, actor user.User, path string) {
	if path == "reviews/files" {
		if r.Method != http.MethodGet {
			writeBusinessError(w, 405, "method not allowed")
			return
		}
		items, err := deps.AdminRepo.ListFileReviews(r.Context())
		if err != nil {
			writeBusinessError(w, 500, "读取文件审查列表失败")
			return
		}
		writeJSON(w, 200, map[string]any{"status": "ok", "items": items})
		return
	}
	if path == "reviews/shares" {
		if r.Method != http.MethodGet {
			writeBusinessError(w, 405, "method not allowed")
			return
		}
		items, err := deps.AdminRepo.ListShareReviews(r.Context())
		if err != nil {
			writeBusinessError(w, 500, "读取分享审查列表失败")
			return
		}
		writeJSON(w, 200, map[string]any{"status": "ok", "items": items})
		return
	}
	if strings.HasPrefix(path, "reviews/files/") {
		tail := strings.TrimPrefix(path, "reviews/files/")
		parts := strings.Split(tail, "/")
		id := strings.TrimSpace(parts[0])
		if id == "" {
			writeBusinessError(w, 400, "文件编号无效")
			return
		}
		if len(parts) == 2 && (parts[1] == "preview" || parts[1] == "download") {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				writeBusinessError(w, 405, "method not allowed")
				return
			}
			item, err := deps.ResourceRepo.GetByID(r.Context(), id)
			if errors.Is(err, resource.ErrNotFound) || item.Kind != resource.KindFile {
				writeBusinessError(w, 404, "文件不存在")
				return
			}
			if err != nil {
				writeBusinessError(w, 500, "读取文件失败")
				return
			}
			if parts[1] == "preview" {
				serveResourceFilePreview(w, r, deps, item)
			} else {
				serveOwnedFile(w, r, deps, item)
			}
			return
		}
		if len(parts) != 1 || r.Method != http.MethodPut {
			writeBusinessError(w, 405, "method not allowed")
			return
		}
		var req adminReviewRequest
		if decodeSmallJSON(w, r, &req) != nil {
			writeBusinessError(w, 400, "请求格式错误")
			return
		}
		status := reviewActionStatus(req.Action)
		if status == "" {
			writeBusinessError(w, 400, "审核操作无效")
			return
		}
		if status == "rejected" && strings.TrimSpace(req.Reason) == "" {
			writeBusinessError(w, 400, "请填写驳回原因")
			return
		}
		if err := deps.AdminRepo.ReviewFile(r.Context(), id, status, req.Reason, actor.ID); err != nil {
			writeBusinessError(w, 404, "待审文件不存在")
			return
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "file_review."+req.Action, "resource", id, map[string]any{"reason": strings.TrimSpace(req.Reason)}, net.ParseIP(clientIP(r)))
		writeJSON(w, 200, map[string]any{"status": "ok"})
		return
	}
	if strings.HasPrefix(path, "reviews/shares/") {
		id, err := strconv.ParseInt(strings.TrimPrefix(path, "reviews/shares/"), 10, 64)
		if err != nil || id < 1 {
			writeBusinessError(w, 400, "分享编号无效")
			return
		}
		if r.Method != http.MethodPut {
			writeBusinessError(w, 405, "method not allowed")
			return
		}
		var req adminReviewRequest
		if decodeSmallJSON(w, r, &req) != nil {
			writeBusinessError(w, 400, "请求格式错误")
			return
		}
		status := reviewActionStatus(req.Action)
		if status == "" {
			writeBusinessError(w, 400, "审核操作无效")
			return
		}
		if status == "rejected" && strings.TrimSpace(req.Reason) == "" {
			writeBusinessError(w, 400, "请填写驳回原因")
			return
		}
		if err := deps.AdminRepo.ReviewShare(r.Context(), id, status, req.Reason, actor.ID); err != nil {
			writeBusinessError(w, 404, "待审分享不存在")
			return
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "share_review."+req.Action, "share", strconv.FormatInt(id, 10), map[string]any{"reason": strings.TrimSpace(req.Reason)}, net.ParseIP(clientIP(r)))
		writeJSON(w, 200, map[string]any{"status": "ok"})
		return
	}
	writeBusinessError(w, 404, "审核接口不存在")
}

func reviewActionStatus(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "approve":
		return "approved"
	case "reject":
		return "rejected"
	default:
		return ""
	}
}
