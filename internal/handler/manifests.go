package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/apperr"
	"lumina-relay/internal/middleware"
	"lumina-relay/internal/service"
)

func ListManifestHeads(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		result, err := deps.ManifestService.ListHeads(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxGroupID))
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"groupRevision": result.GroupRevision, "heads": result.Heads,
		})
	}
}

func GetManifest(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		version, err := strconv.ParseInt(c.Param("version"), 10, 64)
		if err != nil || version <= 0 {
			writeBadRequest(c)
			return
		}
		result, err := deps.ManifestService.Get(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxGroupID), c.Param("deviceId"), version)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.Header("Content-Type", "application/octet-stream")
		c.Header("ETag", `"`+c.Param("deviceId")+":"+strconv.FormatInt(version, 10)+`"`)
		c.Data(http.StatusOK, "application/octet-stream", result.Ciphertext)
	}
}

func PutOwnManifest(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		baseVersion, err := strconv.ParseInt(c.Param("baseVersion"), 10, 64)
		if err != nil || baseVersion < 0 {
			writeBadRequest(c)
			return
		}
		ciphertext, ok := readRawBody(c)
		if !ok {
			return
		}
		result, err := deps.ManifestService.Put(
			c.Request.Context(), c.GetString(middleware.CtxDeviceID),
			baseVersion, ciphertext)
		if err != nil {
			if err == service.ErrStaleManifest {
				writeAPIError(c, apperr.New(
					apperr.CodeStaleManifest, "Manifest 基础版本已过期").
					WithExtra("currentVersion", result.CurrentVersion))
				return
			}
			writeServiceError(c, err)
			return
		}
		groupID := c.GetString(middleware.CtxGroupID)
		deviceIDs, listErr := deps.Queries.ListDeviceIDsInGroup(
			c.Request.Context(), c.GetString(middleware.CtxAccountID), groupID)
		if listErr == nil {
			group, _ := deps.Queries.GetSyncGroup(c.Request.Context(), groupID)
			deps.EventHub.Publish(deviceIDs, service.Event{
				Type:     "manifest_updated",
				DeviceID: c.GetString(middleware.CtxDeviceID),
				Version:  result.Version, GroupRevision: group.Revision,
				ServerTimeMS: time.Now().UnixMilli(),
			})
		}
		c.JSON(http.StatusOK, gin.H{
			"version": result.Version, "idempotent": result.Idempotent,
		})
	}
}
