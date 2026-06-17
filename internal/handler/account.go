package handler

import (
	"encoding/hex"
	"net/http"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/auth"
	"lumina-relay/internal/service"
)

// registerRequest 是 POST /account/register 的请求体。
// 所有二进制字段以 hex 字符串传输（见 sync-design §213）。
type registerRequest struct {
	RecoveryCodeHash string `json:"recoveryCodeHash" binding:"required"`
	DekSalt          string `json:"dekSalt" binding:"required"`
	DekNonce         string `json:"dekNonce" binding:"required"`
	DekCt            string `json:"dekCt" binding:"required"`
	DevicePubKey     string `json:"devicePubKey" binding:"required"`
	DeviceName       string `json:"deviceName" binding:"required"`
}

// registerResponse 是注册成功响应。注意不含 recoveryCode（计划"API 对齐决策"）。
type registerResponse struct {
	AccountID    string `json:"accountId"`
	DeviceID     string `json:"deviceId"`
	SessionToken string `json:"sessionToken"`
}

// RegisterAccount 返回 POST /account/register 的 gin handler。
// 闭包捕获 deps 以访问 AccountService 与 JWTSecret。
func RegisterAccount(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req registerRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"code":    "bad_request",
				"message": "请求体格式错误",
			}})
			return
		}

		recoveryHash, salt, nonce, ct, ok := decodeRegisterHexFields(c, req)
		if !ok {
			return // decodeRegisterHexFields 已写错误响应
		}

		out, err := deps.AccountService.Register(c.Request.Context(), service.RegisterInput{
			RecoveryCodeHash: recoveryHash,
			DekSalt:          salt,
			DekNonce:         nonce,
			DekCt:            ct,
			DevicePubKey:     req.DevicePubKey,
			DeviceName:       req.DeviceName,
		})
		if err != nil {
			// 注册失败目前只可能是内部错误；统一 500。
			// 精确的 apperr 映射在后续 Task 引入更多错误码时补全。
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
				"code":    "internal_error",
				"message": "注册失败",
			}})
			return
		}

		token, err := auth.IssueToken(deps.JWTSecret, out.AccountID, out.DeviceID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
				"code":    "internal_error",
				"message": "签发会话令牌失败",
			}})
			return
		}

		c.JSON(http.StatusCreated, registerResponse{
			AccountID:    out.AccountID,
			DeviceID:     out.DeviceID,
			SessionToken: token,
		})
	}
}

// decodeRegisterHexFields 把请求体的 hex 字段解码为 bytes。
// 任一非法则写 400 响应并返回 ok=false。
func decodeRegisterHexFields(c *gin.Context, req registerRequest) (hash, salt, nonce, ct []byte, ok bool) {
	type field struct {
		name, raw string
		dest      *[]byte
	}
	fields := []field{
		{"recoveryCodeHash", req.RecoveryCodeHash, &hash},
		{"dekSalt", req.DekSalt, &salt},
		{"dekNonce", req.DekNonce, &nonce},
		{"dekCt", req.DekCt, &ct},
	}
	for _, f := range fields {
		decoded, err := hex.DecodeString(f.raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"code":    "bad_request",
				"message": f.name + " 不是合法 hex",
			}})
			return nil, nil, nil, nil, false
		}
		*f.dest = decoded
	}
	return hash, salt, nonce, ct, true
}
