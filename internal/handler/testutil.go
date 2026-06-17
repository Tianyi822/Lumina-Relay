package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/auth"
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

// registerBody 是注册请求的测试用 body 结构（字段对齐 sync-design §213）。
type registerBody struct {
	RecoveryCodeHash string `json:"recoveryCodeHash"`
	DekSalt          string `json:"dekSalt"`
	DekNonce         string `json:"dekNonce"`
	DekCt            string `json:"dekCt"`
	DevicePubKey     string `json:"devicePubKey"`
	DeviceName       string `json:"deviceName"`
}

// newTestEnv 构造一个接好真实 DB + AccountService + DeviceService + JWT 的测试环境。
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
		DeviceService:  service.NewDeviceService(q),
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

// doGET 发送 GET 请求（testEnv 方法，供需要带 env 的测试用）。
func (e *testEnv) doGET(target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

// registerAccount 通过 POST /account/register 注册一个账户，返回 accountId。
// 供依赖既有账户的端点测试（如 GET /account/dek）快速准备数据。
func (e *testEnv) registerAccount(t *testing.T, body registerBody) string {
	t.Helper()
	rec := e.doPOST("/account/register", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("registerAccount 失败：status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		AccountID string `json:"accountId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析注册响应失败：%v", err)
	}
	return resp.AccountID
}

// registerAccountWithHash 注册账户并返回 (accountId, recoveryCodeHashHex)。
// 设备注册测试需用同一 recoveryCodeHash 模拟客户端正确输入。
func (e *testEnv) registerAccountWithHash(t *testing.T) (string, string) {
	t.Helper()
	hashHex := "686173686564" // "hashed" 的 hex，作为约定恢复码哈希
	accountID := e.registerAccount(t, registerBody{
		RecoveryCodeHash: hashHex,
		DekSalt:          "73616c74",
		DekNonce:         "6e6f6e6365",
		DekCt:            "6374",
		DevicePubKey:     "aabb",
		DeviceName:       "first-device",
	})
	return accountID, hashHex
}

// parseSessionToken 用 env 的 JWT secret 解析 sessionToken，返回 auth.Claims。
func (e *testEnv) parseSessionToken(token string) (auth.Claims, error) {
	return auth.ParseToken(e.jwtSecret, token)
}

// doPOSTRaw 发送原始字节 POST（供非法 JSON 测试用）。
func (e *testEnv) doPOSTRaw(target string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewBufferString(body))
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
