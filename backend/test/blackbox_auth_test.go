// 黑盒测试：通过公开业务 Repo / Bootstrap 入口验证端到端行为。
// 计划里 14 个用例全覆盖。
// 连真 PG，缺 DATABASE_URL 直接 fail-fast。

package server_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/bootstrap"
	"github.com/PYLinTech/XiaoyuPostHub/backend/config"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/group"
	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/quota"
	"github.com/PYLinTech/XiaoyuPostHub/backend/role"
	"github.com/PYLinTech/XiaoyuPostHub/backend/test/dbtest"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

func TestMain(m *testing.M) {
	dbtest.SetupOrExit(m)
	// 启动期 bootstrap：让系统数据就绪
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := bootstrap.NewAuthCatalog(dbtest.Pool()).Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "blackbox_auth_test: bootstrap 失败: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	dbtest.Teardown()
	os.Exit(code)
}

// ---------- 测试工具 ----------

func uniqueName(t *testing.T, prefix string) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b))
}

// int64Ptr 工具：构造 *int64 字面量。
func int64Ptr(v int64) *int64 { return &v }

// cleanupUser 删除 user + 级联清理 user_group_memberships / user_roles / user_permission_overrides。
func cleanupUser(t *testing.T, username string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := dbtest.Pool().Exec(ctx, "DELETE FROM user_permission_overrides WHERE user_id IN (SELECT id FROM users WHERE username = $1)", username); err != nil {
		t.Logf("cleanup override: %v", err)
	}
	if _, err := dbtest.Pool().Exec(ctx, "DELETE FROM user_group_memberships WHERE user_id IN (SELECT id FROM users WHERE username = $1)", username); err != nil {
		t.Logf("cleanup ugm: %v", err)
	}
	if _, err := dbtest.Pool().Exec(ctx, "DELETE FROM user_roles WHERE user_id IN (SELECT id FROM users WHERE username = $1)", username); err != nil {
		t.Logf("cleanup user_roles: %v", err)
	}
	if _, err := dbtest.Pool().Exec(ctx, "DELETE FROM users WHERE username = $1", username); err != nil {
		t.Logf("cleanup user: %v", err)
	}
}

// cleanupGroup 删除 group + 级联清理。
func cleanupGroup(t *testing.T, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := dbtest.Pool().Exec(ctx, "DELETE FROM group_roles WHERE group_id IN (SELECT id FROM user_groups WHERE name = $1)", name); err != nil {
		t.Logf("cleanup group_roles: %v", err)
	}
	if _, err := dbtest.Pool().Exec(ctx, "DELETE FROM user_group_memberships WHERE group_id IN (SELECT id FROM user_groups WHERE name = $1)", name); err != nil {
		t.Logf("cleanup ugm: %v", err)
	}
	if _, err := dbtest.Pool().Exec(ctx, "DELETE FROM user_groups WHERE name = $1", name); err != nil {
		t.Logf("cleanup group: %v", err)
	}
}

// cleanupQuota 删除非系统 quota profile。
func cleanupQuota(t *testing.T, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = dbtest.Pool().Exec(ctx, "DELETE FROM quota_profiles WHERE name = $1 AND is_system = FALSE", name)
}

// cleanupRolePermissions 删除某 role 的所有 permission 绑定（保留 role 行）。
func cleanupRolePermissions(t *testing.T, roleName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = dbtest.Pool().Exec(ctx, "DELETE FROM role_permissions WHERE role_id IN (SELECT id FROM roles WHERE name = $1)", roleName)
}

// freshRepos 构造一组干净的 Repo。共享 dbtest.Pool() / Queries()。
func freshRepos() (*user.Repo, *role.Repo, *group.Repo, *quota.Repo) {
	q := dbtest.Queries()
	roleRepo := role.NewRepo(q)
	groupRepo := group.NewRepo(q, roleRepo)
	quotaRepo := quota.NewRepo(q)
	userRepo := user.NewRepo(dbtest.Pool(), q, roleRepo, groupRepo)
	return userRepo, roleRepo, groupRepo, quotaRepo
}

