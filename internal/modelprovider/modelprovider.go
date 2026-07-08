// Package modelprovider owns reusable non-secret model-service configuration.
package modelprovider

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"pentest/internal/store"
)

type Protocol string

const (
	ProtocolOpenAIChatCompletions Protocol = "openai_chat_completions"
	ProtocolOpenAIResponses       Protocol = "openai_responses"
	ProtocolAnthropicMessages     Protocol = "anthropic_messages"
)

var validProtocols = map[Protocol]bool{
	ProtocolOpenAIChatCompletions: true,
	ProtocolOpenAIResponses:       true,
	ProtocolAnthropicMessages:     true,
}

type Catalog struct {
	Manual       []string `json:"manual,omitempty"`
	Refreshed    []string `json:"refreshed,omitempty"`
	DefaultModel string   `json:"default_model,omitempty"`
}

type Endpoint struct {
	Protocol Protocol `json:"protocol"`
	BaseURL  string   `json:"base_url"`
}

type Provider struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	BaseURL   string     `json:"base_url"`
	Protocols []Protocol `json:"protocols"`
	Endpoints []Endpoint `json:"endpoints"`
	APIKeyEnv string     `json:"api_key_env"`
	Catalog   Catalog    `json:"catalog"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type CreateRequest struct {
	Name      string
	BaseURL   string
	Protocols []Protocol
	Endpoints []Endpoint
	Catalog   Catalog
}

type UpdateRequest struct {
	Name      *string
	BaseURL   *string
	Protocols *[]Protocol
	Endpoints *[]Endpoint
	Catalog   *Catalog
}

var (
	ErrNotFound                  = errors.New("model provider not found")
	ErrMissingName               = errors.New("model provider name is required")
	ErrMissingBaseURL            = errors.New("model provider base URL is required")
	ErrInvalidProtocol           = errors.New("model provider protocol is not supported")
	ErrDuplicateEndpointProtocol = errors.New("model provider endpoint protocol is duplicated")
	ErrInvalidEndpointBaseURL    = errors.New("model provider endpoint base URL is invalid")
	ErrInUse                     = errors.New("model provider is referenced by a runtime profile")
)

type Service struct {
	db *store.DB
}

func NewService(db *store.DB) *Service {
	return &Service{db: db}
}

func (s *Service) Create(req CreateRequest) (Provider, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Provider{}, ErrMissingName
	}
	baseURL, endpoints, err := normalizeRequestedEndpoints(req.BaseURL, req.Protocols, req.Endpoints)
	if err != nil {
		return Provider{}, err
	}
	now := time.Now().UTC()
	created := Provider{
		ID:        s.nextID(name),
		Name:      name,
		BaseURL:   baseURL,
		Protocols: protocolsForEndpoints(endpoints),
		Endpoints: endpoints,
		Catalog:   normalizeCatalog(req.Catalog),
		CreatedAt: now,
		UpdatedAt: now,
	}
	created.APIKeyEnv = APIKeyEnv(created.ID)
	if err := s.insert(created); err != nil {
		return Provider{}, err
	}
	return created, nil
}

func (s *Service) Get(id string) (Provider, error) {
	return scanProvider(s.db.QueryRow(
		`SELECT id, name, base_url, protocols_json, endpoints_json, catalog_json, created_at, updated_at FROM model_providers WHERE id = ?`,
		strings.TrimSpace(id),
	))
}

func (s *Service) List() ([]Provider, error) {
	rows, err := s.db.Query(`SELECT id, name, base_url, protocols_json, endpoints_json, catalog_json, created_at, updated_at FROM model_providers ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list model providers: %w", err)
	}
	defer rows.Close()
	var out []Provider
	for rows.Next() {
		provider, err := scanProvider(rows)
		if err != nil {
			return nil, fmt.Errorf("scan model provider: %w", err)
		}
		out = append(out, provider)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list model providers: %w", err)
	}
	return out, nil
}

