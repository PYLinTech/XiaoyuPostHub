package user

import (
	"context"
	"fmt"

	"github.com/PYLinTech/XiaoyuPostHub/backend/config"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
)

// Repo 业务层唯一访问 users 表的入口。
// 所有"读用户"都走这里,临时附加 'all' 的逻辑在这里做。
type Repo struct {
	q *sqlcgen.Queries
}

// NewRepo 构造 Repo。
func NewRepo(q *sqlcgen.Queries) *Repo {
	return &Repo{q: q}
}

// GetByUsername 包装 sqlc,对 EnvSuperAdmin 临时追加 'all'(仅内存)。
// 业务代码统一从这里读用户,不要直接调 sqlc 生成的方法。
func (r *Repo) GetByUsername(ctx context.Context, name string) (User, error) {
	dbU, err := r.q.GetUserByUsername(ctx, name)
	if err != nil {
		return User{}, err
	}

	u := User{User: dbU}
	if name == config.EnvSuperAdmin {
		u.Groups = appendUnique(u.Groups, "all")
	}
	return u, nil
}

// CreateUser 业务层创建普通用户的入口。
// 防御性剔除 'all',并强制加入 'user'。
// 不允许通过此入口创建 EnvSuperAdmin 同名账号(避免污染)。
func (r *Repo) CreateUser(ctx context.Context, name, hash string, groups []string) (User, error) {
	if name == config.EnvSuperAdmin {
		return User{}, fmt.Errorf("不允许通过此入口创建超管同名账号")
	}
	groups = removeAll(groups)
	groups = appendUnique(groups, "user")

	dbU, err := r.q.CreateUser(ctx, sqlcgen.CreateUserParams{
		Username:     name,
		PasswordHash: hash,
		Groups:       groups,
	})
	if err != nil {
		return User{}, err
	}
	return User{User: dbU}, nil
}