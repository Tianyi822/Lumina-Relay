package middleware

import (
	"crypto/sha256"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/apperr"
)

const defaultHashedLimiterCapacity = 100_000

// HashedLimiter 不保存 IP、用户名或设备 ID 原文。
type HashedLimiter struct {
	mu       sync.Mutex
	window   time.Duration
	max      int
	capacity int
	now      func() time.Time
	hits     map[[sha256.Size]byte][]time.Time
}

func NewHashedLimiter(maximum int, window time.Duration) *HashedLimiter {
	if maximum <= 0 {
		maximum = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	return &HashedLimiter{
		window: window, max: maximum, capacity: defaultHashedLimiterCapacity,
		now: time.Now, hits: make(map[[sha256.Size]byte][]time.Time),
	}
}

func (l *HashedLimiter) Allow(key string) (bool, time.Duration) {
	if l == nil || key == "" {
		return false, time.Minute
	}
	hash := sha256.Sum256([]byte(key))
	now := l.now()
	cutoff := now.Add(-l.window)
	l.mu.Lock()
	defer l.mu.Unlock()

	fresh := l.hits[hash][:0]
	for _, hit := range l.hits[hash] {
		if hit.After(cutoff) {
			fresh = append(fresh, hit)
		}
	}
	if len(fresh) >= l.max {
		l.hits[hash] = fresh
		retry := fresh[0].Add(l.window).Sub(now)
		if retry <= 0 {
			retry = time.Millisecond
		}
		return false, retry
	}
	if len(fresh) == 0 {
		delete(l.hits, hash)
		if len(l.hits) >= l.capacity {
			l.cleanupLocked(cutoff)
			if len(l.hits) >= l.capacity {
				return false, l.window
			}
		}
	}
	l.hits[hash] = append(fresh, now)
	return true, 0
}

func (l *HashedLimiter) cleanupLocked(cutoff time.Time) {
	for key, hits := range l.hits {
		fresh := hits[:0]
		for _, hit := range hits {
			if hit.After(cutoff) {
				fresh = append(fresh, hit)
			}
		}
		if len(fresh) == 0 {
			delete(l.hits, key)
		} else {
			l.hits[key] = fresh
		}
	}
}

func LimitByClientIP(limiter *HashedLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		allowed, retry := limiter.Allow(c.ClientIP())
		if !allowed {
			seconds := int64((retry + time.Second - 1) / time.Second)
			c.Header("Retry-After", strconv.FormatInt(seconds, 10))
			apperr.New(apperr.CodeRateLimited, "请求过于频繁").
				WithExtra("retryAfterMs", retry.Milliseconds()).
				WriteJSON(c.Writer)
			c.Abort()
			return
		}
		c.Next()
	}
}
