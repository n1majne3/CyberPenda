package modelprovider

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed snapshot.json
var bundledCapabilitySnapshot []byte

const (
	LimitSourceCatalog            = "catalog"
	LimitSourceCapabilityCache    = "capability_cache"
	LimitSourceRuntimeDefault     = "runtime_default"
	CapabilityCacheOverlayFile    = "model-capability-cache.json"
	ModelsDevAPIURL               = "https://models.dev/api.json"
	MaxCapabilityCacheBytes       = 16 * 1024 * 1024
	CapabilityCacheRefreshTimeout = 30 * time.Second
)

var ErrCapabilityCacheRefreshFailed = errors.New("model capability cache refresh failed")

type CatalogLimits struct {
	ContextWindow   int `json:"context_window,omitempty"`
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
}

type ResolvedLimits struct {
	ContextWindow         int
	MaxOutputTokens       int
	ContextWindowSource   string
	MaxOutputTokensSource string
}

type CapabilityLookup interface {
	Lookup(modelID string) (CatalogLimits, bool)
}

type CapabilityCache struct {
	mu          sync.RWMutex
	bundled     map[string]CatalogLimits
	overlay     map[string]CatalogLimits
	overlayDir  string
	client      *http.Client
	refreshedAt time.Time
}

func BundledCapabilityLimits() map[string]CatalogLimits {
	var bundled map[string]CatalogLimits
	if err := json.Unmarshal(bundledCapabilitySnapshot, &bundled); err != nil || bundled == nil {
		return map[string]CatalogLimits{}
	}
	return bundled
}

func LoadCapabilityCache(overlayDir string, client *http.Client) *CapabilityCache {
	cache := NewCapabilityCache(BundledCapabilityLimits(), overlayDir, client)
	_ = cache.LoadOverlay()
	return cache
}

func NewCapabilityCache(bundled map[string]CatalogLimits, overlayDir string, client *http.Client) *CapabilityCache {
	if bundled == nil {
		bundled = map[string]CatalogLimits{}
	}
	if client == nil {
		client = &http.Client{Timeout: CapabilityCacheRefreshTimeout}
	}
	return &CapabilityCache{bundled: cloneLimitsMap(bundled), overlayDir: strings.TrimSpace(overlayDir), client: client}
}

func (c *CapabilityCache) Lookup(modelID string) (CatalogLimits, bool) {
	if c == nil {
		return CatalogLimits{}, false
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return CatalogLimits{}, false
	}
	c.mu.RLock()
	entries := c.entries()
	c.mu.RUnlock()
	if got, ok := entries[modelID]; ok && got.hasAny() {
		return got, true
	}
	suffix := modelSuffix(modelID)
	var match *CatalogLimits
	for id, limits := range entries {
		if modelSuffix(id) != suffix || !limits.hasAny() {
			continue
		}
		if match == nil {
			copyLimits := limits
			match = &copyLimits
			continue
		}
		if *match != limits {
			return CatalogLimits{}, false
		}
	}
	if match == nil {
		return CatalogLimits{}, false
	}
	return *match, true
}

