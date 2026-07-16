package user

import (
	"github.com/PYLinTech/XiaoyuPostHub/backend/config"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
)

// User 业务层用户类型，内嵌 sqlc 生成的 db.User（基础字段）+ 运行时状态。
//
// 字段语义：
//   - 基础字段（id/username/password_hash/created_at）：来自 users 表
//   - isSuperAdmin：true 表示该 user 是 .env 指定的真超管
//   - groupIDs：该 user 通过 user_group_memberships 关联的 group id 列表
//   - permissionSet：由所属用户组的 group_permissions 汇总
//
// **关键不变量**：
//   - IsSuperAdmin 的判定**仅**看 username == config.EnvSuperAdmin，
//     不依赖数据库中的用户组或 permissionSet。
//   - HasPermission 对超管短路返回 true，DB 操作**无法**影响真超管权限。
//   - permissionSet 永远只来自 ListEffectivePermissionsByUser 单条 SQL，
//     不存在"内存追加"之类的 hack。
type User struct {
	sqlcgen.User

	// 运行时状态，由 Repo.GetByUsername 在加载时填充。
	isSuperAdmin  bool
	groupIDs      []int64
	permissionSet map[string]bool
}

// IsSuperAdmin 判断是否为真超管。
// 唯一入口：**仅**看 username == config.EnvSuperAdmin。
//
// 真超管身份完全由 .env 决定，不依赖 DB 任何字段。
func (u User) IsSuperAdmin() bool {
	return u.isSuperAdmin
}

// HasPermission 判断 user 是否拥有指定 permission code。
//   - 真超管：永远 true（短路，**不查 DB**）
//   - 普通 user：查 permissionSet（构造时仅从用户组汇总）
//
// 这是业务层判断"用户能否发起动作"的唯一入口。
func (u User) HasPermission(code string) bool {
	if u.isSuperAdmin {
		return true
	}
	return u.permissionSet[code]
}

// GroupIDs 返回该 user 的实际 group id 列表（包括超管的 default_user）。
// 用于展示/审计，不参与权限判断。
func (u User) GroupIDs() []int64 {
	return u.groupIDs
}

// EnvSuperAdminName 返回当前超管用户名（来自 .env）。
// 业务层判断超管身份时的对照值。
func EnvSuperAdminName() string {
	return config.EnvSuperAdmin
}
