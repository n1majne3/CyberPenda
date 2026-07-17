package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pentest/internal/modelprovider"
	"pentest/internal/runtimeprofile"
)

// ErrUnsafeBlackboardV2Layout is returned when a v2 task layout fails confined
// validation before any credential or Runtime projection.
var ErrUnsafeBlackboardV2Layout = errors.New("unsafe Blackboard v2 task layout")

// ErrUnsafeCodexV2Layout remains for callers that still check the Codex-era name.
var ErrUnsafeCodexV2Layout = ErrUnsafeBlackboardV2Layout

// PrepareCodexV2TaskLayout is the Codex specialization of the shared v2 layout
// validator.
func PrepareCodexV2TaskLayout(rootDir, taskID string) (Layout, error) {
	return PrepareBlackboardV2TaskLayout(rootDir, taskID, runtimeprofile.ProviderCodex)
}

// PrepareBlackboardV2TaskLayout validates every existing task-local directory
// before creating any missing directory. V2 launch preparation uses this
// before projecting config, credentials, or Runtime context.
func PrepareBlackboardV2TaskLayout(rootDir, taskID string, provider runtimeprofile.Provider) (Layout, error) {
	if strings.TrimSpace(rootDir) == "" {
		return Layout{}, fmt.Errorf("%w: runner root is required", ErrUnsafeBlackboardV2Layout)
	}
	if !safeBlackboardV2PathComponent(taskID) {
		return Layout{}, fmt.Errorf("%w: invalid task path component", ErrUnsafeBlackboardV2Layout)
	}
	providerDir := providerHomeDir(provider)
	if providerDir == "" {
		return Layout{}, fmt.Errorf("%w: runtime provider is required", ErrUnsafeBlackboardV2Layout)
	}
	rootDir = filepath.Clean(rootDir)
	if info, err := os.Lstat(rootDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return Layout{}, fmt.Errorf("%w: Runtime Root is not a real directory", ErrUnsafeBlackboardV2Layout)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Layout{}, fmt.Errorf("%w: inspect Runtime Root: %v", ErrUnsafeBlackboardV2Layout, err)
	} else if err := os.MkdirAll(rootDir, 0o700); err != nil {
		return Layout{}, fmt.Errorf("%w: prepare Runtime Root: %v", ErrUnsafeBlackboardV2Layout, err)
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return Layout{}, fmt.Errorf("%w: open Runtime Root: %v", ErrUnsafeBlackboardV2Layout, err)
	}
	defer root.Close()
	layout := blackboardV2Layout(rootDir, taskID, providerDir)
	relatives := []string{
		taskID,
		filepath.Join(taskID, "workdir"),
		filepath.Join(taskID, "runtime-home"),
		filepath.Join(taskID, "runtime-home", providerDir),
		filepath.Join(taskID, "skills"),
		filepath.Join(taskID, "artifacts"),
		filepath.Join(taskID, "logs"),
		filepath.Join(taskID, "workdir", ".pentest"),
	}
	if provider == runtimeprofile.ProviderPi {
		relatives = append(relatives, filepath.Join(taskID, "runtime-home", providerDir, "agent"))
	}
	for _, relative := range relatives {
		if err := validateExistingBlackboardV2Directory(root, relative); err != nil {
			return Layout{}, fmt.Errorf("%w: %v", ErrUnsafeBlackboardV2Layout, err)
		}
	}
	rejectRelatives := []string{
		filepath.Join(taskID, "workdir", "AGENTS.md"),
		filepath.Join(taskID, "workdir", "CLAUDE.md"),
		filepath.Join(taskID, "workdir", ".pentest", "scope.json"),
		filepath.Join(taskID, "workdir", ".pentest", "blackboard.json"),
		filepath.Join(taskID, "workdir", ".mcp.json"),
	}
	switch provider {
	case runtimeprofile.ProviderCodex:
		rejectRelatives = append(rejectRelatives,
			filepath.Join(taskID, "runtime-home", providerDir, "config.toml"),
			filepath.Join(taskID, "runtime-home", providerDir, "auth.json"),
		)
	case runtimeprofile.ProviderClaudeCode:
		rejectRelatives = append(rejectRelatives,
			filepath.Join(taskID, "runtime-home", providerDir, "settings.json"),
		)
	case runtimeprofile.ProviderPi:
		rejectRelatives = append(rejectRelatives,
			filepath.Join(taskID, "runtime-home", providerDir, "agent", "mcp.json"),
			filepath.Join(taskID, "runtime-home", providerDir, "agent", "models.json"),
			filepath.Join(taskID, "runtime-home", providerDir, "agent", "auth.json"),
		)
	}
	for _, relative := range rejectRelatives {
		if err := rejectBlackboardV2Symlink(root, relative); err != nil {
			return Layout{}, fmt.Errorf("%w: %v", ErrUnsafeBlackboardV2Layout, err)
		}
	}
	for _, relative := range relatives {
		directory, err := openBlackboardV2Directory(root, relative)
		if err != nil {
			return Layout{}, fmt.Errorf("%w: %v", ErrUnsafeBlackboardV2Layout, err)
		}
		_ = directory.Close()
	}
	return layout, nil
}

