// Package apperr 定义 lumina-relay API 的统一错误模型。
//
// 错误码与 HTTP 状态码映射见 sync-design §6.5：
//
//	bad_recovery_code   401
//	device_revoked      401
//	stale_base          409
//	block_hash_mismatch 400
//	quota_exceeded      413
//	rate_limited        429
//
// dek_unlock_failed 是客户端本地错误（无 HTTP），不在此处理。
// 未知 code 兜底 500，避免遗漏映射导致 200 误成功。
package apperr

import (
	"encoding/json"
	"net/http"
)

// Code 是机器可读的稳定错误码（snake_case 字符串）。
type Code string

// sync-design §6.5 定义的服务端错误码。
const (
	CodeBadRecoveryCode   Code = "bad_recovery_code"
	CodeDeviceRevoked     Code = "device_revoked"
	CodeStaleBase         Code = "stale_base"
	CodeBlockHashMismatch Code = "block_hash_mismatch"
	CodeQuotaExceeded     Code = "quota_exceeded"
	CodeRateLimited       Code = "rate_limited"
)

// Error 是 lumina-relay 的统一 API 错误。
// 通过 errors.As 可从任意包装链中提取，便于 service/handler 层判断。
type Error struct {
	Code    Code
	Message string
	Extra   map[string]any // 额外字段，如 stale_base 的 currentVersion
}

// New 构造一个 Error。message 为人类可读描述（可空，但建议填）。
func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// WithExtra 追加一个额外字段，返回同一 *Error 以支持链式调用。
// 用于 stale_base 携带 currentVersion 等冲突信息。
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
	case CodeBadRecoveryCode, CodeDeviceRevoked:
		return http.StatusUnauthorized
	case CodeStaleBase:
		return http.StatusConflict
	case CodeBlockHashMismatch:
		return http.StatusBadRequest
	case CodeQuotaExceeded:
		return http.StatusRequestEntityTooLarge
	case CodeRateLimited:
		return http.StatusTooManyRequests
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
