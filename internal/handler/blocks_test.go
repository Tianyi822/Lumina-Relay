package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// TestBlocks_PutGet_RoundTrip 验证 PUT 块后 GET 回相同内容。
// PUT 用双层认证（Session+Signed），GET 用 Session。
func TestBlocks_PutGet_RoundTrip(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	_, _, tok, priv := env.registerSignedAccount(t)

	data := []byte("block ciphertext content")
	blockID := sha256Hex(t, data)

	// PUT /blocks/{blockId}（原始字节 body，非 JSON）
	rec := env.signedPUTRaw(t, tok, priv, "/blocks/"+blockID, data)
	if rec.Code != http.StatusCreated {
		t.Fatalf("PUT 应 201，得到 %d body=%s", rec.Code, rec.Body.String())
	}

	// GET /blocks/{blockId}
	req := newRequest(t, http.MethodGet, "/blocks/"+blockID, "")
	req.Header.Set("Authorization", "Bearer "+tok)
	rec = env.serve(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET 应 200，得到 %d", rec.Code)
	}
	got, _ := io.ReadAll(rec.Body)
	if !bytes.Equal(got, data) {
		t.Fatalf("内容不匹配：got %d bytes, want %d", len(got), len(data))
	}
}

// TestBlocks_Put_Idempotent 验证重复 PUT 返回 200。
func TestBlocks_Put_Idempotent(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	_, _, tok, priv := env.registerSignedAccount(t)
	data := []byte("dup content")
	blockID := sha256Hex(t, data)

	// 第一次 201
	rec := env.signedPUTRaw(t, tok, priv, "/blocks/"+blockID, data)
	if rec.Code != http.StatusCreated {
		t.Fatalf("首次应 201，得到 %d", rec.Code)
	}
	// 第二次 200
	rec = env.signedPUTRaw(t, tok, priv, "/blocks/"+blockID, data)
	if rec.Code != http.StatusOK {
		t.Fatalf("重复应 200，得到 %d", rec.Code)
	}
}

// TestBlocks_Put_HashMismatch 验证 sha256(body)!=blockId 返回 400。
func TestBlocks_Put_HashMismatch(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	_, _, tok, priv := env.registerSignedAccount(t)
	// 用错误的 blockId
	rec := env.signedPUTRaw(t, tok, priv, "/blocks/deadbeef", []byte("not matching"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("hash 不匹配应 400，得到 %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestBlocks_Have 验证批量查重返回 missing。
func TestBlocks_Have(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	_, _, tok, priv := env.registerSignedAccount(t)
	data := []byte("existing")
	existID := sha256Hex(t, data)
	env.signedPUTRaw(t, tok, priv, "/blocks/"+existID, data)

	missingID := sha256Hex(t, []byte("missing"))
	body := `{"ids":["` + existID + `","` + missingID + `"]}`
	rec := env.signedPOSTRaw(t, tok, priv, "/blocks/have", []byte(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("have 应 200，得到 %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Missing []string `json:"missing"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(resp.Missing) != 1 || resp.Missing[0] != missingID {
		t.Fatalf("missing = %v, want [%s]", resp.Missing, missingID)
	}
}

// TestBlocks_Get_NotFound 验证下载不存在的块返回 404。
func TestBlocks_Get_NotFound(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	_, _, tok, _ := env.registerSignedAccount(t)
	nope := sha256Hex(t, []byte("nope"))

	req := newRequest(t, http.MethodGet, "/blocks/"+nope, "")
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := env.serve(req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("应 404，得到 %d", rec.Code)
	}
}

// sha256Hex 计算内容的 sha256 hex。
func sha256Hex(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
