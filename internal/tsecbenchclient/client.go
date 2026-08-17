// Package tsecbenchclient provides process-isolated, one-operation access to
// the TSecBench hosted challenge API. It does not own Runtime or host lifecycle.
package tsecbenchclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 1 << 20

var (
	ErrInvalidConfig   = errors.New("TSecBench client configuration is invalid")
	ErrInvalidRequest  = errors.New("TSecBench client request is invalid")
	ErrChallengeAbsent = errors.New("Benchmark Challenge is absent from current platform state")
	ErrCloseNotAllowed = errors.New("Benchmark Challenge close is not allowed without completion or explicit abandonment")
)

type Config struct {
	BaseURL     string
	Token       string
	Client      *http.Client
	Timeout     time.Duration
	ClockPath   string
	Now         func() time.Time
	TokenHeader string
}

type Client struct {
	apiBase     string
	token       string
	client      *http.Client
	timeout     time.Duration
	clock       ClockStore
	tokenHeader string
}

type Challenge struct {
	UniqueCode       string   `json:"unique_code"`
	Description      string   `json:"description,omitempty"`
	Difficulty       any      `json:"difficulty,omitempty"`
	Level            any      `json:"level,omitempty"`
	TotalScore       int      `json:"total_score,omitempty"`
	FlagCount        int      `json:"flag_count,omitempty"`
	CorrectFlagCount int      `json:"correct_flag_count,omitempty"`
	IsCompleted      bool     `json:"is_completed"`
	ContainerStatus  string   `json:"container_status,omitempty"`
	ContainerAddr    []string `json:"container_addr,omitempty"`
	ElapsedMin       *int     `json:"elapsed_min,omitempty"`
	BudgetMin        *int     `json:"budget_min,omitempty"`
	OverBudget       *bool    `json:"over_budget,omitempty"`
	AttemptN         *int     `json:"attempt_n,omitempty"`
}

type ListResult struct {
	Challenges []Challenge `json:"challenges"`
}

type SubmitResult struct {
	Correct          bool `json:"correct"`
	Awarded          any  `json:"awarded,omitempty"`
	CumulativeScore  any  `json:"cumulative_score,omitempty"`
	CorrectFlagCount int  `json:"correct_flag_count,omitempty"`
	TotalFlagCount   int  `json:"total_flag_count,omitempty"`
	MatchedFlagIndex any  `json:"matched_flag_index,omitempty"`
}

type CloseRequest struct {
	UniqueCode    string
	AbandonReason string
}

func New(config Config) (*Client, error) {
	base := strings.TrimSpace(config.BaseURL)
	token := strings.TrimSpace(config.Token)
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || token == "" {
		return nil, ErrInvalidConfig
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	httpClient := config.Client
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	clientCopy := *httpClient
	if clientCopy.Timeout <= 0 || clientCopy.Timeout > timeout {
		clientCopy.Timeout = timeout
	}
	return &Client{
		apiBase:     strings.TrimRight(base, "/") + "/openapi/v1/challenges",
		token:       token,
		client:      &clientCopy,
		timeout:     timeout,
		clock:       ClockStore{Path: strings.TrimSpace(config.ClockPath), Now: config.Now},
		tokenHeader: strings.TrimSpace(config.TokenHeader),
	}, nil
}

func (client *Client) List(ctx context.Context) (ListResult, error) {
	var result ListResult
	raw, err := client.call(ctx, http.MethodGet, client.apiBase, nil)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(raw, &result.Challenges); err != nil || result.Challenges == nil {
		return ListResult{}, errors.New("decode TSecBench challenge list")
	}
	for _, challenge := range result.Challenges {
		if strings.TrimSpace(challenge.UniqueCode) == "" {
			return ListResult{}, errors.New("decode TSecBench challenge list: unique_code is required")
		}
	}
	result.Challenges = client.clock.Annotate(result.Challenges)
	return result, nil
}

func (client *Client) Start(ctx context.Context, uniqueCode string) (json.RawMessage, error) {
	raw, err := client.mutateWithCode(ctx, "start", uniqueCode, nil)
	if err != nil {
		return raw, err
	}
	_ = client.clock.RecordStart(uniqueCode, "", 0)
	return raw, nil
}

func (client *Client) Hint(ctx context.Context, uniqueCode string) (json.RawMessage, error) {
	code := strings.TrimSpace(uniqueCode)
	if code == "" {
		return nil, ErrInvalidRequest
	}
	endpoint, err := queryEndpoint(client.apiBase+"/hint", code)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	return client.call(ctx, http.MethodGet, endpoint, nil)
}

func (client *Client) Submit(ctx context.Context, uniqueCode, candidate string) (SubmitResult, error) {
	var result SubmitResult
	code := strings.TrimSpace(uniqueCode)
	if code == "" || candidate == "" {
		return result, ErrInvalidRequest
	}
	body := struct {
		UniqueCode string `json:"unique_code"`
		Flag       string `json:"flag"`
	}{UniqueCode: code, Flag: candidate}
	raw, err := client.call(ctx, http.MethodPost, client.apiBase+"/submit", body)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return SubmitResult{}, errors.New("decode TSecBench submit response")
	}
	return result, nil
}

func (client *Client) Close(ctx context.Context, request CloseRequest) (json.RawMessage, error) {
	code := strings.TrimSpace(request.UniqueCode)
	reason := strings.TrimSpace(request.AbandonReason)
	if code == "" {
		return nil, ErrInvalidRequest
	}
	state, err := client.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("verify Benchmark Challenge before close: %w", err)
	}
	var found *Challenge
	for index := range state.Challenges {
		if state.Challenges[index].UniqueCode == code {
			found = &state.Challenges[index]
			break
		}
	}
	if found == nil {
		return nil, ErrChallengeAbsent
	}
	completed := found.IsCompleted || (found.FlagCount > 0 && found.CorrectFlagCount >= found.FlagCount)
	if !completed && reason == "" {
		return nil, ErrCloseNotAllowed
	}
	raw, err := client.mutateWithCode(ctx, "close", code, nil)
	if err != nil {
		return raw, err
	}
	_ = client.clock.Clear(code)
	return raw, nil
}

func (client *Client) mutateWithCode(ctx context.Context, operation, uniqueCode string, body any) (json.RawMessage, error) {
	code := strings.TrimSpace(uniqueCode)
	if code == "" {
		return nil, ErrInvalidRequest
	}
	endpoint, err := queryEndpoint(client.apiBase+"/"+operation, code)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	return client.call(ctx, http.MethodPost, endpoint, body)
}

func queryEndpoint(endpoint, code string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("unique_code", code)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (client *Client) call(ctx context.Context, method, endpoint string, body any) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, ErrInvalidRequest
		}
		reader = bytes.NewReader(raw)
	}
	requestContext, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, method, endpoint, reader)
	if err != nil {
		return nil, errors.New("prepare TSecBench request")
	}
	header := client.tokenHeader
	if header == "" {
		header = "BENCHMARK_TOKEN"
	}
	request.Header.Set(header, client.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("TSecBench request failed: %s", redact(err.Error(), client.token))
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, errors.New("read TSecBench response")
	}
	if len(raw) > maxResponseBytes {
		return nil, errors.New("TSecBench response exceeded size limit")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(redact(string(raw), client.token))
		return nil, fmt.Errorf("TSecBench returned HTTP %d: %s", response.StatusCode, message)
	}
	if !json.Valid(raw) {
		return nil, errors.New("TSecBench returned malformed JSON")
	}
	return json.RawMessage(raw), nil
}

func redact(value, secret string) string {
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "[REDACTED]")
}
