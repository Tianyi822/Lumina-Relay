package middleware

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestSecurityHeaders_SetsBaseline 验证所有基线安全头都被设置（HSTS 除外，需 HTTPS）。
func TestSecurityHeaders_SetsBaseline(t *testing.T) {
	r := gin.New()
	r.Use(SecurityHeaders())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.ServeHTTP(rec, req)

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"Cache-Control":          "no-store",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	// 明文 HTTP 不应发 HSTS（避免首访被中间人剥离）
	if hsts := rec.Header().Get("Strict-Transport-Security"); hsts != "" {
		t.Errorf("明文 HTTP 不应发 HSTS，得到 %q", hsts)
	}
}

// TestSecurityHeaders_HSTS_WithTLS 验证 TLS 直连时发 HSTS。
func TestSecurityHeaders_HSTS_WithTLS(t *testing.T) {
	r := gin.New()
	r.Use(SecurityHeaders())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.TLS = &tls.ConnectionState{HandshakeComplete: true} // 模拟 TLS 直连
	r.ServeHTTP(rec, req)

	hsts := rec.Header().Get("Strict-Transport-Security")
	if hsts == "" {
		t.Fatal("TLS 请求应发 HSTS，但头为空")
	}
	if want := "max-age=63072000; includeSubDomains; preload"; hsts != want {
		t.Errorf("HSTS = %q, want %q", hsts, want)
	}
}

// TestSecurityHeaders_HSTS_WithForwardedProto 验证反代透传 X-Forwarded-Proto=https 时发 HSTS。
func TestSecurityHeaders_HSTS_WithForwardedProto(t *testing.T) {
	r := gin.New()
	r.Use(SecurityHeaders())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	r.ServeHTTP(rec, req)

	if hsts := rec.Header().Get("Strict-Transport-Security"); hsts == "" {
		t.Fatal("X-Forwarded-Proto=https 应发 HSTS，但头为空")
	}
}

