package handler

import (
	"encoding/json"
	"testing"
)

// TestHealth 验证 GET /health 返回 200 且 body 为 {"status":"ok"}。
// 见计划 Task 8a Step 1：路由未注册时 404 或编译失败。
func TestHealth(t *testing.T) {
	rec := doGET(newTestRouter(t), "/health")

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("status = %q, want ok", body.Status)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want application/json; charset=utf-8", ct)
	}
}
