package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

	// 故意用错误的 hash（32 字节合法长度但内容错，确保走 service 层校验返回 401）
	body := map[string]string{
		"accountId":        accountID,
		"recoveryCodeHash": "00" + strings.Repeat("11", 31), // 32 字节，内容错误
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

// TestListDevices_ReturnsActive 验证 GET /devices 返回当前账户下未吊销设备列表。
// 注册带真实公钥的账户（首台设备即 "signed-dev"），再注册第二台设备，列表应有 2 项。
func TestListDevices_ReturnsActive(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	accountID, _, token, _ := env.registerSignedAccount(t)

	// 注册第二台设备（用同一 recoveryCodeHash）
	rec := env.doPOST("/device/register", map[string]string{
		"accountId":        accountID,
		"recoveryCodeHash": testRecoveryHashHex,
		"devicePubKey":     "aabbcc",
		"deviceName":       "second-dev",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("注册第二台设备失败：status=%d body=%s", rec.Code, rec.Body.String())
	}

	// 拉 /devices（带 session token）
	req := httptest.NewRequest(http.MethodGet, "/devices", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = env.serve(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	var list []struct {
		DeviceID     string `json:"deviceId"`
		DeviceName   string `json:"deviceName"`
		DevicePubKey string `json:"devicePubKey"`
		CreatedAt    int64  `json:"createdAt"`
		LastSeenAt   int64  `json:"lastSeenAt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if len(list) != 2 {
		t.Fatalf("设备数 = %d, want 2", len(list))
	}
	names := map[string]bool{}
	for _, d := range list {
		names[d.DeviceName] = true
		if d.LastSeenAt == 0 {
			t.Errorf("设备 %s 的 lastSeenAt 不应为 0", d.DeviceID)
		}
	}
	if !names["signed-dev"] || !names["second-dev"] {
		t.Errorf("设备名不匹配：%v", names)
	}
}

// TestListDevices_NoToken 验证无 session 调 GET /devices 返 401。
func TestListDevices_NoToken(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	req := httptest.NewRequest(http.MethodGet, "/devices", nil)
	rec := env.serve(req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
