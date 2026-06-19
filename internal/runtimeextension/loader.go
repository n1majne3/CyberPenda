package runtimeextension

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func LoadDirectory(dir string) ([]Extension, []error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []error{fmt.Errorf("read runtime extension dir %q: %w", dir, err)}
	}
	var extensions []Extension
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("read runtime extension manifest %q: %w", path, err))
			continue
		}
		var extension Extension
		if err := json.Unmarshal(raw, &extension); err != nil {
			errs = append(errs, fmt.Errorf("decode runtime extension manifest %q: %w", path, err))
			continue
		}
		if err := Validate(extension); err != nil {
			errs = append(errs, fmt.Errorf("validate runtime extension manifest %q: %w", path, err))
			continue
		}
		extensions = append(extensions, extension)
	}
	return extensions, errs
}
