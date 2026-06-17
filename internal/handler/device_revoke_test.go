package handler

import (
	"net/http"
	"testing"
)

// TestDeleteDevice_RevokesDevice 验证：
// 1) DELETE /device/:deviceId 成功（204）；
// 2) 随后该设备的 session token 访问受保护端点 → 401 device_revoked。
// 见 sync-design §288-289：吊销后该设备 sessionToken 立即失效。
func TestDeleteDevice_RevokesDevice(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	_, deviceID, tok, priv := env.registerSignedAccount(t)

	// 用合法 session+签名 DELETE 该设备
	rec := env.signedDELETE(t, tok, priv, "/device/"+deviceID)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE 应 204，得到 %d，body=%s", rec.Code, rec.Body.String())
	}

	// 吊销后：用同 token 访问受保护端点 → device_revoked
	req := newRequest(t, http.MethodPut, "/account/dek", `{}`)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec = env.serve(req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("吊销后访问应 401，得到 %d", rec.Code)
	}
	if !containsStr(rec.Body.String(), "device_revoked") {
		t.Errorf("应含 device_revoked，body=%s", rec.Body.String())
	}
}

// TestDeleteDevice_NotFound 验证删除不存在的设备返回 404。
func TestDeleteDevice_NotFound(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	_, _, tok, priv := env.registerSignedAccount(t)

	rec := env.signedDELETE(t, tok, priv, "/device/nonexistent-device")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("删除不存在设备应 404，得到 %d", rec.Code)
	}
}

// containsStr 是最小字符串包含判断。
func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