// ---------- 3b. 第二次 bootstrap 不重置 user role 权限 ----------

func TestBlackbox_UserRolePermissionNotResetByBootstrap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, roleRepo, _, _ := freshRepos()

	user, err := roleRepo.GetByName(ctx, role.NameUser)
	if err != nil {
		t.Fatalf("GetByName(user): %v", err)
	}

	// 改 user role 权限：删 share，加 manage_users（管理后台才能加的）
	if err := roleRepo.RevokePermission(ctx, user.ID, permission.Share); err != nil {
		t.Fatalf("RevokePermission(share): %v", err)
	}
	if err := roleRepo.GrantPermission(ctx, user.ID, permission.ManageUsers); err != nil {
		t.Fatalf("GrantPermission(manage_users): %v", err)
	}

	// 跑第二次 bootstrap
	if err := bootstrap.NewAuthCatalog(dbtest.Pool()).Run(ctx); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}

	// 验证：user role 的 share 应被剔除，manage_users 应保留
	perms, err := roleRepo.ListPermissions(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListPermissions: %v", err)
	}
	hasShare, hasManageUsers := false, false
	for _, p := range perms {
		if p == permission.Share {
			hasShare = true
		}
		if p == permission.ManageUsers {
			hasManageUsers = true
		}
	}
	if hasShare {
		t.Errorf("bootstrap 后 user role 不应重置回 share，得到 %v", perms)
	}
	if !hasManageUsers {
		t.Errorf("bootstrap 后 user role 应保留 manage_users，得到 %v", perms)
	}

	// 恢复
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = dbtest.Pool().Exec(cleanupCtx, "DELETE FROM role_permissions WHERE role_id = $1 AND permission = $2", user.ID, permission.ManageUsers)
		_, _ = dbtest.Pool().Exec(cleanupCtx, "INSERT INTO role_permissions (role_id, permission) VALUES ($1, $2) ON CONFLICT DO NOTHING", user.ID, permission.Share)
	})
}

// ---------- 4. 第二次 bootstrap 不覆盖 default_user quota profile 的限额字段 ----------

func TestBlackbox_DefaultUserQuotaLimitsNotOverwritten(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, _, quotaRepo := freshRepos()

	// 查 default_user quota id
	defaultQP, err := quotaRepo.GetByName(ctx, quota.NameDefaultUser)
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}

	// 改 default_user quota 的限额字段
	storage := int64Ptr(12345678)
	if err := quotaRepo.UpdateQuotaProfile(ctx, defaultQP.ID, "manual desc", storage, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("UpdateQuotaProfile: %v", err)
	}

	// 跑第二次 bootstrap
	if err := bootstrap.NewAuthCatalog(dbtest.Pool()).Run(ctx); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}

	// 验证：storage_bytes_limit 仍应是 12345678（bootstrap 没重置）
	got, err := quotaRepo.GetByName(ctx, quota.NameDefaultUser)
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if !got.StorageBytesLimit.Valid || got.StorageBytesLimit.Int64 != 12345678 {
		t.Errorf("default_user quota storage_bytes_limit 被重置: %v", got.StorageBytesLimit)
	}
	if !got.IsSystem {
		t.Error("default_user quota 仍应是 is_system=true")
	}
}

// ---------- 5. 第二次 bootstrap 不覆盖 default_user group 的 quota_profile_id ----------

