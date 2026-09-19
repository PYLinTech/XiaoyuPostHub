package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/randomtoken"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
	"github.com/jackc/pgx/v5"
)

// syncPickupCodeLive 读取当前取件码配置并同步指定分享的取件码占位状态。
//
// 返回 (新码, 是否换发, 配置与同步是否成功)。owner 为 nil 时不限定归属（管理端）。
// 编辑分享、批量启停、管理端改期三条路径共用同一实现：此前它们各写一份"读配置 →
// 构造生成选项 → 调用同步 → 记日志"，配置口径与错误处理容易互相漂移。
func syncPickupCodeLive(ctx context.Context, deps Deps, owner *int64, shareID int64) (string, bool, bool) {
	settings, err := deps.SystemSettings.Get(ctx)
	if err != nil {
		log.Printf("读取分享码配置失败（取件码同步跳过）id=%d：%v", shareID, err)
		return "", false, false
	}
	options := randomtoken.CodeOptions{
		Length: int(settings.PickupLength), CaseSensitive: settings.PickupCaseSensitive,
		IncludeLetters: settings.PickupIncludeLetters, IncludeNumbers: settings.PickupIncludeNumbers,
	}
	code, rotated, syncErr := deps.SharingRepo.SyncPickupCodeLive(ctx, owner, shareID, options)
	if syncErr != nil {
		log.Printf("同步取件码占位失败 id=%d：%v", shareID, syncErr)
		return "", false, false
	}
	return code, rotated, true
}

// quotaExempt 判断用户是否豁免配额：超级管理员不受配额限制——超管与
// default_user 共用配额，若把该组配额调小或置零会锁死超管自身的上传与分享
// 管理能力（超管仍受 hardUploadLimit 这类系统硬上限约束）。
func quotaExempt(ctx context.Context, deps Deps, userID int64) bool {
	owner, err := deps.UserRepo.GetByID(ctx, userID)
	return err == nil && owner.IsSuperAdmin()
}

func authenticatedUser(deps Deps, r *http.Request) (user.User, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return user.User{}, pgx.ErrNoRows
	}
	userID, err := deps.SessionRepo.GetUserIDByToken(r.Context(), cookie.Value)
	if err != nil {
		return user.User{}, err
	}
	return deps.UserRepo.GetByID(r.Context(), userID)
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return errors.New("content type")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("multiple json values")
	}
	return nil
}

func requireUser(w http.ResponseWriter, r *http.Request, deps Deps) (user.User, bool) {
	u, err := authenticatedUser(deps, r)
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, user.ErrUserDisabled) {
		http.SetCookie(w, expiredSessionCookie(deps.HTTPS))
		writeJSON(w, http.StatusUnauthorized, apiStatusResponse{Status: "error", Msg: "未登录"})
		return user.User{}, false
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiStatusResponse{Status: "error", Msg: "读取登录状态失败"})
		return user.User{}, false
	}
	if !u.HasPermission(permission.Login) {
		http.SetCookie(w, expiredSessionCookie(deps.HTTPS))
		writeJSON(w, http.StatusUnauthorized, apiStatusResponse{Status: "error", Msg: "未登录"})
		return user.User{}, false
	}
	return u, true
}

func writeBusinessError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiStatusResponse{Status: "error", Msg: msg})
}
