package user_test

// 集成测试：Repo.GetByUsername 和 Repo.CreateUser。
// 共享 auth_test.go 的 testPool、setEnvSuperAdmin、uniqueUsername 等 helper。

import (
	"context"
	"testing"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

// --- Repo.GetByUsername：普通用户不追加 'all' ---

func TestRepo_GetByUsername_NotEnvSuperAdmin(t *testing.T) {
	name := uniqueUsername(t, "get_not_admin")
	insertUser(t, name, "sha256:salt:hash", []string{"user"}, []string{})
	t.Cleanup(func() { cleanupUser(t, name) })

	// EnvSuperAdmin 设成"库里不存在的另一个名字"，保证 name != EnvSuperAdmin
	setEnvSuperAdmin(t, "env_admin_"+uniqueUsername(t, "x"), "irrelevant")

	q := sqlcgen.New(testPool)
	r := user.NewRepo(q)
	u, err := r.GetByUsername(context.Background(), name)
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if u.IsSuperAdmin() {
		t.Error("普通用户 IsSuperAdmin() 应为 false")
	}
	// 验证库里持久化的 roles 也没被改
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dbU, err := q.GetUserByUsername(ctx, name)
	if err != nil {
		t.Fatalf("db verify GetUserByUsername: %v", err)
	}
	if contains(dbU.Roles, "all") {
		t.Errorf("库里 roles = %v, 不应含 'all'", dbU.Roles)
	}
}

// --- Repo.GetByUsername：EnvSuperAdmin 用户临时追加 'all' 到 Roles ---

func TestRepo_GetByUsername_IsEnvSuperAdmin(t *testing.T) {
	name := uniqueUsername(t, "get_is_admin")
	insertUser(t, name, "sha256:salt:hash_admin", []string{"user"}, []string{})
	t.Cleanup(func() { cleanupUser(t, name) })
	setEnvSuperAdmin(t, name, "sha256:salt:hash_admin")

	q := sqlcgen.New(testPool)
	r := user.NewRepo(q)
	u, err := r.GetByUsername(context.Background(), name)
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if !u.IsSuperAdmin() {
		t.Error("EnvSuperAdmin 用户 IsSuperAdmin() 应为 true（Roles 临时附加）")
	}
	// 关键：验证库里的 roles 没被持久化改写
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dbU, err := q.GetUserByUsername(ctx, name)
	if err != nil {
		t.Fatalf("db verify GetUserByUsername: %v", err)
	}
	if contains(dbU.Roles, "all") {
		t.Errorf("库里 roles = %v, Roles 临时附加 'all' 不能持久化", dbU.Roles)
	}
}

// --- Repo.CreateUser：拒绝 EnvSuperAdmin 同名 ---

func TestRepo_CreateUser_RejectsEnvSuperAdminName(t *testing.T) {
	name := uniqueUsername(t, "create_rejected")
	setEnvSuperAdmin(t, name, "sha256:salt:hash")
	t.Cleanup(func() { cleanupUser(t, name) })

	q := sqlcgen.New(testPool)
	r := user.NewRepo(q)
	_, err := r.CreateUser(context.Background(), name, "sha256:salt:hash",
		[]string{"user"}, []string{})
	if err == nil {
		t.Error("CreateUser 应拒绝 EnvSuperAdmin 同名账号")
	}
	// 兜底验证库没被写入
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = q.GetUserByUsername(ctx, name)
	if err == nil {
		t.Error("库里不该有这个用户")
	}
}

// --- Repo.CreateUser：自动从 roles 剔除 'all' ---

func TestRepo_CreateUser_StripsAllFromRoles(t *testing.T) {
	name := uniqueUsername(t, "create_strip_all")
	setEnvSuperAdmin(t, "env_admin_"+uniqueUsername(t, "x"), "irrelevant")
	t.Cleanup(func() { cleanupUser(t, name) })

	q := sqlcgen.New(testPool)
	r := user.NewRepo(q)
	_, err := r.CreateUser(context.Background(), name, "sha256:salt:hash",
		[]string{"user", "all"}, // ← 故意在 roles 里传 'all'
		[]string{"vip"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := q.GetUserByUsername(ctx, name)
	if err != nil {
		t.Fatalf("verify GetUserByUsername: %v", err)
	}
	if contains(got.Roles, "all") {
		t.Errorf("库里 roles = %v, 'all' 应被剔除", got.Roles)
	}
	if !contains(got.Roles, "user") {
		t.Errorf("库里 roles = %v, 'user' 应保留", got.Roles)
	}
	// groups 业务层零干预，调用方传啥就是啥
	if !contains(got.Groups, "vip") {
		t.Errorf("库里 groups = %v, 应透传 'vip'", got.Groups)
	}
}

// --- Repo.CreateUser：强制 roles 加入 'user' ---

func TestRepo_CreateUser_ForcesUserRole(t *testing.T) {
	name := uniqueUsername(t, "create_force_user")
	setEnvSuperAdmin(t, "env_admin_"+uniqueUsername(t, "x"), "irrelevant")
	t.Cleanup(func() { cleanupUser(t, name) })

	q := sqlcgen.New(testPool)
	r := user.NewRepo(q)
	// 故意 roles 不传 'user'，groups 也不传
	_, err := r.CreateUser(context.Background(), name, "sha256:salt:hash",
		[]string{}, []string{})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := q.GetUserByUsername(ctx, name)
	if err != nil {
		t.Fatalf("verify GetUserByUsername: %v", err)
	}
	if !contains(got.Roles, "user") {
		t.Errorf("库里 roles = %v, 'user' 应被强制加入", got.Roles)
	}
	if len(got.Groups) != 0 {
		t.Errorf("库里 groups = %v, want empty", got.Groups)
	}
}
