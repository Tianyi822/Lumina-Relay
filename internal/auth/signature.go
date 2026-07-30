package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// BuildCanonical 固定设备 HTTP PoP 的跨语言文本格式。
func BuildCanonical(method, path, timestamp, nonce string, body []byte) string {
	sum := sha256.Sum256(body)
	return strings.Join([]string{
		strings.ToUpper(method), path, timestamp, nonce, hex.EncodeToString(sum[:]),
	}, "\n")
}

func VerifySignature(publicKey []byte, message []byte, encodedSignature string) bool {
	if len(publicKey) != ed25519.PublicKeySize {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if err != nil || len(signature) != ed25519.SignatureSize ||
		base64.RawURLEncoding.EncodeToString(signature) != encodedSignature {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(publicKey), message, signature)
}

func DecodePublicKey(encoded string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != ed25519.PublicKeySize ||
		base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return nil, fmt.Errorf("Ed25519 公钥格式非法")
	}
	return raw, nil
}

func EncodeBase64URL(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}

func DecodeBase64URL(encoded string, minBytes, maxBytes int) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) < minBytes || len(raw) > maxBytes ||
		base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return nil, fmt.Errorf("base64url 字段格式非法")
	}
	return raw, nil
}
