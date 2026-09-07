// Package modeskill owns the system-controlled Blackboard Mode Skills
// projected into interactive and Working Graph Runtimes. These bundles are
// not stored in the ordinary Skill catalog and cannot be edited or opted out
// by a Runtime Profile. Disabled Blackboard Mode has no Mode Skill: its only
// launch content is the owner launch boundary's state-file reminder.
package modeskill

import (
	"bytes"
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

// ErrDisabledHasNoSkill reports that Disabled Blackboard Mode has no Mode
// Skill. It is an expected resolution result, not a malformed mode value.
var ErrDisabledHasNoSkill = errors.New("disabled Blackboard Mode has no Mode Skill")

//go:embed bundles/*/SKILL.md
var embedded embed.FS

var specs = map[Mode]Spec{
	ModeInteractive:  {Mode: ModeInteractive, ID: "cyberpenda-blackboard-interactive", Name: "CyberPenda Blackboard Interactive"},
	ModeWorkingGraph: {Mode: ModeWorkingGraph, ID: "cyberpenda-blackboard-working-graph", Name: "CyberPenda Blackboard Working Graph"},
}

// domainModes holds every valid Blackboard Mode value. Disabled stays valid
// even though no Mode Skill exists for it.
var domainModes = map[Mode]bool{
	ModeInteractive:  true,
	ModeWorkingGraph: true,
	ModeDisabled:     true,
}

// Valid reports whether mode is a Blackboard Mode value.
func Valid(mode Mode) bool {
	return domainModes[mode]
}

func Resolve(mode Mode) (Spec, error) {
	spec, ok := specs[mode]
	if !ok {
		if mode == ModeDisabled {
			return Spec{}, ErrDisabledHasNoSkill
		}
		return Spec{}, fmt.Errorf("%w: %q", errInvalidMode, mode)
	}
	return spec, nil
}

// InjectInvocation prepends the provider-neutral startup directive for the
// system-selected Mode Skill and any additional system-managed Skills. It
// deliberately excludes ordinary catalog Skills, which remain available for
// task-driven selection instead of being invoked as an indiscriminate batch.
// Disabled Blackboard Mode has no Mode Skill: with no additional Skills the
// goal is returned unchanged, because the owner launch boundary already adds
// the state-file reminder.
func InjectInvocation(goal string, mode Mode, additionalSkillIDs ...string) (string, error) {
	ids := make([]string, 0, 1+len(additionalSkillIDs))
	if spec, err := Resolve(mode); err == nil {
		ids = append(ids, spec.ID)
	} else if !errors.Is(err, ErrDisabledHasNoSkill) {
		return "", err
	}
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	for _, id := range additionalSkillIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return goal, nil
	}
	var prompt strings.Builder
	prompt.WriteString("REQUIRED SKILL INVOCATION\n\nBefore any task work, invoke and follow these projected Skills in order:\n")
	for index, id := range ids {
		fmt.Fprintf(&prompt, "%d. `%s`\n", index+1, id)
	}
	prompt.WriteString("\nUse the Runtime's native Skill invocation mechanism for each exact ID. If no invocation primitive exists, read that projected Skill's SKILL.md completely. Keep the loaded instructions active for this Runtime Turn.\n\nTASK GOAL:\n")
	prompt.WriteString(goal)
	return prompt.String(), nil
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

// RetireGenerated removes only byte-identical system projections from discovery.
// Modified Skill files require operator repair instead of being overwritten.
func RetireGenerated(skillsRoot string) error {
	for _, mode := range []Mode{ModeInteractive, ModeWorkingGraph} {
		spec := specs[mode]
		dir := filepath.Join(skillsRoot, spec.ID)
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("Mode Skill root must be a directory: %s", dir)
		}
		path := filepath.Join(dir, "SKILL.md")
		info, err = os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Mode Skill must be a regular file: %s", path)
		}
		actual, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		expected, err := embedded.ReadFile("bundles/" + spec.ID + "/SKILL.md")
		if err != nil {
			return err
		}
		if !bytes.Equal(actual, expected) {
			return fmt.Errorf("modified Mode Skill blocks FGS upgrade: %s", path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func ValidateBundleCompatibility(mode Mode, bundle skill.Bundle) error {
	return validateBundleCompatibility(mode, bundle, false)
}

// ValidateFGSBundleCompatibility treats both historical enabled values as FGS.
func ValidateFGSBundleCompatibility(mode Mode, bundle skill.Bundle) error {
	return validateBundleCompatibility(mode, bundle, true)
}

func validateBundleCompatibility(mode Mode, bundle skill.Bundle, fgs bool) error {
	if !Valid(mode) {
		return fmt.Errorf("%w: %q", errInvalidMode, mode)
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
		if candidate == mode || (fgs && candidate != ModeDisabled && mode != ModeDisabled) {
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
		if !Valid(mode) {
			return nil, true, fmt.Errorf("unknown Blackboard Mode %q", mode)
		}
		if !seen[mode] {
			seen[mode] = true
			allowed = append(allowed, mode)
		}
	}
	return allowed, true, nil
}
