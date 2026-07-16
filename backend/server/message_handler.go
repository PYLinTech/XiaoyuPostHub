package server

import (
	"net/http"
	"strconv"
)

type messageIDsRequest struct {
	IDs []int64 `json:"ids"`
}

func messagesHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		u, ok := requireUser(w, r, deps)
		if !ok {
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items, unread, err := deps.InboxRepo.List(r.Context(), u.ID, limit)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取消息失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "items": items, "unreadCount": unread})
	}
}

func messageReadHandler(deps Deps) http.HandlerFunc {
	return messageStateHandler(deps, false)
}

func messageDeleteHandler(deps Deps) http.HandlerFunc {
	return messageStateHandler(deps, true)
}

func messageStateHandler(deps Deps, deleting bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeBusinessError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		u, ok := requireUser(w, r, deps)
		if !ok {
			return
		}
		var req messageIDsRequest
		if err := decodeSmallJSON(w, r, &req); err != nil || len(req.IDs) == 0 || len(req.IDs) > 200 {
			writeBusinessError(w, http.StatusBadRequest, "消息编号无效")
			return
		}
		var err error
		if deleting {
			err = deps.InboxRepo.Delete(r.Context(), u.ID, req.IDs)
		} else {
			err = deps.InboxRepo.MarkRead(r.Context(), u.ID, req.IDs)
		}
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "更新消息状态失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	}
}
