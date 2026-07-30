package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"lumina-relay/internal/auth"
	"lumina-relay/internal/db"
)

type connectionFixture struct {
	service        *ConnectionService
	q              *db.Queries
	instanceID     string
	loginPublic    ed25519.PublicKey
	loginPrivate   ed25519.PrivateKey
	accountPublic  ed25519.PublicKey
	accountPrivate ed25519.PrivateKey
	accountID      string
	deviceAID      string
}

func newConnectionFixture(t *testing.T) (*connectionFixture, func()) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "relay.db")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := db.New(database)
	instanceID, err := q.GetOrCreateInstanceID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	loginPublic, loginPrivate, _ := ed25519.GenerateKey(rand.Reader)
	accountPublic, accountPrivate, _ := ed25519.GenerateKey(rand.Reader)
	return &connectionFixture{
		service: NewConnectionService(
			q, auth.NewChallengeStore(100), instanceID,
			bytes.Repeat([]byte{9}, 32), 16),
		q: q, instanceID: instanceID,
		loginPublic: loginPublic, loginPrivate: loginPrivate,
		accountPublic: accountPublic, accountPrivate: accountPrivate,
		accountID: uuid.NewString(), deviceAID: uuid.NewString(),
	}, func() { _ = database.Close() }
}

func (f *connectionFixture) register(t *testing.T) ConnectionResult {
	t.Helper()
	ctx := context.Background()
	start, err := f.service.Start(ctx, "Alice")
	if err != nil || start.AccountExists {
		t.Fatalf("start=%+v err=%v", start, err)
	}
	devicePublic, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
	envelope := bytes.Repeat([]byte{6}, DEKEnvelopeBytes)
	hash := sha256.Sum256(envelope)
	transcript, err := auth.BuildAccountCreateTranscript(
		f.instanceID, start.AttemptID, "alice", f.accountID,
		start.Challenge, start.AuthSalt,
		f.loginPublic, f.accountPublic, hash[:], f.deviceAID, "Device A", devicePublic)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.service.Complete(ctx, CompleteConnectionInput{
		AttemptID: start.AttemptID, AccountID: f.accountID,
		DeviceID: f.deviceAID, DeviceName: "Device A",
		DevicePublicKey: devicePublic, LoginPublicKey: f.loginPublic,
		AccountAuthPublicKey: f.accountPublic, DEKEnvelope: envelope,
		LoginProof:   signText(f.loginPrivate, transcript),
		AccountProof: signText(f.accountPrivate, transcript),
		DeviceProof:  signText(devicePrivate, transcript),
	})
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	if result.AccountExists || result.Bootstrap.Username != "alice" ||
		result.Bootstrap.GroupRevision != 1 {
		t.Fatalf("注册结果=%+v", result)
	}
	return result
}

