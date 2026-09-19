package server

import (
	"errors"
	"fmt"
	"html"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/admin"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

type moderationRequest struct {
	ResourceIDs []string `json:"resourceIds"`
	ShareIDs    []int64  `json:"shareIds"`
	Status      string   `json:"status"`
	Reason      string   `json:"reason"`
	Delete      bool     `json:"delete"`
	Blocked     bool     `json:"blocked"`
}

type reviewDownloadRequest struct {
	ResourceIDs []string `json:"resourceIds"`
}

type fileReviewTask struct {
	ID         string                 `json:"id"`
	OwnerName  string                 `json:"ownerName"`
	UploadedAt time.Time              `json:"uploadedAt"`
	Status     string                 `json:"status"`
	FileCount  int                    `json:"fileCount"`
	Children   []admin.FileReviewItem `json:"children"`
}

func handleAdminReviews(w http.ResponseWriter, r *http.Request, deps Deps, actor user.User, path string) {
	switch {
	case path == "reviews/files" && r.Method == http.MethodGet:
		handleFileReviewList(w, r, deps)
	case path == "reviews/files" && r.Method == http.MethodPut:
		handleFileModeration(w, r, deps, actor)
	case path == "reviews/files/download" && r.Method == http.MethodPost:
		handleReviewDownload(w, r, deps)
	case path == "reviews/files/trash" && r.Method == http.MethodGet:
		handleReviewTrashList(w, r, deps)
	case path == "reviews/files/trash" && r.Method == http.MethodDelete:
		handleReviewTrashEmpty(w, r, deps, actor)
	case strings.HasPrefix(path, "reviews/files/trash/") && r.Method == http.MethodDelete:
		handleReviewTrashDelete(w, r, deps, actor, strings.TrimPrefix(path, "reviews/files/trash/"))
	case path == "reviews/shares" && r.Method == http.MethodGet:
		handleShareReviewList(w, r, deps)
	case path == "reviews/shares" && r.Method == http.MethodPut:
		handleShareModeration(w, r, deps, actor)
	default:
		writeBusinessError(w, http.StatusNotFound, "审核接口不存在")
	}
}

func reviewPage(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	return page, size
}

func reviewScopes(r *http.Request) map[string]bool {
	out := map[string]bool{}
	for _, value := range strings.Split(r.URL.Query().Get("scopes"), ",") {
		out[strings.TrimSpace(value)] = true
	}
	if len(out) == 0 || (len(out) == 1 && out[""]) {
		return map[string]bool{"id": true, "name": true}
	}
	return out
}

func handleFileReviewList(w http.ResponseWriter, r *http.Request, deps Deps) {
	flat, err := deps.AdminRepo.ListFileReviews(r.Context())
	if err != nil {
		log.Printf("读取文件审查列表失败: %v", err)
		writeBusinessError(w, http.StatusInternalServerError, "读取文件审查列表失败")
		return
	}
	grouped := make(map[string]*fileReviewTask)
	order := make([]string, 0)
	for _, child := range flat {
		// 分组键包含归属用户：batchId 由客户端提供，若只按批次分组，恶意用户可
		// 用自己的 batchId 把文件混进他人审核卡片（归属显示也会串）。
		groupKey := fmt.Sprintf("%s\x00%d", child.TaskID, child.OwnerUserID)
		task := grouped[groupKey]
		if task == nil {
			task = &fileReviewTask{
				ID:         fmt.Sprintf("%s#%d", child.TaskID, child.OwnerUserID),
				OwnerName:  child.OwnerName,
				UploadedAt: child.SubmittedAt,
			}
			grouped[groupKey] = task
			order = append(order, groupKey)
		}
		task.Children = append(task.Children, child)
		if child.SubmittedAt.Before(task.UploadedAt) {
			task.UploadedAt = child.SubmittedAt
		}
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	scopes := reviewScopes(r)
	tasks := make([]fileReviewTask, 0, len(order))
	for _, id := range order {
		task := grouped[id]
		task.Status = aggregateFileStatus(task.Children)
		if query != "" && !fileTaskMatches(*task, query, scopes) {
			continue
		}
		task.FileCount = len(task.Children)
		if len(task.Children) > 10 {
			task.Children = task.Children[:10]
		}
		tasks = append(tasks, *task)
	}
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].UploadedAt.After(tasks[j].UploadedAt) })
	page, size := reviewPage(r)
	start := (page - 1) * size
	end := min(start+size, len(tasks))
	if start > len(tasks) {
		start, end = len(tasks), len(tasks)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "items": tasks[start:end], "total": len(tasks), "page": page, "pageSize": size})
}

