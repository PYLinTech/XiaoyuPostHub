package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/filestore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/randomtoken"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
	"github.com/PYLinTech/XiaoyuPostHub/backend/sharing"
	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
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
		if req.ShareType == "pickup" && req.ExpiresInSeconds != nil {
			writeBusinessError(w, http.StatusBadRequest, "取件码有效期由系统统一配置，创建时不能指定")
			return
		}
		expiresAt, err := expiryFromSeconds(req.ExpiresInSeconds, 24*time.Hour)
		if err != nil {
			writeBusinessError(w, http.StatusBadRequest, err.Error())
			return
		}
		codeSettings, err := deps.SystemSettings.Get(r.Context())
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取分享码配置失败")
			return
		}
		if req.ShareType == "pickup" {
			// 取件码有效期完全由系统管理员统一配置，不接受创建者传入的值。
			// NULL 表示永久；新安装默认值为 3600 秒。
			seconds := int64(0)
			if codeSettings.PickupMaxLifetimeSeconds.Valid {
				seconds = codeSettings.PickupMaxLifetimeSeconds.Int64
			}
			expiresAt, err = pickupExpiryFromSeconds(seconds)
			if err != nil {
				writeBusinessError(w, http.StatusBadRequest, err.Error())
				return
			}
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
		writeJSON(w, http.StatusCreated, map[string]any{
			"status": "ok", "token": token, "url": "/d/" + token,
			"resource": item, "expiresAt": created.ExpiresAt,
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
		var expiresAt *time.Time
		if req.ExpiresInSeconds != nil {
			expiresAt, err = expiryFromSeconds(req.ExpiresInSeconds, 0)
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
			var activeCount, enableCount int64
			if shares {
				activeCount, quotaErr = deps.SharingRepo.CountActiveSharesByOwner(r.Context(), u.ID)
				if quotaErr == nil {
					enableCount, quotaErr = deps.SharingRepo.CountSharesToEnableByOwner(r.Context(), u.ID, req.IDs)
				}
				if quotaErr == nil && profile.ActiveShareCountLimit.Valid && activeCount+enableCount > profile.ActiveShareCountLimit.Int64 {
					writeBusinessError(w, http.StatusTooManyRequests, "启用后将超过有效分享数量上限")
					return
				}
			} else {
				activeCount, quotaErr = deps.SharingRepo.CountActiveDirectLinksByOwner(r.Context(), u.ID)
				if quotaErr == nil {
					enableCount, quotaErr = deps.SharingRepo.CountDirectLinksToEnableByOwner(r.Context(), u.ID, req.IDs)
				}
				if quotaErr == nil && profile.ActiveDirectLinkLimit.Valid && activeCount+enableCount > profile.ActiveDirectLinkLimit.Int64 {
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
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
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
		item, err := deps.SharingRepo.GetShareByPickupCode(r.Context(), parts[0])
		if err != nil || item.TokenValue == nil {
			writeBusinessError(w, http.StatusNotFound, "取件码无效或已过期")
			return
		}
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
	if item.PasswordValue != nil && !verifySharePassword(r.Header.Get("X-Share-Password"), *item.PasswordValue) {
		writeBusinessError(w, http.StatusUnauthorized, "分享密码错误")
		return
	}
	if !isSingleFileShare(item) {
		writeBusinessError(w, http.StatusBadRequest, "文件夹请使用目录预览")
		return
	}
	sharedFile := item.Resources[0]
	path, err := deps.FileStore.ValidateFile(r.Context(), sharedFile)
	if err != nil {
		writeBusinessError(w, http.StatusUnprocessableEntity, "文件完整性校验失败")
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeBusinessError(w, http.StatusNotFound, "文件不存在")
		return
	}
	defer file.Close()
	contentType := ""
	if sharedFile.MimeType != nil {
		contentType = strings.TrimSpace(*sharedFile.MimeType)
	}
	if contentType == "" {
		contentType = mime.TypeByExtension(strings.ToLower(filepath.Ext(sharedFile.Name)))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": sharedFile.Name}))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, sharedFile.Name, sharedFile.UpdatedAt, file)
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
		"folderPackMode":    settings.FolderPackMode,
		"shareDeliveryMode": settings.ShareDeliveryMode,
		"prepareUrl":        "/api/shares/" + token + "/downloads",
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
	if item.PasswordValue != nil && !verifySharePassword(r.Header.Get("X-Share-Password"), *item.PasswordValue) {
		writeBusinessError(w, http.StatusUnauthorized, "分享密码错误")
		return
	}
	settings, err := deps.SystemSettings.Get(r.Context())
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取下载策略失败")
		return
	}
	expiresAt := time.Now().Add(5 * time.Minute)

	// 单文件始终作为一个后端制品；文件夹根据全局策略选择后端 ZIP 或前端清单。
	if isSingleFileShare(item) || settings.FolderPackMode == systemsetting.PackBackend {
		path, size, name, contentType, cleanup, prepErr := prepareShareDownload(r, deps, item)
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
		jobToken, createErr := deps.SharingRepo.CreateDownloadJob(r.Context(), sharing.CreateDownloadJobParams{
			ShareID:  item.ID,
			PackMode: systemsetting.PackBackend, DeliveryMode: settings.ShareDeliveryMode,
			ArtifactPath: strPtr(path), ArtifactName: strPtr(name),
			ArtifactContentType: strPtr(contentType), ArtifactSHA256: strPtr(checksum),
			ArtifactTemporary: !isSingleFileShare(item),
			TotalBytes:        size, ExpiresAt: expiresAt,
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
			if _, err := deps.FileStore.ValidateFile(r.Context(), entry.Resource); err != nil {
				writeDownloadPreparationError(w, err)
				return
			}
			totalBytes += entry.SizeBytes
			files = append(files, sharing.DownloadJobFileParam{
				ResourceID: entry.ID, RelativePath: filepath.ToSlash(entry.RelativePath),
			})
		}
		manifest = append(manifest, manifestItem)
	}
	jobToken, err := deps.SharingRepo.CreateDownloadJob(r.Context(), sharing.CreateDownloadJobParams{
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
			if err != nil {
				writeBusinessError(w, http.StatusGone, "下载地址已失效或已使用")
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
			delivery := serveDownload(w, r, artifact.Path, artifact.SizeBytes, artifact.Name, artifact.ContentType)
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
			if err != nil {
				writeBusinessError(w, http.StatusGone, "文件下载地址已失效或已使用")
				return
			}
			path, err := deps.FileStore.ValidateFile(r.Context(), file.Resource)
			if err != nil {
				writeDownloadPreparationError(w, err)
				return
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
			contentType := "application/octet-stream"
			if file.Resource.MimeType != nil && *file.Resource.MimeType != "" {
				contentType = *file.Resource.MimeType
			}
			delivery := serveDownload(w, r, path, file.Resource.SizeBytes, file.Resource.Name, contentType)
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
		token := strings.TrimPrefix(r.URL.Path, "/api/direct/")
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
		path, size, name, contentType, cleanup, err := prepareDownload(r, deps, item.Resource)
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			writeDownloadPreparationError(w, err)
			return
		}
		delivery := serveDownload(w, r, path, size, name, contentType)
		if delivery.complete && delivery.start == 0 && delivery.end == size-1 {
			commitCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			defer cancel()
			if _, err := deps.SharingRepo.CompleteDirectDownload(commitCtx, item.ID, size); err != nil {
				log.Printf("记录直链完整下载失败：%v", err)
			}
		}
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
	return buildResourceSelectionTree(r, deps, item.Resources, virtualRoot)
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

func prepareShareDownload(r *http.Request, deps Deps, item sharing.Share) (string, int64, string, string, func(), error) {
	if isSingleFileShare(item) {
		return prepareDownload(r, deps, item.Resources[0])
	}
	tree, err := buildShareTree(r, deps, item)
	if err != nil {
		return "", 0, "", "", nil, err
	}
	path, size, err := deps.FileStore.BuildZip(r.Context(), tree)
	cleanup := func() {
		if path != "" {
			_ = os.Remove(path)
		}
	}
	return path, size, tree[0].Name + ".zip", "application/zip", cleanup, err
}

func prepareDownload(r *http.Request, deps Deps, item resource.Resource) (string, int64, string, string, func(), error) {
	if item.Kind == resource.KindFile {
		path, err := deps.FileStore.ValidateFile(r.Context(), item)
		contentType := "application/octet-stream"
		if item.MimeType != nil && *item.MimeType != "" {
			contentType = *item.MimeType
		}
		return path, item.SizeBytes, item.Name, contentType, nil, err
	}
	tree, err := deps.ResourceRepo.ListTree(r.Context(), item.ID)
	if err != nil {
		return "", 0, "", "", nil, err
	}
	path, size, err := deps.FileStore.BuildZip(r.Context(), tree)
	cleanup := func() {
		if path != "" {
			_ = os.Remove(path)
		}
	}
	return path, size, item.Name + ".zip", "application/zip", cleanup, err
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

func serveDownload(w http.ResponseWriter, r *http.Request, path string, size int64, name, contentType string) downloadDelivery {
	f, err := os.Open(path)
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "打开下载文件失败")
		return downloadDelivery{}
	}
	defer f.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	counter := &countingResponseWriter{ResponseWriter: w}
	start, end, trackable := requestedDownloadRange(r.Header.Get("Range"), size)
	http.ServeContent(counter, r, name, time.Time{}, f)
	statusOK := counter.status == http.StatusOK || counter.status == http.StatusPartialContent
	return downloadDelivery{start: start, end: end, complete: trackable && statusOK && counter.written == end-start+1}
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
	if !profile.ActiveShareCountLimit.Valid {
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
	if !profile.ActiveDirectLinkLimit.Valid {
		return true, nil
	}
	count, err := deps.SharingRepo.CountActiveDirectLinksByOwner(r.Context(), userID)
	return count < profile.ActiveDirectLinkLimit.Int64, err
}

func writeDownloadPreparationError(w http.ResponseWriter, err error) {
	if errors.Is(err, filestore.ErrChecksumMismatch) {
		writeBusinessError(w, http.StatusUnprocessableEntity, "文件完整性校验失败")
		return
	}
	log.Printf("准备下载失败：%v", err)
	writeBusinessError(w, http.StatusInternalServerError, "准备下载失败")
}
