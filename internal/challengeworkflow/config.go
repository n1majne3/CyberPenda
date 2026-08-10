package challengeworkflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type platformConfigFile struct {
	Platforms []httpPlatformManifest `json:"platforms"`
}
type httpPlatformManifest struct {
	Name           string `json:"name"`
	BaseURL        string `json:"base_url"`
	ClaimPath      string `json:"claim_path"`
	SubmitPath     string `json:"submit_path"`
	AbandonPath    string `json:"abandon_path"`
	FinalizePath   string `json:"finalize_path"`
	BearerTokenEnv string `json:"bearer_token_env,omitempty"`
}

// LoadHTTPAdapters loads strict management-time Platform Adapter metadata.
// Secret values stay outside the file and are read only from the named host
// environment variable.
func LoadHTTPAdapters(path string) (map[string]PlatformAdapter, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Challenge Platform config: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var config platformConfigFile
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode Challenge Platform config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode Challenge Platform config: trailing JSON is not allowed")
	}
	if len(config.Platforms) == 0 {
		return nil, fmt.Errorf("at least one Challenge Platform is required")
	}
	result := make(map[string]PlatformAdapter, len(config.Platforms))
	for _, manifest := range config.Platforms {
		name := strings.TrimSpace(manifest.Name)
		if name == "" {
			return nil, fmt.Errorf("Challenge Platform name is required")
		}
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("duplicate Challenge Platform %q", name)
		}
		client := &http.Client{Timeout: 30 * time.Second}
		if env := strings.TrimSpace(manifest.BearerTokenEnv); env != "" {
			token := strings.TrimSpace(os.Getenv(env))
			if token == "" {
				return nil, fmt.Errorf("Challenge Platform %q requires environment variable %s", name, env)
			}
			client.Transport = bearerTransport{token: token, base: http.DefaultTransport}
		}
		adapter, err := NewHTTPAdapter(HTTPAdapterConfig{BaseURL: manifest.BaseURL, ClaimPath: manifest.ClaimPath, SubmitPath: manifest.SubmitPath, AbandonPath: manifest.AbandonPath, FinalizePath: manifest.FinalizePath, Client: client})
		if err != nil {
			return nil, fmt.Errorf("configure Challenge Platform %q: %w", name, err)
		}
		result[name] = adapter
	}
	return result, nil
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (transport bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(clone)
}
