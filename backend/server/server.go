// Package server 提供 XiaoyuPostHub 后端的 HTTP 路由与错误处理能力。
package server

import (
	"fmt"
	"net/http"

	"github.com/PYLinTech/XiaoyuPostHub/backend/group"
	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/quota"
	"github.com/PYLinTech/XiaoyuPostHub/backend/role"
	"github.com/PYLinTech/XiaoyuPostHub/backend/session"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

// Deps 业务层依赖集合，由 main.go 构造后注入 NewRouter。
// 后续 handler 通过 deps 拿到 user/session/role/permission/group/quota repo 做权限校验。
type Deps struct {
	UserRepo       *user.Repo
	SessionRepo    *session.Repo
	RoleRepo       *role.Repo
	PermissionRepo *permission.Repo
	GroupRepo      *group.Repo
	QuotaRepo      *quota.Repo
	CookieSecure   bool
}

// NewRouter 构造分流路由:/api/* → APIHandler,其余 → NewStaticHandler(SPA fallback on)。
// 启动期校验失败(staticDir 不存在 / 缺 index.html)返回 error,由 main.go 处理;
// 本函数不自行终止进程——构造函数边界。
//
// 参数顺序：staticDir, userRepo, sessionRepo, roleRepo, permRepo, groupRepo, quotaRepo。
// sessionRepo 用于登录、登出、会话校验和登录限流。
func NewRouter(
	staticDir string,
	userRepo *user.Repo,
	sessionRepo *session.Repo,
	roleRepo *role.Repo,
	permRepo *permission.Repo,
	groupRepo *group.Repo,
	quotaRepo *quota.Repo,
	cookieSecure bool,
) (http.Handler, error) {
	if userRepo == nil || sessionRepo == nil {
		return nil, fmt.Errorf("初始化 API 失败：userRepo 和 sessionRepo 必须提供")
	}
	staticH, err := NewStaticHandler(StaticConfig{
		Dir:         staticDir,
		SPAFallback: true,
	})
	if err != nil {
		return nil, fmt.Errorf("初始化静态文件服务失败：%w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", APIHandler(Deps{
		UserRepo:       userRepo,
		SessionRepo:    sessionRepo,
		RoleRepo:       roleRepo,
		PermissionRepo: permRepo,
		GroupRepo:      groupRepo,
		QuotaRepo:      quotaRepo,
		CookieSecure:   cookieSecure,
	}))
	mux.Handle("/", staticH)
	return WithErrorPage(mux), nil
}

// APIHandler 注册后端 API。
//
// 路由清单：
//   - GET  /api/health              存活探测
//   - POST /api/user/login          登录（写 cookie）
//   - GET  /api/user/userInfo       当前会话用户信息（读 cookie）
//   - POST /api/user/logout         登出（删除会话并清除 cookie）
//
// 不再注册其他业务接口；后续按需追加。
func APIHandler(deps Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("/api/user/login", loginHandler(deps))
	mux.HandleFunc("/api/user/userInfo", userInfoHandler(deps))
	mux.HandleFunc("/api/user/logout", logoutHandler(deps))

	return mux
}
