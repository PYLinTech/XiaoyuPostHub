package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
	"github.com/jackc/pgx/v5"
)

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
		http.SetCookie(w, expiredSessionCookie(deps.CookieSecure))
		writeJSON(w, http.StatusUnauthorized, apiStatusResponse{Status: "error", Msg: "未登录"})
		return user.User{}, false
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiStatusResponse{Status: "error", Msg: "读取登录状态失败"})
		return user.User{}, false
	}
	if !u.HasPermission(permission.Login) {
		http.SetCookie(w, expiredSessionCookie(deps.CookieSecure))
		writeJSON(w, http.StatusUnauthorized, apiStatusResponse{Status: "error", Msg: "未登录"})
		return user.User{}, false
	}
	return u, true
}

func writeBusinessError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiStatusResponse{Status: "error", Msg: msg})
}
