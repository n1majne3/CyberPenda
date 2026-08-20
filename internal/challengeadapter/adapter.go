// Package challengeadapter loads declarative Challenge Platform adapters.
// Overlay files under the Runtime data root replace baked adapters without a rebuild.
package challengeadapter

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed adapters/*.json
var baked embed.FS

var ErrUnknownAdapter = errors.New("challenge platform adapter is unknown")

type Manifest struct {
	ID                    string               `json:"id"`
	DisplayName           string               `json:"display_name,omitempty"`
	BaseURLEnv            string               `json:"base_url_env"`
	TokenEnv              string               `json:"token_env"`
	TokenHeader           string               `json:"token_header"`
	MaxActive             int                  `json:"max_active,omitempty"`
	Budgets               map[string]int       `json:"budgets,omitempty"`
	Operations            map[string]Operation `json:"operations"`
	CloseRequiresComplete bool                 `json:"close_requires_complete,omitempty"`
	AbandonViaClose       bool                 `json:"abandon_via_close,omitempty"`
}

type Operation struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query,omitempty"`
	JSON   map[string]string `json:"json,omitempty"`
	Result string            `json:"result,omitempty"`
}

// SearchDirs returns overlay directories first, then baked defaults.
func SearchDirs(dirs ...string) []string {
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir != "" {
			out = append(out, dir)
		}
	}
	return out
}

// DefaultSearchDirs is the hosted lookup order: env dir, /data overlay, baked.
func DefaultSearchDirs() []string {
	return SearchDirs(
		strings.TrimSpace(os.Getenv("CYBERPENDA_ADAPTER_DIR")),
		"/data/adapters",
		"/opt/cyberpenda/adapters",
	)
}

// Load reads adapterID.json from the first matching search directory, else builtin.
func Load(adapterID string, searchDirs []string) (Manifest, error) {
	id := strings.TrimSpace(adapterID)
	if id == "" {
		id = "tsecbench"
	}
	name := id + ".json"
	for _, dir := range searchDirs {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		manifest, err := parseManifest(raw)
		if err != nil {
			return Manifest{}, err
		}
		if strings.TrimSpace(manifest.ID) == "" {
			manifest.ID = id
		}
		return manifest, nil
	}
	raw, err := baked.ReadFile("adapters/" + name)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: %s", ErrUnknownAdapter, id)
	}
	manifest, err := parseManifest(raw)
	if err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func parseManifest(raw []byte) (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, errors.New("decode challenge platform adapter")
	}
	if strings.TrimSpace(manifest.ID) == "" || strings.TrimSpace(manifest.BaseURLEnv) == "" || strings.TrimSpace(manifest.TokenEnv) == "" {
		return Manifest{}, errors.New("challenge platform adapter is incomplete")
	}
	if strings.TrimSpace(manifest.TokenHeader) == "" {
		manifest.TokenHeader = "Authorization"
	}
	return manifest, nil
}
