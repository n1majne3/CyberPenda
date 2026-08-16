package tsecbenchclient_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"pentest/internal/tsecbenchclient"
)

func TestListUsesTheCredentialHeaderAndReturnsCurrentPlatformState(t *testing.T) {
	httpClient := handlerClient(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/openapi/v1/challenges" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("BENCHMARK_TOKEN") != "benchmark-secret" {
			t.Fatalf("BENCHMARK_TOKEN header = %q", request.Header.Get("BENCHMARK_TOKEN"))
		}
		_, _ = io.WriteString(response, `[{"unique_code":"one","flag_count":1,"correct_flag_count":0,"is_completed":false,"container_status":"stopped","container_addr":[]}]`)
	}))
	client := newClient(t, "http://benchmark.test", httpClient, time.Second)

	result, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(result.Challenges) != 1 || result.Challenges[0].UniqueCode != "one" {
		t.Fatalf("List result = %#v", result)
	}
}

func TestSubmitPerformsExactlyOneMutationAndNeverClosesAfterAnIncorrectFlag(t *testing.T) {
	var mu sync.Mutex
	requests := []string{}
	httpClient := handlerClient(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests = append(requests, request.Method+" "+request.URL.Path)
		mu.Unlock()
		if request.URL.Path != "/openapi/v1/challenges/submit" {
			t.Fatalf("unexpected request = %s %s", request.Method, request.URL.Path)
		}
		_, _ = io.WriteString(response, `{"correct":false,"awarded":0,"correct_flag_count":0,"total_flag_count":1}`)
	}))
	client := newClient(t, "http://benchmark.test", httpClient, time.Second)

	result, err := client.Submit(context.Background(), "one", "FLAG{wrong}")
	if err != nil {
		t.Fatalf("Submit error = %v", err)
	}
	if result.Correct {
		t.Fatalf("Submit result = %#v", result)
	}
	if len(requests) != 1 || requests[0] != "POST /openapi/v1/challenges/submit" {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestCloseRefusesAnIncompleteChallengeWithoutCallingTheMutationEndpoint(t *testing.T) {
	closeCalls := 0
	httpClient := handlerClient(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /openapi/v1/challenges":
			_, _ = io.WriteString(response, `[{"unique_code":"one","flag_count":2,"correct_flag_count":1,"is_completed":false,"container_status":"available","container_addr":["127.0.0.1:1234"]}]`)
		case "POST /openapi/v1/challenges/close":
			closeCalls++
			_, _ = io.WriteString(response, `{"closed":true}`)
		default:
			http.NotFound(response, request)
		}
	}))
	client := newClient(t, "http://benchmark.test", httpClient, time.Second)

	_, err := client.Close(context.Background(), tsecbenchclient.CloseRequest{UniqueCode: "one"})
	if !errors.Is(err, tsecbenchclient.ErrCloseNotAllowed) {
		t.Fatalf("Close error = %v", err)
	}
	if closeCalls != 0 {
		t.Fatalf("close calls = %d", closeCalls)
	}
}

func TestCloseAllowsCompletionOrExplicitReasonedAbandonment(t *testing.T) {
	tests := []struct {
		name      string
		completed bool
		request   tsecbenchclient.CloseRequest
	}{
		{name: "completed", completed: true, request: tsecbenchclient.CloseRequest{UniqueCode: "one"}},
		{name: "abandoned", completed: false, request: tsecbenchclient.CloseRequest{UniqueCode: "one", AbandonReason: "first-pass budget exhausted"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			closeCalls := 0
			httpClient := handlerClient(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				switch request.Method + " " + request.URL.Path {
				case "GET /openapi/v1/challenges":
					if test.completed {
						_, _ = io.WriteString(response, `[{"unique_code":"one","flag_count":1,"correct_flag_count":1,"is_completed":true,"container_status":"available"}]`)
					} else {
						_, _ = io.WriteString(response, `[{"unique_code":"one","flag_count":1,"correct_flag_count":0,"is_completed":false,"container_status":"available"}]`)
					}
				case "POST /openapi/v1/challenges/close":
					closeCalls++
					_, _ = io.WriteString(response, `{"closed":true}`)
				default:
					http.NotFound(response, request)
				}
			}))
			client := newClient(t, "http://benchmark.test", httpClient, time.Second)

			if _, err := client.Close(context.Background(), test.request); err != nil {
				t.Fatalf("Close error = %v", err)
			}
			if closeCalls != 1 {
				t.Fatalf("close calls = %d", closeCalls)
			}
		})
	}
}

func TestMutationFailureIsReturnedWithoutAutomaticRetry(t *testing.T) {
	calls := 0
	httpClient := handlerClient(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		response.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(response, `{"code":"temporary","message":"try later"}`)
	}))
	client := newClient(t, "http://benchmark.test", httpClient, time.Second)

	_, err := client.Start(context.Background(), "one")
	if err == nil {
		t.Fatal("Start error = nil")
	}
	if calls != 1 {
		t.Fatalf("mutation calls = %d, want 1", calls)
	}
}

func TestTimeoutAndMalformedSuccessRemainBoundedClientErrors(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		})}
		client := newClient(t, "http://benchmark.test", httpClient, 10*time.Millisecond)
		if _, err := client.List(context.Background()); err == nil {
			t.Fatal("List timeout error = nil")
		}
	})

	t.Run("malformed success", func(t *testing.T) {
		httpClient := handlerClient(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			_, _ = io.WriteString(response, `{not-json`)
		}))
		client := newClient(t, "http://benchmark.test", httpClient, time.Second)
		if _, err := client.List(context.Background()); err == nil {
			t.Fatal("malformed List error = nil")
		}
	})
}

func TestErrorsRedactTheExactBenchmarkToken(t *testing.T) {
	httpClient := handlerClient(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(response, `{"message":"token benchmark-secret was rejected"}`)
	}))
	client := newClient(t, "http://benchmark.test", httpClient, time.Second)

	_, err := client.List(context.Background())
	if err == nil || strings.Contains(err.Error(), "benchmark-secret") {
		t.Fatalf("redacted error = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func handlerClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		recorder := newResponseRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.response(request), nil
	})}
}

type responseRecorder struct {
	header http.Header
	body   strings.Builder
	status int
}

func newResponseRecorder() *responseRecorder {
	return &responseRecorder{header: make(http.Header), status: http.StatusOK}
}

func (recorder *responseRecorder) Header() http.Header             { return recorder.header }
func (recorder *responseRecorder) WriteHeader(status int)          { recorder.status = status }
func (recorder *responseRecorder) Write(value []byte) (int, error) { return recorder.body.Write(value) }
func (recorder *responseRecorder) response(request *http.Request) *http.Response {
	return &http.Response{
		StatusCode: recorder.status,
		Header:     recorder.header,
		Body:       io.NopCloser(strings.NewReader(recorder.body.String())),
		Request:    request,
	}
}

func newClient(t *testing.T, baseURL string, httpClient *http.Client, timeout time.Duration) *tsecbenchclient.Client {
	t.Helper()
	client, err := tsecbenchclient.New(tsecbenchclient.Config{
		BaseURL: baseURL,
		Token:   "benchmark-secret",
		Client:  httpClient,
		Timeout: timeout,
	})
	if err != nil {
		t.Fatalf("New Client error = %v", err)
	}
	return client
}
