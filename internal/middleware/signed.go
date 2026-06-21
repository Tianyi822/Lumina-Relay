package middleware

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/auth"
)

// 签名头名称（见 sync-design §574-577）。
const (
	headerTimestamp = "X-Timestamp"
	headerNonce     = "X-Nonce"
	headerSignature = "X-Signature"
)

// maxSkew 是允许的 timestamp 偏差。计划 Task 12b Step 3 定为 ±5min
// （与 nonce TTL 一致；sync-design §578 原文 ±60s 被计划放宽）。
const maxSkew = 5 * time.Minute

// RequireSignedWrite 返回一个 gin 中间件，校验写操作的设备密钥签名。
// 前置依赖 RequireSession（从 context 取 devicePubKey）。
// 校验：X-Timestamp ±5min、X-Nonce 去重、X-Signature Ed25519。
// 任一失败返回 401。
func RequireSignedWrite(nonceStore *auth.NonceStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 取 session 注入的设备公钥（hex）
		pubHex, _ := c.Get(CtxDeviceKey)
		pubKey, err := auth.DecodePublicKey(pubHex.(string))
		if err != nil {
			writeUnauthorized(c, "invalid device public key")
			return
		}

		// 读 body（算 canonical 用），重建供后续 handler 重读。
		// 注意：body 可能被 BodyLimit 中间件用 MaxBytesReader 包裹，
		// 超限时 ReadAll 返回 *http.MaxBytesError，需经 HandleBodyReadError
		// 统一处理（MaxBytesReader 已写 413，此处不可重复写）。
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if HandleBodyReadError(c, err, http.StatusUnauthorized, "unauthorized", "read body failed") {
			return
		}
		_ = c.Request.Body.Close()
		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		tsStr := c.GetHeader(headerTimestamp)
		nonce := c.GetHeader(headerNonce)
		sigHex := c.GetHeader(headerSignature)
		if tsStr == "" || nonce == "" || sigHex == "" {
			writeUnauthorized(c, "missing signature headers")
			return
		}

		// timestamp ±5min
		tsMs, err := strconv.ParseInt(tsStr, 10, 64)
		if err != nil {
			writeUnauthorized(c, "invalid timestamp")
			return
		}
		tsTime := time.UnixMilli(tsMs)
		now := time.Now()
		if tsTime.Before(now.Add(-maxSkew)) || tsTime.After(now.Add(maxSkew)) {
			writeUnauthorized(c, "timestamp out of range")
			return
		}

		// nonce 去重
		if !nonceStore.UseOnce(nonce) {
			writeUnauthorized(c, "nonce already used")
			return
		}

		// 构造 canonical（path 用原始 URL，含 query）
		canon := auth.BuildCanonical(c.Request.Method, c.Request.URL.Path, tsStr, nonce, bodyBytes)
		if !auth.VerifySignature(pubKey, canon, sigHex) {
			writeUnauthorized(c, "signature verification failed")
			return
		}

		c.Next()
	}
}
