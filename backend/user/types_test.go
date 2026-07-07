package user

// 白盒测试:直接测未导出的纯函数 appendUnique / removeAll,
// 以及 User 的业务方法 IsSuperAdmin。
// 这些都不需要 DB,跑得很快,永久存在。

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

// --- removeAll ---

func TestRemoveAll_RemovesAll(t *testing.T) {
	got := removeAll([]string{"user", "all"})
	want := []string{"user"}
	if !slices.Equal(got, want) {
		t.Errorf("removeAll([user,all]) = %v, want %v", got, want)
	}
}

func TestRemoveAll_NoopWhenNoAll(t *testing.T) {
	in := []string{"user"}
	got := removeAll(in)
	if !slices.Equal(got, in) {
		t.Errorf("removeAll([user]) = %v, want %v", got, in)
	}
}

func TestRemoveAll_RemovesMultipleAll(t *testing.T) {
	got := removeAll([]string{"all", "user", "all"})
	want := []string{"user"}
	if !slices.Equal(got, want) {
		t.Errorf("removeAll([all,user,all]) = %v, want %v", got, want)
	}
}

func TestRemoveAll_EmptyStart(t *testing.T) {
	got := removeAll(nil)
	if len(got) != 0 {
		t.Errorf("removeAll(nil) = %v, want empty", got)
	}
}

// --- User.IsSuperAdmin ---

func TestUser_IsSuperAdmin(t *testing.T) {
	cases := []struct {
		name   string
		groups []string
		want   bool
	}{
		{"has all", []string{"user", "all"}, true},
		{"only all", []string{"all"}, true},
		{"only user", []string{"user"}, false},
		{"empty", []string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := User{User: sqlcgen.User{Groups: tc.groups}}
			if got := u.IsSuperAdmin(); got != tc.want {
				t.Errorf("IsSuperAdmin(%v) = %v, want %v", tc.groups, got, tc.want)
			}
		})
	}
}

// --- User.IsActive ---
// 已删除:语义错误('user' 是身份不是状态),命名误导 IsActive,无真实业务效用。
// 未来若需"判断是否在 user 组",应命名 HasUserIdentity 或类似,而不是 IsActive。