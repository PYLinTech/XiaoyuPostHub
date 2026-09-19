package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/blobstore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/filestore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/randomtoken"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
	"github.com/PYLinTech/XiaoyuPostHub/backend/sharing"
	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
	"github.com/jackc/pgx/v5/pgtype"
)

const maxLinkLifetime = 10 * 365 * 24 * time.Hour

type createShareRequest struct {
	ShareType         string   `json:"shareType"`
	ResourceIDs       []string `json:"resourceIds"`
	Password          *string  `json:"password"`
	NoPassword        bool     `json:"noPassword"`
	ExpiresInSeconds  *int64   `json:"expiresInSeconds"`
	ShowOwner         bool     `json:"showOwner"`
	DownloadLimit     *int64   `json:"downloadLimit"`
	TrafficLimitBytes *int64   `json:"trafficLimitBytes"`
	Description       string   `json:"description"`
	DescriptionFormat string   `json:"descriptionFormat"`
}

type createDirectLinkRequest struct {
	ResourceID        string `json:"resourceId"`
	ExpiresInSeconds  *int64 `json:"expiresInSeconds"`
	DownloadLimit     *int64 `json:"downloadLimit"`
	TrafficLimitBytes *int64 `json:"trafficLimitBytes"`
}

type updateShareRequest struct {
	ExpiresInSeconds  *int64  `json:"expiresInSeconds"`
	PasswordMode      string  `json:"passwordMode"`
	Password          *string `json:"password"`
	ShowOwner         bool    `json:"showOwner"`
	DownloadLimit     *int64  `json:"downloadLimit"`
	TrafficLimitBytes *int64  `json:"trafficLimitBytes"`
	Description       string  `json:"description"`
	DescriptionFormat string  `json:"descriptionFormat"`
}

type updateDirectLinkRequest struct {
	ExpiresInSeconds  *int64 `json:"expiresInSeconds"`
	DownloadLimit     *int64 `json:"downloadLimit"`
	TrafficLimitBytes *int64 `json:"trafficLimitBytes"`
}

type batchLinkRequest struct {
	IDs    []int64 `json:"ids"`
	Action string  `json:"action"`
}

func createShareHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			u, ok := requireUser(w, r, deps)
			if !ok {
				return
			}
			items, err := deps.SharingRepo.ListSharesByOwner(r.Context(), u.ID)
			if err != nil {
				writeBusinessError(w, http.StatusInternalServerError, "读取分享列表失败")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "items": items})
			return
		}
		if r.Method != http.MethodPost {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		u, ok := requireUser(w, r, deps)
		if !ok {
			return
		}
		var req createShareRequest
		if err := decodeSmallJSON(w, r, &req); err != nil {
			writeBusinessError(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if req.ShareType == "" {
			req.ShareType = "link"
		}
		if req.ShareType != "link" && req.ShareType != "pickup" {
			writeBusinessError(w, http.StatusBadRequest, "分享类型无效")
			return
		}
		requiredPermission := permission.Share
		if req.ShareType == "pickup" {
			requiredPermission = permission.PickupShare
		}
		if !u.HasPermission(requiredPermission) {
			writeBusinessError(w, http.StatusForbidden, "没有对应的分享权限")
			return
		}
		resourceIDs := normalizeResourceIDs(req.ResourceIDs)
		if len(resourceIDs) == 0 || len(resourceIDs) > 100 {
			writeBusinessError(w, http.StatusBadRequest, "请选择 1 至 100 项内容")
			return
		}
		resources := make([]resource.Resource, 0, len(resourceIDs))
		for _, resourceID := range resourceIDs {
			item, err := deps.ResourceRepo.GetOwned(r.Context(), u.ID, resourceID)
			if err != nil {
				writeBusinessError(w, http.StatusNotFound, "部分资源不存在")
				return
			}
			if !resourceTreeApproved(w, r, deps, item) {
				return
			}
			resources = append(resources, item)
		}
		if err := validateOptionalLimit(req.DownloadLimit, req.TrafficLimitBytes); err != nil {
			writeBusinessError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(req.Description) > 512<<10 {
			writeBusinessError(w, http.StatusBadRequest, "分享说明过长")
			return
		}
		format := strings.ToLower(strings.TrimSpace(req.DescriptionFormat))
		if format == "" {
			format = "markdown"
		}
		if format != "markdown" && format != "html" {
			writeBusinessError(w, http.StatusBadRequest, "说明格式只支持 markdown 或 html")
			return
		}
		codeSettings, err := deps.SystemSettings.Get(r.Context())
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取分享码配置失败")
			return
		}
		knobs, err := deps.SystemSettings.GetKnobs(r.Context())
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取分享码配置失败")
			return
		}
		var expiresAt *time.Time
		if req.ShareType == "pickup" {
			// 取件码有效期可自选：0 = 永久（需管理员允许），未指定时用系统默认值。
			// 码空间有限，永久码请配合"清理失效取件码"维护使用。
			expiresAt, err = pickupExpiryFromRequest(req.ExpiresInSeconds, pickupDefaultLifetime(codeSettings.PickupMaxLifetimeSeconds), knobs.PickupAllowPermanent)
		} else {
			expiresAt, err = expiryFromSeconds(req.ExpiresInSeconds, 24*time.Hour)
		}
		if err != nil {
			writeBusinessError(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.ShareType == "pickup" && req.Password == nil && !req.NoPassword {
			req.NoPassword = true
		}
		passwordValue, generatedPassword, err := sharePassword(req, randomtoken.CodeOptions{
			Length: int(codeSettings.ShareLength), CaseSensitive: codeSettings.ShareCaseSensitive,
			IncludeLetters: codeSettings.ShareIncludeLetters, IncludeNumbers: codeSettings.ShareIncludeNumbers,
		})
		if err != nil {
			writeBusinessError(w, http.StatusBadRequest, err.Error())
			return
		}
		withinQuota, err := withinActiveShareQuota(r, deps, u.ID)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取分享配额失败")
			return
		}
		if !withinQuota {
			writeBusinessError(w, http.StatusTooManyRequests, "已达到有效分享数量上限")
			return
		}
		reviewSettings, settingsErr := deps.AdminRepo.GetReviewSettings(r.Context())
		if settingsErr != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取分享审核配置失败")
			return
		}
		reviewRequired := reviewSettings.CustomShareRequiresReview && strings.TrimSpace(req.Description) != ""
		created, token, err := deps.SharingRepo.CreateShare(r.Context(), sharing.CreateShareParams{
			OwnerUserID: u.ID, ResourceIDs: resourceIDs, PasswordValue: passwordValue,
			ExpiresAt: expiresAt, ShowOwner: req.ShowOwner, Description: req.Description,
			DescriptionFormat: format, DownloadLimit: req.DownloadLimit,
			TrafficLimitBytes: req.TrafficLimitBytes, ShareType: req.ShareType,
			PickupOptions: randomtoken.CodeOptions{Length: int(codeSettings.PickupLength), CaseSensitive: codeSettings.PickupCaseSensitive,
				IncludeLetters: codeSettings.PickupIncludeLetters, IncludeNumbers: codeSettings.PickupIncludeNumbers},
		})
		if errors.Is(err, sharing.ErrPickupCodesExhausted) {
			writeBusinessError(w, http.StatusConflict, "系统取件码已用尽，请联系系统管理员处理！")
			return
		}
		if err != nil {
			log.Printf("创建分享失败：%v", err)
			writeBusinessError(w, http.StatusInternalServerError, "创建分享失败")
			return
		}
		reviewStatus := "approved"
		if reviewRequired {
			if err := deps.AdminRepo.MarkSharePending(r.Context(), created.ID); err != nil {
				_ = deps.SharingRepo.BatchSharesByOwner(r.Context(), u.ID, []int64{created.ID}, "disable")
				writeBusinessError(w, http.StatusInternalServerError, "提交分享审核失败")
				return
			}
			reviewStatus = "pending"
		}
		response := map[string]any{
			"status": "ok", "token": token, "url": "/s/" + token,
			"resource": resources[0], "resourceCount": len(resources), "expiresAt": created.ExpiresAt, "reviewStatus": reviewStatus,
		}
		if req.ShareType == "pickup" {
			response["pickupCode"] = token
			response["url"] = "/m?code=" + token
		}
		if generatedPassword != "" {
			response["generatedPassword"] = generatedPassword
		}
		writeJSON(w, http.StatusCreated, response)
	}
}

func createDirectLinkHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			u, ok := requireUser(w, r, deps)
			if !ok {
				return
			}
			items, err := deps.SharingRepo.ListDirectLinksByOwner(r.Context(), u.ID)
			if err != nil {
				writeBusinessError(w, http.StatusInternalServerError, "读取直链列表失败")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "items": items})
			return
		}
		if r.Method != http.MethodPost {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		u, ok := requireUser(w, r, deps)
		if !ok {
			return
		}
		if !u.HasPermission(permission.DirectLink) {
			writeBusinessError(w, http.StatusForbidden, "没有创建直链权限")
			return
		}
		var req createDirectLinkRequest
		if err := decodeSmallJSON(w, r, &req); err != nil {
			writeBusinessError(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		item, err := deps.ResourceRepo.GetOwned(r.Context(), u.ID, strings.TrimSpace(req.ResourceID))
		if err != nil {
			writeBusinessError(w, http.StatusNotFound, "资源不存在")
			return
		}
		if item.Kind != resource.KindFile {
			writeBusinessError(w, http.StatusBadRequest, "直链仅支持单个文件")
			return
		}
		if !requireApprovedFile(w, r, deps, item.ID) {
			return
		}
		if err := validateOptionalLimit(req.DownloadLimit, req.TrafficLimitBytes); err != nil {
			writeBusinessError(w, http.StatusBadRequest, err.Error())
			return
		}
		expiresAt, err := expiryFromSeconds(req.ExpiresInSeconds, 0)
		if err != nil {
			writeBusinessError(w, http.StatusBadRequest, err.Error())
			return
		}
		withinQuota, err := withinActiveDirectQuota(r, deps, u.ID)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取直链配额失败")
			return
		}
		if !withinQuota {
			writeBusinessError(w, http.StatusTooManyRequests, "已达到有效直链数量上限")
			return
		}
		created, token, err := deps.SharingRepo.CreateDirectLink(r.Context(), sharing.CreateDirectLinkParams{
			OwnerUserID: u.ID, ResourceID: item.ID, ExpiresAt: expiresAt,
			DownloadLimit: req.DownloadLimit, TrafficLimitBytes: req.TrafficLimitBytes,
		})
		if err != nil {
			log.Printf("创建直链失败：%v", err)
			writeBusinessError(w, http.StatusInternalServerError, "创建直链失败")
			return
		}
		// sha256 是资源的明文校验码：直链交付不再做全量校验，生成时展示给
		// 使用者，由使用者自行核对下载内容的完整性。
		writeJSON(w, http.StatusCreated, map[string]any{
			"status": "ok", "token": token, "url": "/d/" + token,
			"sha256": item.SHA256Checksum, "resource": item, "expiresAt": created.ExpiresAt,
		})
	}
}

func shareManageHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		u, ok := requireUser(w, r, deps)
		if !ok {
			return
		}
		id, err := parseManagedLinkID(r.URL.Path, "/api/shares/manage/")
		if err != nil {
			writeBusinessError(w, http.StatusBadRequest, "分享编号无效")
			return
		}
		var req updateShareRequest
		if err := decodeSmallJSON(w, r, &req); err != nil {
			writeBusinessError(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if err := validateOptionalLimit(req.DownloadLimit, req.TrafficLimitBytes); err != nil {
			writeBusinessError(w, http.StatusBadRequest, err.Error())
			return
		}
		format := strings.ToLower(strings.TrimSpace(req.DescriptionFormat))
		if format != "markdown" && format != "html" {
			writeBusinessError(w, http.StatusBadRequest, "说明格式只支持 markdown 或 html")
			return
		}
		if len(req.Description) > 512<<10 {
			writeBusinessError(w, http.StatusBadRequest, "分享说明过长")
			return
		}
		// 分享类型决定有效期规则：取件码可自选（0 = 永久，需管理员允许）。
		shareState, stateErr := deps.SharingRepo.GetShareState(r.Context(), &u.ID, id)
		if stateErr != nil {
			if errors.Is(stateErr, sharing.ErrNotFound) {
				writeBusinessError(w, http.StatusNotFound, "分享不存在")
				return
			}
			writeBusinessError(w, http.StatusInternalServerError, "读取分享失败")
			return
		}
		var expiresAt *time.Time
		if req.ExpiresInSeconds != nil {
			if shareState.IsPickup {
				codeSettings, settingsErr := deps.SystemSettings.Get(r.Context())
				if settingsErr != nil {
					writeBusinessError(w, http.StatusInternalServerError, "读取分享码配置失败")
					return
				}
				knobs, knobErr := deps.SystemSettings.GetKnobs(r.Context())
				if knobErr != nil {
					writeBusinessError(w, http.StatusInternalServerError, "读取分享码配置失败")
					return
				}
				expiresAt, err = pickupExpiryFromRequest(req.ExpiresInSeconds, pickupDefaultLifetime(codeSettings.PickupMaxLifetimeSeconds), knobs.PickupAllowPermanent)
			} else {
				expiresAt, err = expiryFromSeconds(req.ExpiresInSeconds, 0)
			}
			if err != nil {
				writeBusinessError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		updatePassword := req.PasswordMode != "keep"
		var passwordValue *string
		generatedPassword := ""
		switch req.PasswordMode {
		case "keep":
		case "none":
		case "random", "custom":
			codeSettings, settingsErr := deps.SystemSettings.Get(r.Context())
			if settingsErr != nil {
				writeBusinessError(w, http.StatusInternalServerError, "读取分享码配置失败")
				return
			}
			passwordRequest := createShareRequest{}
			if req.PasswordMode == "custom" {
				passwordRequest.Password = req.Password
				if req.Password == nil || *req.Password == "" {
					writeBusinessError(w, http.StatusBadRequest, "请输入分享密码")
					return
				}
			}
			passwordValue, generatedPassword, err = sharePassword(passwordRequest, randomtoken.CodeOptions{
				Length: int(codeSettings.ShareLength), CaseSensitive: codeSettings.ShareCaseSensitive,
				IncludeLetters: codeSettings.ShareIncludeLetters, IncludeNumbers: codeSettings.ShareIncludeNumbers,
			})
			if err != nil {
				writeBusinessError(w, http.StatusBadRequest, err.Error())
				return
			}
		default:
			writeBusinessError(w, http.StatusBadRequest, "密码配置无效")
			return
		}
		reviewSettings, settingsErr := deps.AdminRepo.GetReviewSettings(r.Context())
		if settingsErr != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取分享审核配置失败")
			return
		}
		reviewRequired := reviewSettings.CustomShareRequiresReview && strings.TrimSpace(req.Description) != ""
		err = deps.SharingRepo.UpdateShareByOwner(r.Context(), sharing.UpdateShareParams{
			OwnerUserID: u.ID, ID: id, UpdateExpiresAt: req.ExpiresInSeconds != nil, ExpiresAt: expiresAt,
			UpdatePassword: updatePassword, PasswordValue: passwordValue,
			ShowOwner: req.ShowOwner, Description: req.Description, DescriptionFormat: format,
			DownloadLimit: req.DownloadLimit, TrafficLimitBytes: req.TrafficLimitBytes,
		})
		if errors.Is(err, sharing.ErrNotFound) {
			writeBusinessError(w, http.StatusNotFound, "分享不存在")
			return
		}
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "保存分享配置失败")
			return
		}
		reviewStatus := "approved"
		if reviewRequired {
			if err := deps.AdminRepo.MarkSharePending(r.Context(), id); err != nil {
				_ = deps.SharingRepo.BatchSharesByOwner(r.Context(), u.ID, []int64{id}, "disable")
				writeBusinessError(w, http.StatusInternalServerError, "提交分享审核失败")
				return
			}
			reviewStatus = "pending"
		} else if err := deps.AdminRepo.ClearShareReview(r.Context(), id); err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "清理分享审核状态失败")
			return
		}
		response := map[string]any{"status": "ok", "reviewStatus": reviewStatus}
		if generatedPassword != "" {
			response["generatedPassword"] = generatedPassword
		}
		// 取件码占位同步：延长有效期/重新启用后需要重新占位（原码已被占用则换发
		// 新码）；缩短到已过期或停用则释放占位，码空间可被其它分享重新分配。
		if shareState.IsPickup {
			if newCode, rotated, ok := syncPickupCodeLive(r.Context(), deps, &u.ID, id); ok && rotated {
				response["pickupCode"] = newCode
				response["pickupCodeRotated"] = true
			}
		}
		writeJSON(w, http.StatusOK, response)
	}
}

func directLinkManageHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		u, ok := requireUser(w, r, deps)
		if !ok {
			return
		}
		id, err := parseManagedLinkID(r.URL.Path, "/api/direct-links/manage/")
		if err != nil {
			writeBusinessError(w, http.StatusBadRequest, "直链编号无效")
			return
		}
		var req updateDirectLinkRequest
		if err := decodeSmallJSON(w, r, &req); err != nil {
			writeBusinessError(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if err := validateOptionalLimit(req.DownloadLimit, req.TrafficLimitBytes); err != nil {
			writeBusinessError(w, http.StatusBadRequest, err.Error())
			return
		}
		var expiresAt *time.Time
		if req.ExpiresInSeconds != nil {
			expiresAt, err = expiryFromSeconds(req.ExpiresInSeconds, 0)
			if err != nil {
				writeBusinessError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		err = deps.SharingRepo.UpdateDirectLinkByOwner(r.Context(), sharing.UpdateDirectLinkParams{
			OwnerUserID: u.ID, ID: id, UpdateExpiresAt: req.ExpiresInSeconds != nil,
			ExpiresAt: expiresAt, DownloadLimit: req.DownloadLimit, TrafficLimitBytes: req.TrafficLimitBytes,
		})
		if errors.Is(err, sharing.ErrNotFound) {
			writeBusinessError(w, http.StatusNotFound, "直链不存在")
			return
		}
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "保存直链配置失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	}
}

func shareBatchManageHandler(deps Deps) http.HandlerFunc {
	return batchManageHandler(deps, true)
}

func directLinkBatchManageHandler(deps Deps) http.HandlerFunc {
	return batchManageHandler(deps, false)
}

func batchManageHandler(deps Deps, shares bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		u, ok := requireUser(w, r, deps)
		if !ok {
			return
		}
		var req batchLinkRequest
		if err := decodeSmallJSON(w, r, &req); err != nil {
			writeBusinessError(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		// release_codes 是维护动作（无需选择具体记录）：释放本账号下已失效的取件码
		// 占位，让码空间可被重新分配。永久取件码较多时用它腾位置。
		if req.Action == "release_codes" {
			if !shares {
				writeBusinessError(w, http.StatusBadRequest, "批量操作参数无效")
				return
			}
			freed, releasedErr := deps.SharingRepo.ReleaseDeadPickupCodes(r.Context(), &u.ID)
			if releasedErr != nil {
				writeBusinessError(w, http.StatusInternalServerError, "清理失效取件码失败")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "released": freed})
			return
		}
		req.IDs = normalizeLinkIDs(req.IDs)
		if len(req.IDs) == 0 || len(req.IDs) > 500 || (req.Action != "enable" && req.Action != "disable" && req.Action != "delete") {
			writeBusinessError(w, http.StatusBadRequest, "批量操作参数无效")
			return
		}
		if req.Action == "enable" {
			profile, quotaErr := deps.QuotaRepo.GetEffectiveQuotaByUser(r.Context(), u.ID)
			if quotaErr != nil {
				writeBusinessError(w, http.StatusInternalServerError, "读取链接配额失败")
				return
			}
			// 超管豁免配额：跳过"启用后是否超限"的预检。
			exempt := quotaExempt(r.Context(), deps, u.ID)
			var activeCount, enableCount int64
			if shares {
				activeCount, quotaErr = deps.SharingRepo.CountActiveSharesByOwner(r.Context(), u.ID)
				if quotaErr == nil {
					enableCount, quotaErr = deps.SharingRepo.CountSharesToEnableByOwner(r.Context(), u.ID, req.IDs)
				}
				if quotaErr == nil && !exempt && profile.ActiveShareCountLimit.Valid && activeCount+enableCount > profile.ActiveShareCountLimit.Int64 {
					writeBusinessError(w, http.StatusTooManyRequests, "启用后将超过有效分享数量上限")
					return
				}
			} else {
				activeCount, quotaErr = deps.SharingRepo.CountActiveDirectLinksByOwner(r.Context(), u.ID)
				if quotaErr == nil {
					enableCount, quotaErr = deps.SharingRepo.CountDirectLinksToEnableByOwner(r.Context(), u.ID, req.IDs)
				}
				if quotaErr == nil && !exempt && profile.ActiveDirectLinkLimit.Valid && activeCount+enableCount > profile.ActiveDirectLinkLimit.Int64 {
					writeBusinessError(w, http.StatusTooManyRequests, "启用后将超过有效直链数量上限")
					return
				}
			}
			if quotaErr != nil {
				writeBusinessError(w, http.StatusInternalServerError, "读取链接配额失败")
				return
			}
		}
		var err error
		if shares {
			err = deps.SharingRepo.BatchSharesByOwner(r.Context(), u.ID, req.IDs, req.Action)
		} else {
			err = deps.SharingRepo.BatchDirectLinksByOwner(r.Context(), u.ID, req.IDs, req.Action)
		}
		if errors.Is(err, sharing.ErrNotFound) {
			writeBusinessError(w, http.StatusNotFound, "部分记录不存在")
			return
		}
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "批量操作失败")
			return
		}
		// 批量启用/停用都要同步取件码占位：启用时原码若已被其它分享占用则换发新码
		// 并在响应中返回（前端提示"取件码已更换"）；停用时立即释放占位——否则停用的
		// 码继续占用码空间，与"失效即释放"的统一口径不一致，短码配置下会更快耗尽。
		rotated := make([]map[string]any, 0)
		if shares && (req.Action == "enable" || req.Action == "disable") {
			for _, shareID := range req.IDs {
				if newCode, isRotated, ok := syncPickupCodeLive(r.Context(), deps, &u.ID, shareID); ok && isRotated {
					rotated = append(rotated, map[string]any{"id": shareID, "pickupCode": newCode})
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "rotatedPickupCodes": rotated})
	}
}

func publicShareHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/shares/")
		parts := strings.Split(path, "/")
		if len(parts) == 1 && parts[0] != "" && r.Method == http.MethodGet {
			shareMetadata(w, r, deps, parts[0])
			return
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "preview" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			sharePreview(w, r, deps, parts[0])
			return
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "downloads" && r.Method == http.MethodPost {
			createShareDownloadJob(w, r, deps, parts[0])
			return
		}
		writeBusinessError(w, http.StatusNotFound, "分享不存在")
	}
}

func publicPickupHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/pickups/"), "/")
		if len(parts) == 0 || parts[0] == "" || len(parts) > 2 {
			writeBusinessError(w, http.StatusNotFound, "取件码无效或已过期")
			return
		}
		if !allowPublicSecretAttempt(r) {
			writeBusinessError(w, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
			return
		}
		item, err := deps.SharingRepo.GetShareByPickupCode(r.Context(), parts[0])
		if err != nil || item.TokenValue == nil {
			recordPublicSecretFailure(r)
			writeBusinessError(w, http.StatusNotFound, "取件码无效或已过期")
			return
		}
		resetPublicSecretFailures(r)
		clone := r.Clone(r.Context())
		urlCopy := *r.URL
		urlCopy.Path = "/api/shares/" + *item.TokenValue
		if len(parts) == 2 {
			urlCopy.Path += "/" + parts[1]
		}
		clone.URL = &urlCopy
		clone.Header.Set("X-Internal-Pickup-Code", parts[0])
		publicShareHandler(deps)(w, clone)
	}
}

