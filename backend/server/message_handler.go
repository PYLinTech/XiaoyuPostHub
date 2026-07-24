package server

import (
	"net/http"
	"strconv"
)

type messageIDsRequest struct {
	IDs []int64 `json:"ids"`
	All bool    `json:"all"`
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
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
		if page < 1 {
			page = 1
		}
		if page > 1_000_000 {
			page = 1_000_000
		}
		if pageSize < 1 || pageSize > 50 {
			pageSize = 10
		}
		items, total, unread, err := deps.InboxRepo.List(r.Context(), u.ID, page, pageSize)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "读取消息失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":      "ok",
			"items":       items,
			"page":        page,
			"pageSize":    pageSize,
			"total":       total,
			"unreadCount": unread,
		})
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
		if err := decodeSmallJSON(w, r, &req); err != nil ||
			(!req.All && len(req.IDs) == 0) ||
			len(req.IDs) > 200 {
			writeBusinessError(w, http.StatusBadRequest, "消息编号无效")
			return
		}
		var err error
		if deleting {
			if req.All {
				err = deps.InboxRepo.DeleteAll(r.Context(), u.ID)
			} else {
				err = deps.InboxRepo.Delete(r.Context(), u.ID, req.IDs)
			}
		} else if req.All {
			err = deps.InboxRepo.MarkAllRead(r.Context(), u.ID)
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
