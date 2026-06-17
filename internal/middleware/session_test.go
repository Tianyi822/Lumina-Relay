package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/auth"
	"lumina-relay/internal/db"
)

// sessionTestEnv 装配真实 DB + JWT secret，供 session 中间件测试。
type sessionTestEnv struct {
	q          *db.Queries
	jwtSecret  []byte
	cleanup    func()
	accountID  string
	deviceID   string
	devicePriv string // hex 设备公钥（devices 表里存的）
}

func newSessionTestEnv(t *testing.T) *sessionTestEnv {
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
	e := &sessionTestEnv{
		q:         q,
		jwtSecret: []byte("session-test-secret-32-bytes!!!"),
		cleanup:   func() { _ = backend.Close() },
	}
	e.seedAccountAndDevice(t)
	return e
}

// seedAccountAndDevice 建账户 + 首台设备，供签发 token。
func (e *sessionTestEnv) seedAccountAndDevice(t *testing.T) {
	t.Helper()
	e.accountID = "acc-session"
	e.deviceID = "dev-session"
	e.devicePriv = "pub-hex-abc"
	ctx := context.Background()
	if err := e.q.CreateAccount(ctx, db.CreateAccountParams{
		AccountID: e.accountID, RecoveryCodeHash: []byte("h"),
		DekSalt: []byte("s"), DekNonce: []byte("n"), DekCt: []byte("c"), CreatedAt: 1,
	}); err != nil {
		t.Fatalf("建账户失败：%v", err)
	}
	if err := e.q.CreateDevice(ctx, db.CreateDeviceParams{
		DeviceID: e.deviceID, AccountID: e.accountID,
		DevicePubKey: e.devicePriv, DeviceName: "dev", CreatedAt: 1,
	}); err != nil {
		t.Fatalf("建设备失败：%v", err)
	}
}

// makeToken 签发当前设备的 session token。
func (e *sessionTestEnv) makeToken(t *testing.T) string {
	t.Helper()
	tok, err := auth.IssueToken(e.jwtSecret, e.accountID, e.deviceID)
	if err != nil {
		t.Fatalf("签发 token 失败：%v", err)
	}
	return tok
}

// TestRequireSession_MissingToken 验证无 Authorization 头返回 401。
func TestRequireSession_MissingToken(t *testing.T) {
	env := newSessionTestEnv(t)
	defer env.cleanup()

	r := gin.New()
	r.Use(RequireSession(env.q, env.jwtSecret))
	r.GET("/x", func(c *gin.Context) { c.Status(200) })

	rec := getWithBearer(r, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无 token 应 401，得到 %d", rec.Code)
	}
}

// TestRequireSession_ValidToken 验证合法 token 通过，且 context 含 accountId/deviceId。
func TestRequireSession_ValidToken(t *testing.T) {
	env := newSessionTestEnv(t)
	defer env.cleanup()

	var gotAccount, gotDevice string
	r := gin.New()
	r.Use(RequireSession(env.q, env.jwtSecret))
	r.GET("/x", func(c *gin.Context) {
		gotAccount = c.GetString("accountId")
		gotDevice = c.GetString("deviceId")
		c.Status(200)
	})

	rec := getWithBearer(r, "Bearer "+env.makeToken(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("合法 token 应 200，得到 %d，body=%s", rec.Code, rec.Body.String())
	}
	if gotAccount != env.accountID {
		t.Errorf("context accountId = %q, want %q", gotAccount, env.accountID)
	}
	if gotDevice != env.deviceID {
		t.Errorf("context deviceId = %q, want %q", gotDevice, env.deviceID)
	}
}

// TestRequireSession_RevokedDevice 验证已吊销设备的 token 返回 401 device_revoked。
func TestRequireSession_RevokedDevice(t *testing.T) {
	env := newSessionTestEnv(t)
	defer env.cleanup()

	// 吊销设备
	if _, err := env.q.RevokeDevice(context.Background(), env.deviceID, 999); err != nil {
		t.Fatalf("吊销失败：%v", err)
	}

	r := gin.New()
	r.Use(RequireSession(env.q, env.jwtSecret))
	r.GET("/x", func(c *gin.Context) { c.Status(200) })

	rec := getWithBearer(r, "Bearer "+env.makeToken(t))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("已吊销设备应 401，得到 %d", rec.Code)
	}
}

// TestRequireSession_BadToken 验证非法 token 返回 401。
func TestRequireSession_BadToken(t *testing.T) {
	env := newSessionTestEnv(t)
	defer env.cleanup()

	r := gin.New()
	r.Use(RequireSession(env.q, env.jwtSecret))
	r.GET("/x", func(c *gin.Context) { c.Status(200) })

	rec := getWithBearer(r, "Bearer not.a.valid.token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("非法 token 应 401，得到 %d", rec.Code)
	}
}

// getWithBearer 发 GET，可设 Authorization 头。
func getWithBearer(r http.Handler, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}
