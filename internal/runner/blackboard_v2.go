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
)

// ProjectCodexBlackboardV2Files installs Codex's one persistent checklist and
// immutable Scope projection. The compact launch header travels in argv once.
func ProjectCodexBlackboardV2Files(layout Layout, header blackboardv2.LaunchHeader, scope project.Scope) error {
	if err := validateBlackboardV2Header(header); err != nil {
		return err
	}
	info, err := os.Lstat(layout.Workdir)
	if err != nil {
		return fmt.Errorf("inspect Codex workdir: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Codex workdir must be a real directory")
	}
	root, err := os.OpenRoot(layout.Workdir)
	if err != nil {
		return fmt.Errorf("open Codex workdir: %w", err)
	}
	defer root.Close()
	if err := root.Mkdir(".pentest", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("prepare Codex task context directory: %w", err)
	}
	pentestRoot, err := root.OpenRoot(".pentest")
	if err != nil {
		return fmt.Errorf("open confined Codex task context directory: %w", err)
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
	if err := writeRootFileAtomically(root, "AGENTS.md", instructions); err != nil {
		return fmt.Errorf("project Codex checklist: %w", err)
	}
	for _, obsolete := range []struct {
		root *os.Root
		name string
	}{{root, "CLAUDE.md"}, {pentestRoot, "context.json"}} {
		if err := obsolete.root.Remove(obsolete.name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove duplicate Runtime context %s: %w", obsolete.name, err)
		}
	}
	return nil
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
