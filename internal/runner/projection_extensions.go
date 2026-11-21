package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pentest/internal/modeskill"
	"pentest/internal/runtimeextension"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
)

func validateBlackboardMode(mode modeskill.Mode) error {
	if !modeskill.Valid(mode) {
		return fmt.Errorf("invalid Blackboard Mode: %q", mode)
	}
	return nil
}

func projectModeAndUserSkills(layout Layout, req ProjectionRequest) error {
	if err := validateBlackboardMode(req.BlackboardMode); err != nil {
		return err
	}
	if req.BlackboardProtocol == "fgs" && req.BlackboardMode != modeskill.ModeDisabled {
		if err := modeskill.RetireGenerated(layout.SkillsRoot); err != nil {
			return err
		}
	}
	if req.BlackboardMode != modeskill.ModeDisabled && req.BlackboardProtocol != "fgs" {
		if _, err := modeskill.Project(layout.SkillsRoot, req.BlackboardMode); err != nil {
			return err
		}
	}
	for _, bundle := range req.SkillBundles {
		validate := modeskill.ValidateBundleCompatibility
		if req.BlackboardProtocol == "fgs" {
			validate = modeskill.ValidateFGSBundleCompatibility
		}
		if err := validate(req.BlackboardMode, bundle); err != nil {
			return err
		}
	}
	if len(req.SkillBundles) > 0 {
		return projectSkillBundles(layout, req.SkillBundles)
	}
	return nil
}

func addModeSkillProjectionPreview(projection *ConfigProjection, mode modeskill.Mode, layout Layout) {
	if mode == modeskill.ModeDisabled {
		return
	}
	spec, err := modeskill.Resolve(mode)
	if err != nil {
		return
	}
	if projection.Config == nil {
		projection.Config = map[string]any{}
	}
	projection.Config["mode_skill"] = map[string]any{
		"id": spec.ID, "name": spec.Name, "target": filepath.Join(layout.SkillsRoot, spec.ID), "system_owned": true,
	}
}

func projectSkillBundles(layout Layout, bundles []skill.Bundle) error {
	if strings.TrimSpace(layout.SkillsRoot) == "" {
		return fmt.Errorf("skills root is required")
	}
	if err := os.MkdirAll(layout.SkillsRoot, 0o700); err != nil {
		return fmt.Errorf("prepare skills root: %w", err)
	}
	targets := map[string]string{}
	for _, bundle := range bundles {
		projectionID := skill.DisplayID(bundle.ID, bundle.Source)
		projectionName := skill.DisplayName(bundle.Name, bundle.ID, bundle.Source)
		if previous, exists := targets[projectionID]; exists {
			return fmt.Errorf("project skill %q: source-free skill folder %q conflicts with %q", bundle.ID, projectionID, previous)
		}
		targets[projectionID] = bundle.ID
		meta := skill.Metadata{ID: projectionID, Name: projectionName}
		if strings.TrimSpace(meta.Name) == "" {
			meta.Name = projectionID
		}
		if err := skill.ValidateBundle(bundle.Path, meta); err != nil {
			return err
		}
		target := filepath.Join(layout.SkillsRoot, projectionID)
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("clear projected skill %q: %w", projectionID, err)
		}
		if err := copyRuntimeExtensionDir(bundle.Path, target); err != nil {
			return fmt.Errorf("project skill %q: %w", projectionID, err)
		}
	}
	return nil
}

func addSkillProjectionPreview(projection *ConfigProjection, bundles []skill.Bundle, layout Layout) {
	if projection.Config == nil {
		projection.Config = map[string]any{}
	}
	previews := make([]map[string]any, 0, len(bundles))
	for _, bundle := range bundles {
		projectionID := skill.DisplayID(bundle.ID, bundle.Source)
		preview := map[string]any{
			"id":     projectionID,
			"name":   skill.DisplayName(bundle.Name, bundle.ID, bundle.Source),
			"target": filepath.Join(layout.SkillsRoot, projectionID),
		}
		previews = append(previews, preview)
	}
	projection.Config["skills"] = previews
}