func (s *Service) Update(id string, req UpdateRequest) (Provider, error) {
	existing, err := s.Get(id)
	if err != nil {
		return Provider{}, err
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return Provider{}, ErrMissingName
		}
		existing.Name = name
	}
	if req.BaseURL != nil {
		baseURL, err := NormalizeBaseURL(*req.BaseURL)
		if err != nil {
			return Provider{}, err
		}
		existing.BaseURL = baseURL
	}
	if req.Protocols != nil {
		protocols, err := NormalizeProtocols(*req.Protocols)
		if err != nil {
			return Provider{}, err
		}
		existing.Protocols = protocols
	}
	if req.Endpoints != nil {
		endpoints, err := NormalizeEndpoints(*req.Endpoints)
		if err != nil {
			return Provider{}, err
		}
		existing.Endpoints = endpoints
	} else if req.BaseURL != nil || req.Protocols != nil {
		endpoints, err := BackfillEndpoints(existing.BaseURL, existing.Protocols)
		if err != nil {
			return Provider{}, err
		}
		existing.Endpoints = endpoints
	}
	if req.Catalog != nil {
		existing.Catalog = mergeCatalog(existing.Catalog, *req.Catalog)
	}
	existing = withEndpointCompatibility(existing)
	existing.UpdatedAt = time.Now().UTC()
	if err := s.update(existing); err != nil {
		return Provider{}, err
	}
	return existing, nil
}

func (s *Service) Delete(id string) error {
	id = strings.TrimSpace(id)
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM runtime_profiles WHERE json_extract(fields_json, '$.model_provider_id') = ?`, id).Scan(&count); err != nil {
		return fmt.Errorf("check model provider references: %w", err)
	}
	if count > 0 {
		return ErrInUse
	}
	result, err := s.db.Exec(`DELETE FROM model_providers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete model provider: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete model provider: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) RefreshModels(ctx context.Context, id string, client *http.Client) (Provider, error) {
	provider, err := s.Get(id)
	if err != nil {
		return Provider{}, err
	}
	key := strings.TrimSpace(os.Getenv(provider.APIKeyEnv))
	if key == "" {
		return Provider{}, fmt.Errorf("model provider API key env %s is not configured", provider.APIKeyEnv)
	}
	return s.RefreshModelsWithKey(ctx, id, client, key)
}

