package user

// 白盒测试：直接测未导出的纯函数 appendUnique / removeRoleAll，
// 以及 User 的业务方法 IsSuperAdmin。
// 这些都不需要 DB，跑得很快，永久存在。

import (
	"slices"
	"testing"

	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
)

// --- appendUnique ---

func TestAppendUnique_AddsNewElement(t *testing.T) {
	got := appendUnique([]string{"user"}, "all")
	want := []string{"user", "all"}
	if !slices.Equal(got, want) {
		t.Errorf("appendUnique([user], all) = %v, want %v", got, want)
	}
}

func TestAppendUnique_NoopWhenExisting(t *testing.T) {
	in := []string{"user", "all"}
	got := appendUnique(in, "all")
	if !slices.Equal(got, in) {
		t.Errorf("appendUnique([user,all], all) = %v, want %v", got, in)
	}
}

func TestAppendUnique_EmptyStart(t *testing.T) {
	got := appendUnique(nil, "user")
	want := []string{"user"}
	if !slices.Equal(got, want) {
		t.Errorf("appendUnique(nil, user) = %v, want %v", got, want)
	}
}

// --- removeRoleAll ---

func TestRemoveRoleAll_RemovesAll(t *testing.T) {
	got := removeRoleAll([]string{"user", "all"})
	want := []string{"user"}
	if !slices.Equal(got, want) {
		t.Errorf("removeRoleAll([user,all]) = %v, want %v", got, want)
	}
}

func TestRemoveRoleAll_NoopWhenNoAll(t *testing.T) {
	in := []string{"user"}
	got := removeRoleAll(in)
	if !slices.Equal(got, in) {
		t.Errorf("removeRoleAll([user]) = %v, want %v", got, in)
	}
}

func TestRemoveRoleAll_RemovesMultipleAll(t *testing.T) {
	got := removeRoleAll([]string{"all", "user", "all"})
	want := []string{"user"}
	if !slices.Equal(got, want) {
		t.Errorf("removeRoleAll([all,user,all]) = %v, want %v", got, want)
	}
}

func TestRemoveRoleAll_EmptyStart(t *testing.T) {
	got := removeRoleAll(nil)
	if len(got) != 0 {
		t.Errorf("removeRoleAll(nil) = %v, want empty", got)
	}
}

// --- User.IsSuperAdmin（仅读 Roles） ---

func TestUser_IsSuperAdmin(t *testing.T) {
	cases := []struct {
		name  string
		roles []string
		want  bool
	}{
		{"has all", []string{"user", "all"}, true},
		{"only all", []string{"all"}, true},
		{"only user", []string{"user"}, false},
		{"empty", []string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := User{User: sqlcgen.User{Roles: tc.roles}}
			if got := u.IsSuperAdmin(); got != tc.want {
				t.Errorf("IsSuperAdmin(%v) = %v, want %v", tc.roles, got, tc.want)
			}
		})
	}
}

// --- IsActive 已删除 ---
// 语义错误（'user' 是身份不是状态），命名误导 IsActive，无真实业务效用。
// 未来若需"判断是否在 user 组"，应命名 HasUserIdentity 或类似，而不是 IsActive。
