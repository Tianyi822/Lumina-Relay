package server

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/auth"
	"lumina-relay/internal/db"
	"lumina-relay/internal/handler"
	"lumina-relay/internal/service"
	"lumina-relay/internal/store"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// integEnv 是端到端集成测试环境：真实 DB + 全部 service + BlockStore + JWT + NonceStore。
type integEnv struct {
	router    *gin.Engine
	jwtSecret []byte
	cleanup   func()
}

func newIntegEnv(t *testing.T) *integEnv {
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
	secret := []byte("integration-jwt-secret-32-bytes!")
	bs := store.NewBlockStore(filepath.Join(t.TempDir(), "blocks"))
	nonceStore := auth.NewNonceStore(5 * time.Minute)
	deps := handler.Deps{
		AccountService:  service.NewAccountService(q),
		DeviceService:   service.NewDeviceService(q),
		ManifestService: service.NewManifestService(q),
		BlocksService:   service.NewBlocksService(q, bs, 1024),
		JWTSecret:       secret,
		Queries:         q,
		NonceStore:      nonceStore,
	}
	return &integEnv{
		router:    handler.NewRouter(deps),
		jwtSecret: secret,
		cleanup:   func() { _ = backend.Close() },
	}
}

// TestIntegration_RegisterUploadManifestFlow 验证完整业务链路：
// 1) 注册账户+设备（获得 sessionToken）
// 2) PUT 一个密文块
// 3) PUT manifest（引用该块）
// 4) GET manifest 验证内容一致
//
// 这是计划 Task 15 的核心：若前面 Task 均完成则应通过；
// 失败说明路由/wiring 有缺口。
func TestIntegration_RegisterUploadManifestFlow(t *testing.T) {
	env := newIntegEnv(t)
	defer env.cleanup()

	// 1. 注册：生成真 Ed25519 密钥对，注册账户+设备
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("生成密钥对失败：%v", err)
	}
	regBody := map[string]string{
		"recoveryCodeHash": handler.TestRecoveryHashHex,
		"dekSalt":          "73616c74",
		"dekNonce":         "6e6f6e6365",
		"dekCt":            "6374",
		"devicePubKey":     hex.EncodeToString(pub),
		"deviceName":       "integration-device",
	}
	regRec := env.doJSON(http.MethodPost, "/account/register", regBody, "")
	if regRec.Code != http.StatusCreated {
		t.Fatalf("注册失败：%d body=%s", regRec.Code, regRec.Body.String())
	}
	var reg struct {
		AccountID    string `json:"accountId"`
		DeviceID     string `json:"deviceId"`
		SessionToken string `json:"sessionToken"`
	}
	json.Unmarshal(regRec.Body.Bytes(), &reg)
	if reg.AccountID == "" || reg.DeviceID == "" || reg.SessionToken == "" {
		t.Fatalf("注册响应字段缺失：%+v", reg)
	}

	// 2. PUT 一个密文块（双层认证）
	blockData := []byte("encrypted block payload for integration")
	sum := sha256Sum(blockData)
	blockID := hex.EncodeToString(sum[:])
	blockRec := env.signedReq(http.MethodPut, reg.SessionToken, priv, "/blocks/"+blockID, blockData)
	if blockRec.Code != http.StatusCreated {
		t.Fatalf("PUT 块失败：%d body=%s", blockRec.Code, blockRec.Body.String())
	}

	// 3. PUT manifest（双层认证，baseVersion=0）
	manifestCT := []byte("encrypted manifest referencing the block")
	manifestHex := hex.EncodeToString(manifestCT)
	manBody, _ := json.Marshal(map[string]any{"ciphertext": manifestHex, "baseVersion": 0})
	manRec := env.signedReq(http.MethodPut, reg.SessionToken, priv, "/manifest", manBody)
	if manRec.Code != http.StatusOK {
		t.Fatalf("PUT manifest 失败：%d body=%s", manRec.Code, manRec.Body.String())
	}
	var manResp struct {
		Version int `json:"version"`
	}
	json.Unmarshal(manRec.Body.Bytes(), &manResp)
	if manResp.Version != 1 {
		t.Fatalf("manifest version = %d, want 1", manResp.Version)
	}

	// 4. GET manifest 验证内容（Session 认证）
	getRec := env.doJSON(http.MethodGet, "/manifest", nil, reg.SessionToken)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET manifest 失败：%d", getRec.Code)
	}
	var getResp struct {
		Version    int    `json:"version"`
		Ciphertext string `json:"ciphertext"`
	}
	json.Unmarshal(getRec.Body.Bytes(), &getResp)
	if getResp.Version != 1 {
		t.Errorf("GET manifest version = %d, want 1", getResp.Version)
	}
	if getResp.Ciphertext != manifestHex {
		t.Errorf("GET manifest ciphertext = %q, want %q", getResp.Ciphertext, manifestHex)
	}
}

