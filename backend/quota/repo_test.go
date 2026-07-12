package quota_test

// 白盒测试：quota.Repo 的业务规则。
// 重点：DeleteQuotaProfile 拒绝系统、UpdateQuotaProfile 允许系统、
// GetEffectiveQuotaByUser 不存在 user 时不返回 default。
// 连真 PG，缺 DB fail-fast。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/bootstrap"
	"github.com/PYLinTech/XiaoyuPostHub/backend/group"
	"github.com/PYLinTech/XiaoyuPostHub/backend/quota"
	"github.com/PYLinTech/XiaoyuPostHub/backend/role"
	"github.com/PYLinTech/XiaoyuPostHub/backend/test/dbtest"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
	"github.com/jackc/pgx/v5"
)

func TestMain(m *testing.M) {
	dbtest.SetupOrExit(m)
	// 启动期 seed：让系统数据就绪
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := bootstrap.NewAuthCatalog(dbtest.Pool()).Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "quota_test: bootstrap 失败: %v\n", err)
		os.Exit(1)
	}
	// 共享一个合法 bcrypt cost=12 测试哈希：
	//   - 直接 INSERT users.password_hash 时使用；
	//   - 避免后续 password 算法演进时硬编码字符串失效。
	// bcrypt 较慢（~250ms/次），TestMain 只生成一次。
	var err error
	testBcryptHash, err = user.HashPassword("test-password")
	if err != nil {
		fmt.Fprintf(os.Stderr, "quota_test: 生成 bcrypt 测试哈希失败: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	dbtest.Teardown()
	os.Exit(code)
}

// testBcryptHash 是 TestMain 启动时一次性生成的合法 bcrypt cost=12 哈希。
var testBcryptHash string

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

func TestCreateQuotaProfile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q := dbtest.Queries()
	r := quota.NewRepo(q)

	name := uniqueName(t, "qp")
	created, err := r.CreateQuotaProfile(ctx, name, "test", int64Ptr(100), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateQuotaProfile: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = dbtest.Pool().Exec(cleanupCtx, "DELETE FROM quota_profiles WHERE name = $1", name)
	})
	if created.IsSystem {
		t.Error("业务层创建的 quota profile 不应是 is_system=true")
	}
	if !created.StorageBytesLimit.Valid || created.StorageBytesLimit.Int64 != 100 {
		t.Errorf("StorageBytesLimit = %v, want 100", created.StorageBytesLimit)
	}
	if created.SingleFileBytesLimit.Valid {
		t.Errorf("SingleFileBytesLimit 应为 NULL（不限），得到 %v", created.SingleFileBytesLimit)
	}
}

func TestDeleteQuotaProfile_RejectsSystem(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q := dbtest.Queries()
	r := quota.NewRepo(q)

	defaultQP, err := r.GetByName(ctx, quota.NameDefaultUser)
	if err != nil {
		t.Fatalf("GetByName(default_user): %v", err)
	}
	if !defaultQP.IsSystem {
		t.Fatal("default_user 应是 is_system=true")
	}
	err = r.DeleteQuotaProfile(ctx, defaultQP.ID)
	if err != quota.ErrQuotaProfileIsSystem {
		t.Errorf("DeleteQuotaProfile(default_user) 期望 ErrQuotaProfileIsSystem，得到 %v", err)
	}
}

