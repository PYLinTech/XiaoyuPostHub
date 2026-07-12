package user

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PYLinTech/XiaoyuPostHub/backend/config"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/group"
	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/role"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrInvalidCredentials 登录失败（账号不存在 / 密码错误 / 没有登录权限 / 入参为空）。
//
// 业务层故意**不**区分具体原因——登录接口统一回复"账号或者密码错误"，
// 避免向攻击者泄露"账号是否存在"。
var ErrInvalidCredentials = errors.New("user: invalid credentials")

// Repo 业务层访问 users 表的入口。
//
// 业务层"读用户"有两条等价路径：
//   - GetByUsername：登录、BootstrapSuperAdmin、命令行工具按用户名查找
//   - GetByID：userInfo / 已登录后的所有后续接口按 id 加载
//
// 两条路径行为完全一致：超管短路、加载 group 列表、单条 SQL 合并 effective permissions。
// 调用方**不要**直接用 sqlcgen.GetUserByUsername / sqlcgen.GetUserByID，
// 因为加载完整 User（含 isSuperAdmin / groupIDs / permissionSet）需要走 Repo
// 才能保证超管身份判断、permission 集合合并的统一行为。
type Repo struct {
	pool   *pgxpool.Pool
	q      *sqlcgen.Queries
	roleR  *role.Repo
	groupR *group.Repo
}

// NewRepo 构造 Repo。
//   - pool：用于 CreateUser 走事务
//   - q：sqlc 生成的 Queries
//   - roleR：用于加载 permission 集合
//   - groupR：用于加载 group 列表、CreateUser 事务中分配 default_user group
func NewRepo(pool *pgxpool.Pool, q *sqlcgen.Queries, roleR *role.Repo, groupR *group.Repo) *Repo {
	return &Repo{pool: pool, q: q, roleR: roleR, groupR: groupR}
}

// GetByUsername 业务层"按用户名读用户"的入口。
//
// 行为：
//   - 真超管（username == config.EnvSuperAdmin）：isSuperAdmin=true，
//     groupIDs 与 permissionSet 留空（**不查 DB**）
//   - 普通 user：从 users 表读基础字段 + 加载 group 列表 + 单条 SQL 合并最终权限
func (r *Repo) GetByUsername(ctx context.Context, name string) (User, error) {
	dbU, err := r.q.GetUserByUsername(ctx, name)
	if err != nil {
		return User{}, err
	}
	return r.hydrate(ctx, dbU)
}

// GetByID 业务层"按用户 id 读用户"的入口，与 GetByUsername 行为完全一致：
//
//   - 真超管短路：若该 user 的 username == config.EnvSuperAdmin，直接标记 isSuperAdmin=true
//   - 普通 user：从 users 表读基础字段 + 加载 group 列表 + 单条 SQL 合并最终权限
//
// 用于已登录场景（userInfo 接口拿 cookie → user_id → 加载完整 User）。
func (r *Repo) GetByID(ctx context.Context, id int64) (User, error) {
	dbU, err := r.q.GetUserByID(ctx, id)
	if err != nil {
		return User{}, err
	}
	return r.hydrate(ctx, dbU)
}

// hydrate 把 sqlcgen.User 升级成业务层 User（填超管标记 / groupIDs / permissionSet）。
//
// 抽出来是为了让 GetByUsername 和 GetByID 共享"加载完整 User"的逻辑，
// 保证两条入口行为完全一致：
//   - 真超管：不查 group / permission，直接标记 isSuperAdmin=true
//   - 普通 user：group 列表加载失败 → wrap error 返回；permissionSet 加载失败 → wrap error 返回
func (r *Repo) hydrate(ctx context.Context, dbU sqlcgen.User) (User, error) {
	u := User{User: dbU}

	if dbU.Username == config.EnvSuperAdmin {
		u.isSuperAdmin = true
		return u, nil
	}

	// 普通 user：加载 group 列表
	groupIDs, err := r.groupR.ListGroupIDsByUser(ctx, dbU.ID)
	if err != nil {
		return User{}, fmt.Errorf("加载 user %d 的 group 列表失败：%w", dbU.ID, err)
	}
	u.groupIDs = groupIDs

	// 单条 SQL 合并 (user_roles + group_roles) ∪ user_allow - user_deny
	perms, err := r.roleR.ListEffectivePermissionsByUser(ctx, dbU.ID)
	if err != nil {
		return User{}, fmt.Errorf("加载 user %d 的 effective permission 失败：%w", dbU.ID, err)
	}
	u.permissionSet = make(map[string]bool, len(perms))
	for _, p := range perms {
		u.permissionSet[p] = true
	}
	return u, nil
}

