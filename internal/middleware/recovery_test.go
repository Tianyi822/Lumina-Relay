package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestRecovery_ReturnsInternalErrorOnPanic 验证 handler panic 被转成统一
// 500 错误体（而非 net/http 默认的连接重置），错误码为 internal_error。
func TestRecovery_ReturnsInternalErrorOnPanic(t *testing.T) {
	r := gin.New()
	r.Use(Recovery())
	r.GET("/boom", func(c *gin.Context) { panic("boom") })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "internal_error") {
		t.Fatalf("body = %s, want 包含 internal_error", body)
	}
}

// TestRecovery_PassesThroughNormalResponses 验证无 panic 时正常响应不受影响。
func TestRecovery_PassesThroughNormalResponses(t *testing.T) {
	r := gin.New()
	r.Use(Recovery())
	r.GET("/ok", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