// ProjectCodexV2RuntimeConfig projects Codex config without ever constructing
// the legacy identity context. Credential lookup retains its Project binding,
// but the generic Codex writer receives no IDs, daemon address, or auth token.
func ProjectCodexV2RuntimeConfig(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest) (ConfigProjection, error) {
	return ProjectBlackboardV2RuntimeConfig(layout, profile, req)
}

// ProjectBlackboardV2RuntimeConfig projects provider config for a Blackboard v2
// Continuation without legacy identity context files. Claude and Pi retain the
// trusted MCP grant URL; Codex remains networkless for Project Interface writes.
func ProjectBlackboardV2RuntimeConfig(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest) (ConfigProjection, error) {
	switch profile.Provider {
	case runtimeprofile.ProviderCodex:
		return projectCodexV2RuntimeConfig(layout, profile, req)
	case runtimeprofile.ProviderClaudeCode:
		return projectClaudeV2RuntimeConfig(layout, profile, req)
	case runtimeprofile.ProviderPi:
		return projectPiV2RuntimeConfig(layout, profile, req)
	default:
		return ConfigProjection{}, fmt.Errorf("Blackboard v2 projection is unsupported for provider %q", profile.Provider)
	}
}

func projectCodexV2RuntimeConfig(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest) (ConfigProjection, error) {
	if profile.Provider != runtimeprofile.ProviderCodex {
		return ConfigProjection{}, fmt.Errorf("Codex v2 projection requires the Codex provider")
	}
	if err := os.MkdirAll(layout.ProviderHome, 0o700); err != nil {
		return ConfigProjection{}, fmt.Errorf("prepare provider home: %w", err)
	}
	if len(req.SkillBundles) > 0 {
		if err := projectSkillBundles(layout, req.SkillBundles); err != nil {
			return ConfigProjection{}, err
		}
	}
	if req.Sandbox || len(req.SkillBundles) > 0 {
		target := sandboxSkillsImagePath
		if len(req.SkillBundles) > 0 {
			target = layout.SkillsRoot
			if req.Sandbox {
				target = "/task/skills"
			}
		}
		if err := PrepareSandboxSkills(layout, profile.Provider, target); err != nil {
			return ConfigProjection{}, err
		}
	}

	materialized, err := resolveMaterializedCredentials(profile, req)
	if err != nil {
		return ConfigProjection{}, err
	}
	if req.ModelSnapshot != nil && req.ModelSnapshot.APIKeyEnv != "" {
		value := strings.TrimSpace(os.Getenv(req.ModelSnapshot.APIKeyEnv))
		if value == "" {
			if resolved, ok := materializeModelProviderAPIKey(req); ok {
				value = resolved
			}
		}
		if value != "" {
			materialized = map[string]string{"OPENAI_API_KEY": value}
		}
	}
	projectionProfile := profile
	projectionProfile.Fields.CredentialRefs = nil
	projectionProfile.Fields.APIKeys = materialized
	projectionRequest := req
	projectionRequest.ProjectID = ""
	projectionRequest.TaskID = ""
	projectionRequest.DaemonAddr = ""
	projectionRequest.AuthToken = ""
	projectionRequest.Credentials = nil
	projection, err := projectCodexConfig(layout, projectionProfile, projectionRequest)
	if err != nil {
		return ConfigProjection{}, err
	}
	projection.ResolvedProfile = profile
	projection.ModelSnapshot = req.ModelSnapshot
	if err := projectRuntimeExtensions(layout, profile, req, &projection); err != nil {
		return ConfigProjection{}, err
	}
	if len(req.SkillBundles) > 0 {
		addSkillProjectionPreview(&projection, req.SkillBundles, layout)
	}
	return projection, nil
}