func projectRuntimeExtensions(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest, projection *ConfigProjection) error {
	if req.RuntimeExtensions == nil || len(profile.Fields.RuntimeExtensions) == 0 {
		return nil
	}
	previews := make([]map[string]any, 0, len(profile.Fields.RuntimeExtensions))
	for _, ref := range profile.Fields.RuntimeExtensions {
		if !runtimeExtensionRefEnabled(ref) {
			continue
		}
		extension, ok := req.RuntimeExtensions.Get(ref.ID)
		if !ok {
			if preview, ok := runtimeExtensionCatalogPreview(ref); ok {
				previews = append(previews, preview)
				continue
			}
			return fmt.Errorf("runtime extension %q not found", ref.ID)
		}
		if !runtimeextension.CompatibleWith(extension, string(profile.Provider)) {
			return fmt.Errorf("runtime extension %q is not compatible with provider %q", ref.ID, profile.Provider)
		}
		targetRoot, err := runtimeExtensionTargetRoot(layout, extension.Projection.Location)
		if err != nil {
			return err
		}
		target := filepath.Join(targetRoot, filepath.FromSlash(extension.Projection.Path))
		if err := copyRuntimeExtension(extension, target); err != nil {
			return err
		}
		preview := map[string]any{
			"id":     extension.ID,
			"name":   extension.Name,
			"target": target,
		}
		if len(ref.Config) > 0 {
			preview["config"] = ref.Config
		}
		previews = append(previews, preview)
	}
	if len(previews) == 0 {
		return nil
	}
	if projection.Config == nil {
		projection.Config = map[string]any{}
	}
	projection.Config["runtime_extensions"] = previews
	return nil
}

func runtimeExtensionCatalogPreview(ref runtimeprofile.RuntimeExtensionRef) (map[string]any, bool) {
	registry := strings.TrimSpace(ref.Config["registry"])
	installRef := strings.TrimSpace(ref.Config["install_ref"])
	if registry == "" && installRef == "" {
		return nil, false
	}
	preview := map[string]any{
		"id":     ref.ID,
		"source": "catalog",
	}
	if registry != "" {
		preview["registry"] = registry
	}
	if installRef != "" {
		preview["install_ref"] = installRef
	}
	if sourceURL := strings.TrimSpace(ref.Config["source_url"]); sourceURL != "" {
		preview["source_url"] = sourceURL
	}
	if len(ref.Config) > 0 {
		preview["config"] = ref.Config
	}
	return preview, true
}

func runtimeExtensionRefEnabled(ref runtimeprofile.RuntimeExtensionRef) bool {
	return ref.Enabled == nil || *ref.Enabled
}

func runtimeExtensionTargetRoot(layout Layout, location string) (string, error) {
	switch location {
	case "provider_home":
		return layout.ProviderHome, nil
	case "runtime_home":
		return layout.RuntimeHome, nil
	case "workdir":
		return layout.Workdir, nil
	default:
		return "", fmt.Errorf("unsupported runtime extension projection location %q", location)
	}
}

func copyRuntimeExtension(extension runtimeextension.Extension, target string) error {
	switch extension.Source.Type {
	case "local_dir":
		return copyRuntimeExtensionDir(extension.Source.Path, target)
	case "local_file":
		return copyRuntimeExtensionFile(extension.Source.Path, target)
	default:
		return fmt.Errorf("unsupported runtime extension source type %q", extension.Source.Type)
	}
}

func copyRuntimeExtensionDir(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("runtime extension source must not contain symlinks: %s", path)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(dst, 0o700)
		}
		return copyRuntimeExtensionFile(path, dst)
	})
}

func copyRuntimeExtensionFile(source, target string) error {
	raw, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read runtime extension source %q: %w", source, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("prepare runtime extension target: %w", err)
	}
	if err := os.WriteFile(target, raw, 0o600); err != nil {
		return fmt.Errorf("write runtime extension target %q: %w", target, err)
	}
	return nil
}
