package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/service"
)

type discoveryLimits struct {
	MaxJSONBytes        int   `json:"maxJsonBytes"`
	MaxManifestBytes    int   `json:"maxManifestBytes"`
	MaxSessionFileBytes int   `json:"maxSessionFileBytes"`
	MaxBlockBytes       int   `json:"maxBlockBytes"`
	MaxMissingIDs       int   `json:"maxMissingIds"`
	MaxDeviceNameBytes  int   `json:"maxDeviceNameBytes"`
	BlockGCGraceSec     int64 `json:"blockGcGraceSeconds"`
}

type discoveryResponse struct {
	Protocol     string          `json:"protocol"`
	InstanceID   string          `json:"instanceId"`
	ServerTimeMS int64           `json:"serverTimeMs"`
	Capabilities []string        `json:"capabilities"`
	Limits       discoveryLimits `json:"limits"`
}

func Discovery(instanceID string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if instanceID == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
				"code": "relay_not_initialized", "message": "Relay 尚未初始化",
			}})
			return
		}
		c.JSON(http.StatusOK, discoveryResponse{
			Protocol: "lumina-relay", InstanceID: instanceID,
			ServerTimeMS: time.Now().UnixMilli(),
			Capabilities: []string{
				"password-proof", "device-proof", "sync-groups",
				"device-manifests", "session-files", "websocket-events",
			},
			Limits: discoveryLimits{
				MaxJSONBytes: 64 << 10, MaxManifestBytes: service.MaxManifestBytes,
				MaxSessionFileBytes: service.MaxSessionFileBytes,
				MaxBlockBytes:       1 << 20, MaxMissingIDs: service.MaxMissingIDs,
				MaxDeviceNameBytes: service.MaxDeviceNameBytes,
				BlockGCGraceSec:    int64(service.BlockOrphanGracePeriod / time.Second),
			},
		})
	}
}
