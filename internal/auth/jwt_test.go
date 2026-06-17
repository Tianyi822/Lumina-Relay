package auth

import (
	"testing"
)

// TestIssueParseSessionToken 验证签发后解析，AccountID/DeviceID 与原始一致。
func TestIssueParseSessionToken(t *testing.T) {
	secret := []byte("test-secret-key-at-least-32-bytes!")
	accountID := "acc-1"
	deviceID := "dev-1"

	token, err := IssueToken(secret, accountID, deviceID)
	if err != nil {
		t.Fatalf("IssueToken 失败：%v", err)
	}
	if token == "" {
		t.Fatal("token 不应为空")
	}

	claims, err := ParseToken(secret, token)
	if err != nil {
		t.Fatalf("ParseToken 失败：%v", err)
	}
	if claims.AccountID != accountID {
		t.Fatalf("AccountID 不一致：got %q, want %q", claims.AccountID, accountID)
	}
	if claims.DeviceID != deviceID {
		t.Fatalf("DeviceID 不一致：got %q, want %q", claims.DeviceID, deviceID)
	}
}

// TestParseToken_WrongSecret 验证用错误密钥验签会失败。
func TestParseToken_WrongSecret(t *testing.T) {
	signingSecret := []byte("test-secret-key-at-least-32-bytes!")
	verifySecret := []byte("a-completely-different-secret-key!!")
	token, err := IssueToken(signingSecret, "acc-1", "dev-1")
	if err != nil {
		t.Fatalf("IssueToken 失败：%v", err)
	}
	if _, err := ParseToken(verifySecret, token); err == nil {
		t.Fatal("用错误密钥验签应失败，但返回 nil")
	}
}

// TestParseToken_Malformed 验证解析非 token 字符串会失败。
func TestParseToken_Malformed(t *testing.T) {
	secret := []byte("test-secret-key-at-least-32-bytes!")
	if _, err := ParseToken(secret, "not.a.valid.token"); err == nil {
		t.Fatal("解析格式错误的字符串应失败，但返回 nil")
	}
}
