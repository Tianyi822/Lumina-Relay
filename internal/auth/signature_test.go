package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestBuildCanonical_Format 验证 canonical 串的固定格式：
// method\npath\ntimestamp\nnonce\nhex(sha256(body))
// 字段顺序与分隔符必须稳定，否则客户端与服务端签名不一致。
func TestBuildCanonical_Format(t *testing.T) {
	got := BuildCanonical("PUT", "/blocks/abc", "1700000000000", "nonce1234", []byte("body-bytes"))
	want := "PUT\n/blocks/abc\n1700000000000\nnonce1234\n" + hexSHA256([]byte("body-bytes"))
	if got != want {
		t.Fatalf("BuildCanonical 不匹配:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestBuildCanonical_BodyHashUsesSHA256Hex 验证 body 部分确实是 sha256 的 hex，
// 而非原始 body 或其他哈希——这决定了客户端如何计算。
func TestBuildCanonical_BodyHashUsesSHA256Hex(t *testing.T) {
	body := []byte("hello")
	canon := BuildCanonical("POST", "/x", "1", "n", body)
	// 末尾字段应为 sha256("hello") 的 hex
	expected := hexSHA256(body)
	if !strings.HasSuffix(canon, "\n"+expected) {
		t.Fatalf("canonical 末尾应为 \\n+sha256hex(body)，got %q", canon)
	}
}

// TestVerifySignature_Valid 验证用私钥签名后，公钥能验签通过。
func TestVerifySignature_Valid(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("生成密钥失败：%v", err)
	}
	canon := BuildCanonical("PUT", "/manifest", "1700000000000", "n1", []byte("payload"))
	sig := ed25519.Sign(priv, []byte(canon))

	if !VerifySignature(pub, canon, hex.EncodeToString(sig)) {
		t.Fatal("合法签名验签失败")
	}
}

// TestVerifySignature_TamperedCanonical 验证篡改 canonical 后验签失败。
func TestVerifySignature_TamperedCanonical(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	sig := ed25519.Sign(priv, []byte(BuildCanonical("PUT", "/a", "1", "n", []byte("x"))))

	// 用不同 path 构造的 canonical 验签，应失败
	if VerifySignature(pub, BuildCanonical("PUT", "/b", "1", "n", []byte("x")), hex.EncodeToString(sig)) {
		t.Fatal("篡改 path 后验签不应通过")
	}
}

// TestVerifySignature_BadSignatureHex 验证签名不是合法 hex 时返回 false 而非 panic。
func TestVerifySignature_BadSignatureHex(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	if VerifySignature(pub, "any-canonical", "not-hex-zz") {
		t.Fatal("非法 hex 签名不应通过")
	}
}

// TestVerifySignature_WrongKey 验证用错误的公钥验签失败。
func TestVerifySignature_WrongKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	otherPub, _, _ := ed25519.GenerateKey(nil)
	canon := BuildCanonical("PUT", "/a", "1", "n", []byte("x"))
	sig := ed25519.Sign(priv, []byte(canon))

	if VerifySignature(otherPub, canon, hex.EncodeToString(sig)) {
		t.Fatal("用错误公钥验签不应通过")
	}
}

// hexSHA256 是测试 helper，独立计算 sha256 hex 以交叉校验实现。
func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
