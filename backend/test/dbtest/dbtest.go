// Package dbtest 为需要真实 PostgreSQL 的测试提供共享连接池与 schema 应用。
//
// 使用方式（TestMain 模板）：
//
//	func TestMain(m *testing.M) {
//	    dbtest.SetupOrExit(m)
//	    code := m.Run()
//	    dbtest.Teardown()
//	    os.Exit(code)
//	}
//
// 任何 test 函数都可以通过 dbtest.Pool() / dbtest.Queries() 拿到连接。
// 缺 DATABASE_URL 时 fail-fast（不 skip），遵循项目 "DB 是基础设施" 的约定。
package dbtest

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/db"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	pool    *pgxpool.Pool
	queries *sqlcgen.Queries
)

// Pool 返回测试共享连接池。TestMain 还没跑时调用会 panic。
func Pool() *pgxpool.Pool { return pool }

// Queries 返回测试共享 sqlc Queries。
func Queries() *sqlcgen.Queries { return queries }

// SetupOrExit 在 TestMain 里调用：读 DATABASE_URL、打开连接、reset + apply schema。
// 任何一步失败直接 os.Exit(1)——缺 DB 时绝不能 skip，否则测的是空气。
//
// reset 行为：DROP SCHEMA public CASCADE + CREATE SCHEMA public，再按数字顺序
// 执行根目录 migrations 中的 SQL，使每个测试包都从干净状态开始。
func SetupOrExit(m *testing.M) {
	url := requireDBURL()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := db.Open(ctx, url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: 打开测试 DB 失败: %v\n", err)
		os.Exit(1)
	}
	pool = d.Pool()
	queries = sqlcgen.New(pool)

	resetCtx, resetCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer resetCancel()
	if _, err := pool.Exec(resetCtx, "DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;"); err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: reset public schema 失败: %v\n", err)
		os.Exit(1)
	}

	schemaCtx, schemaCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer schemaCancel()
	if err := applyTestSQL(schemaCtx); err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: 应用 schema 失败: %v\n", err)
		os.Exit(1)
	}
}

func applyTestSQL(ctx context.Context) error {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("无法定位测试目录")
	}
	dir := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "migrations")
	files, err := filepath.Glob(filepath.Join(dir, "[0-9][0-9][0-9].sql"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for _, file := range files {
		body, readErr := os.ReadFile(file)
		if readErr != nil {
			return readErr
		}
		if _, execErr := tx.Exec(ctx, string(body)); execErr != nil {
			return fmt.Errorf("执行 %s: %w", filepath.Base(file), execErr)
		}
	}
	return tx.Commit(ctx)
}

// Teardown 关闭连接池。TestMain 退出前调用。
func Teardown() {
	if pool != nil {
		pool.Close()
	}
}

// requireDBURL 只读取测试专用连接，避免污染应用配置测试。
func requireDBURL() string {
	url := os.Getenv("TEST_DATABASE_URL")
	if url != "" {
		return requireDedicatedTestDatabase(url)
	}
	url = readTestEnv("TEST_DATABASE_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, "dbtest: 未找到 TEST_DATABASE_URL，测试 fail-fast")
		os.Exit(1)
	}
	return requireDedicatedTestDatabase(url)
}

// 测试初始化会删除整个 public schema，因此数据库名必须明确带 test。
// 这道硬保护优先于便利性，防止误把应用数据库配置成测试数据库。
func requireDedicatedTestDatabase(rawURL string) string {
	parsed, err := pgxpool.ParseConfig(rawURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dbtest: TEST_DATABASE_URL 格式无效")
		os.Exit(1)
	}
	name := strings.ToLower(parsed.ConnConfig.Database)
	if !strings.Contains(name, "test") {
		fmt.Fprintf(os.Stderr, "dbtest: 拒绝清空非测试数据库 %q；数据库名必须包含 test\n", parsed.ConnConfig.Database)
		os.Exit(1)
	}
	return rawURL
}

// readTestEnv 读取 backend/.test.env。
// 测试内不复用 config 包：那是为测试而生的形态。
// 路径从 dbtest.go 回溯到 backend/.test.env。
func readTestEnv(key string) string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	backendDir := filepath.Join(filepath.Dir(thisFile), "..", "..")
	path := filepath.Join(backendDir, ".test.env")
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		if k == key {
			return strings.TrimSpace(line[eq+1:])
		}
	}
	return ""
}
