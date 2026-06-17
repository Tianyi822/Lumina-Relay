package handler

import (
	"encoding/hex"
	"net/http"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/apperr"
	"lumina-relay/internal/service"
)

// manifestPutRequest 是 PUT /manifest 的请求体。
type manifestPutRequest struct {
	Ciphertext string `json:"ciphertext" binding:"required"` // hex 编码的 manifest 密文
	BaseVersion int64 `json:"baseVersion"`                   // 客户端基于的版本
}

// manifestVersionResponse 是 PUT 成功响应（见 sync-design §647）。
type manifestVersionResponse struct {
	Version int64 `json:"version"`
}

// manifestResponse 是 GET 响应（当前版本 + ciphertext）。
type manifestResponse struct {
	Version    int    `json:"version"`
	Ciphertext string `json:"ciphertext"` // hex；version=0 时为空
}

// GetManifest 返回 GET /manifest 的 handler。
// 前置 RequireSession。从 context 取 accountId，返回该账户当前 manifest。
func GetManifest(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		accountID := c.GetString("accountId")
		out, err := deps.ManifestService.Get(c.Request.Context(), accountID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
				"code": "internal_error", "message": "读取 manifest 失败",
			}})
			return
		}
		c.JSON(http.StatusOK, manifestResponse{
			Version:    int(out.Version),
			Ciphertext: hex.EncodeToString(out.Ciphertext),
		})
	}
}

// PutManifest 返回 PUT /manifest 的 handler。
// 前置 RequireSession + RequireSignedWrite。
// 乐观并发：baseVersion 过期返回 409 stale_base + currentVersion。
func PutManifest(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		accountID := c.GetString("accountId")
		deviceID := c.GetString("deviceId")

		var req manifestPutRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"code": "bad_request", "message": "请求体格式错误",
			}})
			return
		}
		ct, err := hex.DecodeString(req.Ciphertext)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"code": "bad_request", "message": "ciphertext 不是合法 hex",
			}})
			return
		}

		out, err := deps.ManifestService.Put(c.Request.Context(), service.ManifestPutInput{
			AccountID:   accountID,
			Ciphertext:  ct,
			BaseVersion: req.BaseVersion,
			DeviceID:    deviceID,
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
				"code": "internal_error", "message": "提交 manifest 失败",
			}})
			return
		}
		if out.Conflict {
			// stale_base 携带 currentVersion（sync-design §648）
			apperr.New(apperr.CodeStaleBase, "base version 过期").
				WithExtra("currentVersion", out.CurrentVersion).
				WriteJSON(c.Writer)
			return
		}
		c.JSON(http.StatusOK, manifestVersionResponse{Version: out.NewVersion})
	}
}