func (s *Service) RefreshModelsWithKey(ctx context.Context, id string, client *http.Client, key string) (Provider, error) {
	provider, err := s.Get(id)
	if err != nil {
		return Provider{}, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return Provider{}, fmt.Errorf("model provider API key env %s is not configured", provider.APIKeyEnv)
	}
	if client == nil {
		client = http.DefaultClient
	}
	refreshURL, err := CatalogRefreshURL(provider)
	if err != nil {
		return Provider{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, refreshURL, nil)
	if err != nil {
		return Provider{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return Provider{}, fmt.Errorf("refresh model catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Provider{}, fmt.Errorf("refresh model catalog: upstream status %d", resp.StatusCode)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Provider{}, fmt.Errorf("parse model catalog: %w", err)
	}
	var ids []string
	for _, item := range payload.Data {
		if id := strings.TrimSpace(item.ID); id != "" {
			ids = append(ids, id)
		}
	}
	provider.Catalog = mergeRefreshed(provider.Catalog, ids)
	provider.UpdatedAt = time.Now().UTC()
	if err := s.update(provider); err != nil {
		return Provider{}, err
	}
	return provider, nil
}

func (s *Service) insert(provider Provider) error {
	protocolsJSON, endpointsJSON, catalogJSON, err := encode(provider)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO model_providers (id, name, base_url, protocols_json, endpoints_json, catalog_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		provider.ID, provider.Name, provider.BaseURL, protocolsJSON, endpointsJSON, catalogJSON,
		provider.CreatedAt.Format(time.RFC3339Nano), provider.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("store model provider: %w", err)
	}
	return nil
}

func (s *Service) update(provider Provider) error {
	protocolsJSON, endpointsJSON, catalogJSON, err := encode(provider)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`UPDATE model_providers SET name = ?, base_url = ?, protocols_json = ?, endpoints_json = ?, catalog_json = ?, updated_at = ? WHERE id = ?`,
		provider.Name, provider.BaseURL, protocolsJSON, endpointsJSON, catalogJSON, provider.UpdatedAt.Format(time.RFC3339Nano), provider.ID,
	)
	if err != nil {
		return fmt.Errorf("store model provider update: %w", err)
	}
	return nil
}

func encode(provider Provider) (string, string, string, error) {
	storedProtocols := provider.Protocols
	if len(provider.Endpoints) > 0 {
		storedProtocols = []Protocol{}
	}
	protocolsJSON, err := json.Marshal(storedProtocols)
	if err != nil {
		return "", "", "", fmt.Errorf("encode protocols: %w", err)
	}
	endpointsJSON, err := json.Marshal(provider.Endpoints)
	if err != nil {
		return "", "", "", fmt.Errorf("encode endpoints: %w", err)
	}
	catalogJSON, err := json.Marshal(provider.Catalog)
	if err != nil {
		return "", "", "", fmt.Errorf("encode catalog: %w", err)
	}
	return string(protocolsJSON), string(endpointsJSON), string(catalogJSON), nil
}

func scanProvider(row interface{ Scan(dest ...any) error }) (Provider, error) {
	var provider Provider
	var protocolsJSON, endpointsJSON, catalogJSON, createdAt, updatedAt string
	err := row.Scan(&provider.ID, &provider.Name, &provider.BaseURL, &protocolsJSON, &endpointsJSON, &catalogJSON, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, ErrNotFound
	}
	if err != nil {
		return Provider{}, err
	}
	if err := json.Unmarshal([]byte(protocolsJSON), &provider.Protocols); err != nil {
		return Provider{}, fmt.Errorf("decode protocols: %w", err)
	}
	if err := json.Unmarshal([]byte(endpointsJSON), &provider.Endpoints); err != nil {
		return Provider{}, fmt.Errorf("decode endpoints: %w", err)
	}
	if err := json.Unmarshal([]byte(catalogJSON), &provider.Catalog); err != nil {
		return Provider{}, fmt.Errorf("decode catalog: %w", err)
	}
	var errParse error
	if provider.CreatedAt, errParse = time.Parse(time.RFC3339Nano, createdAt); errParse != nil {
		return Provider{}, fmt.Errorf("parse created_at: %w", errParse)
	}
	if provider.UpdatedAt, errParse = time.Parse(time.RFC3339Nano, updatedAt); errParse != nil {
		return Provider{}, fmt.Errorf("parse updated_at: %w", errParse)
	}
	provider.APIKeyEnv = APIKeyEnv(provider.ID)
	provider.Catalog = normalizeCatalog(provider.Catalog)
	provider, err = normalizeScannedProvider(provider)
	if err != nil {
		return Provider{}, err
	}
	return provider, nil
}

func NormalizeBaseURL(input string) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(input), "/")
	if baseURL == "" {
		return "", ErrMissingBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("%w: %q", ErrMissingBaseURL, input)
	}
	return baseURL, nil
}

func NormalizeProtocols(protocols []Protocol) ([]Protocol, error) {
	seen := map[Protocol]bool{}
	var out []Protocol
	for _, protocol := range protocols {
		protocol = Protocol(strings.TrimSpace(string(protocol)))
		if protocol == "" || seen[protocol] {
			continue
		}
		if !validProtocols[protocol] {
			return nil, fmt.Errorf("%w: %s", ErrInvalidProtocol, protocol)
		}
		seen[protocol] = true
		out = append(out, protocol)
	}
	return out, nil
}

func NormalizeEndpoints(endpoints []Endpoint) ([]Endpoint, error) {
	seen := map[Protocol]bool{}
	var out []Endpoint
	for _, endpoint := range endpoints {
		protocol := Protocol(strings.TrimSpace(string(endpoint.Protocol)))
		if protocol == "" {
			continue
		}
		if !validProtocols[protocol] {
			return nil, fmt.Errorf("%w: %s", ErrInvalidProtocol, protocol)
		}
		if seen[protocol] {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateEndpointProtocol, protocol)
		}
		baseURL, err := NormalizeEndpointBaseURL(protocol, endpoint.BaseURL)
		if err != nil {
			return nil, err
		}
		seen[protocol] = true
		out = append(out, Endpoint{Protocol: protocol, BaseURL: baseURL})
	}
	return out, nil
}

