package middleware

import (
	"runtime/debug"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/apperr"
	"lumina-relay/internal/logger"
)

// Recovery 返回一个 gin 中间件，捕获 handler 链中的 panic。
// 无此中间件时，net/http 对连接级 panic 只做"关闭连接 + stderr 打印"，
// 客户端收到连接重置而非明确 500，且服务端无结构化日志。本中间件记录
// 结构化错误（含堆栈）并返回统一错误体（{error:{code:"internal_error"}}）。
//
// 与 logger.Recover 的关系：handler 运行在 net/http 的请求 goroutine 中，
// gin 的 recovery 在其链内捕获，无需 re-panic；子 goroutine（WS 读循环、
// 后台 GC）仍需在各自入口 defer logger.RecoverLogOnly()。
func Recovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered any) {
		fields := []logger.Field{
			logger.Any("panic", recovered),
			logger.String("method", c.Request.Method),
			logger.String("path", c.Request.URL.Path),
			logger.String("client_ip", c.ClientIP()),
			logger.String("stack", string(debug.Stack())),
		}
		// session 中间件可能已注入 accountId，附带记录便于按账户追踪 panic 来源。
		if accountID, ok := c.Get(CtxAccountID); ok {
			if id, ok := accountID.(string); ok && id != "" {
				fields = append(fields, logger.String("account_id", id))
			}
		}
		logger.Error("handler panic recovered", fields...)
		apiErr := apperr.New(apperr.CodeInternalError, "服务内部错误")
		apiErr.WriteJSON(c.Writer)
		c.Abort()
	})
}
