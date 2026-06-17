// Package middleware 实现 lumina-relay 的 HTTP 中间件。
//
// 当前提供 IP 维度的请求限流（sync-design §6.6），
// 用于保护恢复码爆破敏感端点（/account/dek、/device/register）。
package middleware

import (
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/apperr"
)

// ipLimiter 实现按 IP 的滑动窗口限流。
// 每个 IP 维护窗口内请求时间戳列表；超过 max 即拒绝。
// 并发安全。
type ipLimiter struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	now    func() time.Time
	hits   map[string][]time.Time // ip -> 窗口内时间戳
}

// option 是构造 ipLimiter 的可选项。
type option func(*ipLimiter)

// NewIPLimiter 构造一个滑动窗口 IP 限流器。
// max 为窗口内允许的最大请求数，window 为窗口时长。
// 可选 withNow 注入可控时钟（仅供测试）。
func NewIPLimiter(max int, window time.Duration, opts ...option) *ipLimiter {
	l := &ipLimiter{
		window: window,
		max:    max,
		now:    time.Now,
		hits:   make(map[string][]time.Time),
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// withNow 注入 now 函数，供测试控制时间推进。非导出，仅同包测试可用。
func withNow(f func() time.Time) option {
	return func(l *ipLimiter) { l.now = f }
}

// Allow 判断指定 IP 是否在限额内。
// 返回 true 表示放行（并记录本次），false 表示超限。
// 同时执行滑动：移除窗口外的旧时间戳。
func (l *ipLimiter) Allow(ip string) bool {
	now := l.now()
	cutoff := now.Add(-l.window)

	l.mu.Lock()
	defer l.mu.Unlock()

	// 滑动：保留窗口内的时间戳
	fresh := l.hits[ip][:0]
	for _, t := range l.hits[ip] {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}

	if len(fresh) >= l.max {
		l.hits[ip] = fresh
		return false
	}
	fresh = append(fresh, now)
	l.hits[ip] = fresh
	return true
}

// IPLimit 返回一个 gin 中间件，对每个请求按来源 IP 限流。
// 超限则写 429 + rate_limited（见 sync-design §6.5/§6.6），中止请求链。
func IPLimit(l *ipLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		if !l.Allow(ip) {
			apperr.New(apperr.CodeRateLimited, "请求过于频繁").WriteJSON(c.Writer)
			c.Abort()
			return
		}
		c.Next()
	}
}