func sharePreview(w http.ResponseWriter, r *http.Request, deps Deps, token string) {
	item, status, err := loadUsableShare(r, deps, token)
	if err != nil {
		writeBusinessError(w, status, err.Error())
		return
	}
	if item.PasswordValue != nil {
		if !allowPublicSecretAttempt(r) {
			writeBusinessError(w, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
			return
		}
		if !verifySharePassword(r.Header.Get("X-Share-Password"), *item.PasswordValue) {
			recordPublicSecretFailure(r)
			writeBusinessError(w, http.StatusUnauthorized, "分享密码错误")
			return
		}
		resetPublicSecretFailures(r)
	}
	if !isSingleFileShare(item) {
		writeBusinessError(w, http.StatusBadRequest, "文件夹请使用目录预览")
		return
	}
	sharedFile := item.Resources[0]
	if !requireDeliverableFile(w, r, deps, sharedFile.ID) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	// 与下载共用交付策略：加密文件在"服务器实时解密关"时输出密文 + 解密元数据，
	// 前端读取响应头解密后交给预览器。
	serveFileContent(w, r, deps, sharedFile, "inline")
}

func shareMetadata(w http.ResponseWriter, r *http.Request, deps Deps, token string) {
	item, status, err := loadUsableShare(r, deps, token)
	if err != nil {
		writeBusinessError(w, status, err.Error())
		return
	}
	locked := item.PasswordValue != nil && !verifySharePassword(r.Header.Get("X-Share-Password"), *item.PasswordValue)
	shareName, shareKind, shareSize := shareSummary(item)
	response := map[string]any{
		"status": "ok", "name": shareName, "kind": shareKind,
		"sizeBytes": shareSize, "passwordRequired": item.PasswordValue != nil,
		"locked": locked, "expiresAt": item.ExpiresAt,
		"downloadCount": item.DownloadCount, "downloadLimit": item.DownloadLimit,
		"trafficUsedBytes": item.TrafficUsedBytes, "trafficLimitBytes": item.TrafficLimitBytes,
	}
	settings, settingsErr := deps.SystemSettings.Get(r.Context())
	if settingsErr != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取下载策略失败")
		return
	}
	response["downloadPolicy"] = map[string]any{
		"folderPackMode":       settings.FolderPackMode,
		"shareDeliveryMode":    settings.ShareDeliveryMode,
		"shareRetrievalMode":   settings.ShareRetrievalMode,
		"proxyRealtimeDecrypt": settings.ProxyRealtimeDecrypt,
		"prepareUrl":           "/api/shares/" + token + "/downloads",
	}
	if pickupCode := r.Header.Get("X-Internal-Pickup-Code"); pickupCode != "" {
		response["downloadPolicy"].(map[string]any)["prepareUrl"] = "/api/pickups/" + pickupCode + "/downloads"
	}
	if !locked {
		response["description"] = item.Description
		response["descriptionFormat"] = item.DescriptionFormat
		if item.ShowOwner {
			response["owner"] = map[string]any{"username": item.OwnerUsername, "avatar": defaultUserAvatar}
		}
		if shareKind == resource.KindFolder {
			tree, treeErr := buildShareTree(r, deps, item)
			if treeErr != nil {
				writeBusinessError(w, http.StatusInternalServerError, "读取文件夹预览失败")
				return
			}
			response["items"] = previewTree(tree)
		} else {
			response["mimeType"] = item.Resources[0].MimeType
			// 透出加密状态：分享页据此展示「已加密存储」标识，让访问者知情。
			if blob, blobErr := resourceBlob(r.Context(), deps, item.Resources[0]); blobErr == nil {
				response["encrypted"] = blob.Encryption != nil
			}
		}
	}
	writeJSON(w, http.StatusOK, response)
}

