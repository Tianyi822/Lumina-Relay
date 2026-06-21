package handler

import (
	"os"
	"testing"

	"lumina-relay/internal/logger"
)

// TestMain 确保访问日志中间件（经 NewRouter 挂载）在测试中不 nil panic。
// 生产中 main.go 调用 InitBootstrap；测试环境在此对齐。
func TestMain(m *testing.M) {
	logger.InitBootstrap()
	os.Exit(m.Run())
}
