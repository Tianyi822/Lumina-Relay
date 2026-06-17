package handler

import (
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/apperr"
	"lumina-relay/internal/auth"
	"lumina-relay/internal/service"
)

// deviceRegisterRequest 是 POST /device/register 的请求体。
// recoveryCodeHash 为客户端算好的 hex（与注册时同一算法）。
type deviceRegisterRequest struct {
	AccountID        string `json:"accountId" binding:"required"`
	RecoveryCodeHash string `json:"recoveryCodeHash" binding:"required"`
	DevicePubKey     string `json:"devicePubKey" binding:"required"`
	DeviceName       string `json:"deviceName" binding:"required"`
}

// deviceRegisterResponse 是设备注册成功响应（见 sync-design §259）。
type deviceRegisterResponse struct {
	DeviceID     string `json:"deviceId"`
	SessionToken string `json:"sessionToken"`
}

// RegisterDevice 返回 POST /device/register 的 gin handler。
// 限流由路由层挂载（IPLimiter 5/min）。handler 负责业务：
// 解码 hash → service.RegisterDevice → IssueToken → 200。
func RegisterDevice(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req deviceRegisterRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"code":    "bad_request",
				"message": "请求体格式错误",
			}})
			return
		}

		recoveryHash, err := hex.DecodeString(req.RecoveryCodeHash)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"code":    "bad_request",
				"message": "recoveryCodeHash 不是合法 hex",
			}})
			return
		}

		out, err := deps.DeviceService.RegisterDevice(c.Request.Context(), service.DeviceRegisterInput{
			AccountID:        req.AccountID,
			RecoveryCodeHash: recoveryHash,
			DevicePubKey:     req.DevicePubKey,
			DeviceName:       req.DeviceName,
		})
		if err != nil {
			switch {
			case errors.Is(err, service.ErrBadRecoveryCode):
				apperr.New(apperr.CodeBadRecoveryCode, "恢复码错误").WriteJSON(c.Writer)
			case errors.Is(err, service.ErrAccountNotFound):
				c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
					"code":    "account_not_found",
					"message": "账户不存在",
				}})
			default:
				c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
					"code":    "internal_error",
					"message": "注册设备失败",
				}})
			}
			return
		}

		token, err := auth.IssueToken(deps.JWTSecret, req.AccountID, out.DeviceID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
				"code":    "internal_error",
				"message": "签发会话令牌失败",
			}})
			return
		}

		c.JSON(http.StatusOK, deviceRegisterResponse{
			DeviceID:     out.DeviceID,
			SessionToken: token,
		})
	}
}
