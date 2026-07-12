package server

import (
	"net/http"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/session"
)

// session cookie 配置：
//   - 名称固定为 xph_session
//   - HttpOnly：JS 无法读取，避免被 XSS 偷
//   - SameSite=Lax：跨站 GET 允许（普通导航），跨站 POST 拦截
//   - Secure：由 SESSION_COOKIE_SECURE 控制，默认启用
//   - 不设置 Domain：默认当前 host，避免跨子站携带
//   - 不设置 Expires（MaxAge 已足够浏览器持久化）
//
// 不参与签名 / 不参与加密——token 本身就是 256 bit 不可枚举随机数。
const sessionCookieName = "xph_session"

// newSessionCookie 构造登录成功的 Set-Cookie 值。
//
// 调用方在 loginHandler 里 `http.SetCookie(w, newSessionCookie(token))`。
func newSessionCookie(token string, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(session.TTL.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

func expiredSessionCookie(secure bool) *http.Cookie {
	c := newSessionCookie("", secure)
	c.MaxAge = -1
	c.Expires = time.Unix(1, 0)
	return c
}
