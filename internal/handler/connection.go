package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/auth"
	"lumina-relay/internal/middleware"
	"lumina-relay/internal/service"
)

type connectionStartRequest struct {
	Username string `json:"username"`
}

type kdfParamsResponse struct {
	Name        string `json:"name"`
	MemoryKiB   int    `json:"memoryKiB"`
	Iterations  int    `json:"iterations"`
	Parallelism int    `json:"parallelism"`
	OutputBytes int    `json:"outputBytes"`
}

func StartConnection(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request connectionStartRequest
		if err := decodeJSON(c, &request); err != nil {
			writeBadRequest(c)
			return
		}
		username, err := service.NormalizeUsername(request.Username)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		if deps.UsernameLimiter != nil {
			if allowed, retry := deps.UsernameLimiter.Allow(username); !allowed {
				writeRateLimit(c, retry)
				return
			}
		}
		result, err := deps.ConnectionService.Start(c.Request.Context(), username)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"accountExists": result.AccountExists,
			"attemptId":     result.AttemptID,
			"challenge":     auth.EncodeBase64URL(result.Challenge),
			"authSalt":      auth.EncodeBase64URL(result.AuthSalt),
			"expiresAt":     result.ExpiresAt,
			"kdf": kdfParamsResponse{
				Name: "argon2id", MemoryKiB: 65536, Iterations: 3,
				Parallelism: 1, OutputBytes: 32,
			},
		})
	}
}

type completeConnectionRequest struct {
	AttemptID            string `json:"attemptId"`
	AccountID            string `json:"accountId,omitempty"`
	DeviceID             string `json:"deviceId"`
	DeviceName           string `json:"deviceName"`
	DevicePublicKey      string `json:"devicePublicKey"`
	LoginPublicKey       string `json:"loginPublicKey,omitempty"`
	AccountAuthPublicKey string `json:"accountAuthPublicKey,omitempty"`
	DEKEnvelope          string `json:"dekEnvelope,omitempty"`
	LoginProof           string `json:"loginProof"`
	AccountProof         string `json:"accountProof,omitempty"`
	DeviceProof          string `json:"deviceProof"`
}

func CompleteConnection(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request completeConnectionRequest
		if err := decodeJSON(c, &request); err != nil {
			writeBadRequest(c)
			return
		}
		deviceKey, err := auth.DecodePublicKey(request.DevicePublicKey)
		if err != nil {
			writeBadRequest(c)
			return
		}
		var loginKey, accountKey, envelope []byte
		if request.LoginPublicKey != "" {
			loginKey, err = auth.DecodePublicKey(request.LoginPublicKey)
			if err != nil {
				writeBadRequest(c)
				return
			}
		}
		if request.AccountAuthPublicKey != "" {
			accountKey, err = auth.DecodePublicKey(request.AccountAuthPublicKey)
			if err != nil {
				writeBadRequest(c)
				return
			}
		}
		if request.DEKEnvelope != "" {
			envelope, err = auth.DecodeBase64URL(
				request.DEKEnvelope,
				service.DEKEnvelopeBytes, service.DEKEnvelopeBytes)
			if err != nil {
				writeBadRequest(c)
				return
			}
		}
		result, err := deps.ConnectionService.Complete(
			c.Request.Context(),
			service.CompleteConnectionInput{
				AttemptID: request.AttemptID, AccountID: request.AccountID,
				DeviceID: request.DeviceID, DeviceName: request.DeviceName,
				DevicePublicKey: deviceKey, LoginPublicKey: loginKey,
				AccountAuthPublicKey: accountKey, DEKEnvelope: envelope,
				LoginProof: request.LoginProof, AccountProof: request.AccountProof,
				DeviceProof: request.DeviceProof,
			},
		)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		writeConnectionResult(c, http.StatusOK, result)
	}
}

type sessionChallengeRequest struct {
	DeviceID string `json:"deviceId"`
}

func StartSession(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request sessionChallengeRequest
		if err := decodeJSON(c, &request); err != nil {
			writeBadRequest(c)
			return
		}
		result, err := deps.ConnectionService.StartSession(
			c.Request.Context(), request.DeviceID)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"attemptId": result.AttemptID,
			"challenge": auth.EncodeBase64URL(result.Challenge),
			"expiresAt": result.ExpiresAt,
		})
	}
}

type completeSessionRequest struct {
	AttemptID string `json:"attemptId"`
	Signature string `json:"signature"`
}

func CompleteSession(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request completeSessionRequest
		if err := decodeJSON(c, &request); err != nil {
			writeBadRequest(c)
			return
		}
		result, err := deps.ConnectionService.CompleteSession(
			c.Request.Context(), request.AttemptID, request.Signature)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		writeConnectionResult(c, http.StatusOK, result)
	}
}

func GetBootstrap(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		result, err := deps.ConnectionService.Bootstrap(
			c.Request.Context(),
			c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxDeviceID),
		)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		writeBootstrap(c, result)
	}
}

func writeConnectionResult(c *gin.Context, status int, result service.ConnectionResult) {
	c.JSON(status, gin.H{
		"accountExists": result.AccountExists,
		"session": gin.H{
			"token": result.Session.Token, "expiresAt": result.Session.ExpiresAt,
			"proofBinding": result.Session.ProofBinding,
		},
		"bootstrap": bootstrapJSON(result.Bootstrap),
	})
}

func writeBootstrap(c *gin.Context, result service.Bootstrap) {
	c.JSON(http.StatusOK, bootstrapJSON(result))
}

func bootstrapJSON(value service.Bootstrap) gin.H {
	return gin.H{
		"accountId": value.AccountID, "username": value.Username,
		"deviceId":             value.DeviceID,
		"dekEnvelope":          auth.EncodeBase64URL(value.DEKEnvelope),
		"accountAuthPublicKey": auth.EncodeBase64URL(value.AccountAuthPublicKey),
		"cryptoStateRevision":  value.CryptoStateRevision,
		"dekEpoch":             value.DEKEpoch,
		"syncGroupId":          value.SyncGroupID,
		"groupRevision":        value.GroupRevision,
		"hasOtherSyncData":     value.HasOtherSyncData,
		"serverTimeMs":         value.ServerTimeMS,
	}
}
