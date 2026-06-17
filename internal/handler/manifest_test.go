package handler

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestGetManifest_ReturnsCurrent 验证 PUT 后 GET 返回最新版本 + ciphertext。
func TestGetManifest_ReturnsCurrent(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	_, _, tok, priv := env.registerSignedAccount(t)

	// 先 PUT 一个 manifest
	ctHex := hex.EncodeToString([]byte("manifest-content"))
	putBody := `{"ciphertext":"` + ctHex + `","baseVersion":0}`
	rec := env.signedPUT(t, tok, priv, "/manifest", putBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT 失败：%d body=%s", rec.Code, rec.Body.String())
	}

	// GET 应返回 version 1 + ciphertext
	req := newRequest(t, http.MethodGet, "/manifest", "")
	req.Header.Set("Authorization", "Bearer "+tok)
	rec = env.serve(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET 失败：%d", rec.Code)
	}
	var resp struct {
		Version    int    `json:"version"`
		Ciphertext string `json:"ciphertext"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if resp.Version != 1 {
		t.Errorf("version = %d, want 1", resp.Version)
	}
	if resp.Ciphertext != ctHex {
		t.Errorf("ciphertext = %q, want %q", resp.Ciphertext, ctHex)
	}
}

// TestPutManifest_Success 验证 PUT 返回新版本号。
func TestPutManifest_Success(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	_, _, tok, priv := env.registerSignedAccount(t)

	ctHex := hex.EncodeToString([]byte("v1-content"))
	body := `{"ciphertext":"` + ctHex + `","baseVersion":0}`
	rec := env.signedPUT(t, tok, priv, "/manifest", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT 应 200，得到 %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if resp.Version != 1 {
		t.Fatalf("version = %d, want 1", resp.Version)
	}
}

// TestPutManifest_StaleBase_Returns409 验证 baseVersion 过期时返回 409 + currentVersion。
// 见 sync-design §648。
func TestPutManifest_StaleBase_Returns409(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	_, _, tok, priv := env.registerSignedAccount(t)

	ctHex := hex.EncodeToString([]byte("v1"))
	// 第一次 PUT 成功（version → 1）
	env.signedPUT(t, tok, priv, "/manifest", `{"ciphertext":"`+ctHex+`","baseVersion":0}`)

	// 第二次用 baseVersion=0（过期，head 已是 1）→ 409
	rec := env.signedPUT(t, tok, priv, "/manifest", `{"ciphertext":"`+ctHex+`","baseVersion":0}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("应 409，得到 %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "stale_base") {
		t.Errorf("应含 stale_base，body=%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "currentVersion") {
		t.Errorf("应含 currentVersion，body=%s", rec.Body.String())
	}
}

// TestGetManifest_Empty 验证空账户 GET 返回 version 0。
func TestGetManifest_Empty(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	_, _, tok, _ := env.registerSignedAccount(t)

	req := newRequest(t, http.MethodGet, "/manifest", "")
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := env.serve(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET 应 200，得到 %d", rec.Code)
	}
	var resp struct {
		Version int `json:"version"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Version != 0 {
		t.Errorf("version = %d, want 0", resp.Version)
	}
}
