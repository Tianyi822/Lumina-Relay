package middleware

import (
	"github.com/gin-gonic/gin"
)

// SecurityHeaders 返回一个 gin 中间件，为所有响应注入安全相关 HTTP 头。
//
// 这是 API 服务（非浏览器渲染目标）的基线防护：
//   - X-Content-Type-Options: nosniff      — 禁止浏览器 MIME 嗅探
//   - X-Frame-Options: DENY                — 禁止被任意页面 iframe 嵌入（防点击劫持）
//   - Referrer-Policy: no-referrer         — 不泄露 Referer
//   - Cache-Control: no-store              — 响应含敏感数据，禁止缓存
//   - Strict-Transport-Security            — 强制 HTTPS（仅 HTTPS 请求生效；见下）
//
// HSTS 仅在 HTTPS（TLS 终止于本服务或反代透传 X-Forwarded-Proto=https）时发送，
// 避免在明文 HTTP 首次访问时被中间人剥离头。max-age=63072000（2 年），含 preload。
//
// 即使当前客户端是原生 App（不解析这些头），加上无害且为未来 Web 客户端预留防护。
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")

		// HSTS 仅 HTTPS 下发送（r.TLS != nil 表示 TLS 直连本服务；
		// X-Forwarded-Proto=https 表示 TLS 由前置反代终止）。
		if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
		}

		c.Next()
	}
}
