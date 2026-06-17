package handler

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
)

// TestDeviceRegister_ValidRecoveryCode 验证：
// 用正确恢复码 hash 注册设备 → 200 + deviceId + sessionToken。
// 见 sync-design §255-259。
func TestDeviceRegister_ValidRecoveryCode(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	accountID, hashHex := env.registerAccountWithHash(t)

	body := map[string]string{
		"accountId":        accountID,
		"recoveryCodeHash": hashHex,
		"devicePubKey":     "aabbcc",
		"deviceName":       "laptop",
	}
	rec := env.doPOST("/device/register", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		DeviceID     string `json:"deviceId"`
		SessionToken string `json:"sessionToken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if resp.DeviceID == "" {
		t.Error("deviceId 不应为空")
	}
	if resp.SessionToken == "" {
		t.Error("sessionToken 不应为空")
	}

	// sessionToken 应能被 JWT 解析，且绑定到原 accountId
	claims, err := env.parseSessionToken(resp.SessionToken)
	if err != nil {
		t.Fatalf("sessionToken 解析失败：%v", err)
	}
	if claims.AccountID != accountID {
		t.Errorf("token accountId = %q, want %q", claims.AccountID, accountID)
	}
	if claims.DeviceID != resp.DeviceID {
		t.Errorf("token deviceId = %q, want %q", claims.DeviceID, resp.DeviceID)
	}
}

// TestDeviceRegister_BadRecoveryCode 验证恢复码不匹配返回 401。
func TestDeviceRegister_BadRecoveryCode(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	accountID, _ := env.registerAccountWithHash(t)

	// 故意用错误的 hash（合法 hex 但内容错）
	body := map[string]string{
		"accountId":        accountID,
		"recoveryCodeHash": hex.EncodeToString([]byte("totally-wrong")),
		"devicePubKey":     "aabbcc",
		"deviceName":       "laptop",
	}
	rec := env.doPOST("/device/register", body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body=%s", rec.Code, rec.Body.String())
	}
}

// TestDeviceRegister_BadJSON 验证非法 JSON 返回 400。
func TestDeviceRegister_BadJSON(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	rec := env.doPOSTRaw("/device/register", "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应返回 400，得到 %d", rec.Code)
	}
}
