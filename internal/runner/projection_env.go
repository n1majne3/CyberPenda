package runner

import (
	"path/filepath"
	"strings"

	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
)

// LaunchConfigPath returns the config path passed to the runtime CLI. Sandbox
// launches use container paths under /task.
func LaunchConfigPath(layout Layout, provider runtimeprofile.Provider, hostConfigPath string, sandbox bool) string {
	if !sandbox {
		return hostConfigPath
	}
	rel, err := filepath.Rel(layout.TaskRoot, hostConfigPath)
	if err != nil {
		return hostConfigPath
	}
	return "/task/" + filepath.ToSlash(rel)
}

// LaunchProcessEnv returns process environment variables for the launch adapter.
// Claude Code reads settings from CLAUDE_HOME; profile env lives in settings.json.
func LaunchProcessEnv(layout Layout, profile runtimeprofile.Profile, sandbox bool, ctx RuntimeOwnerContext) map[string]string {
	return launchProcessEnv(layout, profile, sandbox, ctx, nil)
}

func launchProcessEnv(layout Layout, profile runtimeprofile.Profile, sandbox bool, ctx RuntimeOwnerContext, registry *runtimeplugin.Registry) map[string]string {
	env := map[string]string{}
	if sandbox {
		// Claude Code allows --dangerously-skip-permissions in sandboxed containers
		// when IS_SANDBOX=1, even if the process runs as root inside Docker.
		env["IS_SANDBOX"] = "1"
		env["PENTEST_SKILLS_DIR"] = sandboxSkillsImagePath
	}
	if ctx.Owner.ProjectID != "" {
		env["PENTEST_PROJECT_ID"] = ctx.Owner.ProjectID
	}
	if ctx.Owner.TaskID != "" {
		env["PENTEST_TASK_ID"] = ctx.Owner.TaskID
	}
	if ctx.Owner.SessionID != "" {
		env["PENTEST_SESSION_ID"] = ctx.Owner.SessionID
	}
	if ctx.BlackboardProtocol != "" {
		env["PENTEST_BLACKBOARD_PROTOCOL"] = ctx.BlackboardProtocol
	}
	if ctx.ContinuationID != "" {
		env["PENTEST_CONTINUATION_ID"] = ctx.ContinuationID
	}
	if ctx.MCPURL != "" {
		env["PENTEST_MCP_URL"] = ctx.MCPURL
	}
	// PENTEST_AUTH_TOKEN is deliberately never projected: the daemon operator
	// token authorizes the full API and must stay outside Runtime boundaries.
	// Runtime traffic authenticates with the narrower PENTEST_INTERFACE_TOKEN.
	if ctx.InterfaceToken != "" {
		env["PENTEST_INTERFACE_TOKEN"] = ctx.InterfaceToken
	}
	if ctx.APIURL != "" {
		env["PENTEST_API_URL"] = ctx.APIURL
		if ctx.BlackboardProtocol == "fgs" {
			// Older sandbox CLIs append /api/v2 without normalizing the base.
			// A daemon root is accepted by both old and current FGS clients.
			env["PENTEST_API_URL"] = strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(ctx.APIURL), "/"), "/api")
		}
	}
	if ctx.BlackboardMode != "" {
		env["PENTEST_BLACKBOARD_MODE"] = ctx.BlackboardMode
	}
	if ctx.WorkingGraphRoot != "" {
		env["PENTEST_WORKING_GRAPH_ROOT"] = launchVisiblePath(layout, ctx.WorkingGraphRoot, sandbox)
	}
	if ctx.WorkingGraphOutbox != "" {
		env["PENTEST_WORKING_GRAPH_OUTBOX"] = launchVisiblePath(layout, ctx.WorkingGraphOutbox, sandbox)
	}
	if ctx.WorkingGraphReceipts != "" {
		env["PENTEST_WORKING_GRAPH_RECEIPTS"] = launchVisiblePath(layout, ctx.WorkingGraphReceipts, sandbox)
	}
	manifestEnvRendered := false
	if plugin, ok := runtimePluginForProvider(profile.Provider, registry); ok {
		rendered, err := runtimeplugin.RenderEnv(plugin.ProcessEnv, processEnvRenderContext(layout, profile, sandbox))
		if err == nil {
			for key, value := range rendered {
				env[key] = value
			}
			manifestEnvRendered = len(rendered) > 0
		}
	}
	if !manifestEnvRendered {
		for key, value := range profile.Fields.Env {
			env[key] = value
		}
	}
	return env
}

func launchVisiblePath(layout Layout, hostPath string, sandbox bool) string {
	if !sandbox || !filepath.IsAbs(hostPath) {
		return hostPath
	}
	if visible, ok := sandboxMountedPath(layout.TaskRoot, "/task", hostPath); ok {
		return visible
	}
	if visible, ok := sandboxMountedPath(layout.Workdir, "/task/workdir", hostPath); ok {
		return visible
	}
	return hostPath
}

func sandboxMountedPath(hostRoot, sandboxRoot, hostPath string) (string, bool) {
	// Layouts may use the daemon's relative runs directory, while the graph
	// receiver resolves its paths to absolute names. Compare the same form.
	if hostRoot == "" {
		return "", false
	}
	var err error
	hostRoot, err = filepath.Abs(hostRoot)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(hostRoot, hostPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return sandboxRoot, true
	}
	return strings.TrimSuffix(sandboxRoot, "/") + "/" + filepath.ToSlash(rel), true
}

func processEnvRenderContext(layout Layout, profile runtimeprofile.Profile, sandbox bool) runtimeplugin.RenderContext {
	runtimeHome := layout.RuntimeHome
	workdir := layout.Workdir
	if sandbox {
		runtimeHome = "/task/runtime-home"
		workdir = "/task/workdir"
	}
	return runtimeplugin.RenderContext{
		Scalars: map[string]string{
			"runtime_home":  runtimeHome,
			"workdir":       workdir,
			"provider_home": filepath.Join(runtimeHome, providerHomeDir(profile.Provider)),
		},
	}
}

// LaunchProcessEnvWithCredentials returns process environment variables for a
// runtime launch, including profile env and resolved API key material needed by
// runtimes that interpolate env references from their generated config.
func LaunchProcessEnvWithCredentials(layout Layout, profile runtimeprofile.Profile, sandbox bool, ctx RuntimeOwnerContext, req ProjectionRequest) (map[string]string, error) {
	env := launchProcessEnv(layout, profile, sandbox, ctx, req.RuntimePlugins)
	if sandbox && len(req.SkillBundles) > 0 {
		env["PENTEST_SKILLS_DIR"] = "/task/skills"
	}
	for key, value := range profile.Fields.Env {
		env[key] = value
	}

	if profile.Provider == runtimeprofile.ProviderClaudeCode {
		claudeEnv, err := buildClaudeEnv(profile, req)
		if err != nil {
			return nil, err
		}
		for key, value := range claudeEnv {
			env[key] = value
		}
		return processEnvForBlackboardProjection(env, req.BlackboardProjection), nil
	}

	materialized, err := resolveMaterializedCredentials(profile, req)
	if err != nil {
		return nil, err
	}
	for key, value := range materialized {
		env[key] = value
	}
	return processEnvForBlackboardProjection(env, req.BlackboardProjection), nil
}

func processEnvForBlackboardProjection(env map[string]string, projection BlackboardProjection) map[string]string {
	if projection != BlackboardProjectionOmitted {
		return env
	}
	for key := range env {
		if isBlackboardAuthorityEnv(key) {
			delete(env, key)
		}
	}
	return env
}
