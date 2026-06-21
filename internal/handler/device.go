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
		// 强制 32 字节（SHA-256），防短哈希爆破
		if len(recoveryHash) != 32 {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"code":    "bad_request",
				"message": "recoveryCodeHash 长度必须为 32 字节",
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
			case errors.Is(err, service.ErrAccountLocked):
				apperr.New(apperr.CodeRateLimited, "恢复码尝试过多，请稍后再试").WriteJSON(c.Writer)
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

// DeleteDevice 返回 DELETE /device/:deviceId 的 gin handler。
// 前置依赖 RequireSession + RequireSignedWrite。
// 吊销指定设备，幂等。只能吊销调用者自己账户名下的设备（防越权）。
// 见 sync-design §288-289。
func DeleteDevice(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		callerAccountID := c.GetString("accountId")
		deviceID := c.Param("deviceId")
		if deviceID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"code": "bad_request", "message": "缺少 deviceId",
			}})
			return
		}

		if err := deps.DeviceService.RevokeDevice(c.Request.Context(), callerAccountID, deviceID); err != nil {
			switch {
			case errors.Is(err, service.ErrDeviceForbidden):
				c.JSON(http.StatusForbidden, gin.H{"error": gin.H{
					"code": "forbidden", "message": "无权操作该设备",
				}})
			case errors.Is(err, service.ErrDeviceNotFound):
				c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
					"code": "device_not_found", "message": "设备不存在",
				}})
			default:
				c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
					"code": "internal_error", "message": "吊销设备失败",
				}})
			}
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// deviceListItem 是 GET /devices 响应数组的单项。
type deviceListItem struct {
	DeviceID     string `json:"deviceId"`
	DeviceName   string `json:"deviceName"`
	DevicePubKey string `json:"devicePubKey"`
	CreatedAt    int64  `json:"createdAt"`
	LastSeenAt   int64  `json:"lastSeenAt"`
}

// ListDevices 返回 GET /devices 的 gin handler。
// 前置依赖 RequireSession（路由层挂载）。从 context 取 accountId，列出该账户下未吊销设备。
func ListDevices(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		accountID := c.GetString("accountId")
		if accountID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{
				"code": "unauthorized", "message": "session missing",
			}})
			return
		}
		devs, err := deps.DeviceService.ListDevices(c.Request.Context(), accountID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
				"code": "internal_error", "message": "列出设备失败",
			}})
			return
		}
		items := make([]deviceListItem, len(devs))
		for i, d := range devs {
			items[i] = deviceListItem{
				DeviceID:     d.DeviceID,
				DeviceName:   d.DeviceName,
				DevicePubKey: d.DevicePubKey,
				CreatedAt:    d.CreatedAt,
				LastSeenAt:   d.LastSeenAt,
			}
		}
		c.JSON(http.StatusOK, items)
	}
}
