package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CommandRunner abstracts running an external command so the importer can be
// tested without spawning npx. The production implementation is execRunner.
type CommandRunner interface {
	// Run executes cmd in dir, returning combined stdout/stderr. A non-zero exit
	// returns the output along with the error so callers can surface it.
	Run(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if exitErr, ok := err.(*exec.ExitError); ok {
		// Attach stderr to the returned bytes for a useful error message.
		return append(out, exitErr.Stderr...), err
	}
	return out, err
}

// NPXSkillsImporter is the production controlled importer. It delegates skill
// resolution to the `skills` CLI (`npx skills add`), which handles well-known
// skill URLs, GitHub owner/repo shorthand, and github.com URLs. Callers never
// supply a shell command; the importer runs a fixed command shape and reads
// the installed bundle files back into an ImportedBundle.
//
// The CLI installs bundles under a universal agent directory
// (.agents/skills/<name>/) and records source provenance in skills-lock.json;
// this importer reads both back rather than trusting the request fields.
type NPXSkillsImporter struct {
	// Runner runs npx. nil means execRunner (real exec).
	Runner CommandRunner
	// Binary is the npx binary path. Empty means "npx".
	Binary string
}

func (i NPXSkillsImporter) runner() CommandRunner {
	if i.Runner != nil {
		return i.Runner
	}
	return execRunner{}
}

func (i NPXSkillsImporter) binary() string {
	if b := strings.TrimSpace(i.Binary); b != "" {
		return b
	}
	return "npx"
}

// skillsLock mirrors the subset of skills-lock.json the importer needs.
type skillsLock struct {
	Skills map[string]skillsLockEntry `json:"skills"`
}

type skillsLockEntry struct {
	Source          string `json:"source"`
	SourceURL       string `json:"sourceUrl"`
	SourceType      string `json:"sourceType"`
	SkillPath       string `json:"skillPath,omitempty"`
	ComputedHash    string `json:"computedHash,omitempty"`
	WellKnownDigest string `json:"wellKnownDigest,omitempty"`
}

// universalSkillsDir is the agent-agnostic directory the skills CLI writes to
// when invoked with --agent amp. Bundles land at <workdir>/.agents/skills/<name>.
const universalSkillsDir = ".agents/skills"

// ImportSkill resolves and downloads a skill bundle from the request source.
// It requires a non-empty SourceURL; Package and Ref are ignored. For sources
// that contain multiple skills, it publishes the first one (use owner/repo@name
// to target a specific skill); the skills-lock.json is still consulted for
// provenance of the selected skill.
func (i NPXSkillsImporter) ImportSkill(ctx context.Context, request ImportRequest) (ImportedBundle, error) {
	sourceURL := strings.TrimSpace(request.SourceURL)
	if sourceURL == "" {
		return ImportedBundle{}, fmt.Errorf("%w: source_url is required", ErrInvalidSkill)
	}
	workdir, err := os.MkdirTemp("", "skill-import-*")
	if err != nil {
		return ImportedBundle{}, fmt.Errorf("create skill import workdir: %w", err)
	}
	defer os.RemoveAll(workdir)

	args := []string{
		"--yes", "skills", "add", sourceURL,
		"--skill", "*", "--agent", "amp",
		"--yes", "--copy",
	}
	if out, err := i.runner().Run(ctx, workdir, i.binary(), args...); err != nil {
		hint := strings.TrimSpace(string(out))
		if hint == "" {
			hint = err.Error()
		}
		return ImportedBundle{}, fmt.Errorf("%w: npx skills add failed: %s", ErrInvalidSkill, hint)
	}

	lock, err := readSkillsLock(workdir)
	if err != nil {
		return ImportedBundle{}, err
	}
	target, ok := selectInstalledSkill(lock, sourceURL)
	if !ok {
		return ImportedBundle{}, fmt.Errorf("%w: no skill was installed from %q", ErrInvalidSkill, sourceURL)
	}
	files, err := readBundleFiles(filepath.Join(workdir, universalSkillsDir, target))
	if err != nil {
		return ImportedBundle{}, err
	}
	instruction, ok := files["SKILL.md"]
	if !ok {
		return ImportedBundle{}, fmt.Errorf("%w: installed bundle for %q has no SKILL.md", ErrInvalidSkill, target)
	}
	name, description := parseSkillFrontMatter(instruction)
	if strings.TrimSpace(name) == "" {
		name = target
	}
	entry := lock.Skills[target]
	sourceKind := entry.SourceType
	if strings.TrimSpace(sourceKind) == "" {
		sourceKind = strings.TrimSpace(request.SourceKind)
	}
	return ImportedBundle{
		Metadata: Metadata{
			ID:          target,
			Name:        name,
			Description: description,
			Source: SourceProvenance{
				Kind:      sourceKind,
				SourceURL: entry.SourceURL,
				Package:   entry.Source,
				Ref:       entry.WellKnownDigest,
			},
		},
		Files: files,
	}, nil
}

func readSkillsLock(workdir string) (skillsLock, error) {
	raw, err := os.ReadFile(filepath.Join(workdir, "skills-lock.json"))
	if err != nil {
		return skillsLock{}, fmt.Errorf("%w: read skills-lock.json: %v", ErrInvalidSkill, err)
	}
	var lock skillsLock
	if err := json.Unmarshal(raw, &lock); err != nil {
		return skillsLock{}, fmt.Errorf("%w: decode skills-lock.json: %v", ErrInvalidSkill, err)
	}
	if len(lock.Skills) == 0 {
		return skillsLock{}, fmt.Errorf("%w: skills-lock.json lists no installed skills", ErrInvalidSkill)
	}
	return lock, nil
}

// selectInstalledSkill picks the installed skill to publish. If the source URL
// carries an @filter (owner/repo@name), prefer that skill; otherwise prefer the
// skill whose lock sourceUrl matches the request, falling back to the first.
func selectInstalledSkill(lock skillsLock, sourceURL string) (string, bool) {
	if at := strings.LastIndex(sourceURL, "@"); at > 0 {
		filter := sourceURL[at+1:]
		if filter != "" {
			for name := range lock.Skills {
				if name == filter {
					return name, true
				}
			}
		}
	}
	for name, entry := range lock.Skills {
		if entry.SourceURL == sourceURL {
			return name, true
		}
	}
	for name := range lock.Skills {
		return name, true
	}
	return "", false
}

// readBundleFiles reads every regular file under root, keyed by path relative
// to root, reusing the bundle path-safety validation.
func readBundleFiles(root string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: bundle must not contain symlink %s", ErrInvalidSkill, path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if err := ValidateRelativeBundlePath(rel); err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read skill file %q: %w", rel, err)
		}
		files[rel] = string(content)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
