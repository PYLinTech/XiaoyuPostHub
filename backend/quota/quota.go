// Package quota 实现配额（quota_profiles）业务层。
//
// 设计要点：
//   - quota 字段语义：NULL = 不限，0 = 不允许，正数 = 限制值
//   - 系统 quota profile（is_system=true）不可删，但允许改数值
//   - 用户有效 quota 只从所属用户组中按 priority 选择
package quota

// NameDefaultUser 系统默认 quota profile：所有 user 的兜底。
const NameDefaultUser = "default_user"

// NameGuest 是未登录访客（guest）系统 quota profile / 用户组的名字。
// 未登录访问不匹配任何用户组成员身份，统一按 guest 组绑定的方案限流，每个 IP
// 视为一个独立用户（见 server 层的下载配额校验）。
const NameGuest = "guest"
