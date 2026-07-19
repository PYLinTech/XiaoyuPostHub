//go:build integration

package user_test

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/bootstrap"
	"github.com/PYLinTech/XiaoyuPostHub/backend/config"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/group"
	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/quota"
	"github.com/PYLinTech/XiaoyuPostHub/backend/test/dbtest"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

func TestMain(m *testing.M) {
	dbtest.SetupOrExit(m)
	code := m.Run()
	dbtest.Teardown()
	os.Exit(code)
}

func TestBootstrapSuperAdminMatchesDefaultGroupPermissionsAndQuota(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := bootstrap.NewAuthCatalog(dbtest.Pool()).Run(ctx); err != nil {
		t.Fatalf("bootstrap auth catalog: %v", err)
	}

	q := dbtest.Queries()
	defaultQuota, err := q.GetQuotaProfileByName(ctx, quota.NameDefaultUser)
	if err != nil {
		t.Fatalf("get default quota: %v", err)
	}
	defaultGroup, err := q.GetUserGroupByName(ctx, group.NameDefaultUser)
	if err != nil {
		t.Fatalf("get default group: %v", err)
	}

	// 超级管理员账号始终以默认用户组为唯一所属组。
	otherGroup, err := q.CreateUserGroup(ctx, sqlcgen.CreateUserGroupParams{
		Name: "other_admin_group", QuotaProfileID: defaultQuota.ID, Priority: 100,
	})
	if err != nil {
		t.Fatalf("create other group: %v", err)
	}
	oldHash, err := user.HashPassword("old-admin-password")
	if err != nil {
		t.Fatalf("hash old password: %v", err)
	}
	dbUser, err := q.CreateUser(ctx, sqlcgen.CreateUserParams{
		Username: "root_admin", PasswordHash: oldHash,
	})
	if err != nil {
		t.Fatalf("create existing admin: %v", err)
	}
	if _, err := q.AssignUserToGroup(ctx, sqlcgen.AssignUserToGroupParams{
		UserID: dbUser.ID, GroupID: otherGroup.ID,
	}); err != nil {
		t.Fatalf("assign other group: %v", err)
	}

	configuredHash, err := user.HashPassword("configured-admin-password")
	if err != nil {
		t.Fatalf("hash configured password: %v", err)
	}
	config.EnvSuperAdmin = dbUser.Username
	config.EnvSuperAdminPasswordHash = configuredHash

	// 连续执行两次，验证启动同步可以安全重入且不会丢失默认组。
	for i := 0; i < 2; i++ {
		if err := user.BootstrapSuperAdmin(ctx, dbtest.Pool()); err != nil {
			t.Fatalf("bootstrap super admin run %d: %v", i+1, err)
		}
	}

	groupIDs, err := q.ListGroupIDsByUser(ctx, dbUser.ID)
	if err != nil {
		t.Fatalf("list admin groups: %v", err)
	}
	if !slices.Equal(groupIDs, []int64{defaultGroup.ID}) {
		t.Fatalf("admin groups = %v, want only default group %d", groupIDs, defaultGroup.ID)
	}

	permissions, err := q.ListEffectivePermissionsByUser(ctx, dbUser.ID)
	if err != nil {
		t.Fatalf("list admin group permissions: %v", err)
	}
	if !slices.Contains(permissions, permission.Login) || !slices.Contains(permissions, permission.Upload) {
		t.Fatalf("admin default-group permissions missing: %v", permissions)
	}

	quotaRepo := quota.NewRepo(q)
	effectiveQuota, err := quotaRepo.GetEffectiveQuotaByUser(ctx, dbUser.ID)
	if err != nil {
		t.Fatalf("get admin effective quota: %v", err)
	}
	if effectiveQuota.ID != defaultQuota.ID {
		t.Fatalf("admin quota = %d, want default quota %d", effectiveQuota.ID, defaultQuota.ID)
	}

	groupRepo := group.NewRepo(q)
	userRepo := user.NewRepo(dbtest.Pool(), q, groupRepo)
	hydrated, err := userRepo.GetByID(ctx, dbUser.ID)
	if err != nil {
		t.Fatalf("hydrate admin: %v", err)
	}
	if !hydrated.IsSuperAdmin() {
		t.Fatal("hydrated user is not super admin")
	}
	if !slices.Equal(hydrated.GroupIDs(), []int64{defaultGroup.ID}) {
		t.Fatalf("hydrated admin groups = %v, want %d", hydrated.GroupIDs(), defaultGroup.ID)
	}
	if !hydrated.HasPermission(permission.ManageUsers) {
		t.Fatal("super admin lost its all-permissions override")
	}

	// 新创建的超级管理员同样直接绑定默认组。
	newHash, err := user.HashPassword("new-admin-password")
	if err != nil {
		t.Fatalf("hash new admin password: %v", err)
	}
	config.EnvSuperAdmin = "new_root_admin"
	config.EnvSuperAdminPasswordHash = newHash
	if err := user.BootstrapSuperAdmin(ctx, dbtest.Pool()); err != nil {
		t.Fatalf("bootstrap new super admin: %v", err)
	}
	newAdmin, err := q.GetUserByUsername(ctx, config.EnvSuperAdmin)
	if err != nil {
		t.Fatalf("get new super admin: %v", err)
	}
	newAdminGroupIDs, err := q.ListGroupIDsByUser(ctx, newAdmin.ID)
	if err != nil {
		t.Fatalf("list new admin groups: %v", err)
	}
	if !slices.Equal(newAdminGroupIDs, []int64{defaultGroup.ID}) {
		t.Fatalf("new admin groups = %v, want %d", newAdminGroupIDs, defaultGroup.ID)
	}
}
