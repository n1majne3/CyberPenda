package runner

import (
	"fmt"
	"os"
	"path/filepath"

	"pentest/internal/runtimeprofile"
)

const sandboxSkillsImagePath = "/opt/pentest/skills"

// SkillsWorkdirRelPath is the project-relative directory where a runtime discovers
// task-provisioned skills inside the task workdir.
func SkillsWorkdirRelPath(provider runtimeprofile.Provider) string {
	switch provider {
	case runtimeprofile.ProviderClaudeCode:
		return ".claude/skills"
	default:
		return ".agents/skills"
	}
}

// PrepareSandboxSkills links skills into the task workdir and provider home so
// Claude/Codex/Pi can discover them. Without an explicit target it preserves
// the legacy image-baked skills path used by sandbox images.
func PrepareSandboxSkills(layout Layout, provider runtimeprofile.Provider, targets ...string) error {
	target := sandboxSkillsImagePath
	if len(targets) > 0 && targets[0] != "" {
		target = targets[0]
	}

	switch provider {
	case runtimeprofile.ProviderClaudeCode:
		claudeDir := filepath.Join(layout.Workdir, ".claude")
		if err := os.MkdirAll(claudeDir, 0o700); err != nil {
			return fmt.Errorf("prepare claude workdir: %w", err)
		}
		if err := symlinkUnlessExists(filepath.Join(claudeDir, "skills"), target); err != nil {
			return err
		}
		if err := symlinkUnlessExists(filepath.Join(layout.ProviderHome, "skills"), target); err != nil {
			return err
		}
	case runtimeprofile.ProviderCodex:
		agentsDir := filepath.Join(layout.Workdir, ".agents")
		if err := os.MkdirAll(agentsDir, 0o700); err != nil {
			return fmt.Errorf("prepare sandbox agents dir: %w", err)
		}
		if err := symlinkUnlessExists(filepath.Join(agentsDir, "skills"), target); err != nil {
			return err
		}
		if err := symlinkUnlessExists(filepath.Join(layout.ProviderHome, "skills"), target); err != nil {
			return err
		}
	case runtimeprofile.ProviderPi:
		agentsDir := filepath.Join(layout.Workdir, ".agents")
		if err := os.MkdirAll(agentsDir, 0o700); err != nil {
			return fmt.Errorf("prepare sandbox agents dir: %w", err)
		}
		if err := symlinkUnlessExists(filepath.Join(agentsDir, "skills"), target); err != nil {
			return err
		}
		agentDir := filepath.Join(layout.ProviderHome, "agent")
		if err := os.MkdirAll(agentDir, 0o700); err != nil {
			return fmt.Errorf("prepare pi agent dir: %w", err)
		}
		if err := symlinkUnlessExists(filepath.Join(agentDir, "skills"), target); err != nil {
			return err
		}
	default:
		agentsDir := filepath.Join(layout.Workdir, ".agents")
		if err := os.MkdirAll(agentsDir, 0o700); err != nil {
			return fmt.Errorf("prepare sandbox agents dir: %w", err)
		}
		if err := symlinkUnlessExists(filepath.Join(agentsDir, "skills"), target); err != nil {
			return err
		}
	}
	return nil
}

func symlinkUnlessExists(linkPath, target string) error {
	if _, err := os.Lstat(linkPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect %s: %w", linkPath, err)
	}
	if err := os.Symlink(target, linkPath); err != nil {
		return fmt.Errorf("link %s -> %s: %w", linkPath, target, err)
	}
	return nil
}