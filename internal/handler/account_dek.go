package handler

import (
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/service"
)

// dekEnvelopeResponse 是 GET /account/dek 的响应体（hex 编码，见 sync-design §625）。
type dekEnvelopeResponse struct {
	Salt  string `json:"salt"`
	Nonce string `json:"nonce"`
	Ct    string `json:"ct"`
}

// GetAccountDEK 返回 GET /account/dek 的 gin handler。
// 限流由路由层挂载（IPLimiter 10/min），handler 只负责业务。
// 响应：200 { "dekEnvelope": { salt, nonce, ct } }。
func GetAccountDEK(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		accountID := c.Query("accountId")
		if accountID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"code":    "bad_request",
				"message": "缺少 accountId 参数",
			}})
			return
		}

		dek, err := deps.AccountService.GetDEK(c.Request.Context(), accountID)
		if err != nil {
			if errors.Is(err, service.ErrAccountNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
					"code":    "account_not_found",
					"message": "账户不存在",
				}})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
				"code":    "internal_error",
				"message": "读取 DEK 失败",
			}})
			return
		}

		c.JSON(http.StatusOK, gin.H{"dekEnvelope": dekEnvelopeResponse{
			Salt:  hex.EncodeToString(dek.Salt),
			Nonce: hex.EncodeToString(dek.Nonce),
			Ct:    hex.EncodeToString(dek.Ct),
		}})
	}
}

// putDekRequest 是 PUT /account/dek 的请求体（新 dekEnvelope，hex）。
type putDekRequest struct {
	DekSalt  string `json:"dekSalt" binding:"required"`
	DekNonce string `json:"dekNonce" binding:"required"`
	DekCt    string `json:"dekCt" binding:"required"`
}

// PutAccountDEK 返回 PUT /account/dek 的 gin handler。
// 前置依赖 RequireSession + RequireSignedWrite（路由层挂载）。
// 从 context 取 accountId（session 注入），替换该账户的 dekEnvelope。
func PutAccountDEK(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		accountID := c.GetString("accountId")
		if accountID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{
				"code": "unauthorized", "message": "session missing",
			}})
			return
		}

		var req putDekRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"code": "bad_request", "message": "请求体格式错误",
			}})
			return
		}
		salt, err := hex.DecodeString(req.DekSalt)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"code": "bad_request", "message": "dekSalt 不是合法 hex",
			}})
			return
		}
		nonce, err := hex.DecodeString(req.DekNonce)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"code": "bad_request", "message": "dekNonce 不是合法 hex",
			}})
			return
		}
		ct, err := hex.DecodeString(req.DekCt)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"code": "bad_request", "message": "dekCt 不是合法 hex",
			}})
			return
		}

		if err := deps.AccountService.UpdateDEK(c.Request.Context(), accountID, service.DEKEnvelope{
			Salt: salt, Nonce: nonce, Ct: ct,
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
				"code": "internal_error", "message": "更新 DEK 失败",
			}})
			return
		}
		c.Status(http.StatusNoContent)
	}
}
