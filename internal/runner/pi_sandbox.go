package runner

import (
	"fmt"
	"strings"
)

const (
	defaultPiNPMPackage = "@earendil-works/pi-coding-agent"
	piNPMPackageEnv     = "PENTEST_PI_NPM_PACKAGE"
	piNPMVersionEnv     = "PENTEST_PI_NPM_VERSION"
)

func WrapSandboxPiCommand(runtimeCommand []string, profileEnv map[string]string) ([]string, error) {
	if len(runtimeCommand) == 0 {
		return nil, fmt.Errorf("pi runtime command is required")
	}
	piArgs := append([]string{}, runtimeCommand...)
	if strings.TrimSpace(piArgs[0]) != "" {
		piArgs = piArgs[1:]
	}

	pkg := strings.TrimSpace(profileEnv[piNPMPackageEnv])
	if pkg == "" {
		pkg = defaultPiNPMPackage
	}
	version := strings.TrimSpace(profileEnv[piNPMVersionEnv])
	installTarget := pkg
	if version != "" {
		installTarget = pkg + "@" + version
	}

	quoted := make([]string, len(piArgs))
	for i, arg := range piArgs {
		quoted[i] = shellQuote(arg)
	}
	script := fmt.Sprintf(
		"set -e\nif ! command -v pi >/dev/null 2>&1; then\n  npm install -g %s > /task/logs/pi-bootstrap.log 2>&1\nfi\nexec pi %s\n",
		shellQuote(installTarget),
		strings.Join(quoted, " "),
	)

	return []string{"sh", "-c", script}, nil
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\'"'"'`) + "'"
}
