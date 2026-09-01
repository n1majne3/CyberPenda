// Package modeskill owns the single system-controlled Blackboard Mode Skill
// projected into every Runtime. These bundles are not stored in the ordinary
// Skill catalog and cannot be edited or opted out by a Runtime Profile.
package modeskill

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"pentest/internal/skill"
)

type Mode string

const (
	ModeInteractive  Mode = "interactive"
	ModeWorkingGraph Mode = "working_graph"
	ModeDisabled     Mode = "disabled"
)

type Spec struct {
	Mode Mode
	ID   string
	Name string
}

var errInvalidMode = errors.New("invalid Blackboard Mode Skill")

//go:embed bundles/*/SKILL.md
var embedded embed.FS

var specs = map[Mode]Spec{
	ModeInteractive:  {Mode: ModeInteractive, ID: "cyberpenda-blackboard-interactive", Name: "CyberPenda Blackboard Interactive"},
	ModeWorkingGraph: {Mode: ModeWorkingGraph, ID: "cyberpenda-blackboard-working-graph", Name: "CyberPenda Blackboard Working Graph"},
	ModeDisabled:     {Mode: ModeDisabled, ID: "cyberpenda-blackboard-disabled", Name: "CyberPenda Blackboard Disabled"},
}

func Resolve(mode Mode) (Spec, error) {
	spec, ok := specs[mode]
	if !ok {
		return Spec{}, fmt.Errorf("%w: %q", errInvalidMode, mode)
	}
	return spec, nil
}

func Project(skillsRoot string, mode Mode) (skill.Bundle, error) {
	spec, err := Resolve(mode)
	if err != nil {
		return skill.Bundle{}, err
	}
	root := filepath.Join(strings.TrimSpace(skillsRoot), spec.ID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return skill.Bundle{}, fmt.Errorf("prepare Mode Skill root: %w", err)
	}
	body, err := embedded.ReadFile(filepath.ToSlash(filepath.Join("bundles", spec.ID, "SKILL.md")))
	if err != nil {
		return skill.Bundle{}, fmt.Errorf("read embedded Mode Skill: %w", err)
	}
	target := filepath.Join(root, "SKILL.md")
	temporary := target + ".tmp"
	if err := os.WriteFile(temporary, body, 0o600); err != nil {
		return skill.Bundle{}, fmt.Errorf("stage Mode Skill: %w", err)
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return skill.Bundle{}, fmt.Errorf("publish Mode Skill: %w", err)
	}
	bundle := skill.Bundle{ID: spec.ID, Name: spec.Name, Source: skill.SourceProvenance{Kind: "system"}, Path: root}
	if err := skill.ValidateBundle(root, skill.Metadata{ID: spec.ID, Name: spec.Name}); err != nil {
		return skill.Bundle{}, err
	}
	return bundle, nil
}

func ValidateBundleCompatibility(mode Mode, bundle skill.Bundle) error {
	if _, err := Resolve(mode); err != nil {
		return err
	}
	for _, spec := range specs {
		if skill.DisplayID(bundle.ID, bundle.Source) == spec.ID {
			return fmt.Errorf("%w: user Skill %q conflicts with system Mode Skill", skill.ErrInvalidSkill, bundle.ID)
		}
	}
	body, err := os.ReadFile(filepath.Join(bundle.Path, "SKILL.md"))
	if err != nil {
		return fmt.Errorf("%w: read Skill compatibility metadata for %q: %v", skill.ErrInvalidSkill, bundle.ID, err)
	}
	allowed, present, err := parseBlackboardModes(string(body))
	if err != nil {
		return fmt.Errorf("%w: Skill %q blackboard_modes: %v", skill.ErrInvalidSkill, bundle.ID, err)
	}
	if !present {
		return nil
	}
	for _, candidate := range allowed {
		if candidate == mode {
			return nil
		}
	}
	values := make([]string, 0, len(allowed))
	for _, candidate := range allowed {
		values = append(values, string(candidate))
	}
	sort.Strings(values)
	return fmt.Errorf("%w: Skill %q is incompatible with Blackboard Mode %q; allowed modes: %s", skill.ErrInvalidSkill, bundle.ID, mode, strings.Join(values, ", "))
}

func parseBlackboardModes(document string) ([]Mode, bool, error) {
	lines := strings.Split(document, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, false, nil
	}
	var values []string
	found := false
	list := false
	for _, raw := range lines[1:] {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "---" {
			break
		}
		if strings.HasPrefix(trimmed, "blackboard_modes:") {
			found = true
			list = true
			inline := strings.TrimSpace(strings.TrimPrefix(trimmed, "blackboard_modes:"))
			if inline != "" {
				list = false
				inline = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(inline, "["), "]"))
				if inline != "" {
					values = append(values, strings.Split(inline, ",")...)
				}
			}
			continue
		}
		if list && strings.HasPrefix(trimmed, "-") {
			values = append(values, strings.TrimSpace(strings.TrimPrefix(trimmed, "-")))
			continue
		}
		if list && trimmed != "" && !strings.HasPrefix(raw, " ") {
			list = false
		}
	}
	if !found {
		return nil, false, nil
	}
	if len(values) == 0 {
		return nil, true, errors.New("must list at least one mode")
	}
	allowed := make([]Mode, 0, len(values))
	seen := map[Mode]bool{}
	for _, raw := range values {
		mode := Mode(strings.Trim(strings.TrimSpace(raw), "'\""))
		if _, err := Resolve(mode); err != nil {
			return nil, true, err
		}
		if !seen[mode] {
			seen[mode] = true
			allowed = append(allowed, mode)
		}
	}
	return allowed, true, nil
}
