package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	Runner CommandRunner
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

type skillsLock struct {
	Skills map[string]skillsLockEntry `json:"skills"`
}

type skillsLockEntry struct {
	Source          string `json:"source"`
	SourceURL       string `json:"sourceUrl"`
	SourceType      string `json:"sourceType"`
	Ref             string `json:"ref,omitempty"`
	SkillPath       string `json:"skillPath,omitempty"`
	ComputedHash    string `json:"computedHash,omitempty"`
	WellKnownDigest string `json:"wellKnownDigest,omitempty"`
}

// universalSkillsDir is the agent-agnostic directory the skills CLI writes to
// when invoked with --agent amp. Bundles land at <workdir>/.agents/skills/<name>.
const universalSkillsDir = ".agents/skills"

// importShorthandPattern matches the slash-separated shorthand the skills CLI
// resolves: a well-known name or owner/repo path. Segments start alphanumeric so
// local paths, option-shaped strings, and other argv-hostile input cannot pass.
var importShorthandPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*(/[A-Za-z0-9][A-Za-z0-9_.-]*)*$`)

var importFilterPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

var importVersionPinPattern = regexp.MustCompile(`^v?\d+(\.\d+)+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
var importCommitSHAPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

func isVersionPin(value string) bool {
	return importVersionPinPattern.MatchString(value) || importCommitSHAPattern.MatchString(value)
}

func sourceSelectsRevision(source string) bool {
	if strings.Contains(source, "#") {
		return true
	}
	if strings.Contains(source, "/tree/") || strings.Contains(source, "/-/tree/") {
		return true
	}
	if at := strings.LastIndex(source, "@"); at > 0 {
		return isVersionPin(source[at+1:])
	}
	return false
}

func sourceSupportsRefSelection(source string) bool {
	if !strings.Contains(source, "://") {
		return strings.Contains(source, "/")
	}
	parsed, err := url.Parse(source)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "github.com" || host == "gitlab.com" || strings.HasSuffix(strings.ToLower(parsed.Path), ".git")
}

// validateImportSource reports whether source is a shape the fixed
// `npx skills add` command may resolve: an https:// URL, or a shorthand such as
// owner/repo or a well-known name with an optional @skill filter. Local paths,
// non-HTTPS schemes, and option-shaped strings are rejected so the importer can
// never redirect the fixed command shape at local files or inject options.
func validateImportSource(source string) error {
	if source == "" {
		return fmt.Errorf("source_url is required")
	}
	if scheme, rest, ok := strings.Cut(source, "://"); ok {
		if !strings.EqualFold(scheme, "https") {
			return fmt.Errorf("source scheme %q is not allowed; use https", scheme)
		}
		if host := strings.TrimSpace(rest); host == "" || strings.HasPrefix(host, "/") {
			return fmt.Errorf("https source must include a host")
		}
		return nil
	}
	if strings.HasPrefix(source, "-") {
		return fmt.Errorf("source must not start with an option dash")
	}
	shorthand := source
	filter := ""
	if at := strings.LastIndex(source, "@"); at > 0 {
		shorthand, filter = source[:at], source[at+1:]
	}
	if !importShorthandPattern.MatchString(shorthand) {
		return fmt.Errorf("source %q is not an https URL or owner/repo shorthand", source)
	}
	if filter != "" && !importFilterPattern.MatchString(filter) {
		return fmt.Errorf("source skill filter %q is invalid", filter)
	}
	return nil
}

func resolveImportSource(request ImportRequest) (string, error) {
	sourceURL := strings.TrimSpace(request.SourceURL)
	packageSource := strings.TrimSpace(request.Package)
	if sourceURL != "" && packageSource != "" {
		return "", fmt.Errorf("package and source_url are ambiguous; supply only one")
	}
	source := sourceURL
	if source == "" {
		source = packageSource
	}
	if err := validateImportSource(source); err != nil {
		return "", err
	}
	if ref := strings.TrimSpace(request.Ref); ref != "" {
		if sourceSelectsRevision(source) {
			return "", fmt.Errorf("source already selects a revision; supply only one ref")
		}
		if !sourceSupportsRefSelection(source) {
			return "", fmt.Errorf("ref is supported only for git sources, not well-known bundles")
		}
		source += "#" + encodeRefFragment(ref)
	}
	return source, nil
}

func (i NPXSkillsImporter) ImportSkill(ctx context.Context, request ImportRequest) (ImportedBundle, error) {
	sourceURL, err := resolveImportSource(request)
	if err != nil {
		return ImportedBundle{}, fmt.Errorf("%w: %v", ErrInvalidSkill, err)
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
				Ref:       firstNonEmpty(entry.Ref, entry.WellKnownDigest),
			},
		},
		Files: files,
	}, nil
}

func encodeRefFragment(ref string) string {
	const hex = "0123456789ABCDEF"
	var encoded strings.Builder
	for i := 0; i < len(ref); i++ {
		b := ref[i]
		unreserved := b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' || b == '.' || b == '_' || b == '~'
		if unreserved {
			encoded.WriteByte(b)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hex[b>>4])
		encoded.WriteByte(hex[b&0x0f])
	}
	return encoded.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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
	if filter := skillFilterFromSource(sourceURL); filter != "" {
		for name := range lock.Skills {
			if name == filter {
				return name, true
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

func skillFilterFromSource(source string) string {
	if hash := strings.Index(source, "#"); hash >= 0 {
		fragment := source[hash+1:]
		if at := strings.LastIndex(fragment, "@"); at >= 0 {
			return fragment[at+1:]
		}
		source = source[:hash]
	}
	if at := strings.LastIndex(source, "@"); at > 0 {
		filter := source[at+1:]
		if !isVersionPin(filter) {
			return filter
		}
	}
	return ""
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
