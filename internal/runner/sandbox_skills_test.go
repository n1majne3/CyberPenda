package runner_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

const sandboxTaskSkillsDir = "/task/skills"

// requireSkillsDiscovery asserts the Runtime skills discovery contract at
// discovery. A host that can create symbolic links points the path at target; a
// Windows host materializes a real directory instead, because creating that link
// needs a privilege the daemon does not hold.
func requireSkillsDiscovery(t *testing.T, discovery, target string) {
	t.Helper()
	info, err := os.Lstat(discovery)
	if err != nil {
		t.Fatalf("skills discovery path %s: %v", discovery, err)
	}
	if runtime.GOOS == "windows" {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			t.Fatalf("skills discovery path %s must be a real directory, mode = %v", discovery, info.Mode())
		}
		return
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("skills discovery path %s must be a symbolic link, mode = %v", discovery, info.Mode())
	}
	if got, err := os.Readlink(discovery); err != nil || got != target {
		t.Fatalf("skills discovery link %s = %q, err = %v, want %q", discovery, got, err, target)
	}
}

// requireSkillContent asserts that a discovery path exposes one projected Skill
// document. It holds on every host: a POSIX host reads through the link and a
// Windows host reads the materialized directory.
func requireSkillContent(t *testing.T, discovery, skillID, doc string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(discovery, skillID, "SKILL.md"))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(discovery, skillID, "SKILL.md"), err)
	}
	if string(raw) != doc {
		t.Fatalf("%s SKILL.md = %q, want %q", discovery, raw, doc)
	}
}

func requireProjectedSkill(t *testing.T, layout runner.Layout, skillID, doc string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(layout.SkillsRoot, skillID), 0o700); err != nil {
		t.Fatalf("prepare projected skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.SkillsRoot, skillID, "SKILL.md"), []byte(doc), 0o600); err != nil {
		t.Fatalf("write projected skill: %v", err)
	}
}

func providerHomeSkills(layout runner.Layout, provider runtimeprofile.Provider) string {
	if provider == runtimeprofile.ProviderPi {
		return filepath.Join(layout.ProviderHome, "agent", "skills")
	}
	return filepath.Join(layout.ProviderHome, "skills")
}

func TestSkillsWorkdirRelPath(t *testing.T) {
	if got := runner.SkillsWorkdirRelPath(runtimeprofile.ProviderClaudeCode); got != ".claude/skills" {
		t.Fatalf("claude skills path = %q", got)
	}
	if got := runner.SkillsWorkdirRelPath(runtimeprofile.ProviderCodex); got != ".agents/skills" {
		t.Fatalf("codex skills path = %q", got)
	}
}

func TestPrepareSandboxSkillsLinksClaudeWorkdirAndProviderHome(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-1", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	if err := runner.PrepareSandboxSkills(layout, runtimeprofile.ProviderClaudeCode); err != nil {
		t.Fatalf("prepare sandbox skills: %v", err)
	}

	workdirLink := filepath.Join(layout.Workdir, ".claude", "skills")
	requireSkillsDiscovery(t, workdirLink, "/opt/pentest/skills")
	if _, err := os.Lstat(filepath.Join(layout.Workdir, ".agents", "skills")); !os.IsNotExist(err) {
		t.Fatalf("expected no .agents/skills link for claude code, err=%v", err)
	}
	requireSkillsDiscovery(t, filepath.Join(layout.ProviderHome, "skills"), "/opt/pentest/skills")
}

func TestPrepareSandboxSkillsLinksCodexWorkdirAndProviderHome(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-1", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	if err := runner.PrepareSandboxSkills(layout, runtimeprofile.ProviderCodex); err != nil {
		t.Fatalf("prepare sandbox skills: %v", err)
	}

	workdirLink := filepath.Join(layout.Workdir, ".agents", "skills")
	requireSkillsDiscovery(t, workdirLink, "/opt/pentest/skills")
}

// A Windows daemon cannot create the symbolic links that expose task Skills to a
// Sandbox Runtime. Projection must still succeed, and enabled Skills must stay
// discoverable at the Runtime discovery paths.
func TestPrepareSandboxSkillsWithoutSymlinkPrivilege(t *testing.T) {
	for _, provider := range []runtimeprofile.Provider{runtimeprofile.ProviderPi, runtimeprofile.ProviderClaudeCode} {
		t.Run(string(provider), func(t *testing.T) {
			layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-1", provider)
			if err != nil {
				t.Fatalf("prepare layout: %v", err)
			}
			workdirSkills := filepath.Join(layout.Workdir, filepath.FromSlash(runner.SkillsWorkdirRelPath(provider)))
			homeSkills := providerHomeSkills(layout, provider)

			// Disabled or empty Skills project no content, so they must not need
			// a link this host cannot create.
			if err := runner.PrepareSandboxSkills(layout, provider, sandboxTaskSkillsDir); err != nil {
				t.Fatalf("prepare sandbox skills without Skill bundles: %v", err)
			}
			requireSkillsDiscovery(t, workdirSkills, sandboxTaskSkillsDir)
			requireSkillsDiscovery(t, homeSkills, sandboxTaskSkillsDir)

			// The legacy image-baked target has no host directory to expose.
			imageLayout, err := runner.PrepareTaskLayout(t.TempDir(), "task-2", provider)
			if err != nil {
				t.Fatalf("prepare image layout: %v", err)
			}
			if err := runner.PrepareSandboxSkills(imageLayout, provider); err != nil {
				t.Fatalf("prepare sandbox skills for the image-baked target: %v", err)
			}
			requireSkillsDiscovery(t, filepath.Join(imageLayout.Workdir, filepath.FromSlash(runner.SkillsWorkdirRelPath(provider))), "/opt/pentest/skills")

			// An enabled Skill stays discoverable at both discovery paths.
			const skillID = "recon-helper"
			const doc = "# Recon\n"
			requireProjectedSkill(t, layout, skillID, doc)
			if err := runner.PrepareSandboxSkills(layout, provider, sandboxTaskSkillsDir); err != nil {
				t.Fatalf("prepare sandbox skills with an enabled Skill: %v", err)
			}
			requireSkillsDiscovery(t, workdirSkills, sandboxTaskSkillsDir)
			requireSkillsDiscovery(t, homeSkills, sandboxTaskSkillsDir)
			if runtime.GOOS == "windows" {
				// The sandbox target path exists only inside the container, so a
				// POSIX host cannot read it. A Windows host must prove the
				// content, because it materializes a copy instead of linking.
				requireSkillContent(t, workdirSkills, skillID, doc)
				requireSkillContent(t, homeSkills, skillID, doc)
			}

			// A later Runtime Continuation that projects another Skill must
			// expose it as well.
			const laterID = "later-skill"
			const laterDoc = "# Later\n"
			requireProjectedSkill(t, layout, laterID, laterDoc)
			if err := runner.PrepareSandboxSkills(layout, provider, sandboxTaskSkillsDir); err != nil {
				t.Fatalf("refresh sandbox skills: %v", err)
			}
			if runtime.GOOS == "windows" {
				requireSkillContent(t, workdirSkills, laterID, laterDoc)
			}
		})
	}
}
