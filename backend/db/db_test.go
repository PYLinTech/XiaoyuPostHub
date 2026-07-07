package db

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// --- 白盒：DescribeURL 脱敏（无需数据库，永久跑） ---

func TestDescribeURL_RemovesPassword(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "full url with password",
			in:   "postgresql://alice:secret@localhost:5432/xph?sslmode=disable",
			want: "postgresql://alice:***@localhost:5432/xph?sslmode=disable",
		},
		{
			name: "no password",
			in:   "postgresql://bob@localhost:5432/xph",
			want: "postgresql://bob@localhost:5432/xph",
		},
		{
			name: "empty",
			in:   "",
			want: "<empty>",
		},
		{
			name: "garbage",
			in:   "not a url at all",
			want: "<unparseable url>",
		},
		{
			name: "password containing weird chars still scrubbed",
			in:   "postgresql://bob:p%40ss%21@db.local:5432/xph",
			want: "postgresql://bob:***@db.local:5432/xph",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DescribeURL(tc.in); got != tc.want {
				t.Errorf("DescribeURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// --- 白盒：池默认值（无需数据库，永久跑） ---

func TestPickMaxConns_DefaultsAndOverrides(t *testing.T) {
	if got := pickMaxConns(0); got < 4 {
		t.Errorf("default MaxConns should be >= 4, got %d", got)
	}
	if got := pickMaxConns(7); got != 7 {
		t.Errorf("explicit MaxConns = %d, want 7", got)
	}
}

func TestPickMinConns_DefaultsAndOverrides(t *testing.T) {
	if got := pickMinConns(0); got != 2 {
		t.Errorf("default MinConns = %d, want 2", got)
	}
	if got := pickMinConns(3); got != 3 {
		t.Errorf("explicit MinConns = %d, want 3", got)
	}
}

// --- 真实 PostgreSQL 集成：默认必跑，缺 DB 直接 FAIL（fail-fast 哲学） ---

// requireDB 返回用于集成测试的 PostgreSQL 连接 URL，查找顺序：
//
//  1. 进程环境 DATABASE_URL（生产同源；CI 直接注入）
//  2. 项目根 deploy/.env 内的 DATABASE_URL（开发者本机一键 fallback）
//
// 任何一路缺失或 ping 失败都 t.Fatal 而不是 t.Skip——
// DB 是 XiaoyuPostHub 的基础设施，缺它测的是空气，必须立刻红。
func requireDB(t *testing.T) string {
	t.Helper()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = readDeployEnv(t, "DATABASE_URL")
	}
	if url == "" {
		t.Fatal("未找到 DATABASE_URL：请设置环境变量或在 deploy/.env 中提供")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := pingURL(ctx, url); err != nil {
		t.Fatalf("DATABASE_URL 不可达：%v", err)
	}
	return url
}

// readDeployEnv 从项目根 deploy/.env 中按 key 取值。这是测试内部的解析器：
//
//   - **不复用 config 包**：配置层不该为测试提供"按 key 查"之类的便利 API，
//     那是为测试而生的形态。测试自己实现一个够用的子集解析器。
//   - **足够支持现有 deploy/.env**：空行 / 整行 # 注释 / 可选 export 前缀 / KEY=VALUE。
//     引号与转义不展开（现有 .env 里没用过），保持 20 行内可审计。
func readDeployEnv(t *testing.T, key string) string {
	t.Helper()
	path := deployEnvPath(t)
	f, err := os.Open(path)
	if err != nil {
		return "" // 文件不存在 / 读不开 → 视为"无"，让调用方决定
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
		if k != key {
			continue
		}
		return strings.TrimSpace(line[eq+1:])
	}
	return ""
}

// deployEnvPath 从本测试源码位置回溯到 deploy/.env 的绝对路径。
func deployEnvPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位 db_test.go 路径")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	if err != nil {
		t.Fatalf("解析 project root 失败：%v", err)
	}
	return filepath.Join(root, "deploy", ".env")
}

func TestOpen_SuccessPing(t *testing.T) {
	url := requireDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	d, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if d.Pool() == nil {
		t.Fatal("Pool() 返回 nil")
	}
	if err := d.Ping(ctx); err != nil {
		t.Errorf("Ping after Open: %v", err)
	}
}

func TestOpen_BadURLReturnsError(t *testing.T) {
	// 这个用例故意用烂 URL、不调 requireDB，连真实 DB 都不会被波及。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := Open(ctx, "postgres://bad_syntax")
	if err == nil {
		t.Fatal("expected error for malformed url")
	}
}

func TestOpen_UnreachableFailsQuickly(t *testing.T) {
	// 指向肯定没服务的端口；连接应在 ctx 内失败（Open 内部 ping 用了 5s 上限）。
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	_, err := Open(ctx, "postgresql://nobody:nobody@127.0.0.1:1/nobody?sslmode=disable&connect_timeout=2")
	if err == nil {
		t.Fatal("expected error connecting to closed port")
	}
}

func TestDB_NilSafety(t *testing.T) {
	var d *DB
	if d.Pool() != nil {
		t.Error("nil receiver's Pool() must be nil")
	}
	if err := d.Ping(context.Background()); err == nil {
		t.Error("nil receiver's Ping must error")
	}
}

func TestDB_CloseThenPingFails(t *testing.T) {
	url := requireDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	d, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	d.Close()

	pingCtx, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if err := d.Ping(pingCtx); err == nil {
		t.Error("Ping after Close should fail")
	}
}

// pingURL 是测试辅助：开一个临时 pool，仅用一次 Ping，再关闭。
// 抽出来是为了在 requireDB 阶段也能复用同一条 connect-and-verify 路径。
func pingURL(ctx context.Context, dbURL string) error {
	cfg, err := pgxpool.ParseConfig(dbURL)
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
