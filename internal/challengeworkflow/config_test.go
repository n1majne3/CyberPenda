package challengeworkflow

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestLoadHTTPAdaptersBuildsAuthenticatedProductionAdapter(t *testing.T) {
	t.Setenv("ARENA_TOKEN", "secret")
	path := filepath.Join(t.TempDir(), "platforms.json")
	raw := `{"platforms":[{"name":"arena","base_url":"https://arena.example","claim_path":"/claim","submit_path":"/submit","abandon_path":"/abandon","finalize_path":"/finalize","bearer_token_env":"ARENA_TOKEN"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	adapters, err := LoadHTTPAdapters(path)
	if err != nil {
		t.Fatal(err)
	}
	httpAdapter := adapters["arena"].(*HTTPAdapter)
	transport := httpAdapter.config.Client.Transport.(bearerTransport)
	transport.base = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/claim" || request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("request = %s auth %q", request.URL.Path, request.Header.Get("Authorization"))
		}
		responseRaw, _ := json.Marshal(PlatformClaimResponse{ExternalAttemptID: "42", ChallengeID: "3121", Summary: "claimed"})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(responseRaw))), Header: make(http.Header)}, nil
	})
	httpAdapter.config.Client.Transport = transport
	result, err := adapters["arena"].Claim(context.Background(), PlatformClaimRequest{OperationID: "op-1", ChallengeID: "3121"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExternalAttemptID != "42" {
		t.Fatalf("result = %#v", result)
	}
}