// TestIntegration_SecondManifestVersion 验证乐观并发：第二次 PUT 用 baseVersion=1 成功推进。
func TestIntegration_SecondManifestVersion(t *testing.T) {
	env := newIntegEnv(t)
	defer env.cleanup()

	pub, priv, _ := ed25519.GenerateKey(nil)
	tok := registerAndGetToken(t, env, pub)

	ct1 := hex.EncodeToString([]byte("manifest-v1"))
	body1, _ := json.Marshal(map[string]any{"ciphertext": ct1, "baseVersion": 0})
	env.signedReq(http.MethodPut, tok, priv, "/manifest", body1)

	// 第二次 baseVersion=1 → version 2
	ct2 := hex.EncodeToString([]byte("manifest-v2"))
	body2, _ := json.Marshal(map[string]any{"ciphertext": ct2, "baseVersion": 1})
	rec := env.signedReq(http.MethodPut, tok, priv, "/manifest", body2)
	if rec.Code != http.StatusOK {
		t.Fatalf("第二次 PUT 应 200，得到 %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Version int `json:"version"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Version != 2 {
		t.Fatalf("version = %d, want 2", resp.Version)
	}
}

// registerAndGetToken 注册并返回 sessionToken。
func registerAndGetToken(t *testing.T, env *integEnv, pub ed25519.PublicKey) string {
	t.Helper()
	body := map[string]string{
		"recoveryCodeHash": handler.TestRecoveryHashHex,
		"dekSalt":          "73616c74",
		"dekNonce":         "6e6f6e6365",
		"dekCt":            "6374",
		"devicePubKey":     hex.EncodeToString(pub),
		"deviceName":       "dev",
	}
	rec := env.doJSON(http.MethodPost, "/account/register", body, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("注册失败：%d", rec.Code)
	}
	var resp struct {
		SessionToken string `json:"sessionToken"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	return resp.SessionToken
}

// doJSON 发送 JSON 请求。authBearer 非空时设 Authorization。
func (e *integEnv) doJSON(method, path string, body any, authBearer string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewBuffer(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if authBearer != "" {
		req.Header.Set("Authorization", "Bearer "+authBearer)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

// signedReq 构造合法签名的请求。nonce 每次唯一。
func (e *integEnv) signedReq(method, token string, priv ed25519.PrivateKey, path string, body []byte) *httptest.ResponseRecorder {
	tsStr := strconv.FormatInt(time.Now().UnixMilli(), 10)
	nonce := strconv.FormatInt(time.Now().UnixNano(), 16)
	canon := auth.BuildCanonical(method, path, tsStr, nonce, body)
	sig := ed25519.Sign(priv, []byte(canon))

	req := httptest.NewRequest(method, path, bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Timestamp", tsStr)
	req.Header.Set("X-Nonce", nonce)
	req.Header.Set("X-Signature", hex.EncodeToString(sig))
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

// sha256Sum 计算内容的 sha256（与 service 内一致）。
func sha256Sum(b []byte) [32]byte {
	return sha256.Sum256(b)
}
