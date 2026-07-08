package db

// 文本断言：检查 schema 文件内容是否符合"启动期幂等"目标。
// 不需要 DB。

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// resolveSchemaDir 从本测试文件位置回溯到 backend/db/schema 的绝对路径。
// 不依赖 go test 的 cwd。
func resolveSchemaDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位 schema_text_test.go 路径")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), ".."))
	if err != nil {
		t.Fatalf("解析 project root 失败：%v", err)
	}
	return filepath.Join(root, "db", "schema")
}

func readSchema(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(resolveSchemaDir(t), name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// ---- 结构性：FK 必须内联在 CREATE TABLE 里 ----

// 003 禁止出现裸的"ADD CONSTRAINT users_quota_profile_id_fkey"——
// quota_profiles 已经在 000 建好，users 表的 quota_profile_id FK 必须在
// 001 CREATE TABLE 里就内联；任何后续 ALTER 都会破坏 schema 幂等。
func TestSchema_003_NoNakedAddConstraintForUsersQuotaFK(t *testing.T) {
	body := readSchema(t, "003_groups_quotas.sql")
	if strings.Contains(body, "ADD CONSTRAINT users_quota_profile_id_fkey") {
		t.Error("003 出现 'ADD CONSTRAINT users_quota_profile_id_fkey'，schema 不可幂等。" +
			"quota_profiles 已经在 000 建好，users 表的 FK 必须在 001_users.sql 里内联。")
	}
}

// 001_users.sql 必须内联 quota_profile_id FK 引用 quota_profiles
func TestSchema_001_UsersInlineQuotaProfileFK(t *testing.T) {
	body := readSchema(t, "001_users.sql")
	want := "quota_profile_id"
	if !strings.Contains(body, want) {
		t.Error("001_users.sql 必须含 quota_profile_id 字段")
	}
	wantFK := "REFERENCES quota_profiles(id)"
	if !strings.Contains(body, wantFK) {
		t.Error("001_users.sql 必须内联 quota_profile_id FK 引用 quota_profiles(id)，" +
			"不能放到后续文件用 ALTER TABLE 补 FK")
	}
}

// 000_quota_profiles.sql 必须存在
func TestSchema_000_QuotaProfilesExists(t *testing.T) {
	body := readSchema(t, "000_quota_profiles.sql")
	if !strings.Contains(body, "CREATE TABLE IF NOT EXISTS quota_profiles") {
		t.Error("000_quota_profiles.sql 必须建 quota_profiles 表（让 001_users FK 可内联）")
	}
}

// 003 不再重复建 quota_profiles（已经在 000 建过）
func TestSchema_003_NoDuplicateQuotaProfilesCreate(t *testing.T) {
	body := readSchema(t, "003_groups_quotas.sql")
	if strings.Contains(body, "CREATE TABLE IF NOT EXISTS quota_profiles") {
		t.Error("003_groups_quotas.sql 不应再 CREATE quota_profiles（已迁移到 000_quota_profiles.sql）")
	}
}

// ---- 启动期约束：禁止兼容层 ----

// 项目原则：项目初期不做旧库迁移或兼容逻辑。schema 文件应保持干净，
// 不含任何 IF EXISTS / DROP ... IF EXISTS / IF NOT EXISTS 兼容语法（除了
// CREATE TABLE IF NOT EXISTS 这种纯幂等的建表）。
// 任何兼容/迁移代码都应放在独立的迁移工具里，不污染核心 schema。
func TestSchema_NoCompatibilitySyntax(t *testing.T) {
	dir := resolveSchemaDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read schema dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body := readSchema(t, e.Name())
		// 检查所有非"CREATE TABLE IF NOT EXISTS"的 IF EXISTS / IF NOT EXISTS
		// 简化版：扫描整文件，发现可疑模式就报
		if strings.Contains(body, "DROP INDEX IF EXISTS") {
			t.Errorf("%s 含 'DROP INDEX IF EXISTS'，是旧库兼容语法", e.Name())
		}
		if strings.Contains(body, "DROP COLUMN IF EXISTS") {
			t.Errorf("%s 含 'DROP COLUMN IF EXISTS'，是旧库兼容语法", e.Name())
		}
		if strings.Contains(body, "ADD COLUMN IF NOT EXISTS") {
			t.Errorf("%s 含 'ADD COLUMN IF NOT EXISTS'，是旧库兼容语法", e.Name())
		}
		if strings.Contains(body, "ADD CONSTRAINT IF NOT EXISTS") {
			t.Errorf("%s 含 'ADD CONSTRAINT IF NOT EXISTS'，是旧库兼容语法", e.Name())
		}
		if strings.Contains(body, "DROP CONSTRAINT IF EXISTS") {
			t.Errorf("%s 含 'DROP CONSTRAINT IF EXISTS'，是旧库兼容语法", e.Name())
		}
		if strings.Contains(body, "DROP SCHEMA IF EXISTS") {
			t.Errorf("%s 含 'DROP SCHEMA IF EXISTS'，是测试/迁移残留，不应在生产 schema", e.Name())
		}
		if strings.Contains(body, "DO $$") && strings.Contains(body, "information_schema") {
			t.Errorf("%s 含 DO 块 + information_schema 查询，是旧库迁移/守卫逻辑", e.Name())
		}
	}
}

// 002 必须有 roles_no_super_admin CHECK 约束（数据层兜底 super_admin 不入库）
func TestSchema_002_RolesNoSuperAdminCheck(t *testing.T) {
	body := readSchema(t, "002_permissions_and_roles.sql")
	if !strings.Contains(body, "roles_no_super_admin") {
		t.Error("002 必须有 'roles_no_super_admin' CHECK 约束，防止 super_admin 入库")
	}
}

// ---- 启动期约束：禁止 bootstrap_state / lock 表 ----

// 项目原则：bootstrap 不新增状态表。状态全部用 SQL 行为表达。
// schema 文件中不应出现 CREATE TABLE bootstrap_state / lock / migration_history 等。
func TestSchema_NoBootstrapStateTable(t *testing.T) {
	dir := resolveSchemaDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read schema dir: %v", err)
	}
	forbidden := []string{"bootstrap_state", "bootstrap_lock", "migration_history", "schema_migrations"}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body := readSchema(t, e.Name())
		for _, f := range forbidden {
			if strings.Contains(body, "CREATE TABLE IF NOT EXISTS "+f) ||
				strings.Contains(body, "CREATE TABLE "+f) {
				t.Errorf("%s 创建了禁止的状态表 %q，bootstrap 应保持无状态（用 SQL 行为 + 事务 + advisory lock）", e.Name(), f)
			}
		}
	}
}
