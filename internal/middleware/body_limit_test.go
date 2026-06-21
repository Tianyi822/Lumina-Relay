package middleware

import (
	"io"
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

// TestHandleBodyReadError_MaxBytesErrorAbortsOnly 验证当读取错误是 *http.MaxBytesError
//（MaxBytesReader 触发的超限）时，HandleBodyReadError 仅 Abort 中间件链，
// 不再写第二个响应（C1/C2 回归：防止双写损坏）。
//
// 注：真实部署中 MaxBytesReader 会先向 ResponseWriter 写 413；
// httptest.ResponseRecorder 不触发该副作用，故此处只验证"不再追加写"。
func TestHandleBodyReadError_MaxBytesErrorAbortsOnly(t *testing.T) {
	r := gin.New()
	r.GET("/x", func(c *gin.Context) {
		maxErr := &http.MaxBytesError{Limit: maxJSONBody}
		handled := HandleBodyReadError(c, maxErr, http.StatusBadRequest, "bad_request", "不应写入此消息")
		if !handled {
			t.Error("MaxBytesError 应被处理（返回 true）")
		}
		// 若 Abort 生效，后续代码不执行；此处手动写以验证它"不应该被到达"
		// （AbortWithStatusJSON 之后的代码 gin 仍会执行，除非显式 return；
		//  handler 里有 return，所以这行测的是"没触发 fallback 写"）
	})
	r.GET("/y", func(c *gin.Context) {
		// 普通 read 错误：应走 fallback 写 fallbackCode
		HandleBodyReadError(c, io.ErrUnexpectedEOF, http.StatusBadRequest, "bad_request", "读取失败")
	})

	// /x：MaxBytesError 分支，body 不含 fallback 消息
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if strings.Contains(rec.Body.String(), "不应写入此消息") {
		t.Errorf("MaxBytesError 分支不应写 fallback body：%q", rec.Body.String())
	}

	// /y：普通错误，body 含 fallback 消息
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/y", nil))
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("普通错误 status=%d, want 400", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "读取失败") {
		t.Errorf("普通错误应写 fallback body：%q", rec2.Body.String())
	}
}

// TestHandleBodyReadError_NilErrorReturnsFalse 验证无错误时不处理。
func TestHandleBodyReadError_NilErrorReturnsFalse(t *testing.T) {
	r := gin.New()
	r.GET("/x", func(c *gin.Context) {
		if HandleBodyReadError(c, nil, 400, "bad", "msg") {
			t.Error("nil error 不应返回 true")
		}
		c.Status(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
