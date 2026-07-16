// Package group 实现用户组（user_groups）业务层。
//
// 设计要点：
//   - 用户组权限直接保存在 group_permissions
//   - 优先级 priority 越高，匹配 quota 时越优先
//   - 系统用户组（is_system=true）不可删；业务层 DeleteUserGroup 拒绝
package group

// NameDefaultUser 系统默认用户组：所有新建用户自动加入。
// 新用户自动加入此组并继承基础权限和默认配额。
const NameDefaultUser = "default_user"
