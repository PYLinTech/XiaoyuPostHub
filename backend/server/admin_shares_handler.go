package server

import (
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/PYLinTech/XiaoyuPostHub/backend/sharing"
	"github.com/PYLinTech/XiaoyuPostHub/backend/upload"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

// handleAdminShares 管理端分享与取件码管理：
//
//	GET    /api/admin/shares?type=pickup|link&q=<关键字>   列表（含取件码状态）
//	PUT    /api/admin/shares/{id}                          改有效期（0/null = 永久）/启停/解封
//	DELETE /api/admin/shares/{id}                          删除（软删 + 释放取件码占位）
//	POST   /api/admin/shares/release-codes                 一键释放全站失效取件码
//
// 管理员可随时调整任意分享的有效期（含永久）：永久码不占"有效分享"配额以外的
// 额外资源，但会占用码空间，因此配套 release-codes 维护动作腾出码位置。
func handleAdminShares(w http.ResponseWriter, r *http.Request, deps Deps, actor user.User, path string) {
	switch {
	case path == "shares" && r.Method == http.MethodGet:
		// limit 可选：默认 500，上限 2000（列表按创建时间倒序，超出会截断）。
		limit := 500
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, parseErr := strconv.Atoi(raw)
			if parseErr != nil || parsed < 1 {
				writeBusinessError(w, http.StatusBadRequest, "limit 无效")
				return
			}
			limit = parsed
		}
		items, err := deps.SharingRepo.ListAdminShares(r.Context(),
			strings.TrimSpace(r.URL.Query().Get("type")),
			strings.TrimSpace(r.URL.Query().Get("q")), limit)
		if err != nil {
			log.Printf("读取分享列表失败：%v", err)
			writeBusinessError(w, http.StatusInternalServerError, "读取分享列表失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "items": items})
	case path == "shares/release-codes" && r.Method == http.MethodPost:
		freed, err := deps.SharingRepo.ReleaseDeadPickupCodes(r.Context(), nil)
		if err != nil {
			log.Printf("释放失效取件码失败：%v", err)
			writeBusinessError(w, http.StatusInternalServerError, "清理失效取件码失败")
			return
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "share.release_codes", "shares", "", map[string]any{"released": freed}, net.ParseIP(clientIP(r)))
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "released": freed})
	case strings.HasPrefix(path, "shares/") && r.Method == http.MethodPut:
		adminUpdateShare(w, r, deps, actor, strings.TrimPrefix(path, "shares/"))
	case strings.HasPrefix(path, "shares/") && r.Method == http.MethodDelete:
		adminDeleteShare(w, r, deps, actor, strings.TrimPrefix(path, "shares/"))
	default:
		writeBusinessError(w, http.StatusNotFound, "管理接口不存在")
	}
}

type adminShareUpdateRequest struct {
	// ExpiresInSeconds：0 = 永久；缺省表示不修改有效期。
	ExpiresInSeconds *int64 `json:"expiresInSeconds"`
	// Active：启用/停用；缺省表示不修改。
	Active *bool `json:"active"`
	// Unblock：解除管理员封禁与软删除标记。
	Unblock bool `json:"unblock"`
}

