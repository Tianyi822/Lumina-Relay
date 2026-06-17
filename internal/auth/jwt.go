package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims 是 session JWT 的载荷，含账户与设备标识。
type Claims struct {
	AccountID string
	DeviceID  string
}

type sessionClaims struct {
	AccountID string `json:"accountId"`
	DeviceID  string `json:"deviceId"`
	jwt.RegisteredClaims
}

// IssueToken 签发 HS256 session token。
func IssueToken(secret []byte, accountID, deviceID string) (string, error) {
	claims := sessionClaims{
		AccountID: accountID,
		DeviceID:  deviceID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(secret)
	if err != nil {
		return "", fmt.Errorf("签发 token：%w", err)
	}
	return signed, nil
}

// ParseToken 解析并校验 HS256 session token，返回 Claims。
func ParseToken(secret []byte, tokenStr string) (Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &sessionClaims{}, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("不支持的签名算法：%v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return Claims{}, fmt.Errorf("解析 token：%w", err)
	}

	claims, ok := token.Claims.(*sessionClaims)
	if !ok || !token.Valid {
		return Claims{}, fmt.Errorf("无效 token")
	}
	return Claims{
		AccountID: claims.AccountID,
		DeviceID:  claims.DeviceID,
	}, nil
}
