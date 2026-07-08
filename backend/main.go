// Package main 启动 XiaoyuPostHub 后端：监听 :8080，
// 将 /api/* 反向代理到后端 API handler，其余路径反代到同级 web 目录。
//
// 启动流程：
//  1. 解析命令行 flag（env-file 路径）
//  2. 加载与校验配置（config.Load）
//  3. 连接 PostgreSQL（db.Open，启动期 Ping 一次）
//  4. 启动 HTTP server（保持原有 NewRouter 单接口形态）
//  5. 收到 SIGINT/SIGTERM 优雅关闭 HTTP server 与 DB 连接池
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

	"github.com/PYLinTech/XiaoyuPostHub/backend/config"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
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

	// 把 .env 中的超管信息同步到运行时全局变量
	config.EnvSuperAdmin = cfg.SuperAdminUsername
	config.EnvSuperAdminPasswordHash = cfg.SuperAdminPasswordHash

	bootCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	database, err := db.Open(bootCtx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		log.Fatalf("数据库初始化失败：%v", err)
	}
	// 启动期 Ping 由 db.Open 完成；此处仅打印一条对运维有用的确认日志，
	// 绝不输出密码或完整连接串。
	log.Printf("数据库已连接：%s", db.DescribeURL(cfg.DatabaseURL))

	// 启动期初始化序列（顺序重要）
	// 1. 应用 schema（建表，幂等）
	schemaCtx, schemaCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := db.ApplySchema(schemaCtx, database.Pool(), "db/schema"); err != nil {
		schemaCancel()
		log.Fatalf("应用 schema 失败：%v", err)
	}
	schemaCancel()

	q := sqlcgen.New(database.Pool())

	// 2. 自愈清理：扫描库中残留的 'all'（篡改/脏数据兜底）
	healCtx, healCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := user.HealAllRole(healCtx, q); err != nil {
		healCancel()
		log.Fatalf("自愈清理失败：%v", err)
	}
	healCancel()

	// 3. 初始化超管账号（创建/覆盖/不动）
	bootCtx2, bootCancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	if err := user.BootstrapSuperAdmin(bootCtx2, q); err != nil {
		bootCancel2()
		log.Fatalf("初始化超管失败：%v", err)
	}
	bootCancel2()

	_ = user.NewRepo(q) // TODO: 接入 HTTP handler 时把这个 _ 去掉,userRepo 传给 server 包

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           server.NewRouter(resolveStaticDir(cfg.StaticDir)),
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

// defaultEnvFile 决定默认配置路径：
//   1. 优先 XPH_ENV_FILE 环境变量
//   2. 否则 deploy/.env（相对当前工作目录）
//
// 容器内一般由 compose.yaml 注入完整环境变量，env-file 设空字符串即可；
// 本地开发直接 `go run ./backend` 时会被 `deploy/.env` 兜底。
func defaultEnvFile() string {
	if v := os.Getenv("XPH_ENV_FILE"); v != "" {
		return v
	}
	return "deploy/.env"
}

// displayEnvFile 给日志用，避免打印空的 env-file 让运维迷惑。
func displayEnvFile(p string) string {
	if p == "" {
		return "<none, only env vars>"
	}
	return p
}

// resolveStaticDir 返回静态资源目录。优先级：配置项 > 二进制同级 web > 当前工作目录 web。
// 找不到也无所谓：FileServer 返回 404，由 server.WithErrorPage 替换为内置 404 页。
func resolveStaticDir(override string) string {
	if override != "" {
		return override
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "web")
	}
	return "web"
}