func (c *CapabilityCache) LoadOverlay() error {
	if c == nil || strings.TrimSpace(c.overlayDir) == "" {
		return nil
	}
	path := filepath.Join(c.overlayDir, CapabilityCacheOverlayFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var overlay map[string]CatalogLimits
	if err := json.Unmarshal(raw, &overlay); err != nil {
		return fmt.Errorf("decode model capability cache overlay: %w", err)
	}
	c.mu.Lock()
	c.overlay = overlay
	if info, statErr := os.Stat(path); statErr == nil {
		c.refreshedAt = info.ModTime().UTC()
	}
	c.mu.Unlock()
	return nil
}

func (c *CapabilityCache) Refresh(ctx context.Context) error {
	if c == nil {
		return ErrCapabilityCacheRefreshFailed
	}
	if c.client == nil {
		c.client = &http.Client{Timeout: CapabilityCacheRefreshTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ModelsDevAPIURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCapabilityCacheRefreshFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: upstream status %d", ErrCapabilityCacheRefreshFailed, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(MaxCapabilityCacheBytes)+1))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCapabilityCacheRefreshFailed, err)
	}
	if len(raw) > MaxCapabilityCacheBytes {
		return fmt.Errorf("%w: response exceeds size limit", ErrCapabilityCacheRefreshFailed)
	}
	flat, err := FlattenModelsDev(raw)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCapabilityCacheRefreshFailed, err)
	}
	if strings.TrimSpace(c.overlayDir) != "" {
		if err := os.MkdirAll(c.overlayDir, 0o700); err != nil {
			return err
		}
		encoded, err := json.Marshal(flat)
		if err != nil {
			return err
		}
		path := filepath.Join(c.overlayDir, CapabilityCacheOverlayFile)
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.overlay = flat
	c.refreshedAt = time.Now().UTC()
	c.mu.Unlock()
	return nil
}

func (c *CapabilityCache) Status() (refreshedAt time.Time, count int) {
	if c == nil {
		return time.Time{}, 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.refreshedAt, len(c.entries())
}

func (c *CapabilityCache) entries() map[string]CatalogLimits {
	if len(c.overlay) > 0 {
		return c.overlay
	}
	return c.bundled
}

func FlattenModelsDev(raw []byte) (map[string]CatalogLimits, error) {
	var doc map[string]struct {
		Models map[string]struct {
			ID    string `json:"id"`
			Limit struct {
				Context int `json:"context"`
				Output  int `json:"output"`
			} `json:"limit"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse models.dev catalog: %w", err)
	}
	votes := map[string]map[CatalogLimits]int{}
	for _, provider := range doc {
		for key, model := range provider.Models {
			id := strings.TrimSpace(model.ID)
			if id == "" {
				id = strings.TrimSpace(key)
			}
			if id == "" || model.Limit.Context < 1 || model.Limit.Output < 1 {
				continue
			}
			limits := CatalogLimits{ContextWindow: model.Limit.Context, MaxOutputTokens: model.Limit.Output}
			if votes[id] == nil {
				votes[id] = map[CatalogLimits]int{}
			}
			votes[id][limits]++
		}
	}
	out := make(map[string]CatalogLimits, len(votes))
	for id, counts := range votes {
		out[id] = pickLimits(counts)
	}
	return out, nil
}

func pickLimits(counts map[CatalogLimits]int) CatalogLimits {
	var best CatalogLimits
	bestCount := -1
	for limits, count := range counts {
		if count > bestCount ||
			(count == bestCount && (limits.ContextWindow > best.ContextWindow ||
				(limits.ContextWindow == best.ContextWindow && limits.MaxOutputTokens > best.MaxOutputTokens))) {
			best = limits
			bestCount = count
		}
	}
	return best
}

func ResolveLimits(modelID string, catalog Catalog, cache CapabilityLookup) ResolvedLimits {
	modelID = strings.TrimSpace(modelID)
	resolved := ResolvedLimits{
		ContextWindowSource:   LimitSourceRuntimeDefault,
		MaxOutputTokensSource: LimitSourceRuntimeDefault,
	}
	if override, ok := catalog.Limits[modelID]; ok {
		if override.ContextWindow >= 1 {
			resolved.ContextWindow = override.ContextWindow
			resolved.ContextWindowSource = LimitSourceCatalog
		}
		if override.MaxOutputTokens >= 1 {
			resolved.MaxOutputTokens = override.MaxOutputTokens
			resolved.MaxOutputTokensSource = LimitSourceCatalog
		}
	}
	if resolved.ContextWindowSource != LimitSourceRuntimeDefault && resolved.MaxOutputTokensSource != LimitSourceRuntimeDefault {
		return resolved
	}
	if cache != nil {
		if cached, ok := cache.Lookup(modelID); ok {
			if resolved.ContextWindowSource == LimitSourceRuntimeDefault && cached.ContextWindow >= 1 {
				resolved.ContextWindow = cached.ContextWindow
				resolved.ContextWindowSource = LimitSourceCapabilityCache
			}
			if resolved.MaxOutputTokensSource == LimitSourceRuntimeDefault && cached.MaxOutputTokens >= 1 {
				resolved.MaxOutputTokens = cached.MaxOutputTokens
				resolved.MaxOutputTokensSource = LimitSourceCapabilityCache
			}
		}
	}
	return resolved
}

func (l CatalogLimits) hasAny() bool {
	return l.ContextWindow >= 1 || l.MaxOutputTokens >= 1
}

func modelSuffix(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if i := strings.LastIndex(modelID, "/"); i >= 0 && i+1 < len(modelID) {
		return strings.ToLower(modelID[i+1:])
	}
	return strings.ToLower(modelID)
}

func cloneLimitsMap(in map[string]CatalogLimits) map[string]CatalogLimits {
	out := make(map[string]CatalogLimits, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
