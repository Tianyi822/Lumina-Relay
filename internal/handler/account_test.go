package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"lumina-relay/internal/auth"
)

// TestRegisterAccount_CreatesAccountAndDevice 验证：
// 1) POST /account/register 返回 201；
// 2) 响应含 accountId、deviceId、sessionToken；
// 3) 响应**不含** recoveryCode（计划"API 对齐决策"）。
func TestRegisterAccount_CreatesAccountAndDevice(t *testing.T) {
	env := newTestEnv(t) // 构造真实 DB + AccountService + JWT
	body := map[string]string{
		"recoveryCodeHash": "686173686564", // "hashed" 的 hex
		"dekSalt":          "73616c74",     // "salt"
		"dekNonce":         "6e6f6e6365",   // "nonce"
		"dekCt":            "6374",         // "ct"
		"devicePubKey":     "a1b2c3",
		"deviceName":       "iphone",
	}
	rec := env.doPOST("/account/register", body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		AccountID    string `json:"accountId"`
		DeviceID     string `json:"deviceId"`
		SessionToken string `json:"sessionToken"`
		RecoveryCode string `json:"recoveryCode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if resp.AccountID == "" {
		t.Error("accountId 不应为空")
	}
	if resp.DeviceID == "" {
		t.Error("deviceId 不应为空")
	}
	if resp.SessionToken == "" {
		t.Error("sessionToken 不应为空")
	}
	if resp.RecoveryCode != "" {
		t.Errorf("响应不应包含 recoveryCode，得到 %q", resp.RecoveryCode)
	}

	// 验证 sessionToken 可被同一 JWT secret 解析（证明 handler 正确签发）。
	claims, err := auth.ParseToken(env.jwtSecret, resp.SessionToken)
	if err != nil {
		t.Fatalf("签发的 sessionToken 无法解析：%v", err)
	}
	if claims.AccountID != resp.AccountID {
		t.Errorf("token accountId = %q, want %q", claims.AccountID, resp.AccountID)
	}
	if claims.DeviceID != resp.DeviceID {
		t.Errorf("token deviceId = %q, want %q", claims.DeviceID, resp.DeviceID)
	}
}

// TestRegisterAccount_BadJSON 验证非法请求体返回 400 而非 500。
func TestRegisterAccount_BadJSON(t *testing.T) {
	env := newTestEnv(t)
	req := httptest.NewRequest(http.MethodPost, "/account/register", bytes.NewBufferString("{not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应返回 400，得到 %d", rec.Code)
	}
}
