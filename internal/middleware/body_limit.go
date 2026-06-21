package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// 请求体大小上限（防 OOM）。
const (
	// maxBlockBody 块上传单块上限（加密块通常 16-256KB，1MB 留足余量）。
	maxBlockBody int64 = 1 << 20 // 1 MiB
	// maxJSONBody JSON 请求体上限（register/manifest/blocks-have 等小 JSON 足够）。
	maxJSONBody int64 = 1 << 16 // 64 KiB
)

// bodyLimitResponse 是超限时的统一错误响应。
var bodyLimitExceeded = struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}{
	Code: "request_too_large", Message: "请求体超过大小上限",
}

// BodyLimitBlock 限制块上传（application/octet-stream）body 不超过 1 MiB。
// 挂在 PUT /blocks/:blockId 路由上（在 RequireSignedWrite 之前）。
func BodyLimitBlock() gin.HandlerFunc {
	return func(c *gin.Context) {
		limitBody(c, maxBlockBody)
	}
}

// BodyLimitJSON 限制 JSON 请求体不超过 64 KiB。
// 挂在 ShouldBindJSON 的端点上。
func BodyLimitJSON() gin.HandlerFunc {
	return func(c *gin.Context) {
		limitBody(c, maxJSONBody)
	}
}

// limitBody 用 http.MaxBytesReader 包裹请求体；超限时 ReadAll/ShouldBindJSON
// 会返回错误，但 MaxBytesReader 在超限时也会触发 immediate 413（net/http）。
// 此处主动写 413 响应并 abort，避免下游 handler 再读取。
func limitBody(c *gin.Context, max int64) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max)
	// 注意：不在此处主动校验 ContentLength（分块传输可能无该头）。
	// 实际超限由下游读取触发；若请求已声明 Content-Length 超限，主动拒绝。
	if c.Request.ContentLength > max {
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": bodyLimitExceeded})
		return
	}
	c.Next()
}
