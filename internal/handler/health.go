// Package handler 实现 lumina-relay 的 HTTP 请求处理。
//
// Handler 层是"薄编排"：解析请求 → 调用 service → 用 apperr 映射响应。
// 业务逻辑在 internal/service/；数据访问在 internal/db/。
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Health 返回服务存活状态。无依赖检查（DB/JWT 等的健康探测留给后续）。
// 响应：200 {"status":"ok"}。
func Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
