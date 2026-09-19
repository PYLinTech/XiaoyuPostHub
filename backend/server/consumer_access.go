package server

// 分享消费侧（在线预览 / 下载）的权限门禁。
//
// 模型：能否消费分享内容由「消费者身份」决定，与创建动作的权限
// （share / pickup_share / direct_link）无关：
//   - 已登录：按账号所属用户组当前被授予的 preview / download（超管短路放行）；
//   - 未登录：按 guest 系统用户组（未登录访客组）当前被授予的同一权限。
//
// guest 组的 preview / download 由迁移 040 补齐、启动期 bootstrap 兜底新建，
// 默认授予以保证升级后匿名分享行为不变；管理员可在「权限与配额」中取消勾选
// 来关闭匿名预览 / 下载。登录用户组的 download / preview 同样在此生效——
// 此前消费侧不校验权限，未授予 download 的账号仍可从分享页下载，属于绕过点。
//
// 失败口径（fail-closed）：guest 组缺失、权限读取失败时不放行，返回 503。
// 配置异常必须显式暴露，不能静默降级为"人人可下载"。
//
// 放置位置：各消费入口的第一道校验（比分享状态、密码校验更早），
// 拒绝时不再触碰分享与资源数据。

import (
	"errors"
	"log"
	"net/http"

	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/quota"
	"github.com/PYLinTech/XiaoyuPostHub/backend/user"
	"github.com/jackc/pgx/v5"
)

// consumerUser 解析请求的消费身份：
//   - 已登录：(用户, true, nil)，权限取自其用户组；
//   - 确定匿名（无会话 Cookie / 会话失效 / 账号被停用）：(零值, false, nil)；
//   - 会话或用户资料读取失败：(零值, false, err)，调用方按 fail-closed 处理。
func consumerUser(deps Deps, r *http.Request) (user.User, bool, error) {
	u, err := authenticatedUser(deps, r)
	switch {
	case err == nil:
		return u, true, nil
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, user.ErrUserDisabled):
		return user.User{}, false, nil
	default:
		return user.User{}, false, err
	}
}

// requireShareConsumePermission 校验分享消费侧权限（preview / download）。
// 通过返回 true；被拒绝时已写出响应并返回 false，调用方应立即返回。
func requireShareConsumePermission(w http.ResponseWriter, r *http.Request, deps Deps, code string) bool {
	u, loggedIn, err := consumerUser(deps, r)
	if err != nil {
		log.Printf("读取分享消费者身份失败：%v", err)
		writeBusinessError(w, http.StatusServiceUnavailable, "访问服务暂时不可用，请稍后再试")
		return false
	}
	if loggedIn {
		if u.HasPermission(code) {
			return true
		}
		writeBusinessError(w, http.StatusForbidden, consumerDeniedMessage(code))
		return false
	}
	// 匿名请求按 guest 系统用户组的当前权限判断：组由迁移 038 预置，
	// 缺失或读库失败一律拒绝（fail-closed），与匿名配额的失败口径一致。
	perms, err := deps.GroupRepo.PermissionSetByName(r.Context(), quota.NameGuest)
	if err != nil {
		log.Printf("读取未登录访客用户组权限失败：%v", err)
		writeBusinessError(w, http.StatusServiceUnavailable, "访客权限配置不可用，请联系管理员")
		return false
	}
	if !perms[code] {
		writeBusinessError(w, http.StatusForbidden, consumerDeniedMessage(code))
		return false
	}
	return true
}

// consumerDeniedMessage 按权限码给出对外提示（登录与匿名共用）。
func consumerDeniedMessage(code string) string {
	switch code {
	case permission.Preview:
		return "没有预览权限"
	case permission.Download:
		return "没有下载权限"
	default:
		return "没有访问权限"
	}
}
