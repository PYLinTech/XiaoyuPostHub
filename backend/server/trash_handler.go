package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
)

func trashHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := requireUser(w, r, deps)
		if !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			items, err := deps.ResourceRepo.ListTrashOwned(r.Context(), u.ID)
			if err != nil {
				writeBusinessError(w, http.StatusInternalServerError, "读取回收站失败")
				return
			}
			settings, err := deps.SystemSettings.Get(r.Context())
			if err != nil {
				writeBusinessError(w, http.StatusInternalServerError, "读取回收期限失败")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "items": items, "retentionDays": settings.TrashRetentionDays})
		case http.MethodDelete:
			if !u.HasPermission(permission.DeleteOwn) {
				writeBusinessError(w, http.StatusForbidden, "没有清空回收站权限")
				return
			}
			// 清空回收站只标记"彻底删除"：用户不可见、不可恢复，物理文件保留
			// 供管理员审查与清理。
			if err := deps.ResourceRepo.PurgeAllTrashOwned(r.Context(), u.ID); err != nil {
				writeBusinessError(w, http.StatusInternalServerError, "清空回收站失败")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		default:
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func trashItemHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := requireUser(w, r, deps)
		if !ok {
			return
		}
		if !u.HasPermission(permission.DeleteOwn) {
			writeBusinessError(w, http.StatusForbidden, "没有管理回收站权限")
			return
		}
		parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/trash/"), "/"), "/")
		if len(parts) == 2 && parts[1] == "restore" && r.Method == http.MethodPost {
			restoredName, err := deps.ResourceRepo.RestoreOwned(r.Context(), u.ID, parts[0])
			if err != nil {
				if errors.Is(err, resource.ErrNotFound) {
					writeBusinessError(w, http.StatusNotFound, "回收站项目不存在")
					return
				}
				writeResourceMutationError(w, err)
				return
			}
			// 目标位置已有同名条目时会自动改名，把最终名字回给前端提示用户。
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "name": restoredName})
			return
		}
		if len(parts) != 1 || parts[0] == "" || r.Method != http.MethodDelete {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// "永久删除"对用户只意味着不可见、不可恢复；资源行与物理文件保留。
		err := deps.ResourceRepo.PurgeTrashedOwned(r.Context(), u.ID, parts[0])
		if errors.Is(err, resource.ErrNotFound) {
			writeBusinessError(w, http.StatusNotFound, "回收站项目不存在")
			return
		}
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "永久删除失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	}
}
