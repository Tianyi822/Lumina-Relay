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
