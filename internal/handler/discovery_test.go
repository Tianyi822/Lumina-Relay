package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"lumina-relay/internal/service"
)

func TestDiscoveryIsUnversioned(t *testing.T) {
	const instanceID = "test-instance"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/lumina-relay", nil)
	NewRouter(Deps{InstanceID: instanceID}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["protocol"] != "lumina-relay" || raw["instanceId"] != instanceID {
		t.Fatalf("discovery=%v", raw)
	}
	for _, forbidden := range []string{"apiVersion", "apiBase", "cryptoSuite"} {
		if _, exists := raw[forbidden]; exists {
			t.Errorf("无版本协议不应返回 %q", forbidden)
		}
	}
}

func TestDiscoveryAdvertisesSessionFiles(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/lumina-relay", nil)
	NewRouter(Deps{InstanceID: "test-instance"}).ServeHTTP(rec, req)

	var body struct {
		Capabilities []string `json:"capabilities"`
		Limits       struct {
			MaxSessionFileBytes int `json:"maxSessionFileBytes"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(body.Capabilities, "session-files") {
		t.Fatalf("capabilities=%v", body.Capabilities)
	}
	if body.Limits.MaxSessionFileBytes != service.MaxSessionFileBytes {
		t.Fatalf("maxSessionFileBytes=%d", body.Limits.MaxSessionFileBytes)
	}
}
