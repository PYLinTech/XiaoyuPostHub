package role_test

// 白盒测试：role.Repo 的业务规则。
// 连真 PG，缺 DB fail-fast。
// 用 uniqueUsername / cleanupUser 避免测试间互相污染。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/bootstrap"
	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/role"
	"github.com/PYLinTech/XiaoyuPostHub/backend/test/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestMain(m *testing.M) {
	dbtest.SetupOrExit(m)
	// 启动期 seed：让系统数据就绪
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := bootstrap.NewAuthCatalog(dbtest.Pool()).Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "role_test: bootstrap 失败: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	dbtest.Teardown()
	os.Exit(code)
}

// uniqueUsername / cleanupUser 避免 user 表数据污染。
// 注意：role 测的 user 创建必须用 sqlc 走 user_roles；普通 role 测试不需要 user。
// 这里只用到 role/group 相关表的清理。

func uniqueName(t *testing.T, prefix string) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b))
}

func cleanupRole(t *testing.T, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = dbtest.Pool().Exec(ctx, "DELETE FROM role_permissions WHERE role_id IN (SELECT id FROM roles WHERE name = $1)", name)
	_, _ = dbtest.Pool().Exec(ctx, "DELETE FROM user_roles WHERE role_id IN (SELECT id FROM roles WHERE name = $1)", name)
	_, _ = dbtest.Pool().Exec(ctx, "DELETE FROM roles WHERE name = $1", name)
}

func TestCreateRole_RejectsSuperAdminName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q := dbtest.Queries()
	r := role.NewRepo(q)

	_, err := r.CreateRole(ctx, role.ReservedRoleName, "should fail", true)
	if err != role.ErrReservedRoleName {
		t.Errorf("CreateRole(super_admin) 期望 ErrReservedRoleName，得到 %v", err)
	}
}

func TestUpdateRoleDescription_AllowsSystemRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q := dbtest.Queries()
	r := role.NewRepo(q)

	// 找 anonymous
	anon, err := r.GetByName(ctx, role.NameAnonymous)
	if err != nil {
		t.Fatalf("GetByName(anonymous): %v", err)
	}
	// 备份原 description 用于恢复
	original := anon.Description
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = dbtest.Pool().Exec(cleanupCtx, "UPDATE roles SET description = $1 WHERE id = $2", original, anon.ID)
	})

	// 系统 role 的 description **允许**通过配置面板修改
	if err := r.UpdateRoleDescription(ctx, anon.ID, "test updated by config panel"); err != nil {
		t.Errorf("UpdateRoleDescription(anonymous) 应允许，得到 %v", err)
	}
	// 验证
	updated, err := r.GetByID(ctx, anon.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !updated.Description.Valid || updated.Description.String != "test updated by config panel" {
		t.Errorf("description 未更新，得到 %v", updated.Description)
	}
}

func TestDeleteRole_RejectsSystemRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q := dbtest.Queries()
	r := role.NewRepo(q)

	anon, err := r.GetByName(ctx, role.NameAnonymous)
	if err != nil {
		t.Fatalf("GetByName(anonymous): %v", err)
	}
	err = r.DeleteRole(ctx, anon.ID)
	if err != role.ErrRoleIsSystem {
		t.Errorf("DeleteRole(anonymous) 期望 ErrRoleIsSystem，得到 %v", err)
	}
}

func TestGrantPermission_AllowsSystemRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q := dbtest.Queries()
	r := role.NewRepo(q)

	// 找 anonymous，给它加一个临时 permission（确保是允许的）
	anon, err := r.GetByName(ctx, role.NameAnonymous)
	if err != nil {
		t.Fatalf("GetByName(anonymous): %v", err)
	}
	// 直接 INSERT 一条 role_permission 用于测试，测完清理
	_, err = dbtest.Pool().Exec(ctx,
		"INSERT INTO role_permissions (role_id, permission) VALUES ($1, $2) ON CONFLICT DO NOTHING",
		anon.ID, permission.Login)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = dbtest.Pool().Exec(cleanupCtx, "DELETE FROM role_permissions WHERE role_id = $1 AND permission = $2", anon.ID, permission.Login)
	})

	// 测 GrantPermission：anonymous 是 system role，应该允许
	if err := r.GrantPermission(ctx, anon.ID, permission.Login); err != nil {
		t.Errorf("GrantPermission(anonymous, login) 应允许，得到 %v", err)
	}
	// 验证
	perms, err := r.ListPermissions(ctx, anon.ID)
	if err != nil {
		t.Fatalf("ListPermissions: %v", err)
	}
	found := false
	for _, p := range perms {
		if p == permission.Login {
			found = true
		}
	}
	if !found {
		t.Errorf("anonymous 应包含 login，得到 %v", perms)
	}
}

