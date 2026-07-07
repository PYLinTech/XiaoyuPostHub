package user_test

// 集成测试:BootstrapSuperAdmin 三种情况。
// 需要真 PG;缺 DB fail-fast。
// TestMain 在本文件定义,repo_test.go 共享 testPool。

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/config"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool 是包内共享的连接池,所有 user_test 测试都用它。
var testPool *pgxpool.Pool

// TestMain 在所有测试开始前建一次池,结束关掉。
// DATABASE_URL 查找顺序:进程 env → deploy/.env,缺一就 fail-fast。
func TestMain(m *testing.M) {
	url := requireDBURL()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	d, err := db.Open(ctx, url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "user_test: 打开测试 DB 失败: %v\n", err)
		os.Exit(1)
	}
	testPool = d.Pool()

	// 应用 schema,确保 users 表存在(schema 用 CREATE TABLE IF NOT EXISTS 幂等)。
	// 路径用绝对路径:从本测试文件位置回溯到 backend/ 根,避免 cwd 依赖。
	schemaCtx, schemaCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer schemaCancel()
	if err := db.ApplySchema(schemaCtx, testPool, resolveSchemaDir()); err != nil {
		fmt.Fprintf(os.Stderr, "user_test: 应用 schema 失败: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	d.Close()
	os.Exit(code)
}

// resolveSchemaDir 从本测试源码位置回溯到 backend/db/schema 的绝对路径,
// 不依赖 go test 的 cwd。注意:user_test 在 backend/user/ 下,
// 回溯 1 级到 module root (backend/),不是项目根。
func resolveSchemaDir() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "db/schema" // 兜底
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), ".."))
	if err != nil {
		return "db/schema"
	}
	return filepath.Join(root, "db", "schema")
}

func requireDBURL() string {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = readDeployEnv()
	}
	if url == "" {
		fmt.Fprintln(os.Stderr, "user_test: 未找到 DATABASE_URL,测试 fail-fast")
		os.Exit(1)
	}
	return url
}

// readDeployEnv 从项目根 deploy/.env 读 DATABASE_URL。
// 简化版解析器(空行/注释/export/KEY=VALUE),不展开引号转义,
// 因为现有 .env 不会在 DATABASE_URL 上用引号。
func readDeployEnv() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
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
		if k == "DATABASE_URL" {
			return strings.TrimSpace(line[eq+1:])
		}
	}
	return ""
}

// setEnvSuperAdmin 在测试期间覆盖全局,测完恢复。
func setEnvSuperAdmin(t *testing.T, name, hash string) {
	t.Helper()
	origName := config.EnvSuperAdmin
	origHash := config.EnvSuperAdminPasswordHash
	config.EnvSuperAdmin = name
	config.EnvSuperAdminPasswordHash = hash
	t.Cleanup(func() {
		config.EnvSuperAdmin = origName
		config.EnvSuperAdminPasswordHash = origHash
	})
}

// uniqueUsername 用 crypto/rand 生成 8 字节后缀,避免测试间用户名冲突。
func uniqueUsername(t *testing.T, prefix string) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b))
}

// cleanupUser 测试结束删掉这个 username,保持库干净。
func cleanupUser(t *testing.T, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := testPool.Exec(ctx, "DELETE FROM users WHERE username = $1", name); err != nil {
		t.Logf("warning: cleanup user %q: %v", name, err)
	}
}

// insertUser 先清理再插入,保证测试起点干净。
func insertUser(t *testing.T, name, hash string, groups []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = testPool.Exec(ctx, "DELETE FROM users WHERE username = $1", name)
	_, err := testPool.Exec(ctx,
		"INSERT INTO users (username, password_hash, groups) VALUES ($1, $2, $3)",
		name, hash, groups)
	if err != nil {
		t.Fatalf("INSERT user %q: %v", name, err)
	}
}

func contains(s []string, item string) bool {
	for _, x := range s {
		if x == item {
			return true
		}
	}
	return false
}

// --- BootstrapSuperAdmin:不存在 → 创建 ---

func TestBootstrapSuperAdmin_CreateWhenMissing(t *testing.T) {
	name := uniqueUsername(t, "create_when_missing")
	setEnvSuperAdmin(t, name, "sha256:salt:hash_create")
	t.Cleanup(func() { cleanupUser(t, name) })

	q := sqlcgen.New(testPool)
	if err := user.BootstrapSuperAdmin(context.Background(), q); err != nil {
		t.Fatalf("BootstrapSuperAdmin: %v", err)
	}

	got, err := q.GetUserByUsername(context.Background(), name)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got.PasswordHash != "sha256:salt:hash_create" {
		t.Errorf("PasswordHash = %q, want sha256:salt:hash_create", got.PasswordHash)
	}
	if !contains(got.Groups, "user") {
		t.Errorf("Groups = %v, want contains 'user'", got.Groups)
	}
	if contains(got.Groups, "all") {
		t.Errorf("Groups = %v, must NOT contain 'all'", got.Groups)
	}
}

// --- BootstrapSuperAdmin:存在 + hash 不一致 → 覆盖 ---

func TestBootstrapSuperAdmin_OverwriteHashWhenMismatch(t *testing.T) {
	name := uniqueUsername(t, "overwrite_hash")
	insertUser(t, name, "sha256:old_salt:old_hash", []string{"user"})
	t.Cleanup(func() { cleanupUser(t, name) })
	setEnvSuperAdmin(t, name, "sha256:new_salt:new_hash")

	q := sqlcgen.New(testPool)
	if err := user.BootstrapSuperAdmin(context.Background(), q); err != nil {
		t.Fatalf("BootstrapSuperAdmin: %v", err)
	}

	got, err := q.GetUserByUsername(context.Background(), name)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got.PasswordHash != "sha256:new_salt:new_hash" {
		t.Errorf("PasswordHash = %q, want sha256:new_salt:new_hash", got.PasswordHash)
	}
}

// --- BootstrapSuperAdmin:存在 + hash 一致 → 不动 ---

func TestBootstrapSuperAdmin_NoOpWhenMatch(t *testing.T) {
	name := uniqueUsername(t, "noop_when_match")
	insertUser(t, name, "sha256:same_salt:same_hash", []string{"user"})
	t.Cleanup(func() { cleanupUser(t, name) })
	setEnvSuperAdmin(t, name, "sha256:same_salt:same_hash")

	q := sqlcgen.New(testPool)
	if err := user.BootstrapSuperAdmin(context.Background(), q); err != nil {
		t.Fatalf("BootstrapSuperAdmin: %v", err)
	}

	got, err := q.GetUserByUsername(context.Background(), name)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got.PasswordHash != "sha256:same_salt:same_hash" {
		t.Errorf("PasswordHash = %q, want unchanged", got.PasswordHash)
	}
	if !contains(got.Groups, "user") {
		t.Errorf("Groups = %v, want contains 'user'", got.Groups)
	}
}