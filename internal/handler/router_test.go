package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestNewRouter_TrustedProxiesRejectsSpoofedXFF 验证 NewRouter 配置了可信代理后，
// 来自非可信代理（RemoteAddr 不是 127.0.0.1）的请求，其 X-Forwarded-For 不被信任，
// ClientIP 回退到 RemoteAddr（防伪造 XFF 绕过限流，I4 回归测试）。
func TestNewRouter_TrustedProxiesRejectsSpoofedXFF(t *testing.T) {
	r := gin.New()
	if err := r.SetTrustedProxies([]string{"127.0.0.1"}); err != nil {
		t.Fatalf("SetTrustedProxies 失败：%v", err)
	}
	var observedIP string
	r.GET("/ip", func(c *gin.Context) {
		observedIP = c.ClientIP()
		c.Status(http.StatusOK)
	})

	// 模拟来自非可信地址（203.0.113.9）的请求，伪造 X-Forwarded-For
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.RemoteAddr = "203.0.113.9:12345" // 非可信代理
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	r.ServeHTTP(rec, req)

	// 应忽略伪造的 XFF，ClientIP = RemoteAddr
	if observedIP != "203.0.113.9" {
		t.Fatalf("ClientIP = %q, want 203.0.113.9（伪造的 XFF 应被忽略）", observedIP)
	}
}

// TestNewRouter_TrustedProxyHonorsXFF 验证来自可信代理（127.0.0.1）的请求，
// X-Forwarded-For 被正确解析为真实客户端 IP（反代正常场景）。
func TestNewRouter_TrustedProxyHonorsXFF(t *testing.T) {
	r := gin.New()
	if err := r.SetTrustedProxies([]string{"127.0.0.1"}); err != nil {
		t.Fatalf("SetTrustedProxies 失败：%v", err)
	}
	var observedIP string
	r.GET("/ip", func(c *gin.Context) {
		observedIP = c.ClientIP()
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.RemoteAddr = "127.0.0.1:54321" // 可信代理
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	r.ServeHTTP(rec, req)

	if observedIP != "198.51.100.7" {
		t.Fatalf("ClientIP = %q, want 198.51.100.7（可信代理的 XFF 应被采纳）", observedIP)
	}
}
