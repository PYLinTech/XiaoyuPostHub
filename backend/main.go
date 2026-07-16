// Package main 启动 XiaoyuPostHub 后端：监听 :8080，提供 API 与前端静态文件。
//
// 启动流程：
//  1. 加载 ENV_FILE 指定的配置文件（默认 deploy/.env）
//  2. 加载与校验配置（config.Load）
//  3. 连接 PostgreSQL（db.Open，启动期 Ping 一次）
//  4. 应用 schema（db.ApplyEmbeddedSchema，SQL 通过 go:embed 编进二进制，幂等）
//  5. 初始化 system_settings 默认行（不覆盖已有配置）
//  6. BootstrapAuthCatalog（默认配额方案和默认用户组）
//  7. BootstrapSuperAdmin（创建/同步超管账号并绑定默认用户组）
//  8. 构造 group / quota / user 等仓库
//  9. 启动 HTTP server（注入 repo）
//  10. 收到 SIGINT/SIGTERM 优雅关闭
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/admin"
	"github.com/PYLinTech/XiaoyuPostHub/backend/bootstrap"
	"github.com/PYLinTech/XiaoyuPostHub/backend/config"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/filestore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/group"
	"github.com/PYLinTech/XiaoyuPostHub/backend/inbox"
	"github.com/PYLinTech/XiaoyuPostHub/backend/quota"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
	"github.com/PYLinTech/XiaoyuPostHub/backend/server"
	"github.com/PYLinTech/XiaoyuPostHub/backend/session"
	"github.com/PYLinTech/XiaoyuPostHub/backend/sharing"
	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

func main() {
	if os.Getenv("XPH_INTERNAL_HASH_PASSWORD") == "true" {
		password, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
		if err != nil || len(password) == 0 || len(password) > 4096 {
			log.Fatal("invalid password input")
		}
		hash, err := user.HashPassword(string(password))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(hash)
		return
	}
	if action := os.Getenv("XPH_INTERNAL_DATABASE_ACTION"); action != "" {
		runInternalDatabaseAction(action)
		return
	}

	envFile := defaultEnvFile()
	cfg, err := config.Load(envFile)
	if err != nil {
		log.Fatalf("配置加载失败：%v", err)
	}
	log.Printf("配置加载成功：ENV_FILE=%s", displayEnvFile(envFile))

	config.EnvSuperAdmin = cfg.SuperAdminUsername
	config.EnvSuperAdminPasswordHash = cfg.SuperAdminPasswordHash

	bootCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	database, err := db.Open(bootCtx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		log.Fatalf("数据库初始化失败：%v", err)
	}
	log.Printf("数据库已连接：%s", db.DescribeURL(cfg.DatabaseURL))

	// 1. 应用 schema（SQL 通过 go:embed 编进二进制，部署产物不再需要 db/schema 目录）
	schemaCtx, schemaCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := db.ApplyEmbeddedSchema(schemaCtx, database.Pool()); err != nil {
		schemaCancel()
		log.Fatalf("应用 schema 失败：%v", err)
	}
	schemaCancel()

	q := sqlcgen.New(database.Pool())

	// 2. 初始化程序自身的非敏感运行期配置；已有值不会被默认值覆盖。
	settingsCtx, settingsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	settingsRepo := systemsetting.NewRepo(q)
	if err := settingsRepo.EnsureDefaults(settingsCtx); err != nil {
		settingsCancel()
		log.Fatalf("初始化系统配置失败：%v", err)
	}
	settingsCancel()
	// 3. BootstrapAuthCatalog：默认配额方案和默认用户组
	bootCtx2, bootCancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	if err := bootstrap.NewAuthCatalog(database.Pool()).Run(bootCtx2); err != nil {
		bootCancel2()
		log.Fatalf("BootstrapAuthCatalog 失败：%v", err)
	}
	bootCancel2()

	// 4. BootstrapSuperAdmin：固定绑定 default_user，确保权限和配额都有组来源
	bootCtx3, bootCancel3 := context.WithTimeout(context.Background(), 5*time.Second)
	if err := user.BootstrapSuperAdmin(bootCtx3, database.Pool()); err != nil {
		bootCancel3()
		log.Fatalf("初始化超管失败：%v", err)
	}
	bootCancel3()

	// 5. 构造 Repo
	groupRepo := group.NewRepo(q)
	quotaRepo := quota.NewRepo(q)
	userRepo := user.NewRepo(database.Pool(), q, groupRepo)
	sessionRepo := session.NewRepo(database.Pool())
	resourceRepo := resource.NewRepo(database.Pool())
	sharingRepo := sharing.NewRepo(database.Pool())
	fileStore := filestore.New(settingsRepo)
	adminRepo := admin.NewRepo(database.Pool())
	inboxRepo := inbox.NewRepo(database.Pool())
	staticPath := cfg.StaticDir

	handler, err := server.NewRouterWithDeps(staticPath, server.Deps{
		UserRepo:       userRepo,
		SessionRepo:    sessionRepo,
		GroupRepo:      groupRepo,
		QuotaRepo:      quotaRepo,
		ResourceRepo:   resourceRepo,
		SharingRepo:    sharingRepo,
		FileStore:      fileStore,
		SystemSettings: settingsRepo,
		AdminRepo:      adminRepo,
		InboxRepo:      inboxRepo,
		CookieSecure:   cfg.SessionCookieSecure,
	})
	if err != nil {
		log.Fatalf("初始化 HTTP 路由失败：%v", err)
	}

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// 文件传输由请求体大小、配额和校验约束；不使用全局短超时截断大文件。
		ReadTimeout:  0,
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	cleanupCtx, stopCleanup := context.WithCancel(context.Background())
	defer stopCleanup()
	go sessionRepo.StartCleanup(cleanupCtx)
	go sharingRepo.StartDownloadJobCleanup(cleanupCtx)
	go func() {
		log.Printf("XiaoyuPostHub 后端已启动，监听 %s，静态目录：%s", srv.Addr, staticPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		log.Printf("收到信号 %s，开始优雅关闭", sig)
	case err := <-errCh:
		log.Fatalf("服务异常：%v", err)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server 优雅关闭异常：%v", err)
	}

	database.Close()
	log.Printf("数据库连接池已关闭，后端退出")
}

func runInternalDatabaseAction(action string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	switch action {
	case "test":
		if err := db.TestInstallConnection(ctx, os.Getenv("DATABASE_URL")); err != nil {
			log.Fatal(err)
		}
	case "provision":
		passwordBytes := make([]byte, 24)
		if _, err := rand.Read(passwordBytes); err != nil {
			log.Fatal("生成数据库密码失败")
		}
		password := base64.RawURLEncoding.EncodeToString(passwordBytes)
		databaseURL, err := db.ProvisionDatabase(
			ctx,
			os.Getenv("XPH_DATABASE_ADMIN_URL"),
			"xiaoyuposthub",
			"xiaoyuposthub",
			password,
		)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(databaseURL)
	default:
		log.Fatal("不支持的数据库安装操作")
	}
}

func defaultEnvFile() string {
	if v := os.Getenv("ENV_FILE"); v != "" {
		return v
	}
	return "deploy/.env"
}

func displayEnvFile(p string) string {
	if p == "" {
		return "<none, only env vars>"
	}
	return p
}