func aggregateFileStatus(items []admin.FileReviewItem) string {
	status := "normal"
	for _, item := range items {
		switch {
		case !item.Exists:
			return "deleted"
		case item.Blocked:
			status = "blocked"
		case item.PurgedAt != nil && status != "blocked":
			// 用户已彻底删除：资源仍在（管理员可见、可处置），但用户侧不可恢复。
			status = "purged"
		case item.TrashedAt != nil && status != "blocked" && status != "purged":
			status = "trashed"
		case item.Status == "pending" && status == "normal":
			status = "pending"
		case item.Status == "rejected" && status == "normal":
			status = "rejected"
		}
	}
	return status
}

func fileTaskMatches(task fileReviewTask, query string, scopes map[string]bool) bool {
	if scopes["id"] && strings.Contains(strings.ToLower(task.ID), query) {
		return true
	}
	if scopes["username"] && strings.Contains(strings.ToLower(task.OwnerName), query) {
		return true
	}
	if scopes["name"] {
		for _, child := range task.Children {
			if strings.Contains(strings.ToLower(child.Name), query) {
				return true
			}
		}
	}
	return false
}

func handleShareReviewList(w http.ResponseWriter, r *http.Request, deps Deps) {
	items, err := deps.AdminRepo.ListShareReviews(r.Context())
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取分享审查列表失败")
		return
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	scopes := reviewScopes(r)
	filtered := items[:0]
	for _, item := range items {
		if query == "" ||
			(scopes["id"] && strings.Contains(strconv.FormatInt(item.ShareID, 10), query)) ||
			(scopes["name"] && strings.Contains(strings.ToLower(item.ResourceName), query)) ||
			(scopes["username"] && strings.Contains(strings.ToLower(item.OwnerName), query)) {
			filtered = append(filtered, item)
		}
	}
	page, size := reviewPage(r)
	start := (page - 1) * size
	end := min(start+size, len(filtered))
	if start > len(filtered) {
		start, end = len(filtered), len(filtered)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "items": filtered[start:end], "total": len(filtered), "page": page, "pageSize": size})
}

func decodeModeration(w http.ResponseWriter, r *http.Request) (moderationRequest, bool) {
	var req moderationRequest
	if err := decodeSmallJSON(w, r, &req); err != nil {
		writeBusinessError(w, http.StatusBadRequest, "请求格式错误")
		return req, false
	}
	req.Status = strings.ToLower(strings.TrimSpace(req.Status))
	req.Reason = strings.TrimSpace(req.Reason)
	if (req.Status != "approved" && req.Status != "rejected") || len([]rune(req.Reason)) > 100 || (req.Status == "rejected" && req.Reason == "") {
		writeBusinessError(w, http.StatusBadRequest, "审核状态或审核意见无效")
		return req, false
	}
	if req.Status == "approved" {
		req.Delete, req.Blocked = false, false
	}
	return req, true
}

func handleFileModeration(w http.ResponseWriter, r *http.Request, deps Deps, actor user.User) {
	req, ok := decodeModeration(w, r)
	if !ok || len(req.ResourceIDs) == 0 || len(req.ResourceIDs) > 500 {
		if ok {
			writeBusinessError(w, http.StatusBadRequest, "请选择待审核文件")
		}
		return
	}
	warnings := make([]string, 0)
	failures := make([]map[string]any, 0)
	seen := map[string]bool{}
	for _, id := range req.ResourceIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		item, err := deps.AdminRepo.GetFileReviewItem(r.Context(), id)
		if err != nil {
			// 逐条返回失败而不是中断整批：避免"部分生效、前端只看到整体失败"。
			failures = append(failures, map[string]any{"resourceId": id, "error": "读取审核项失败"})
			continue
		}
		if item.Exists {
			if _, err := deps.ResourceRepo.SetAdminDisposition(r.Context(), id, req.Delete, req.Blocked); err != nil {
				failures = append(failures, map[string]any{"resourceId": id, "name": item.Name, "error": "文件处置失败，可能存在同名恢复冲突"})
				continue
			}
		} else if !req.Delete {
			warnings = append(warnings, item.Name+"：文件已超过回收期限并永久删除")
		}
		if err := deps.AdminRepo.ReviewFile(r.Context(), item, req.Status, req.Reason, req.Delete, req.Blocked, actor.ID); err != nil {
			failures = append(failures, map[string]any{"resourceId": id, "name": item.Name, "error": "保存文件审核失败"})
			continue
		}
		notifyFileReview(r, deps, item, req)
	}
	_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "file_review.submit", "resources", strings.Join(req.ResourceIDs, ","), map[string]any{"status": req.Status, "reason": req.Reason, "delete": req.Delete, "blocked": req.Blocked}, net.ParseIP(clientIP(r)))
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "warnings": warnings, "failures": failures})
}

