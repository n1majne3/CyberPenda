package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"pentest/internal/runtimeprofile"
)

type omittedProjectionFileKind uint8

const (
	omittedProjectionInstruction omittedProjectionFileKind = iota
	omittedProjectionContext
	omittedProjectionMCPJSON
	omittedProjectionCodexConfig
	omittedProjectionClaudeSettings
	omittedProjectionHermesConfig
	omittedProjectionCredentialConfig
)

type omittedProjectionFile struct {
	base     string
	relative string
	kind     omittedProjectionFileKind
}

// preflightOmittedBlackboardProjection is intentionally read-only. Blackboard
// Mode is immutable, so a valid Omitted launch uses a fresh or already-Omitted
// Runtime Layout. A layout with stale Required projection must fail closed and
// remain unchanged for operator recovery.
func preflightOmittedBlackboardProjection(layout Layout, profile runtimeprofile.Profile) error {
	trustedMCPNames := omittedProjectionTrustedMCPNames(profile)
	files := []omittedProjectionFile{
		{layout.Workdir, "AGENTS.md", omittedProjectionInstruction},
		{layout.Workdir, "CLAUDE.md", omittedProjectionInstruction},
		{layout.Workdir, filepath.Join(".pentest", "context.json"), omittedProjectionContext},
		{layout.Workdir, filepath.Join(".pentest", "blackboard.json"), omittedProjectionContext},
		{layout.Workdir, ".mcp.json", omittedProjectionMCPJSON},
		{layout.ProviderHome, "settings.json", omittedProjectionClaudeSettings},
		{layout.ProviderHome, "config.toml", omittedProjectionCodexConfig},
		{layout.ProviderHome, "config.yaml", omittedProjectionHermesConfig},
		{layout.ProviderHome, ".env", omittedProjectionCredentialConfig},
		{layout.ProviderHome, "auth.json", omittedProjectionCredentialConfig},
		{layout.ProviderHome, filepath.Join("agent", "mcp.json"), omittedProjectionMCPJSON},
		{layout.ProviderHome, filepath.Join("agent", "auth.json"), omittedProjectionCredentialConfig},
	}
	for _, file := range files {
		raw, found, err := readOmittedProjectionFile(file.base, file.relative)
		if err != nil {
			return fmt.Errorf("Omitted Blackboard projection preflight rejected %s: %w", file.relative, err)
		}
		if !found {
			continue
		}
		var (
			stale      bool
			inspectErr error
		)
		switch file.kind {
		case omittedProjectionInstruction:
			stale = containsGeneratedBlackboardInstructions(raw)
		case omittedProjectionContext:
			stale = true
		case omittedProjectionMCPJSON:
			stale, inspectErr = containsTrustedMCPJSON(raw, trustedMCPNames)
		case omittedProjectionCodexConfig:
			stale, inspectErr = containsTrustedCodexConfig(raw, trustedMCPNames)
		case omittedProjectionClaudeSettings:
			stale = containsBlackboardAuthorityText(raw) || bytes.Contains(raw, []byte("mcp__pentest__"))
		case omittedProjectionHermesConfig:
			stale, inspectErr = containsTrustedHermesConfig(raw, trustedMCPNames)
		case omittedProjectionCredentialConfig:
			stale = containsBlackboardAuthorityText(raw)
		}
		if inspectErr != nil {
			return fmt.Errorf("Omitted Blackboard projection preflight could not inspect %s: %w", file.relative, inspectErr)
		}
		if stale {
			return fmt.Errorf("Omitted Blackboard projection preflight found stale Blackboard artifact %s", file.relative)
		}
	}
	return nil
}

func containsGeneratedBlackboardInstructions(raw []byte) bool {
	if bytes.Contains(raw, []byte("# Blackboard workflow\n\n")) {
		return true
	}
	return bytes.Contains(raw, []byte("Trusted MCP is pre-configured")) &&
		bytes.Contains(raw, []byte("## Required workflow")) &&
		bytes.Contains(raw, []byte("blackboard_finish")) &&
		bytes.Contains(raw, []byte(".pentest/context.json"))
}

func containsTrustedMCPJSON(raw []byte, trustedNames map[string]struct{}) (bool, error) {
	var config struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return false, fmt.Errorf("parse known MCP JSON: %w", err)
	}
	if containsBlackboardAuthorityText(raw) {
		return true, nil
	}
	for name := range config.MCPServers {
		if isOmittedProjectionTrustedMCPName(name, trustedNames) {
			return true, nil
		}
	}
	return false, nil
}

