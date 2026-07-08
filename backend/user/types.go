package user

import (
	"github.com/PYLinTech/XiaoyuPostHub/backend/config"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
)

// User 业务层用户类型，内嵌 sqlc 生成的 db.User（基础字段）+ 运行时状态。
//
// 字段语义：
//   - 基础字段（id/username/password_hash/quota_profile_id/created_at）：来自 users 表
//   - quota_profile_id：用户专属 quota，为空时使用 group / default_user 的 quota
//   - isSuperAdmin：true 表示该 user 是 .env 指定的真超管
//   - groupIDs：该 user 通过 user_group_memberships 关联的 group id 列表（超管为空）
//   - permissionSet：合并自 role → role_permissions + group 继承 + 用户个人 allow/deny
//
// **关键不变量**：
//   - IsSuperAdmin 的判定**仅**看 username == config.EnvSuperAdmin，
//     不读 DB、不读 roleIDs、不读 permissionSet。
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
//   - 普通 user：查 permissionSet（构造时已合并自 user_roles + group_roles + override）
//
// 这是业务层判断"用户能否发起动作"的唯一入口。
func (u User) HasPermission(code string) bool {
	if u.isSuperAdmin {
		return true
	}
	return u.permissionSet[code]
}

// GroupIDs 返回该 user 的 group id 列表（超管返回空切片）。
// 用于展示/审计，不参与权限判断。
func (u User) GroupIDs() []int64 {
	if u.isSuperAdmin {
		return nil
	}
	return u.groupIDs
}

// EnvSuperAdminName 返回当前超管用户名（来自 .env）。
// 业务层判断超管身份时的对照值。
func EnvSuperAdminName() string {
	return config.EnvSuperAdmin
}
