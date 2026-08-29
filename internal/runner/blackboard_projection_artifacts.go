package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"pentest/internal/runtimeprofile"
)

// The ownership record is the generator boundary for Blackboard projection
// cleanup. A path is removable only when Required projection recorded that
// CyberPenda generated it. Omitted projection removes the record itself, so it
// does not leave a Blackboard manifest in the Runtime Layout.
const blackboardProjectionOwnershipRecord = ".cyberpenda-blackboard-projection-files.json"

type blackboardProjectionArtifact string

const (
	blackboardProjectionAgentsFile    blackboardProjectionArtifact = "workdir-agents"
	blackboardProjectionClaudeFile    blackboardProjectionArtifact = "workdir-claude"
	blackboardProjectionContextFile   blackboardProjectionArtifact = "workdir-context"
	blackboardProjectionSnapshotFile  blackboardProjectionArtifact = "workdir-blackboard"
	blackboardProjectionClaudeMCPFile blackboardProjectionArtifact = "workdir-claude-mcp"
	blackboardProjectionPiMCPFile     blackboardProjectionArtifact = "pi-mcp"
	blackboardProjectionRecordSchema                               = "cyberpenda-blackboard-projection-files/v1"
)

type blackboardProjectionArtifactRecord struct {
	Schema    string                         `json:"schema"`
	Artifacts []blackboardProjectionArtifact `json:"artifacts"`
}

func recordBlackboardProjectionArtifacts(layout Layout, artifacts ...blackboardProjectionArtifact) error {
	if len(artifacts) == 0 {
		return nil
	}
	record, found, err := readBlackboardProjectionArtifactRecord(layout)
	if err != nil {
		return err
	}
	if !found {
		record = blackboardProjectionArtifactRecord{Schema: blackboardProjectionRecordSchema}
	}
	owned := make(map[blackboardProjectionArtifact]struct{}, len(record.Artifacts)+len(artifacts))
	for _, artifact := range record.Artifacts {
		if _, _, ok := blackboardProjectionArtifactLocation(layout, artifact); !ok {
			return fmt.Errorf("Blackboard projection ownership record contains unknown artifact %q", artifact)
		}
		owned[artifact] = struct{}{}
	}
	for _, artifact := range artifacts {
		if _, _, ok := blackboardProjectionArtifactLocation(layout, artifact); !ok {
			return fmt.Errorf("cannot record unknown Blackboard projection artifact %q", artifact)
		}
		owned[artifact] = struct{}{}
	}
	record.Artifacts = record.Artifacts[:0]
	for artifact := range owned {
		record.Artifacts = append(record.Artifacts, artifact)
	}
	sort.Slice(record.Artifacts, func(i, j int) bool { return record.Artifacts[i] < record.Artifacts[j] })
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Blackboard projection ownership record: %w", err)
	}
	root, err := openRealProjectionRoot(layout.RuntimeHome)
	if err != nil {
		return fmt.Errorf("open Runtime home for Blackboard projection ownership: %w", err)
	}
	defer root.Close()
	if err := writeRootFileAtomically(root, blackboardProjectionOwnershipRecord, raw); err != nil {
		return fmt.Errorf("write Blackboard projection ownership record: %w", err)
	}
	return nil
}

func clearRecordedBlackboardProjectionArtifacts(layout Layout) error {
	record, found, err := readBlackboardProjectionArtifactRecord(layout)
	if err != nil || !found {
		return err
	}
	type artifactLocation struct {
		artifact blackboardProjectionArtifact
		base     string
		relative string
	}
	locations := make([]artifactLocation, 0, len(record.Artifacts))
	for _, artifact := range record.Artifacts {
		base, relative, ok := blackboardProjectionArtifactLocation(layout, artifact)
		if !ok {
			return fmt.Errorf("Blackboard projection ownership record contains unknown artifact %q", artifact)
		}
		locations = append(locations, artifactLocation{artifact: artifact, base: base, relative: relative})
	}
	for _, location := range locations {
		raw, found, err := readRecordedProjectionFile(location.base, location.relative)
		if err != nil {
			return fmt.Errorf("validate generated Blackboard projection artifact %q: %w", location.relative, err)
		}
		if found && !matchesGeneratedBlackboardProjectionArtifact(location.artifact, raw) {
			return fmt.Errorf("recorded Blackboard projection artifact %q does not match the CyberPenda generator contract", location.relative)
		}
	}
	for _, location := range locations {
		if err := removeRecordedProjectionFile(location.base, location.relative); err != nil {
			return fmt.Errorf("remove generated Blackboard projection artifact %q: %w", location.relative, err)
		}
	}
	root, err := openRealProjectionRoot(layout.RuntimeHome)
	if err != nil {
		return fmt.Errorf("open Runtime home for Blackboard projection cleanup: %w", err)
	}
	defer root.Close()
	if err := root.Remove(blackboardProjectionOwnershipRecord); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove Blackboard projection ownership record: %w", err)
	}
	return nil
}

