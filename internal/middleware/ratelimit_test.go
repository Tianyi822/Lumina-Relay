package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// TestIPLimiter_BlocksAfterMax 验证：同 IP 连续请求 max 次后，第 max+1 次返回 429
// 且 body 含 rate_limited code（见 sync-design §6.6 / §6.5）。
//
// 计划 Task 10a Step 1 要求：max+1 次最后一次 429 + rate_limited。
func TestIPLimiter_BlocksAfterMax(t *testing.T) {
	max := 3
	limiter := NewIPLimiter(max, time.Minute)

	r := gin.New()
	r.Use(IPLimit(limiter))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	// 前 max 次应通过
	for i := 0; i < max; i++ {
		rec := doGet(r, "1.2.3.4")
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求应为 200，得到 %d", i+1, rec.Code)
		}
	}

	// 第 max+1 次应被限流
	rec := doGet(r, "1.2.3.4")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("超限请求应为 429，得到 %d", rec.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if body.Error.Code != "rate_limited" {
		t.Fatalf("code = %q, want rate_limited", body.Error.Code)
	}
}

// TestIPLimiter_DistinctIPsIndependent 验证不同 IP 各自有独立配额。
func TestIPLimiter_DistinctIPsIndependent(t *testing.T) {
	limiter := NewIPLimiter(2, time.Minute)
	r := gin.New()
	r.Use(IPLimit(limiter))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	// IP-A 用满 2 次
	doGet(r, "10.0.0.1")
	doGet(r, "10.0.0.1")
	// IP-B 第一次仍应成功（独立配额）
	if rec := doGet(r, "10.0.0.2"); rec.Code != http.StatusOK {
		t.Fatalf("不同 IP 应有独立配额，得到 %d", rec.Code)
	}
}

// TestIPLimiter_WindowReset 验证窗口滚动后配额恢复。
// 用可控时钟推进时间，避免真睡 1 分钟。
func TestIPLimiter_WindowReset(t *testing.T) {
	now := time.Now()
	limiter := NewIPLimiter(2, time.Minute, withNow(func() time.Time { return now }))

	if !limiter.Allow("ip") {
		t.Fatal("第 1 次应允许")
	}
	if !limiter.Allow("ip") {
		t.Fatal("第 2 次应允许")
	}
	if limiter.Allow("ip") {
		t.Fatal("第 3 次应拒绝")
	}

	// 推进到窗口之后，配额恢复
	now = now.Add(time.Minute + time.Second)
	if !limiter.Allow("ip") {
		t.Fatal("窗口滚动后应重新允许")
	}
}

// TestIPLimiter_ConcurrentSafe 验证并发调用不会让超限请求漏过。
func TestIPLimiter_ConcurrentSafe(t *testing.T) {
	limiter := NewIPLimiter(5, time.Minute)
	var allowed int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if limiter.Allow("same-ip") {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 5 {
		t.Fatalf("并发下应只允许 5 次，实际 %d", allowed)
	}
}

// doGet 发一次 GET，remoteAddr 模拟来源 IP。
func doGet(r http.Handler, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = ip + ":1234"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}
