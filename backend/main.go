// Package main 启动 XiaoyuPostHub 后端：监听 :8080，
// 将 /api/* 反向代理到后端 API handler，其余路径反代到同级 web 目录。
//
// 启动流程：
//  1. 解析命令行 flag（env-file 路径）
//  2. 加载与校验配置（config.Load）
//  3. 连接 PostgreSQL（db.Open，启动期 Ping 一次）
//  4. 应用 schema（db.ApplyEmbeddedSchema，SQL 通过 go:embed 编进二进制，幂等）
//  5. BootstrapAuthCatalog（permissions / 系统 role / quota / user group）
//  6. BootstrapSuperAdmin（创建/同步超管账号，不加入 default_user group、不分配 role）
//  7. 构造 permissionRepo / roleRepo / groupRepo / quotaRepo / userRepo
//  8. 启动 HTTP server（注入 repo）
//  9. 收到 SIGINT/SIGTERM 优雅关闭
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/bootstrap"
	"github.com/PYLinTech/XiaoyuPostHub/backend/config"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/group"
	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/quota"
	"github.com/PYLinTech/XiaoyuPostHub/backend/role"
	"github.com/PYLinTech/XiaoyuPostHub/backend/server"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

func main() {
	envFile := flag.String("env-file", defaultEnvFile(),
		"运行配置 .env 文件路径，留空则仅依赖进程环境变量")
	flag.Parse()

	cfg, err := config.Load(*envFile)
	if err != nil {
		log.Fatalf("配置加载失败：%v", err)
	}
	log.Printf("配置加载成功：env-file=%s", displayEnvFile(*envFile))

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

	// 2. BootstrapAuthCatalog：permissions / 系统 role / quota / user group
	bootCtx2, bootCancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	if err := bootstrap.NewAuthCatalog(database.Pool()).Run(bootCtx2); err != nil {
		bootCancel2()
		log.Fatalf("BootstrapAuthCatalog 失败：%v", err)
	}
	bootCancel2()

	// 3. BootstrapSuperAdmin：不加入 default_user group、不分配 role
	bootCtx3, bootCancel3 := context.WithTimeout(context.Background(), 5*time.Second)
	if err := user.BootstrapSuperAdmin(bootCtx3, database.Pool()); err != nil {
		bootCancel3()
		log.Fatalf("初始化超管失败：%v", err)
	}
	bootCancel3()

	// 4. 构造 Repo
	permRepo := permission.NewRepo(q)
	roleRepo := role.NewRepo(q)
	groupRepo := group.NewRepo(q, roleRepo) // group.Repo 需要 role reader 校验 assignable
	quotaRepo := quota.NewRepo(q)
	userRepo := user.NewRepo(database.Pool(), q, roleRepo, groupRepo)

	handler, err := server.NewRouter(
		resolveStaticDir(cfg.StaticDir),
		userRepo, roleRepo, permRepo, groupRepo, quotaRepo,
	)
	if err != nil {
		log.Fatalf("初始化 HTTP 路由失败：%v", err)
	}

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		staticDir := resolveStaticDir(cfg.StaticDir)
		log.Printf("XiaoyuPostHub 后端已启动，监听 %s，静态目录：%s", srv.Addr, staticDir)
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

func defaultEnvFile() string {
	if v := os.Getenv("XPH_ENV_FILE"); v != "" {
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

func resolveStaticDir(override string) string {
	if override != "" {
		return override
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "web")
	}
	return "web"
}