func readBlackboardProjectionArtifactRecord(layout Layout) (blackboardProjectionArtifactRecord, bool, error) {
	root, err := openRealProjectionRoot(layout.RuntimeHome)
	if err != nil {
		return blackboardProjectionArtifactRecord{}, false, fmt.Errorf("open Runtime home for Blackboard projection ownership: %w", err)
	}
	defer root.Close()
	info, err := root.Lstat(blackboardProjectionOwnershipRecord)
	if errors.Is(err, os.ErrNotExist) {
		return blackboardProjectionArtifactRecord{}, false, nil
	}
	if err != nil {
		return blackboardProjectionArtifactRecord{}, false, fmt.Errorf("inspect Blackboard projection ownership record: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return blackboardProjectionArtifactRecord{}, false, fmt.Errorf("Blackboard projection ownership record is not a regular file")
	}
	file, err := root.Open(blackboardProjectionOwnershipRecord)
	if err != nil {
		return blackboardProjectionArtifactRecord{}, false, fmt.Errorf("open Blackboard projection ownership record: %w", err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 64*1024))
	if err != nil {
		return blackboardProjectionArtifactRecord{}, false, fmt.Errorf("read Blackboard projection ownership record: %w", err)
	}
	var record blackboardProjectionArtifactRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return blackboardProjectionArtifactRecord{}, false, fmt.Errorf("decode Blackboard projection ownership record: %w", err)
	}
	if record.Schema != blackboardProjectionRecordSchema {
		return blackboardProjectionArtifactRecord{}, false, fmt.Errorf("Blackboard projection ownership record has unsupported schema %q", record.Schema)
	}
	return record, true, nil
}

func blackboardProjectionArtifactLocation(layout Layout, artifact blackboardProjectionArtifact) (string, string, bool) {
	switch artifact {
	case blackboardProjectionAgentsFile:
		return layout.Workdir, "AGENTS.md", true
	case blackboardProjectionClaudeFile:
		return layout.Workdir, "CLAUDE.md", true
	case blackboardProjectionContextFile:
		return layout.Workdir, filepath.Join(".pentest", "context.json"), true
	case blackboardProjectionSnapshotFile:
		return layout.Workdir, filepath.Join(".pentest", "blackboard.json"), true
	case blackboardProjectionClaudeMCPFile:
		return layout.Workdir, ".mcp.json", true
	case blackboardProjectionPiMCPFile:
		return layout.RuntimeHome, filepath.Join(providerHomeDir(runtimeprofile.ProviderPi), "agent", "mcp.json"), true
	default:
		return "", "", false
	}
}

func removeRecordedProjectionFile(base, relative string) error {
	root, name, found, err := openRecordedProjectionFile(base, relative)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	defer root.Close()
	return root.Remove(name)
}

func readRecordedProjectionFile(base, relative string) ([]byte, bool, error) {
	root, name, found, err := openRecordedProjectionFile(base, relative)
	if err != nil || !found {
		return nil, found, err
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 16*1024*1024))
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

func openRecordedProjectionFile(base, relative string) (*os.Root, string, bool, error) {
	root, err := openRealProjectionRoot(base)
	if err != nil {
		return nil, "", false, err
	}
	current := root
	components := splitProjectionPath(relative)
	if len(components) == 0 {
		_ = current.Close()
		return nil, "", false, fmt.Errorf("recorded projection artifact path is invalid")
	}
	for _, component := range components[:len(components)-1] {
		info, err := current.Lstat(component)
		if errors.Is(err, os.ErrNotExist) {
			_ = current.Close()
			return nil, "", false, nil
		}
		if err != nil {
			_ = current.Close()
			return nil, "", false, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			_ = current.Close()
			return nil, "", false, fmt.Errorf("generated projection parent %q is not a real directory", component)
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			_ = current.Close()
			return nil, "", false, err
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			_ = next.Close()
			_ = current.Close()
			return nil, "", false, fmt.Errorf("generated projection parent %q changed during cleanup", component)
		}
		_ = current.Close()
		current = next
	}
	name := components[len(components)-1]
	info, err := current.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		_ = current.Close()
		return nil, "", false, nil
	}
	if err != nil {
		_ = current.Close()
		return nil, "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		_ = current.Close()
		return nil, "", false, fmt.Errorf("recorded projection artifact is not a regular file")
	}
	return current, name, true, nil
}

func matchesGeneratedBlackboardProjectionArtifact(artifact blackboardProjectionArtifact, raw []byte) bool {
	switch artifact {
	case blackboardProjectionAgentsFile, blackboardProjectionClaudeFile:
		return bytes.HasPrefix(raw, []byte("# Blackboard workflow\n\n")) ||
			bytes.HasPrefix(raw, []byte("# Pentest task context\n\n")) ||
			bytes.HasPrefix(raw, []byte("# Non-Project Session context\n\n"))
	case blackboardProjectionContextFile:
		var context map[string]string
		if err := json.Unmarshal(raw, &context); err != nil || len(context) == 0 {
			return false
		}
		hasOwner := false
		for key, value := range context {
			switch key {
			case "project_id", "task_id", "session_id":
				hasOwner = hasOwner || value != ""
			case "mcp_url":
			default:
				return false
			}
		}
		return hasOwner
	case blackboardProjectionSnapshotFile:
		var snapshot struct {
			Schema string `json:"schema"`
		}
		return json.Unmarshal(raw, &snapshot) == nil && snapshot.Schema == "runtime-blackboard/v2"
	case blackboardProjectionClaudeMCPFile, blackboardProjectionPiMCPFile:
		var config struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if err := json.Unmarshal(raw, &config); err != nil {
			return false
		}
		_, generated := config.MCPServers[trustedMCPServerName]
		return generated
	default:
		return false
	}
}

func splitProjectionPath(path string) []string {
	clean := filepath.Clean(path)
	if clean == "." || clean == "" || filepath.IsAbs(clean) {
		return nil
	}
	var components []string
	for clean != "." {
		dir, file := filepath.Split(clean)
		if file == "" || file == "." || file == ".." {
			return nil
		}
		components = append([]string{file}, components...)
		clean = filepath.Clean(dir)
	}
	return components
}

func openRealProjectionRoot(path string) (*os.Root, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("projection root is not a real directory")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, fmt.Errorf("projection root changed while opening")
	}
	return root, nil
}
