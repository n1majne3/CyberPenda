package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveLaunchRuntimeProfileCreatesAndReusesProfile(t *testing.T) {
	server := newDaemon(t)
	create := httptest.NewRequest(http.MethodPost, "/api/model-providers", bytes.NewReader([]byte(`{
		"name":"MiMo",
		"base_url":"https://api.example.test/v1",
		"protocols":["openai_responses"],
		"catalog":{"manual":["mimo"],"default_model":"mimo"}
	}`)))
	create.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, create)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create provider status %d body %s", resp.Code, resp.Body.String())
	}
	var provider struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&provider); err != nil {
		t.Fatalf("decode provider: %v", err)
	}
	providerID := provider.ID

	body := []byte(`{"provider":"codex","model_provider_id":"` + providerID + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles/resolve-launch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resolveResp := httptest.NewRecorder()
	server.ServeHTTP(resolveResp, req)
	if resolveResp.Code != http.StatusOK {
		t.Fatalf("expected resolve status 200, got %d with body %s", resolveResp.Code, resolveResp.Body.String())
	}
	var first struct {
		ProfileID string `json:"profile_id"`
		Created   bool   `json:"created"`
	}
	if err := json.NewDecoder(resolveResp.Body).Decode(&first); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if first.ProfileID == "" || !first.Created {
		t.Fatalf("expected created profile, got %#v", first)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles/resolve-launch", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	resp2 := httptest.NewRecorder()
	server.ServeHTTP(resp2, req2)
	if resp2.Code != http.StatusOK {
		t.Fatalf("expected second resolve status 200, got %d with body %s", resp2.Code, resp2.Body.String())
	}
	var second struct {
		ProfileID string `json:"profile_id"`
		Created   bool   `json:"created"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&second); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if second.Created || second.ProfileID != first.ProfileID {
		t.Fatalf("expected reuse, got %#v vs %#v", second, first)
	}
}