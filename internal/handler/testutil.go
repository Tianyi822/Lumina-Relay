package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// init 把 Gin 切到 Release 模式并关掉默认日志输出，避免测试刷屏。
// testutil 是测试基建，仅 handler 包内部测试引用。
func init() {
	gin.SetMode(gin.TestMode)
}

// newTestEnv 构造最小测试环境。本 Task（8a）只含路由构造所需依赖；
// 后续 Task 按需扩展（DB、JWT secret、BlockStore 等）。
//
// 计划约定：testutil 随 Task 需要逐步增长，每个 Task 的 Green 只添加本 Task 用到的 helper。
type testEnv struct {
	router *gin.Engine
}

// newTestRouter 构造一个挂载了全部已实现路由的 *gin.Engine，供 handler 测试调用。
// 随端点增加，NewRouter 的 deps 也会相应扩展；这里集中注入测试 deps。
func newTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	return NewRouter(Deps{})
}

// doGET 是 GET 请求的薄封装，返回 *httptest.ResponseRecorder。
// 命名为 doGET 而非 GET，避免与关键字/类型混淆。
func doGET(r http.Handler, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}
