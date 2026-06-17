package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// BuildCanonical 构造写操作的待签名 canonical 串。
// 格式固定为（字段顺序与换行分隔必须稳定，客户端与服务端需一致）：
//
//	method\n path\n timestamp\n nonce\n hex(sha256(body))
//
// 其中 body 的哈希取 sha256 并以小写 hex 编码（与 sync-design §6.4 块 hash 风格一致）。
// timestamp/nonce 由调用方（中间件）从请求头提取，本函数不做范围校验。
func BuildCanonical(method, path, timestamp, nonce string, body []byte) string {
	sum := sha256.Sum256(body)
	return strings.Join([]string{method, path, timestamp, nonce, hex.EncodeToString(sum[:])}, "\n")
}

// VerifySignature 校验 Ed25519 签名。
// pubKey 为设备公钥（调用方负责从存储字段解码为 ed25519.PublicKey）；
// canonical 为 BuildCanonical 的输出；sigHex 为 X-Signature 头的 hex 编码签名。
// 任何环节（hex 非法、长度不符、验签失败）都返回 false，绝不 panic。
func VerifySignature(pubKey ed25519.PublicKey, canonical, sigHex string) bool {
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	if len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pubKey, []byte(canonical), sig)
}
