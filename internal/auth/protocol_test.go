package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"testing"
	"time"
)

func TestTranscriptGoldenVector(t *testing.T) {
	got, err := BuildTranscript("domain", []byte("A"), []byte{0, 1})
	if err != nil {
		t.Fatal(err)
	}
	const want = "646f6d61696e0000000141000000020001"
	if hex.EncodeToString(got) != want {
		t.Fatalf("transcript=%x want=%s", got, want)
	}
}

func TestProtocolTranscriptGoldenVectors(t *testing.T) {
	revision := int64(42)
	vectors := []struct {
		name  string
		want  string
		build func() ([]byte, error)
	}{
		{
			name: "account-create",
			want: "6c756d696e612d6163636f756e742d6372656174650000000169000000016100000002010200000005616c6963650000000461636374000000010300000001040000000105000000010600000003646576000000064c6170746f700000000107",
			build: func() ([]byte, error) {
				return BuildAccountCreateTranscript(
					"i", "a", "alice", "acct",
					[]byte{1, 2}, []byte{3}, []byte{4}, []byte{5}, []byte{6},
					"dev", "Laptop", []byte{7})
			},
		},
		{
			name: "login-proof",
			want: "6c756d696e612d6c6f67696e2d70726f6f660000000169000000016100000005616c69636500000002010200000003646576000000064c6170746f700000000107",
			build: func() ([]byte, error) {
				return BuildLoginTranscript(
					"i", "a", "alice", []byte{1, 2},
					"dev", "Laptop", []byte{7})
			},
		},
		{
			name: "device-session",
			want: "6c756d696e612d6465766963652d73657373696f6e0000000169000000016100000002010200000003646576",
			build: func() ([]byte, error) {
				return BuildSessionTranscript("i", "a", []byte{1, 2}, "dev")
			},
		},
		{
			name: "discard-sync-groups",
			want: "6c756d696e612d646973636172642d73796e632d67726f75707300000001690000000461636374000000036465760000000567726f757000000008000000000000002a",
			build: func() ([]byte, error) {
				return BuildDiscardGroupsTranscript("i", "acct", "dev", "group", revision)
			},
		},
		{
			name: "dek-envelope-aad",
			want: "6c756d696e612d64656b2d656e76656c6f7065000000016900000005616c6963650000000461636374000000020102",
			build: func() ([]byte, error) {
				return BuildDEKEnvelopeAAD("i", "alice", "acct", []byte{1, 2})
			},
		},
	}
	for _, vector := range vectors {
		t.Run(vector.name, func(t *testing.T) {
			got, err := vector.build()
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(got) != vector.want {
				t.Fatalf("transcript=%x want=%s", got, vector.want)
			}
		})
	}
}

func TestVerifySignatureUsesCanonicalBase64URL(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	message := []byte(BuildCanonical("put", "/blocks/x", "1", "nonce", []byte("body")))
	signature := ed25519.Sign(privateKey, message)
	encoded := base64.RawURLEncoding.EncodeToString(signature)
	if !VerifySignature(publicKey, message, encoded) {
		t.Fatal("合法签名未通过")
	}
	if VerifySignature(publicKey, message, encoded+"=") {
		t.Fatal("非规范 base64url 不应通过")
	}
}

func TestSessionTokenStrictlyBindsInstanceAndDevice(t *testing.T) {
	secret := bytes.Repeat([]byte{7}, 32)
	now := time.Now().Truncate(time.Second)
	issued, err := IssueSessionToken(secret, "instance", "account", "device", now)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ParseSessionToken(secret, "instance", issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.AccountID != "account" || claims.DeviceID != "device" || claims.TokenID == "" {
		t.Fatalf("claims=%+v", claims)
	}
	if _, err := ParseSessionToken(secret, "other-instance", issued.Token); err == nil {
		t.Fatal("不同 instance 不应接受 token")
	}
}

func TestChallengeTakeIsSingleUse(t *testing.T) {
	store := NewChallengeStore(10)
	attempt, err := store.Create(Attempt{Kind: AttemptConnection, Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Take(attempt.ID, AttemptConnection)
	if err != nil || first.Username != "alice" {
		t.Fatalf("首次消费=%+v err=%v", first, err)
	}
	if _, err := store.Take(attempt.ID, AttemptConnection); err == nil {
		t.Fatal("attempt 重放应失败")
	}
}
