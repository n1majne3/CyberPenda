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

var ErrUnsafeCodexV2Layout = errors.New("unsafe Codex v2 task layout")

// PrepareCodexV2TaskLayout validates every existing task-local directory
// before creating any missing directory. V2 launch preparation uses this
// before projecting config, credentials, or Runtime context.
func PrepareCodexV2TaskLayout(rootDir, taskID string) (Layout, error) {
	if strings.TrimSpace(rootDir) == "" {
		return Layout{}, fmt.Errorf("%w: runner root is required", ErrUnsafeCodexV2Layout)
	}
	if !safeCodexV2PathComponent(taskID) {
		return Layout{}, fmt.Errorf("%w: invalid task path component", ErrUnsafeCodexV2Layout)
	}
	rootDir = filepath.Clean(rootDir)
	if info, err := os.Lstat(rootDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return Layout{}, fmt.Errorf("%w: Runtime Root is not a real directory", ErrUnsafeCodexV2Layout)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Layout{}, fmt.Errorf("%w: inspect Runtime Root: %v", ErrUnsafeCodexV2Layout, err)
	} else if err := os.MkdirAll(rootDir, 0o700); err != nil {
		return Layout{}, fmt.Errorf("%w: prepare Runtime Root: %v", ErrUnsafeCodexV2Layout, err)
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return Layout{}, fmt.Errorf("%w: open Runtime Root: %v", ErrUnsafeCodexV2Layout, err)
	}
	defer root.Close()
	layout := codexV2Layout(rootDir, taskID)
	relatives := []string{
		taskID,
		filepath.Join(taskID, "workdir"),
		filepath.Join(taskID, "runtime-home"),
		filepath.Join(taskID, "runtime-home", "codex"),
		filepath.Join(taskID, "skills"),
		filepath.Join(taskID, "artifacts"),
		filepath.Join(taskID, "logs"),
		filepath.Join(taskID, "workdir", ".pentest"),
	}
	for _, relative := range relatives {
		if err := validateExistingCodexV2Directory(root, relative); err != nil {
			return Layout{}, fmt.Errorf("%w: %v", ErrUnsafeCodexV2Layout, err)
		}
	}
	for _, relative := range []string{
		filepath.Join(taskID, "runtime-home", "codex", "config.toml"),
		filepath.Join(taskID, "runtime-home", "codex", "auth.json"),
		filepath.Join(taskID, "workdir", "AGENTS.md"),
		filepath.Join(taskID, "workdir", ".pentest", "scope.json"),
		filepath.Join(taskID, "workdir", ".pentest", "blackboard.json"),
	} {
		if err := rejectCodexV2Symlink(root, relative); err != nil {
			return Layout{}, fmt.Errorf("%w: %v", ErrUnsafeCodexV2Layout, err)
		}
	}
	for _, relative := range relatives {
		directory, err := openCodexV2Directory(root, relative)
		if err != nil {
			return Layout{}, fmt.Errorf("%w: %v", ErrUnsafeCodexV2Layout, err)
		}
		_ = directory.Close()
	}
	return layout, nil
}

// ProjectCodexV2RuntimeConfig projects Codex config without ever constructing
// the legacy identity context. Credential lookup retains its Project binding,
// but the generic Codex writer receives no IDs, daemon address, or auth token.
func ProjectCodexV2RuntimeConfig(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest) (ConfigProjection, error) {
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

// CodexV2ProfileWithModelSnapshot applies the in-memory model resolution that
// config projection would otherwise discover only after touching the layout.
func CodexV2ProfileWithModelSnapshot(profile runtimeprofile.Profile, snapshot modelprovider.Snapshot) runtimeprofile.Profile {
	return profileWithModelSnapshot(profile, snapshot)
}

func codexV2Layout(rootDir, taskID string) Layout {
	taskRoot := filepath.Join(rootDir, taskID)
	return Layout{
		TaskRoot:     taskRoot,
		Workdir:      filepath.Join(taskRoot, "workdir"),
		RuntimeHome:  filepath.Join(taskRoot, "runtime-home"),
		ProviderHome: filepath.Join(taskRoot, "runtime-home", "codex"),
		SkillsRoot:   filepath.Join(taskRoot, "skills"),
		Artifacts:    filepath.Join(taskRoot, "artifacts"),
		Logs:         filepath.Join(taskRoot, "logs"),
	}
}

func safeCodexV2PathComponent(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value &&
		!filepath.IsAbs(value) && !strings.ContainsAny(value, `/\\`)
}

func validateExistingCodexV2Directory(root *os.Root, relative string) error {
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

func openCodexV2Directory(root *os.Root, relative string) (*os.Root, error) {
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

func rejectCodexV2Symlink(root *os.Root, relative string) error {
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
