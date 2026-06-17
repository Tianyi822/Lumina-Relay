package middleware

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/apperr"
	"lumina-relay/internal/auth"
	"lumina-relay/internal/db"
)

// Context keys：session 中间件注入 gin.Context 的字段名。
const (
	CtxAccountID = "accountId"
	CtxDeviceID  = "deviceId"
	CtxDeviceKey = "devicePubKey"
)

// bearerPrefix 是 Authorization 头的前缀。
const bearerPrefix = "Bearer "

// RequireSession 返回一个 gin 中间件，校验 Bearer JWT session token。
// 通过后注入 accountId/deviceId/devicePubKey 到 gin.Context，并拒绝已吊销设备。
// 失败响应统一 401（缺失/格式错/非法 token→无 code；已吊销→device_revoked）。
func RequireSession(q *db.Queries, jwtSecret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		if !strings.HasPrefix(raw, bearerPrefix) {
			writeUnauthorized(c, "missing or malformed Authorization header")
			return
		}
		token := strings.TrimPrefix(raw, bearerPrefix)

		claims, err := auth.ParseToken(jwtSecret, token)
		if err != nil {
			writeUnauthorized(c, "invalid session token")
			return
		}

		dev, err := q.GetDevice(c.Request.Context(), claims.DeviceID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeUnauthorized(c, "device not found")
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
				"code": "internal_error", "message": "查询设备失败",
			}})
			c.Abort()
			return
		}
		if dev.RevokedAt.Valid {
			apperr.New(apperr.CodeDeviceRevoked, "设备已吊销").WriteJSON(c.Writer)
			c.Abort()
			return
		}

		// token 中的 accountId 必须与设备记录一致（防 token 伪造账户归属）
		if dev.AccountID != claims.AccountID {
			writeUnauthorized(c, "token account mismatch")
			return
		}

		c.Set(CtxAccountID, dev.AccountID)
		c.Set(CtxDeviceID, dev.DeviceID)
		c.Set(CtxDeviceKey, dev.DevicePubKey)
		c.Next()
	}
}

// writeUnauthorized 写统一 401 响应并中止请求链。
func writeUnauthorized(c *gin.Context, msg string) {
	c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{
		"code": "unauthorized", "message": msg,
	}})
	c.Abort()
}
