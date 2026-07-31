package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"lumina-relay/internal/apperr"
	"lumina-relay/internal/service"
)

func decodeJSON(c *gin.Context, destination any) error {
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return err
	}
	if len(raw) == 0 || !utf8.Valid(raw) {
		return errors.New("请求体不是合法 JSON")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("请求体只能包含一个 JSON 值")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("请求体只能包含一个 JSON 值")
		}
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON key 必须是字符串")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("JSON 字段 %q 重复", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("JSON 结构非法")
	}
}

func writeBadRequest(c *gin.Context) {
	writeAPIError(c, apperr.New(apperr.CodeBadRequest, "请求格式无效"))
}

func writeAPIError(c *gin.Context, apiError *apperr.Error) {
	if apiError.Extra == nil {
		apiError.Extra = make(map[string]any)
	}
	requestID := strings.TrimSpace(c.GetHeader("X-Request-ID"))
	if requestID == "" || len(requestID) > 128 {
		requestID = uuid.NewString()
	}
	apiError.Extra["requestId"] = requestID
	apiError.WriteJSON(c.Writer)
	c.Abort()
}

func writeServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		writeBadRequest(c)
	case errors.Is(err, service.ErrInvalidCredentials):
		writeAPIError(c, apperr.New(apperr.CodeInvalidCredentials, "账号或证明无效"))
	case errors.Is(err, service.ErrAccountBecameExisting):
		writeAPIError(c, apperr.New(
			apperr.CodeAccountBecameExisting,
			"账号刚刚被创建，请按已有账号流程重试",
		))
	case errors.Is(err, service.ErrDeviceRevoked):
		writeAPIError(c, apperr.New(apperr.CodeDeviceRevoked, "设备已吊销"))
	case errors.Is(err, service.ErrInvalidSyncCode):
		writeAPIError(c, apperr.New(apperr.CodeInvalidSyncCode, "同步码无效"))
	case errors.Is(err, service.ErrAlreadyJoined):
		writeAPIError(c, apperr.New(apperr.CodeAlreadyJoined, "设备已在同一同步组"))
	case errors.Is(err, service.ErrGroupChanged):
		writeAPIError(c, apperr.New(apperr.CodeGroupChanged, "同步组已变化，请刷新后重试"))
	case errors.Is(err, service.ErrStaleManifest):
		writeAPIError(c, apperr.New(apperr.CodeStaleManifest, "Manifest 基础版本已过期"))
	case errors.Is(err, service.ErrManifestNotFound):
		writeAPIError(c, apperr.New(apperr.CodeManifestNotFound, "Manifest 不存在"))
	case errors.Is(err, service.ErrSessionFileNotFound):
		writeAPIError(c, apperr.New(apperr.CodeSessionFileNotFound, "会话文件不存在"))
	case errors.Is(err, service.ErrSessionIDConflict):
		writeAPIError(c, apperr.New(apperr.CodeSessionIDConflict, "会话 ID 已被其他同步组占用"))
	case errors.Is(err, service.ErrInvalidSessionID):
		writeAPIError(c, apperr.New(apperr.CodeInvalidSessionID, "会话 ID 格式无效"))
	case errors.Is(err, service.ErrBlockHashMismatch):
		writeAPIError(c, apperr.New(apperr.CodeBlockHashMismatch, "块 SHA-256 不匹配"))
	case errors.Is(err, service.ErrBlockNotFound):
		writeAPIError(c, apperr.New(apperr.CodeBlockNotFound, "块不存在"))
	case errors.Is(err, service.ErrBlockBusy):
		writeAPIError(c, apperr.New(apperr.CodeBlockBusy, "相同块正在上传或回收，请重试"))
	case errors.Is(err, service.ErrQuotaExceeded):
		writeAPIError(c, apperr.New(apperr.CodeQuotaExceeded, "账户存储配额不足"))
	default:
		writeAPIError(c, apperr.New(apperr.CodeInternalError, "服务内部错误"))
	}
}

func readRawBody(c *gin.Context) ([]byte, bool) {
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeAPIError(c, apperr.New(apperr.CodeBodyTooLarge, "请求体超过大小上限"))
		} else {
			writeBadRequest(c)
		}
		return nil, false
	}
	return raw, true
}
