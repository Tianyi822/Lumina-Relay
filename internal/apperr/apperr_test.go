package apperr

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
)

// TestHTTPStatus 验证每个已知 code 映射到正确的 HTTP 状态码，
// 且未知 code 兜底 500（见 sync-design §6.5）。
func TestHTTPStatus(t *testing.T) {
	cases := []struct {
		name string
		err  *Error
		want int
	}{
		{"invalid_credentials→401", New(CodeInvalidCredentials, ""), 401},
		{"device_revoked→401", New(CodeDeviceRevoked, ""), 401},
		{"stale_manifest→409", New(CodeStaleManifest, ""), 409},
		{"block_hash_mismatch→400", New(CodeBlockHashMismatch, ""), 400},
		{"quota_exceeded→413", New(CodeQuotaExceeded, ""), 413},
		{"rate_limited→429", New(CodeRateLimited, ""), 429},
		{"unknown→500", New(Code("totally_unknown"), ""), 500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.err.HTTPStatus(); got != c.want {
				t.Fatalf("HTTPStatus = %d, want %d", got, c.want)
			}
		})
	}
}

// TestError_Message 验证 Error() 输出稳定可读，且能参与 errors.Is 链。
func TestError_Message(t *testing.T) {
	e := New(CodeStaleManifest, "base version 137 is behind current 139")
	if got := e.Error(); got != "stale_manifest: base version 137 is behind current 139" {
		t.Fatalf("Error() = %q", got)
	}
}

// TestWriteJSON_StaleBaseIncludesExtra 验证 WriteJSON 把 extra 字段（如 currentVersion）
// 合并进 error JSON，且 HTTP 状态码与 code 一致（409）。
func TestWriteJSON_StaleManifestIncludesExtra(t *testing.T) {
	rec := httptest.NewRecorder()
	e := New(CodeStaleManifest, "base version 137 is behind current 139").
		WithExtra("currentVersion", 139)

	e.WriteJSON(rec)

	if got := rec.Code; got != 409 {
		t.Fatalf("status = %d, want 409", got)
	}
	var body struct {
		Error struct {
			Code           string `json:"code"`
			Message        string `json:"message"`
			CurrentVersion int    `json:"currentVersion"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if body.Error.Code != "stale_manifest" {
		t.Errorf("code = %q, want stale_manifest", body.Error.Code)
	}
	if body.Error.Message == "" {
		t.Errorf("message 不应为空")
	}
	if body.Error.CurrentVersion != 139 {
		t.Errorf("currentVersion = %d, want 139", body.Error.CurrentVersion)
	}
}

// TestWriteJSON_ContentType 验证响应头 Content-Type 为 application/json。
func TestWriteJSON_ContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	New(CodeRateLimited, "slow down").WriteJSON(rec)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

// TestError_As 验证 Error 可被 errors.As 提取，便于上层判断错误类型。
func TestError_As(t *testing.T) {
	wrapped := errors.Join(New(CodeDeviceRevoked, "bye"))
	var target *Error
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As 未能提取 *Error")
	}
	if target.Code != CodeDeviceRevoked {
		t.Fatalf("code = %q, want device_revoked", target.Code)
	}
}
