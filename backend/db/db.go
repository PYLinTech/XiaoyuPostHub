// Package db 负责创建并管理后端使用的 PostgreSQL 连接池。
//
// 设计选择：
//
//   - 使用 jackc/pgx v5 + pgxpool：纯 Go、性能优于 database/sql+lib/pq，
//     内建连接池管理与上下文感知 API。
//   - Open 在初始化期就会做一次 Ping：连接失败立即暴露，不让服务以"半残"状态上线。
//   - 默认池参数保守（MaxConns 派生自 GOMAXPROCS），
//     后续压力测试后可在 config.Config 增加阈值字段再覆盖。
//   - 任何输出都不得泄露 DATABASE_URL 中的密码；
//     DescribeURL 仅保留 scheme://user@host:port/db 这种运营诊断信息。
package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB 是后端使用的 PostgreSQL 连接池的薄封装。
// 暴露 Pool() 让上层（未来的 repository 层）拿到底层进行查询。
type DB struct {
	pool *pgxpool.Pool
}

// Open 创建并验证一个连接池。
//
//  1. 解析 DATABASE_URL；
//  2. 应用默认池参数；
//  3. 用 ctx 做一次 Ping，失败立即返回错误（含底层 wrapped error）。
//
// ctx 应使用 main 的启动上下文带超时（例如 5s），避免无界阻塞。
func Open(ctx context.Context, dbURL string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, fmt.Errorf("解析 DATABASE_URL 失败：%w", err)
	}
	cfg.MaxConns = pickMaxConns(cfg.MaxConns)
	cfg.MinConns = pickMinConns(cfg.MinConns)
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("创建连接池失败：%w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("连接 PostgreSQL 失败：%w", err)
	}

	return &DB{pool: pool}, nil
}

// Close 释放全部连接；幂等。
func (d *DB) Close() {
	if d == nil || d.pool == nil {
		return
	}
	d.pool.Close()
}

// Pool 返回底层连接池，供上层做查询。
func (d *DB) Pool() *pgxpool.Pool {
	if d == nil {
		return nil
	}
	return d.pool
}

// Ping 做一次快速活体探测；用于 /api/health 升级或监控探针。
func (d *DB) Ping(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return errors.New("db: 尚未初始化")
	}
	return d.pool.Ping(ctx)
}

// DescribeURL 把 DATABASE_URL 脱敏成"postgresql://user@host:port/db?sslmode=disable"。
// 仅保留协议、用户、地址、端口、库名与查询参数；密码替换为 ***。
// 用于日志和运维诊断。
func DescribeURL(dbURL string) string {
	if strings.TrimSpace(dbURL) == "" {
		return "<empty>"
	}

	// 不是标准 URL 时直接返回固定占位，避免日志反而泄露异常字符串。
	u, err := url.Parse(dbURL)
	if err != nil || u.Scheme == "" {
		return "<unparseable url>"
	}

	var sb strings.Builder
	sb.WriteString(u.Scheme)
	sb.WriteString("://")
	if u.User != nil {
		sb.WriteString(u.User.Username())
		if _, hasPwd := u.User.Password(); hasPwd {
			sb.WriteString(":***")
		}
		sb.WriteByte('@')
	}
	if u.Host != "" {
		sb.WriteString(u.Host)
	}
	if u.Path != "" {
		sb.WriteString(u.Path)
	}
	if u.RawQuery != "" {
		sb.WriteByte('?')
		sb.WriteString(u.RawQuery)
	}
	return sb.String()
}

// pickMaxConns 在用户未显式设置时提供合理默认值；保留 pgx 的 0=无限制语义。
func pickMaxConns(fallback int32) int32 {
	if fallback > 0 {
		return fallback
	}
	n := runtime.GOMAXPROCS(0) * 4
	if n < 4 {
		n = 4
	}
	if n > 50 {
		n = 50
	}
	return int32(n)
}

// pickMinConns 默认保持少量预热连接，避免冷启动抖一下。
func pickMinConns(fallback int32) int32 {
	if fallback > 0 {
		return fallback
	}
	return 2
}

// ApplySchema 按文件名顺序执行 schema 目录下的所有 .sql 文件。
// 文件名约定 001_xxx.sql、002_xxx.sql,字典序即执行顺序。
// 推荐所有 CREATE TABLE 用 IF NOT EXISTS,以保证幂等。
func ApplySchema(ctx context.Context, pool *pgxpool.Pool, schemaDir string) error {
	files, err := filepath.Glob(filepath.Join(schemaDir, "*.sql"))
	if err != nil {
		return fmt.Errorf("扫描 schema 目录失败: %w", err)
	}
	sort.Strings(files)
	for _, f := range files {
		sqlBytes, readErr := os.ReadFile(f)
		if readErr != nil {
			return fmt.Errorf("读取 %s 失败: %w", f, readErr)
		}
		if _, execErr := pool.Exec(ctx, string(sqlBytes)); execErr != nil {
			return fmt.Errorf("执行 %s 失败: %w", f, execErr)
		}
	}
	return nil
}
