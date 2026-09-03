package skill_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"pentest/internal/runtimeprofile"
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

	// The curated builtin set: sandbox tooling discipline the model cannot know
	// from pretraining, the product-integrated scoreboard flow, and the
	// operator-authored benchmark orchestration skill.
	want := []string{
		"scoreboard-driven-web-challenge",
		"ctf-orchestrator",
		"tooling-ffuf",
		"tooling-httpx",
		"tooling-katana",
		"tooling-naabu",
		"tooling-nmap",
		"tooling-nuclei",
		"tooling-sqlmap",
		"tooling-subfinder",
	}
	for _, id := range want {
		assertBuiltinBundle(t, byID, id)
	}
	wantSet := map[string]bool{}
	for _, id := range want {
		wantSet[id] = true
	}
	extra := []string{}
	for id := range byID {
		if !wantSet[id] {
			extra = append(extra, id)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		t.Fatalf("builtin bundle set should contain exactly %d bundles; unexpected extras: %v", len(want), extra)
	}
	if len(byID) != len(want) {
		t.Fatalf("builtin bundle set should contain exactly %d bundles, got %d", len(want), len(byID))
	}
}

// TestBuiltinBundlesAreValidUTF8Text guards bundle file encodings: every file
// must be valid UTF-8 (files are read as strings by the daemon) and free of
// control characters other than the ordinary text whitespace. Bundles are not
// restricted to English; vendored skills may carry localized content.
func TestBuiltinBundlesAreValidUTF8Text(t *testing.T) {
	bundles, err := skill.BuiltinBundles()
	if err != nil {
		t.Fatalf("load builtin bundles: %v", err)
	}
	for _, bundle := range bundles {
		for path, content := range bundle.Files {
			if !utf8.ValidString(content) {
				t.Fatalf("builtin bundle %q file %q is not valid UTF-8", bundle.Metadata.ID, path)
			}
			for _, r := range content {
				// Vendored security payloads legitimately embed control bytes
				// (NUL injection, JPEG/PNG magic bytes, DLE/SUB in crafted
				// requests), so only reject ASCII control characters that make
				// a text document unreadable and are never payload content:
				// BEL, BS, FF, VT, ESC, and DEL.
				switch r {
				case '\a', '\b', '\f', '\v', 0x1b, 0x7f:
					t.Fatalf("builtin bundle %q file %q contains control character U+%04X", bundle.Metadata.ID, path, r)
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
	seeded, err := svc.Get("tooling-nmap")
	if err != nil {
		t.Fatalf("get seeded builtin: %v", err)
	}
	if seeded.Source.Kind != "builtin" || seeded.Source.Package != "" || seeded.Source.Ref != "" || seeded.Source.SourceURL != "" {
		t.Fatalf("unexpected builtin provenance: %#v", seeded.Source)
	}

	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{ID: "tooling-nmap", Name: "User Edited XSS"},
		Files:    map[string]string{"SKILL.md": "# user edit"},
	}); err != nil {
		t.Fatalf("user edit builtin skill: %v", err)
	}
	if err := svc.InstallBuiltinSkills(ctx); err != nil {
		t.Fatalf("reinstall builtins: %v", err)
	}
	files, err := svc.Files("tooling-nmap")
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
	expected := builtinBundleByID(t, "tooling-nmap").Metadata

	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{
			ID:          "cyberstrikeai-tooling-nmap",
			Name:        "cyberstrikeai-tooling-nmap",
			Description: "XSS testing",
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
	got, err := svc.Get("tooling-nmap")
	if err != nil {
		t.Fatalf("get sanitized builtin: %v", err)
	}
	if _, err := svc.Get("cyberstrikeai-tooling-nmap"); err == nil {
		t.Fatalf("expected legacy source-prefixed builtin ID to be removed")
	}
	if got.Source.Kind != "builtin" || got.Source.Package != "" || got.Source.Ref != "" || got.Source.SourceURL != "" {
		t.Fatalf("expected old builtin source details to be hidden, got %#v", got.Source)
	}
	if got.Name != expected.Name || got.Description != expected.Description {
		t.Fatalf("expected builtin metadata to be refreshed from embedded bundle, got name=%q description=%q", got.Name, got.Description)
	}
	files, err := svc.Files("tooling-nmap")
	if err != nil {
		t.Fatalf("read sanitized files: %v", err)
	}
	if files["SKILL.md"] != "# user edit" {
		t.Fatalf("sanitize overwrote user edit: %q", files["SKILL.md"])
	}
	if _, err := os.Stat(filepath.Join(skillsRoot, "bundles", "cyberstrikeai-tooling-nmap")); !os.IsNotExist(err) {
		t.Fatalf("expected legacy source-prefixed bundle folder to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(skillsRoot, "bundles", "tooling-nmap", "SKILL.md")); err != nil {
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

func TestInstallBuiltinSkillsPurgesRetiredVendoredBuiltinIDs(t *testing.T) {
	db := openTestStore(t)
	skillsRoot := filepath.Join(t.TempDir(), "skills")
	svc := skill.NewService(db, skillsRoot)
	ctx := context.Background()
	profile, err := runtimeprofile.NewService(db).Create("fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{
			ID:     "reverse-skill-router",
			Name:   "reverse-skill-router",
			Source: skill.SourceProvenance{Kind: "builtin"},
		},
		Files: map[string]string{"SKILL.md": "# vendored"},
	}); err != nil {
		t.Fatalf("publish retired vendored builtin: %v", err)
	}
	if err := svc.SetOptOut(profile.ID, "reverse-skill-router", true); err != nil {
		t.Fatalf("set retired Profile Skill Opt-Out: %v", err)
	}
	if err := svc.SetGlobalOptOut("reverse-skill-router", true); err != nil {
		t.Fatalf("set retired Global Skill Opt-Out: %v", err)
	}
	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{
			ID:     "business-logic-testing",
			Name:   "business-logic-testing",
			Source: skill.SourceProvenance{Kind: "builtin"},
		},
		Files: map[string]string{"SKILL.md": "# retired successor-less legacy"},
	}); err != nil {
		t.Fatalf("publish retired successor-less legacy builtin: %v", err)
	}

	if err := svc.InstallBuiltinSkills(ctx); err != nil {
		t.Fatalf("install builtins: %v", err)
	}
	for _, retiredID := range []string{"reverse-skill-router", "business-logic-testing"} {
		if _, err := svc.Get(retiredID); err == nil {
			t.Fatalf("expected retired builtin ID %q to be removed", retiredID)
		}
		if _, err := os.Stat(filepath.Join(skillsRoot, "bundles", retiredID)); !os.IsNotExist(err) {
			t.Fatalf("expected retired bundle folder %q to be removed, stat err=%v", retiredID, err)
		}
	}
	enabled, err := svc.EnabledSkills(profile.ID)
	if err != nil {
		t.Fatalf("list enabled skills: %v", err)
	}
	for _, enabledSkill := range enabled {
		if enabledSkill.ID == "reverse-skill-router" || enabledSkill.ID == "business-logic-testing" {
			t.Fatalf("expected retired builtin %q to stay out of enabled skills", enabledSkill.ID)
		}
	}
}

func TestInstallBuiltinSkillsPurgesRetiredStrixPrefixedLegacyIDs(t *testing.T) {
	db := openTestStore(t)
	skillsRoot := filepath.Join(t.TempDir(), "skills")
	svc := skill.NewService(db, skillsRoot)
	ctx := context.Background()

	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{
			ID:     "strix-coordination-root-agent",
			Name:   "root-agent",
			Source: skill.SourceProvenance{Kind: "builtin"},
		},
		Files: map[string]string{"SKILL.md": "# retired"},
	}); err != nil {
		t.Fatalf("publish retired strix legacy builtin: %v", err)
	}
	if err := svc.InstallBuiltinSkills(ctx); err != nil {
		t.Fatalf("install builtins: %v", err)
	}
	if _, err := svc.Get("strix-coordination-root-agent"); err == nil {
		t.Fatal("expected retired strix-prefixed legacy builtin ID to be removed")
	}
}