// Authenticate 业务层"登录校验"入口。
//
// 行为：
//  1. 校验 username / password 非空（trim 后）
//  2. 通过 Repo.GetByUsername 加载完整 User（含超管判定、permissionSet）
//  3. 校验 password 与 users.password_hash 匹配（bcrypt cost=12）
//  4. 校验 user 拥有 permission.Login
//
// 任一步失败统一返回 ErrInvalidCredentials，**不**区分具体原因——
// 调用方（loginHandler）一律回复"账号或者密码错误"，
// 避免泄露"账号是否存在 / 是否缺权限"等敏感信息。
func (r *Repo) Authenticate(ctx context.Context, username, password string) (User, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return User{}, ErrInvalidCredentials
	}

	u, err := r.GetByUsername(ctx, username)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, err
	}

	if !VerifyPassword(password, u.PasswordHash) {
		return User{}, ErrInvalidCredentials
	}

	if !u.HasPermission(permission.Login) {
		return User{}, ErrInvalidCredentials
	}

	return u, nil
}

// CreateUser 业务层创建普通用户。
//
// 参数：
//   - name：用户名
//   - password：明文密码（函数内部用 bcrypt cost=12 生成 hash，不接受调用方预生成）
//
// 行为（**单事务**）：
//  1. trim name / password 并拒绝 EnvSuperAdmin 同名账号（避免污染超管身份）
//  2. 拒绝空用户名 / 空密码
//  3. 用 HashPassword 生成 bcrypt hash
//  4. 插入 user
//  5. 查 default_user group id
//  6. 把 user 加入 default_user group
//  7. 提交事务
//  8. 调 GetByUsername 加载完整 User（含 groupIDs + permissionSet）
//
// **不**显式分配 user role——默认权限通过 default_user group → user role 获得。
// 事务保证"创建用户"和"加入默认组"原子成功或失败。
//
// 返回的 User **完整**：调 GetByUsername 后包含 groupIDs / permissionSet，
// 调用方**不要**自己再调 GetByUsername。
func (r *Repo) CreateUser(ctx context.Context, name, password string) (User, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return User{}, fmt.Errorf("user: username 不能为空")
	}
	if password == "" {
		return User{}, fmt.Errorf("user: password 不能为空")
	}
	if name == config.EnvSuperAdmin {
		return User{}, fmt.Errorf("user: 不允许通过此入口创建超管同名账号")
	}

	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	qtx := r.q.WithTx(tx)

	dbU, err := qtx.CreateUser(ctx, sqlcgen.CreateUserParams{
		Username:     name,
		PasswordHash: hash,
	})
	if err != nil {
		return User{}, fmt.Errorf("insert user: %w", err)
	}

	// 查 default_user group id
	grp, err := qtx.GetUserGroupByName(ctx, group.NameDefaultUser)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, fmt.Errorf("default_user group 不存在，请先跑 bootstrap")
		}
		return User{}, fmt.Errorf("查 default_user group: %w", err)
	}

	// 加入 default_user group
	if _, err := qtx.AssignUserToGroup(ctx, sqlcgen.AssignUserToGroupParams{
		UserID:  dbU.ID,
		GroupID: grp.ID,
	}); err != nil {
		return User{}, fmt.Errorf("加入 default_user group: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit: %w", err)
	}

	// 提交后加载完整 User：含 groupIDs + 通过 default_user group 继承的 permissionSet。
	// 这避免调用方拿到半成品 User 后再调一次 GetByUsername。
	return r.GetByUsername(ctx, name)
}

// ---------- 用户个人权限覆盖（user_permission_overrides） ----------

// SetPermissionOverride 设置用户的某条 permission 个人覆盖。
//   - code 必须是 permission.All 中的合法 code
//   - effect 只能是 "allow" 或 "deny"
//
// upsert 语义：已存在则更新 effect/reason/updated_at；不存在则插入。
func (r *Repo) SetPermissionOverride(ctx context.Context, userID int64, code, effect, reason string) error {
	if !permission.IsValid(code) {
		return fmt.Errorf("user: 未知 permission code: %s", code)
	}
	if effect != "allow" && effect != "deny" {
		return fmt.Errorf("user: permission override effect 只能是 allow 或 deny，得到 %q", effect)
	}
	_, err := r.q.SetUserPermissionOverride(ctx, sqlcgen.SetUserPermissionOverrideParams{
		UserID:     userID,
		Permission: code,
		Effect:     effect,
		Reason:     strToText(reason),
	})
	return err
}

// ClearPermissionOverride 移除用户对某条 permission 的个人覆盖。
// 该 permission 后续行为完全由 user_roles / group_roles 决定。
func (r *Repo) ClearPermissionOverride(ctx context.Context, userID int64, code string) error {
	if !permission.IsValid(code) {
		return fmt.Errorf("user: 未知 permission code: %s", code)
	}
	_, err := r.q.ClearUserPermissionOverride(ctx, sqlcgen.ClearUserPermissionOverrideParams{
		UserID:     userID,
		Permission: code,
	})
	return err
}

// ListPermissionOverrides 列出 user 的所有个人覆盖（按 permission 排序）。
func (r *Repo) ListPermissionOverrides(ctx context.Context, userID int64) ([]sqlcgen.UserPermissionOverride, error) {
	return r.q.ListUserPermissionOverrides(ctx, userID)
}

// strToText 工具：空字符串转 SQL NULL。
func strToText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}