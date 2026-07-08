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

// ApplyEmbeddedSchema 按文件名顺序执行编译进二进制的 schema SQL。
//
// 与 ApplySchema 的区别：
//   - 数据源：embeddedSchemaFS（go:embed 编进二进制，运行时无需任何磁盘路径）
//   - 调用方：main.go 启动期 + 集成测试 SetupOrExit
//
// 部署产物只需要 xph-backend 二进制和 web/ 目录，不再需要 db/schema。
//
// 行为约束：
//   - 文件名按字典序排序执行，保证 000 → 001 → 002 → 003 的依赖顺序
//   - 如果嵌入的 schema 目录里一个 .sql 文件都没有（开发期被人删空），返回 error
//     而非静默成功——静默成功会让 bootstrap 在空库上跑出更混乱的报错
//   - 任意一条 SQL 执行失败立即返回，错误信息包含具体文件名
func ApplyEmbeddedSchema(ctx context.Context, pool *pgxpool.Pool) error {
	entries, err := embeddedSchemaFS.ReadDir("schema")
	if err != nil {
		return fmt.Errorf("读取 embedded schema 目录失败: %w", err)
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".sql") {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	if len(names) == 0 {
		return fmt.Errorf("embedded schema 目录为空，没有任何 .sql 文件被编译进二进制")
	}

	for _, name := range names {
		path := "schema/" + name
		sqlBytes, err := embeddedSchemaFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("读取 embedded schema 文件 %s 失败: %w", path, err)
		}
		if _, execErr := pool.Exec(ctx, string(sqlBytes)); execErr != nil {
			return fmt.Errorf("执行 embedded schema 文件 %s 失败: %w", path, execErr)
		}
	}
	return nil
}

// ApplySchema 从磁盘目录读取并按文件名顺序执行 .sql 文件。
//
// **运行时启动期不应调用本函数**——改用 ApplyEmbeddedSchema。
// 磁盘版保留只为了以下场景：
//   - 开发者本地手工跑一个临时 SQL 目录做调试
//   - 未来如果引入独立的迁移工具（当前不引入，见 bootstrap/auth.go 设计原则）
//
// 行为约束：
//   - 按文件名排序执行
//   - 如果目录下一个 .sql 都没有，返回 error（避免静默"成功"导致后续
//     bootstrap 在空库上跑出更混乱的报错）
//   - 推荐所有 CREATE TABLE 用 IF NOT EXISTS 以保证幂等
func ApplySchema(ctx context.Context, pool *pgxpool.Pool, schemaDir string) error {
	files, err := filepath.Glob(filepath.Join(schemaDir, "*.sql"))
	if err != nil {
		return fmt.Errorf("扫描 schema 目录失败: %w", err)
	}
	sort.Strings(files)
	if len(files) == 0 {
		return fmt.Errorf("在 %s 下没有找到任何 .sql schema 文件", schemaDir)
	}
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
