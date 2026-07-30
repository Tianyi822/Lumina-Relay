// Package apperr 定义 lumina-relay API 的统一错误模型。
//
// 错误码与 HTTP 状态码映射由无版本 API 契约固定。
// 未知 code 兜底 500，避免遗漏映射导致 200 误成功。
package apperr

import (
	"encoding/json"
	"net/http"
)

// Code 是机器可读的稳定错误码（snake_case 字符串）。
type Code string

// 无版本协议稳定错误码。
const (
	CodeInvalidCredentials    Code = "invalid_credentials"
	CodeAccountBecameExisting Code = "account_became_existing"
	CodeInvalidDeviceProof    Code = "invalid_device_proof"
	CodeDeviceRevoked         Code = "device_revoked"
	CodeInvalidSyncCode       Code = "invalid_sync_code"
	CodeAlreadyJoined         Code = "already_joined"
	CodeStaleManifest         Code = "stale_manifest"
	CodeGroupChanged          Code = "group_changed"
	CodeBlockHashMismatch     Code = "block_hash_mismatch"
	CodeBlockNotFound         Code = "block_not_found"
	CodeManifestNotFound      Code = "manifest_not_found"
	CodeBlockBusy             Code = "block_busy"
	CodeQuotaExceeded         Code = "quota_exceeded"
	CodeRateLimited           Code = "rate_limited"
	CodeBadRequest            Code = "bad_request"
	CodeBodyTooLarge          Code = "body_too_large"
	CodeInternalError         Code = "internal_error"
)

// Error 是 lumina-relay 的统一 API 错误。
// 通过 errors.As 可从任意包装链中提取，便于 service/handler 层判断。
type Error struct {
	Code    Code
	Message string
	Extra   map[string]any // 额外字段，如 stale_manifest 的 currentVersion
}

// New 构造一个 Error。message 为人类可读描述（可空，但建议填）。
func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// WithExtra 追加一个额外字段，返回同一 *Error 以支持链式调用。
// 用于 stale_manifest 携带 currentVersion 等冲突信息。
// 设计上 Error 由构造方独占（New 后立即配置并返回），不共享 map。
func (e *Error) WithExtra(key string, value any) *Error {
	if e.Extra == nil {
		e.Extra = make(map[string]any)
	}
	e.Extra[key] = value
	return e
}

// Error 实现 error 接口，输出 "code: message" 形式。
func (e *Error) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

// HTTPStatus 返回该错误对应的 HTTP 状态码。
// 未知 code 兜底 500（见 sync-design §6.5 兜底原则）。
func (e *Error) HTTPStatus() int {
	switch e.Code {
	case CodeInvalidCredentials, CodeInvalidDeviceProof,
		CodeDeviceRevoked, CodeInvalidSyncCode:
		return http.StatusUnauthorized
	case CodeAccountBecameExisting, CodeAlreadyJoined,
		CodeStaleManifest, CodeGroupChanged, CodeBlockBusy:
		return http.StatusConflict
	case CodeBlockHashMismatch, CodeBadRequest:
		return http.StatusBadRequest
	case CodeBlockNotFound, CodeManifestNotFound:
		return http.StatusNotFound
	case CodeQuotaExceeded, CodeBodyTooLarge:
		return http.StatusRequestEntityTooLarge
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodeInternalError:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// WriteJSON 将错误以 sync-design §6.5 的统一格式写入 ResponseWriter：
//
//	{ "error": { "code": "...", "message": "...", <extra>... } }
//
// 同时设置 Content-Type 与对应 HTTP 状态码。Extra 字段平铺到 error 对象内。
func (e *Error) WriteJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.HTTPStatus())

	payload := map[string]any{
		"code":    string(e.Code),
		"message": e.Message,
	}
	for k, v := range e.Extra {
		payload[k] = v
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": payload})
}
