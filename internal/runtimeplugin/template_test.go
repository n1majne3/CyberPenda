package runtimeplugin_test

import (
	"reflect"
	"testing"

	"pentest/internal/runtimeplugin"
)

func TestRenderLaunchSplicesListsAndScalars(t *testing.T) {
	template := runtimeplugin.LaunchTemplate{
		Args: []string{"{{binary}}", "--model", "{{model}}", "{{mcp_args}}", "{{custom_args}}", "--", "{{goal}}"},
	}
	args, err := runtimeplugin.RenderLaunch(template, runtimeplugin.RenderContext{
		Scalars: map[string]string{
			"binary": "claude",
			"model":  "glm-5.2",
			"goal":   "map app",
		},
		Lists: map[string][]string{
			"mcp_args":    []string{"--mcp-config", "/task/workdir/.mcp.json"},
			"custom_args": []string{"--permission-mode", "bypassPermissions"},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := []string{"claude", "--model", "glm-5.2", "--mcp-config", "/task/workdir/.mcp.json", "--permission-mode", "bypassPermissions", "--", "map app"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestRenderLaunchSuppressesSingletonDefaults(t *testing.T) {
	template := runtimeplugin.LaunchTemplate{
		Args: []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "{{custom_args}}", "{{goal}}"},
		SingletonOptions: []runtimeplugin.SingletonOption{
			{Options: []string{"-p", "--print"}, Arity: 0},
			{Options: []string{"--output-format"}, Arity: 1},
			{Options: []string{"--verbose"}, Arity: 0},
		},
	}
	args, err := runtimeplugin.RenderLaunch(template, runtimeplugin.RenderContext{
		Scalars: map[string]string{"goal": "map app"},
		Lists:   map[string][]string{"custom_args": []string{"--print", "--output-format", "text", "--verbose"}},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := []string{"claude", "--print", "--output-format", "text", "--verbose", "map app"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestRenderLaunchOmitsFlagWhenPlaceholderIsEmpty(t *testing.T) {
	template := runtimeplugin.LaunchTemplate{
		Args: []string{"codex", "run", "--model", "{{model}}", "--", "{{goal}}"},
	}
	args, err := runtimeplugin.RenderLaunch(template, runtimeplugin.RenderContext{
		Scalars: map[string]string{"goal": "map app"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := []string{"codex", "run", "--", "map app"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestRenderLaunchOmitsGoalSeparatorWhenGoalIsEmpty(t *testing.T) {
	template := runtimeplugin.LaunchTemplate{
		Args: []string{"codex", "run", "{{codex_goal_prefix}}", "{{goal}}"},
	}
	args, err := runtimeplugin.RenderLaunch(template, runtimeplugin.RenderContext{
		Scalars: map[string]string{"codex_subcommand": "run"},
		Lists:   map[string][]string{"codex_goal_prefix": []string{}},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := []string{"codex", "run"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestRenderEnvReplacesScalarFragments(t *testing.T) {
	env, err := runtimeplugin.RenderEnv(map[string]string{
		"CLAUDE_HOME": "{{runtime_home}}/claude",
	}, runtimeplugin.RenderContext{
		Scalars: map[string]string{"runtime_home": "/task/runtime-home"},
	})
	if err != nil {
		t.Fatalf("render env: %v", err)
	}
	if env["CLAUDE_HOME"] != "/task/runtime-home/claude" {
		t.Fatalf("unexpected env: %#v", env)
	}
}