func TestBlackbox_DefaultUserGroupQuotaProfileNotOverwritten(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, roleRepo, groupRepo, quotaRepo := freshRepos()

	// 创建非系统 quota + 绑到 default_user group
	quotaName := "quota_for_test_default_group"
	created, err := quotaRepo.CreateQuotaProfile(ctx, quotaName, "", int64Ptr(999), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateQuotaProfile: %v", err)
	}
	t.Cleanup(func() { cleanupQuota(t, quotaName) })

	defaultGroup, err := groupRepo.GetByName(ctx, group.NameDefaultUser)
	if err != nil {
		t.Fatalf("GetByName(default_user): %v", err)
	}
	if err := groupRepo.UpdateGroupQuotaProfile(ctx, defaultGroup.ID, &created.ID); err != nil {
		t.Fatalf("UpdateGroupQuotaProfile: %v", err)
	}

	// 跑第二次 bootstrap
	if err := bootstrap.NewAuthCatalog(dbtest.Pool()).Run(ctx); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}

	// 验证：quota_profile_id 仍应是 created.ID
	got, err := groupRepo.GetByName(ctx, group.NameDefaultUser)
	if err != nil {
		t.Fatalf("GetByName(default_user): %v", err)
	}
	if !got.QuotaProfileID.Valid || got.QuotaProfileID.Int64 != created.ID {
		t.Errorf("default_user group quota_profile_id 被重置: %v (want %d)", got.QuotaProfileID, created.ID)
	}
	if !got.IsSystem {
		t.Error("default_user group 仍应是 is_system=true")
	}

	_ = roleRepo
}

// ---------- 6. anonymous 始终 is_system=true, assignable=false ----------

func TestBlackbox_AnonymousSystemFlagsStable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, roleRepo, _, _ := freshRepos()

	// 直接 UPDATE 试图把 anonymous 改坏
	_, err := dbtest.Pool().Exec(ctx,
		"UPDATE roles SET is_system = FALSE, assignable = TRUE WHERE name = $1", role.NameAnonymous)
	if err != nil {
		t.Fatalf("人为破坏 anonymous: %v", err)
	}

	// 跑 bootstrap，identity 字段应该被修复
	if err := bootstrap.NewAuthCatalog(dbtest.Pool()).Run(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	anon, err := roleRepo.GetByName(ctx, role.NameAnonymous)
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if !anon.IsSystem {
		t.Error("anonymous 应被修复为 is_system=true")
	}
	if anon.Assignable {
		t.Error("anonymous 应被修复为 assignable=false")
	}
}

// ---------- 7. user role 始终 is_system=true, assignable=true ----------

func TestBlackbox_UserRoleSystemFlagsStable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, roleRepo, _, _ := freshRepos()

	_, err := dbtest.Pool().Exec(ctx,
		"UPDATE roles SET is_system = FALSE, assignable = FALSE WHERE name = $1", role.NameUser)
	if err != nil {
		t.Fatalf("人为破坏 user role: %v", err)
	}

	if err := bootstrap.NewAuthCatalog(dbtest.Pool()).Run(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	user, err := roleRepo.GetByName(ctx, role.NameUser)
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if !user.IsSystem {
		t.Error("user role 应被修复为 is_system=true")
	}
	if !user.Assignable {
		t.Error("user role 应被修复为 assignable=true")
	}
}

// ---------- 8. user_roles 和 group_roles 中不存在 assignable=false 的 role ----------

