package middleware

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/apperr"
	"lumina-relay/internal/auth"
	"lumina-relay/internal/db"
)

const (
	CtxAccountID = "accountId"
	CtxDeviceID  = "deviceId"
	CtxDeviceKey = "devicePublicKey"
	CtxGroupID   = "syncGroupId"
	CtxTokenID   = "sessionTokenId"

	HeaderTimestamp = "X-Timestamp"
	HeaderNonce     = "X-Nonce"
	HeaderSignature = "X-Signature"

	maxProofSkew = 5 * time.Minute
)

func RequireSession(
	q *db.Queries,
	jwtSecret []byte,
	instanceID string,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			writeAuthError(c, apperr.CodeInvalidDeviceProof, "会话无效")
			return
		}
		claims, err := auth.ParseSessionToken(
			jwtSecret, instanceID, strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			writeAuthError(c, apperr.CodeInvalidDeviceProof, "会话无效")
			return
		}
		device, err := q.GetDevice(c.Request.Context(), claims.DeviceID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeAuthError(c, apperr.CodeInvalidDeviceProof, "会话无效")
			} else {
				apperr.New(apperr.CodeInternalError, "查询设备失败").WriteJSON(c.Writer)
				c.Abort()
			}
			return
		}
		if device.Status == "revoked" || device.RevokedAt.Valid {
			writeAuthError(c, apperr.CodeDeviceRevoked, "设备已吊销")
			return
		}
		if device.AccountID != claims.AccountID || !device.SyncGroupID.Valid {
			writeAuthError(c, apperr.CodeInvalidDeviceProof, "会话无效")
			return
		}
		c.Set(CtxAccountID, device.AccountID)
		c.Set(CtxDeviceID, device.DeviceID)
		c.Set(CtxDeviceKey, append([]byte(nil), device.SigningPublicKey...))
		c.Set(CtxGroupID, device.SyncGroupID.String)
		c.Set(CtxTokenID, claims.TokenID)
		_ = q.TouchDeviceLastSeen(c.Request.Context(), device.DeviceID, time.Now().Unix())
		c.Next()
	}
}

// RequireDeviceProof 对所有账户数据请求验证设备 Ed25519 PoP，并在验签后
// 用 SQLite 复合主键持久占用 nonce，重启后仍不能重放。
func RequireDeviceProof(q *db.Queries) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.URL.RawQuery != "" ||
			c.Request.RequestURI != c.Request.URL.Path {
			writeAuthError(c, apperr.CodeInvalidDeviceProof, "请求路径不规范")
			return
		}
		publicKeyValue, ok := c.Get(CtxDeviceKey)
		if !ok {
			writeAuthError(c, apperr.CodeInvalidDeviceProof, "缺少设备身份")
			return
		}
		publicKey, ok := publicKeyValue.([]byte)
		if !ok {
			writeAuthError(c, apperr.CodeInvalidDeviceProof, "设备身份无效")
			return
		}
		body, err := io.ReadAll(c.Request.Body)
		if HandleBodyReadError(c, err, http.StatusBadRequest, "bad_request", "读取请求失败") {
			return
		}
		_ = c.Request.Body.Close()
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		timestampText := c.GetHeader(HeaderTimestamp)
		nonceText := c.GetHeader(HeaderNonce)
		signature := c.GetHeader(HeaderSignature)
		timestampMS, err := strconv.ParseInt(timestampText, 10, 64)
		if err != nil || strconv.FormatInt(timestampMS, 10) != timestampText ||
			nonceText == "" || signature == "" {
			writeAuthError(c, apperr.CodeInvalidDeviceProof, "设备证明无效")
			return
		}
		signedAt := time.UnixMilli(timestampMS)
		now := time.Now()
		if signedAt.Before(now.Add(-maxProofSkew)) || signedAt.After(now.Add(maxProofSkew)) {
			writeAuthError(c, apperr.CodeInvalidDeviceProof, "设备证明无效")
			return
		}
		nonce, err := auth.DecodeBase64URL(nonceText, 16, 32)
		if err != nil {
			writeAuthError(c, apperr.CodeInvalidDeviceProof, "设备证明无效")
			return
		}
		canonical := auth.BuildCanonical(
			c.Request.Method, c.Request.URL.Path, timestampText, nonceText, body)
		if !auth.VerifySignature(publicKey, []byte(canonical), signature) {
			writeAuthError(c, apperr.CodeInvalidDeviceProof, "设备证明无效")
			return
		}
		deviceID := c.GetString(CtxDeviceID)
		nonceHash := sha256.Sum256(nonce)
		used, err := q.UseRequestNonce(
			c.Request.Context(), deviceID, nonceHash[:],
			signedAt.Add(maxProofSkew+time.Minute).UnixMilli(), now.UnixMilli())
		if err != nil {
			apperr.New(apperr.CodeInternalError, "记录请求 nonce 失败").WriteJSON(c.Writer)
			c.Abort()
			return
		}
		if !used {
			writeAuthError(c, apperr.CodeInvalidDeviceProof, "设备证明无效")
			return
		}
		c.Next()
	}
}

func writeAuthError(c *gin.Context, code apperr.Code, message string) {
	apperr.New(code, message).WriteJSON(c.Writer)
	c.Abort()
}
