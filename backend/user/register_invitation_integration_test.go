//go:build integration

package user_test

// 邀请码与 guest 用户组的防护集成测试（真实 PostgreSQL，TestMain 由
// super_admin_test.go 提供）：
//   - 发码端拒绝 guest 组目标（admin.IssueInvitationCodes）；
//   - 注册端对历史遗留的 guest 组邀请码兜底：视为无效，不写入成员关系、
//     不消费邀请码、不创建账号；
//   - 回归：普通用户组邀请码仍可正常注册并加入对应组。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/admin"
	"github.com/PYLinTech/XiaoyuPostHub/backend/bootstrap"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/group"
	"github.com/PYLinTech/XiaoyuPostHub/backend/quota"
	"github.com/PYLinTech/XiaoyuPostHub/backend/randomtoken"
	"github.com/PYLinTech/XiaoyuPostHub/backend/systemsetting"
	"github.com/PYLinTech/XiaoyuPostHub/backend/test/dbtest"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
)

// insertInvitationCode 直接落库一条指向指定用户组的邀请码，返回明文码。
func insertInvitationCode(t *testing.T, issuerID, groupID int64) string {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.Pool()
	policy, err := user.NewRepo(pool, dbtest.Queries(), group.NewRepo(dbtest.Queries())).RegistrationPolicy(ctx)
	if err != nil {
		t.Fatalf("读取注册策略: %v", err)
	}
	code, err := randomtoken.NewCode(policy.CodeOptions)
	if err != nil {
		t.Fatalf("生成邀请码: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO invitation_codes(code_hash,code_prefix,issued_by_user_id,issued_to_group_id)
		VALUES($1,$2,$3,$4)`, randomtoken.Hash(code), code[:4], issuerID, groupID); err != nil {
		t.Fatalf("写入邀请码: %v", err)
	}
	return code
}

func TestGuestGroupInvitationIsRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := dbtest.Pool()
	q := dbtest.Queries()

	// Register 与发码都读取 system_settings 单行。
	if err := systemsetting.NewRepo(q, pool).EnsureDefaults(ctx); err != nil {
		t.Fatalf("初始化 system_settings: %v", err)
	}

	var guestGroupID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM user_groups WHERE name=$1`, quota.NameGuest).Scan(&guestGroupID); err != nil {
		t.Fatalf("读取 guest 用户组: %v", err)
	}
	issuer, err := q.CreateUser(ctx, sqlcgen.CreateUserParams{Username: "itest_issuer", PasswordHash: "integration-test"})
	if err != nil {
		t.Fatalf("创建发码账号: %v", err)
	}

	// 1) 发码端：guest 组不是合法邀请目标（此前可以发出，注册后即写入 guest 成员关系）。
	if _, err := admin.NewRepo(pool).IssueInvitationCodes(ctx, issuer.ID, "group", guestGroupID, 1, 0); !errors.Is(err, admin.ErrInvitationTargetInvalid) {
		t.Fatalf("guest 组邀请码应被拒绝，实际 err=%v", err)
	}

	// 2) 注册端兜底：模拟历史遗留的 guest 组邀请码，注册必须视为无效。
	code := insertInvitationCode(t, issuer.ID, guestGroupID)
	userRepo := user.NewRepo(pool, q, group.NewRepo(q))
	if _, err := userRepo.Register(ctx, "itest_newbie", "Str0ngPass!", code); !errors.Is(err, user.ErrInvitationInvalid) {
		t.Fatalf("guest 邀请码注册应返回 ErrInvitationInvalid，实际 err=%v", err)
	}
	var unused bool
	if err := pool.QueryRow(ctx, `SELECT used_at IS NULL FROM invitation_codes WHERE code_hash=$1`, randomtoken.Hash(code)).Scan(&unused); err != nil || !unused {
		t.Fatalf("guest 邀请码不应被消费：unused=%v err=%v", unused, err)
	}
	var memberCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_group_memberships WHERE group_id=$1`, guestGroupID).Scan(&memberCount); err != nil {
		t.Fatalf("统计 guest 组成员: %v", err)
	}
	if memberCount != 0 {
		t.Fatalf("guest 组不应有成员，实际 %d", memberCount)
	}
	var created int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE username=$1`, "itest_newbie").Scan(&created); err != nil || created != 0 {
		t.Fatalf("被拒注册不应创建账号：count=%d err=%v", created, err)
	}
}

// TestNormalGroupInvitationStillWorks 回归：普通用户组邀请码不受 guest 防护影响。
func TestNormalGroupInvitationStillWorks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := dbtest.Pool()
	q := dbtest.Queries()

	if err := bootstrap.NewAuthCatalog(pool).Run(ctx); err != nil {
		t.Fatalf("初始化默认用户组: %v", err)
	}
	if err := systemsetting.NewRepo(q, pool).EnsureDefaults(ctx); err != nil {
		t.Fatalf("初始化 system_settings: %v", err)
	}

	var groupID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO user_groups(name,is_system,description,quota_profile_id,priority)
		SELECT 'itest_normal_group',false,'集成测试用户组',id,0 FROM quota_profiles WHERE name=$1
		RETURNING id`, quota.NameDefaultUser).Scan(&groupID); err != nil {
		t.Fatalf("创建普通用户组: %v", err)
	}
	issuer, err := q.CreateUser(ctx, sqlcgen.CreateUserParams{Username: "itest_issuer2", PasswordHash: "integration-test"})
	if err != nil {
		t.Fatalf("创建发码账号: %v", err)
	}

	code := insertInvitationCode(t, issuer.ID, groupID)
	userRepo := user.NewRepo(pool, q, group.NewRepo(q))
	created, err := userRepo.Register(ctx, "itest_okuser", "Str0ngPass!", code)
	if err != nil {
		t.Fatalf("普通组邀请码注册失败: %v", err)
	}
	var invitedMembership int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM user_group_memberships WHERE user_id=$1 AND group_id=$2`, created.ID, groupID).Scan(&invitedMembership); err != nil || invitedMembership != 1 {
		t.Fatalf("注册后应加入邀请组：count=%d err=%v", invitedMembership, err)
	}
	var consumed bool
	if err := pool.QueryRow(ctx, `SELECT used_at IS NOT NULL FROM invitation_codes WHERE code_hash=$1`, randomtoken.Hash(code)).Scan(&consumed); err != nil || !consumed {
		t.Fatalf("邀请码应被消费：consumed=%v err=%v", consumed, err)
	}
}
