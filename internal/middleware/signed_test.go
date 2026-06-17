package middleware

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/auth"
	"lumina-relay/internal/db"
)

// signedTestEnv 装配真实 DB + 真实 Ed25519 密钥对，供签名中间件测试。
type signedTestEnv struct {
	q         *db.Queries
	jwtSecret []byte
	cleanup   func()
	accountID string
	deviceID  string
	pubKey    ed25519.PublicKey
	privKey   ed25519.PrivateKey
}

func newSignedTestEnv(t *testing.T) *signedTestEnv {
	t.Helper()
	dsn := t.TempDir() + "/test.db"
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	backend, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	q := db.New(backend)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("生成密钥对失败：%v", err)
	}
	e := &signedTestEnv{
		q: q, jwtSecret: []byte("signed-test-secret-32-bytes!!!"),
		cleanup: func() { _ = backend.Close() },
		pubKey: pub, privKey: priv,
		accountID: "acc-signed", deviceID: "dev-signed",
	}
	ctx := context.Background()
	if err := q.CreateAccount(ctx, db.CreateAccountParams{
		AccountID: e.accountID, RecoveryCodeHash: []byte("h"),
		DekSalt: []byte("s"), DekNonce: []byte("n"), DekCt: []byte("c"), CreatedAt: 1,
	}); err != nil {
		t.Fatalf("建账户失败：%v", err)
	}
	if err := q.CreateDevice(ctx, db.CreateDeviceParams{
		DeviceID: e.deviceID, AccountID: e.accountID,
		DevicePubKey: hex.EncodeToString(pub), DeviceName: "dev", CreatedAt: 1,
	}); err != nil {
		t.Fatalf("建设备失败：%v", err)
	}
	return e
}

// newSignedRouter 挂 RequireSession + RequireSignedWrite，供测试。
func (e *signedTestEnv) newSignedRouter(nonceStore *auth.NonceStore) *gin.Engine {
	r := gin.New()
	r.Use(RequireSession(e.q, e.jwtSecret))
	r.Use(RequireSignedWrite(nonceStore))
	r.PUT("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

// signAndPut 构造合法签名 PUT 请求。timestamp 为毫秒。
func (e *signedTestEnv) signAndPut(t *testing.T, r http.Handler, method, path, body string, ts time.Time) *httptest.ResponseRecorder {
	t.Helper()
	tsStr := strconv.FormatInt(ts.UnixMilli(), 10)
	nonce := hex.EncodeToString([]byte("nonce-fixed-16b!")) // 固定 nonce 便于去重测试
	canon := auth.BuildCanonical(method, path, tsStr, nonce, []byte(body))
	sig := ed25519.Sign(e.privKey, []byte(canon))

	tok, err := auth.IssueToken(e.jwtSecret, e.accountID, e.deviceID)
	if err != nil {
		t.Fatalf("签发 token 失败：%v", err)
	}

	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Timestamp", tsStr)
	req.Header.Set("X-Nonce", nonce)
	req.Header.Set("X-Signature", hex.EncodeToString(sig))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestRequireSignedWrite_MissingHeaders 验证缺签名头返回 401。
func TestRequireSignedWrite_MissingHeaders(t *testing.T) {
	env := newSignedTestEnv(t)
	defer env.cleanup()
	nonceStore := auth.NewNonceStore(5 * time.Minute)
	r := env.newSignedRouter(nonceStore)

	// 先签发一个合法 session token 过 RequireSession
	tok, _ := auth.IssueToken(env.jwtSecret, env.accountID, env.deviceID)
	req := httptest.NewRequest(http.MethodPut, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	// 故意不带 X-Timestamp/X-Nonce/X-Signature
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺签名头应 401，得到 %d", rec.Code)
	}
}

// TestRequireSignedWrite_ValidSignature 验证合法签名通过。
func TestRequireSignedWrite_ValidSignature(t *testing.T) {
	env := newSignedTestEnv(t)
	defer env.cleanup()
	nonceStore := auth.NewNonceStore(5 * time.Minute)
	r := env.newSignedRouter(nonceStore)

	now := time.Now()
	rec := env.signAndPut(t, r, http.MethodPut, "/x", "body", now)
	if rec.Code != http.StatusOK {
		t.Fatalf("合法签名应 200，得到 %d，body=%s", rec.Code, rec.Body.String())
	}
}

// TestRequireSignedWrite_ReplayedNonce 验证重复 nonce 被拒（防重放）。
func TestRequireSignedWrite_ReplayedNonce(t *testing.T) {
	env := newSignedTestEnv(t)
	defer env.cleanup()
	nonceStore := auth.NewNonceStore(5 * time.Minute)
	r := env.newSignedRouter(nonceStore)

	now := time.Now()
	// 第一次成功
	if rec := env.signAndPut(t, r, http.MethodPut, "/x", "body", now); rec.Code != http.StatusOK {
		t.Fatalf("首次应 200，得到 %d", rec.Code)
	}
	// 第二次同 nonce 应被拒（用新 timestamp 避开 timestamp 检查路径，但 nonce 相同）
	rec := env.signAndPut(t, r, http.MethodPut, "/x", "body", now.Add(1*time.Second))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("重复 nonce 应 401，得到 %d", rec.Code)
	}
}

// TestRequireSignedWrite_BadSignature 验证签名错误被拒。
func TestRequireSignedWrite_BadSignature(t *testing.T) {
	env := newSignedTestEnv(t)
	defer env.cleanup()
	nonceStore := auth.NewNonceStore(5 * time.Minute)
	r := env.newSignedRouter(nonceStore)

	now := time.Now()
	// 用另一个密钥对签名
	_, otherPriv, _ := ed25519.GenerateKey(nil)
	tsStr := strconv.FormatInt(now.UnixMilli(), 10)
	nonce := "abcdef0123456789"
	canon := auth.BuildCanonical(http.MethodPut, "/x", tsStr, nonce, []byte("body"))
	sig := ed25519.Sign(otherPriv, []byte(canon))

	tok, _ := auth.IssueToken(env.jwtSecret, env.accountID, env.deviceID)
	req := httptest.NewRequest(http.MethodPut, "/x", bytes.NewBufferString("body"))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Timestamp", tsStr)
	req.Header.Set("X-Nonce", nonce)
	req.Header.Set("X-Signature", hex.EncodeToString(sig))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错误签名应 401，得到 %d", rec.Code)
	}
}

// TestRequireSignedWrite_StaleTimestamp 验证超时窗口的 timestamp 被拒。
func TestRequireSignedWrite_StaleTimestamp(t *testing.T) {
	env := newSignedTestEnv(t)
	defer env.cleanup()
	nonceStore := auth.NewNonceStore(5 * time.Minute)
	r := env.newSignedRouter(nonceStore)

	// 6 分钟前，超出 ±5min 窗口
	stale := time.Now().Add(-6 * time.Minute)
	rec := env.signAndPut(t, r, http.MethodPut, "/x", "body", stale)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("过期 timestamp 应 401，得到 %d", rec.Code)
	}
}
