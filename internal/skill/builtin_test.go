package skill_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"pentest/internal/skill"
)

func TestBuiltinBundlesIncludeRequestedProjects(t *testing.T) {
	bundles, err := skill.BuiltinBundles()
	if err != nil {
		t.Fatalf("load builtin bundles: %v", err)
	}
	byID := map[string]skill.ImportedBundle{}
	for _, bundle := range bundles {
		if strings.HasPrefix(bundle.Metadata.ID, "cyberstrikeai-") || strings.HasPrefix(bundle.Metadata.ID, "strix-") {
			t.Fatalf("builtin bundle ID %q should not include a source prefix", bundle.Metadata.ID)
		}
		byID[bundle.Metadata.ID] = bundle
	}

	assertBuiltinBundle(t, byID, "vulnerabilities-xss")
	assertBuiltinBundle(t, byID, "api-security-testing")

	for _, prunedID := range []string{
		"cyberstrike-eino-demo",
		"security-awareness-training",
		"incident-response",
		"security-automation",
		"coordination-root-agent",
		"scan-modes-quick",
		"scan-modes-standard",
		"scan-modes-deep",
		"tooling-agent-browser",
		"tooling-python",
		"sql-injection-testing",
		"xss-testing",
		"ssrf-testing",
		"csrf-testing",
		"idor-testing",
		"file-upload-testing",
		"business-logic-testing",
		"command-injection-testing",
		"secure-code-review",
		"vulnerability-assessment",
		"cyberstrikeai-api-security-testing",
		"strix-vulnerabilities-xss",
	} {
		if _, ok := byID[prunedID]; ok {
			t.Fatalf("pruned builtin skill %q should not be bundled", prunedID)
		}
	}
}

func TestBuiltinBundlesAreEnglishOnly(t *testing.T) {
	bundles, err := skill.BuiltinBundles()
	if err != nil {
		t.Fatalf("load builtin bundles: %v", err)
	}
	for _, bundle := range bundles {
		for path, content := range bundle.Files {
			for _, r := range content {
				if unicode.Is(unicode.Han, r) {
					t.Fatalf("builtin bundle %q file %q contains non-English Han character %q", bundle.Metadata.ID, path, r)
				}
			}
		}
	}
}

func TestInstallBuiltinSkillsSeedsMissingBundlesWithoutOverwritingUserEdits(t *testing.T) {
	db := openTestStore(t)
	svc := skill.NewService(db, filepath.Join(t.TempDir(), "skills"))
	ctx := context.Background()

	if err := svc.InstallBuiltinSkills(ctx); err != nil {
		t.Fatalf("install builtins: %v", err)
	}
	seeded, err := svc.Get("vulnerabilities-xss")
	if err != nil {
		t.Fatalf("get seeded builtin: %v", err)
	}
	if seeded.Source.Kind != "builtin" || seeded.Source.Package != "" || seeded.Source.Ref != "" || seeded.Source.SourceURL != "" {
		t.Fatalf("unexpected builtin provenance: %#v", seeded.Source)
	}

	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{ID: "vulnerabilities-xss", Name: "User Edited XSS"},
		Files:    map[string]string{"SKILL.md": "# user edit"},
	}); err != nil {
		t.Fatalf("user edit builtin skill: %v", err)
	}
	if err := svc.InstallBuiltinSkills(ctx); err != nil {
		t.Fatalf("reinstall builtins: %v", err)
	}
	files, err := svc.Files("vulnerabilities-xss")
	if err != nil {
		t.Fatalf("read edited builtin files: %v", err)
	}
	if files["SKILL.md"] != "# user edit" {
		t.Fatalf("builtin reinstall overwrote user edit: %q", files["SKILL.md"])
	}
}

