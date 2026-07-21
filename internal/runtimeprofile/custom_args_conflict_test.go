package runtimeprofile_test

import (
	"errors"
	"strings"
	"testing"

	"pentest/internal/runtimeprofile"
)

func TestValidateCustomArgsRejectsDocumentedAliases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		provider  runtimeprofile.Provider
		args      []string
		wantArg   string // complete offending form (may include value)
		wantFlag  string // flag or config key
		wantField string
	}{
		// Codex — model (builtin argv --model; CLI also -m)
		{name: "codex --model", provider: runtimeprofile.ProviderCodex, args: []string{"--model", "gpt-5"}, wantArg: "--model gpt-5", wantFlag: "--model", wantField: "model"},
		{name: "codex --model=", provider: runtimeprofile.ProviderCodex, args: []string{"--model=gpt-5"}, wantArg: "--model=gpt-5", wantFlag: "--model", wantField: "model"},
		{name: "codex -m", provider: runtimeprofile.ProviderCodex, args: []string{"-m", "gpt-5"}, wantArg: "-m gpt-5", wantFlag: "-m", wantField: "model"},
		{name: "codex -m=", provider: runtimeprofile.ProviderCodex, args: []string{"-m=gpt-5"}, wantArg: "-m=gpt-5", wantFlag: "-m", wantField: "model"},
		// Codex — -c / --config token shapes for model / model_provider / effort
		{name: "codex -c model=", provider: runtimeprofile.ProviderCodex, args: []string{"-c", "model=gpt-5"}, wantArg: "-c model=gpt-5", wantFlag: "model", wantField: "model"},
		{name: "codex --config model=", provider: runtimeprofile.ProviderCodex, args: []string{"--config", "model=gpt-5"}, wantArg: "--config model=gpt-5", wantFlag: "model", wantField: "model"},
		{name: "codex --config=model=", provider: runtimeprofile.ProviderCodex, args: []string{"--config=model=gpt-5"}, wantArg: "--config=model=gpt-5", wantFlag: "model", wantField: "model"},
		{name: "codex -c model_provider=", provider: runtimeprofile.ProviderCodex, args: []string{"-c", "model_provider=custom"}, wantArg: "-c model_provider=custom", wantFlag: "model_provider", wantField: "model_provider"},
		{name: "codex --config model_provider=", provider: runtimeprofile.ProviderCodex, args: []string{"--config", "model_provider=custom"}, wantArg: "--config model_provider=custom", wantFlag: "model_provider", wantField: "model_provider"},
		{name: "codex -c model_reasoning_effort=", provider: runtimeprofile.ProviderCodex, args: []string{"-c", "model_reasoning_effort=high"}, wantArg: "-c model_reasoning_effort=high", wantFlag: "model_reasoning_effort", wantField: "reasoning_effort"},
		{name: "codex --config model_reasoning_effort=", provider: runtimeprofile.ProviderCodex, args: []string{"--config", "model_reasoning_effort=xhigh"}, wantArg: "--config model_reasoning_effort=xhigh", wantFlag: "model_reasoning_effort", wantField: "reasoning_effort"},
		{name: "codex --config=model_reasoning_effort=", provider: runtimeprofile.ProviderCodex, args: []string{`--config=model_reasoning_effort="high"`}, wantArg: "--config=model_reasoning_effort=high", wantFlag: "model_reasoning_effort", wantField: "reasoning_effort"},
		{name: "codex -c=model_reasoning_effort", provider: runtimeprofile.ProviderCodex, args: []string{"-c=model_reasoning_effort=max"}, wantArg: "-c=model_reasoning_effort=max", wantFlag: "model_reasoning_effort", wantField: "reasoning_effort"},

		// Claude Code — builtin --model; CLI --effort
		{name: "claude --model", provider: runtimeprofile.ProviderClaudeCode, args: []string{"--model", "claude-opus"}, wantArg: "--model claude-opus", wantFlag: "--model", wantField: "model"},
		{name: "claude --model=", provider: runtimeprofile.ProviderClaudeCode, args: []string{"--model=claude-opus"}, wantArg: "--model=claude-opus", wantFlag: "--model", wantField: "model"},
		{name: "claude --effort", provider: runtimeprofile.ProviderClaudeCode, args: []string{"--effort", "high"}, wantArg: "--effort high", wantFlag: "--effort", wantField: "reasoning_effort"},
		{name: "claude --effort=", provider: runtimeprofile.ProviderClaudeCode, args: []string{"--effort=max"}, wantArg: "--effort=max", wantFlag: "--effort", wantField: "reasoning_effort"},

		// Pi — builtin --model / --provider; CLI --thinking
		{name: "pi --provider", provider: runtimeprofile.ProviderPi, args: []string{"--provider", "custom"}, wantArg: "--provider custom", wantFlag: "--provider", wantField: "model_provider"},
		{name: "pi --provider=", provider: runtimeprofile.ProviderPi, args: []string{"--provider=openai"}, wantArg: "--provider=openai", wantFlag: "--provider", wantField: "model_provider"},
		{name: "pi --model", provider: runtimeprofile.ProviderPi, args: []string{"--model", "DeepSeek-V4-Pro"}, wantArg: "--model DeepSeek-V4-Pro", wantFlag: "--model", wantField: "model"},
		{name: "pi --model=", provider: runtimeprofile.ProviderPi, args: []string{"--model=DeepSeek-V4-Pro"}, wantArg: "--model=DeepSeek-V4-Pro", wantFlag: "--model", wantField: "model"},
		{name: "pi --thinking", provider: runtimeprofile.ProviderPi, args: []string{"--thinking", "medium"}, wantArg: "--thinking medium", wantFlag: "--thinking", wantField: "reasoning_effort"},
		{name: "pi --thinking=", provider: runtimeprofile.ProviderPi, args: []string{"--thinking=high"}, wantArg: "--thinking=high", wantFlag: "--thinking", wantField: "reasoning_effort"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := runtimeprofile.ValidateCustomArgs(tc.provider, tc.args)
			if err == nil {
				t.Fatalf("expected conflict for %v %v", tc.provider, tc.args)
			}
			var conflict *runtimeprofile.CustomArgConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("error type = %T (%v), want CustomArgConflictError", err, err)
			}
			if conflict.Argument != tc.wantArg {
				t.Fatalf("Argument = %q, want %q (err=%v)", conflict.Argument, tc.wantArg, err)
			}
			if conflict.Flag != tc.wantFlag {
				t.Fatalf("Flag = %q, want %q", conflict.Flag, tc.wantFlag)
			}
			if conflict.Field != tc.wantField {
				t.Fatalf("Field = %q, want %q (err=%v)", conflict.Field, tc.wantField, err)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.wantFlag) && !strings.Contains(msg, tc.wantArg) {
				t.Fatalf("error must name offending arg %q / flag %q: %s", tc.wantArg, tc.wantFlag, msg)
			}
			if !strings.Contains(msg, tc.wantField) && !strings.Contains(strings.ToLower(msg), structuredFieldPhrase(tc.wantField)) {
				t.Fatalf("error must name structured field %q: %s", tc.wantField, msg)
			}
			if conflict.CustomArgs != nil && !equalStrings(conflict.CustomArgs, tc.args) {
				t.Fatalf("conflict must not rewrite custom args: got %#v want %#v", conflict.CustomArgs, tc.args)
			}
		})
	}
}

