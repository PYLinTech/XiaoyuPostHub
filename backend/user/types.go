package user

import (
	"slices"

	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
)

// User 业务层用户类型,内嵌 sqlc 生成的 db.User。
// 这样既能直接访问所有字段,也能加自己的方法。
type User struct {
	sqlcgen.User
}

// IsSuperAdmin 判断用户是否拥有 'all' 权限。
// 这是业务层判断超管的唯一入口。
func (u User) IsSuperAdmin() bool {
	return slices.Contains(u.Groups, "all")
}

// appendUnique 追加元素,已存在则不变(幂等)。
func appendUnique(groups []string, g string) []string {
	for _, x := range groups {
		if x == g {
			return groups
		}
	}
	return append(groups, g)
}

// removeAll 从 groups 数组里移除所有 'all'。
func removeAll(groups []string) []string {
	out := make([]string, 0, len(groups))
	for _, x := range groups {
		if x != "all" {
			out = append(out, x)
		}
	}
	return out
}