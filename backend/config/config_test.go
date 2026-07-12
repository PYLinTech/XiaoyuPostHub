package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 测试场景：.env 解析器（白盒）
// 覆盖语法、注释、空行、export 前缀、引号转义、错误格式容错。

func TestParseEnvLine(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		wantKey   string
		wantValue string
		wantOK    bool
	}{
		// 正常 KV
		{"simple k=v", "FOO=bar", "FOO", "bar", true},
		{"trim both sides", "  FOO =  bar  ", "FOO", "bar", true},

		// 空行 / 注释
		{"empty line", "", "", "", false},
		{"whitespace only", "   \t  ", "", "", false},
		{"comment line", "# 数据库地址", "", "", false},
		{"hash without space still comment", "#FOO=bar", "", "", false},

		// export 前缀
		{"export prefix", "export FOO=bar", "FOO", "bar", true},
		{"export with spaces", "  export   FOO=bar", "FOO", "bar", true},

		// 引号
		{"double quoted", `FOO="hello world"`, "FOO", "hello world", true},
		{"quoted empty", `FOO=""`, "FOO", "", true},
		{"escaped quote", `FOO="he\"llo"`, "FOO", `he"llo`, true},
		{"escaped backslash", `FOO="a\\b"`, "FOO", `a\b`, true},
		{"plain value keeps space inside", `FOO=hello world`, "FOO", "hello world", true},

		// 错误格式（应容错跳过，不应 panic）
		{"no equals", "FOO_no_eq_at_all", "", "", false},
		{"empty key", "=value", "", "", false},
		{"only equals", "=", "", "", false},

		// URL 内的等号 / 井号
		{"url with equals", `DB_URL=postgresql://u:p@h:5432/d?sslmode=disable`, "DB_URL",
			"postgresql://u:p@h:5432/d?sslmode=disable", true},

		// 行内带 #（当作 VALUE 内容，不当注释）
		{"hash in value", `FOO=abc#def`, "FOO", "abc#def", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k, v, ok := parseEnvLine(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if k != tc.wantKey {
				t.Errorf("key = %q, want %q", k, tc.wantKey)
			}
			if v != tc.wantValue {
				t.Errorf("value = %q, want %q", v, tc.wantValue)
			}
		})
	}
}

func TestParseEnvReader_DedupesAndCounts(t *testing.T) {
	in := strings.Join([]string{
		"# 注释",
		"",
		"FOO=1",
		"FOO=2", // 后写覆盖前写
		"BAR=hello",
		"export BAZ=qux",
	}, "\n")

	out, err := parseEnvReader(strings.NewReader(in), make(map[string]string))
	if err != nil {
		t.Fatal(err)
	}
	if out["FOO"] != "2" {
		t.Errorf("FOO = %q, want 2", out["FOO"])
	}
	if out["BAR"] != "hello" {
		t.Errorf("BAR = %q", out["BAR"])
	}
	if out["BAZ"] != "qux" {
		t.Errorf("BAZ = %q", out["BAZ"])
	}
}

// --- Load：文件存在 + 环境变量覆盖 + 校验（白盒 + 文件 I/O 集成） ---

func TestLoad_FromFile(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	mustWrite(t, envPath, strings.Join([]string{
		"# 测试 env",
		"DATABASE_URL=postgresql://u:p@host:5432/db",
		"SUPER_ADMIN_USERNAME=admin",
		"SUPER_ADMIN_PASSWORD_HASH=" + bcryptCost12Hash,
	}, "\n"))

	cfg, err := Load(envPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DatabaseURL != "postgresql://u:p@host:5432/db" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.SuperAdminUsername != "admin" {
		t.Errorf("SuperAdminUsername = %q", cfg.SuperAdminUsername)
	}
	if cfg.SuperAdminPasswordHash != bcryptCost12Hash {
		t.Errorf("SuperAdminPasswordHash = %q", cfg.SuperAdminPasswordHash)
	}
	if cfg.EnvFile != envPath {
		t.Errorf("EnvFile = %q", cfg.EnvFile)
	}
}

// env 优先级：环境变量必须覆盖 .env 文件内的值。
func TestLoad_EnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	mustWrite(t, envPath, "DATABASE_URL=from-file\nSUPER_ADMIN_USERNAME=admin\nSUPER_ADMIN_PASSWORD_HASH="+bcryptCost12Hash+"\n")

	t.Setenv("DATABASE_URL", "from-env")

	cfg, err := Load(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "from-env" {
		t.Errorf("env should override file: got %q", cfg.DatabaseURL)
	}
}

func TestLoad_MissingFileIsOK(t *testing.T) {
	// 用一个一定不存在的路径，验证 Load 不会因文件 I/O 报 panic，
	// 字段缺失导致的 *ValidationError 是另一回事（也合法）。
	dir := t.TempDir()
	bogus := filepath.Join(dir, ".env.not.exists")
	cfg, err := Load(bogus)

	// 这里允许两种结果：
	//   a. 没有任何字段被填齐，validate() 返回 ValidationError。
	//   b. 运气好正好全空，但仍然 validate() 返回错误。
	// 关键断言：返回的错误不应是 os.ErrNotExist 之类的 I/O 错误；
	// 而是 *ValidationError（或 nil）。
	if err != nil {
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("missing file: expected *ValidationError, got %T: %v", err, err)
		}
	}
	if cfg != nil && cfg.EnvFile != bogus {
		t.Errorf("EnvFile = %q, want %q", cfg.EnvFile, bogus)
	}
}