func TestBlackbox_NonAssignableRoleBindingsCleanedUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, roleRepo, groupRepo, _ := freshRepos()

	// 先确保 anonymous 的 default_user quota + group 都不受影响
	// 创建测试 user + group
	username := uniqueName(t, "u_clean")
	t.Cleanup(func() { cleanupUser(t, username) })
	if _, err := dbtest.Pool().Exec(ctx, "INSERT INTO users (username, password_hash) VALUES ($1, $2)", username, "hash"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var userID int64
	if err := dbtest.Pool().QueryRow(ctx, "SELECT id FROM users WHERE username = $1", username).Scan(&userID); err != nil {
		t.Fatalf("query user id: %v", err)
	}

	groupName := uniqueName(t, "g_clean")
	t.Cleanup(func() { cleanupGroup(t, groupName) })
	g, err := groupRepo.CreateGroup(ctx, groupName, "", nil, 0)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	// 直接 SQL 绕过业务校验，把 anonymous 塞进 user_roles / group_roles
	anon, err := roleRepo.GetByName(ctx, role.NameAnonymous)
	if err != nil {
		t.Fatalf("GetByName(anonymous): %v", err)
	}
	if _, err := dbtest.Pool().Exec(ctx,
		"INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2)", userID, anon.ID); err != nil {
		t.Fatalf("人为塞 user_role: %v", err)
	}
	if _, err := dbtest.Pool().Exec(ctx,
		"INSERT INTO group_roles (group_id, role_id) VALUES ($1, $2)", g.ID, anon.ID); err != nil {
		t.Fatalf("人为塞 group_role: %v", err)
	}

	// 跑 bootstrap
	if err := bootstrap.NewAuthCatalog(dbtest.Pool()).Run(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// 验证：两条脏关系都被清理
	var nUserRoles, nGroupRoles int
	if err := dbtest.Pool().QueryRow(ctx,
		"SELECT count(*) FROM user_roles WHERE user_id = $1 AND role_id = $2", userID, anon.ID).Scan(&nUserRoles); err != nil {
		t.Fatalf("count user_roles: %v", err)
	}
	if err := dbtest.Pool().QueryRow(ctx,
		"SELECT count(*) FROM group_roles WHERE group_id = $1 AND role_id = $2", g.ID, anon.ID).Scan(&nGroupRoles); err != nil {
		t.Fatalf("count group_roles: %v", err)
	}
	if nUserRoles != 0 {
		t.Errorf("user_roles 残留 anonymous 关联 %d 条", nUserRoles)
	}
	if nGroupRoles != 0 {
		t.Errorf("group_roles 残留 anonymous 关联 %d 条", nGroupRoles)
	}
}

func TestBlackbox_BootstrapIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = dbtest.Queries() // 保留引用以便未来用

	// 跑两次，第二次应不报错
	if err := bootstrap.NewAuthCatalog(dbtest.Pool()).Run(ctx); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	if err := bootstrap.NewAuthCatalog(dbtest.Pool()).Run(ctx); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
}

// ---------- 2. anonymous 存在且不可分配 ----------

func TestBlackbox_AnonymousNotAssignable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	userRepo, roleRepo, groupRepo, _ := freshRepos()

	anon, err := roleRepo.GetByName(ctx, role.NameAnonymous)
	if err != nil {
		t.Fatalf("GetByName(anonymous): %v", err)
	}
	if !anon.IsSystem {
		t.Error("anonymous 应是 is_system=true")
	}
	if anon.Assignable {
		t.Error("anonymous 应是 assignable=false")
	}

	// 创建一个普通 user
	username := uniqueName(t, "u_anon_reject")
	t.Cleanup(func() { cleanupUser(t, username) })

	u, err := userRepo.CreateUser(ctx, username, "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// 分配 anonymous 给 user：应被拒
	err = roleRepo.AssignRoleToUser(ctx, u.ID, anon.ID)
	if err != role.ErrRoleNotAssignable {
		t.Errorf("AssignRoleToUser(anonymous) 期望 ErrRoleNotAssignable，得到 %v", err)
	}

	// 分配 anonymous 给 default_user group：应被拒
	defaultGroup, err := groupRepo.GetByName(ctx, group.NameDefaultUser)
	if err != nil {
		t.Fatalf("GetByName(default_user): %v", err)
	}
	err = groupRepo.AssignRoleToGroup(ctx, defaultGroup.ID, anon.ID)
	if err != group.ErrRoleNotAssignable {
		t.Errorf("AssignRoleToGroup(anonymous) 期望 ErrRoleNotAssignable，得到 %v", err)
	}
}

// ---------- 3. 系统 role 权限允许修改 + bootstrap 不重置 ----------

