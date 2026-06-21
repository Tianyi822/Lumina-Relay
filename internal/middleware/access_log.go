package middleware

import (
	"time"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/logger"
)

// 健康检查路径：高频探测，记录会刷屏且无业务价值，中间件内短路跳过。
const healthPath = "/health"

// AccessLog 返回一个 gin 中间件，记录每个请求的访问日志到统一 logger（zap/slog）。
//
// 级别按状态码分级：
//   - 2xx/3xx → Info（正常）
//   - 4xx     → Warn（客户端错误：可能是攻击或误用）
//   - 5xx     → Error（服务端故障，需关注）
//
// 字段：method、path、status、latency_ms、client_ip、account_id（session 注入后才有）。
// GET /health 不记录（高频探测无业务价值）。
func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 健康检查短路：不计时、不记录。
		if c.Request.URL.Path == healthPath {
			c.Next()
			return
		}

		start := time.Now()
		c.Next()
		latencyMs := time.Since(start).Milliseconds()

		fields := []logger.Field{
			logger.String("method", c.Request.Method),
			logger.String("path", c.Request.URL.Path),
			logger.Int("status", c.Writer.Status()),
			logger.Int64("latency_ms", latencyMs),
			logger.String("client_ip", c.ClientIP()),
		}
		// 若 session 中间件已注入 accountId，附带记录（便于按账户追踪）。
		if accountID, ok := c.Get(CtxAccountID); ok {
			if id, ok := accountID.(string); ok && id != "" {
				fields = append(fields, logger.String("account_id", id))
			}
		}

		status := c.Writer.Status()
		switch {
		case status >= 500:
			logger.Error("http_request", fields...)
		case status >= 400:
			logger.Warn("http_request", fields...)
		default:
			logger.Info("http_request", fields...)
		}
	}
}
