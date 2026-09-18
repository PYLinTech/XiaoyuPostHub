package loginseal

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

const (
	// nonceTTL 是 nonce 的最长存活时间：签发后 5 分钟内必须完成提交，
	// 之后即使未被使用也会失效。
	nonceTTL = 5 * time.Minute
	// nonceCapacity 是未消费 nonce 的池上限。每个 nonce 仅几十字节，
	// 该上限远高于正常登录并发，同时给恶意刷取设了内存边界。
	nonceCapacity = 1 << 16
	// nonceSweepInterval 控制过期清理频率，避免每次签发都全量扫描。
	nonceSweepInterval = time.Minute
)

// nonceStore 是带过期与容量上限的一次性随机数池。
//
// nonce 本身是 256 位随机值，进程重启后池清空，旧 nonce 自然失效。
type nonceStore struct {
	mu        sync.Mutex
	expires   map[string]time.Time
	order     []string
	lastSweep time.Time
	ttl       time.Duration
	capacity  int
}

func newNonceStore() *nonceStore {
	return &nonceStore{
		expires:  make(map[string]time.Time),
		ttl:      nonceTTL,
		capacity: nonceCapacity,
	}
}

// issue 生成并登记一个 nonce。容量达到上限时先淘汰最旧的一条，
// 保证签发始终可用（正常流量下远不会触发）。
func (s *nonceStore) issue() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	nonce := base64.RawURLEncoding.EncodeToString(raw)

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	if len(s.expires) >= s.capacity {
		s.evictOldestLocked()
	}
	s.expires[nonce] = now.Add(s.ttl)
	s.order = append(s.order, nonce)
	return nonce, nil
}

// consume 原子地校验并移除 nonce：只有"存在且未过期"才返回 true，
// 无论结果如何都会删除该 nonce（一次性语义）。
func (s *nonceStore) consume(nonce string) bool {
	if nonce == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expiresAt, ok := s.expires[nonce]
	if !ok {
		return false
	}
	delete(s.expires, nonce)
	return time.Now().Before(expiresAt)
}

// sweepLocked 清理过期条目并重建顺序表，调用方必须持有锁。
func (s *nonceStore) sweepLocked(now time.Time) {
	if now.Sub(s.lastSweep) < nonceSweepInterval {
		return
	}
	s.lastSweep = now
	kept := s.order[:0]
	for _, nonce := range s.order {
		expiresAt, ok := s.expires[nonce]
		if !ok {
			continue
		}
		if !now.Before(expiresAt) {
			delete(s.expires, nonce)
			continue
		}
		kept = append(kept, nonce)
	}
	s.order = kept
}

// evictOldestLocked 淘汰顺序表中最旧的一条，调用方必须持有锁。
func (s *nonceStore) evictOldestLocked() {
	for len(s.order) > 0 {
		nonce := s.order[0]
		s.order = s.order[1:]
		if _, ok := s.expires[nonce]; ok {
			delete(s.expires, nonce)
			return
		}
	}
}
