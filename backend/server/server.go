// Package server 提供 XiaoyuPostHub 后端的 HTTP 路由与错误处理能力。
package server

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/PYLinTech/XiaoyuPostHub/backend/admin"
	"github.com/PYLinTech/XiaoyuPostHub/backend/filestore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/group"
	"github.com/PYLinTech/XiaoyuPostHub/backend/inbox"
	"github.com/PYLinTech/XiaoyuPostHub/backend/quota"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
	"github.com/PYLinTech/XiaoyuPostHub/backend/session"
	"github.com/PYLinTech/XiaoyuPostHub/backend/sharing"
	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
	"github.com/PYLinTech/XiaoyuPostHub/backend/upload"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

// Deps 业务层依赖集合，由 main.go 构造后注入 NewRouter。
// 后续 handler 通过 deps 使用用户、用户组和配额等仓库。
type Deps struct {
	UserRepo       *user.Repo
	SessionRepo    *session.Repo
	GroupRepo      *group.Repo
	QuotaRepo      *quota.Repo
	ResourceRepo   *resource.Repo
	SharingRepo    *sharing.Repo
	FileStore      *filestore.Store
	SystemSettings *systemsetting.Repo
	AdminRepo      *admin.Repo
	InboxRepo      *inbox.Repo
	UploadRepo     *upload.Repo
	CookieSecure   bool
}

// NewRouter 构造分流路由:/api/* → APIHandler,其余 → NewStaticHandler(SPA fallback on)。
// 启动期校验失败(staticDir 不存在 / 缺 index.html)返回 error,由 main.go 处理;
// 本函数不自行终止进程——构造函数边界。
//
// 参数顺序：staticDir, userRepo, sessionRepo, groupRepo, quotaRepo。
// sessionRepo 用于登录、登出、会话校验和登录限流。
func NewRouter(
	staticDir string,
	userRepo *user.Repo,
	sessionRepo *session.Repo,
	groupRepo *group.Repo,
	quotaRepo *quota.Repo,
	cookieSecure bool,
) (http.Handler, error) {
	return NewRouterWithDeps(staticDir, Deps{
		UserRepo:     userRepo,
		SessionRepo:  sessionRepo,
		GroupRepo:    groupRepo,
		QuotaRepo:    quotaRepo,
		CookieSecure: cookieSecure,
	})
}

// NewRouterWithDeps 构造完整业务路由。保留 NewRouter 作为认证层测试和旧调用方的兼容入口。
func NewRouterWithDeps(staticDir string, deps Deps) (http.Handler, error) {
	if deps.UserRepo == nil || deps.SessionRepo == nil {
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
	mux.Handle("/api/", APIHandler(deps))
	// API 必须保留结构化 JSON 错误；浏览器静态页面继续使用内置 HTML 错误页。
	mux.Handle("/", WithErrorPage(staticH))
	return mux, nil
}

// APIHandler 注册后端 API。
//
// 路由清单：
//   - GET  /api/health              存活探测
//   - POST /api/user/login          登录（写 cookie）
//   - GET  /api/user/userInfo       当前会话用户信息（读 cookie）
//   - POST /api/user/logout         登出（删除会话并清除 cookie）
func APIHandler(deps Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	if deps.SystemSettings != nil {
		mux.HandleFunc("/api/site-config", siteConfigHandler(deps))
		mux.HandleFunc("/api/site-icon", siteIconHandler(deps))
	}

	mux.HandleFunc("/api/user/login", loginHandler(deps))
	mux.HandleFunc("/api/user/register", registerHandler(deps))
	mux.HandleFunc("/api/user/registration-settings", registrationSettingsHandler(deps))
	mux.HandleFunc("/api/user/userInfo", userInfoHandler(deps))
	mux.HandleFunc("/api/user/logout", logoutHandler(deps))
	if deps.InboxRepo != nil {
		mux.HandleFunc("/api/messages", messagesHandler(deps))
		mux.HandleFunc("/api/messages/read", messageReadHandler(deps))
		mux.HandleFunc("/api/messages/delete", messageDeleteHandler(deps))
	}

	// 业务依赖完整时开放资源、分享与直链接口；认证层的独立测试可只注入用户和会话仓库。
	if deps.ResourceRepo != nil && deps.SharingRepo != nil && deps.FileStore != nil && deps.QuotaRepo != nil && deps.SystemSettings != nil {
		if deps.UploadRepo != nil {
			mux.HandleFunc("/api/uploads/config", uploadConfigHandler(deps))
			mux.HandleFunc("/api/uploads", uploadSessionsHandler(deps))
			mux.HandleFunc("/api/uploads/", uploadSessionItemHandler(deps))
		}
		mux.HandleFunc("/api/resources/folders", folderHandler(deps))
		mux.HandleFunc("/api/resources", resourceListHandler(deps))
		mux.HandleFunc("/api/resources/", resourceItemHandler(deps))
		mux.HandleFunc("/api/trash", trashHandler(deps))
		mux.HandleFunc("/api/trash/", trashItemHandler(deps))
		mux.HandleFunc("/api/shares", createShareHandler(deps))
		mux.HandleFunc("/api/shares/manage", shareBatchManageHandler(deps))
		mux.HandleFunc("/api/shares/manage/", shareManageHandler(deps))
		mux.HandleFunc("/api/shares/", publicShareHandler(deps))
		mux.HandleFunc("/api/direct-links", createDirectLinkHandler(deps))
		mux.HandleFunc("/api/direct-links/manage", directLinkBatchManageHandler(deps))
		mux.HandleFunc("/api/direct-links/manage/", directLinkManageHandler(deps))
		mux.HandleFunc("/api/direct/", directDownloadHandler(deps))
		mux.HandleFunc("/api/share-downloads/", shareDownloadJobHandler(deps))
	}
	if deps.AdminRepo != nil && deps.SystemSettings != nil {
		mux.HandleFunc("/api/admin/", adminHandler(deps))
	}

	// 未注册接口也保持统一 JSON 协议，避免前端收到 HTML 后无法读取错误信息。
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeBusinessError(w, http.StatusNotFound, "接口不存在")
	})

	return protectAPI(mux)
}

func protectAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete {
			if rawOrigin := r.Header.Get("Origin"); rawOrigin != "" {
				origin, err := url.Parse(rawOrigin)
				if err != nil || origin.Host != r.Host {
					writeBusinessError(w, http.StatusForbidden, "请求来源无效")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
