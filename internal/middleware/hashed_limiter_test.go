package middleware

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHashedLimiterBoundsConcurrentCalls(t *testing.T) {
	limiter := NewHashedLimiter(5, time.Minute)
	var allowed atomic.Int64
	var wait sync.WaitGroup
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if ok, _ := limiter.Allow("same-device"); ok {
				allowed.Add(1)
			}
		}()
	}
	wait.Wait()
	if allowed.Load() != 5 {
		t.Fatalf("allowed=%d want=5", allowed.Load())
	}
}

func TestHashedLimiterSeparatesKeys(t *testing.T) {
	limiter := NewHashedLimiter(1, time.Minute)
	if ok, _ := limiter.Allow("A"); !ok {
		t.Fatal("A 首次应通过")
	}
	if ok, _ := limiter.Allow("B"); !ok {
		t.Fatal("B 应有独立额度")
	}
	if ok, _ := limiter.Allow("A"); ok {
		t.Fatal("A 第二次应被限制")
	}
}
