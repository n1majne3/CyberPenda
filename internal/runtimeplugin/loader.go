package runtimeplugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadDirectory reads trusted runtime plugin manifests from the top level of a
// local directory. It does not recurse and it only considers .json files.
func LoadDirectory(dir string) ([]Plugin, []error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []error{fmt.Errorf("read runtime plugin dir %q: %w", dir, err)}
	}

	var plugins []Plugin
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("read runtime plugin manifest %q: %w", path, err))
			continue
		}
		var plugin Plugin
		if err := json.Unmarshal(raw, &plugin); err != nil {
			errs = append(errs, fmt.Errorf("decode runtime plugin manifest %q: %w", path, err))
			continue
		}
		if err := Validate(plugin); err != nil {
			errs = append(errs, fmt.Errorf("validate runtime plugin manifest %q: %w", path, err))
			continue
		}
		plugins = append(plugins, plugin)
	}
	return plugins, errs
}
