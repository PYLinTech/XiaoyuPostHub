package db

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

var installIdentifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,31}$`)

// TestInstallConnection 验证连接可达，并确认该账号能在 public schema 创建对象。
// 检查表在事务中创建并立即回滚，不会留下任何数据。
func TestInstallConnection(ctx context.Context, databaseURL string) error {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("连接 PostgreSQL 失败：%w", err)
	}
	defer conn.Close(context.Background())

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始权限检查失败：%w", err)
	}
	defer tx.Rollback(context.Background())

	if _, err := tx.Exec(ctx, `CREATE TABLE public.xph_install_permission_check (id integer)`); err != nil {
		return fmt.Errorf("数据库账号缺少 public schema 建表权限：%w", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		return fmt.Errorf("结束权限检查失败：%w", err)
	}
	return nil
}

// ProvisionDatabase 使用管理员连接创建或修正应用专用用户和数据库，返回应用连接地址。
func ProvisionDatabase(ctx context.Context, adminURL, databaseName, username, password string) (string, error) {
	if !installIdentifierPattern.MatchString(databaseName) {
		return "", fmt.Errorf("数据库名格式无效")
	}
	if !installIdentifierPattern.MatchString(username) {
		return "", fmt.Errorf("数据库用户名格式无效")
	}
	if len(password) < 16 {
		return "", fmt.Errorf("数据库密码长度不足")
	}

	parsed, err := url.Parse(adminURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" {
		return "", fmt.Errorf("管理员连接地址格式无效")
	}

	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return "", fmt.Errorf("连接 PostgreSQL 管理员账号失败：%w", err)
	}
	defer conn.Close(context.Background())

	roleIdent := pgx.Identifier{username}.Sanitize()
	databaseIdent := pgx.Identifier{databaseName}.Sanitize()
	passwordLiteral := quoteInstallLiteral(password)

	var roleExists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=$1)`, username).Scan(&roleExists); err != nil {
		return "", fmt.Errorf("检查数据库用户失败：%w", err)
	}
	if roleExists {
		if _, err := conn.Exec(ctx, fmt.Sprintf(`ALTER ROLE %s WITH LOGIN PASSWORD %s`, roleIdent, passwordLiteral)); err != nil {
			return "", fmt.Errorf("更新数据库用户失败：%w", err)
		}
	} else if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE ROLE %s WITH LOGIN PASSWORD %s`, roleIdent, passwordLiteral)); err != nil {
		return "", fmt.Errorf("创建数据库用户失败：%w", err)
	}

	var databaseExists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)`, databaseName).Scan(&databaseExists); err != nil {
		return "", fmt.Errorf("检查数据库失败：%w", err)
	}
	if databaseExists {
		if _, err := conn.Exec(ctx, fmt.Sprintf(`ALTER DATABASE %s OWNER TO %s`, databaseIdent, roleIdent)); err != nil {
			return "", fmt.Errorf("修正数据库所有者失败：%w", err)
		}
	} else if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, databaseIdent, roleIdent)); err != nil {
		return "", fmt.Errorf("创建数据库失败：%w", err)
	}

	parsed.User = url.UserPassword(username, password)
	parsed.Path = "/" + databaseName
	applicationURL := parsed.String()
	if err := TestInstallConnection(ctx, applicationURL); err != nil {
		return "", err
	}
	return applicationURL, nil
}

func quoteInstallLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
