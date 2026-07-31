package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/apperr"
	"lumina-relay/internal/middleware"
	"lumina-relay/internal/service"
)

func GenerateSyncCode(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		deviceID := c.GetString(middleware.CtxDeviceID)
		if deps.SyncCodeLimiter != nil {
			if allowed, retry := deps.SyncCodeLimiter.Allow(deviceID); !allowed {
				writeRateLimit(c, retry)
				return
			}
		}
		result, err := deps.SyncService.GenerateCode(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			deviceID, c.GetString(middleware.CtxGroupID))
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{
			"code": result.Code, "expiresAt": result.ExpiresAt,
		})
	}
}

type redeemSyncCodeRequest struct {
	Code string `json:"code"`
}

func RedeemSyncCode(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		deviceID := c.GetString(middleware.CtxDeviceID)
		if deps.SyncCodeLimiter != nil {
			if allowed, retry := deps.SyncCodeLimiter.Allow(deviceID); !allowed {
				writeRateLimit(c, retry)
				return
			}
		}
		var request redeemSyncCodeRequest
		if err := decodeJSON(c, &request); err != nil {
			writeBadRequest(c)
			return
		}
		result, err := deps.SyncService.RedeemCode(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			deviceID, request.Code)
		if err != nil {
			if err == service.ErrAlreadyJoined {
				writeAPIError(c, apperr.New(
					apperr.CodeAlreadyJoined, "设备已在同一同步组").
					WithExtra("groupRevision", result.GroupRevision))
				return
			}
			writeServiceError(c, err)
			return
		}
		deps.EventHub.Publish(result.AffectedDeviceIDs, service.Event{
			Type: "sync_group_merged", GroupRevision: result.GroupRevision,
			ServerTimeMS: time.Now().UnixMilli(),
		})
		c.JSON(http.StatusOK, gin.H{
			"joined": true, "syncGroupId": result.CanonicalGroupID,
			"groupRevision": result.GroupRevision,
		})
	}
}

func ListDevices(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		devices, err := deps.SyncService.ListDevices(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxGroupID))
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"devices": devices})
	}
}

func RevokeDevice(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		target := c.Param("deviceId")
		revoked, err := deps.SyncService.RevokeDevice(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxDeviceID),
			c.GetString(middleware.CtxGroupID), target)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		if revoked {
			recipients, _ := deps.Queries.ListDeviceIDsInGroup(
				c.Request.Context(), c.GetString(middleware.CtxAccountID),
				c.GetString(middleware.CtxGroupID))
			deps.EventHub.Publish(recipients, service.Event{
				Type: "device_revoked", DeviceID: target,
				ServerTimeMS: time.Now().UnixMilli(),
			})
		}
		c.JSON(http.StatusOK, gin.H{"revoked": revoked})
	}
}

type discardGroupsRequest struct {
	GroupRevision int64  `json:"groupRevision"`
	AccountProof  string `json:"accountProof"`
}

func DiscardOtherGroups(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request discardGroupsRequest
		if err := decodeJSON(c, &request); err != nil {
			writeBadRequest(c)
			return
		}
		result, err := deps.SyncService.DiscardOtherGroups(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxDeviceID), c.GetString(middleware.CtxGroupID),
			request.GroupRevision, request.AccountProof)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		recipients, _ := deps.Queries.ListDeviceIDsInGroup(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxGroupID))
		recipients = append(recipients, result.RevokedDeviceIDs...)
		for _, revokedDeviceID := range result.RevokedDeviceIDs {
			deps.EventHub.Publish(recipients, service.Event{
				Type: "device_revoked", DeviceID: revokedDeviceID,
				ServerTimeMS: time.Now().UnixMilli(),
			})
		}
		c.JSON(http.StatusOK, gin.H{
			"discardedDevices": len(result.RevokedDeviceIDs),
			"reclaimedBytes":   result.ReclaimedBytes,
		})
	}
}

func writeRateLimit(c *gin.Context, retry time.Duration) {
	writeAPIError(c, apperr.New(apperr.CodeRateLimited, "请求过于频繁").
		WithExtra("retryAfterMs", retry.Milliseconds()))
}
