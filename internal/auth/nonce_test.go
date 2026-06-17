package auth

import (
	"sync"
	"testing"
	"time"
)

// TestNonceStore_ReplayRejected 验证：同一 nonce 首次 UseOnce 返回 true（被接受），
// 再次 UseOnce 返回 false（被拒绝，视为重放）。
// 见 sync-design §578：服务端对 nonce 做 5min 去重。
func TestNonceStore_ReplayRejected(t *testing.T) {
	s := NewNonceStore(5 * time.Minute)
	nonce := "nonce-abc-123"

	if !s.UseOnce(nonce) {
		t.Fatal("首次 UseOnce 应被接受（true）")
	}
	if s.UseOnce(nonce) {
		t.Fatal("重复 UseOnce 应被拒绝（false），视为重放")
	}
}

// TestNonceStore_DistinctNoncesAccepted 验证不同 nonce 互不影响。
func TestNonceStore_DistinctNoncesAccepted(t *testing.T) {
	s := NewNonceStore(5 * time.Minute)
	if !s.UseOnce("a") {
		t.Fatal("nonce 'a' 应被接受")
	}
	if !s.UseOnce("b") {
		t.Fatal("nonce 'b' 应被接受（与 a 无关）")
	}
}

// TestNonceStore_ExpiresAfterTTL 验证 nonce 在 TTL 过期后可再次使用。
// 这同时覆盖了"过期项不无限堆积"的设计：用可控时钟注入过期时间。
func TestNonceStore_ExpiresAfterTTL(t *testing.T) {
	now := time.Now()
	s := NewNonceStore(5*time.Minute, withNowFunc(func() time.Time { return now }))

	if !s.UseOnce("expiring") {
		t.Fatal("首次应被接受")
	}
	// 推进到 TTL 之后，同一 nonce 应重新可用
	now = now.Add(6 * time.Minute)
	if !s.UseOnce("expiring") {
		t.Fatal("TTL 过期后同一 nonce 应重新被接受")
	}
}

// TestNonceStore_ConcurrentSameNonce 验证并发下同一 nonce 只有一个 goroutine 成功。
// 这是防重放的核心保证：竞态不能让两个请求都拿到 true。
func TestNonceStore_ConcurrentSameNonce(t *testing.T) {
	s := NewNonceStore(5 * time.Minute)
	nonce := "race-nonce"

	var wg sync.WaitGroup
	var accepted int32
	var mu sync.Mutex
	const n = 50
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.UseOnce(nonce) {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if accepted != 1 {
		t.Fatalf("并发下同一 nonce 应只有 1 个成功，实际 %d", accepted)
	}
}

// TestNonceStore_CleanupDropsExpired 验证过期清理生效：过期后 store 内不再持有该 nonce，
// 且不无限增长（通过 Len 观察过期项被移除）。
func TestNonceStore_CleanupDropsExpired(t *testing.T) {
	now := time.Now()
	s := NewNonceStore(5*time.Minute, withNowFunc(func() time.Time { return now }))

	s.UseOnce("old-1")
	s.UseOnce("old-2")
	if got := s.Len(); got != 2 {
		t.Fatalf("清理前 Len = %d, want 2", got)
	}

	// 推进时间，触发惰性清理（在下次 UseOnce 时清理过期项）
	now = now.Add(6 * time.Minute)
	s.UseOnce("fresh")
	if got := s.Len(); got != 1 {
		t.Fatalf("清理后 Len = %d, want 1（仅 fresh）", got)
	}
}
