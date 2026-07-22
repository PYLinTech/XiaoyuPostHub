package server

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
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

type registerRequest struct {
	UserName       string `json:"userName"`
	Password       string `json:"password"`
	InvitationCode string `json:"invitationCode"`
}
type totpLoginRequest struct {
	ChallengeToken string `json:"challengeToken"`
	Code           string `json:"code"`
}
type totpConfirmRequest struct {
	Secret string `json:"secret"`
	Code   string `json:"code"`
}

// apiStatusResponse 通用返回：status=ok | error，error 时 msg 必填。
//
// 前端只看 res.data.status === 'ok'。
type apiStatusResponse struct {
	Status string `json:"status"`
	Msg    string `json:"msg,omitempty"`
}

// userInfoResponse 只返回当前布局和权限路由实际使用的字段。
type userInfoResponse struct {
	ID                int64    `json:"id"`
	Name              string   `json:"name"`
	Avatar            string   `json:"avatar"`
	Permissions       []string `json:"permissions"`
	AdminPermissions  []string `json:"adminPermissions"`
	IsSuperAdmin      bool     `json:"isSuperAdmin"`
	RequiresTOTPSetup bool     `json:"requiresTOTPSetup"`
}

const defaultUserAvatar = "/assets/default-avatar.svg"

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

		accountKey := strings.ToLower(strings.TrimSpace(req.UserName))
		requestIP := clientIP(r)
		if retry, err := deps.SessionRepo.RetryAfter(r.Context(), accountKey, requestIP); err != nil {
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
			retry, recordErr := deps.SessionRepo.RecordFailure(r.Context(), accountKey, requestIP)
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
		settings, settingsErr := deps.SystemSettings.Get(r.Context())
		if settingsErr != nil {
			writeJSON(w, 500, apiStatusResponse{Status: "error", Msg: "登录失败"})
			return
		}
		forced := settings.LoginTotpEnabled && u.HasAssignedPermission(permission.RequireLoginTOTP)
		allowed := settings.LoginTotpEnabled && u.HasPermission(permission.UseLoginTOTP)
		if allowed && u.TotpSecret.Valid {
			challenge, challengeErr := deps.UserRepo.CreateTOTPChallenge(r.Context(), u.ID)
			if challengeErr != nil {
				writeJSON(w, 500, apiStatusResponse{Status: "error", Msg: "登录失败"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "totp_required", "challengeToken": challenge})
			return
		}
		showSetupWarning := false
		if forced && !u.TotpSecret.Valid {
			if u.TotpGraceUsed {
				writeJSON(w, http.StatusForbidden, apiStatusResponse{Status: "error", Msg: "账号必须先配置登录动态令牌，请联系系统管理员"})
				return
			}
			showSetupWarning = true
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
		// 会话成功创建后再消耗首次放行机会，避免瞬时会话故障把用户锁在登录页外。
		if showSetupWarning {
			if err := deps.UserRepo.MarkTOTPGraceUsed(r.Context(), u.ID); err != nil {
				writeJSON(w, 500, apiStatusResponse{Status: "error", Msg: "登录失败"})
				return
			}
		}

		http.SetCookie(w, newSessionCookie(token, deps.CookieSecure))

		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "requiresTOTPSetup": showSetupWarning})
	}
}

func totpLoginHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, 405, apiStatusResponse{Status: "error", Msg: "method not allowed"})
			return
		}
		var req totpLoginRequest
		if decodeJSON(w, r, &req) != nil {
			writeJSON(w, 400, apiStatusResponse{Status: "error", Msg: "请求格式错误"})
			return
		}
		userID, err := deps.UserRepo.ConsumeTOTPChallenge(r.Context(), req.ChallengeToken, req.Code)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, apiStatusResponse{Status: "error", Msg: "动态令牌无效或已过期"})
			return
		}
		u, err := deps.UserRepo.GetByID(r.Context(), userID)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, apiStatusResponse{Status: "error", Msg: "账号不可用"})
			return
		}
		settings, err := deps.SystemSettings.Get(r.Context())
		if err != nil || !settings.LoginTotpEnabled || !u.HasPermission(permission.UseLoginTOTP) {
			writeJSON(w, http.StatusUnauthorized, apiStatusResponse{Status: "error", Msg: "登录动态令牌策略已变更，请重新登录"})
			return
		}
		token, _, err := deps.SessionRepo.Create(r.Context(), userID)
		if err != nil {
			writeJSON(w, 500, apiStatusResponse{Status: "error", Msg: "创建登录会话失败"})
			return
		}
		http.SetCookie(w, newSessionCookie(token, deps.CookieSecure))
		writeJSON(w, 200, apiStatusResponse{Status: "ok"})
	}
}

func totpSettingsHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := requireUser(w, r, deps)
		if !ok {
			return
		}
		settings, err := deps.SystemSettings.Get(r.Context())
		if err != nil {
			writeBusinessError(w, 500, "读取动态令牌配置失败")
			return
		}
		allowed := settings.LoginTotpEnabled && u.HasPermission(permission.UseLoginTOTP)
		switch {
		case r.Method == http.MethodGet:
			writeJSON(w, 200, map[string]any{"status": "ok", "enabled": settings.LoginTotpEnabled, "allowed": allowed, "configured": u.TotpSecret.Valid, "required": u.HasAssignedPermission(permission.RequireLoginTOTP)})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/begin"):
			if !allowed {
				writeBusinessError(w, 403, "没有配置动态令牌权限")
				return
			}
			setup, err := deps.UserRepo.BeginTOTPSetup(r.Context(), u.ID, settings.SiteName, u.Username)
			if err != nil {
				writeBusinessError(w, 500, "生成动态令牌失败")
				return
			}
			writeJSON(w, 200, map[string]any{"status": "ok", "setup": setup})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/confirm"):
			if !allowed {
				writeBusinessError(w, 403, "没有配置动态令牌权限")
				return
			}
			var req totpConfirmRequest
			if decodeJSON(w, r, &req) != nil {
				writeBusinessError(w, 400, "请求格式错误")
				return
			}
			if err := deps.UserRepo.SaveTOTP(r.Context(), u.ID, req.Secret, req.Code); err != nil {
				writeBusinessError(w, 400, "动态令牌校验失败")
				return
			}
			writeJSON(w, 200, apiStatusResponse{Status: "ok"})
		default:
			writeBusinessError(w, 405, "method not allowed")
		}
	}
}

func registrationSettingsHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiStatusResponse{Status: "error", Msg: "method not allowed"})
			return
		}
		policy, err := deps.UserRepo.RegistrationPolicy(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiStatusResponse{Status: "error", Msg: "读取注册配置失败"})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok", "registrationRequiresInvitation": policy.RequiresInvitation,
			"invitationCodeLength":         policy.CodeOptions.Length,
			"invitationCodeCaseSensitive":  policy.CodeOptions.CaseSensitive,
			"invitationCodeIncludeLetters": policy.CodeOptions.IncludeLetters,
			"invitationCodeIncludeNumbers": policy.CodeOptions.IncludeNumbers,
		})
	}
}

func registerHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, apiStatusResponse{Status: "error", Msg: "method not allowed"})
			return
		}
		var req registerRequest
		if err := decodeJSON(w, r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, apiStatusResponse{Status: "error", Msg: "请求格式错误"})
			return
		}
		_, err := deps.UserRepo.Register(r.Context(), req.UserName, req.Password, req.InvitationCode)
		switch {
		case errors.Is(err, user.ErrInvitationRequired):
			writeJSON(w, http.StatusBadRequest, apiStatusResponse{Status: "error", Msg: "注册需要邀请码"})
		case errors.Is(err, user.ErrInvitationInvalid):
			writeJSON(w, http.StatusBadRequest, apiStatusResponse{Status: "error", Msg: "邀请码无效或已被使用"})
		case errors.Is(err, user.ErrUsernameUnavailable):
			writeJSON(w, http.StatusConflict, apiStatusResponse{Status: "error", Msg: "账号已存在"})
		case errors.Is(err, user.ErrRegistrationInput):
			writeJSON(w, http.StatusBadRequest, apiStatusResponse{Status: "error", Msg: "账号至少 3 个字符，密码至少 8 个字符"})
		case err != nil:
			log.Printf("注册用户失败：%v", err)
			writeJSON(w, http.StatusInternalServerError, apiStatusResponse{Status: "error", Msg: "注册失败"})
		default:
			writeJSON(w, http.StatusOK, apiStatusResponse{Status: "ok"})
		}
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
	// 反向代理通过 X-Real-IP 传递客户端地址。
	if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
		return ip.String()
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return decodeJSONBody(w, r, dst, 16<<10)
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
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, user.ErrUserDisabled) {
			http.SetCookie(w, expiredSessionCookie(deps.CookieSecure))
			writeJSON(w, http.StatusUnauthorized, apiStatusResponse{Status: "error", Msg: "未登录"})
			return
		}
		if err != nil {
			log.Printf("读取会话用户失败：%v", err)
			writeJSON(w, http.StatusInternalServerError, apiStatusResponse{Status: "error", Msg: "读取用户信息失败"})
			return
		}
		if !u.HasPermission(permission.Login) {
			http.SetCookie(w, expiredSessionCookie(deps.CookieSecure))
			writeJSON(w, http.StatusUnauthorized, apiStatusResponse{Status: "error", Msg: "未登录"})
			return
		}

		response := buildUserInfoResponse(u)
		if settings, settingsErr := deps.SystemSettings.Get(r.Context()); settingsErr == nil {
			response.RequiresTOTPSetup = settings.LoginTotpEnabled && u.HasAssignedPermission(permission.RequireLoginTOTP) && !u.TotpSecret.Valid
		}
		writeJSON(w, http.StatusOK, response)
	}
}

// ---------- 工具 ----------

// writeJSON 统一 JSON 响应：设 Content-Type + WriteHeader + Encode。
func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(v)
}

// buildUserInfoResponse 把业务层 User 装成前端实际使用的会话信息。
func buildUserInfoResponse(u user.User) userInfoResponse {
	return userInfoResponse{
		ID:               u.ID,
		Name:             u.Username,
		Avatar:           defaultUserAvatar,
		Permissions:      effectivePermissions(u),
		AdminPermissions: adminPermissions(u),
		IsSuperAdmin:     u.IsSuperAdmin(),
	}
}

func effectivePermissions(u user.User) []string {
	out := make([]string, 0, len(permission.All))
	for _, code := range permission.All {
		if u.HasPermission(code) {
			out = append(out, code)
		}
	}
	return out
}

func adminPermissions(u user.User) []string {
	out := make([]string, 0, len(permission.Admin))
	for _, code := range permission.Admin {
		if u.HasPermission(code) {
			out = append(out, code)
		}
	}
	return out
}