func TestBlackbox_SystemRolePermissionNotResetByBootstrap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, roleRepo, _, _ := freshRepos()

	anon, err := roleRepo.GetByName(ctx, role.NameAnonymous)
	if err != nil {
		t.Fatalf("GetByName(anonymous): %v", err)
	}

	// 改 anonymous 权限：加 preview，删 login
	if err := roleRepo.GrantPermission(ctx, anon.ID, permission.Preview); err != nil {
		t.Fatalf("GrantPermission(preview): %v", err)
	}
	if err := roleRepo.RevokePermission(ctx, anon.ID, permission.Login); err != nil {
		t.Fatalf("RevokePermission(login): %v", err)
	}

	// 跑第二次 bootstrap
	if err := bootstrap.NewAuthCatalog(dbtest.Pool()).Run(ctx); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}

	// 验证：preview 应保留，login 应被剔除（说明 bootstrap 没重置）
	perms, err := roleRepo.ListAnonymousPermissions(ctx)
	if err != nil {
		t.Fatalf("ListAnonymousPermissions: %v", err)
	}
	hasPreview, hasLogin := false, false
	for _, p := range perms {
		if p == permission.Preview {
			hasPreview = true
		}
		if p == permission.Login {
			hasLogin = true
		}
	}
	if !hasPreview {
		t.Errorf("bootstrap 后 anonymous 应保留 preview，得到 %v", perms)
	}
	if hasLogin {
		t.Errorf("bootstrap 后 anonymous 不应包含 login（之前手动剔除），得到 %v", perms)
	}

	// 恢复：清理
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// 把 anonymous 权限恢复到默认（login 重新加、preview 移除）
		_, _ = dbtest.Pool().Exec(cleanupCtx, "DELETE FROM role_permissions WHERE role_id = $1 AND permission = $2", anon.ID, permission.Preview)
		_, _ = dbtest.Pool().Exec(cleanupCtx, "INSERT INTO role_permissions (role_id, permission) VALUES ($1, $2) ON CONFLICT DO NOTHING", anon.ID, permission.Login)
	})
}

// ---------- 4. 新用户默认加入 default_user 组 ----------

func TestBlackbox_NewUserJoinsDefaultUserGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	userRepo, _, _, _ := freshRepos()

	username := uniqueName(t, "u_new")
	t.Cleanup(func() { cleanupUser(t, username) })

	if _, err := userRepo.CreateUser(ctx, username, "hash"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	u, err := userRepo.GetByUsername(ctx, username)
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if len(u.GroupIDs()) < 1 {
		t.Errorf("新用户应至少属于 1 个 group，得到 %d", len(u.GroupIDs()))
	}
	// 基础权限验证
	if !u.HasPermission(permission.Login) {
		t.Error("新用户应通过 default_user group 继承 login")
	}
	if !u.HasPermission(permission.Upload) {
		t.Error("新用户应通过 default_user group 继承 upload")
	}
}

// ---------- 5. 用户个人 allow 生效 ----------

