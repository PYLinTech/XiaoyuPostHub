//go:build integration

// 集成测试：连真 PG，验证 ApplyEmbeddedSchema 连续执行不报错。
// 这才是"启动期 schema 幂等"的真正验证。
//
// 独立子包（test/db_applytest/）避免和 db 包的内部测试 import cycle。
// 共用 test/dbtest 的连接池。

package db_applytest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/db"
	"github.com/PYLinTech/XiaoyuPostHub/backend/test/dbtest"
)

func TestMain(m *testing.M) {
	dbtest.SetupOrExit(m)
	code := m.Run()
	dbtest.Teardown()
	os.Exit(code)
}

func TestApplyEmbeddedSchema_IdempotentSecondRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// TestMain 已经 apply 一次（reset + apply），这是第二次。
	if err := db.ApplyEmbeddedSchema(ctx, dbtest.Pool()); err != nil {
		t.Fatalf("ApplyEmbeddedSchema 第二次执行失败（schema 非幂等）：%v", err)
	}
}

func TestApplyEmbeddedSchema_IdempotentThirdRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.ApplyEmbeddedSchema(ctx, dbtest.Pool()); err != nil {
		t.Fatalf("ApplyEmbeddedSchema 第三次执行失败：%v", err)
	}
}

func TestApplyEmbeddedSchema_AllExpectedTablesExist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	expected := []string{
		"quota_profiles", "users",
		"user_groups", "user_group_memberships", "group_permissions",
	}
	for _, tname := range expected {
		var exists bool
		err := dbtest.Pool().QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)",
			tname).Scan(&exists)
		if err != nil {
			t.Fatalf("check %s: %v", tname, err)
		}
		if !exists {
			t.Errorf("表 %s 不存在", tname)
		}
	}
}

func TestApplyEmbeddedSchema_OldPermissionTablesAbsent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var exists bool
	for _, name := range []string{"permissions", "roles", "role_permissions", "user_roles", "group_roles", "user_permission_overrides"} {
		err := dbtest.Pool().QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name=$1)", name).Scan(&exists)
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Errorf("旧表 %s 不应存在", name)
		}
	}
}
