package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestBodyLimitJSON_RejectsOversized 验证 JSON body 超过 64 KiB 时返回 413。
func TestBodyLimitJSON_RejectsOversized(t *testing.T) {
	r := gin.New()
	r.Use(BodyLimitJSON())
	r.POST("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	// 构造 > 64 KiB 的 body
	oversized := strings.Repeat("x", 70*1024)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(oversized))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413（body 超 64KiB）", rec.Code)
	}
}

// TestBodyLimitJSON_AllowsNormal 验证正常大小 body 通过。
func TestBodyLimitJSON_AllowsNormal(t *testing.T) {
	r := gin.New()
	r.Use(BodyLimitJSON())
	r.POST("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"ok":true}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（正常 body 应通过）", rec.Code)
	}
}

// TestBodyLimitBlock_RejectsOversized 验证块上传超过 1 MiB 返回 413。
func TestBodyLimitBlock_RejectsOversized(t *testing.T) {
	r := gin.New()
	r.Use(BodyLimitBlock())
	r.PUT("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	// 构造 > 1 MiB 的 body
	oversized := strings.Repeat("x", 1<<20+1024) // 1MiB + 1KiB
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(oversized))
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413（块超 1MiB）", rec.Code)
	}
}

// TestBodyLimitBlock_AllowsUnderLimit 验证 1 MiB 以内的块通过。
func TestBodyLimitBlock_AllowsUnderLimit(t *testing.T) {
	r := gin.New()
	r.Use(BodyLimitBlock())
	r.PUT("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	// 刚好 1 MiB - 1 字节
	data := strings.Repeat("x", (1<<20)-1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(data))
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（块在 1MiB 内应通过）", rec.Code)
	}
}