func TestBlackbox_UserAllowOverride(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	userRepo, _, _, _ := freshRepos()

	username := uniqueName(t, "u_allow")
	t.Cleanup(func() { cleanupUser(t, username) })

	if _, err := userRepo.CreateUser(ctx, username, "hash"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	u, err := userRepo.GetByUsername(ctx, username)
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if u.HasPermission(permission.DeleteAny) {
		t.Error("新用户不应有 delete_any（默认 user role 不含）")
	}

	// 加 allow
	if err := userRepo.SetPermissionOverride(ctx, u.ID, permission.DeleteAny, "allow", "test allow"); err != nil {
		t.Fatalf("SetPermissionOverride: %v", err)
	}

	// 重新加载
	u2, err := userRepo.GetByUsername(ctx, username)
	if err != nil {
		t.Fatalf("GetByUsername(after): %v", err)
	}
	if !u2.HasPermission(permission.DeleteAny) {
		t.Error("allow 覆盖后应有 delete_any")
	}
}

// ---------- 6. 用户个人 deny 优先级最高 ----------

func TestBlackbox_UserDenyOverride(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	userRepo, _, _, _ := freshRepos()

	username := uniqueName(t, "u_deny")
	t.Cleanup(func() { cleanupUser(t, username) })

	if _, err := userRepo.CreateUser(ctx, username, "hash"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	u, err := userRepo.GetByUsername(ctx, username)
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if !u.HasPermission(permission.Upload) {
		t.Fatal("基线：用户应有 upload")
	}

	// deny upload
	if err := userRepo.SetPermissionOverride(ctx, u.ID, permission.Upload, "deny", "test deny"); err != nil {
		t.Fatalf("SetPermissionOverride: %v", err)
	}
	u2, err := userRepo.GetByUsername(ctx, username)
	if err != nil {
		t.Fatalf("GetByUsername(after): %v", err)
	}
	if u2.HasPermission(permission.Upload) {
		t.Error("deny 覆盖后应没有 upload")
	}
}

// ---------- 7. quota 优先级 ----------

func TestBlackbox_QuotaPriority(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	userRepo, roleRepo, groupRepo, quotaRepo := freshRepos()

	// 准备三个 quota
	quotaUserOwn, err := quotaRepo.CreateQuotaProfile(ctx, uniqueName(t, "qp_own"), "", int64Ptr(100), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("create qp_own: %v", err)
	}
	t.Cleanup(func() { cleanupQuota(t, quotaUserOwn.Name) })

	quotaGroupHigh, err := quotaRepo.CreateQuotaProfile(ctx, uniqueName(t, "qp_high"), "", int64Ptr(200), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("create qp_high: %v", err)
	}
	t.Cleanup(func() { cleanupQuota(t, quotaGroupHigh.Name) })

	// group_high 绑 qp_high
	groupHigh, err := groupRepo.CreateGroup(ctx, uniqueName(t, "g_high"), "", &quotaGroupHigh.ID, 10)
	if err != nil {
		t.Fatalf("create g_high: %v", err)
	}
	t.Cleanup(func() { cleanupGroup(t, groupHigh.Name) })

	// 准备 user1：无 user quota，加入 high group → 应得 high
	user1 := uniqueName(t, "u1")
	t.Cleanup(func() { cleanupUser(t, user1) })
	if _, err := dbtest.Pool().Exec(ctx, "INSERT INTO users (username, password_hash) VALUES ($1, $2)", user1, "hash"); err != nil {
		t.Fatalf("insert u1: %v", err)
	}
	var u1ID int64
	if err := dbtest.Pool().QueryRow(ctx, "SELECT id FROM users WHERE username = $1", user1).Scan(&u1ID); err != nil {
		t.Fatalf("query u1 id: %v", err)
	}
	if err := groupRepo.AssignUserToGroup(ctx, u1ID, groupHigh.ID); err != nil {
		t.Fatalf("AssignUserToGroup: %v", err)
	}

	got, err := quotaRepo.GetEffectiveQuotaByUser(ctx, u1ID)
	if err != nil {
		t.Fatalf("GetEffectiveQuotaByUser(u1): %v", err)
	}
	if !got.StorageBytesLimit.Valid || got.StorageBytesLimit.Int64 != 200 {
		t.Errorf("u1 应得 high (200)，得到 %v", got.StorageBytesLimit)
	}

	// 准备 user2：设 user_own quota → 应得 own
	user2 := uniqueName(t, "u2")
	t.Cleanup(func() { cleanupUser(t, user2) })
	if _, err := dbtest.Pool().Exec(ctx, "INSERT INTO users (username, password_hash, quota_profile_id) VALUES ($1, $2, $3)", user2, "hash", quotaUserOwn.ID); err != nil {
		t.Fatalf("insert u2: %v", err)
	}
	var u2ID int64
	if err := dbtest.Pool().QueryRow(ctx, "SELECT id FROM users WHERE username = $1", user2).Scan(&u2ID); err != nil {
		t.Fatalf("query u2 id: %v", err)
	}

	got2, err := quotaRepo.GetEffectiveQuotaByUser(ctx, u2ID)
	if err != nil {
		t.Fatalf("GetEffectiveQuotaByUser(u2): %v", err)
	}
	if !got2.StorageBytesLimit.Valid || got2.StorageBytesLimit.Int64 != 100 {
		t.Errorf("u2 应得 own (100)，得到 %v", got2.StorageBytesLimit)
	}

	// 准备 user3：无 user quota、无 group quota → 应得 default_user
	user3 := uniqueName(t, "u3")
	t.Cleanup(func() { cleanupUser(t, user3) })
	if _, err := dbtest.Pool().Exec(ctx, "INSERT INTO users (username, password_hash) VALUES ($1, $2)", user3, "hash"); err != nil {
		t.Fatalf("insert u3: %v", err)
	}
	var u3ID int64
	if err := dbtest.Pool().QueryRow(ctx, "SELECT id FROM users WHERE username = $1", user3).Scan(&u3ID); err != nil {
		t.Fatalf("query u3 id: %v", err)
	}

	got3, err := quotaRepo.GetEffectiveQuotaByUser(ctx, u3ID)
	if err != nil {
		t.Fatalf("GetEffectiveQuotaByUser(u3): %v", err)
	}
	if got3.Name != quota.NameDefaultUser {
		t.Errorf("u3 应得 default_user，得到 %q", got3.Name)
	}

	// 不存在的 user_id
	if _, err := quotaRepo.GetEffectiveQuotaByUser(ctx, 99999999999); err == nil {
		t.Error("不存在的 user_id 应返回 error")
	} else if !strings.Contains(err.Error(), "不存在") && !strings.Contains(err.Error(), "no rows") {
		t.Logf("不存在的 user 错误信息：%v（可以接受）", err)
	}

	_ = userRepo
	_ = roleRepo
}

// ---------- 8. EnvSuperAdmin 行为 ----------

func TestBlackbox_SuperAdmin(t *testing.T) {
	origName := config.EnvSuperAdmin
	origHash := config.EnvSuperAdminPasswordHash
	t.Cleanup(func() {
		config.EnvSuperAdmin = origName
		config.EnvSuperAdminPasswordHash = origHash
	})

	username := uniqueName(t, "admin")
	config.EnvSuperAdmin = username
	config.EnvSuperAdminPasswordHash = "hash"

	if err := user.BootstrapSuperAdmin(context.Background(), dbtest.Pool()); err != nil {
		t.Fatalf("BootstrapSuperAdmin: %v", err)
	}
	t.Cleanup(func() { cleanupUser(t, username) })

	userRepo, _, _, _ := freshRepos()
	u, err := userRepo.GetByUsername(context.Background(), username)
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if !u.IsSuperAdmin() {
		t.Error("超管 IsSuperAdmin() 应为 true")
	}
	if !u.HasPermission("anything_random") {
		t.Error("超管 HasPermission(any) 应为 true")
	}
	if len(u.GroupIDs()) != 0 {
		t.Errorf("超管不应属于任何 group，得到 %d", len(u.GroupIDs()))
	}

	// 验证 user_roles / user_group_memberships 里没有超管
	var nRoles, nGrp int
	if err := dbtest.Pool().QueryRow(context.Background(),
		"SELECT count(*) FROM user_roles WHERE user_id = $1", u.ID).Scan(&nRoles); err != nil {
		t.Fatalf("count user_roles: %v", err)
	}
	if err := dbtest.Pool().QueryRow(context.Background(),
		"SELECT count(*) FROM user_group_memberships WHERE user_id = $1", u.ID).Scan(&nGrp); err != nil {
		t.Fatalf("count ugm: %v", err)
	}
	if nRoles != 0 {
		t.Errorf("超管不应有 user_roles 记录，得到 %d", nRoles)
	}
	if nGrp != 0 {
		t.Errorf("超管不应有 user_group_memberships 记录，得到 %d", nGrp)
	}
}

// sanity 引用：让编译器用上一些 import
var _ = sqlcgen.User{}
