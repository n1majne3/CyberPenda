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
	array, err := driver.challengeArray(raw)
	if err != nil {
		return tsecbenchclient.ListResult{}, err
	}
	elements := []json.RawMessage{}
	if err := json.Unmarshal(array, &elements); err != nil {
		return tsecbenchclient.ListResult{}, errors.New("decode challenge list")
	}
	result.Challenges = make([]tsecbenchclient.Challenge, 0, len(elements))
	for _, element := range elements {
		var challenge tsecbenchclient.Challenge
		if err := json.Unmarshal(element, &challenge); err != nil {
			return tsecbenchclient.ListResult{}, errors.New("decode challenge list")
		}
		if err := driver.applyChallengeFields(&challenge, element); err != nil {
			return tsecbenchclient.ListResult{}, err
		}
		result.Challenges = append(result.Challenges, challenge)
	}
	result.Challenges = driver.clock.Annotate(result.Challenges)
	return result, nil
}

// challengeArray accepts a bare array, a {"challenges": [...]} wrapper,
// and an envelope object whose non-zero code means a platform rejection.
func (driver *HTTPDriver) challengeArray(raw json.RawMessage) (json.RawMessage, error) {
	var envelope struct {
		Code    *int            `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Code != nil {
		if *envelope.Code != 0 {
			return nil, fmt.Errorf("challenge platform rejected request: %s", strings.ReplaceAll(envelope.Message, driver.token, "[REDACTED]"))
		}
		if isArrayJSON(envelope.Data) {
			return envelope.Data, nil
		}
		return nil, errors.New("decode challenge list")
	}
	var direct []json.RawMessage
	if err := json.Unmarshal(raw, &direct); err == nil && direct != nil {
		return raw, nil
	}
	var wrapped struct {
		Challenges json.RawMessage `json:"challenges"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && isArrayJSON(wrapped.Challenges) {
		return wrapped.Challenges, nil
	}
	return nil, errors.New("decode challenge list")
}

func isArrayJSON(raw json.RawMessage) bool {
	return bytes.HasPrefix(bytes.TrimSpace(raw), []byte("["))
}

func (driver *HTTPDriver) applyChallengeFields(challenge *tsecbenchclient.Challenge, element json.RawMessage) error {
	if len(driver.manifest.ChallengeFields) == 0 {
		return nil
	}
	var source map[string]any
	if err := json.Unmarshal(element, &source); err != nil {
		return errors.New("decode challenge fields source")
	}
	for target, path := range driver.manifest.ChallengeFields {
		value, ok := resolveJSONPath(source, path)
		if !ok {
			continue
		}
		if err := assignChallengeField(challenge, target, value); err != nil {
			return err
		}
	}
	return nil
}

func resolveJSONPath(source map[string]any, path string) (any, bool) {
	var current any = source
	for _, segment := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		if current, ok = object[segment]; !ok {
			return nil, false
		}
	}
	return current, true
}

func assignChallengeField(challenge *tsecbenchclient.Challenge, target string, value any) error {
	switch target {
	case "unique_code", "description":
		text, ok := stringValue(value)
		if !ok {
			return fmt.Errorf("challenge_fields value for %s is not a string", target)
		}
		if target == "unique_code" {
			challenge.UniqueCode = text
		} else {
			challenge.Description = text
		}
	case "is_completed":
		flag, ok := boolValue(value)
		if !ok {
			return errors.New("challenge_fields value for is_completed is not a boolean")
		}
		challenge.IsCompleted = flag
	case "total_score":
		number, ok := intValue(value)
		if !ok {
			return errors.New("challenge_fields value for total_score is not a number")
		}
		challenge.TotalScore = number
	case "container_addr":
		list, ok := stringSliceValue(value)
		if !ok {
			return errors.New("challenge_fields value for container_addr is not an address")
		}
		challenge.ContainerAddr = list
	default:
		return fmt.Errorf("challenge_fields target %s is not supported", target)
	}
	return nil
}

func stringValue(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok
}

func boolValue(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		if typed == "true" {
			return true, true
		}
		if typed == "false" {
			return false, true
		}
	}
	return false, false
}

func intValue(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok {
		return 0, false
	}
	return int(number), true
}

func stringSliceValue(value any) ([]string, bool) {
	switch typed := value.(type) {
	case string:
		return []string{typed}, true
	case []any:
		list := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			list = append(list, text)
		}
		return list, true
	}
	return nil, false
}

func truthyValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		return typed == "true" || typed == "1"
	}
	return false
}

func (driver *HTTPDriver) Start(ctx context.Context, code string) (json.RawMessage, error) {
	raw, err := driver.call(ctx, "start", code, "")
	if err != nil {
		return raw, err
	}
	if err := driver.checkEnvelope(raw); err != nil {
		return nil, err
	}
	_ = driver.clock.RecordStart(code, "", 0)
	return raw, nil
}

func (driver *HTTPDriver) checkEnvelope(raw json.RawMessage) error {
	var envelope struct {
		Code    *int   `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Code == nil {
		return nil
	}
	if *envelope.Code != 0 {
		return fmt.Errorf("challenge platform rejected request: %s", strings.ReplaceAll(envelope.Message, driver.token, "[REDACTED]"))
	}
	return nil
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
	if driver.manifest.SubmitCorrect == "" {
		if err := json.Unmarshal(raw, &result); err != nil {
			return tsecbenchclient.SubmitResult{}, errors.New("decode submit result")
		}
		return result, nil
	}
	var envelope struct {
		Code    *int   `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return tsecbenchclient.SubmitResult{}, errors.New("decode submit result")
	}
	result.Message = envelope.Message
	if envelope.Code != nil && *envelope.Code != 0 {
		return result, nil
	}
	var source map[string]any
	if err := json.Unmarshal(raw, &source); err != nil {
		return tsecbenchclient.SubmitResult{}, errors.New("decode submit result")
	}
	value, ok := resolveJSONPath(source, driver.manifest.SubmitCorrect)
	if !ok {
		return tsecbenchclient.SubmitResult{}, fmt.Errorf("submit_correct path %s is absent from the submit response", driver.manifest.SubmitCorrect)
	}
	result.Correct = truthyValue(value)
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
	if driver.manifest.TokenQuery != "" {
		query.Set(driver.manifest.TokenQuery, driver.token)
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
	if driver.manifest.TokenQuery == "" {
		request.Header.Set(driver.manifest.TokenHeader, driver.token)
	}
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
