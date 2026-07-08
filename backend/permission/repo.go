package permission

import (
	"context"

	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
)

// Repo 是 permissions 表的只读访问入口。
// 写入只在 bootstrap.AuthCatalog 启动期发生，Repo 不暴露写入方法。
type Repo struct {
	q *sqlcgen.Queries
}

func NewRepo(q *sqlcgen.Queries) *Repo { return &Repo{q: q} }

// List 返回所有 permission（按 code 排序）。
func (r *Repo) List(ctx context.Context) ([]sqlcgen.Permission, error) {
	return r.q.ListPermissions(ctx)
}

// Codes 返回所有 permission code 列表（按 DB 自然顺序）。
// 用于启动期对比/校验 seed 是否完整。
func (r *Repo) Codes(ctx context.Context) ([]string, error) {
	return r.q.ListPermissionCodes(ctx)
}
