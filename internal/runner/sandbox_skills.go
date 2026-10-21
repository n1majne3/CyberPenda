package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"pentest/internal/runtimeprofile"
)

const sandboxSkillsImagePath = "/opt/pentest/skills"

// sandboxTaskSkillsPath is the sandbox-side path of Layout.SkillsRoot: a sandbox
// launch mounts the task root at /task.
const sandboxTaskSkillsPath = "/task/skills"

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

// PrepareSandboxSkills exposes skills at the task workdir and provider home
// discovery paths so Claude/Codex/Pi can find them. Without an explicit target it
// preserves the legacy image-baked skills path used by sandbox images.
func PrepareSandboxSkills(layout Layout, provider runtimeprofile.Provider, targets ...string) error {
	target := sandboxSkillsImagePath
	if len(targets) > 0 && targets[0] != "" {
		target = targets[0]
	}
	source := skillsSource(layout, target)

	switch provider {
	case runtimeprofile.ProviderClaudeCode:
		claudeDir := filepath.Join(layout.Workdir, ".claude")
		if err := os.MkdirAll(claudeDir, 0o700); err != nil {
			return fmt.Errorf("prepare claude workdir: %w", err)
		}
		if err := exposeSkillsDir(filepath.Join(claudeDir, "skills"), target, source); err != nil {
			return err
		}
		if err := exposeSkillsDir(filepath.Join(layout.ProviderHome, "skills"), target, source); err != nil {
			return err
		}
	case runtimeprofile.ProviderCodex:
		agentsDir := filepath.Join(layout.Workdir, ".agents")
		if err := os.MkdirAll(agentsDir, 0o700); err != nil {
			return fmt.Errorf("prepare sandbox agents dir: %w", err)
		}
		if err := exposeSkillsDir(filepath.Join(agentsDir, "skills"), target, source); err != nil {
			return err
		}
		if err := exposeSkillsDir(filepath.Join(layout.ProviderHome, "skills"), target, source); err != nil {
			return err
		}
	case runtimeprofile.ProviderPi:
		agentsDir := filepath.Join(layout.Workdir, ".agents")
		if err := os.MkdirAll(agentsDir, 0o700); err != nil {
			return fmt.Errorf("prepare sandbox agents dir: %w", err)
		}
		if err := exposeSkillsDir(filepath.Join(agentsDir, "skills"), target, source); err != nil {
			return err
		}
		agentDir := filepath.Join(layout.ProviderHome, "agent")
		if err := os.MkdirAll(agentDir, 0o700); err != nil {
			return fmt.Errorf("prepare pi agent dir: %w", err)
		}
		if err := exposeSkillsDir(filepath.Join(agentDir, "skills"), target, source); err != nil {
			return err
		}
	default:
		agentsDir := filepath.Join(layout.Workdir, ".agents")
		if err := os.MkdirAll(agentsDir, 0o700); err != nil {
			return fmt.Errorf("prepare sandbox agents dir: %w", err)
		}
		if err := exposeSkillsDir(filepath.Join(agentsDir, "skills"), target, source); err != nil {
			return err
		}
	}
	return nil
}

// skillsSource returns the host directory whose content a discovery path must
// expose, or "" when the target exists only inside the sandbox image.
func skillsSource(layout Layout, target string) string {
	switch target {
	case sandboxSkillsImagePath:
		return ""
	case sandboxTaskSkillsPath:
		return layout.SkillsRoot
	default:
		return target
	}
}

// exposeSkillsDir makes Skills reachable at a Runtime discovery path. A host that
// can create symbolic links points the path at the sandbox-side target, so the
// task skills root stays the only copy. A Windows host cannot create that link,
// so it materializes the same content as a real directory instead.
func exposeSkillsDir(discovery, target, source string) error {
	if canSymlinkSkills() {
		return symlinkUnlessExists(discovery, target)
	}
	return materializeSkillsDir(discovery, source)
}

// canSymlinkSkills reports whether this host can create the symbolic link that
// exposes a sandbox-side skills path to a Runtime. Windows requires
// SeCreateSymbolicLinkPrivilege or Developer Mode for that call.
func canSymlinkSkills() bool {
	return runtime.GOOS != "windows"
}

// materializeSkillsDir refreshes discovery as a real directory holding a copy of
// the task skills root, so a later Runtime Continuation sees newly enabled
// Skills. An image-only target has no host copy: the directory stays empty,
// because image content is not reachable from the host.
func materializeSkillsDir(discovery, source string) error {
	if info, err := os.Lstat(discovery); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if !info.IsDir() {
			return fmt.Errorf("skills discovery path is not a directory: %s", discovery)
		}
		if err := os.RemoveAll(discovery); err != nil {
			return fmt.Errorf("refresh skills discovery path %s: %w", discovery, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect %s: %w", discovery, err)
	}
	if source == "" {
		if err := os.MkdirAll(discovery, 0o700); err != nil {
			return fmt.Errorf("prepare skills discovery path %s: %w", discovery, err)
		}
		return nil
	}
	if err := copyRuntimeExtensionDir(source, discovery); err != nil {
		return fmt.Errorf("materialize skills at %s: %w", discovery, err)
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