func projectClaudeV2RuntimeConfig(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest) (ConfigProjection, error) {
	if profile.Provider != runtimeprofile.ProviderClaudeCode {
		return ConfigProjection{}, fmt.Errorf("Claude v2 projection requires the Claude Code provider")
	}
	if err := prepareBlackboardV2Skills(layout, profile, req); err != nil {
		return ConfigProjection{}, err
	}
	// Identity stays off model-visible context files. DaemonAddr and grant
	// AuthToken remain so the trusted MCP allowlist/projection can fire.
	projectionRequest := req
	projectionRequest.ProjectID = ""
	projectionRequest.TaskID = ""
	projectionRequest.RuntimeContext = nil
	projection, err := projectClaudeSettings(layout, profile, projectionRequest)
	if err != nil {
		return ConfigProjection{}, err
	}
	projection.ResolvedProfile = profile
	projection.ModelSnapshot = req.ModelSnapshot
	if err := projectRuntimeExtensions(layout, profile, req, &projection); err != nil {
		return ConfigProjection{}, err
	}
	if len(req.SkillBundles) > 0 {
		addSkillProjectionPreview(&projection, req.SkillBundles, layout)
	}
	return projection, nil
}

func projectPiV2RuntimeConfig(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest) (ConfigProjection, error) {
	if profile.Provider != runtimeprofile.ProviderPi {
		return ConfigProjection{}, fmt.Errorf("Pi v2 projection requires the Pi provider")
	}
	if err := prepareBlackboardV2Skills(layout, profile, req); err != nil {
		return ConfigProjection{}, err
	}
	projectionRequest := req
	projectionRequest.ProjectID = ""
	projectionRequest.TaskID = ""
	projectionRequest.RuntimeContext = nil
	projection, err := projectPiConfig(layout, profile, projectionRequest)
	if err != nil {
		return ConfigProjection{}, err
	}
	projection.ResolvedProfile = profile
	projection.ModelSnapshot = req.ModelSnapshot
	if err := projectRuntimeExtensions(layout, profile, req, &projection); err != nil {
		return ConfigProjection{}, err
	}
	if len(req.SkillBundles) > 0 {
		addSkillProjectionPreview(&projection, req.SkillBundles, layout)
	}
	return projection, nil
}

func prepareBlackboardV2Skills(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest) error {
	if err := os.MkdirAll(layout.ProviderHome, 0o700); err != nil {
		return fmt.Errorf("prepare provider home: %w", err)
	}
	if len(req.SkillBundles) > 0 {
		if err := projectSkillBundles(layout, req.SkillBundles); err != nil {
			return err
		}
	}
	if req.Sandbox || len(req.SkillBundles) > 0 {
		target := sandboxSkillsImagePath
		if len(req.SkillBundles) > 0 {
			target = layout.SkillsRoot
			if req.Sandbox {
				target = "/task/skills"
			}
		}
		if err := PrepareSandboxSkills(layout, profile.Provider, target); err != nil {
			return err
		}
	}
	return nil
}

