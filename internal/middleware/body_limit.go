package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// 请求体大小上限（防 OOM）。
const (
	// maxBlockBody 块上传单块上限（加密块通常 16-256KB，1MB 留足余量）。
	maxBlockBody int64 = 1 << 20 // 1 MiB
	// maxJSONBody 连接、邀请码和块索引等 JSON 请求体上限。
	maxJSONBody int64 = 1 << 16 // 64 KiB
	// maxManifestBody 设备级加密 Manifest 上限。
	maxManifestBody int64 = 4 << 20 // 4 MiB
)

// bodyLimitExceeded 是超限时的统一错误响应。
var bodyLimitExceeded = gin.H{
	"error": gin.H{
		"code":    "body_too_large",
		"message": "请求体超过大小上限",
	},
}

// BodyLimitBlock 限制块上传（application/octet-stream）body 不超过 1 MiB。
// 挂在 PUT /blocks/:blockId 路由上（在 RequireDeviceProof 之前）。
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

// BodyLimitManifest 限制原始密文 Manifest 请求体。
func BodyLimitManifest() gin.HandlerFunc {
	return func(c *gin.Context) {
		limitBody(c, maxManifestBody)
	}
}

// BodyLimitSessionFile 限制原始会话 JSONL 文件（含 index.json）请求体。
// 与 Manifest 同为 4 MiB 上限。
func BodyLimitSessionFile() gin.HandlerFunc {
	return func(c *gin.Context) {
		limitBody(c, maxManifestBody)
	}
}

// limitBody 用 http.MaxBytesReader 包裹请求体。
//
// 两道防线：
//  1. 若请求声明了 Content-Length 且超限，立即 AbortWithStatusJSON(413)。
//  2. 否则（chunked / 无 Content-Length）包裹后放行，由下游读取触发 MaxBytesReader
//     超限。此时下游的 io.ReadAll 会返回 *http.MaxBytesError，调用
//     HandleBodyReadError 统一处理（不再重复写响应）。
func limitBody(c *gin.Context, max int64) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max)
	if c.Request.ContentLength > max {
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, bodyLimitExceeded)
		return
	}
	c.Next()
}

// HandleBodyReadError 处理下游（signed 中间件 / handler）读取 body 时的错误。
//
// 关键：若是 MaxBytesReader 触发的超限（*http.MaxBytesError），写 413 响应并中止。
//
// 注意：gin 的 ResponseWriter 不实现 stdlib 的 requestTooLarge 接口，故
// MaxBytesReader 不会自动写 413（实测：仅 c.Abort() 会返回 200 空体）。
// 必须在此显式写 413，否则超大上传会静默"成功"（200）且跳过后续校验。
//
// 其他读取错误（如连接中断）按 fallbackStatus + fallbackCode 返回。
//
// 返回 true 表示已处理（调用方应直接 return）。
func HandleBodyReadError(c *gin.Context, err error, fallbackStatus int, fallbackCode, fallbackMsg string) bool {
	if err == nil {
		return false
	}
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		// 显式写 413：gin 不会因 MaxBytesReader 自动写响应
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, bodyLimitExceeded)
		return true
	}
	c.AbortWithStatusJSON(fallbackStatus, gin.H{"error": gin.H{
		"code": fallbackCode, "message": fallbackMsg,
	}})
	return true
}
