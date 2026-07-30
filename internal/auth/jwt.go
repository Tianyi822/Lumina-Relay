package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	sessionAudience = "lumina-relay"
	sessionTTL      = 24 * time.Hour
)

type Claims struct {
	AccountID string
	DeviceID  string
	TokenID   string
	ExpiresAt time.Time
}

type sessionClaims struct {
	AccountID string `json:"accountId"`
	DeviceID  string `json:"deviceId"`
	jwt.RegisteredClaims
}

type SessionToken struct {
	Token     string
	ExpiresAt int64
	TokenID   string
}

func IssueSessionToken(
	secret []byte,
	instanceID, accountID, deviceID string,
	now time.Time,
) (SessionToken, error) {
	rawID := make([]byte, 24)
	if _, err := rand.Read(rawID); err != nil {
		return SessionToken{}, fmt.Errorf("生成 session id：%w", err)
	}
	tokenID := base64.RawURLEncoding.EncodeToString(rawID)
	expires := now.Add(sessionTTL)
	claims := sessionClaims{
		AccountID: accountID,
		DeviceID:  deviceID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    instanceID,
			Subject:   deviceID,
			Audience:  jwt.ClaimStrings{sessionAudience},
			ExpiresAt: jwt.NewNumericDate(expires),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        tokenID,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(secret)
	if err != nil {
		return SessionToken{}, fmt.Errorf("签发 session：%w", err)
	}
	return SessionToken{Token: signed, ExpiresAt: expires.Unix(), TokenID: tokenID}, nil
}

func ParseSessionToken(secret []byte, instanceID, tokenText string) (Claims, error) {
	parsed, err := jwt.ParseWithClaims(
		tokenText,
		&sessionClaims{},
		func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodHS256 {
				return nil, fmt.Errorf("JWT 算法非法")
			}
			return secret, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(instanceID),
		jwt.WithAudience(sessionAudience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	if err != nil {
		return Claims{}, fmt.Errorf("解析 session：%w", err)
	}
	value, ok := parsed.Claims.(*sessionClaims)
	if !ok || !parsed.Valid || value.Subject == "" || value.ID == "" ||
		value.Subject != value.DeviceID || value.AccountID == "" ||
		value.ExpiresAt == nil {
		return Claims{}, fmt.Errorf("session claims 非法")
	}
	return Claims{
		AccountID: value.AccountID,
		DeviceID:  value.DeviceID,
		TokenID:   value.ID,
		ExpiresAt: value.ExpiresAt.Time,
	}, nil
}
