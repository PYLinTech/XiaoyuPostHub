package user_test

// 集成测试:Repo.GetByUsername 和 Repo.CreateUser。
// 共享 auth_test.go 的 testPool、setEnvSuperAdmin、uniqueUsername 等 helper。

import (
	"context"
	"testing"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

// --- Repo.GetByUsername:普通用户不追加 'all' ---

func TestRepo_GetByUsername_NotEnvSuperAdmin(t *testing.T) {
	name := uniqueUsername(t, "get_not_admin")
	insertUser(t, name, "sha256:salt:hash", []string{"user"})
	t.Cleanup(func() { cleanupUser(t, name) })

	// EnvSuperAdmin 设成"库里不存在的另一个名字",保证 name != EnvSuperAdmin
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
	// 验证库里持久化的 groups 也没被改
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dbU, err := q.GetUserByUsername(ctx, name)
	if err != nil {
		t.Fatalf("db verify GetUserByUsername: %v", err)
	}
	if contains(dbU.Groups, "all") {
		t.Errorf("库里 groups = %v, 不应含 'all'", dbU.Groups)
	}
}

// --- Repo.GetByUsername:EnvSuperAdmin 用户临时追加 'all' ---

func TestRepo_GetByUsername_IsEnvSuperAdmin(t *testing.T) {
	name := uniqueUsername(t, "get_is_admin")
	insertUser(t, name, "sha256:salt:hash_admin", []string{"user"})
	t.Cleanup(func() { cleanupUser(t, name) })
	setEnvSuperAdmin(t, name, "sha256:salt:hash_admin")

	q := sqlcgen.New(testPool)
	r := user.NewRepo(q)
	u, err := r.GetByUsername(context.Background(), name)
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if !u.IsSuperAdmin() {
		t.Error("EnvSuperAdmin 用户 IsSuperAdmin() 应为 true(临时附加)")
	}
	// 关键:验证库里的 groups 没被持久化改写
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dbU, err := q.GetUserByUsername(ctx, name)
	if err != nil {
		t.Fatalf("db verify GetUserByUsername: %v", err)
	}
	if contains(dbU.Groups, "all") {
		t.Errorf("库里 groups = %v, 临时附加 'all' 不能持久化", dbU.Groups)
	}
}

// --- Repo.CreateUser:拒绝 EnvSuperAdmin 同名 ---

func TestRepo_CreateUser_RejectsEnvSuperAdminName(t *testing.T) {
	name := uniqueUsername(t, "create_rejected")
	setEnvSuperAdmin(t, name, "sha256:salt:hash")
	t.Cleanup(func() { cleanupUser(t, name) })

	q := sqlcgen.New(testPool)
	r := user.NewRepo(q)
	_, err := r.CreateUser(context.Background(), name, "sha256:salt:hash", []string{"user"})
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

// --- Repo.CreateUser:自动剔除 'all' ---

func TestRepo_CreateUser_StripsAll(t *testing.T) {
	name := uniqueUsername(t, "create_strip_all")
	setEnvSuperAdmin(t, "env_admin_"+uniqueUsername(t, "x"), "irrelevant")
	t.Cleanup(func() { cleanupUser(t, name) })

	q := sqlcgen.New(testPool)
	r := user.NewRepo(q)
	_, err := r.CreateUser(context.Background(), name, "sha256:salt:hash",
		[]string{"user", "all"}) // ← 故意传 'all'
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := q.GetUserByUsername(ctx, name)
	if err != nil {
		t.Fatalf("verify GetUserByUsername: %v", err)
	}
	if contains(got.Groups, "all") {
		t.Errorf("库里 groups = %v, 'all' 应被剔除", got.Groups)
	}
	if !contains(got.Groups, "user") {
		t.Errorf("库里 groups = %v, 'user' 应保留", got.Groups)
	}
}

// --- Repo.CreateUser:自动加 'user' ---

func TestRepo_CreateUser_ForcesUser(t *testing.T) {
	name := uniqueUsername(t, "create_force_user")
	setEnvSuperAdmin(t, "env_admin_"+uniqueUsername(t, "x"), "irrelevant")
	t.Cleanup(func() { cleanupUser(t, name) })

	q := sqlcgen.New(testPool)
	r := user.NewRepo(q)
	// 故意不传 'user'
	_, err := r.CreateUser(context.Background(), name, "sha256:salt:hash", []string{})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := q.GetUserByUsername(ctx, name)
	if err != nil {
		t.Fatalf("verify GetUserByUsername: %v", err)
	}
	if !contains(got.Groups, "user") {
		t.Errorf("库里 groups = %v, 'user' 应被强制加入", got.Groups)
	}
}