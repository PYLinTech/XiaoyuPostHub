package server

// 下载方向配额：与上传配额（存储空间 / 单文件 / 每日上传流量与次数）对称，
// 按「下载者」限制每日下载流量与次数。
//
// 主体口径：
//   - 已登录：按账号计，配额来自其用户组绑定的方案；超级管理员豁免（与上传一致）；
//   - 未登录：按来源 IP 识别，每个 IP 视为一个独立用户，配额统一来自 guest 系统
//     用户组绑定的方案（该组由迁移 038 预置、不可删除、不可分配成员）。
//
// 记账口径：自然日（服务器本地时区），数据落在 download_usage_events（只增不减）。
// 计划型下载（分享 / 取件 / 文件页）在"创建下载计划"时即校验并记账——302 与
// 本机中转两条取数路径口径一致；对外直链（/d/）在开始取流前校验、完整读完后记账
// （只取中段的探测请求不计数，与直链自身的下载次数结算口径一致）。

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/quota"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	errDownloadQuotaCount = errors.New("已达到今日下载次数上限，请明天再试")
	errDownloadQuotaBytes = errors.New("已达到今日下载流量上限，请明天再试")
)

// downloadLimits 是下载方向的限额（NULL = 不限，0 = 不允许）。
type downloadLimits struct {
	bytes pgtype.Int8
	count pgtype.Int8
}

// downloadSubject 是一次下载的配额主体（登录用户或未登录 IP）。
type downloadSubject struct {
	// userID 非空表示已登录；为空时按 ip 识别。
	userID *int64
	ip     string
	limits downloadLimits
	// exempt 为真时既不校验也不记账（超级管理员）。
	exempt bool
}

// resolveDownloadSubject 解析本次请求的配额主体。
// guest 方案缺失（迁移未执行等异常部署）时放行并记日志：分享下载是核心链路，
// 不应因配置缺失整体不可用（管理员可在日志中看到异常）。
func resolveDownloadSubject(r *http.Request, deps Deps) downloadSubject {
	if u, err := authenticatedUser(deps, r); err == nil {
		if u.IsSuperAdmin() {
			return downloadSubject{exempt: true}
		}
		profile, profileErr := deps.QuotaRepo.GetEffectiveQuotaByUser(r.Context(), u.ID)
		if profileErr != nil {
			log.Printf("读取用户下载配额失败 user=%d：%v", u.ID, profileErr)
			return downloadSubject{exempt: true}
		}
		userID := u.ID
		return downloadSubject{
			userID: &userID,
			limits: downloadLimits{bytes: profile.DailyDownloadBytesLimit, count: profile.DailyDownloadCountLimit},
		}
	}
	profile, err := deps.QuotaRepo.GetGuestQuota(r.Context())
	if err != nil {
		if errors.Is(err, quota.ErrQuotaProfileNotFound) {
			log.Printf("未找到 %s 用户组/配额方案，未登录下载暂不限流：%v", quota.NameGuest, err)
		} else {
			log.Printf("读取未登录下载配额失败：%v", err)
		}
		return downloadSubject{exempt: true}
	}
	return downloadSubject{
		ip:     normalizeClientIP(clientIP(r)),
		limits: downloadLimits{bytes: profile.DailyDownloadBytesLimit, count: profile.DailyDownloadCountLimit},
	}
}

// checkDownloadQuota 校验下载者当日额度是否还容得下 sizeBytes（不记账）。
// 无任何限额时不查询用量。
func checkDownloadQuota(ctx context.Context, deps Deps, subject downloadSubject, sizeBytes int64) error {
	if subject.exempt {
		return nil
	}
	if !subject.limits.count.Valid && !subject.limits.bytes.Valid {
		return nil
	}
	since := startOfLocalDay(time.Now())
	var count, used int64
	var err error
	if subject.userID != nil {
		count, used, err = deps.ResourceRepo.DownloadUsageSinceUser(ctx, *subject.userID, since)
	} else {
		count, used, err = deps.ResourceRepo.DownloadUsageSinceIP(ctx, subject.ip, since)
	}
	if err != nil {
		return fmt.Errorf("读取每日下载用量失败: %w", err)
	}
	if subject.limits.count.Valid && count >= subject.limits.count.Int64 {
		return errDownloadQuotaCount
	}
	if subject.limits.bytes.Valid && used+sizeBytes > subject.limits.bytes.Int64 {
		return errDownloadQuotaBytes
	}
	return nil
}

// recordDownloadUsage 追加一条下载用量事件（预扣或结算，取决于调用点）。
// 记账失败只记日志：下载已经发生，不能因统计写入失败回滚交付。
func recordDownloadUsage(ctx context.Context, deps Deps, subject downloadSubject, sizeBytes int64, source string) {
	if subject.exempt {
		return
	}
	// 用量事件要求 user_id 与 client_ip 至少有一个：两者都取不到时不写库
	// （该主体的额度也无从统计），避免触发表约束后刷日志。
	if subject.userID == nil && subject.ip == "" {
		return
	}
	if err := deps.ResourceRepo.RecordDownloadUsage(ctx, subject.userID, subject.ip, sizeBytes, source); err != nil {
		log.Printf("记录下载用量失败（source=%s）：%v", source, err)
	}
}

// guardPlannedDownload 用于"创建下载计划"型下载（分享 / 取件 / 文件页）：
// 计划创建即视为该次下载发生，校验通过后立即记账。
// 返回的错误交给 writeDownloadQuotaFailure 转成对外响应。
func guardPlannedDownload(r *http.Request, deps Deps, sizeBytes int64, source string) error {
	subject := resolveDownloadSubject(r, deps)
	if err := checkDownloadQuota(r.Context(), deps, subject, sizeBytes); err != nil {
		return err
	}
	recordDownloadUsage(r.Context(), deps, subject, sizeBytes, source)
	return nil
}

// writeDownloadQuotaFailure 输出下载配额校验失败：配额不足为 429，
// 其余（用量统计读取失败等）按内部错误处理。
func writeDownloadQuotaFailure(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, errDownloadQuotaCount), errors.Is(err, errDownloadQuotaBytes):
		writeBusinessError(w, http.StatusTooManyRequests, err.Error())
	default:
		log.Printf("下载配额校验失败：%v", err)
		writeBusinessError(w, http.StatusInternalServerError, "读取下载配额失败")
	}
}

// normalizeClientIP 归一化 IP 文本（IPv6 压缩写法等），保证同一地址稳定聚合。
func normalizeClientIP(value string) string {
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	return value
}
