// Package main 启动 XiaoyuPostHub 后端：监听 :8080，
// 将 /api/* 反向代理到后端 API handler，其余路径反代到同级 web 目录。
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/server"
)

func main() {
	srv := &http.Server{
		Addr:              ":8080",
		Handler:           server.NewRouter(resolveStaticDir()),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		staticDir := resolveStaticDir()
		log.Printf("XiaoyuPostHub 后端已启动，监听 %s，静态目录：%s", srv.Addr, staticDir)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("优雅关闭异常：%v", err)
	}
}

// resolveStaticDir 返回二进制同级目录下的 web 路径。
// 找不到也无所谓：FileServer 返回 404，由 server.WithErrorPage 替换为内置 404 页。
func resolveStaticDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "web")
	}
	return "web"
}