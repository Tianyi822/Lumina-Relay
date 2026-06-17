package handler

import (
	"github.com/gin-gonic/gin"
)

// Deps 是构造路由所需的依赖集合。本 Task（8a）尚无业务依赖；
// 后续 Task 按 service 增量添加字段（AccountService、ManifestService 等）。
//
// 集中在 Deps 而非全局变量，便于测试注入与生命周期管理。
type Deps struct{}

// NewRouter 基于 deps 构造 *gin.Engine 并注册全部已实现的路由。
// 随端点增加，这里增量注册（每个 handler Task 的 Green 阶段加一行）。
func NewRouter(deps Deps) *gin.Engine {
	r := gin.New()
	r.GET("/health", Health)
	return r
}
