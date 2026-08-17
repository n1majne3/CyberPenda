package challengeadapter_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"pentest/internal/challengeadapter"
)

func TestHTTPDriverListUsesManifestPathAndHeader(t *testing.T) {
	var gotPath, gotHeader string
	client := challengeadapter.NewHTTPDriver(challengeadapter.HTTPDriverConfig{
		Manifest: mustTSec(t),
		BaseURL:  "http://bench.test",
		Token:    "secret",
		Client: &http.Client{Transport: roundTrip(func(request *http.Request) *http.Response {
			gotPath = request.URL.Path
			gotHeader = request.Header.Get("BENCHMARK_TOKEN")
			return jsonResponse(`[{"unique_code":"one","difficulty":"easy","is_completed":false,"container_status":"stopped"}]`)
		})},
		Timeout: time.Second,
	})
	result, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/openapi/v1/challenges" || gotHeader != "secret" {
		t.Fatalf("path=%q header=%q", gotPath, gotHeader)
	}
	if len(result.Challenges) != 1 || result.Challenges[0].UniqueCode != "one" {
		t.Fatalf("result=%#v", result)
	}
}

func TestHTTPDriverStartSubstitutesCodeQuery(t *testing.T) {
	var got string
	client := challengeadapter.NewHTTPDriver(challengeadapter.HTTPDriverConfig{
		Manifest: mustTSec(t),
		BaseURL:  "http://bench.test",
		Token:    "secret",
		Client: &http.Client{Transport: roundTrip(func(request *http.Request) *http.Response {
			got = request.URL.RawQuery
			return jsonResponse(`{"unique_code":"web-1","container_addr":["10.0.0.1:80"]}`)
		})},
		Timeout: time.Second,
	})
	if _, err := client.Start(context.Background(), "web-1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "unique_code=web-1") {
		t.Fatalf("query=%q", got)
	}
}

func mustTSec(t *testing.T) challengeadapter.Manifest {
	t.Helper()
	manifest, err := challengeadapter.Load("tsecbench", nil)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

type roundTripFunc func(*http.Request) *http.Response

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request), nil
}

func roundTrip(fn func(*http.Request) *http.Response) http.RoundTripper {
	return roundTripFunc(fn)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