// createShareDownloadJob 创建五分钟有效的下载任务。任务完整取流后才计数；
// 同一任务的并行 Range、重试和中断请求由服务端按覆盖区间归并。
func createShareDownloadJob(w http.ResponseWriter, r *http.Request, deps Deps, shareToken string) {
	item, status, err := loadUsableShare(r, deps, shareToken)
	if err != nil {
		writeBusinessError(w, status, err.Error())
		return
	}
	if item.PasswordValue != nil {
		if !allowPublicSecretAttempt(r) {
			writeBusinessError(w, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
			return
		}
		if !verifySharePassword(r.Header.Get("X-Share-Password"), *item.PasswordValue) {
			recordPublicSecretFailure(r)
			writeBusinessError(w, http.StatusUnauthorized, "分享密码错误")
			return
		}
		resetPublicSecretFailures(r)
	}
	settings, err := deps.SystemSettings.Get(r.Context())
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取下载策略失败")
		return
	}
	expiresAt := time.Now().Add(5 * time.Minute)

	// 单文件分享：不落制品，直接创建统一交付会话（/dl/<id>）；
	// 会话在交付时完成"预扣额度 → 传输 → 记录区间"的既有计数流程。
	if isSingleFileShare(item) {
		sharedFile := item.Resources[0]
		if !requireDeliverableFile(w, r, deps, sharedFile.ID) {
			return
		}
		blob, blobErr := resourceBlob(r.Context(), deps, sharedFile)
		if blobErr != nil {
			writeDownloadPreparationError(w, blobErr)
			return
		}
		if availableErr := deps.Blobs.VerifyAvailable(r.Context(), blob); availableErr != nil {
			writeDownloadPreparationError(w, availableErr)
			return
		}
		// 302 交付：管理员选择 redirect 且对象后端支持直链时，直接下发第三方
		// 直链（流量不经过我们）。明文对象可由浏览器直跳；加密对象必须由浏览器
		// 端解密，因此同时下发密钥信封（缺公钥的旧客户端返回 428）。
		if settings.ShareRetrievalMode == systemsetting.RetrievalRedirect {
			if redirectURL, ok := presignBlob(r.Context(), deps, blob, blobstore.PresignForShare); ok {
				var redirectEncryption map[string]any
				if blob.Encryption != nil {
					meta, metaErr := clientEncryptionMetadata(r, deps, blob)
					if metaErr != nil {
						if errors.Is(metaErr, errClientKeyMissing) {
							writeBusinessError(w, http.StatusPreconditionRequired, "请在浏览器中打开此页面后重试")
							return
						}
						writeBusinessError(w, http.StatusInternalServerError, "准备解密信息失败")
						return
					}
					redirectEncryption = meta
				}
				jobID, _, createErr := deps.SharingRepo.CreateDownloadJob(r.Context(), sharing.CreateDownloadJobParams{
					ShareID:  item.ID,
					PackMode: systemsetting.PackBackend, DeliveryMode: systemsetting.DeliveryBlob,
					ArtifactName: strPtr(sharedFile.Name), ArtifactContentType: strPtr(blobContentType(sharedFile)),
					ArtifactSHA256: strPtr(blob.SHA256), TotalBytes: blob.SizeBytes, ExpiresAt: expiresAt,
				})
				if createErr != nil {
					writeCreateDownloadJobError(w, createErr)
					return
				}
				// 302 交付由浏览器直连第三方，服务端无法观测传输：按一次完整交付
				// 结算（预扣额度 + 完整区间），保证下载次数与流量统计可用。
				activated, reserveErr := deps.SharingRepo.ReserveDownloadJob(r.Context(), jobID)
				if reserveErr != nil {
					writeBusinessError(w, http.StatusInternalServerError, "更新分享用量失败")
					return
				}
				if !activated {
					writeBusinessError(w, http.StatusTooManyRequests, "分享已过期或达到下载限制")
					return
				}
				settleEnd := blob.SizeBytes - 1
				if settleEnd < 0 {
					settleEnd = 0
				}
				if _, err := deps.SharingRepo.RecordDownloadRange(r.Context(), jobID, "artifact", 0, settleEnd); err != nil {
					log.Printf("302 交付结算失败：%v", err)
				}
				writeJSON(w, http.StatusCreated, map[string]any{
					"status": "ok", "packMode": systemsetting.PackBackend,
					"deliveryMode": "redirect",
					"expiresAt":    expiresAt, "url": redirectURL,
					// sizeBytes 统一为明文口径（前端展示与进度）；密文长度由
					// encryption.wireSize 提供；sha256 供接收端自行校验。
					"fileName": sharedFile.Name, "sizeBytes": blob.SizeBytes,
					"sha256":     blob.SHA256,
					"encryption": redirectEncryption,
				})
				return
			}
		}
		// 加密文件 + 服务器实时解密关：由浏览器端解密（响应下发密钥信封）。
		// 「一次性临时链接」由浏览器直接跳转消费地址、拿不到密钥信封，因此降级为
		// 「前端读取 Blob 流」，由前端拉取密文后解密。
		clientDecrypt := needsClientDecryption(blob, settings.ProxyRealtimeDecrypt, false)
		var encryptionMeta map[string]any
		deliveryMode := settings.ShareDeliveryMode
		if clientDecrypt {
			meta, metaErr := clientEncryptionMetadata(r, deps, blob)
			if metaErr != nil {
				if errors.Is(metaErr, errClientKeyMissing) {
					writeBusinessError(w, http.StatusPreconditionRequired, "请在浏览器中打开此页面后重试")
					return
				}
				writeBusinessError(w, http.StatusInternalServerError, "准备解密信息失败")
				return
			}
			encryptionMeta = meta
			deliveryMode = systemsetting.DeliveryBlob
		}
		jobID, _, createErr := deps.SharingRepo.CreateDownloadJob(r.Context(), sharing.CreateDownloadJobParams{
			ShareID:  item.ID,
			PackMode: systemsetting.PackBackend, DeliveryMode: settings.ShareDeliveryMode,
			ArtifactName: strPtr(sharedFile.Name), ArtifactContentType: strPtr(blobContentType(sharedFile)),
			ArtifactSHA256: strPtr(blob.SHA256), TotalBytes: blob.SizeBytes, ExpiresAt: expiresAt,
		})
		if createErr != nil {
			writeCreateDownloadJobError(w, createErr)
			return
		}
		sessionID, sessionErr := deps.Deliveries.create(&deliverySession{
			BlobID: blob.ID, Name: sharedFile.Name, ContentType: blobContentType(sharedFile),
			SizeBytes:      blob.SizeBytes,
			Purpose:        blobstore.PresignForShare,
			SHA256:         blob.SHA256,
			ClientDecrypt:  clientDecrypt,
			EncryptionMeta: encryptionMeta,
			OnStart: func(ctx context.Context) (bool, error) {
				return deps.SharingRepo.ReserveDownloadJob(ctx, jobID)
			},
			OnComplete: func(ctx context.Context, start, end int64, complete bool) {
				if !complete {
					return
				}
				// 单文件会话等价于旧的"后端制品"模式：以 artifact 作为对象标识，
				// 完整取流后提交下载次数与流量。
				if _, err := deps.SharingRepo.RecordDownloadRange(ctx, jobID, "artifact", start, end); err != nil {
					log.Printf("记录分享下载完成区间失败：%v", err)
				}
			},
		})
		if sessionErr != nil {
			writeBusinessError(w, http.StatusInternalServerError, "创建下载任务失败")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"status": "ok", "packMode": systemsetting.PackBackend,
			"deliveryMode": deliveryMode,
			"expiresAt":    expiresAt, "url": "/dl/" + sessionID,
			"fileName": sharedFile.Name, "sizeBytes": blob.SizeBytes,
			"sha256":     blob.SHA256,
			"encryption": encryptionMeta,
		})
		return
	}

	// 文件夹后端打包：生成 ZIP 制品并登记下载任务（制品仅存在于本机，交付固定中转）。
	if settings.FolderPackMode == systemsetting.PackBackend {
		path, size, name, contentType, zipFiles, cleanup, prepErr := prepareShareZip(r, deps, item)
		if prepErr != nil {
			if cleanup != nil {
				cleanup()
			}
			writeDownloadPreparationError(w, prepErr)
			return
		}
		checksum, diskSize, checksumErr := filestore.ChecksumFile(path)
		if checksumErr != nil || diskSize != size {
			if cleanup != nil {
				cleanup()
			}
			writeBusinessError(w, http.StatusUnprocessableEntity, "下载制品完整性校验失败")
			return
		}
		_, jobToken, createErr := deps.SharingRepo.CreateDownloadJob(r.Context(), sharing.CreateDownloadJobParams{
			ShareID:  item.ID,
			PackMode: systemsetting.PackBackend, DeliveryMode: settings.ShareDeliveryMode,
			ArtifactPath: strPtr(path), ArtifactName: strPtr(name),
			ArtifactContentType: strPtr(contentType), ArtifactSHA256: strPtr(checksum),
			ArtifactTemporary: true,
			TotalBytes:        size, ExpiresAt: expiresAt,
			// 后端模式计数只认 "artifact" 区间（不读文件清单），登记文件集合
			// 仅用于 claim 时的可交付性复核。
			Files: zipFiles,
		})
		if createErr != nil {
			if cleanup != nil {
				cleanup()
			}
			writeCreateDownloadJobError(w, createErr)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"status": "ok", "packMode": systemsetting.PackBackend,
			"deliveryMode": settings.ShareDeliveryMode,
			"expiresAt":    expiresAt, "url": "/api/share-downloads/" + jobToken,
			"fileName": name, "sizeBytes": size,
		})
		return
	}

	tree, err := buildShareTree(r, deps, item)
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取文件夹失败")
		return
	}
	var totalBytes int64
	files := make([]sharing.DownloadJobFileParam, 0)
	manifest := make([]map[string]any, 0, len(tree))
	for _, entry := range tree {
		manifestItem := map[string]any{
			"resourceId": entry.ID, "kind": entry.Kind,
			"relativePath": filepath.ToSlash(entry.RelativePath), "sizeBytes": entry.SizeBytes,
		}
		if entry.Kind == resource.KindFile {
			blob, blobErr := resourceBlob(r.Context(), deps, entry.Resource)
			if blobErr != nil {
				writeDownloadPreparationError(w, blobErr)
				return
			}
			// 下发明文哈希供前端逐文件校验（服务端不做交付前校验）。
			manifestItem["sha256"] = blob.SHA256
			totalBytes += entry.SizeBytes
			files = append(files, sharing.DownloadJobFileParam{
				ResourceID: entry.ID, RelativePath: filepath.ToSlash(entry.RelativePath),
			})
		}
		manifest = append(manifest, manifestItem)
	}
	_, jobToken, err := deps.SharingRepo.CreateDownloadJob(r.Context(), sharing.CreateDownloadJobParams{
		ShareID:  item.ID,
		PackMode: systemsetting.PackFrontend, DeliveryMode: settings.ShareDeliveryMode,
		TotalBytes: totalBytes, ExpiresAt: expiresAt, Files: files,
	})
	if err != nil {
		writeCreateDownloadJobError(w, err)
		return
	}
	for _, manifestItem := range manifest {
		if manifestItem["kind"] == resource.KindFile {
			manifestItem["url"] = "/api/share-downloads/" + jobToken + "/files/" + manifestItem["resourceId"].(string)
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"status": "ok", "packMode": systemsetting.PackFrontend,
		"deliveryMode": settings.ShareDeliveryMode, "expiresAt": expiresAt,
		"archiveName": tree[0].Name + ".zip", "totalBytes": totalBytes,
		"items": manifest,
	})
}

func shareDownloadJobHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/share-downloads/"), "/")
		if len(parts) == 1 && parts[0] != "" {
			artifact, err := deps.SharingRepo.ClaimDownloadArtifact(r.Context(), parts[0])
			if errors.Is(err, sharing.ErrNotFound) {
				writeBusinessError(w, http.StatusGone, "下载地址已失效或已使用")
				return
			}
			if err != nil {
				log.Printf("读取分享下载制品失败：%v", err)
				writeBusinessError(w, http.StatusInternalServerError, "准备下载失败")
				return
			}
			checksum, size, err := filestore.ChecksumFile(artifact.Path)
			if err != nil || checksum != artifact.SHA256 || size != artifact.SizeBytes {
				writeBusinessError(w, http.StatusUnprocessableEntity, "下载制品完整性校验失败")
				return
			}
			activated, err := deps.SharingRepo.ReserveDownloadJob(r.Context(), artifact.JobID)
			if err != nil {
				writeBusinessError(w, http.StatusInternalServerError, "更新分享用量失败")
				return
			}
			if !activated {
				writeBusinessError(w, http.StatusTooManyRequests, "分享已过期或达到下载限制")
				return
			}
			delivery := serveLocalArtifact(w, r, artifact.Path, artifact.SizeBytes, artifact.Name, artifact.ContentType)
			if delivery.complete {
				commitCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
				defer cancel()
				if _, err := deps.SharingRepo.RecordDownloadRange(commitCtx, artifact.JobID, "artifact", delivery.start, delivery.end); err != nil {
					log.Printf("记录分享下载完成区间失败：%v", err)
				}
			}
			return
		}
		if len(parts) == 3 && parts[0] != "" && parts[1] == "files" && parts[2] != "" {
			file, err := deps.SharingRepo.ClaimDownloadJobFile(r.Context(), parts[0], parts[2])
			if errors.Is(err, sharing.ErrNotFound) {
				writeBusinessError(w, http.StatusGone, "文件下载地址已失效或已使用")
				return
			}
			if err != nil {
				log.Printf("读取分享下载文件失败：%v", err)
				writeBusinessError(w, http.StatusInternalServerError, "准备下载失败")
				return
			}
			blob, blobErr := resourceBlob(r.Context(), deps, file.Resource)
			if blobErr != nil {
				writeDownloadPreparationError(w, blobErr)
				return
			}
			settings, settingsErr := deps.SystemSettings.Get(r.Context())
			if settingsErr != nil {
				writeBusinessError(w, http.StatusInternalServerError, "读取交付策略失败")
				return
			}
			// 302 交付（仅明文）：直接重定向到第三方直链——流量不经过我们，第三方
			// 响应无法携带解密元数据，因此加密文件固定走中转。第三方同样无法观测
			// 传输，按一次完整交付结算（管理员选择 302 即接受该口径）。
			if blob.Encryption == nil && settings.ShareRetrievalMode == systemsetting.RetrievalRedirect {
				if availableErr := deps.Blobs.VerifyAvailable(r.Context(), blob); availableErr == nil {
					if redirectURL, ok := presignBlob(r.Context(), deps, blob, blobstore.PresignForShare); ok {
						activated, reserveErr := deps.SharingRepo.ReserveDownloadJob(r.Context(), file.JobID)
						if reserveErr != nil {
							writeBusinessError(w, http.StatusInternalServerError, "更新分享用量失败")
							return
						}
						if !activated {
							writeBusinessError(w, http.StatusTooManyRequests, "分享已过期或达到下载限制")
							return
						}
						settleEnd := blob.SizeBytes - 1
						if settleEnd < 0 {
							settleEnd = 0
						}
						if _, err := deps.SharingRepo.RecordDownloadRange(r.Context(), file.JobID, file.Resource.ID, 0, settleEnd); err != nil {
							log.Printf("302 交付结算失败：%v", err)
						}
						http.Redirect(w, r, redirectURL, http.StatusFound)
						return
					}
				}
			}
			activated, err := deps.SharingRepo.ReserveDownloadJob(r.Context(), file.JobID)
			if err != nil {
				writeBusinessError(w, http.StatusInternalServerError, "更新分享用量失败")
				return
			}
			if !activated {
				writeBusinessError(w, http.StatusTooManyRequests, "分享已过期或达到下载限制")
				return
			}
			// 加密文件在"服务器实时解密关"时输出密文 + 解密元数据（前端逐文件解密）。
			if needsClientDecryption(blob, settings.ProxyRealtimeDecrypt, false) {
				meta, metaErr := clientEncryptionMetadata(r, deps, blob)
				if metaErr != nil {
					if errors.Is(metaErr, errClientKeyMissing) {
						writeBusinessError(w, http.StatusPreconditionRequired, "请在浏览器中打开此页面后重试")
						return
					}
					writeBusinessError(w, http.StatusInternalServerError, "准备解密信息失败")
					return
				}
				delivery, ok := deliverBlobContent(w, r, deps, blob, file.Resource.Name,
					blobContentType(file.Resource), "", true, meta, blobstore.PresignForShare)
				if !ok {
					return
				}
				if delivery.complete {
					// 计数换算回明文坐标：与任务登记的明文总量同坐标系，
					// 防止构造"明文长度的密文 Range"提前触发完整下载判定。
					plainStart, plainEnd := plainRangeForWireRange(delivery.start, delivery.end,
						blobstore.EncryptionChunkSize, blob.SizeBytes)
					commitCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
					defer cancel()
					if _, err := deps.SharingRepo.RecordDownloadRange(commitCtx, file.JobID, file.Resource.ID, plainStart, plainEnd); err != nil {
						log.Printf("记录分享文件下载完成区间失败：%v", err)
					}
				}
				return
			}
			delivery, ok := deliverBlobContent(w, r, deps, blob, file.Resource.Name,
				blobContentType(file.Resource), "", false, nil, blobstore.PresignForShare)
			if !ok {
				return
			}
			if delivery.complete {
				commitCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
				defer cancel()
				if _, err := deps.SharingRepo.RecordDownloadRange(commitCtx, file.JobID, file.Resource.ID, delivery.start, delivery.end); err != nil {
					log.Printf("记录分享文件下载完成区间失败：%v", err)
				}
			}
			return
		}
		writeBusinessError(w, http.StatusNotFound, "下载地址不存在")
	}
}

func writeCreateDownloadJobError(w http.ResponseWriter, err error) {
	if errors.Is(err, sharing.ErrLimitReached) {
		writeBusinessError(w, http.StatusTooManyRequests, "分享已过期或达到下载限制")
		return
	}
	log.Printf("创建下载任务失败：%v", err)
	writeBusinessError(w, http.StatusInternalServerError, "创建下载任务失败")
}

func strPtr(value string) *string { return &value }

func directDownloadHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		token := strings.TrimPrefix(r.URL.Path, "/d/")
		if token == "" || strings.Contains(token, "/") {
			writeBusinessError(w, http.StatusNotFound, "直链不存在")
			return
		}
		item, err := deps.SharingRepo.GetDirectLinkByToken(r.Context(), token)
		if errors.Is(err, sharing.ErrAdminBlocked) {
			writeBusinessError(w, http.StatusForbidden, "该直链已被管理员封禁")
			return
		}
		if errors.Is(err, sharing.ErrNotFound) {
			writeBusinessError(w, http.StatusNotFound, "直链不存在")
			return
		}
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取直链失败")
			return
		}
		if item.ExpiresAt != nil && !item.ExpiresAt.After(time.Now()) {
			writeBusinessError(w, http.StatusGone, "该直链已过期")
			return
		}
		if !item.IsActive {
			writeBusinessError(w, http.StatusGone, "该直链已停用")
			return
		}
		if item.Resource.Kind != resource.KindFile {
			writeBusinessError(w, http.StatusGone, "直链仅支持单个文件")
			return
		}
		if !requireDeliverableFile(w, r, deps, item.Resource.ID) {
			return
		}
		if item.DownloadLimit != nil && item.DownloadCount >= *item.DownloadLimit {
			writeBusinessError(w, http.StatusGone, "该直链的下载次数已用完")
			return
		}
		if item.TrafficLimitBytes != nil && item.TrafficUsedBytes+item.Resource.SizeBytes > *item.TrafficLimitBytes {
			writeBusinessError(w, http.StatusGone, "该直链的可用流量已用完")
			return
		}
		if r.Method == http.MethodHead {
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// 直链交付不做交付前全量校验：生成直链时已展示 SHA-256，并由
		// X-XPH-Content-SHA256 响应头下发，使用者可自行核对；交付走统一实现，
		// 完整下载后计入直链次数与流量。
		blob, blobErr := resourceBlob(r.Context(), deps, item.Resource)
		if blobErr != nil {
			writeDownloadPreparationError(w, blobErr)
			return
		}
		size := blob.SizeBytes
		// 直链始终由本机中转交付：对外只提供一个固定地址、且必须交给用户可用的
		// 明文内容（第三方地址是临时的、加密对象在其上还是密文），因此不做 302。
		// 「直链优先使用」只影响服务端从 123 取内容时消耗哪份额度（直链流量 /
		// 自用下载流量），两条通道都经本机中转、交付的字节完全相同。
		serveDeliverySession(w, r, deps, &deliverySession{
			Purpose: blobstore.PresignForDirect,
			BlobID:  blob.ID, Name: item.Resource.Name, ContentType: blobContentType(item.Resource),
			SizeBytes: size, SHA256: blob.SHA256,
			OnComplete: func(ctx context.Context, start, end int64, complete bool) {
				// 只在"取到文件末尾"时计数：直链没有任务级的区间归并（不同于分享
				// 下载），若额外要求 start==0，客户端只要把一次下载拆成多段 Range
				// （下载器/断点续传的常态）就永远不满足完整下载条件，次数与流量
				// 限制会被完全绕过。以 end==size-1 为准：取完整文件的客户端必然
				// 命中一次；只取中段的探测请求不计数。
				_ = start
				if !complete || end != size-1 {
					return
				}
				if _, err := deps.SharingRepo.CompleteDirectDownload(ctx, item.ID, size); err != nil {
					log.Printf("记录直链完整下载失败：%v", err)
				}
			},
		})
	}
}

