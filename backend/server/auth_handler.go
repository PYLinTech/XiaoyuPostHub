package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

// ---------- 协议结构 ----------

// loginRequest 对齐前端约定的登录请求体字段名。
//
// 前端固定发 userName / password（驼峰），不能用 username。
type loginRequest struct {
	UserName string `json:"userName"`
	Password string `json:"password"`
}

// apiStatusResponse 通用返回：status=ok | error，error 时 msg 必填。
//
// 前端只看 res.data.status === 'ok'。
type apiStatusResponse struct {
	Status string `json:"status"`
	Msg    string `json:"msg,omitempty"`
}

// userInfoResponse 用户信息返回结构（对齐前端约定字段）。
//
// 当前实现：Avatar / Job / JobName / Organization / OrganizationName / Verified
// 用固定占位值；其他字段留空。后续从用户 profile 表 / 配置读取时只改 buildUserInfoResponse。
type userInfoResponse struct {
	Name             string              `json:"name"`
	Avatar           string              `json:"avatar"`
	Email            string              `json:"email"`
	Job              string              `json:"job"`
	JobName          string              `json:"jobName"`
	Organization     string              `json:"organization"`
	OrganizationName string              `json:"organizationName"`
	Location         string              `json:"location"`
	LocationName     string              `json:"locationName"`
	Introduction     string              `json:"introduction"`
	PersonalWebsite  string              `json:"personalWebsite"`
	Verified         bool                `json:"verified"`
	PhoneNumber      string              `json:"phoneNumber"`
	AccountID        string              `json:"accountId"`
	RegistrationTime string              `json:"registrationTime"`
	Permissions      map[string][]string `json:"permissions"`
}

// ---------- POST /api/user/login ----------

// loginHandler 登录入口。
//
// 协议：
//   - Method：仅 POST；其他 → 405
//   - Body：JSON { userName, password }
//   - 成功：200 + {"status":"ok"} + Set-Cookie: xph_session=<token>
//   - 失败：200 + {"status":"error","msg":"..."}（业务错误统一用 200，
//     让前端只看 status 字段；只有"方法不允许"用 405）
//
// msg 文案严格按前端约定：用户名/密码为空、账号或密码错误等。
// "账号或密码错误"由 user.ErrInvalidCredentials 触发，覆盖
// "账号不存在 / 密码错 / 无 login 权限"所有分支，避免泄露用户存在性。
func loginHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, apiStatusResponse{
				Status: "error",
				Msg:    "method not allowed",
			})
			return
		}

		var req loginRequest
		if err := decodeJSON(w, r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, apiStatusResponse{Status: "error", Msg: "请求格式错误"})
			return
		}
		if len(req.UserName) > 64 || len(req.Password) > 1024 {
			writeJSON(w, http.StatusBadRequest, apiStatusResponse{Status: "error", Msg: "请求格式错误"})
			return
		}

		if req.UserName == "" {
			writeJSON(w, http.StatusOK, apiStatusResponse{
				Status: "error",
				Msg:    "用户名不能为空",
			})
			return
		}

		if req.Password == "" {
			writeJSON(w, http.StatusOK, apiStatusResponse{
				Status: "error",
				Msg:    "密码不能为空",
			})
			return
		}

		accountKey := "account:" + strings.ToLower(strings.TrimSpace(req.UserName))
		ipKey := "ip:" + clientIP(r)
		if retry, err := deps.SessionRepo.RetryAfter(r.Context(), accountKey, ipKey); err != nil {
			log.Printf("检查登录限制失败：%v", err)
			writeJSON(w, 500, apiStatusResponse{Status: "error", Msg: "登录失败"})
			return
		} else if retry > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
			writeJSON(w, http.StatusTooManyRequests, apiStatusResponse{Status: "error", Msg: "失败次数过多，请稍后再试"})
			return
		}

		u, err := deps.UserRepo.Authenticate(r.Context(), req.UserName, req.Password)
		if errors.Is(err, user.ErrInvalidCredentials) {
			retry, recordErr := deps.SessionRepo.RecordFailure(r.Context(), accountKey, ipKey)
			if recordErr != nil {
				log.Printf("记录登录失败次数失败：%v", recordErr)
				writeJSON(w, 500, apiStatusResponse{Status: "error", Msg: "登录失败"})
				return
			}
			if retry > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
			}
			writeJSON(w, http.StatusOK, apiStatusResponse{
				Status: "error",
				Msg:    "账号或者密码错误",
			})
			return
		}
		if err != nil {
			log.Printf("认证用户失败：%v", err)
			writeJSON(w, http.StatusInternalServerError, apiStatusResponse{
				Status: "error",
				Msg:    "登录失败",
			})
			return
		}

		if err := deps.SessionRepo.ClearFailures(r.Context(), accountKey); err != nil {
			log.Printf("清除登录失败次数失败：%v", err)
			writeJSON(w, 500, apiStatusResponse{Status: "error", Msg: "登录失败"})
			return
		}
		token, _, err := deps.SessionRepo.Create(r.Context(), u.ID)
		if err != nil {
			log.Printf("创建登录会话失败：%v", err)
			writeJSON(w, http.StatusInternalServerError, apiStatusResponse{
				Status: "error",
				Msg:    "创建登录会话失败",
			})
			return
		}

		http.SetCookie(w, newSessionCookie(token, deps.CookieSecure))

		writeJSON(w, http.StatusOK, apiStatusResponse{
			Status: "ok",
		})
	}
}

func logoutHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, 405, apiStatusResponse{Status: "error", Msg: "method not allowed"})
			return
		}
		if c, err := r.Cookie(sessionCookieName); err == nil {
			if err := deps.SessionRepo.DeleteByToken(r.Context(), c.Value); err != nil {
				log.Printf("删除登录会话失败：%v", err)
				writeJSON(w, 500, apiStatusResponse{Status: "error", Msg: "登出失败"})
				return
			}
		}
		http.SetCookie(w, expiredSessionCookie(deps.CookieSecure))
		writeJSON(w, 200, apiStatusResponse{Status: "ok"})
	}
}

func clientIP(r *http.Request) string {
	if x := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); net.ParseIP(x) != nil {
		return x
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return errors.New("content type")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("multiple json values")
	}
	return nil
}

// ---------- GET /api/user/userInfo ----------

// userInfoHandler 拿当前会话用户信息。
//
// 流程：
//  1. 仅 GET
//  2. 读 xph_session cookie
//  3. SessionRepo.GetUserIDByToken → 不存在 / 过期 → 401 + {"status":"error","msg":"未登录"}
//  4. UserRepo.GetByID(userID) → 出错 → 401（DB 异常不应泄露给前端）
//  5. 返回 buildUserInfoResponse(u)
func userInfoHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiStatusResponse{
				Status: "error",
				Msg:    "method not allowed",
			})
			return
		}

		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, apiStatusResponse{
				Status: "error",
				Msg:    "未登录",
			})
			return
		}

		userID, err := deps.SessionRepo.GetUserIDByToken(r.Context(), cookie.Value)
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusUnauthorized, apiStatusResponse{
				Status: "error",
				Msg:    "未登录",
			})
			return
		}
		if err != nil {
			log.Printf("查询登录会话失败：%v", err)
			writeJSON(w, http.StatusInternalServerError, apiStatusResponse{Status: "error", Msg: "读取用户信息失败"})
			return
		}

		u, err := deps.UserRepo.GetByID(r.Context(), userID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusUnauthorized, apiStatusResponse{Status: "error", Msg: "未登录"})
			return
		}
		if err != nil {
			log.Printf("读取会话用户失败：%v", err)
			writeJSON(w, http.StatusInternalServerError, apiStatusResponse{Status: "error", Msg: "读取用户信息失败"})
			return
		}

		writeJSON(w, http.StatusOK, buildUserInfoResponse(u))
	}
}

