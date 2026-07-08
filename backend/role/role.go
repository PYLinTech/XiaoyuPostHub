// Package role 定义角色（role）业务层：Role 类型、Repo。
//
// 角色语义：
//   is_system = true：
//     - name 不可改、role 行不可删
//     - permission 绑定**允许**通过配置面板修改（不强制只能走 PR 改 seed）
//   assignable = false：
//     - 不允许分配给真实用户
//     - 不允许分配给用户组
//     - anonymous 必须 assignable=false
//
// 核心不变量：
//   1. name = "super_admin" 是**代码层概念**（真超管走 .env 短路），
//      业务层 CreateRole 拒绝这个 name；数据层由 roles_no_super_admin CHECK 兜底。
//   2. 启动期 bootstrap 在单事务 + advisory lock 下，直接走 GetRoleByName +
//      CreateRole 创建缺失的系统 role，**不**依赖 bootstrap.sql 专用 query。
//   3. assigned_at 字段保留：审计表后续需要用它做追溯。
package role

// 系统 role 的 name 常量。NameSuperAdmin 故意**不导出**——
// 提醒维护者这个 name 不能出现在 roles 表里。
const (
	NameAnonymous = "anonymous" // 虚拟身份，assignable=false
	NameUser      = "user"      // 普通用户基线
)
