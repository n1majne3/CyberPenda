package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// EngineKind names the container engine behind PENTEST_CONTAINER_CLI.
type EngineKind string

const (
	EngineDocker  EngineKind = "docker"
	EnginePodman  EngineKind = "podman"
	EngineUnknown EngineKind = "unknown"
)

// EngineInfo is the probed container engine used for sandbox create flags and
// preflight. Detection is best-effort and never starts a long-lived container.
type EngineInfo struct {
	CLI      string     `json:"cli"`
	Kind     EngineKind `json:"kind"`
	Name     string     `json:"name,omitempty"`
	Rootless bool       `json:"rootless,omitempty"`
	Detail   string     `json:"detail,omitempty"`
}

// CommandRunner runs a container CLI subprocess. Tests inject fakes; production
// uses the real executable on PATH.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// DefaultCommandRunner executes name with args and returns combined output.
func DefaultCommandRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

// NormalizeContainerCLI returns the configured CLI or the product default.
func NormalizeContainerCLI(cli string) string {
	cli = strings.TrimSpace(cli)
	if cli == "" {
		return "docker"
	}
	return cli
}

// DetectEngine probes the container CLI. It prefers `info` output and falls
// back to the executable basename when the engine is offline.
func DetectEngine(ctx context.Context, cli string, run CommandRunner) (EngineInfo, error) {
	if run == nil {
		run = DefaultCommandRunner
	}
	cli = NormalizeContainerCLI(cli)
	info := EngineInfo{
		CLI:  cli,
		Kind: kindFromCLIName(cli),
	}

	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := run(probeCtx, cli, "info")
	text := string(out)
	if err != nil {
		// Absolute CLI paths are used by unit tests as create/start doubles that
		// do not implement `info`. Production CLIs are bare names on PATH and
		// must reach a live engine.
		if filepath.IsAbs(cli) {
			if st, statErr := os.Stat(cli); statErr == nil && !st.IsDir() {
				if info.Kind == EngineUnknown {
					info.Kind = EngineDocker
				}
				info.Name = "test-container-cli"
				info.Detail = fmt.Sprintf("test CLI %s (engine info unavailable)", cli)
				return info, nil
			}
		}
		if info.Kind == EngineUnknown {
			info.Kind = EngineDocker
		}
		return info, fmt.Errorf("container CLI %q is not ready: %w: %s", cli, err, strings.TrimSpace(text))
	}
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "podman") || info.Kind == EnginePodman:
		info.Kind = EnginePodman
	case strings.Contains(lower, "docker") || info.Kind == EngineDocker:
		info.Kind = EngineDocker
	}
	switch {
	case strings.Contains(lower, "orbstack"):
		info.Name = "OrbStack"
	case strings.Contains(lower, "docker desktop"):
		info.Name = "Docker Desktop"
	case strings.Contains(lower, "podman desktop") || (info.Kind == EnginePodman && strings.Contains(lower, "desktop")):
		info.Name = "Podman Desktop"
	case info.Kind == EnginePodman:
		info.Name = "Podman"
	case info.Kind == EngineDocker:
		info.Name = "Docker"
	default:
		info.Name = string(info.Kind)
	}
	info.Rootless = isRootlessInfo(lower, text)
	info.Detail = fmt.Sprintf("%s via %s", info.Name, cli)
	return info, nil
}

// HostGatewayAddHosts returns docker/podman --add-host values so the sandbox
// can reach a daemon on the host. host.docker.internal is always set for the
// host-proxy-only entrypoint and existing MCP wiring. Podman also gets
// host.containers.internal for engines that prefer that name.
func HostGatewayAddHosts(kind EngineKind) []string {
	hosts := []string{"host.docker.internal:host-gateway"}
	if kind == EnginePodman || kind == EngineUnknown {
		hosts = append(hosts, "host.containers.internal:host-gateway")
	}
	return hosts
}

// SupportsVPNTun reports whether the engine is expected to honour
// --device /dev/net/tun and --cap-add NET_ADMIN for an OpenVPN client.
// Rootless engines fail closed; rootful Docker/OrbStack/Podman pass.
func (info EngineInfo) SupportsVPNTun() (ok bool, detail string) {
	if info.Rootless {
		return false, "rootless container engines cannot grant NET_ADMIN and /dev/net/tun for OpenVPN"
	}
	switch info.Kind {
	case EngineDocker, EnginePodman:
		name := info.Name
		if name == "" {
			name = string(info.Kind)
		}
		return true, fmt.Sprintf("%s can grant /dev/net/tun and NET_ADMIN at container create", name)
	default:
		return false, "unknown container engine; cannot verify sandbox VPN TUN support"
	}
}

func kindFromCLIName(cli string) EngineKind {
	base := strings.ToLower(filepath.Base(cli))
	// Windows may append .exe.
	base = strings.TrimSuffix(base, ".exe")
	switch {
	case base == "podman" || strings.HasPrefix(base, "podman-"):
		return EnginePodman
	case base == "docker" || strings.HasPrefix(base, "docker-"):
		return EngineDocker
	default:
		return EngineUnknown
	}
}

func isRootlessInfo(lower, raw string) bool {
	if strings.Contains(lower, "rootless: true") || strings.Contains(lower, `"rootless": true`) {
		return true
	}
	// Docker rootless often exposes rootless in security options.
	if strings.Contains(lower, "name=rootless") {
		return true
	}
	// Podman info Human format: "rootless: true" under security.
	for _, line := range strings.Split(raw, "\n") {
		trim := strings.TrimSpace(strings.ToLower(line))
		if strings.HasPrefix(trim, "rootless:") && strings.Contains(trim, "true") {
			return true
		}
	}
	return false
}