func NormalizeEndpointBaseURL(protocol Protocol, input string) (string, error) {
	baseURL, err := NormalizeBaseURL(input)
	if err != nil {
		return "", err
	}
	if hasOperationSuffix(baseURL) {
		return "", fmt.Errorf("%w for %s: %q includes an operation suffix; provide the model protocol base URL before messages, responses, or chat/completions", ErrInvalidEndpointBaseURL, protocol, input)
	}
	return baseURL, nil
}

func BackfillEndpoints(baseURL string, protocols []Protocol) ([]Endpoint, error) {
	normalizedBaseURL, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	protocols, err = NormalizeProtocols(protocols)
	if err != nil {
		return nil, err
	}
	endpoints := make([]Endpoint, 0, len(protocols))
	for _, protocol := range protocols {
		endpointBaseURL := normalizedBaseURL
		if protocol == ProtocolAnthropicMessages {
			endpointBaseURL = DropFinalPathSegment(normalizedBaseURL)
		}
		endpoints = append(endpoints, Endpoint{Protocol: protocol, BaseURL: endpointBaseURL})
	}
	return NormalizeEndpoints(endpoints)
}

func DropFinalPathSegment(input string) string {
	baseURL := strings.TrimRight(strings.TrimSpace(input), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return baseURL
	}
	segments := nonEmptyPathSegments(parsed.Path)
	if len(segments) == 0 {
		return baseURL
	}
	segments = segments[:len(segments)-1]
	if len(segments) == 0 {
		parsed.Path = ""
	} else {
		parsed.Path = "/" + strings.Join(segments, "/")
	}
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/")
}

func CatalogRefreshURL(provider Provider) (string, error) {
	for _, protocol := range []Protocol{ProtocolOpenAIChatCompletions, ProtocolOpenAIResponses} {
		endpoint, ok := provider.EndpointFor(protocol)
		if !ok {
			continue
		}
		parsed, err := url.Parse(endpoint.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return "", fmt.Errorf("%w: %q", ErrMissingBaseURL, endpoint.BaseURL)
		}
		parsed.Path = "/v1/models"
		parsed.RawPath = ""
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String(), nil
	}
	return "", fmt.Errorf("model catalog refresh requires an OpenAI-family endpoint")
}

func normalizeRequestedEndpoints(baseURL string, protocols []Protocol, endpoints []Endpoint) (string, []Endpoint, error) {
	if len(endpoints) > 0 {
		normalized, err := NormalizeEndpoints(endpoints)
		if err != nil {
			return "", nil, err
		}
		return compatibilityBaseURL("", normalized), normalized, nil
	}
	normalizedBaseURL, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return "", nil, err
	}
	if len(protocols) == 0 {
		return normalizedBaseURL, nil, nil
	}
	normalized, err := BackfillEndpoints(normalizedBaseURL, protocols)
	if err != nil {
		return "", nil, err
	}
	return compatibilityBaseURL(normalizedBaseURL, normalized), normalized, nil
}

func normalizeScannedProvider(provider Provider) (Provider, error) {
	if len(provider.Endpoints) > 0 {
		endpoints, err := NormalizeEndpoints(provider.Endpoints)
		if err != nil {
			return Provider{}, err
		}
		provider.Endpoints = endpoints
		return withEndpointCompatibility(provider), nil
	}
	if strings.TrimSpace(provider.BaseURL) == "" || len(provider.Protocols) == 0 {
		protocols, err := NormalizeProtocols(provider.Protocols)
		if err != nil {
			return Provider{}, err
		}
		provider.Protocols = protocols
		return provider, nil
	}
	endpoints, err := BackfillEndpoints(provider.BaseURL, provider.Protocols)
	if err != nil {
		return Provider{}, err
	}
	provider.Endpoints = endpoints
	return withEndpointCompatibility(provider), nil
}

func withEndpointCompatibility(provider Provider) Provider {
	provider.Protocols = protocolsForEndpoints(provider.Endpoints)
	provider.BaseURL = compatibilityBaseURL(provider.BaseURL, provider.Endpoints)
	return provider
}

func compatibilityBaseURL(existing string, endpoints []Endpoint) string {
	if len(endpoints) > 0 {
		return endpoints[0].BaseURL
	}
	return strings.TrimRight(strings.TrimSpace(existing), "/")
}

