package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/apperr"
	"lumina-relay/internal/middleware"
	"lumina-relay/internal/service"
)

// publishSessionEvent 向同步组内所有设备广播会话文件变更事件，模式同 PutOwnManifest。
func publishSessionEvent(c *gin.Context, deps Deps, eventType, sessionID string, version int64) {
	accountID := c.GetString(middleware.CtxAccountID)
	groupID := c.GetString(middleware.CtxGroupID)
	deviceIDs, err := deps.Queries.ListDeviceIDsInGroup(c.Request.Context(), accountID, groupID)
	if err != nil {
		return
	}
	deps.EventHub.Publish(deviceIDs, service.Event{
		Type: eventType, DeviceID: c.GetString(middleware.CtxDeviceID),
		SessionID: sessionID, Version: version,
		ServerTimeMS: time.Now().UnixMilli(),
	})
}

// writeStaleSessionFile 输出 409 stale_session_file，携带当前版本供客户端 LWW 决策。
func writeStaleSessionFile(c *gin.Context, currentVersion int64) {
	writeAPIError(c, apperr.New(apperr.CodeStaleSessionFile, "会话文件基础版本已过期").
		WithExtra("currentVersion", currentVersion))
}

func ListSessionFiles(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := deps.SessionFileService.List(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxGroupID))
		if err != nil {
			writeServiceError(c, err)
			return
		}
		sessions := make([]gin.H, 0, len(rows))
		for _, row := range rows {
			sessions = append(sessions, gin.H{
				"sessionId": row.SessionID, "version": row.Version,
				"size": row.Size, "updatedAt": row.UpdatedAt,
			})
		}
		c.JSON(http.StatusOK, gin.H{"sessions": sessions})
	}
}

func GetSessionFile(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		content, err := deps.SessionFileService.Get(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxGroupID), c.Param("sessionId"))
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.Header("X-Session-File-Version", strconv.FormatInt(content.Version, 10))
		c.Data(http.StatusOK, "application/octet-stream", content.Data)
	}
}

func PutSessionFile(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		baseVersion, err := strconv.ParseInt(c.Param("baseVersion"), 10, 64)
		if err != nil || baseVersion < 0 {
			writeBadRequest(c)
			return
		}
		data, ok := readRawBody(c)
		if !ok {
			return
		}
		sessionID := c.Param("sessionId")
		result, err := deps.SessionFileService.Rewrite(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxGroupID), c.GetString(middleware.CtxDeviceID),
			sessionID, baseVersion, data)
		if err != nil {
			if errors.Is(err, service.ErrStaleSessionFile) {
				writeStaleSessionFile(c, result.CurrentVersion)
				return
			}
			writeServiceError(c, err)
			return
		}
		publishSessionEvent(c, deps, "session_file_updated", sessionID, result.Version)
		c.JSON(http.StatusOK, gin.H{"version": result.Version, "size": result.Size})
	}
}

func AppendSessionFile(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		baseVersion, err := strconv.ParseInt(c.Param("baseVersion"), 10, 64)
		if err != nil || baseVersion < 1 {
			writeBadRequest(c)
			return
		}
		data, ok := readRawBody(c)
		if !ok {
			return
		}
		sessionID := c.Param("sessionId")
		result, err := deps.SessionFileService.Append(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxGroupID), c.GetString(middleware.CtxDeviceID),
			sessionID, baseVersion, data)
		if err != nil {
			if errors.Is(err, service.ErrStaleSessionFile) {
				writeStaleSessionFile(c, result.CurrentVersion)
				return
			}
			writeServiceError(c, err)
			return
		}
		publishSessionEvent(c, deps, "session_file_updated", sessionID, result.Version)
		c.JSON(http.StatusOK, gin.H{"version": result.Version, "size": result.Size})
	}
}

func DeleteSessionFile(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		sessionID := c.Param("sessionId")
		deleted, err := deps.SessionFileService.Delete(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxGroupID), c.GetString(middleware.CtxDeviceID),
			sessionID)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		if deleted {
			publishSessionEvent(c, deps, "session_file_deleted", sessionID, 0)
		}
		c.JSON(http.StatusOK, gin.H{"deleted": deleted})
	}
}

func GetSessionIndex(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		content, err := deps.SessionFileService.GetIndex(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxGroupID))
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.Header("X-Session-File-Version", strconv.FormatInt(content.Version, 10))
		c.Data(http.StatusOK, "application/octet-stream", content.Data)
	}
}

func PutSessionIndex(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		baseVersion, err := strconv.ParseInt(c.Param("baseVersion"), 10, 64)
		if err != nil || baseVersion < 0 {
			writeBadRequest(c)
			return
		}
		data, ok := readRawBody(c)
		if !ok {
			return
		}
		result, err := deps.SessionFileService.PutIndex(
			c.Request.Context(), c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxGroupID), c.GetString(middleware.CtxDeviceID),
			baseVersion, data)
		if err != nil {
			if errors.Is(err, service.ErrStaleSessionFile) {
				writeStaleSessionFile(c, result.CurrentVersion)
				return
			}
			writeServiceError(c, err)
			return
		}
		publishSessionEvent(c, deps, "session_index_updated", "", result.Version)
		c.JSON(http.StatusOK, gin.H{"version": result.Version, "size": result.Size})
	}
}