func TestUnknownUsernameRegistersAndExistingLoginStartsIsolated(t *testing.T) {
	fixture, cleanup := newConnectionFixture(t)
	defer cleanup()
	first := fixture.register(t)
	if _, err := fixture.q.PutDeviceManifest(
		context.Background(), fixture.deviceAID, 0, []byte("A-state"), time.Now().Unix(),
	); err != nil {
		t.Fatal(err)
	}

	start, err := fixture.service.Start(context.Background(), "ALICE")
	if err != nil || !start.AccountExists {
		t.Fatalf("existing start=%+v err=%v", start, err)
	}
	deviceBPublic, deviceBPrivate, _ := ed25519.GenerateKey(rand.Reader)
	deviceBID := uuid.NewString()
	transcript, err := auth.BuildLoginTranscript(
		fixture.instanceID, start.AttemptID, "alice", start.Challenge,
		deviceBID, "Device B", deviceBPublic)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.service.Complete(context.Background(), CompleteConnectionInput{
		AttemptID: start.AttemptID, DeviceID: deviceBID, DeviceName: "Device B",
		DevicePublicKey: deviceBPublic,
		LoginProof:      signText(fixture.loginPrivate, transcript),
		DeviceProof:     signText(deviceBPrivate, transcript),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.AccountExists || !second.Bootstrap.HasOtherSyncData {
		t.Fatalf("已有账号登录结果=%+v", second)
	}
	a, _ := fixture.q.GetDevice(context.Background(), first.Bootstrap.DeviceID)
	b, _ := fixture.q.GetDevice(context.Background(), second.Bootstrap.DeviceID)
	if a.SyncGroupID.String == b.SyncGroupID.String {
		t.Fatal("新登录设备不应自动加入旧同步组")
	}
}

func TestWrongPasswordProofDoesNotCreateDevice(t *testing.T) {
	fixture, cleanup := newConnectionFixture(t)
	defer cleanup()
	fixture.register(t)
	start, err := fixture.service.Start(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	devicePublic, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
	_, wrongPrivate, _ := ed25519.GenerateKey(rand.Reader)
	deviceID := uuid.NewString()
	transcript, _ := auth.BuildLoginTranscript(
		fixture.instanceID, start.AttemptID, "alice", start.Challenge,
		deviceID, "Attacker", devicePublic)
	_, err = fixture.service.Complete(context.Background(), CompleteConnectionInput{
		AttemptID: start.AttemptID, DeviceID: deviceID, DeviceName: "Attacker",
		DevicePublicKey: devicePublic,
		LoginProof:      signText(wrongPrivate, transcript),
		DeviceProof:     signText(devicePrivate, transcript),
	})
	if err != ErrInvalidCredentials {
		t.Fatalf("错误密码证明 err=%v", err)
	}
	var devices int
	if err := fixture.q.CountRows(context.Background(), &devices, db.TableDevices); err != nil {
		t.Fatal(err)
	}
	if devices != 1 {
		t.Fatalf("错误密码后 devices=%d want=1", devices)
	}
}

func TestConcurrentRegistrationHasSingleWinner(t *testing.T) {
	fixture, cleanup := newConnectionFixture(t)
	defer cleanup()
	ctx := context.Background()
	startA, err := fixture.service.Start(ctx, "Race.User")
	if err != nil || startA.AccountExists {
		t.Fatalf("start A=%+v err=%v", startA, err)
	}
	startB, err := fixture.service.Start(ctx, "race.user")
	if err != nil || startB.AccountExists {
		t.Fatalf("start B=%+v err=%v", startB, err)
	}

	makeInput := func(start ConnectionStart) CompleteConnectionInput {
		accountID := uuid.NewString()
		deviceID := uuid.NewString()
		devicePublic, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
		envelope := bytes.Repeat(
			[]byte(accountID[:1]), DEKEnvelopeBytes)
		envelopeHash := sha256.Sum256(envelope)
		transcript, buildErr := auth.BuildAccountCreateTranscript(
			fixture.instanceID, start.AttemptID, "race.user", accountID,
			start.Challenge, start.AuthSalt,
			fixture.loginPublic, fixture.accountPublic, envelopeHash[:],
			deviceID, "Race Device", devicePublic)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		return CompleteConnectionInput{
			AttemptID: start.AttemptID, AccountID: accountID,
			DeviceID: deviceID, DeviceName: "Race Device",
			DevicePublicKey: devicePublic, LoginPublicKey: fixture.loginPublic,
			AccountAuthPublicKey: fixture.accountPublic, DEKEnvelope: envelope,
			LoginProof:   signText(fixture.loginPrivate, transcript),
			AccountProof: signText(fixture.accountPrivate, transcript),
			DeviceProof:  signText(devicePrivate, transcript),
		}
	}
	inputs := []CompleteConnectionInput{makeInput(startA), makeInput(startB)}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, input := range inputs {
		input := input
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := fixture.service.Complete(ctx, input)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	var succeeded, becameExisting int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrAccountBecameExisting):
			becameExisting++
		default:
			t.Fatalf("并发抢注出现意外错误：%v", err)
		}
	}
	if succeeded != 1 || becameExisting != 1 {
		t.Fatalf("并发抢注 succeeded=%d becameExisting=%d", succeeded, becameExisting)
	}
}

func signText(privateKey ed25519.PrivateKey, message []byte) string {
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, message))
}
