package auth

import (
	"sync"
	"time"
)

// defaultTTL 是 nonce 去重窗口的默认时长。
// sync-design §578：服务端对 nonce 做 5min 去重（与 timestamp ±60s 窗口留余量）。
const defaultTTL = 5 * time.Minute

// NonceStore 记录已见 nonce，防止请求重放（sync-design §578）。
// 设计为惰性清理：每次 UseOnce 时顺便清掉过期项，避免后台 goroutine 的生命周期问题。
// 并发安全。
type NonceStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	seen    map[string]time.Time // nonce -> 首次见到的时间
}

// nonceOption 是构造 NonceStore 的可选项（functional option 模式）。
type nonceOption func(*NonceStore)

// NewNonceStore 构造一个 NonceStore。ttl 为去重窗口；<=0 时用 defaultTTL。
// 可选 withNowFunc 注入可控时钟（仅供测试）。
func NewNonceStore(ttl time.Duration, opts ...nonceOption) *NonceStore {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	s := &NonceStore{
		ttl:  ttl,
		now:  time.Now,
		seen: make(map[string]time.Time),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// withNowFunc 注入 now 函数，供测试控制时间推进。非导出，仅同包测试可用。
func withNowFunc(f func() time.Time) nonceOption {
	return func(s *NonceStore) { s.now = f }
}

// UseOnce 尝试占用一个 nonce。首次返回 true（接受）；
// 若 nonce 在 TTL 窗口内已出现过则返回 false（拒绝重放）。
// 同时执行惰性清理：移除已过期的旧项，避免无限增长。
func (s *NonceStore) UseOnce(nonce string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cleanupLocked(now)

	if _, exists := s.seen[nonce]; exists {
		return false
	}
	s.seen[nonce] = now
	return true
}

// Len 返回当前已记录的 nonce 数量，主要供测试与运维观测。
func (s *NonceStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

// cleanupLocked 移除所有已过期项。调用方必须持有 s.mu。
func (s *NonceStore) cleanupLocked(now time.Time) {
	cutoff := now.Add(-s.ttl)
	for n, t := range s.seen {
		if t.Before(cutoff) {
			delete(s.seen, n)
		}
	}
}
