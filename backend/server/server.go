// Package server 提供 XiaoyuPostHub 后端的 HTTP 路由与错误处理能力。
package server

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/PYLinTech/XiaoyuPostHub/backend/admin"
	"github.com/PYLinTech/XiaoyuPostHub/backend/blobstore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/filestore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/group"
	"github.com/PYLinTech/XiaoyuPostHub/backend/inbox"
	"github.com/PYLinTech/XiaoyuPostHub/backend/loginseal"
	"github.com/PYLinTech/XiaoyuPostHub/backend/quota"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
	"github.com/PYLinTech/XiaoyuPostHub/backend/session"
	"github.com/PYLinTech/XiaoyuPostHub/backend/sharing"
	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
	"github.com/PYLinTech/XiaoyuPostHub/backend/upload"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

//	Deps 业务层依赖集合，由 main.go 构造后注入 NewRouter。
//
// 后续 handler 通过 deps 使用用户、用户组和配额等仓库。
type Deps struct {
	UserRepo     *user.Repo
	SessionRepo  *session.Repo
	GroupRepo    *group.Repo
	QuotaRepo    *quota.Repo
	ResourceRepo *resource.Repo
	SharingRepo  *sharing.Repo
	FileStore    *filestore.Store
	// HostDiskPath 是「实时概览」统计的宿主磁盘路径（部署级配置，默认 "/"）。
	HostDiskPath   string
	SystemSettings *systemsetting.Repo
	AdminRepo      *admin.Repo
	InboxRepo      *inbox.Repo
	UploadRepo     *upload.Repo
	// Blobs 提供物理对象的读写与引用计数；交付相关接口依赖它。
	Blobs *blobstore.Service
	// Deliveries 是统一交付接口 /dl/<id> 的内存会话表，由 NewRouter 初始化。
	Deliveries *deliveryManager
	// HTTPS 声明站点是否通过 HTTPS 提供服务：为 false 时会话 Cookie 不带
	// Secure 属性（否则浏览器不会在 HTTP 下回传）。
	HTTPS bool
	// PasswordSeal 提供账号密码类请求的公钥与解密能力。生产路径必须注入，
	// 为空时相关接口返回"安全通道不可用"，不会退化为接受明文。
	PasswordSeal *loginseal.Seal
	// HSTSEnabled 控制是否下发 Strict-Transport-Security 响应头。
	HSTSEnabled bool
}

// NewRouter 构造应用路由：/api/* 由 APIHandler 处理，其余路径由静态文件服务处理。
// staticDir 不存在或缺少 index.html 时返回错误，由调用方决定如何终止启动。
func NewRouter(staticDir string, deps Deps) (http.Handler, error) {
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

	if deps.Deliveries == nil {
		deps.Deliveries = newDeliveryManager()
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", APIHandler(deps))
	// 直链即数据：/d/<token> 必须挂在外层 mux（APIHandler 只接收 /api/ 前缀），
	// 由后端直接返回文件流，可直接浏览器下载或 curl 调用；错误保持 JSON 协议。
	if deps.ResourceRepo != nil && deps.SharingRepo != nil && deps.FileStore != nil && deps.QuotaRepo != nil && deps.SystemSettings != nil {
		mux.HandleFunc("/d/", directDownloadHandler(deps))
	}
	// 统一交付接口：内存级随机地址，业务入口（直链/分享下载/临时链接）解析到它。
	if deps.Blobs != nil {
		mux.HandleFunc("/dl/", deliveryHandler(deps))
	}
	// API 必须保留结构化 JSON 错误；浏览器静态页面继续使用内置 HTML 错误页。
	mux.Handle("/", WithErrorPage(homePageHandler(deps, staticH)))
	// HSTS 覆盖全部响应（页面、API、直链），只在显式启用时下发。
	if deps.HSTSEnabled {
		return withHSTS(mux), nil
	}
	return mux, nil
}

// withHSTS 给所有响应补充 Strict-Transport-Security。
//
// 响应头只在 HTTPS 上被浏览器采纳（HTTP 响应会被忽略），因此默认启用对
// 明文部署没有副作用；一旦站点以 HTTPS 提供服务，浏览器会记住并在一年内
// 强制后续访问走 HTTPS，避免首次访问被降级劫持。
func withHSTS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		next.ServeHTTP(w, r)
	})
}

// APIHandler 注册后端 API。
//
// 路由清单：
//   - GET  /api/health              存活探测
//   - GET  /api/user/login/seal     签发登录加密公钥与一次性 nonce
//   - POST /api/user/login          登录（写 cookie，接收加密信封）
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

	// 公钥与一次性 nonce 的签发入口：登录、注册与重设密码提交前都要先取回。
	mux.HandleFunc("/api/user/login/seal", loginSealHandler(deps))
	mux.HandleFunc("/api/user/login", loginHandler(deps))
	mux.HandleFunc("/api/user/login/totp", totpLoginHandler(deps))
	mux.HandleFunc("/api/user/totp", totpSettingsHandler(deps))
	mux.HandleFunc("/api/user/totp/begin", totpSettingsHandler(deps))
	mux.HandleFunc("/api/user/totp/confirm", totpSettingsHandler(deps))
	mux.HandleFunc("/api/user/register", registerHandler(deps))
	mux.HandleFunc("/api/user/registration-settings", registrationSettingsHandler(deps))
	mux.HandleFunc("/api/user/userInfo", userInfoHandler(deps))
	mux.HandleFunc("/api/user/logout", logoutHandler(deps))
	if deps.InboxRepo != nil {
		mux.HandleFunc("/api/messages", messagesHandler(deps))
		mux.HandleFunc("/api/messages/read", messageReadHandler(deps))
		mux.HandleFunc("/api/messages/delete", messageDeleteHandler(deps))
	}

	// 业务依赖完整时开放资源、分享与直链接口。
	if deps.ResourceRepo != nil && deps.SharingRepo != nil && deps.FileStore != nil && deps.QuotaRepo != nil && deps.SystemSettings != nil {
		if deps.UploadRepo != nil {
			mux.HandleFunc("/api/uploads/config", uploadConfigHandler(deps))
			mux.HandleFunc("/api/uploads/conflicts", uploadConflictsHandler(deps))
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
		mux.HandleFunc("/api/pickups/", publicPickupHandler(deps))
		mux.HandleFunc("/api/direct-links", createDirectLinkHandler(deps))
		mux.HandleFunc("/api/direct-links/manage", directLinkBatchManageHandler(deps))
		mux.HandleFunc("/api/direct-links/manage/", directLinkManageHandler(deps))
		mux.HandleFunc("/api/share-downloads/", shareDownloadJobHandler(deps))
	}
	// Blobs 是管理接口的必需依赖（存储后端、加密开关、维护任务都会调用它），
	// 缺失时应整体不注册，而不是在请求处理中 panic。
	if deps.AdminRepo != nil && deps.SystemSettings != nil && deps.Blobs != nil {
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