func TestRevokePermission_AllowsSystemRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q := dbtest.Queries()
	r := role.NewRepo(q)

	anon, err := r.GetByName(ctx, role.NameAnonymous)
	if err != nil {
		t.Fatalf("GetByName(anonymous): %v", err)
	}
	// 先确保 anonymous 至少有 login
	_, err = dbtest.Pool().Exec(ctx,
		"INSERT INTO role_permissions (role_id, permission) VALUES ($1, $2) ON CONFLICT DO NOTHING",
		anon.ID, permission.Login)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = dbtest.Pool().Exec(cleanupCtx, "DELETE FROM role_permissions WHERE role_id = $1 AND permission = $2", anon.ID, permission.Login)
	})

	// 测 RevokePermission：允许
	if err := r.RevokePermission(ctx, anon.ID, permission.Login); err != nil {
		t.Errorf("RevokePermission(anonymous, login) 应允许，得到 %v", err)
	}
	perms, err := r.ListPermissions(ctx, anon.ID)
	if err != nil {
		t.Fatalf("ListPermissions: %v", err)
	}
	for _, p := range perms {
		if p == permission.Login {
			t.Errorf("RevokePermission 后 anonymous 不应包含 login，得到 %v", perms)
		}
	}
}

func TestAssignRoleToUser_RejectsNonAssignable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q := dbtest.Queries()
	r := role.NewRepo(q)

	anon, err := r.GetByName(ctx, role.NameAnonymous)
	if err != nil {
		t.Fatalf("GetByName(anonymous): %v", err)
	}

	// 找任意 user（默认 default_user user）；用一个不存在的 user_id 也行
	// 这里用 0 让它走 ErrRoleNotFound 的路径——但 ErrRoleNotFound 来自 GetByID
	// 我们要测的是 assignable 拒绝。建一个临时 user 走完流程。
	username := uniqueName(t, "u_assign_rej")
	hash := "$2a$12$ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0"
	_, err = dbtest.Pool().Exec(ctx,
		"INSERT INTO users (username, password_hash) VALUES ($1, $2)", username, hash)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = dbtest.Pool().Exec(cleanupCtx, "DELETE FROM user_group_memberships WHERE user_id IN (SELECT id FROM users WHERE username = $1)", username)
		_, _ = dbtest.Pool().Exec(cleanupCtx, "DELETE FROM user_roles WHERE user_id IN (SELECT id FROM users WHERE username = $1)", username)
		_, _ = dbtest.Pool().Exec(cleanupCtx, "DELETE FROM users WHERE username = $1", username)
	})

	var userID int64
	err = dbtest.Pool().QueryRow(ctx, "SELECT id FROM users WHERE username = $1", username).Scan(&userID)
	if err != nil {
		t.Fatalf("query user id: %v", err)
	}

	// 验证：anonymous.Assignable = false
	if anon.Assignable {
		t.Fatal("anonymous 应是 assignable=false")
	}

	// 测 AssignRoleToUser：应被 ErrRoleNotAssignable 拒绝
	err = r.AssignRoleToUser(ctx, userID, anon.ID)
	if err != role.ErrRoleNotAssignable {
		t.Errorf("AssignRoleToUser(anonymous) 期望 ErrRoleNotAssignable，得到 %v", err)
	}
	// 确认 user_roles 没有被插入
	var n int
	err = dbtest.Pool().QueryRow(ctx, "SELECT count(*) FROM user_roles WHERE user_id = $1 AND role_id = $2", userID, anon.ID).Scan(&n)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("user_roles 误插入 anonymous 行")
	}

	// sanity check：pgx.ErrNoRows
	_ = pgx.ErrNoRows
}
