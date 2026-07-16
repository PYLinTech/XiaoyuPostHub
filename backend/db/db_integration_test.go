//go:build integration

package db

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// requireDB 返回测试专用 PostgreSQL 连接 URL。integration 测试被显式启用后，
// 缺少配置或数据库不可达都必须失败，不能静默跳过。
func requireDB(t *testing.T) string {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		url = readTestEnv(t, "TEST_DATABASE_URL")
	}
	if url == "" {
		t.Fatal("未找到 TEST_DATABASE_URL：请设置环境变量或 backend/.test.env")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := pingURL(ctx, url); err != nil {
		t.Fatalf("TEST_DATABASE_URL 不可达：%v", err)
	}
	return url
}

func readTestEnv(t *testing.T, key string) string {
	t.Helper()
	f, err := os.Open(filepath.Join("..", ".test.env"))
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		keyValue := strings.SplitN(line, "=", 2)
		if len(keyValue) == 2 && strings.TrimSpace(keyValue[0]) == key {
			return strings.TrimSpace(keyValue[1])
		}
	}
	return ""
}

func TestOpen_SuccessPing(t *testing.T) {
	url := requireDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	database, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	if database.Pool() == nil {
		t.Fatal("Pool() 返回 nil")
	}
	if err := database.Ping(ctx); err != nil {
		t.Errorf("Ping after Open: %v", err)
	}
}

func TestDB_CloseThenPingFails(t *testing.T) {
	url := requireDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	database, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	database.Close()

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer pingCancel()
	if err := database.Ping(pingCtx); err == nil {
		t.Error("Ping after Close should fail")
	}
}

func pingURL(ctx context.Context, databaseURL string) error {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return err
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	return pool.Ping(ctx)
}