func TestInstallBuiltinSkillsPurgesRetiredPrunedLegacyIDs(t *testing.T) {
	db := openTestStore(t)
	skillsRoot := filepath.Join(t.TempDir(), "skills")
	svc := skill.NewService(db, skillsRoot)
	ctx := context.Background()

	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{
			ID:     "cyberstrikeai-incident-response",
			Name:   "incident-response",
			Source: skill.SourceProvenance{Kind: "builtin"},
		},
		Files: map[string]string{"SKILL.md": "# retired"},
	}); err != nil {
		t.Fatalf("publish retired legacy builtin: %v", err)
	}
	if err := svc.InstallBuiltinSkills(ctx); err != nil {
		t.Fatalf("install builtins: %v", err)
	}
	if _, err := svc.Get("cyberstrikeai-incident-response"); err == nil {
		t.Fatal("expected retired legacy builtin ID to be removed")
	}
}

func TestInstallBuiltinSkillsPurgesRetiredDefaultBuiltinIDs(t *testing.T) {
	db := openTestStore(t)
	skillsRoot := filepath.Join(t.TempDir(), "skills")
	svc := skill.NewService(db, skillsRoot)
	ctx := context.Background()

	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{
			ID:     "api-security-testing",
			Name:   "API Security Testing",
			Source: skill.SourceProvenance{Kind: "builtin"},
		},
		Files: map[string]string{"SKILL.md": "# retired"},
	}); err != nil {
		t.Fatalf("publish retired builtin: %v", err)
	}
	if err := svc.InstallBuiltinSkills(ctx); err != nil {
		t.Fatalf("install builtins: %v", err)
	}
	if _, err := svc.Get("api-security-testing"); err == nil {
		t.Fatal("expected retired default builtin ID to be removed")
	}
}