func containsTrustedCodexConfig(raw []byte, trustedNames map[string]struct{}) (bool, error) {
	var config struct {
		MCPServers map[string]any `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal(raw, &config); err != nil {
		return false, fmt.Errorf("parse known Codex TOML: %w", err)
	}
	if containsBlackboardAuthorityText(raw) {
		return true, nil
	}
	for name := range config.MCPServers {
		if isOmittedProjectionTrustedMCPName(name, trustedNames) {
			return true, nil
		}
	}
	return false, nil
}

func containsTrustedHermesConfig(raw []byte, trustedNames map[string]struct{}) (bool, error) {
	var config struct {
		MCPServers map[string]any `yaml:"mcp_servers"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		return false, fmt.Errorf("parse known Hermes YAML: %w", err)
	}
	if containsBlackboardAuthorityText(raw) {
		return true, nil
	}
	for name := range config.MCPServers {
		if isOmittedProjectionTrustedMCPName(name, trustedNames) {
			return true, nil
		}
	}
	return false, nil
}

func omittedProjectionTrustedMCPNames(profile runtimeprofile.Profile) map[string]struct{} {
	names := map[string]struct{}{}
	reservedNameDeclaredExternal := false
	for _, server := range profile.Fields.MCPServers {
		name := strings.ToLower(strings.TrimSpace(server.Name))
		if name == "" {
			continue
		}
		if name == trustedMCPServerName && server.Mode == runtimeprofile.MCPServerExternal {
			reservedNameDeclaredExternal = true
		}
		if server.Mode == runtimeprofile.MCPServerTrusted {
			names[name] = struct{}{}
		}
	}
	if _, declaredTrusted := names[trustedMCPServerName]; !declaredTrusted && !reservedNameDeclaredExternal {
		names[trustedMCPServerName] = struct{}{}
	}
	return names
}

func isOmittedProjectionTrustedMCPName(name string, trustedNames map[string]struct{}) bool {
	_, trusted := trustedNames[strings.ToLower(strings.TrimSpace(name))]
	return trusted
}

func containsBlackboardAuthorityText(raw []byte) bool {
	upper := bytes.ToUpper(raw)
	if bytes.Contains(upper, []byte("PENTEST_INTERFACE_TOKEN")) ||
		bytes.Contains(upper, []byte("PENTEST_AUTH_TOKEN")) ||
		bytes.Contains(upper, []byte("PENTEST_MCP_URL")) ||
		bytes.Contains(upper, []byte("PENTEST_API_URL")) {
		return true
	}
	return false
}

func readOmittedProjectionFile(base, relative string) ([]byte, bool, error) {
	root, err := openOmittedProjectionRoot(base)
	if err != nil {
		return nil, false, err
	}
	current := root
	components := splitOmittedProjectionPath(relative)
	if len(components) == 0 {
		_ = current.Close()
		return nil, false, fmt.Errorf("known projection path is invalid")
	}
	for _, component := range components[:len(components)-1] {
		info, err := current.Lstat(component)
		if errors.Is(err, os.ErrNotExist) {
			_ = current.Close()
			return nil, false, nil
		}
		if err != nil {
			_ = current.Close()
			return nil, false, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			_ = current.Close()
			return nil, false, fmt.Errorf("known projection parent %q is not a real directory", component)
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			_ = current.Close()
			return nil, false, err
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			_ = next.Close()
			_ = current.Close()
			return nil, false, fmt.Errorf("known projection parent %q changed during preflight", component)
		}
		_ = current.Close()
		current = next
	}
	defer current.Close()
	name := components[len(components)-1]
	info, err := current.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("known projection file is not a regular file")
	}
	file, err := current.Open(name)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, false, fmt.Errorf("known projection file changed during preflight")
	}
	const maxProjectionFileSize = 16 * 1024 * 1024
	raw, err := io.ReadAll(io.LimitReader(file, maxProjectionFileSize+1))
	if err != nil {
		return nil, false, err
	}
	if len(raw) > maxProjectionFileSize {
		return nil, false, fmt.Errorf("known projection file is too large to inspect safely")
	}
	return raw, true, nil
}

func openOmittedProjectionRoot(path string) (*os.Root, error) {
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

func splitOmittedProjectionPath(path string) []string {
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
