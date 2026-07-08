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
// reset 行为：DROP SCHEMA public CASCADE + CREATE SCHEMA public，
// 把项目自建的所有表清空，再按文件名顺序 apply 三个 schema 文件。
// 这样**每个测试包都从干净状态开始**，避免跨包 schema 状态污染
// （也是因为 schema 文件用纯 DDL、不带 IF EXISTS 守卫）。
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

	schemaCtx, schemaCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer schemaCancel()
	if err := db.ApplySchema(schemaCtx, pool, resolveSchemaDir()); err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: 应用 schema 失败: %v\n", err)
		os.Exit(1)
	}
}

// Teardown 关闭连接池。TestMain 退出前调用。
func Teardown() {
	if pool != nil {
		pool.Close()
	}
}

// requireDBURL 找 DATABASE_URL，顺序：进程 env → deploy/.env。
func requireDBURL() string {
	url := os.Getenv("DATABASE_URL")
	if url != "" {
		return url
	}
	url = readDeployEnv("DATABASE_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, "dbtest: 未找到 DATABASE_URL，测试 fail-fast")
		os.Exit(1)
	}
	return url
}

// readDeployEnv 简化的 .env 解析器：空行/注释/export/KEY=VALUE，不展开引号。
// 测试内不复用 config 包：那是为测试而生的形态。
// 路径从 dbtest.go 回溯三级到项目根（test/dbtest/ → test/ → backend/ → <root>）。
func readDeployEnv(key string) string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	path := filepath.Join(root, "deploy", ".env")
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

// resolveSchemaDir 从 dbtest 源码位置回溯到 backend/db/schema 的绝对路径。
// 路径：test/dbtest/ → test/ → backend/ → 项目根；schema 在 backend/db/schema/。
func resolveSchemaDir() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "db/schema"
	}
	backendDir, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	if err != nil {
		return "db/schema"
	}
	return filepath.Join(backendDir, "db", "schema")
}
