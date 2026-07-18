package server

import (
	"errors"
	"log"
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
			keys, err := deps.ResourceRepo.EmptyTrashOwned(r.Context(), u.ID)
			if err != nil {
				writeBusinessError(w, http.StatusInternalServerError, "清空回收站失败")
				return
			}
			removeStorageKeys(r, deps, keys)
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
			if err := deps.ResourceRepo.RestoreOwned(r.Context(), u.ID, parts[0]); err != nil {
				if errors.Is(err, resource.ErrNotFound) {
					writeBusinessError(w, http.StatusNotFound, "回收站项目不存在")
					return
				}
				writeResourceMutationError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
			return
		}
		if len(parts) != 1 || parts[0] == "" || r.Method != http.MethodDelete {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		tree, err := deps.ResourceRepo.DeleteTrashedOwned(r.Context(), u.ID, parts[0])
		if errors.Is(err, resource.ErrNotFound) {
			writeBusinessError(w, http.StatusNotFound, "回收站项目不存在")
			return
		}
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "永久删除失败")
			return
		}
		keys := make([]string, 0)
		for _, item := range tree {
			if item.Kind == resource.KindFile && item.StorageKey != nil {
				keys = append(keys, *item.StorageKey)
			}
		}
		removeStorageKeys(r, deps, keys)
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	}
}

func removeStorageKeys(r *http.Request, deps Deps, keys []string) {
	for _, key := range keys {
		if err := deps.FileStore.Remove(r.Context(), key); err != nil {
			log.Printf("清理回收站文件失败 key=%s: %v", key, err)
		}
	}
}