func loadUsableShare(r *http.Request, deps Deps, token string) (sharing.Share, int, error) {
	item, err := deps.SharingRepo.GetShareByToken(r.Context(), token)
	if errors.Is(err, sharing.ErrAdminBlocked) {
		return sharing.Share{}, http.StatusForbidden, errors.New("该分享已被管理员封禁")
	}
	if errors.Is(err, sharing.ErrNotFound) {
		return sharing.Share{}, http.StatusNotFound, errors.New("分享不存在")
	}
	if err != nil {
		return sharing.Share{}, http.StatusInternalServerError, errors.New("读取分享失败")
	}
	if !item.IsActive || (item.ExpiresAt != nil && !item.ExpiresAt.After(time.Now())) {
		return sharing.Share{}, http.StatusGone, errors.New("分享已失效")
	}
	approved, err := deps.AdminRepo.IsShareApproved(r.Context(), item.ID)
	if err != nil {
		return sharing.Share{}, http.StatusInternalServerError, errors.New("读取分享审核状态失败")
	}
	if !approved {
		return sharing.Share{}, http.StatusForbidden, errors.New("分享正在审核或未通过审核")
	}
	return item, 0, nil
}

func resourceTreeApproved(w http.ResponseWriter, r *http.Request, deps Deps, item resource.Resource) bool {
	if item.Kind == resource.KindFile {
		return requireApprovedFile(w, r, deps, item.ID)
	}
	tree, err := deps.ResourceRepo.ListTree(r.Context(), item.ID)
	if errors.Is(err, resource.ErrNotFound) {
		writeBusinessError(w, http.StatusNotFound, "文件夹不存在")
		return false
	}
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取文件夹内容失败")
		return false
	}
	for _, entry := range tree {
		if entry.Kind == resource.KindFile && !requireApprovedFile(w, r, deps, entry.ID) {
			return false
		}
	}
	return true
}