func notifyFileReview(r *http.Request, deps Deps, item admin.FileReviewItem, req moderationRequest) {
	if deps.InboxRepo == nil || item.OwnerUserID == 0 {
		return
	}
	body := fmt.Sprintf("您的文件“%s”已通过审核。", item.Name)
	if req.Status == "rejected" {
		body = fmt.Sprintf("因为%s，您的文件“%s”未通过审核。", req.Reason, item.Name)
		if req.Delete {
			body += "已将该文件删除并移入受限回收站。"
		}
		if req.Blocked {
			body += "该文件已被拉黑，无法继续使用。"
		}
	}
	content := "<p>" + html.EscapeString(body) + "</p>"
	_, _ = deps.InboxRepo.InsertUser(r.Context(), item.OwnerUserID, "文件审核结果", content, "审核结果")
}

func handleShareModeration(w http.ResponseWriter, r *http.Request, deps Deps, actor user.User) {
	req, ok := decodeModeration(w, r)
	if !ok || len(req.ShareIDs) == 0 || len(req.ShareIDs) > 500 {
		if ok {
			writeBusinessError(w, http.StatusBadRequest, "请选择待审核分享")
		}
		return
	}
	failures := make([]map[string]any, 0)
	for _, id := range req.ShareIDs {
		item, err := deps.AdminRepo.GetShareReviewItem(r.Context(), id)
		if err != nil {
			failures = append(failures, map[string]any{"shareId": id, "error": "读取审核项失败"})
			continue
		}
		if err := deps.AdminRepo.SetShareDisposition(r.Context(), id, req.Delete, req.Blocked); err != nil {
			failures = append(failures, map[string]any{"shareId": id, "error": "保存分享处置失败"})
			continue
		}
		if err := deps.AdminRepo.ReviewShare(r.Context(), id, req.Status, req.Reason, req.Delete, req.Blocked, actor.ID); err != nil {
			failures = append(failures, map[string]any{"shareId": id, "error": "保存分享审核失败"})
			continue
		}
		notifyShareReview(r, deps, item, req)
	}
	_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "share_review.submit", "shares", fmt.Sprint(req.ShareIDs), map[string]any{"status": req.Status, "reason": req.Reason, "delete": req.Delete, "blocked": req.Blocked}, net.ParseIP(clientIP(r)))
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "failures": failures})
}

func notifyShareReview(r *http.Request, deps Deps, item admin.ShareReviewItem, req moderationRequest) {
	if deps.InboxRepo == nil || item.OwnerUserID == 0 {
		return
	}
	body := fmt.Sprintf("您的分享 #%d 已通过审核。", item.ShareID)
	if req.Status == "rejected" {
		body = fmt.Sprintf("因为%s，您的分享 #%d 未通过审核。", req.Reason, item.ShareID)
		if req.Delete {
			body += "已将该分享链接删除。"
		}
		if req.Blocked {
			body += "该分享已被管理员封禁。"
		}
	}
	content := "<p>" + html.EscapeString(body) + "</p>"
	_, _ = deps.InboxRepo.InsertUser(r.Context(), item.OwnerUserID, "分享审核结果", content, "审核结果")
}