func TestValidateCustomArgsRedactsSecretValuesInOffendingArg(t *testing.T) {
	t.Parallel()
	err := runtimeprofile.ValidateCustomArgs(runtimeprofile.ProviderCodex, []string{"--model=sk-abcdefghijklmnopqrstuv"})
	if err == nil {
		t.Fatal("expected conflict")
	}
	var conflict *runtimeprofile.CustomArgConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v", err)
	}
	if conflict.Argument != "--model=[REDACTED]" {
		t.Fatalf("Argument = %q, want redacted form", conflict.Argument)
	}
	if strings.Contains(conflict.Error(), "sk-abcdefghijklmnopqrstuv") {
		t.Fatalf("Error must not leak secret: %s", conflict.Error())
	}
	if conflict.Flag != "--model" {
		t.Fatalf("Flag = %q", conflict.Flag)
	}
}

func TestValidateCustomArgsAllowsNonConflictingArgs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		provider runtimeprofile.Provider
		args     []string
	}{
		{name: "codex non-interactive and strict", provider: runtimeprofile.ProviderCodex, args: []string{"--dangerously-bypass-approvals-and-sandbox", "--strict", "--json"}},
		{name: "codex unrelated -c key", provider: runtimeprofile.ProviderCodex, args: []string{"-c", "sandbox_workspace_write.network_access=true"}},
		{name: "codex unrelated --config key", provider: runtimeprofile.ProviderCodex, args: []string{"--config", "approval_policy=never"}},
		{name: "codex --config=unrelated", provider: runtimeprofile.ProviderCodex, args: []string{"--config=sandbox_workspace_write.network_access=true"}},
		{name: "claude stream options", provider: runtimeprofile.ProviderClaudeCode, args: []string{"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}},
		{name: "claude permission mode", provider: runtimeprofile.ProviderClaudeCode, args: []string{"--permission-mode", "bypassPermissions"}},
		{name: "pi mode and session", provider: runtimeprofile.ProviderPi, args: []string{"--mode", "json", "--session", "sess-1"}},
		{name: "empty", provider: runtimeprofile.ProviderCodex, args: nil},
		{name: "fake ignores known flags", provider: runtimeprofile.ProviderFake, args: []string{"--model", "x", "--thinking", "high", "--provider", "y"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := runtimeprofile.ValidateCustomArgs(tc.provider, tc.args); err != nil {
				t.Fatalf("unexpected conflict: %v", err)
			}
		})
	}
}

func TestValidateCustomArgsDoesNotStripOrReorder(t *testing.T) {
	t.Parallel()
	original := []string{"--strict", "--model", "gpt-5", "--verbose"}
	snapshot := append([]string(nil), original...)
	err := runtimeprofile.ValidateCustomArgs(runtimeprofile.ProviderCodex, original)
	if err == nil {
		t.Fatal("expected conflict")
	}
	if !equalStrings(original, snapshot) {
		t.Fatalf("validation mutated input args: got %#v want %#v", original, snapshot)
	}
}

func TestCreateRejectsConflictingCustomArgs(t *testing.T) {
	service := newTestService(t)
	_, err := service.Create("Codex Conflict", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model:      "gpt-5",
		CustomArgs: []string{"--model", "other"},
	})
	if err == nil {
		t.Fatal("expected create to reject conflicting custom args")
	}
	var conflict *runtimeprofile.CustomArgConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v (%T), want CustomArgConflictError", err, err)
	}
	if conflict.Flag != "--model" || conflict.Field != "model" {
		t.Fatalf("conflict = %+v", conflict)
	}
	// Nothing persisted.
	listed, err := service.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("create must not persist on conflict, got %#v", listed)
	}
}

