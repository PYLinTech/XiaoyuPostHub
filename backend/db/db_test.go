package db

import (
	"context"
	"testing"
	"time"
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
