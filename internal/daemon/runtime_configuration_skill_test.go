package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"pentest/internal/runtimeconfig"
	"pentest/internal/skill"
	"pentest/internal/store"
)

// Historical Runtime Configuration Snapshots keep the skill IDs that were
// enabled at launch. Retired builtins must not block task or session
// continuation: the resolver skips IDs that no longer resolve and keeps
// projecting the surviving skills.
func TestRuntimeSnapshotSkillBundlesSkipsRetiredSkills(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	skills := skill.NewService(db, filepath.Join(t.TempDir(), "skills"))
	if _, err := skills.Publish(context.Background(), skill.PublishRequest{
		Metadata: skill.Metadata{ID: "tooling-nmap", Name: "nmap"},
		Files:    map[string]string{"SKILL.md": "# nmap"},
	}); err != nil {
		t.Fatalf("publish skill: %v", err)
	}

	server := &Server{skills: skills}
	bundles, err := server.runtimeSnapshotSkillBundles(runtimeconfig.RuntimeConfigurationSnapshot{
		EnabledSkillIDs: []string{"tooling-nmap", "reverse-skill-router", "vulnerabilities-xss"},
	})
	if err != nil {
		t.Fatalf("resolve snapshot bundles: %v", err)
	}
	if len(bundles) != 1 || bundles[0].ID != "tooling-nmap" {
		t.Fatalf("expected only the surviving skill bundle, got %#v", bundles)
	}
}