func normalizeResourceIDs(resourceIDs []string) []string {
	seen := make(map[string]struct{}, len(resourceIDs))
	normalized := make([]string, 0, len(resourceIDs))
	for _, value := range resourceIDs {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

func parseManagedLinkID(path, prefix string) (int64, error) {
	value := strings.TrimPrefix(path, prefix)
	if value == "" || strings.Contains(value, "/") {
		return 0, errors.New("invalid id")
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

func normalizeLinkIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func isSingleFileShare(item sharing.Share) bool {
	return len(item.Resources) == 1 && item.Resources[0].Kind == resource.KindFile
}

func shareSummary(item sharing.Share) (string, string, int64) {
	if len(item.Resources) == 1 {
		root := item.Resources[0]
		return root.Name, root.Kind, root.SizeBytes
	}
	return "分享文件", resource.KindFolder, 0
}

// buildShareTree 将多个独立资源映射到一个只存在于当前分享中的虚拟根目录，
// 不移动或复制用户原有的文件。顶层重名时只调整分享视图中的显示名称。
func buildShareTree(r *http.Request, deps Deps, item sharing.Share) ([]resource.TreeEntry, error) {
	virtualID := fmt.Sprintf("share-virtual-root-%d", item.ID)
	virtualRoot := resource.Resource{
		ID: virtualID, OwnerUserID: item.OwnerUserID, Kind: resource.KindFolder,
		Name: "分享文件", CreatedAt: item.CreatedAt, UpdatedAt: item.CreatedAt,
	}
	tree, err := buildResourceSelectionTree(r, deps, item.Resources, virtualRoot)
	if err != nil {
		return nil, err
	}
	return filterDeliverableTree(r, deps, tree)
}

// filterDeliverableTree 从分享目录树中剔除当前不可交付的文件（已入回收站/
// 彻底删除、被管理员拉黑、审核处于待审或驳回）。分享清单、文件夹 ZIP 打包与
// 前端打包清单共用此过滤：处置或内容替换后立即停止交付，分享创建后新增进
// 目录的待审文件也不会被自动并入。
func filterDeliverableTree(r *http.Request, deps Deps, tree []resource.TreeEntry) ([]resource.TreeEntry, error) {
	fileIDs := make([]string, 0, len(tree))
	for _, entry := range tree {
		if entry.Resource.Kind == resource.KindFile {
			fileIDs = append(fileIDs, entry.Resource.ID)
		}
	}
	if len(fileIDs) == 0 {
		return tree, nil
	}
	deliverable, err := deps.AdminRepo.DeliverableFileIDs(r.Context(), fileIDs)
	if err != nil {
		return nil, err
	}
	filtered := make([]resource.TreeEntry, 0, len(tree))
	for _, entry := range tree {
		if entry.Resource.Kind == resource.KindFile {
			if _, ok := deliverable[entry.Resource.ID]; !ok {
				continue
			}
		}
		filtered = append(filtered, entry)
	}
	return filtered, nil
}

// requireDeliverableFile 校验资源当前可对外交付。用于分享预览、单文件下载
// 任务创建与直链交付；已签发任务的逐文件取流由 ClaimDownloadJobFile 的 SQL
// 自身把关（处置后立即失效，包括已签发的任务）。
func requireDeliverableFile(w http.ResponseWriter, r *http.Request, deps Deps, resourceID string) bool {
	deliverable, err := deps.AdminRepo.DeliverableFileIDs(r.Context(), []string{resourceID})
	if err != nil {
		log.Printf("读取文件交付状态失败 resource=%s：%v", resourceID, err)
		writeBusinessError(w, http.StatusInternalServerError, "读取文件状态失败")
		return false
	}
	if _, ok := deliverable[resourceID]; !ok {
		writeBusinessError(w, http.StatusForbidden, "文件正在审核或已被限制访问")
		return false
	}
	return true
}

func buildResourceSelectionTree(r *http.Request, deps Deps, roots []resource.Resource, virtualRoot resource.Resource) ([]resource.TreeEntry, error) {
	if len(roots) == 1 {
		root := roots[0]
		if root.Kind == resource.KindFile {
			return []resource.TreeEntry{{Resource: root, RelativePath: root.Name}}, nil
		}
		tree, err := deps.ResourceRepo.ListTree(r.Context(), root.ID)
		if err != nil {
			return nil, err
		}
		tree[0].ParentID = nil
		return tree, nil
	}

	tree := []resource.TreeEntry{{Resource: virtualRoot, RelativePath: virtualRoot.Name}}
	usedNames := make(map[string]int, len(roots))
	for _, sharedRoot := range roots {
		displayName := uniqueVirtualName(sharedRoot.Name, usedNames)
		if sharedRoot.Kind == resource.KindFile {
			copyRoot := sharedRoot
			copyRoot.Name = displayName
			copyRoot.ParentID = strPtr(virtualRoot.ID)
			tree = append(tree, resource.TreeEntry{
				Resource: copyRoot, RelativePath: filepath.Join(virtualRoot.Name, displayName),
			})
			continue
		}
		subtree, err := deps.ResourceRepo.ListTree(r.Context(), sharedRoot.ID)
		if err != nil {
			return nil, err
		}
		for index := range subtree {
			relativeSuffix := strings.TrimPrefix(subtree[index].RelativePath, sharedRoot.Name)
			subtree[index].RelativePath = filepath.Join(virtualRoot.Name, displayName, relativeSuffix)
			if index == 0 {
				subtree[index].Name = displayName
				subtree[index].ParentID = strPtr(virtualRoot.ID)
			}
		}
		tree = append(tree, subtree...)
	}
	return tree, nil
}

func uniqueVirtualName(name string, used map[string]int) string {
	key := strings.ToLower(name)
	used[key]++
	if used[key] == 1 {
		return name
	}
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	extension := filepath.Ext(name)
	for suffix := used[key]; ; suffix++ {
		candidate := fmt.Sprintf("%s (%d)%s", stem, suffix, extension)
		candidateKey := strings.ToLower(candidate)
		if used[candidateKey] == 0 {
			used[candidateKey] = 1
			return candidate
		}
	}
}

// prepareShareZip 为文件夹分享生成后端 ZIP 制品。单文件分享不再落制品，
// 由统一交付会话直接流式输出。
func prepareShareZip(r *http.Request, deps Deps, item sharing.Share) (string, int64, string, string, []sharing.DownloadJobFileParam, func(), error) {
	tree, err := buildShareTree(r, deps, item)
	if err != nil {
		return "", 0, "", "", nil, nil, err
	}
	// 记录制品实际打包的文件集合：claim 时按该集合复核可交付性，任一成员文件
	// 此后被处置（回收站/拉黑/审核不通过）即拒绝旧制品，需重新打包。
	files := make([]sharing.DownloadJobFileParam, 0, len(tree))
	for _, entry := range tree {
		if entry.Kind == resource.KindFile {
			files = append(files, sharing.DownloadJobFileParam{
				ResourceID:   entry.ID,
				RelativePath: filepath.ToSlash(entry.RelativePath),
			})
		}
	}
	path, size, err := buildZip(r.Context(), deps, tree)
	cleanup := func() {
		if path != "" {
			_ = os.Remove(path)
		}
	}
	return path, size, tree[0].Name + ".zip", "application/zip", files, cleanup, err
}

type downloadDelivery struct {
	start, end int64
	complete   bool
}

type countingResponseWriter struct {
	http.ResponseWriter
	status  int
	written int64
}

func (w *countingResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *countingResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(data)
	w.written += int64(n)
	return n, err
}

func requestedDownloadRange(header string, size int64) (int64, int64, bool) {
	if header == "" {
		return 0, size - 1, size >= 0
	}
	if !strings.HasPrefix(header, "bytes=") || strings.Contains(header, ",") {
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(header, "bytes="), "-")
	if len(parts) != 2 {
		return 0, 0, false
	}
	if parts[0] == "" {
		suffix, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, false
		}
		if suffix > size {
			suffix = size
		}
		return size - suffix, size - 1, true
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false
	}
	end := size - 1
	if parts[1] != "" {
		end, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || end < start {
			return 0, 0, false
		}
		if end >= size {
			end = size - 1
		}
	}
	return start, end, true
}

func previewTree(tree []resource.TreeEntry) []map[string]any {
	items := make([]map[string]any, 0, len(tree))
	for _, entry := range tree {
		item := map[string]any{
			"id": entry.ID, "parentId": entry.ParentID, "kind": entry.Kind,
			"name": entry.Name, "relativePath": filepath.ToSlash(entry.RelativePath),
			"sizeBytes": entry.SizeBytes, "createdAt": entry.CreatedAt, "updatedAt": entry.UpdatedAt,
		}
		if entry.MimeType != nil {
			item["mimeType"] = *entry.MimeType
		}
		items = append(items, item)
	}
	return items
}

func sharePassword(req createShareRequest, options randomtoken.CodeOptions) (*string, string, error) {
	if req.NoPassword && req.Password != nil {
		return nil, "", errors.New("noPassword 与 password 不能同时设置")
	}
	if req.NoPassword {
		return nil, "", nil
	}
	password := ""
	generated := ""
	if req.Password != nil {
		password = *req.Password
	}
	if password == "" {
		var err error
		password, err = randomtoken.NewCode(options)
		if err != nil {
			return nil, "", errors.New("生成随机密码失败")
		}
		generated = password
	}
	if len(password) > 128 {
		return nil, "", errors.New("密码过长")
	}
	return &password, generated, nil
}

func verifySharePassword(input, expected string) bool {
	inputHash := sha256.Sum256([]byte(input))
	expectedHash := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(inputHash[:], expectedHash[:]) == 1
}

func expiryFromSeconds(seconds *int64, defaultDuration time.Duration) (*time.Time, error) {
	if seconds == nil {
		if defaultDuration == 0 {
			return nil, nil
		}
		value := time.Now().Add(defaultDuration)
		return &value, nil
	}
	if *seconds < 0 || *seconds > int64(maxLinkLifetime/time.Second) {
		return nil, errors.New("有效期必须在 0 到 10 年之间")
	}
	if *seconds == 0 {
		return nil, nil
	}
	value := time.Now().Add(time.Duration(*seconds) * time.Second)
	return &value, nil
}

// pickupDefaultLifetime 把系统配置的取件码默认有效期（pgtype.Int8）转成指针：
// NULL 表示"默认永久"。
func pickupDefaultLifetime(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	seconds := value.Int64
	return &seconds
}

// pickupExpiryFromRequest 解析取件码有效期：
//   - 未提供：使用系统默认（pickup_max_lifetime_seconds，NULL = 永久）
//   - 0：永久，仅当管理端允许"永久取件码"时
//   - 正数：不得超过 10 年上限（系统默认值只是默认，不再作为硬上限）
//
// 说明：允许自选较长期限是为了让分享可长期使用；码空间有限，失效码由维护
// 任务与"清理失效取件码"动作释放后可被重新分配；管理员可用"允许永久取件码"
// 开关统一禁止永久码。
func pickupExpiryFromRequest(seconds *int64, defaultLifetime *int64, allowPermanent bool) (*time.Time, error) {
	if seconds == nil {
		if defaultLifetime == nil {
			if !allowPermanent {
				return nil, errors.New("管理员未允许永久取件码，请选择有效期")
			}
			return nil, nil
		}
		return pickupExpiryFromSeconds(*defaultLifetime)
	}
	if *seconds < 0 || *seconds > int64(maxLinkLifetime/time.Second) {
		return nil, errors.New("有效期必须在 0 到 10 年之间")
	}
	if *seconds == 0 {
		if !allowPermanent {
			return nil, errors.New("管理员未允许永久取件码，请选择有效期")
		}
		return nil, nil
	}
	return pickupExpiryFromSeconds(*seconds)
}

func pickupExpiryFromSeconds(seconds int64) (*time.Time, error) {
	if seconds < 0 {
		return nil, errors.New("取件码有效期不能为负数")
	}
	if seconds == 0 {
		return nil, nil
	}
	value := time.Unix(time.Now().Unix()+seconds, 0)
	if value.Year() > 9999 || value.Before(time.Now()) {
		return nil, errors.New("取件码有效期过大")
	}
	return &value, nil
}

func validateOptionalLimit(count, traffic *int64) error {
	if count != nil && *count < 0 {
		return errors.New("下载次数限制不能为负数")
	}
	if traffic != nil && *traffic < 0 {
		return errors.New("流量限制不能为负数")
	}
	return nil
}

func withinActiveShareQuota(r *http.Request, deps Deps, userID int64) (bool, error) {
	profile, err := deps.QuotaRepo.GetEffectiveQuotaByUser(r.Context(), userID)
	if err != nil {
		return false, err
	}
	if !profile.ActiveShareCountLimit.Valid || quotaExempt(r.Context(), deps, userID) {
		return true, nil
	}
	count, err := deps.SharingRepo.CountActiveSharesByOwner(r.Context(), userID)
	return count < profile.ActiveShareCountLimit.Int64, err
}

func withinActiveDirectQuota(r *http.Request, deps Deps, userID int64) (bool, error) {
	profile, err := deps.QuotaRepo.GetEffectiveQuotaByUser(r.Context(), userID)
	if err != nil {
		return false, err
	}
	if !profile.ActiveDirectLinkLimit.Valid || quotaExempt(r.Context(), deps, userID) {
		return true, nil
	}
	count, err := deps.SharingRepo.CountActiveDirectLinksByOwner(r.Context(), userID)
	return count < profile.ActiveDirectLinkLimit.Int64, err
}

func writeDownloadPreparationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, blobstore.ErrChecksumMismatch):
		writeBusinessError(w, http.StatusUnprocessableEntity, "文件完整性校验失败")
	case errors.Is(err, blobstore.ErrBlobNotFound):
		// 对象记录已不存在（资源残留引用 / 链接已损坏）：明确告知不可用，
		// 不要伪装成服务端故障。
		writeBusinessError(w, http.StatusGone, "文件已不存在或已损坏")
	case errors.Is(err, blobstore.ErrBackendUnavailable):
		// 后端未加载（配置不完整或类型未实现）：属于服务端状态问题。
		writeBusinessError(w, http.StatusServiceUnavailable, "存储后端不可用，请联系管理员")
	default:
		log.Printf("准备下载失败：%v", err)
		writeBusinessError(w, http.StatusInternalServerError, "准备下载失败")
	}
}