// CodexV2ProfileWithModelSnapshot applies the in-memory model resolution that
// config projection would otherwise discover only after touching the layout.
func CodexV2ProfileWithModelSnapshot(profile runtimeprofile.Profile, snapshot modelprovider.Snapshot) runtimeprofile.Profile {
	return profileWithModelSnapshot(profile, snapshot)
}

// BlackboardV2ProfileWithModelSnapshot is the shared name for model snapshot
// application during v2 launch planning.
func BlackboardV2ProfileWithModelSnapshot(profile runtimeprofile.Profile, snapshot modelprovider.Snapshot) runtimeprofile.Profile {
	return profileWithModelSnapshot(profile, snapshot)
}

func blackboardV2Layout(rootDir, taskID, providerDir string) Layout {
	taskRoot := filepath.Join(rootDir, taskID)
	return Layout{
		TaskRoot:     taskRoot,
		Workdir:      filepath.Join(taskRoot, "workdir"),
		RuntimeHome:  filepath.Join(taskRoot, "runtime-home"),
		ProviderHome: filepath.Join(taskRoot, "runtime-home", providerDir),
		SkillsRoot:   filepath.Join(taskRoot, "skills"),
		Artifacts:    filepath.Join(taskRoot, "artifacts"),
		Logs:         filepath.Join(taskRoot, "logs"),
	}
}

func safeBlackboardV2PathComponent(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value &&
		!filepath.IsAbs(value) && !strings.ContainsAny(value, `/\\`)
}

func safeCodexV2PathComponent(value string) bool {
	return safeBlackboardV2PathComponent(value)
}

func validateExistingBlackboardV2Directory(root *os.Root, relative string) error {
	current, err := root.OpenRoot(".")
	if err != nil {
		return err
	}
	for _, component := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		info, err := current.Lstat(component)
		if errors.Is(err, os.ErrNotExist) {
			_ = current.Close()
			return nil
		}
		if err != nil {
			_ = current.Close()
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			_ = current.Close()
			return fmt.Errorf("task layout contains a non-directory or symbolic link at %q", relative)
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			_ = current.Close()
			return err
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			_ = next.Close()
			_ = current.Close()
			return fmt.Errorf("task layout changed while validating %q", relative)
		}
		_ = current.Close()
		current = next
	}
	_ = current.Close()
	return nil
}

func validateExistingCodexV2Directory(root *os.Root, relative string) error {
	return validateExistingBlackboardV2Directory(root, relative)
}

func openBlackboardV2Directory(root *os.Root, relative string) (*os.Root, error) {
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		info, err := current.Lstat(component)
		if errors.Is(err, os.ErrNotExist) {
			if err := current.Mkdir(component, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				_ = current.Close()
				return nil, err
			}
			info, err = current.Lstat(component)
		}
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			_ = current.Close()
			return nil, fmt.Errorf("task layout contains a non-directory or symbolic link at %q", relative)
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			_ = next.Close()
			_ = current.Close()
			return nil, fmt.Errorf("task layout changed while opening %q", relative)
		}
		_ = current.Close()
		current = next
	}
	return current, nil
}

func openCodexV2Directory(root *os.Root, relative string) (*os.Root, error) {
	return openBlackboardV2Directory(root, relative)
}

func rejectBlackboardV2Symlink(root *os.Root, relative string) error {
	info, err := root.Lstat(relative)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("task launch file is a symbolic link at %q", relative)
	}
	return nil
}

func rejectCodexV2Symlink(root *os.Root, relative string) error {
	return rejectBlackboardV2Symlink(root, relative)
}

func codexV2Layout(rootDir, taskID string) Layout {
	return blackboardV2Layout(rootDir, taskID, providerHomeDir(runtimeprofile.ProviderCodex))
}