func TestUpdateQuotaProfile_AllowsSystem(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q := dbtest.Queries()
	r := quota.NewRepo(q)

	defaultQP, err := r.GetByName(ctx, quota.NameDefaultUser)
	if err != nil {
		t.Fatalf("GetByName(default_user): %v", err)
	}
	// 把 storage 改成 999，测完恢复
	originalStorage := defaultQP.StorageBytesLimit
	originalDescription := defaultQP.Description

	if err := r.UpdateQuotaProfile(ctx, defaultQP.ID, "test description", int64Ptr(999), nil, nil, nil, nil, nil); err != nil {
		t.Errorf("UpdateQuotaProfile(default_user) 应允许，得到 %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// 恢复
		_, _ = dbtest.Pool().Exec(cleanupCtx,
			"UPDATE quota_profiles SET description = $1, storage_bytes_limit = $2, updated_at = NOW() WHERE id = $3",
			originalDescription, originalStorage, defaultQP.ID)
	})

	got, err := r.GetByID(ctx, defaultQP.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !got.StorageBytesLimit.Valid || got.StorageBytesLimit.Int64 != 999 {
		t.Errorf("StorageBytesLimit = %v, want 999", got.StorageBytesLimit)
	}
}

func TestGetEffectiveQuotaByUser_NotExistUser(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q := dbtest.Queries()
	r := quota.NewRepo(q)

	// 不存在的 user_id（极大）
	_, err := r.GetEffectiveQuotaByUser(ctx, 99999999999)
	if err == nil {
		t.Error("GetEffectiveQuotaByUser(不存在 user) 期望 error，得到 nil")
	}
	if err != pgx.ErrNoRows {
		// 业务层包装后是 ErrQuotaProfileNotFound
		if errStr := err.Error(); errStr == "" {
			t.Errorf("error message 为空: %v", err)
		}
	}
}

// sanity 测试：user + group + quota 完整链路，验证优先级
func TestGetEffectiveQuotaByUser_PriorityUserOwn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q := dbtest.Queries()
	r := quota.NewRepo(q)
	roleRepo := role.NewRepo(q)
	gr := group.NewRepo(q, roleRepo)

	// 准备：
	//   - user_own quota (storage=100)
	//   - group_high quota (storage=200)
	//   - default_user quota (storage=NULL)
	quotaUserOwn, err := r.CreateQuotaProfile(ctx, uniqueName(t, "qp_own"), "", int64Ptr(100), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("create qp_own: %v", err)
	}
	quotaGroupHigh, err := r.CreateQuotaProfile(ctx, uniqueName(t, "qp_high"), "", int64Ptr(200), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("create qp_high: %v", err)
	}
	// 默认 quota 已由 bootstrap 创建

	// 准备 group
	groupHigh, err := gr.CreateGroup(ctx, uniqueName(t, "g_high"), "", &quotaGroupHigh.ID, 10)
	if err != nil {
		t.Fatalf("create g_high: %v", err)
	}

	// 准备 user，绑 high group + user_own quota
	username := uniqueName(t, "u")
	_, err = dbtest.Pool().Exec(ctx, "INSERT INTO users (username, password_hash, quota_profile_id) VALUES ($1, $2, $3)", username, testBcryptHash, quotaUserOwn.ID)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = dbtest.Pool().Exec(cleanupCtx, "DELETE FROM user_group_memberships WHERE user_id IN (SELECT id FROM users WHERE username = $1)", username)
		_, _ = dbtest.Pool().Exec(cleanupCtx, "DELETE FROM user_roles WHERE user_id IN (SELECT id FROM users WHERE username = $1)", username)
		_, _ = dbtest.Pool().Exec(cleanupCtx, "DELETE FROM users WHERE username = $1", username)
		_, _ = dbtest.Pool().Exec(cleanupCtx, "DELETE FROM user_group_memberships WHERE group_id = $1", groupHigh.ID)
		_, _ = dbtest.Pool().Exec(cleanupCtx, "DELETE FROM group_roles WHERE group_id = $1", groupHigh.ID)
		_, _ = dbtest.Pool().Exec(cleanupCtx, "DELETE FROM user_groups WHERE id = $1", groupHigh.ID)
		_, _ = dbtest.Pool().Exec(cleanupCtx, "DELETE FROM quota_profiles WHERE id IN ($1, $2)", quotaUserOwn.ID, quotaGroupHigh.ID)
	})

	var userID int64
	if err := dbtest.Pool().QueryRow(ctx, "SELECT id FROM users WHERE username = $1", username).Scan(&userID); err != nil {
		t.Fatalf("query user id: %v", err)
	}

	// 加入 group
	if err := gr.AssignUserToGroup(ctx, userID, groupHigh.ID); err != nil {
		t.Fatalf("AssignUserToGroup: %v", err)
	}

	// 查询：user_own (100) 应胜出（user 优先级最高）
	got, err := r.GetEffectiveQuotaByUser(ctx, userID)
	if err != nil {
		t.Fatalf("GetEffectiveQuotaByUser: %v", err)
	}
	if !got.StorageBytesLimit.Valid || got.StorageBytesLimit.Int64 != 100 {
		t.Errorf("期望 user_own (100)，得到 %v", got.StorageBytesLimit)
	}
	_ = fmt.Sprint(0) // 防止 import 警告
}