func protocolsForEndpoints(endpoints []Endpoint) []Protocol {
	out := make([]Protocol, 0, len(endpoints))
	for _, endpoint := range endpoints {
		out = append(out, endpoint.Protocol)
	}
	return out
}

func hasOperationSuffix(baseURL string) bool {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	segments := nonEmptyPathSegments(parsed.Path)
	if len(segments) == 0 {
		return false
	}
	last := strings.ToLower(segments[len(segments)-1])
	if last == "messages" || last == "responses" {
		return true
	}
	return len(segments) >= 2 &&
		strings.ToLower(segments[len(segments)-2]) == "chat" &&
		last == "completions"
}

func nonEmptyPathSegments(path string) []string {
	var segments []string
	for _, segment := range strings.Split(strings.Trim(path, "/"), "/") {
		if segment != "" {
			segments = append(segments, segment)
		}
	}
	return segments
}

var slugPartPattern = regexp.MustCompile(`[^a-z0-9]+`)

func (s *Service) nextID(name string) string {
	base := strings.Trim(slugPartPattern.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-"), "-")
	if base == "" {
		base = "provider"
	}
	id := base
	for suffix := 2; s.idExists(id); suffix++ {
		id = fmt.Sprintf("%s-%d", base, suffix)
	}
	return id
}

func (s *Service) idExists(id string) bool {
	var found string
	err := s.db.QueryRow(`SELECT id FROM model_providers WHERE id = ?`, id).Scan(&found)
	return err == nil
}

func APIKeyEnv(providerID string) string {
	upperPartPattern := regexp.MustCompile(`[^A-Z0-9]+`)
	parts := upperPartPattern.Split(strings.ToUpper(strings.TrimSpace(providerID)), -1)
	var nonEmpty []string
	for _, part := range parts {
		if part != "" {
			nonEmpty = append(nonEmpty, part)
		}
	}
	if len(nonEmpty) == 0 {
		return "MODEL_PROVIDER_API_KEY"
	}
	return strings.Join(nonEmpty, "_") + "_API_KEY"
}

func mergeCatalog(existing, incoming Catalog) Catalog {
	incoming = normalizeCatalog(incoming)
	next := Catalog{
		Manual:       incoming.Manual,
		Refreshed:    existing.Refreshed,
		DefaultModel: incoming.DefaultModel,
	}
	if len(incoming.Refreshed) > 0 {
		next.Refreshed = incoming.Refreshed
	}
	return normalizeCatalog(next)
}

func normalizeCatalog(catalog Catalog) Catalog {
	catalog.Manual = uniqueSortedStrings(catalog.Manual)
	catalog.Refreshed = uniqueSortedStrings(catalog.Refreshed)
	refreshed := set(catalog.Refreshed)
	var manual []string
	for _, id := range catalog.Manual {
		if !refreshed[id] {
			manual = append(manual, id)
		}
	}
	catalog.Manual = manual
	catalog.DefaultModel = strings.TrimSpace(catalog.DefaultModel)
	return catalog
}

func mergeRefreshed(catalog Catalog, refreshed []string) Catalog {
	next := Catalog{
		Manual:       catalog.Manual,
		Refreshed:    uniqueSortedStrings(refreshed),
		DefaultModel: catalog.DefaultModel,
	}
	return normalizeCatalog(next)
}

func uniqueSortedStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func set(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

func (p Provider) Supports(protocol Protocol) bool {
	if _, ok := p.EndpointFor(protocol); ok {
		return true
	}
	for _, supported := range p.Protocols {
		if supported == protocol {
			return true
		}
	}
	return false
}

func (p Provider) EndpointFor(protocol Protocol) (Endpoint, bool) {
	for _, endpoint := range p.Endpoints {
		if endpoint.Protocol == protocol {
			return endpoint, true
		}
	}
	return Endpoint{}, false
}

func (c Catalog) Contains(model string) bool {
	model = strings.TrimSpace(model)
	for _, candidate := range append(append([]string{}, c.Manual...), c.Refreshed...) {
		if candidate == model {
			return true
		}
	}
	return false
}