// Load 缺必填字段：必须返回 *ValidationError。
func TestLoad_ValidationError(t *testing.T) {
	cases := []struct {
		name    string
		content string
		missing []string
	}{
		{
			name:    "all empty",
			content: "",
			missing: []string{"DATABASE_URL", "SUPER_ADMIN_USERNAME", "SUPER_ADMIN_PASSWORD_HASH"},
		},
		{
			name:    "only db",
			content: "DATABASE_URL=postgresql://x\n",
			missing: []string{"SUPER_ADMIN_USERNAME", "SUPER_ADMIN_PASSWORD_HASH"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			envPath := filepath.Join(dir, ".env")
			mustWrite(t, envPath, tc.content)

			cfg, err := Load(envPath)
			if err == nil {
				t.Fatalf("expected error, got cfg=%+v", cfg)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error type = %T, want *ValidationError", err)
			}
			if !sameStringSet(ve.Missing, tc.missing) {
				t.Errorf("Missing = %v, want %v", ve.Missing, tc.missing)
			}
			if cfg != nil {
				t.Error("cfg should be nil on validation error")
			}
		})
	}
}

// 环境变量补全：env 提供 username 与 hash，文件只缺也能通过校验。
func TestLoad_PartialFromEnv(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	mustWrite(t, envPath, "DATABASE_URL=postgresql://x\n")

	t.Setenv("SUPER_ADMIN_USERNAME", "env-admin")
	t.Setenv("SUPER_ADMIN_PASSWORD_HASH", bcryptCost12Hash)

	cfg, err := Load(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SuperAdminUsername != "env-admin" {
		t.Errorf("want env-admin, got %q", cfg.SuperAdminUsername)
	}
}

func TestLoad_EmptyPathOnlyEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://env-only")
	t.Setenv("SUPER_ADMIN_USERNAME", "u")
	t.Setenv("SUPER_ADMIN_PASSWORD_HASH", "h")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgresql://env-only" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
}

// --- 辅助 ---

// bcryptCost12Hash 是合法的 bcrypt cost=12 测试用 hash 字符串。
//
//   - 前缀 $2a$12$ (7 字符) + 53 字符 base64 = 60 字符总长，符合 bcrypt 输出格式
//   - 不对应任何真实密码（仅做格式占位，测试只验证 Load 把它原样读出）
const bcryptCost12Hash = "$2a$12$" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
	"abcdefghijklmnopqrstuvwxyz" +
	"0"

// TestBcryptCost12Hash_Format 自检：确保测试用常量本身格式合法。
// 如果有人误改了它，这个测试会先 fail，提示"测试数据坏了，不是 bcrypt 解析逻辑坏了"。
func TestBcryptCost12Hash_Format(t *testing.T) {
	if len(bcryptCost12Hash) != 60 {
		t.Fatalf("bcrypt cost=12 hash 长度应为 60，得到 %d", len(bcryptCost12Hash))
	}
	if !strings.HasPrefix(bcryptCost12Hash, "$2a$12$") {
		t.Fatalf("bcrypt cost=12 hash 必须以 $2a$12$ 开头，得到 %q", bcryptCost12Hash[:10])
	}
}

func TestLoad_SingleQuotedValues(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	mustWrite(t, envPath, "DATABASE_URL='postgresql://local/test'\nSUPER_ADMIN_USERNAME='admin'\nSUPER_ADMIN_PASSWORD_HASH='"+bcryptCost12Hash+"'\n")
	cfg, err := Load(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgresql://local/test" || cfg.SuperAdminUsername != "admin" || cfg.SuperAdminPasswordHash != bcryptCost12Hash {
		t.Fatalf("单引号配置解析错误：%+v", cfg)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]int, len(a))
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}