func adminUpdateShare(w http.ResponseWriter, r *http.Request, deps Deps, actor user.User, rawID string) {
	id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
	if err != nil || id < 1 {
		writeBusinessError(w, http.StatusBadRequest, "分享编号无效")
		return
	}
	var req adminShareUpdateRequest
	if err := decodeSmallJSON(w, r, &req); err != nil {
		writeBusinessError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	state, stateErr := deps.SharingRepo.GetShareState(r.Context(), nil, id)
	if stateErr != nil {
		if errors.Is(stateErr, sharing.ErrNotFound) {
			writeBusinessError(w, http.StatusNotFound, "分享不存在")
			return
		}
		writeBusinessError(w, http.StatusInternalServerError, "读取分享失败")
		return
	}
	params := sharing.AdminUpdateShareParams{ID: id, Unblock: req.Unblock}
	if req.ExpiresInSeconds != nil {
		// 允许管理员把直接进入回收站/过期状态的分享改回可用（改期即恢复语义）。
		if state.IsPickup {
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
			expiresAt, expiryErr := pickupExpiryFromRequest(req.ExpiresInSeconds, pickupDefaultLifetime(codeSettings.PickupMaxLifetimeSeconds), knobs.PickupAllowPermanent)
			if expiryErr != nil {
				writeBusinessError(w, http.StatusBadRequest, expiryErr.Error())
				return
			}
			params.UpdateExpiresAt, params.ExpiresAt = true, expiresAt
		} else {
			expiresAt, expiryErr := expiryFromSeconds(req.ExpiresInSeconds, 0)
			if expiryErr != nil {
				writeBusinessError(w, http.StatusBadRequest, expiryErr.Error())
				return
			}
			params.UpdateExpiresAt, params.ExpiresAt = true, expiresAt
		}
	}
	if req.Active != nil {
		params.UpdateActive, params.Active = true, *req.Active
	}
	if err := deps.SharingRepo.AdminUpdateShare(r.Context(), params); err != nil {
		if errors.Is(err, sharing.ErrNotFound) {
			writeBusinessError(w, http.StatusNotFound, "分享不存在")
			return
		}
		log.Printf("管理端修改分享失败 id=%d：%v", id, err)
		writeBusinessError(w, http.StatusInternalServerError, "保存分享失败")
		return
	}
	response := map[string]any{"status": "ok"}
	// 取件码占位同步：改期/启用后需要重新占位；原码被占用则换发新码。
	if state.IsPickup {
		if newCode, rotated, ok := syncPickupCodeLive(r.Context(), deps, nil, id); ok && rotated {
			response["pickupCode"], response["pickupCodeRotated"] = newCode, true
		}
	}
	_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "share.update", "shares", rawID,
		map[string]any{"expiresInSeconds": req.ExpiresInSeconds, "active": req.Active, "unblock": req.Unblock}, net.ParseIP(clientIP(r)))
	writeJSON(w, http.StatusOK, response)
}

func adminDeleteShare(w http.ResponseWriter, r *http.Request, deps Deps, actor user.User, rawID string) {
	id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
	if err != nil || id < 1 {
		writeBusinessError(w, http.StatusBadRequest, "分享编号无效")
		return
	}
	if err := deps.SharingRepo.AdminDeleteShare(r.Context(), id); err != nil {
		if errors.Is(err, sharing.ErrNotFound) {
			writeBusinessError(w, http.StatusNotFound, "分享不存在")
			return
		}
		log.Printf("管理端删除分享失败 id=%d：%v", id, err)
		writeBusinessError(w, http.StatusInternalServerError, "删除分享失败")
		return
	}
	_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "share.delete", "shares", rawID, map[string]any{}, net.ParseIP(clientIP(r)))
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// handleAdminUploads 管理端在途上传任务管理：
//
//	GET    /api/admin/uploads          列出进行中的上传会话（占临时盘）
//	DELETE /api/admin/uploads/{id}     取消并清理该会话的分片目录
//
// 用于"随时可调、随时可用"：用户放弃的上传会占用临时盘直到过期，管理员可直接
// 清理，而不必等待 7 天过期。
func handleAdminUploads(w http.ResponseWriter, r *http.Request, deps Deps, actor user.User, path string) {
	switch {
	case path == "uploads" && r.Method == http.MethodGet:
		items, err := deps.UploadRepo.ListAdminUploads(r.Context(), 500)
		if err != nil {
			log.Printf("读取在途上传失败：%v", err)
			writeBusinessError(w, http.StatusInternalServerError, "读取在途上传失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "items": items})
	case strings.HasPrefix(path, "uploads/") && r.Method == http.MethodDelete:
		id := strings.TrimSpace(strings.TrimPrefix(path, "uploads/"))
		if id == "" {
			writeBusinessError(w, http.StatusBadRequest, "上传任务编号无效")
			return
		}
		if err := deps.UploadRepo.AdminCancelUpload(r.Context(), id); err != nil {
			if errors.Is(err, upload.ErrNotFound) {
				writeBusinessError(w, http.StatusNotFound, "上传任务不存在或已结束")
				return
			}
			log.Printf("取消上传任务失败 id=%s：%v", id, err)
			writeBusinessError(w, http.StatusInternalServerError, "取消上传任务失败")
			return
		}
		if deps.FileStore != nil {
			if err := deps.FileStore.RemoveUploadSession(r.Context(), id); err != nil {
				log.Printf("清理上传分片目录失败 id=%s：%v", id, err)
			}
		}
		_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "upload.cancel", "upload_session", id, map[string]any{}, net.ParseIP(clientIP(r)))
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	default:
		writeBusinessError(w, http.StatusNotFound, "管理接口不存在")
	}
}
