package handler

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
)

// TestGetAccountDEK_ReturnsEnvelope 验证：
// 注册账户后 GET /account/dek?accountId=... 返回 200 + dekEnvelope（hex 字段）。
// 见 sync-design §620：响应 { "dekEnvelope": { salt, nonce, ct } }。
func TestGetAccountDEK_ReturnsEnvelope(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	// 注册一个账户，拿到 accountId 与写入的 dek 字段
	wantSalt := "73616c74" // "salt"
	wantNonce := "6e6f6e"  // "non"
	wantCt := "6374"       // "ct"
	accountID := env.registerAccount(t, registerBody{
		RecoveryCodeHash: "6861", // "ha"
		DekSalt:          wantSalt,
		DekNonce:         wantNonce,
		DekCt:            wantCt,
		DevicePubKey:     "aabb",
		DeviceName:       "dev",
	})

	rec := env.doGET("/account/dek?accountId=" + accountID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		DekEnvelope struct {
			Salt  string `json:"salt"`
			Nonce string `json:"nonce"`
			Ct    string `json:"ct"`
		} `json:"dekEnvelope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if resp.DekEnvelope.Salt != wantSalt {
		t.Errorf("salt = %q, want %q", resp.DekEnvelope.Salt, wantSalt)
	}
	if resp.DekEnvelope.Nonce != wantNonce {
		t.Errorf("nonce = %q, want %q", resp.DekEnvelope.Nonce, wantNonce)
	}
	if resp.DekEnvelope.Ct != wantCt {
		t.Errorf("ct = %q, want %q", resp.DekEnvelope.Ct, wantCt)
	}

	// 额外验证：hex 可解码（证明不是把乱码当 hex 传）
	for _, f := range []string{resp.DekEnvelope.Salt, resp.DekEnvelope.Nonce, resp.DekEnvelope.Ct} {
		if _, err := hex.DecodeString(f); err != nil {
			t.Errorf("字段 %q 不是合法 hex：%v", f, err)
		}
	}
}

// TestGetAccountDEK_NotFound 验证查询不存在的账户返回 404。
func TestGetAccountDEK_NotFound(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	rec := env.doGET("/account/dek?accountId=nonexistent-uuid")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestGetAccountDEK_MissingParam 验证缺少 accountId 参数返回 400。
func TestGetAccountDEK_MissingParam(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	rec := env.doGET("/account/dek")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestGetAccountDEK_ByRecoveryHash 验证 GET /account/dek?recoveryCodeHash= 返回
// accountId + dekEnvelope（换设备流程用）。
func TestGetAccountDEK_ByRecoveryHash(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	wantSalt := "73616c74"
	wantNonce := "6e6f6e"
	wantCt := "6374"
	hashHex := "686172686172" // 任意合法 hex（"harhar"），作为 recoveryCodeHash
	accountID := env.registerAccount(t, registerBody{
		RecoveryCodeHash: hashHex,
		DekSalt:          wantSalt,
		DekNonce:         wantNonce,
		DekCt:            wantCt,
		DevicePubKey:     "aabb",
		DeviceName:       "dev",
	})

	rec := env.doGET("/account/dek?recoveryCodeHash=" + hashHex)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		AccountID string `json:"accountId"`
		DekEnvelope struct {
			Salt  string `json:"salt"`
			Nonce string `json:"nonce"`
			Ct    string `json:"ct"`
		} `json:"dekEnvelope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if resp.AccountID != accountID {
		t.Errorf("accountId = %q, want %q", resp.AccountID, accountID)
	}
	if resp.DekEnvelope.Salt != wantSalt || resp.DekEnvelope.Nonce != wantNonce || resp.DekEnvelope.Ct != wantCt {
		t.Errorf("dek 字段不匹配：salt=%q nonce=%q ct=%q", resp.DekEnvelope.Salt, resp.DekEnvelope.Nonce, resp.DekEnvelope.Ct)
	}
}

// TestGetAccountDEK_RecoveryHashNotFound 验证恢复码查无此户返 404。
func TestGetAccountDEK_RecoveryHashNotFound(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	rec := env.doGET("/account/dek?recoveryCodeHash=deadbeef")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestGetAccountDEK_BothParams 验证同时传 accountId 与 recoveryCodeHash 返 400。
func TestGetAccountDEK_BothParams(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	rec := env.doGET("/account/dek?accountId=x&recoveryCodeHash=y")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400（两参数互斥）", rec.Code)
	}
}

// TestGetAccountDEK_BadRecoveryHashHex 验证非法 hex 返 400。
func TestGetAccountDEK_BadRecoveryHashHex(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	rec := env.doGET("/account/dek?recoveryCodeHash=zzz")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400（非法 hex）", rec.Code)
	}
}
