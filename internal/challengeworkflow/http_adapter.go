package challengeworkflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// HTTPAdapterConfig maps the four Challenge Workflow operations to one
// Challenge Platform. Auth is supplied by the configured HTTP transport. It is
// never stored in a Task Policy Snapshot or an Evidence response.
type HTTPAdapterConfig struct {
	BaseURL      string
	ClaimPath    string
	SubmitPath   string
	AbandonPath  string
	FinalizePath string
	Client       *http.Client
}

type HTTPAdapter struct{ config HTTPAdapterConfig }

func NewHTTPAdapter(config HTTPAdapterConfig) (*HTTPAdapter, error) {
	parsed, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("valid Challenge Platform base URL is required")
	}
	for _, value := range []*string{&config.ClaimPath, &config.SubmitPath, &config.AbandonPath, &config.FinalizePath} {
		if strings.TrimSpace(*value) == "" {
			return nil, fmt.Errorf("all Challenge Platform operation paths are required")
		}
	}
	if config.Client == nil {
		config.Client = http.DefaultClient
	}
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")
	return &HTTPAdapter{config: config}, nil
}

func (adapter *HTTPAdapter) Claim(ctx context.Context, request PlatformClaimRequest) (PlatformClaimResponse, error) {
	var response PlatformClaimResponse
	return response, adapter.call(ctx, adapter.config.ClaimPath, request, &response)
}
func (adapter *HTTPAdapter) Submit(ctx context.Context, request PlatformSubmitRequest) (PlatformSubmitResponse, error) {
	var response PlatformSubmitResponse
	return response, adapter.call(ctx, adapter.config.SubmitPath, request, &response)
}
func (adapter *HTTPAdapter) Abandon(ctx context.Context, request PlatformAbandonRequest) (PlatformAbandonResponse, error) {
	var response PlatformAbandonResponse
	return response, adapter.call(ctx, adapter.config.AbandonPath, request, &response)
}
func (adapter *HTTPAdapter) Finalize(ctx context.Context, request PlatformFinalizeRequest) (PlatformFinalizeResponse, error) {
	var response PlatformFinalizeResponse
	return response, adapter.call(ctx, adapter.config.FinalizePath, request, &response)
}

func (adapter *HTTPAdapter) call(ctx context.Context, path string, input, output any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, adapter.config.BaseURL+"/"+strings.TrimLeft(path, "/"), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := adapter.config.Client.Do(request)
	if err != nil {
		return fmt.Errorf("call Challenge Platform: %w", err)
	}
	defer response.Body.Close()
	limited, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Challenge Platform returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(bytes.NewReader(limited))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode Challenge Platform response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode Challenge Platform response: trailing JSON is not allowed")
	}
	return nil
}
