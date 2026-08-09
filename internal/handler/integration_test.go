package handler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"

	"lumina-relay/internal/auth"
	"lumina-relay/internal/db"
	"lumina-relay/internal/service"
	"lumina-relay/internal/store"
)

type integrationEnv struct {
	router         http.Handler
	q              *db.Queries
	blockStore     *store.BlockStore
	eventHub       *service.EventHub
	instanceID     string
	loginPublic    ed25519.PublicKey
	loginPrivate   ed25519.PrivateKey
	accountPublic  ed25519.PublicKey
	accountPrivate ed25519.PrivateKey
	accountID      string
	cleanup        func()
}

type testProfile struct {
	deviceID      string
	private       ed25519.PrivateKey
	token         string
	groupRevision int64
}

func newIntegrationEnv(t *testing.T, mutate ...func(*Deps)) *integrationEnv {
	t.Helper()
	root := t.TempDir()
	dsn := filepath.Join(root, "relay.db")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := db.New(database)
	instanceID, err := q.GetOrCreateInstanceID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	jwtSecret := bytes.Repeat([]byte{8}, 32)
	blockStore := store.NewBlockStore(filepath.Join(root, "blocks"))
	hub := service.NewEventHub()
	tickets := service.NewEventTicketStore()
	deps := Deps{
		ConnectionService: service.NewConnectionService(
			q, auth.NewChallengeStore(100), instanceID, jwtSecret, 16),
		SyncService:        service.NewSyncService(q, instanceID, jwtSecret),
		ManifestService:    service.NewManifestService(q),
		BlocksService:      service.NewBlocksService(q, blockStore),
		SessionFileService: service.NewSessionFileService(q),
		EventHub:           hub, EventTickets: tickets, Queries: q,
		JWTSecret: jwtSecret, InstanceID: instanceID,
	}
	for _, fn := range mutate {
		fn(&deps)
	}
	loginPublic, loginPrivate, _ := ed25519.GenerateKey(rand.Reader)
	accountPublic, accountPrivate, _ := ed25519.GenerateKey(rand.Reader)
	return &integrationEnv{
		router: NewRouter(deps), q: q, blockStore: blockStore, eventHub: hub,
		instanceID:  instanceID,
		loginPublic: loginPublic, loginPrivate: loginPrivate,
		accountPublic: accountPublic, accountPrivate: accountPrivate,
		accountID: uuid.NewString(), cleanup: func() { _ = database.Close() },
	}
}

func (e *integrationEnv) postJSON(t *testing.T, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	e.router.ServeHTTP(rec, req)
	return rec
}

type startWire struct {
	AccountExists bool   `json:"accountExists"`
	AttemptID     string `json:"attemptId"`
	Challenge     string `json:"challenge"`
	AuthSalt      string `json:"authSalt"`
}

type connectionWire struct {
	AccountExists bool `json:"accountExists"`
	Session       struct {
		Token string `json:"token"`
	} `json:"session"`
	Bootstrap struct {
		DeviceID         string `json:"deviceId"`
		GroupRevision    int64  `json:"groupRevision"`
		HasOtherSyncData bool   `json:"hasOtherSyncData"`
	} `json:"bootstrap"`
}

