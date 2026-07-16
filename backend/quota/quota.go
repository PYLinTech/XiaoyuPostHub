// Package quota 实现配额（quota_profiles）业务层。
//
// 设计要点：
//   - quota 字段语义：NULL = 不限，0 = 不允许，正数 = 限制值
//   - 系统 quota profile（is_system=true）不可删，但允许改数值
//   - 用户有效 quota 只从所属用户组中按 priority 选择
package quota

// NameDefaultUser 系统默认 quota profile：所有 user 的兜底。
const NameDefaultUser = "default_user"