func TestInstallBuiltinSkillsSanitizesOldBuiltinSourceDetails(t *testing.T) {
	db := openTestStore(t)
	skillsRoot := filepath.Join(t.TempDir(), "skills")
	svc := skill.NewService(db, skillsRoot)
	ctx := context.Background()
	expected := builtinBundleByID(t, "api-security-testing").Metadata

	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{
			ID:          "cyberstrikeai-api-security-testing",
			Name:        "cyberstrikeai-api-security-testing",
			Description: "API安全测试的专业技能和方法论",
			Source: skill.SourceProvenance{
				Kind:      "builtin",
				Package:   "Ed1s0nZ/CyberStrikeAI",
				Ref:       "old-commit",
				SourceURL: "https://github.com/Ed1s0nZ/CyberStrikeAI",
			},
		},
		Files: map[string]string{
			"SKILL.md":    "# user edit",
			"UPSTREAM.md": "old source details",
		},
	}); err != nil {
		t.Fatalf("publish old builtin: %v", err)
	}

	if err := svc.InstallBuiltinSkills(ctx); err != nil {
		t.Fatalf("install builtins: %v", err)
	}
	got, err := svc.Get("api-security-testing")
	if err != nil {
		t.Fatalf("get sanitized builtin: %v", err)
	}
	if _, err := svc.Get("cyberstrikeai-api-security-testing"); err == nil {
		t.Fatalf("expected legacy source-prefixed builtin ID to be removed")
	}
	if got.Source.Kind != "builtin" || got.Source.Package != "" || got.Source.Ref != "" || got.Source.SourceURL != "" {
		t.Fatalf("expected old builtin source details to be hidden, got %#v", got.Source)
	}
	if got.Name != expected.Name || got.Description != expected.Description {
		t.Fatalf("expected builtin metadata to be refreshed from embedded bundle, got name=%q description=%q", got.Name, got.Description)
	}
	files, err := svc.Files("api-security-testing")
	if err != nil {
		t.Fatalf("read sanitized files: %v", err)
	}
	if files["SKILL.md"] != "# user edit" {
		t.Fatalf("sanitize overwrote user edit: %q", files["SKILL.md"])
	}
	if _, err := os.Stat(filepath.Join(skillsRoot, "bundles", "cyberstrikeai-api-security-testing")); !os.IsNotExist(err) {
		t.Fatalf("expected legacy source-prefixed bundle folder to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(skillsRoot, "bundles", "api-security-testing", "SKILL.md")); err != nil {
		t.Fatalf("expected source-free builtin bundle folder, stat err=%v", err)
	}
	if _, ok := files["UPSTREAM.md"]; ok {
		t.Fatalf("sanitize should remove old UPSTREAM.md, got %#v", files)
	}
}

func builtinBundleByID(t *testing.T, id string) skill.ImportedBundle {
	t.Helper()
	bundles, err := skill.BuiltinBundles()
	if err != nil {
		t.Fatalf("load builtin bundles: %v", err)
	}
	for _, bundle := range bundles {
		if bundle.Metadata.ID == id {
			return bundle
		}
	}
	t.Fatalf("missing builtin bundle %q", id)
	return skill.ImportedBundle{}
}

func TestInstallBuiltinSkillsRepairsMissingBuiltinBundleFiles(t *testing.T) {
	db := openTestStore(t)
	skillsRoot := filepath.Join(t.TempDir(), "skills")
	svc := skill.NewService(db, skillsRoot)
	ctx := context.Background()

	if err := os.MkdirAll(filepath.Join(skillsRoot, "bundles", "vulnerabilities-xss"), 0o700); err != nil {
		t.Fatalf("create stale bundle dir: %v", err)
	}
	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{
			ID:     "vulnerabilities-xss",
			Name:   "XSS",
			Source: skill.SourceProvenance{Kind: "builtin"},
		},
		Files: map[string]string{"SKILL.md": "# stale"},
	}); err != nil {
		t.Fatalf("publish stale builtin: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(skillsRoot, "bundles", "vulnerabilities-xss")); err != nil {
		t.Fatalf("remove stale bundle files: %v", err)
	}

	if err := svc.InstallBuiltinSkills(ctx); err != nil {
		t.Fatalf("install builtins: %v", err)
	}
	files, err := svc.Files("vulnerabilities-xss")
	if err != nil {
		t.Fatalf("expected missing builtin bundle files to be repaired: %v", err)
	}
	if files["SKILL.md"] == "" {
		t.Fatalf("expected repaired builtin SKILL.md, got %#v", files)
	}
}

func assertBuiltinBundle(t *testing.T, bundles map[string]skill.ImportedBundle, id string) skill.ImportedBundle {
	t.Helper()
	bundle, ok := bundles[id]
	if !ok {
		t.Fatalf("missing builtin bundle %q", id)
	}
	if bundle.Files["SKILL.md"] == "" {
		t.Fatalf("builtin bundle %q has no SKILL.md", id)
	}
	if bundle.Metadata.Source.Kind != "builtin" || bundle.Metadata.Source.Package != "" || bundle.Metadata.Source.Ref != "" || bundle.Metadata.Source.SourceURL != "" {
		t.Fatalf("builtin bundle %q provenance = %#v", id, bundle.Metadata.Source)
	}
	if _, ok := bundle.Files["UPSTREAM.md"]; ok {
		t.Fatalf("builtin bundle %q should not expose upstream source files", id)
	}
	if bundle.Metadata.Name == "" {
		t.Fatalf("builtin bundle %q has no display name", id)
	}
	return bundle
}
