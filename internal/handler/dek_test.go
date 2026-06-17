package handler

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"lumina-relay/internal/auth"
)

// TestPutAccountDEK_Success 验证：合法 session+签名 PUT /account/dek 返回 204，且 DEK 被更新。
// 见 sync-design §279-280：替换 dekEnvelope。
func TestPutAccountDEK_Success(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	accountID, _, tok, priv := env.registerSignedAccount(t)

	// 读取旧 DEK 验证后续变更
	rec := env.doGET("/account/dek?accountId=" + accountID)
	if rec.Code != http.StatusOK {
		t.Fatalf("读取旧 DEK 失败：%d", rec.Code)
	}

	// 构造 PUT 请求体（新 dekEnvelope）
	newSalt := "6e657753616c74" // "newSalt"
	newNonce := "6e65774e6f6e"  // "newNon"
	newCt := "6e65774374"       // "newCt"
	body := `{"dekSalt":"` + newSalt + `","dekNonce":"` + newNonce + `","dekCt":"` + newCt + `"}`
	rec = env.signedPUT(t, tok, priv, "/account/dek", body)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT /account/dek 应 204，得到 %d，body=%s", rec.Code, rec.Body.String())
	}

	// 验证 DEK 已更新：再 GET，应为新值
	rec = env.doGET("/account/dek?accountId=" + accountID)
	if rec.Code != http.StatusOK {
		t.Fatalf("读取新 DEK 失败：%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), newSalt) {
		t.Errorf("更新后 dek 应含新 salt %q，body=%s", newSalt, rec.Body.String())
	}
}

// TestPutAccountDEK_RequiresSignedWrite 验证：仅 session（无签名头）→ 401。
func TestPutAccountDEK_RequiresSignedWrite(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	_, _, tok, _ := env.registerSignedAccount(t)

	req := newRequest(t, http.MethodPut, "/account/dek", `{}`)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := env.serve(req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无签名应 401，得到 %d", rec.Code)
	}
}

// TestPutAccountDEK_BadSignature 验证签名错误→401。
func TestPutAccountDEK_BadSignature(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	_, _, tok, _ := env.registerSignedAccount(t)
	_, otherPriv, _ := ed25519.GenerateKey(nil)

	tsStr := strconv.FormatInt(time.Now().UnixMilli(), 10)
	nonce := "aabbccdd11223344"
	canon := auth.BuildCanonical(http.MethodPut, "/account/dek", tsStr, nonce, []byte("{}"))
	sig := ed25519.Sign(otherPriv, []byte(canon))

	req := newRequest(t, http.MethodPut, "/account/dek", `{}`)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Timestamp", tsStr)
	req.Header.Set("X-Nonce", nonce)
	req.Header.Set("X-Signature", hex.EncodeToString(sig))
	rec := env.serve(req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错误签名应 401，得到 %d", rec.Code)
	}
}