func (e *integrationEnv) registerA(t *testing.T) testProfile {
	t.Helper()
	startRec := e.postJSON(t, "/connections/start", map[string]string{"username": "Alice"})
	if startRec.Code != http.StatusOK {
		t.Fatalf("start register=%d %s", startRec.Code, startRec.Body.String())
	}
	var start startWire
	if err := json.Unmarshal(startRec.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	salt, _ := base64.RawURLEncoding.DecodeString(start.AuthSalt)
	devicePublic, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
	deviceID := uuid.NewString()
	envelope := bytes.Repeat([]byte{6}, service.DEKEnvelopeBytes)
	envelopeHash := sha256.Sum256(envelope)
	transcript, err := auth.BuildAccountCreateTranscript(
		e.instanceID, start.AttemptID, "alice", e.accountID,
		mustDecodeBase64URL(t, start.Challenge), salt,
		e.loginPublic, e.accountPublic, envelopeHash[:],
		deviceID, "Device A", devicePublic)
	if err != nil {
		t.Fatal(err)
	}
	rec := e.postJSON(t, "/connections/complete", map[string]string{
		"attemptId": start.AttemptID, "accountId": e.accountID,
		"deviceId": deviceID, "deviceName": "Device A",
		"devicePublicKey":      base64.RawURLEncoding.EncodeToString(devicePublic),
		"loginPublicKey":       base64.RawURLEncoding.EncodeToString(e.loginPublic),
		"accountAuthPublicKey": base64.RawURLEncoding.EncodeToString(e.accountPublic),
		"dekEnvelope":          base64.RawURLEncoding.EncodeToString(envelope),
		"loginProof":           signed(e.loginPrivate, transcript),
		"accountProof":         signed(e.accountPrivate, transcript),
		"deviceProof":          signed(devicePrivate, transcript),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("complete register=%d %s", rec.Code, rec.Body.String())
	}
	var result connectionWire
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return testProfile{
		deviceID: deviceID, private: devicePrivate,
		token: result.Session.Token, groupRevision: result.Bootstrap.GroupRevision,
	}
}

func (e *integrationEnv) login(t *testing.T, name string) (testProfile, connectionWire) {
	t.Helper()
	startRec := e.postJSON(t, "/connections/start", map[string]string{"username": "alice"})
	var start startWire
	if err := json.Unmarshal(startRec.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	if !start.AccountExists {
		t.Fatal("已有账号应返回 accountExists=true")
	}
	challenge, _ := base64.RawURLEncoding.DecodeString(start.Challenge)
	devicePublic, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
	deviceID := uuid.NewString()
	transcript, _ := auth.BuildLoginTranscript(
		e.instanceID, start.AttemptID, "alice", challenge,
		deviceID, name, devicePublic)
	rec := e.postJSON(t, "/connections/complete", map[string]string{
		"attemptId": start.AttemptID, "deviceId": deviceID, "deviceName": name,
		"devicePublicKey": base64.RawURLEncoding.EncodeToString(devicePublic),
		"loginProof":      signed(e.loginPrivate, transcript),
		"deviceProof":     signed(devicePrivate, transcript),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("complete login=%d %s", rec.Code, rec.Body.String())
	}
	var result connectionWire
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return testProfile{
		deviceID: deviceID, private: devicePrivate,
		token: result.Session.Token, groupRevision: result.Bootstrap.GroupRevision,
	}, result
}

func (e *integrationEnv) signed(
	t *testing.T,
	profile testProfile,
	method, path string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	nonceText := base64.RawURLEncoding.EncodeToString(nonce)
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	canonical := auth.BuildCanonical(method, path, timestamp, nonceText, body)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+profile.token)
	req.Header.Set("X-Timestamp", timestamp)
	req.Header.Set("X-Nonce", nonceText)
	req.Header.Set("X-Signature", signed(profile.private, []byte(canonical)))
	e.router.ServeHTTP(rec, req)
	return rec
}

func TestABCTransitiveSyncAndBlockIsolation(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()
	a := env.registerA(t)

	manifestA := []byte("opaque-manifest-A")
	rec := env.signed(t, a, http.MethodPut, "/manifests/self/0", manifestA)
	if rec.Code != http.StatusOK {
		t.Fatalf("A manifest=%d %s", rec.Code, rec.Body.String())
	}
	block := []byte("encrypted-block-A")
	blockHash := sha256.Sum256(block)
	blockID := hex.EncodeToString(blockHash[:])
	rec = env.signed(t, a, http.MethodPut, "/blocks/"+blockID, block)
	if rec.Code != http.StatusCreated {
		t.Fatalf("A block=%d %s", rec.Code, rec.Body.String())
	}

	b, bLogin := env.login(t, "Device B")
	if !bLogin.Bootstrap.HasOtherSyncData {
		t.Fatal("B 登录后应获知存在其他同步数据")
	}
	rec = env.signed(t, b, http.MethodGet, "/blocks/"+blockID, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("合并前 B 读取 A block=%d", rec.Code)
	}

	rec = env.signed(t, a, http.MethodPost, "/sync-codes", nil)
	var codeResponse struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &codeResponse); err != nil {
		t.Fatal(err)
	}
	redeemBody, _ := json.Marshal(map[string]string{"code": codeResponse.Code})
	rec = env.signed(t, b, http.MethodPost, "/sync-codes/redeem", redeemBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("B redeem=%d %s", rec.Code, rec.Body.String())
	}
	rec = env.signed(t, b, http.MethodGet, "/blocks/"+blockID, nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), block) {
		t.Fatalf("合并后 B block=%d body=%q", rec.Code, rec.Body.Bytes())
	}
	rec = env.signed(t, b, http.MethodGet, "/manifests/"+a.deviceID+"/1", nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), manifestA) {
		t.Fatalf("合并后 B manifest=%d body=%q", rec.Code, rec.Body.Bytes())
	}

	c, _ := env.login(t, "Device C")
	rec = env.signed(t, b, http.MethodPost, "/sync-codes", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &codeResponse); err != nil {
		t.Fatal(err)
	}
	redeemBody, _ = json.Marshal(map[string]string{"code": codeResponse.Code})
	rec = env.signed(t, c, http.MethodPost, "/sync-codes/redeem", redeemBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("C redeem=%d %s", rec.Code, rec.Body.String())
	}
	rec = env.signed(t, c, http.MethodGet, "/manifests", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("C heads=%d %s", rec.Code, rec.Body.String())
	}
	var heads struct {
		Heads []any `json:"heads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &heads); err != nil {
		t.Fatal(err)
	}
	if len(heads.Heads) != 3 {
		t.Fatalf("C 加入后 heads=%d want=3", len(heads.Heads))
	}
}

func TestListDevicesUsesCamelCaseWireFields(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()
	profile := env.registerA(t)

	rec := env.signed(t, profile, http.MethodGet, "/devices", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("devices=%d %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Devices []map[string]json.RawMessage `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Devices) != 1 {
		t.Fatalf("devices=%d want=1", len(response.Devices))
	}
	const want = "createdAt,deviceId,deviceName,lastSeenAt,status"
	if got := sortedJSONKeys(response.Devices[0]); got != want {
		t.Fatalf("device keys=%q want=%q body=%s", got, want, rec.Body.String())
	}
}

