package handler

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/apperr"
	"lumina-relay/internal/middleware"
	"lumina-relay/internal/service"
)

// blocksHaveRequest 是 POST /blocks/have 的请求体。
type blocksHaveRequest struct {
	IDs []string `json:"ids" binding:"required"`
}

// blocksHaveResponse 是 POST /blocks/have 的响应。
type blocksHaveResponse struct {
	Missing []string `json:"missing"`
}

// maxBlockIDsPerHave 是单次 have 查询的上限（sync-design §656）。
const maxBlockIDsPerHave = 1000

// BlocksHave 返回 POST /blocks/have 的 handler（Session 认证）。
func BlocksHave(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		accountID := c.GetString("accountId")
		var req blocksHaveRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"code": "bad_request", "message": "请求体格式错误",
			}})
			return
		}
		if len(req.IDs) > maxBlockIDsPerHave {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"code": "bad_request", "message": "ids 数量超上限（1000）",
			}})
			return
		}
		missing, err := deps.BlocksService.Have(c.Request.Context(), accountID, req.IDs)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
				"code": "internal_error", "message": "查询失败",
			}})
			return
		}
		if missing == nil {
			missing = []string{}
		}
		c.JSON(http.StatusOK, blocksHaveResponse{Missing: missing})
	}
}

// PutBlock 返回 PUT /blocks/:blockId 的 handler（Session + Signed）。
// body 为原始密文字节（Content-Type: application/octet-stream）。
// 响应 201 新建 / 200 已存在（幂等）；hash 不符 400。
func PutBlock(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		accountID := c.GetString("accountId")
		blockID := c.Param("blockId")

		data, err := io.ReadAll(c.Request.Body)
		if middleware.HandleBodyReadError(c, err, http.StatusBadRequest, "bad_request", "读取 body 失败") {
			return
		}

		out, err := deps.BlocksService.Put(c.Request.Context(), service.BlocksPutInput{
			AccountID: accountID, BlockID: blockID, Data: data,
		})
		if err != nil {
			switch {
			case errors.Is(err, service.ErrBlockHashMismatch):
				apperr.New(apperr.CodeBlockHashMismatch, "块 hash 校验失败").WriteJSON(c.Writer)
			case errors.Is(err, service.ErrQuotaExceeded):
				apperr.New(apperr.CodeQuotaExceeded, "存储配额已满").WriteJSON(c.Writer)
			default:
				c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
					"code": "internal_error", "message": "上传块失败",
				}})
			}
			return
		}
		if out.Created {
			c.Status(http.StatusCreated)
		} else {
			c.Status(http.StatusOK)
		}
	}
}

// GetBlock 返回 GET /blocks/:blockId 的 handler（Session 认证）。
// 返回原始密文字节。
func GetBlock(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		accountID := c.GetString("accountId")
		blockID := c.Param("blockId")

		data, err := deps.BlocksService.Get(c.Request.Context(), accountID, blockID)
		if err != nil {
			if errors.Is(err, service.ErrBlockNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
					"code": "block_not_found", "message": "块不存在",
				}})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
				"code": "internal_error", "message": "读取块失败",
			}})
			return
		}
		c.Data(http.StatusOK, "application/octet-stream", data)
	}
}
