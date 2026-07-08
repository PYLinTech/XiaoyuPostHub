// Package server 提供 XiaoyuPostHub 后端的 HTTP 路由与错误处理能力。
package server

import (
	"fmt"
	"net/http"

	"github.com/PYLinTech/XiaoyuPostHub/backend/group"
	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/quota"
	"github.com/PYLinTech/XiaoyuPostHub/backend/role"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

// Deps 业务层依赖集合，由 main.go 构造后注入 NewRouter。
// 后续 handler 通过 deps 拿到 user/role/permission/group/quota repo 做权限校验。
type Deps struct {
	UserRepo       *user.Repo
	RoleRepo       *role.Repo
	PermissionRepo *permission.Repo
	GroupRepo      *group.Repo
	QuotaRepo      *quota.Repo
}

// NewRouter 构造分流路由:/api/* → APIHandler,其余 → NewStaticHandler(SPA fallback on)。
// 启动期校验失败(staticDir 不存在 / 缺 index.html)返回 error,由 main.go 处理;
// 本函数不自行终止进程——构造函数边界。
func NewRouter(
	staticDir string,
	userRepo *user.Repo,
	roleRepo *role.Repo,
	permRepo *permission.Repo,
	groupRepo *group.Repo,
	quotaRepo *quota.Repo,
) (http.Handler, error) {
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
		RoleRepo:       roleRepo,
		PermissionRepo: permRepo,
		GroupRepo:      groupRepo,
		QuotaRepo:      quotaRepo,
	}))
	mux.Handle("/", staticH)
	return WithErrorPage(mux), nil
}

// APIHandler 注册后端 API。
// 当前仅挂 /api/health 用于存活探测；
// 真实业务接口（含登录链路 / 权限中间件）会在这里挂载。
func APIHandler(deps Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	_ = deps
	return mux
}