func TestInstallBuiltinSkillsRepairsMissingBuiltinBundleFiles(t *testing.T) {
	db := openTestStore(t)
	skillsRoot := filepath.Join(t.TempDir(), "skills")
	svc := skill.NewService(db, skillsRoot)
	ctx := context.Background()

	if err := os.MkdirAll(filepath.Join(skillsRoot, "bundles", "tooling-nmap"), 0o700); err != nil {
		t.Fatalf("create stale bundle dir: %v", err)
	}
	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{
			ID:     "tooling-nmap",
			Name:   "XSS",
			Source: skill.SourceProvenance{Kind: "builtin"},
		},
		Files: map[string]string{"SKILL.md": "# stale"},
	}); err != nil {
		t.Fatalf("publish stale builtin: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(skillsRoot, "bundles", "tooling-nmap")); err != nil {
		t.Fatalf("remove stale bundle files: %v", err)
	}

	if err := svc.InstallBuiltinSkills(ctx); err != nil {
		t.Fatalf("install builtins: %v", err)
	}
	files, err := svc.Files("tooling-nmap")
	if err != nil {
		t.Fatalf("expected missing builtin bundle files to be repaired: %v", err)
	}
	if files["SKILL.md"] == "" {
		t.Fatalf("expected repaired builtin SKILL.md, got %#v", files)
	}
}

func TestBuiltinBundlesHaveValidIDsAndRelativePaths(t *testing.T) {
	bundles, err := skill.BuiltinBundles()
	if err != nil {
		t.Fatalf("load builtin bundles: %v", err)
	}
	idPattern := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	for _, bundle := range bundles {
		if !idPattern.MatchString(bundle.Metadata.ID) {
			t.Fatalf("builtin bundle ID %q does not match the skill ID pattern", bundle.Metadata.ID)
		}
		for path := range bundle.Files {
			if path == "SKILL.md" {
				continue
			}
			if err := validateRelativePathForTest(path); err != nil {
				t.Fatalf("builtin bundle %q file %q: %v", bundle.Metadata.ID, path, err)
			}
		}
	}
}

func validateRelativePathForTest(path string) error {
	if strings.TrimSpace(path) == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return fmt.Errorf("path must be relative")
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("path must not escape root")
		}
	}
	return nil
}

// TestBuiltinBundleScriptsAreShellOnly keeps vendored bundles Linux-ready:
// no Windows .ps1 scripts, and any .sh script carries a bash/sh shebang.
func TestBuiltinBundleScriptsAreShellOnly(t *testing.T) {
	bundles, err := skill.BuiltinBundles()
	if err != nil {
		t.Fatalf("load builtin bundles: %v", err)
	}
	for _, bundle := range bundles {
		for path, content := range bundle.Files {
			if strings.HasSuffix(path, ".ps1") {
				t.Fatalf("builtin bundle %q contains Windows-only script %q", bundle.Metadata.ID, path)
			}
			if strings.HasSuffix(path, ".sh") && !strings.HasPrefix(strings.TrimSpace(content), "#!/") {
				t.Fatalf("builtin bundle %q script %q is missing a shebang", bundle.Metadata.ID, path)
			}
		}
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