func handleReviewDownload(w http.ResponseWriter, r *http.Request, deps Deps) {
	var req reviewDownloadRequest
	if err := decodeSmallJSON(w, r, &req); err != nil || len(req.ResourceIDs) == 0 || len(req.ResourceIDs) > 500 {
		writeBusinessError(w, http.StatusBadRequest, "请选择待下载文件")
		return
	}
	items := make([]admin.FileReviewItem, 0, len(req.ResourceIDs))
	resources := make([]resource.Resource, 0, len(req.ResourceIDs))
	for _, id := range req.ResourceIDs {
		meta, err := deps.AdminRepo.GetFileReviewItem(r.Context(), id)
		if err != nil || !meta.Exists {
			continue
		}
		item, err := deps.ResourceRepo.GetByIDIncludingTrash(r.Context(), id)
		if err != nil {
			continue
		}
		items, resources = append(items, meta), append(resources, item)
	}
	if len(resources) == 0 {
		writeBusinessError(w, http.StatusNotFound, "所选文件已不存在")
		return
	}
	if len(resources) == 1 {
		// 管理员审核场景始终由服务器解密输出明文（与交付开关无关）。
		serveOwnedFileDecrypted(w, r, deps, resources[0])
		return
	}
	root := resource.Resource{ID: fmt.Sprintf("review-%d", time.Now().UnixNano()), Kind: resource.KindFolder, Name: "审核文件", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	tree := []resource.TreeEntry{{Resource: root, RelativePath: root.Name}}
	for index, item := range resources {
		meta := items[index]
		entry := resource.TreeEntry{Resource: item, RelativePath: fmt.Sprintf("%s/%s/%s", archiveSegment(meta.OwnerName), archiveSegment(meta.TaskID), item.Name)}
		tree = append(tree, entry)
	}
	path, size, err := buildZip(r.Context(), deps, tree)
	if path != "" {
		defer os.Remove(path) //nolint:errcheck
	}
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "打包审核文件失败")
		return
	}
	serveLocalArtifact(w, r, path, size, "审核文件.zip", "application/zip")
}

func archiveSegment(value string) string {
	value = strings.TrimSpace(strings.NewReplacer("/", "_", "\\", "_").Replace(value))
	if value == "" || value == "." || value == ".." {
		return "unknown"
	}
	return value
}

func handleReviewTrashList(w http.ResponseWriter, r *http.Request, deps Deps) {
	items, err := deps.AdminRepo.ListFileReviews(r.Context())
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取审核回收站失败")
		return
	}
	out := make([]admin.FileReviewItem, 0)
	for _, item := range items {
		if item.Exists && item.TrashedAt != nil && item.DeleteFile {
			out = append(out, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "items": out})
}

func handleReviewTrashDelete(w http.ResponseWriter, r *http.Request, deps Deps, actor user.User, id string) {
	item, err := deps.AdminRepo.GetFileReviewItem(r.Context(), id)
	if err != nil || !item.Exists || item.TrashedAt == nil || !item.DeleteFile {
		writeBusinessError(w, http.StatusNotFound, "审核回收站文件不存在")
		return
	}
	// 删除资源行并降对象引用（物理文件保留，由清理工具处理）。
	if _, err := deps.ResourceRepo.DeleteAdminTrashedFile(r.Context(), id); err != nil {
		if errors.Is(err, resource.ErrNotFound) {
			writeBusinessError(w, http.StatusNotFound, "审核回收站文件不存在")
			return
		}
		log.Printf("永久删除受限文件失败 id=%s：%v", id, err)
		writeBusinessError(w, http.StatusInternalServerError, "永久删除失败")
		return
	}
	// 这是对"受限文件"的最终处置（释放对象引用），属敏感操作，必须入库审计。
	_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "file.review_trash.purge", "resource", id,
		map[string]any{"name": item.Name, "ownerUserId": item.OwnerUserID, "ownerName": item.OwnerName, "sizeBytes": item.SizeBytes},
		net.ParseIP(clientIP(r)))
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func handleReviewTrashEmpty(w http.ResponseWriter, r *http.Request, deps Deps, actor user.User) {
	items, err := deps.AdminRepo.ListFileReviews(r.Context())
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "读取审核回收站失败")
		return
	}
	purged := 0
	for _, item := range items {
		if item.Exists && item.TrashedAt != nil && item.DeleteFile {
			if _, purgeErr := deps.ResourceRepo.DeleteAdminTrashedFile(r.Context(), item.ResourceID); purgeErr != nil {
				log.Printf("清空审核回收站失败 id=%s：%v", item.ResourceID, purgeErr)
				continue
			}
			purged++
		}
	}
	if purged > 0 {
		_ = deps.AdminRepo.WriteAudit(r.Context(), actor.ID, actor.Username, "file.review_trash.empty", "resource", "",
			map[string]any{"purged": purged}, net.ParseIP(clientIP(r)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "purged": purged})
}
