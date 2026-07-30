package middleware

import (
	"os"
	"testing"

	"lumina-relay/internal/logger"
)

// TestMain 确保访问日志等依赖包级 logger 的中间件在测试中不 nil panic。
// 生产中 main.go 第一行调用 InitBootstrap；测试环境在此对齐。
func TestMain(m *testing.M) {
	logger.InitBootstrap()
	os.Exit(m.Run())
}
