package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// NPXImporter is the production controlled importer. It invokes a fixed npx
// command shape with structured package/ref arguments; callers never provide a
// shell command.
type NPXImporter struct {
	Binary string
}

func (i NPXImporter) ImportSkill(ctx context.Context, request ImportRequest) (ImportedBundle, error) {
	if strings.TrimSpace(request.Package) == "" {
		return ImportedBundle{}, fmt.Errorf("%w: package is required", ErrInvalidSkill)
	}
	binary := strings.TrimSpace(i.Binary)
	if binary == "" {
		binary = "npx"
	}
	args := []string{"--yes", "skills", "import", "--package", strings.TrimSpace(request.Package), "--json"}
	if ref := strings.TrimSpace(request.Ref); ref != "" {
		args = append(args, "--ref", ref)
	}
	if sourceURL := strings.TrimSpace(request.SourceURL); sourceURL != "" {
		args = append(args, "--source-url", sourceURL)
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	raw, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return ImportedBundle{}, fmt.Errorf("npx skills import failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return ImportedBundle{}, fmt.Errorf("run npx skills import: %w", err)
	}
	var imported ImportedBundle
	if err := json.Unmarshal(raw, &imported); err != nil {
		return ImportedBundle{}, fmt.Errorf("decode npx skills import JSON: %w", err)
	}
	return imported, nil
}
