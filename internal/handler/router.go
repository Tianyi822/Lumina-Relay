package handler

import (
	"github.com/gin-gonic/gin"

	"lumina-relay/internal/service"
)

// Deps 是构造路由所需的依赖集合。
// 随 Task 推进增量添加（每个 handler Task 的 Green 阶段补字段）。
//
// 集中在 Deps 而非全局变量，便于测试注入与生命周期管理。
type Deps struct {
	AccountService *service.AccountService
	JWTSecret      []byte // 签发/解析 session token 的 HS256 密钥
}

// NewRouter 基于 deps 构造 *gin.Engine 并注册全部已实现的路由。
// 随端点增加，这里增量注册（每个 handler Task 的 Green 阶段加一行）。
func NewRouter(deps Deps) *gin.Engine {
	r := gin.New()
	r.GET("/health", Health)
	r.POST("/account/register", RegisterAccount(deps))
	return r
}