func TestListManifestHeadsUsesCamelCaseWireFields(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()
	profile := env.registerA(t)

	rec := env.signed(t, profile, http.MethodGet, "/manifests", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("manifests=%d %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Heads []map[string]json.RawMessage `json:"heads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Heads) != 1 {
		t.Fatalf("heads=%d want=1", len(response.Heads))
	}
	const want = "currentVersion,deviceId,updatedAt"
	if got := sortedJSONKeys(response.Heads[0]); got != want {
		t.Fatalf("head keys=%q want=%q body=%s", got, want, rec.Body.String())
	}
}

func TestUnsafePruneRouteIsNotExposed(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()
	profile := env.registerA(t)
	body, err := json.Marshal(map[string]any{
		"groupRevision": profile.groupRevision,
		"keep":          []string{},
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := env.signed(t, profile, http.MethodPost, "/blocks/prune", body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("prune=%d want=404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestLegacyAndVersionedRoutesAreGone(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()
	for _, path := range []string{"/v1/accounts", "/account/register", "/manifest"} {
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status=%d want=404", path, rec.Code)
		}
	}
}

func TestPersistentNonceRejectsExactReplay(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()
	profile := env.registerA(t)
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	nonceText := base64.RawURLEncoding.EncodeToString(nonce)
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	canonical := auth.BuildCanonical(http.MethodGet, "/bootstrap", timestamp, nonceText, nil)
	signature := signed(profile.private, []byte(canonical))

	send := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/bootstrap", nil)
		req.Header.Set("Authorization", "Bearer "+profile.token)
		req.Header.Set("X-Timestamp", timestamp)
		req.Header.Set("X-Nonce", nonceText)
		req.Header.Set("X-Signature", signature)
		env.router.ServeHTTP(rec, req)
		return rec
	}
	if first := send(); first.Code != http.StatusOK {
		t.Fatalf("首次 proof=%d %s", first.Code, first.Body.String())
	}
	if replay := send(); replay.Code != http.StatusUnauthorized {
		t.Fatalf("重放 proof=%d %s", replay.Code, replay.Body.String())
	}
}

func TestLeakedSessionTokenCannotBypassDeviceProof(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()
	profile := env.registerA(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/bootstrap", nil)
	req.Header.Set("Authorization", "Bearer "+profile.token)
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("仅持有 token status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAccountDataBodyLimitsReturn413(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()
	profile := env.registerA(t)
	tests := []struct {
		name   string
		method string
		path   string
		body   []byte
	}{
		{
			name: "json", method: http.MethodPost, path: "/blocks/missing",
			body: bytes.Repeat([]byte("x"), (64<<10)+1),
		},
		{
			name: "manifest", method: http.MethodPut, path: "/manifests/self/0",
			body: bytes.Repeat([]byte("x"), (4<<20)+1),
		},
		{
			name: "block", method: http.MethodPut,
			path: "/blocks/" + strings.Repeat("a", 64),
			body: bytes.Repeat([]byte("x"), (1<<20)+1),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := env.signed(t, profile, test.method, test.path, test.body)
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestDeviceKeyCanResumeSessionWithoutPassword(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()
	profile := env.registerA(t)
	startRec := env.postJSON(t, "/session-challenges", map[string]string{
		"deviceId": profile.deviceID,
	})
	if startRec.Code != http.StatusOK {
		t.Fatalf("session challenge=%d %s", startRec.Code, startRec.Body.String())
	}
	var start struct {
		AttemptID string `json:"attemptId"`
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(startRec.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	challenge, _ := base64.RawURLEncoding.DecodeString(start.Challenge)
	transcript, _ := auth.BuildSessionTranscript(
		env.instanceID, start.AttemptID, challenge, profile.deviceID)
	rec := env.postJSON(t, "/sessions", map[string]string{
		"attemptId": start.AttemptID,
		"signature": signed(profile.private, transcript),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("session resume=%d %s", rec.Code, rec.Body.String())
	}
	var result connectionWire
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Bootstrap.DeviceID != profile.deviceID || result.Session.Token == "" {
		t.Fatalf("resume result=%+v", result)
	}
}

func TestExplicitDiscardRevokesOldGroupAndReleasesQuota(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()
	a := env.registerA(t)
	block := []byte("abandoned-encrypted-block")
	hash := sha256.Sum256(block)
	blockID := hex.EncodeToString(hash[:])
	if rec := env.signed(t, a, http.MethodPut, "/blocks/"+blockID, block); rec.Code != http.StatusCreated {
		t.Fatalf("上传旧组块=%d %s", rec.Code, rec.Body.String())
	}
	b, _ := env.login(t, "Fresh Device")
	deviceB, err := env.q.GetDevice(t.Context(), b.deviceID)
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := auth.BuildDiscardGroupsTranscript(
		env.instanceID, env.accountID, b.deviceID,
		deviceB.SyncGroupID.String, b.groupRevision)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"groupRevision": b.groupRevision,
		"accountProof":  signed(env.accountPrivate, transcript),
	})
	rec := env.signed(t, b, http.MethodPost, "/sync-groups/discard-others", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("discard=%d %s", rec.Code, rec.Body.String())
	}
	rec = env.signed(t, a, http.MethodGet, "/bootstrap", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("旧设备仍可访问：%d %s", rec.Code, rec.Body.String())
	}
	account, err := env.q.GetAccount(t.Context(), env.accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.UsedBytes != 0 {
		t.Fatalf("废弃后 usedBytes=%d want=0", account.UsedBytes)
	}
	if !env.blockStore.Exists(blockID) {
		t.Fatal("孤儿块在安全宽限期内不应被立即物理删除")
	}
	object, err := env.q.GetBlockObject(t.Context(), blockID)
	if err != nil || !object.OrphanedAt.Valid || object.State != "active" {
		t.Fatalf("废弃后的块对象=%+v err=%v", object, err)
	}
}

func TestWebSocketTicketIsSingleUseAndReceivesManifestAndSessionEvents(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()
	profile := env.registerA(t)
	ticketRec := env.signed(t, profile, http.MethodPost, "/event-tickets", nil)
	if ticketRec.Code != http.StatusCreated {
		t.Fatalf("event ticket=%d %s", ticketRec.Code, ticketRec.Body.String())
	}
	var ticketResponse struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(ticketRec.Body.Bytes(), &ticketResponse); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("当前沙箱不允许本地 WebSocket listener：%v", err)
	}
	server := &http.Server{Handler: env.router}
	go server.Serve(listener)
	defer server.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws://" + listener.Addr().String() + "/events"
	connection, response, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"lumina-events", "ticket." + ticketResponse.Ticket},
	})
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("WebSocket 握手失败 status=%d err=%v", status, err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	var ready service.Event
	if err := wsjson.Read(ctx, connection, &ready); err != nil || ready.Type != "ready" {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}

	if rec := env.signed(
		t, profile, http.MethodPut, "/manifests/self/0", []byte("event-manifest"),
	); rec.Code != http.StatusOK {
		t.Fatalf("提交 Manifest=%d %s", rec.Code, rec.Body.String())
	}
	var event service.Event
	if err := wsjson.Read(ctx, connection, &event); err != nil ||
		event.Type != "manifest_updated" || event.Version != 1 {
		t.Fatalf("manifest event=%+v err=%v", event, err)
	}

	rec := env.signed(
		t, profile, http.MethodPut,
		"/session-files/session-1-a1b2c3/0", []byte("cipher"))
	if rec.Code != http.StatusOK {
		t.Fatalf("session PUT=%d %s", rec.Code, rec.Body.String())
	}
	var updated service.Event
	if err := wsjson.Read(ctx, connection, &updated); err != nil ||
		updated.Type != "session_file_updated" ||
		updated.SessionID != "session-1-a1b2c3" ||
		updated.Version != 1 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}

	rec = env.signed(
		t, profile, http.MethodDelete,
		"/session-files/session-1-a1b2c3/1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("session DELETE=%d %s", rec.Code, rec.Body.String())
	}
	var deleted service.Event
	if err := wsjson.Read(ctx, connection, &deleted); err != nil ||
		deleted.Type != "session_file_deleted" ||
		deleted.SessionID != "session-1-a1b2c3" ||
		deleted.Version != 0 {
		t.Fatalf("deleted=%+v err=%v", deleted, err)
	}

	replay, replayResponse, replayErr := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"lumina-events", "ticket." + ticketResponse.Ticket},
	})
	if replay != nil {
		replay.Close(websocket.StatusNormalClosure, "")
	}
	if replayErr == nil || replayResponse == nil ||
		replayResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ticket 重放 response=%v err=%v", replayResponse, replayErr)
	}
}

func TestSessionFileFullLifecycle(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()
	a := env.registerA(t)
	sid := "session-1753857600000-a1b2c3"
	base := "/session-files/" + sid

	rec := env.signed(t, a, http.MethodPut, base+"/0", []byte("cipher-v1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("创建=%d %s", rec.Code, rec.Body.String())
	}
	var put struct {
		Version int64 `json:"version"`
		Size    int64 `json:"size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &put); err != nil {
		t.Fatal(err)
	}
	if put.Version != 1 || put.Size != 9 {
		t.Fatalf("创建结果=%+v", put)
	}

	rec = env.signed(t, a, http.MethodPut, base+"/1", []byte("cipher-v2"))
	if rec.Code != http.StatusOK {
		t.Fatalf("覆盖=%d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &put); err != nil {
		t.Fatal(err)
	}
	if put.Version != 2 || put.Size != 9 {
		t.Fatalf("覆盖结果=%+v", put)
	}

	rec = env.signed(t, a, http.MethodGet, base, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "cipher-v2" {
		t.Fatalf("读取=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Session-File-Version"); got != "2" {
		t.Fatalf("版本头=%q want=2", got)
	}

	rec = env.signed(t, a, http.MethodGet, "/session-files", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表=%d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Sessions []struct {
			SessionID string `json:"sessionId"`
			Version   int64  `json:"version"`
			Size      int64  `json:"size"`
			UpdatedAt int64  `json:"updatedAt"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 1 ||
		list.Sessions[0].SessionID != sid ||
		list.Sessions[0].Version != 2 ||
		list.Sessions[0].Size != 9 ||
		list.Sessions[0].UpdatedAt <= 0 {
		t.Fatalf("列表=%+v", list.Sessions)
	}

	rec = env.signed(t, a, http.MethodDelete, base+"/1", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("过期删除=%d %s", rec.Code, rec.Body.String())
	}
	var conflict struct {
		Error struct {
			Code           string `json:"code"`
			CurrentVersion int64  `json:"currentVersion"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.Error.Code != "stale_session_file" ||
		conflict.Error.CurrentVersion != 2 {
		t.Fatalf("过期删除响应=%+v", conflict.Error)
	}

	rec = env.signed(t, a, http.MethodDelete, base+"/2", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除=%d %s", rec.Code, rec.Body.String())
	}
	var deleted struct {
		Deleted bool `json:"deleted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &deleted); err != nil {
		t.Fatal(err)
	}
	if !deleted.Deleted {
		t.Fatalf("删除响应=%+v", deleted)
	}
	rec = env.signed(t, a, http.MethodGet, base, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("删除后读取=%d want=404", rec.Code)
	}
}

func TestRemovedSessionRoutesReturn404(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()
	a := env.registerA(t)
	sid := "session-1-a1b2c3"

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/session-files/" + sid + "/append/1"},
		{http.MethodGet, "/session-files-index"},
		{http.MethodPut, "/session-files-index/0"},
	} {
		rec := env.signed(t, a, tc.method, tc.path, []byte("cipher"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s status=%d want=404", tc.method, tc.path, rec.Code)
		}
	}
}

func TestSessionIDConflictAcrossGroups(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()
	a := env.registerA(t)
	sid := "session-1-a1b2c3"
	path := "/session-files/" + sid + "/0"
	if rec := env.signed(
		t, a, http.MethodPut, path, []byte("A"),
	); rec.Code != http.StatusOK {
		t.Fatalf("A 创建=%d %s", rec.Code, rec.Body.String())
	}
	b, _ := env.login(t, "Device B")
	rec := env.signed(t, b, http.MethodPut, path, []byte("B"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("B 同 ID 创建=%d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "session_id_conflict" {
		t.Fatalf("错误码=%q", body.Error.Code)
	}
}

func TestSessionFileBodyLimitReturns413(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()
	a := env.registerA(t)
	oversize := bytes.Repeat([]byte("x"), (4<<20)+1)
	rec := env.signed(t, a, http.MethodPut,
		"/session-files/session-1-a1b2c3/0", oversize)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限=%d want=413 %s", rec.Code, rec.Body.String())
	}
}

func TestSessionFilesSurviveSyncGroupMerge(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()
	a := env.registerA(t)

	if rec := env.signed(
		t, a, http.MethodPut, "/session-files/session-1-a/0", []byte("A"),
	); rec.Code != http.StatusOK {
		t.Fatalf("A 上传=%d %s", rec.Code, rec.Body.String())
	}
	b, bLogin := env.login(t, "Device B")
	if !bLogin.Bootstrap.HasOtherSyncData {
		t.Fatal("仅有 session 数据时 B 应获知存在其他同步数据")
	}
	if rec := env.signed(
		t, b, http.MethodPut, "/session-files/session-2-b/0", []byte("B"),
	); rec.Code != http.StatusOK {
		t.Fatalf("B 上传=%d %s", rec.Code, rec.Body.String())
	}

	rec := env.signed(t, a, http.MethodPost, "/sync-codes", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("生成同步码=%d %s", rec.Code, rec.Body.String())
	}
	var codeResponse struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &codeResponse); err != nil {
		t.Fatal(err)
	}
	redeemBody, err := json.Marshal(map[string]string{"code": codeResponse.Code})
	if err != nil {
		t.Fatal(err)
	}
	rec = env.signed(t, b, http.MethodPost, "/sync-codes/redeem", redeemBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("兑换同步码=%d %s", rec.Code, rec.Body.String())
	}

	rec = env.signed(t, b, http.MethodGet, "/session-files", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("合并后列表=%d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Sessions []struct {
			SessionID string `json:"sessionId"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, item := range list.Sessions {
		ids = append(ids, item.SessionID)
	}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "session-1-a,session-2-b" {
		t.Fatalf("合并后 session IDs=%v", ids)
	}
	for path, want := range map[string]string{
		"/session-files/session-1-a": "A",
		"/session-files/session-2-b": "B",
	} {
		rec = env.signed(t, b, http.MethodGet, path, nil)
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Fatalf("GET %s status=%d body=%q", path, rec.Code, rec.Body.String())
		}
	}
}

func TestDiscardOtherGroupsReclaimsSessionQuota(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()
	a := env.registerA(t)
	if rec := env.signed(
		t, a, http.MethodPut, "/session-files/session-1-a/0", []byte("1234"),
	); rec.Code != http.StatusOK {
		t.Fatalf("旧组上传=%d %s", rec.Code, rec.Body.String())
	}
	b, _ := env.login(t, "Device B")
	if rec := env.signed(
		t, b, http.MethodPut, "/session-files/session-2-b/0", []byte("123456"),
	); rec.Code != http.StatusOK {
		t.Fatalf("当前组上传=%d %s", rec.Code, rec.Body.String())
	}

	deviceB, err := env.q.GetDevice(t.Context(), b.deviceID)
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := auth.BuildDiscardGroupsTranscript(
		env.instanceID, env.accountID, b.deviceID,
		deviceB.SyncGroupID.String, b.groupRevision)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"groupRevision": b.groupRevision,
		"accountProof":  signed(env.accountPrivate, transcript),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := env.signed(
		t, b, http.MethodPost, "/sync-groups/discard-others", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("discard=%d %s", rec.Code, rec.Body.String())
	}
	var response struct {
		DiscardedDevices int   `json:"discardedDevices"`
		ReclaimedBytes   int64 `json:"reclaimedBytes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.DiscardedDevices != 1 || response.ReclaimedBytes != 4 {
		t.Fatalf("discard 响应=%+v", response)
	}
	account, err := env.q.GetAccount(t.Context(), env.accountID)
	if err != nil || account.UsedBytes != 6 {
		t.Fatalf("discard 后账号=%+v err=%v", account, err)
	}
}

func signed(privateKey ed25519.PrivateKey, message []byte) string {
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, message))
}

func sortedJSONKeys(value map[string]json.RawMessage) string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func mustDecodeBase64URL(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