func TestUpdateRejectsConflictingCustomArgsWithoutPersisting(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Pi Clean", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		Model:      "DeepSeek-V4-Pro",
		CustomArgs: []string{"--strict-mode", "--flag=value"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err = service.Update(created.ID, "", "", runtimeprofile.Fields{
		Model:      "DeepSeek-V4-Pro",
		CustomArgs: []string{"--thinking", "medium"},
	}, true)
	if err == nil {
		t.Fatal("expected update to reject conflicting custom args")
	}
	var conflict *runtimeprofile.CustomArgConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v (%T), want CustomArgConflictError", err, err)
	}
	if conflict.Flag != "--thinking" {
		t.Fatalf("Flag = %q", conflict.Flag)
	}

	fetched, err := service.Get(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !equalStrings(fetched.Fields.CustomArgs, []string{"--strict-mode", "--flag=value"}) {
		t.Fatalf("stored custom args mutated on rejected update: %#v", fetched.Fields.CustomArgs)
	}
}

func TestReplaceFieldsRejectsConflictingCustomArgsWithoutPersisting(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Claude Clean", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		Model:      "claude-sonnet",
		CustomArgs: []string{"--verbose", "--strict"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err = service.ReplaceFields(created.ID, runtimeprofile.Fields{
		Model:      "claude-sonnet",
		CustomArgs: []string{"--effort", "xhigh"},
	})
	if err == nil {
		t.Fatal("expected ReplaceFields to reject conflicting custom args")
	}
	var conflict *runtimeprofile.CustomArgConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v (%T), want CustomArgConflictError", err, err)
	}
	if conflict.Flag != "--effort" || conflict.Field != "reasoning_effort" {
		t.Fatalf("conflict = %+v", conflict)
	}

	fetched, err := service.Get(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !equalStrings(fetched.Fields.CustomArgs, []string{"--verbose", "--strict"}) {
		t.Fatalf("stored custom args mutated on rejected ReplaceFields: %#v", fetched.Fields.CustomArgs)
	}
}

func TestCreatePreservesNonConflictingCustomArgsByteOrder(t *testing.T) {
	service := newTestService(t)
	args := []string{"--strict", "--flag=value", "positional-keep"}
	created, err := service.Create("Codex Args", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model:      "gpt-5",
		CustomArgs: args,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !equalStrings(created.Fields.CustomArgs, args) {
		t.Fatalf("create custom args = %#v, want %#v", created.Fields.CustomArgs, args)
	}
	fetched, err := service.Get(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !equalStrings(fetched.Fields.CustomArgs, args) {
		t.Fatalf("persisted custom args = %#v, want %#v", fetched.Fields.CustomArgs, args)
	}
}

func structuredFieldPhrase(field string) string {
	switch field {
	case "model":
		return "model"
	case "model_provider":
		return "model provider"
	case "reasoning_effort":
		return "reasoning effort"
	default:
		return field
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
