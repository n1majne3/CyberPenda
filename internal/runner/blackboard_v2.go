package runner

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
)

// ProjectCodexBlackboardV2Files installs Codex's one persistent checklist and
// immutable Scope projection. The compact launch header travels in argv once.
func ProjectCodexBlackboardV2Files(layout Layout, header blackboardv2.LaunchHeader, scope project.Scope) error {
	return ProjectBlackboardV2Files(layout, runtimeprofile.ProviderCodex, header, scope)
}

// ProjectBlackboardV2Files installs the shared one-time checklist and immutable
// Scope Snapshot for any supported Blackboard v2 Runtime. Instruction channel
// is provider-native; content is identical across adapters.
func ProjectBlackboardV2Files(layout Layout, provider runtimeprofile.Provider, header blackboardv2.LaunchHeader, scope project.Scope) error {
	if err := validateBlackboardV2Header(header); err != nil {
		return err
	}
	info, err := os.Lstat(layout.Workdir)
	if err != nil {
		return fmt.Errorf("inspect Runtime workdir: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Runtime workdir must be a real directory")
	}
	root, err := os.OpenRoot(layout.Workdir)
	if err != nil {
		return fmt.Errorf("open Runtime workdir: %w", err)
	}
	defer root.Close()
	if err := root.Mkdir(".pentest", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("prepare task context directory: %w", err)
	}
	pentestRoot, err := root.OpenRoot(".pentest")
	if err != nil {
		return fmt.Errorf("open confined task context directory: %w", err)
	}
	defer pentestRoot.Close()

	scopeJSON, err := json.MarshalIndent(scope, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Scope Snapshot: %w", err)
	}
	if err := writeRootFileAtomically(pentestRoot, "scope.json", scopeJSON); err != nil {
		return fmt.Errorf("project Scope Snapshot: %w", err)
	}
	instructions := []byte("# Blackboard workflow\n\n" + blackboardv2.CodexChecklist() + "\n")
	instructionName := blackboardV2InstructionFile(provider)
	if err := writeRootFileAtomically(root, instructionName, instructions); err != nil {
		return fmt.Errorf("project Runtime checklist: %w", err)
	}
	// Keep the checklist on exactly one discovery path per adapter.
	for _, obsolete := range blackboardV2ObsoleteInstructionFiles(provider) {
		if err := root.Remove(obsolete); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove duplicate Runtime context %s: %w", obsolete, err)
		}
	}
	if err := pentestRoot.Remove("context.json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove legacy Runtime context.json: %w", err)
	}
	entries, err := pentestRoot.Open(".")
	if err != nil {
		return fmt.Errorf("inspect task context directory: %w", err)
	}
	defer entries.Close()
	contents, err := entries.ReadDir(-1)
	if err != nil {
		return fmt.Errorf("list task context directory: %w", err)
	}
	for _, entry := range contents {
		if entry.Name() != "blackboard.json" && entry.Name() != "scope.json" {
			return fmt.Errorf("task context directory contains unapproved file %q", entry.Name())
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return fmt.Errorf("task context file %q is not a confined regular file", entry.Name())
		}
	}
	return nil
}

func blackboardV2InstructionFile(provider runtimeprofile.Provider) string {
	if provider == runtimeprofile.ProviderClaudeCode {
		return "CLAUDE.md"
	}
	return "AGENTS.md"
}

func blackboardV2ObsoleteInstructionFiles(provider runtimeprofile.Provider) []string {
	if provider == runtimeprofile.ProviderClaudeCode {
		return []string{"AGENTS.md"}
	}
	return []string{"CLAUDE.md"}
}

// CodexV2ProcessEnv removes legacy project-interface identity and network
// credentials while retaining runtime/model credentials required by Codex.
func CodexV2ProcessEnv(env map[string]string, layout Layout, sandbox bool) map[string]string {
	return BlackboardV2ProcessEnv(env, layout, sandbox)
}

// BlackboardV2ProcessEnv strips model-visible Project Interface identity and
// transport credentials while retaining runtime/model credentials.
func BlackboardV2ProcessEnv(env map[string]string, layout Layout, sandbox bool) map[string]string {
	clean := make(map[string]string, len(env))
	for key, value := range env {
		switch key {
		case "PENTEST_PROJECT_ID", "PENTEST_TASK_ID", "PENTEST_CONTINUATION_ID",
			"PENTEST_MCP_URL", "PENTEST_API_URL", "PENTEST_AUTH_TOKEN", "PENTEST_INTERFACE_TOKEN",
			"PENTEST_DISABLE_TRUSTED_MCP":
			continue
		}
		if !sandbox && strings.HasPrefix(value, layout.TaskRoot+string(filepath.Separator)) {
			relative, err := filepath.Rel(layout.Workdir, value)
			if err == nil {
				value = filepath.ToSlash(relative)
			}
		}
		clean[key] = value
	}
	if !sandbox {
		clean["PWD"] = "."
	}
	return clean
}

// BlackboardV2SupportsProvider reports whether the provider participates in the
// shared Blackboard v2 Launch Pin / Working Snapshot contract.
func BlackboardV2SupportsProvider(provider runtimeprofile.Provider) bool {
	switch provider {
	case runtimeprofile.ProviderFake, runtimeprofile.ProviderCodex, runtimeprofile.ProviderClaudeCode, runtimeprofile.ProviderPi:
		return true
	default:
		return false
	}
}

// BlackboardV2UsesTrustedMCP reports whether the provider receives the trusted
// MCP Project Interface for v2 writes (Claude and Pi). Codex remains networkless.
func BlackboardV2UsesTrustedMCP(provider runtimeprofile.Provider) bool {
	switch provider {
	case runtimeprofile.ProviderClaudeCode, runtimeprofile.ProviderPi:
		return true
	default:
		return false
	}
}

func validateBlackboardV2Header(header blackboardv2.LaunchHeader) error {
	if strings.TrimSpace(header.Runner) == "" || strings.ContainsAny(header.Runner, "\r\n") {
		return fmt.Errorf("Runner header is invalid")
	}
	if header.ScopePath != ".pentest/scope.json" || header.BlackboardPath != ".pentest/blackboard.json" {
		return fmt.Errorf("Blackboard v2 task-local paths are invalid")
	}
	if header.Schema != "runtime-blackboard/v2" || header.Revision < 0 {
		return fmt.Errorf("Blackboard v2 Snapshot header is invalid")
	}
	return nil
}

func writeRootFileAtomically(root *os.Root, name string, data []byte) error {
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return fmt.Errorf("unsafe task-local filename")
	}
	var token [16]byte
	if _, err := io.ReadFull(rand.Reader, token[:]); err != nil {
		return err
	}
	tempName := ".projection-" + hex.EncodeToString(token[:]) + ".tmp"
	temp, err := root.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		_ = temp.Close()
		if cleanup {
			_ = root.Remove(tempName)
		}
	}()
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := root.Rename(tempName, name); err != nil {
		return err
	}
	cleanup = false
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
