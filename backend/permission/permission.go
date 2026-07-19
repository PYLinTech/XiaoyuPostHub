// Package permission 定义系统所有的原子权限（permission code），
// 权限 code 的权威来源仅在此处，不再在数据库维护重复目录。
//
// 设计要点：
//
//   - Permission 是**原子动作**，用户组直接组合这些权限。
//   - 所有 permission code + 展示文案集中在 Definitions；业务代码禁止散落字符串。
//   - All 是从 Definitions 派生的纯 code 列表，避免两套手写不同步。
//   - Repo 仅做只读查询；写入只在启动 seed（bootstrap.AuthCatalog）发生。
package permission

// 系统支持的 permission code。
const (
	// 认证
	Login = "login" // 登录系统

	// 资源（自己名下）
	Upload    = "upload"     // 上传
	Download  = "download"   // 下载（资源管理界面内）
	Preview   = "preview"    // 预览
	Rename    = "rename"     // 重命名
	DeleteOwn = "delete_own" // 删自己的资源

	// 分享/直链（卡的是"创建动作"，不是"我能不能下载"）
	Share      = "share"       // 创建分享页（密码 + 有效期）
	DirectLink = "direct_link" // 创建直链（curl 友好 + 有效期）

	// 后台管理
	ViewAdminOverview = "view_admin_overview" // 查看实时概览
	ManageUsers       = "manage_users"        // 用户管理
	ManageUserGroups  = "manage_user_groups"  // 用户组管理与成员配置
	ManagePermissions = "manage_permissions"  // 用户组权限配置
	ManageQuotas      = "manage_quotas"       // 配额方案与用户组配额
	ManageInvitations = "manage_invitations"  // 邀请码管理
	ReviewFiles       = "review_files"        // 文件审核
	ReviewShares      = "review_shares"       // 分享审核
	ReadAuditLog      = "read_audit_log"      // 系统审计查看
	ManageSystem      = "manage_system"       // 系统配置
)

// Definition 是 permission 的完整定义：code + 中英文说明。
// Definitions 是唯一来源，All / IsValid 全部派生自它。
type Definition struct {
	Code          string
	Description   string
	DescriptionEN string
}

// Definitions 供权限配置界面展示，数据库只保存 code。
var Definitions = []Definition{
	{Login, "登录系统", "Sign in to the system"},
	{Upload, "上传资源", "Upload resources"},
	{Download, "下载资源", "Download resources"},
	{Preview, "预览资源", "Preview resources"},
	{Rename, "重命名自己的资源", "Rename own resources"},
	{DeleteOwn, "删除自己的资源", "Delete own resources"},
	{Share, "创建分享页", "Create share pages"},
	{DirectLink, "创建直链", "Create direct links"},
	{ViewAdminOverview, "查看实时概览", "View live overview"},
	{ManageUsers, "管理用户", "Manage users"},
	{ManageUserGroups, "管理用户组与成员", "Manage user groups and members"},
	{ManagePermissions, "配置用户组权限", "Configure group permissions"},
	{ManageQuotas, "管理配额", "Manage quotas"},
	{ManageInvitations, "管理邀请码", "Manage invitation codes"},
	{ReviewFiles, "审核文件", "Review files"},
	{ReviewShares, "审核分享", "Review shares"},
	{ReadAuditLog, "查看审计日志", "View audit logs"},
	{ManageSystem, "管理系统配置", "Manage system settings"},
}

// All 是从 Definitions 派生的 code 列表，顺序固定。
// 用于启动 seed 完整性校验、未知 code 白名单检查。
var All []string

// Admin 是能够进入管理面板的细粒度后台权限集合。
var Admin = []string{
	ViewAdminOverview,
	ManageUsers,
	ManageUserGroups,
	ManagePermissions,
	ManageQuotas,
	ManageInvitations,
	ReviewFiles,
	ReviewShares,
	ReadAuditLog,
	ManageSystem,
}

func init() {
	All = make([]string, 0, len(Definitions))
	for _, d := range Definitions {
		All = append(All, d.Code)
	}
}

// IsValid 检查 code 是否在 All 列表里。
// 业务层在处理外部传入的 permission code 时用它做白名单校验。
func IsValid(code string) bool {
	for _, c := range All {
		if c == code {
			return true
		}
	}
	return false
}

// DescriptionByCode 查 code 的中文说明，未知 code 返回空串。
// 主要给权限配置面板使用。
func DescriptionByCode(code string) string {
	for _, d := range Definitions {
		if d.Code == code {
			return d.Description
		}
	}
	return ""
}