// ---------- 工具 ----------

// writeJSON 统一 JSON 响应：设 Content-Type + WriteHeader + Encode。
func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(v)
}

// buildUserInfoResponse 把业务层 User 装成前端期望的 userInfoResponse。
//
// 当前 Avatar / Job / JobName / Organization / OrganizationName / Introduction /
// Verified / AccountID 用固定占位值（与既有前端约定一致）。
// Permissions 由用户的后端有效权限映射生成，超管拥有全部资源。
func buildUserInfoResponse(u user.User) userInfoResponse {
	return userInfoResponse{
		Name:             u.Username,
		Avatar:           "https://lf1-xgcdn-tos.pstatp.com/obj/vcloud/vadmin/start.8e0e4855ee346a46ccff8ff3e24db27b.png",
		Email:            "",
		Job:              "admin",
		JobName:          "管理员",
		Organization:     "XiaoyuPostHub",
		OrganizationName: "XiaoyuPostHub",
		Location:         "",
		LocationName:     "",
		Introduction:     "XiaoyuPostHub 用户",
		PersonalWebsite:  "",
		Verified:         true,
		PhoneNumber:      "",
		AccountID:        "xph-user",
		RegistrationTime: "",
		Permissions:      frontendPermissions(u),
	}
}

// frontendPermissions 构造前端约定的 permissions 字典。
//
//   - 超管：每个资源 → ["*"]
//   - 普通用户：由后端有效权限映射为 read/create/update/delete/share
//
// 资源清单对齐前端约定菜单（workplace / monitor / dashboard / list / form …）。
// 后续如需精细控制（按 effective permission 输出 read/write/share/...），
// 把 actions 替换成从 u.permissionSet 派生的列表。
func frontendPermissions(u user.User) map[string][]string {
	resources := []string{
		"menu.dashboard.workplace",
		"menu.dashboard.monitor",
		"menu.visualization.dataAnalysis",
		"menu.visualization.multiDimensionDataAnalysis",
		"menu.list.searchTable",
		"menu.list.cardList",
		"menu.form.group",
		"menu.form.step",
		"menu.profile.basic",
		"menu.result.success",
		"menu.result.error",
		"menu.exception.403",
		"menu.exception.404",
		"menu.exception.500",
		"menu.user.info",
		"menu.user.setting",
	}

	out := make(map[string][]string, len(resources))
	for _, resource := range resources {
		if u.IsSuperAdmin() {
			out[resource] = []string{"*"}
		}
	}
	grant := func(action string, names ...string) {
		for _, name := range names {
			if slices.Contains(out[name], action) {
				continue
			}
			out[name] = append(out[name], action)
		}
	}
	if u.HasPermission("preview") || u.HasPermission("download") {
		grant("read", "menu.dashboard.workplace", "menu.list.searchTable", "menu.list.cardList", "menu.profile.basic")
	}
	if u.HasPermission("upload") {
		grant("create", "menu.form.group", "menu.form.step")
	}
	if u.HasPermission("rename") {
		grant("update", "menu.list.searchTable", "menu.list.cardList")
	}
	if u.HasPermission("delete_own") || u.HasPermission("delete_any") {
		grant("delete", "menu.list.searchTable", "menu.list.cardList")
	}
	if u.HasPermission("share") || u.HasPermission("direct_link") {
		grant("share", "menu.list.searchTable", "menu.list.cardList")
	}
	if u.HasPermission("manage_users") {
		grant("read", "menu.user.info", "menu.user.setting")
		grant("update", "menu.user.info", "menu.user.setting")
	}
	if u.HasPermission("read_audit_log") {
		grant("read", "menu.dashboard.monitor", "menu.visualization.dataAnalysis", "menu.visualization.multiDimensionDataAnalysis")
	}
	if u.HasPermission("manage_roles") {
		grant("read", "menu.user.setting")
		grant("update", "menu.user.setting")
	}
	return out
}
