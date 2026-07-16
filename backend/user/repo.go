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
	"github.com/PYLinTech/XiaoyuPostHub/backend/randomtoken"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrInvalidCredentials 登录失败（账号不存在 / 密码错误 / 没有登录权限 / 入参为空）。
//
// 业务层故意**不**区分具体原因——登录接口统一回复"账号或者密码错误"，
// 避免向攻击者泄露"账号是否存在"。
var ErrInvalidCredentials = errors.New("user: invalid credentials")

// ErrUserDisabled 会使现有会话与登录统一失效，不向登录接口暴露具体原因。
var ErrUserDisabled = errors.New("user: disabled")

var (
	ErrInvitationRequired  = errors.New("user: 注册需要邀请码")
	ErrInvitationInvalid   = errors.New("user: 邀请码无效或已被使用")
	ErrUsernameUnavailable = errors.New("user: 用户名已存在")
	ErrRegistrationInput   = errors.New("user: 用户名或密码格式不符合要求")
)

// Repo 业务层访问 users 表的入口。
//
// 业务层"读用户"有两条等价路径：
//   - GetByUsername：登录、BootstrapSuperAdmin、命令行工具按用户名查找
//   - GetByID：userInfo / 已登录后的所有后续接口按 id 加载
//
// 两条路径行为完全一致：判定超管、加载 group 列表、单条 SQL 合并 effective permissions。
// 调用方**不要**直接用 sqlcgen.GetUserByUsername / sqlcgen.GetUserByID，
// 因为加载完整 User（含 isSuperAdmin / groupIDs / permissionSet）需要走 Repo
// 才能保证超管身份判断、permission 集合合并的统一行为。
type Repo struct {
	pool   *pgxpool.Pool
	q      *sqlcgen.Queries
	groupR *group.Repo
}

// NewRepo 构造 Repo。
//   - pool：用于 CreateUser 走事务
//   - q：sqlc 生成的 Queries
//   - groupR：用于加载 group 列表、CreateUser 事务中分配 default_user group
func NewRepo(pool *pgxpool.Pool, q *sqlcgen.Queries, groupR *group.Repo) *Repo {
	return &Repo{pool: pool, q: q, groupR: groupR}
}

// GetByUsername 业务层"按用户名读用户"的入口。
//
// 行为：
//   - 真超管（username == config.EnvSuperAdmin）：isSuperAdmin=true，并加载实际用户组
//   - 普通 user：从 users 表读基础字段 + 加载 group 列表 + 单条 SQL 合并最终权限
func (r *Repo) GetByUsername(ctx context.Context, name string) (User, error) {
	dbU, err := r.q.GetUserByUsername(ctx, name)
	if err != nil {
		return User{}, err
	}
	if dbU.IsDisabled {
		return User{}, ErrUserDisabled
	}
	return r.hydrate(ctx, dbU)
}

// GetByID 业务层"按用户 id 读用户"的入口，与 GetByUsername 行为完全一致：
//
//   - 真超管：若该 user 的 username == config.EnvSuperAdmin，标记 isSuperAdmin=true
//   - 所有 user：从 users 表读基础字段 + 加载 group 列表 + 单条 SQL 合并最终权限
//
// 用于已登录场景（userInfo 接口拿 cookie → user_id → 加载完整 User）。
func (r *Repo) GetByID(ctx context.Context, id int64) (User, error) {
	dbU, err := r.q.GetUserByID(ctx, id)
	if err != nil {
		return User{}, err
	}
	if dbU.IsDisabled {
		return User{}, ErrUserDisabled
	}
	return r.hydrate(ctx, dbU)
}

