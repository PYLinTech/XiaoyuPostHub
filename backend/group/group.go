// Package group 实现用户组（user_groups）业务层。
//
// 设计要点：
//   - 用户组权限通过 group_roles → role_permissions 获得，不存 group_permissions
//   - 优先级 priority 越高，匹配 quota 时越优先
//   - 系统用户组（is_system=true）不可删；业务层 DeleteUserGroup 拒绝
//   - 给用户组绑定 role 时，group.Repo 内部用 RoleReader 校验 role.Assignable
//     （避免 anonymous 落入用户组）
package group

// NameDefaultUser 系统默认用户组：所有新建用户自动加入。
// 通过此 group → user role 获得基础权限。
const NameDefaultUser = "default_user"
