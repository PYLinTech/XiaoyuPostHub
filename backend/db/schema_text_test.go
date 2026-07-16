package db

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func schemaDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位 schema 测试")
	}
	return filepath.Join(filepath.Dir(file), "schema")
}

func schemaText(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(schemaDir(t), name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestSchema_UserHasNoDirectPermissionOrQuotaFields(t *testing.T) {
	body := schemaText(t, "001_users.sql")
	if strings.Contains(body, "quota_profile_id") {
		t.Error("users 不应保存用户专属配额")
	}
	groups := schemaText(t, "003_groups_quotas.sql")
	for _, required := range []string{"user_group_memberships", "group_permissions", "quota_profile_id"} {
		if !strings.Contains(groups, required) {
			t.Errorf("用户组 schema 缺少 %s", required)
		}
	}
}

func TestSchema_OldPermissionModelRemoved(t *testing.T) {
	all := ""
	entries, err := os.ReadDir(schemaDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") {
			all += schemaText(t, entry.Name())
		}
	}
	for _, removed := range []string{"CREATE TABLE IF NOT EXISTS permissions", "CREATE TABLE IF NOT EXISTS roles", "role_permissions", "user_roles", "group_roles", "user_permission_overrides"} {
		if strings.Contains(all, removed) {
			t.Errorf("旧权限结构仍存在: %s", removed)
		}
	}
}

func TestSchema_SystemSettingsSingleton(t *testing.T) {
	body := schemaText(t, "005_system_settings.sql")
	if !strings.Contains(body, "system_settings_singleton CHECK (id = 1)") {
		t.Error("system_settings 必须保持单例")
	}
	for _, field := range []string{"folder_pack_mode", "registration_requires_invitation", "invitation_length", "upload_requires_review"} {
		if !strings.Contains(body, field) {
			t.Errorf("system_settings 缺少统一配置字段 %s", field)
		}
	}
}

func TestSchema_NormalizedTransientState(t *testing.T) {
	users := schemaText(t, "001_users.sql")
	for _, removed := range []string{"read_message_ids", "deleted_message_ids"} {
		if strings.Contains(users, removed) {
			t.Errorf("users 仍保存消息状态数组 %s", removed)
		}
	}
	sessions := schemaText(t, "004_user_sessions.sql")
	if !strings.Contains(sessions, "login_failure_events") {
		t.Error("登录失败应使用单事件表")
	}
}