// hydrate 把 sqlcgen.User 升级成业务层 User（填超管标记 / groupIDs / permissionSet）。
//
// 抽出来是为了让 GetByUsername 和 GetByID 共享"加载完整 User"的逻辑，
// 保证两条入口行为完全一致：
//   - 真超管：标记 isSuperAdmin=true，但仍加载其实际 group / permission
//   - 所有 user：group 列表加载失败 → wrap error 返回；permissionSet 加载失败 → wrap error 返回
func (r *Repo) hydrate(ctx context.Context, dbU sqlcgen.User) (User, error) {
	u := User{User: dbU, isSuperAdmin: dbU.Username == config.EnvSuperAdmin}

	// 所有用户（包括超管）都加载实际用户组，使权限、配额和管理界面使用
	// 同一份成员关系；超管的 HasPermission 仍保留全权限短路。
	groupIDs, err := r.groupR.ListGroupIDsByUser(ctx, dbU.ID)
	if err != nil {
		return User{}, fmt.Errorf("加载 user %d 的 group 列表失败：%w", dbU.ID, err)
	}
	u.groupIDs = groupIDs

	// 权限只从用户所属用户组直接汇总。
	perms, err := r.q.ListEffectivePermissionsByUser(ctx, dbU.ID)
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
	if errors.Is(err, ErrUserDisabled) {
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

type RegistrationPolicy struct {
	RequiresInvitation bool
	CodeOptions        randomtoken.CodeOptions
}

func (r *Repo) RegistrationPolicy(ctx context.Context) (RegistrationPolicy, error) {
	var policy RegistrationPolicy
	err := r.pool.QueryRow(ctx, `
		SELECT registration_requires_invitation,invitation_length,invitation_case_sensitive,
		       invitation_include_letters,invitation_include_numbers
		FROM system_settings WHERE id=1`).Scan(
		&policy.RequiresInvitation, &policy.CodeOptions.Length, &policy.CodeOptions.CaseSensitive,
		&policy.CodeOptions.IncludeLetters, &policy.CodeOptions.IncludeNumbers,
	)
	return policy, err
}

// Register 创建公开注册用户，并在同一事务中锁定、消费一次性邀请码。
// 用户组邀请码会让新用户额外加入对应用户组；用户邀请码仅记录邀请来源。
func (r *Repo) Register(ctx context.Context, name, password, invitationCode string) (User, error) {
	name = strings.TrimSpace(name)
	invitationCode = strings.TrimSpace(invitationCode)
	if len(name) < 3 || len(name) > 64 || len(password) < 8 || len(password) > 1024 || name == config.EnvSuperAdmin {
		return User{}, ErrRegistrationInput
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("begin registration tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var policy RegistrationPolicy
	if err := tx.QueryRow(ctx, `
		SELECT registration_requires_invitation,invitation_length,invitation_case_sensitive,
		       invitation_include_letters,invitation_include_numbers
		FROM system_settings WHERE id=1`).Scan(
		&policy.RequiresInvitation, &policy.CodeOptions.Length, &policy.CodeOptions.CaseSensitive,
		&policy.CodeOptions.IncludeLetters, &policy.CodeOptions.IncludeNumbers,
	); err != nil {
		return User{}, err
	}
	invitationCode = randomtoken.NormalizeCode(invitationCode, policy.CodeOptions.CaseSensitive)
	var invitationID int64
	var invitationGroupID pgtype.Int8
	if invitationCode == "" {
		if policy.RequiresInvitation {
			return User{}, ErrInvitationRequired
		}
	} else {
		if !randomtoken.ValidCode(invitationCode, policy.CodeOptions) {
			return User{}, ErrInvitationInvalid
		}
		err := tx.QueryRow(ctx, `
			SELECT id,issued_to_group_id
			FROM invitation_codes
			WHERE code_hash=$1 AND used_at IS NULL AND revoked_at IS NULL
			FOR UPDATE`, randomtoken.Hash(invitationCode)).Scan(&invitationID, &invitationGroupID)
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrInvitationInvalid
		}
		if err != nil {
			return User{}, err
		}
	}

	qtx := r.q.WithTx(tx)
	dbU, err := qtx.CreateUser(ctx, sqlcgen.CreateUserParams{Username: name, PasswordHash: hash})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return User{}, ErrUsernameUnavailable
		}
		return User{}, fmt.Errorf("insert registered user: %w", err)
	}
	defaultGroup, err := qtx.GetUserGroupByName(ctx, group.NameDefaultUser)
	if err != nil {
		return User{}, fmt.Errorf("查 default_user group: %w", err)
	}
	if _, err := qtx.AssignUserToGroup(ctx, sqlcgen.AssignUserToGroupParams{UserID: dbU.ID, GroupID: defaultGroup.ID}); err != nil {
		return User{}, fmt.Errorf("加入 default_user group: %w", err)
	}
	if invitationGroupID.Valid && invitationGroupID.Int64 != defaultGroup.ID {
		if _, err := qtx.AssignUserToGroup(ctx, sqlcgen.AssignUserToGroupParams{UserID: dbU.ID, GroupID: invitationGroupID.Int64}); err != nil {
			return User{}, fmt.Errorf("加入邀请码用户组: %w", err)
		}
	}
	if invitationID != 0 {
		result, err := tx.Exec(ctx, `UPDATE invitation_codes SET used_by_user_id=$1,used_at=NOW() WHERE id=$2 AND used_at IS NULL AND revoked_at IS NULL`, dbU.ID, invitationID)
		if err != nil {
			return User{}, err
		}
		if result.RowsAffected() != 1 {
			return User{}, ErrInvitationInvalid
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit registration: %w", err)
	}
	return r.GetByUsername(ctx, name)
}
