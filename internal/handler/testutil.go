package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/db"
	"lumina-relay/internal/service"
)

// init 把 Gin 切到 Release 模式并关掉默认日志输出，避免测试刷屏。
func init() {
	gin.SetMode(gin.TestMode)
}

// testEnv 是 handler 测试的运行环境，持有真实 DB 后端与依赖。
// 随 Task 推进增量添加字段（BlockStore、ManifestService 等）。
type testEnv struct {
	router    *gin.Engine
	jwtSecret []byte
	q         *db.Queries // 供测试做副作用断言
	cleanup   func()
}

// newTestEnv 构造一个接好真实 DB + AccountService + JWT 的测试环境。
// DB 基于 t.TempDir()，不碰 ~/.lumina-relay。cleanup 关闭连接。
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "test.db")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	backend, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	q := db.New(backend)
	secret := []byte("test-jwt-secret-32-bytes-min!!!")
	deps := Deps{
		AccountService: service.NewAccountService(q),
		JWTSecret:      secret,
	}
	return &testEnv{
		router:    NewRouter(deps),
		jwtSecret: secret,
		q:         q,
		cleanup:   func() { _ = backend.Close() },
	}
}

// doPOST 发送 JSON POST 请求，返回 ResponseRecorder。
func (e *testEnv) doPOST(target string, body any) *httptest.ResponseRecorder {
	raw, err := json.Marshal(body)
	if err != nil {
		panic("doPOST 序列化失败：" + err.Error())
	}
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewBuffer(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

// doGET 是 GET 请求的薄封装（health 测试用）。
func doGET(r http.Handler, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}
