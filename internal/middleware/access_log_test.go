package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/logger"
)

// fakeLogger 实现 logger.Logger，捕获调用供断言。仅测试用。
type fakeLogger struct {
	levels    []string             // 每次调用的级别
	fieldsList [][]logger.Field    // 每次调用的字段
}

func (f *fakeLogger) Debug(msg string, fields ...logger.Field) {
	f.record("debug", fields)
}
func (f *fakeLogger) Info(msg string, fields ...logger.Field) {
	f.record("info", fields)
}
func (f *fakeLogger) Warn(msg string, fields ...logger.Field) {
	f.record("warn", fields)
}
func (f *fakeLogger) Error(msg string, fields ...logger.Field) {
	f.record("error", fields)
}
func (f *fakeLogger) With(_ ...logger.Field) logger.Logger { return f }
func (f *fakeLogger) Sync() error                          { return nil }

func (f *fakeLogger) record(level string, fields []logger.Field) {
	f.levels = append(f.levels, level)
	// 复制一份，避免外部 slice 后续修改污染捕获结果
	cp := make([]logger.Field, len(fields))
	copy(cp, fields)
	f.fieldsList = append(f.fieldsList, cp)
}

// TestAccessLog_DoesNotBlockRequest 验证中间件不阻断请求，响应状态正确透传。
// 日志是否真正调用 logger 由 logger 包自身的测试覆盖；此处只验证 HTTP 行为。
func TestAccessLog_DoesNotBlockRequest(t *testing.T) {
	r := gin.New()
	r.Use(AccessLog())
	r.GET("/x", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（中间件不应阻断）", rec.Code)
	}
}

// TestAccessLog_HealthSkipped 验证 GET /health 不产生访问日志。
func TestAccessLog_HealthSkipped(t *testing.T) {
	fake := &fakeLogger{}
	defer logger.SetGlobalForTest(fake)()

	r := gin.New()
	r.Use(AccessLog())
	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if len(fake.levels) != 0 {
		t.Fatalf("/health 不应记访问日志，但捕获到 %d 条", len(fake.levels))
	}
}

// TestAccessLog_LevelByStatus 验证按状态码分级选 logger 方法：
// 2xx/3xx→info, 4xx→warn, 5xx→error。
func TestAccessLog_LevelByStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   string
	}{
		{"2xx → info", http.StatusOK, "info"},
		{"3xx → info", http.StatusFound, "info"},
		{"4xx → warn", http.StatusBadRequest, "warn"},
		{"5xx → error", http.StatusInternalServerError, "error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeLogger{}
			defer logger.SetGlobalForTest(fake)()

			r := gin.New()
			r.Use(AccessLog())
			r.GET("/x", func(c2 *gin.Context) { c2.Status(c.status) })

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			r.ServeHTTP(w, req)

			if len(fake.levels) != 1 {
				t.Fatalf("捕获到 %d 条日志，want 1", len(fake.levels))
			}
			if fake.levels[0] != c.want {
				t.Fatalf("级别 = %q, want %q", fake.levels[0], c.want)
			}
		})
	}
}

// TestAccessLog_CapturesAccountId 验证 session 中间件注入 accountId 后，
// 访问日志带上该字段。
func TestAccessLog_CapturesAccountId(t *testing.T) {
	fake := &fakeLogger{}
	defer logger.SetGlobalForTest(fake)()

	r := gin.New()
	r.Use(AccessLog())
	r.Use(func(c *gin.Context) {
		c.Set(CtxAccountID, "acc-123")
		c.Next()
	})
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.ServeHTTP(w, req)

	if len(fake.fieldsList) != 1 {
		t.Fatalf("捕获到 %d 条日志，want 1", len(fake.fieldsList))
	}
	for _, f := range fake.fieldsList[0] {
		if f.Key == "account_id" {
			if v, ok := f.Val.(string); ok && v == "acc-123" {
				return
			}
		}
	}
	t.Fatalf("日志字段未包含 account_id=acc-123：%+v", fake.fieldsList[0])
}

// TestAccessLog_RequiredFields 验证基础字段（method/path/status/latency/client_ip）都在。
func TestAccessLog_RequiredFields(t *testing.T) {
	fake := &fakeLogger{}
	defer logger.SetGlobalForTest(fake)()

	r := gin.New()
	r.Use(AccessLog())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.ServeHTTP(w, req)

	if len(fake.fieldsList) != 1 {
		t.Fatalf("捕获到 %d 条日志，want 1", len(fake.fieldsList))
	}
	have := map[string]bool{}
	for _, f := range fake.fieldsList[0] {
		have[f.Key] = true
	}
	for _, key := range []string{"method", "path", "status", "latency_ms", "client_ip"} {
		if !have[key] {
			t.Errorf("缺少字段 %q（实际：%v）", key, have)
		}
	}
}
