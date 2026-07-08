package user

import (
	"slices"

	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
)

// User 业务层用户类型，内嵌 sqlc 生成的 db.User。
// 这样既能直接访问所有字段，也能加自己的方法。
//
// 字段语义说明：
//   - Roles：权限组。固定枚举（user / all），数据库 CHECK + 代码双重兜底。
//     'all' 是超管标记，永远不持久化落库——超管身份仅由 .env 的 EnvSuperAdmin 单点决定。
//   - Groups：用户组。完全自由搭配，规划用于配额管理（VIP/SVIP 等），
//     目前未实现业务逻辑，仅作为预留字段透传。
type User struct {
	sqlcgen.User
}

// IsSuperAdmin 判断用户是否拥有 'all' 权限。
// 这是业务层判断超管的唯一入口，仅读 Roles。
func (u User) IsSuperAdmin() bool {
	return slices.Contains(u.Roles, "all")
}

// appendUnique 追加元素，已存在则不变（幂等）。
// roles / groups 操作都能复用，通用工具。
func appendUnique(roles []string, r string) []string {
	for _, x := range roles {
		if x == r {
			return roles
		}
	}
	return append(roles, r)
}

// removeRoleAll 从 roles 数组里移除所有 'all'。
// 命名 'removeRoleAll' 而非 'removeAll'，明确 'all' 仅属于 roles 语义，
// 避免与 groups 字段的 "删除所有元素" 产生字面歧义。
func removeRoleAll(roles []string) []string {
	out := make([]string, 0, len(roles))
	for _, x := range roles {
		if x != "all" {
			out = append(out, x)
		}
	}
	return out
}
