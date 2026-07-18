package runtime

import (
	"fmt"
	"path/filepath"
	"strings"
)

// RewriteDockerCreateCommand replaces the image command in docker create argv
// with a sandbox-local provider bridge command. BuildSandboxCommand places the
// provider binary immediately after the image, so the provider token is a
// stable, non-secret boundary for this rewrite.
func RewriteDockerCreateCommand(createArgs []string, provider string, bridgeCommand []string) ([]string, error) {
	if len(createArgs) == 0 || strings.TrimSpace(createArgs[0]) != "create" {
		return nil, fmt.Errorf("docker create args are required")
	}
	if len(bridgeCommand) == 0 || strings.TrimSpace(bridgeCommand[0]) == "" {
		return nil, fmt.Errorf("bridge command is required")
	}
	target := strings.TrimSpace(provider)
	if target == "claude_code" {
		target = "claude"
	}
	for i := 1; i < len(createArgs); i++ {
		if !strings.Contains(createArgs[i], "=") && filepath.Base(createArgs[i]) == target {
			out := append([]string(nil), createArgs[:i]...)
			interactive := false
			for _, arg := range out {
				if arg == "-i" || arg == "--interactive" {
					interactive = true
					break
				}
			}
			if !interactive {
				withInteractive := make([]string, 0, len(out)+1)
				withInteractive = append(withInteractive, out[0], "-i")
				out = append(withInteractive, out[1:]...)
			}
			out = append(out, bridgeCommand...)
			return out, nil
		}
	}
	return nil, fmt.Errorf("provider command %q was not found in docker create args", provider)
}
