package skill_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/skill"
)

// fakeRunner materializes a skill bundle into the workdir the importer created,
// mimicking what `npx skills add` would write. Tests configure install to
// describe what gets "installed"; no network is involved.
type fakeRunner struct {
	install  func(dir string) error // writes .agents/skills/<name>/ + skills-lock.json
	exitErr  error                  // when set, Run returns this error and skips install
	recorded []string               // captured argv per Run
}

func (f *fakeRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	f.recorded = append(f.recorded, strings.Join(append([]string{name}, args...), " "))
	if f.exitErr != nil {
		return []byte("simulated npx stderr"), f.exitErr
	}
	if f.install != nil {
		if err := f.install(dir); err != nil {
			return nil, err
		}
	}
	return []byte("installed"), nil
}

// installBundle writes a universal-agent skill bundle plus a skills-lock.json
// into dir, matching the shape produced by the real `skills` CLI.
func installBundle(t *testing.T, dir, name, sourceURL, sourceType string, files map[string]string) {
	t.Helper()
	bundleDir := filepath.Join(dir, ".agents", "skills", name)
	if err := os.MkdirAll(bundleDir, 0o700); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	for rel, content := range files {
		path := filepath.Join(bundleDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	lock := fmt.Sprintf(`{"version":1,"skills":{"%s":{"source":%q,"sourceUrl":%q,"sourceType":%q,"computedHash":"abc"}}}`,
		name, sourceFromURL(sourceURL), sourceURL, sourceType)
	if err := os.WriteFile(filepath.Join(dir, "skills-lock.json"), []byte(lock), 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}
}

func sourceFromURL(sourceURL string) string {
	// Mirror the CLI's "source" provenance field loosely; tests only assert it
	// is preserved, not its exact value.
	if i := strings.Index(sourceURL, "://"); i >= 0 {
		return strings.TrimPrefix(sourceURL, sourceURL[:i+3])
	}
	return sourceURL
}

const sampleSkillMD = "---\nname: arena-helper\ndescription: NSSCTF arena helper.\n---\n\n# Arena Helper\n"

func TestImportRequiresSourceURL(t *testing.T) {
	imp := skill.NPXSkillsImporter{Runner: &fakeRunner{}}
	_, err := imp.ImportSkill(context.Background(), skill.ImportRequest{SourceKind: "well-known"})
	if !errors.Is(err, skill.ErrInvalidSkill) {
		t.Fatalf("expected ErrInvalidSkill for empty source, got %v", err)
	}
}

func TestImportRunsSkillsAddAndPublishesBundle(t *testing.T) {
	runner := &fakeRunner{
		install: func(dir string) error {
			installBundle(t, dir, "arena-helper",
				"https://www.nssctf.cn/skills/@nssctf/nssctf-agent-arena",
				"well-known",
				map[string]string{
					"SKILL.md":             sampleSkillMD,
					"references/api.md":    "# API\n",
					"references/dialog.md": "# Dialog\n",
				})
			return nil
		},
	}
	imp := skill.NPXSkillsImporter{Runner: runner}

	bundle, err := imp.ImportSkill(context.Background(), skill.ImportRequest{
		SourceKind: "well-known",
		SourceURL:  "https://www.nssctf.cn/skills/@nssctf/nssctf-agent-arena",
	})
	if err != nil {
		t.Fatalf("ImportSkill: %v", err)
	}

	// The importer must invoke the real CLI command shape, not the old broken
	// `skills import` subcommand.
	if len(runner.recorded) != 1 {
		t.Fatalf("expected exactly one npx invocation, got %d", len(runner.recorded))
	}
	cmd := runner.recorded[0]
	if !strings.Contains(cmd, "skills add") {
		t.Fatalf("expected `skills add` invocation, got: %s", cmd)
	}
	if strings.Contains(cmd, "skills import") {
		t.Fatalf("must not use the non-existent `skills import` subcommand: %s", cmd)
	}
	if !strings.Contains(cmd, "--agent amp") || !strings.Contains(cmd, "--copy") {
		t.Fatalf("expected --agent amp --copy flags, got: %s", cmd)
	}

	if bundle.Metadata.ID != "arena-helper" {
		t.Fatalf("id = %q, want arena-helper", bundle.Metadata.ID)
	}
	if bundle.Metadata.Name != "arena-helper" {
		t.Fatalf("name = %q (from front matter)", bundle.Metadata.Name)
	}
	if bundle.Metadata.Description != "NSSCTF arena helper." {
		t.Fatalf("description = %q", bundle.Metadata.Description)
	}
	if bundle.Files["SKILL.md"] != sampleSkillMD {
		t.Fatalf("SKILL.md not preserved")
	}
	if bundle.Files["references/api.md"] != "# API\n" {
		t.Fatalf("nested reference not preserved, got %#v", bundle.Files)
	}
	if bundle.Metadata.Source.Kind != "well-known" {
		t.Fatalf("source kind = %q, want well-known (from lock)", bundle.Metadata.Source.Kind)
	}
	if bundle.Metadata.Source.SourceURL != "https://www.nssctf.cn/skills/@nssctf/nssctf-agent-arena" {
		t.Fatalf("source url not recorded from lock: %q", bundle.Metadata.Source.SourceURL)
	}
}

func TestImportSelectsSkillViaAtFilter(t *testing.T) {
	// Multi-skill source: install two skills, request the second via @filter.
	runner := &fakeRunner{
		install: func(dir string) error {
			installBundle(t, dir, "recon",
				"mattpocock/skills@recon", "github",
				map[string]string{"SKILL.md": "---\nname: recon\ndescription: recon skill.\n---\n# recon\n"})
			installBundle(t, dir, "web-tool",
				"mattpocock/skills@web-tool", "github",
				map[string]string{"SKILL.md": "---\nname: web-tool\ndescription: web skill.\n---\n# web\n"})
			// Overwrite the lock with both entries.
			lock := `{"version":1,"skills":{` +
				`"recon":{"source":"mattpocock/skills","sourceUrl":"mattpocock/skills","sourceType":"github","skillPath":"skills/recon/SKILL.md"},` +
				`"web-tool":{"source":"mattpocock/skills","sourceUrl":"mattpocock/skills","sourceType":"github","skillPath":"skills/web-tool/SKILL.md"}}}`
			return os.WriteFile(filepath.Join(dir, "skills-lock.json"), []byte(lock), 0o600)
		},
	}
	imp := skill.NPXSkillsImporter{Runner: runner}

	bundle, err := imp.ImportSkill(context.Background(), skill.ImportRequest{
		SourceKind: "git",
		SourceURL:  "mattpocock/skills@web-tool",
	})
	if err != nil {
		t.Fatalf("ImportSkill: %v", err)
	}
	if bundle.Metadata.ID != "web-tool" {
		t.Fatalf("expected @filter to select web-tool, got %q", bundle.Metadata.ID)
	}
}

func TestImportSurfacesNpxFailure(t *testing.T) {
	runner := &fakeRunner{exitErr: errors.New("exit status 1")}
	imp := skill.NPXSkillsImporter{Runner: runner}

	_, err := imp.ImportSkill(context.Background(), skill.ImportRequest{
		SourceKind: "well-known",
		SourceURL:  "https://example.com/skills/@acme/missing",
	})
	if !errors.Is(err, skill.ErrInvalidSkill) {
		t.Fatalf("expected ErrInvalidSkill on npx failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "npx skills add failed") {
		t.Fatalf("expected error to wrap npx failure, got %q", err.Error())
	}
}

func TestImportRejectsMissingLockFile(t *testing.T) {
	// install() writes the bundle but no skills-lock.json.
	runner := &fakeRunner{
		install: func(dir string) error {
			bundleDir := filepath.Join(dir, ".agents", "skills", "x")
			if err := os.MkdirAll(bundleDir, 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(bundleDir, "SKILL.md"), []byte(sampleSkillMD), 0o600)
		},
	}
	imp := skill.NPXSkillsImporter{Runner: runner}

	_, err := imp.ImportSkill(context.Background(), skill.ImportRequest{
		SourceKind: "well-known",
		SourceURL:  "https://example.com/skills/x",
	})
	if !errors.Is(err, skill.ErrInvalidSkill) {
		t.Fatalf("expected ErrInvalidSkill when lock is missing, got %v", err)
	}
}

func TestImportRejectsSymlinkInBundle(t *testing.T) {
	runner := &fakeRunner{
		install: func(dir string) error {
			installBundle(t, dir, "evil", "https://example.com/skills/evil", "well-known",
				map[string]string{"SKILL.md": sampleSkillMD})
			bundleDir := filepath.Join(dir, ".agents", "skills", "evil")
			return os.Symlink("/etc/passwd", filepath.Join(bundleDir, "leak"))
		},
	}
	imp := skill.NPXSkillsImporter{Runner: runner}

	_, err := imp.ImportSkill(context.Background(), skill.ImportRequest{
		SourceKind: "well-known",
		SourceURL:  "https://example.com/skills/evil",
	})
	if !errors.Is(err, skill.ErrInvalidSkill) {
		t.Fatalf("expected ErrInvalidSkill for symlink in bundle, got %v", err)
	}
}

func TestImportRejectsUnsafeSourcesWithoutExecutingNpx(t *testing.T) {
	for _, source := range []string{
		"file:///etc/passwd",         // local file exfiltration
		"http://plain.example/skill", // non-TLS fetch
		"/etc/passwd",                // absolute local path
		"../outside",                 // relative path escape shape
		"-c",                         // option injection into the fixed npx command
		"--skill",                    // option injection, long form
		"git@evil:host/repo",         // unsupported remote syntax
		"owner/repo;rm -rf",          // garbage charset must not reach argv
		"owner//repo",                // empty path segment
	} {
		runner := &fakeRunner{}
		imp := skill.NPXSkillsImporter{Runner: runner}
		_, err := imp.ImportSkill(context.Background(), skill.ImportRequest{SourceURL: source})
		if !errors.Is(err, skill.ErrInvalidSkill) {
			t.Errorf("source %q: expected ErrInvalidSkill, got %v", source, err)
		}
		if len(runner.recorded) != 0 {
			t.Errorf("source %q: importer must reject before executing npx, ran: %v", source, runner.recorded)
		}
	}
}

func TestImportAcceptsHTTPSSourcesAndShorthandSources(t *testing.T) {
	for _, tc := range []struct {
		source    string
		bundle    string
		sourceURL string
	}{
		{source: "https://github.com/owner/repo", bundle: "repo", sourceURL: "https://github.com/owner/repo"},
		{source: "owner/repo", bundle: "repo", sourceURL: "owner/repo"},
		{source: "owner/repo@helper", bundle: "helper", sourceURL: "https://github.com/owner/repo"},
		{source: "pdf", bundle: "pdf", sourceURL: "pdf"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			runner := &fakeRunner{install: func(dir string) error {
				installBundle(t, dir, tc.bundle, tc.sourceURL, "github", map[string]string{"SKILL.md": sampleSkillMD})
				return nil
			}}
			imp := skill.NPXSkillsImporter{Runner: runner}
			bundle, err := imp.ImportSkill(context.Background(), skill.ImportRequest{SourceURL: tc.source})
			if err != nil {
				t.Fatalf("expected %q accepted, got %v", tc.source, err)
			}
			if bundle.Metadata.ID != tc.bundle {
				t.Fatalf("expected bundle %q, got %q", tc.bundle, bundle.Metadata.ID)
			}
		})
	}
}
