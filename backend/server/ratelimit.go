package server

import (
	"net/http"
	"sync"
	"time"
)

// attemptLimiter 进程内滑动窗口失败限流：用于取件码与分享密码的失败尝试。
// 取件码默认 6 位纯数字、无任何约束时可被高速枚举，这里按"来源 IP + 全局"
// 两个维度限制失败次数。
//
// 注意：计数在进程内存中，当前部署形态为单实例。多实例部署需迁移到共享存储
// （数据库或 Redis），否则各实例独立计数会放宽限制。
type attemptLimiter struct {
	mu       sync.Mutex
	window   time.Duration
	limit    int
	failures map[string][]time.Time
}

func newAttemptLimiter(window time.Duration, limit int) *attemptLimiter {
	return &attemptLimiter{window: window, limit: limit, failures: make(map[string][]time.Time)}
}

func (l *attemptLimiter) pruneLocked(key string, now time.Time) []time.Time {
	kept := l.failures[key][:0]
	for _, at := range l.failures[key] {
		if now.Sub(at) < l.window {
			kept = append(kept, at)
		}
	}
	l.failures[key] = kept
	return kept
}

// Blocked 判断 key 在当前窗口内是否已达到失败上限。
func (l *attemptLimiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.pruneLocked(key, time.Now())) >= l.limit
}

// RecordFailure 记录一次失败尝试。
func (l *attemptLimiter) RecordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.failures[key] = append(l.pruneLocked(key, now), now)
}

// Reset 清零某个 key（验证成功后调用）。
func (l *attemptLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

const (
	secretAttemptWindow = 10 * time.Minute
	// 单 IP 窗口内允许的失败次数。
	secretAttemptLimit = 20
	// 全局兜底：防止换 IP 枚举取件码/密码。
	secretGlobalLimit = 2000
)

var (
	secretIPLimiter     = newAttemptLimiter(secretAttemptWindow, secretAttemptLimit)
	secretGlobalLimiter = newAttemptLimiter(secretAttemptWindow, secretGlobalLimit)
)

// allowPublicSecretAttempt 在解析取件码 / 校验分享密码前调用：任一维度超限即拒绝。
func allowPublicSecretAttempt(r *http.Request) bool {
	ip := clientIP(r)
	return !secretIPLimiter.Blocked(ip) && !secretGlobalLimiter.Blocked("global")
}

// recordPublicSecretFailure 记录一次取件码/密码失败（IP + 全局）。
func recordPublicSecretFailure(r *http.Request) {
	ip := clientIP(r)
	secretIPLimiter.RecordFailure(ip)
	secretGlobalLimiter.RecordFailure("global")
}

// resetPublicSecretFailures 成功验证后清零该 IP 的计数。
func resetPublicSecretFailures(r *http.Request) {
	secretIPLimiter.Reset(clientIP(r))
}
