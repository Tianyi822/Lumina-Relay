package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/middleware"
)

type missingBlocksRequest struct {
	IDs []string `json:"ids"`
}

func MissingBlocks(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request missingBlocksRequest
		if err := decodeJSON(c, &request); err != nil {
			writeBadRequest(c)
			return
		}
		missing, err := deps.BlocksService.Missing(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxGroupID), request.IDs)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"missing": missing})
	}
}

func PutBlock(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		data, ok := readRawBody(c)
		if !ok {
			return
		}
		result, err := deps.BlocksService.Put(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxDeviceID), c.Param("blockId"), data)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		status := http.StatusOK
		if result.Created {
			status = http.StatusCreated
		}
		c.JSON(status, gin.H{"created": result.Created})
	}
}

func GetBlock(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		data, err := deps.BlocksService.Get(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxGroupID), c.Param("blockId"))
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.Data(http.StatusOK, "application/octet-stream", data)
	}
}

type pruneBlocksRequest struct {
	GroupRevision int64    `json:"groupRevision"`
	Keep          []string `json:"keep"`
}

func PruneBlocks(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request pruneBlocksRequest
		if err := decodeJSON(c, &request); err != nil {
			writeBadRequest(c)
			return
		}
		result, err := deps.BlocksService.Prune(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxDeviceID),
			c.GetString(middleware.CtxGroupID), request.GroupRevision, request.Keep)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"reclaimedBytes":  result.ReclaimedBytes,
			"orphanedObjects": len(result.OrphanBlockIDs),
		})
	}
}
