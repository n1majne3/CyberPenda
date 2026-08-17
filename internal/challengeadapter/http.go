package challengeadapter

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

	"pentest/internal/tsecbenchclient"
)

const maxResponseBytes = 1 << 20

type HTTPDriverConfig struct {
	Manifest  Manifest
	BaseURL   string
	Token     string
	Client    *http.Client
	Timeout   time.Duration
	ClockPath string
	Now       func() time.Time
}

// HTTPDriver executes one Challenge Platform using a declarative Manifest.
type HTTPDriver struct {
	manifest Manifest
	baseURL  string
	token    string
	client   *http.Client
	timeout  time.Duration
	clock    tsecbenchclient.ClockStore
}

func NewHTTPDriver(config HTTPDriverConfig) *HTTPDriver {
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	httpClient := config.Client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	return &HTTPDriver{
		manifest: config.Manifest,
		baseURL:  strings.TrimRight(strings.TrimSpace(config.BaseURL), "/"),
		token:    config.Token,
		client:   httpClient,
		timeout:  timeout,
		clock:    tsecbenchclient.ClockStore{Path: config.ClockPath, Now: config.Now},
	}
}

func (driver *HTTPDriver) List(ctx context.Context) (tsecbenchclient.ListResult, error) {
	var result tsecbenchclient.ListResult
	raw, err := driver.call(ctx, "list", "", "")
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(raw, &result.Challenges); err != nil || result.Challenges == nil {
		var wrapped struct {
			Challenges []tsecbenchclient.Challenge `json:"challenges"`
		}
		if wrapErr := json.Unmarshal(raw, &wrapped); wrapErr != nil || wrapped.Challenges == nil {
			return tsecbenchclient.ListResult{}, errors.New("decode challenge list")
		}
		result.Challenges = wrapped.Challenges
	}
	result.Challenges = driver.clock.Annotate(result.Challenges)
	return result, nil
}

func (driver *HTTPDriver) Start(ctx context.Context, code string) (json.RawMessage, error) {
	raw, err := driver.call(ctx, "start", code, "")
	if err != nil {
		return raw, err
	}
	_ = driver.clock.RecordStart(code, "", 0)
	return raw, nil
}

func (driver *HTTPDriver) Hint(ctx context.Context, code string) (json.RawMessage, error) {
	return driver.call(ctx, "hint", code, "")
}

func (driver *HTTPDriver) Submit(ctx context.Context, code, candidate string) (tsecbenchclient.SubmitResult, error) {
	var result tsecbenchclient.SubmitResult
	raw, err := driver.call(ctx, "submit", code, candidate)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return tsecbenchclient.SubmitResult{}, errors.New("decode submit result")
	}
	return result, nil
}

func (driver *HTTPDriver) Close(ctx context.Context, request tsecbenchclient.CloseRequest) (json.RawMessage, error) {
	code := strings.TrimSpace(request.UniqueCode)
	reason := strings.TrimSpace(request.AbandonReason)
	if driver.manifest.CloseRequiresComplete && reason == "" {
		state, err := driver.List(ctx)
		if err != nil {
			return nil, err
		}
		var found *tsecbenchclient.Challenge
		for index := range state.Challenges {
			if state.Challenges[index].UniqueCode == code {
				found = &state.Challenges[index]
				break
			}
		}
		if found == nil {
			return nil, tsecbenchclient.ErrChallengeAbsent
		}
		completed := found.IsCompleted || (found.FlagCount > 0 && found.CorrectFlagCount >= found.FlagCount)
		if !completed {
			return nil, tsecbenchclient.ErrCloseNotAllowed
		}
	}
	raw, err := driver.call(ctx, "close", code, "")
	if err != nil {
		return raw, err
	}
	_ = driver.clock.Clear(code)
	return raw, nil
}

func (driver *HTTPDriver) call(ctx context.Context, op, code, candidate string) (json.RawMessage, error) {
	operation, ok := driver.manifest.Operations[op]
	if !ok {
		return nil, fmt.Errorf("adapter %s has no %s operation", driver.manifest.ID, op)
	}
	replacer := strings.NewReplacer("{{code}}", code, "{{candidate}}", candidate)
	endpoint, err := url.Parse(driver.baseURL + operation.Path)
	if err != nil {
		return nil, errors.New("adapter operation path is invalid")
	}
	query := endpoint.Query()
	for key, value := range operation.Query {
		query.Set(key, replacer.Replace(value))
	}
	endpoint.RawQuery = query.Encode()
	var body io.Reader
	if len(operation.JSON) > 0 {
		payload := map[string]string{}
		for key, value := range operation.JSON {
			payload[key] = strings.NewReplacer("{{code}}", code, "{{candidate}}", candidate).Replace(value)
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, errors.New("encode adapter request")
		}
		body = bytes.NewReader(raw)
	}
	requestContext, cancel := context.WithTimeout(ctx, driver.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, operation.Method, endpoint.String(), body)
	if err != nil {
		return nil, errors.New("prepare adapter request")
	}
	request.Header.Set(driver.manifest.TokenHeader, driver.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := driver.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("challenge platform request failed: %s", strings.ReplaceAll(err.Error(), driver.token, "[REDACTED]"))
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, errors.New("read challenge platform response")
	}
	if len(raw) > maxResponseBytes {
		return nil, errors.New("challenge platform response exceeded size limit")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("challenge platform returned HTTP %d: %s", response.StatusCode, strings.ReplaceAll(string(raw), driver.token, "[REDACTED]"))
	}
	if !json.Valid(raw) {
		return nil, errors.New("challenge platform returned malformed JSON")
	}
	return json.RawMessage(raw), nil
}
