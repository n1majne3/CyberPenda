package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/task"
)

func TestBuildHostProcessEnvStripsDaemonSecretsAndDeduplicates(t *testing.T) {
	t.Setenv("PENTEST_AUTH_TOKEN", "daemon-secret-token")
	t.Setenv("PENTEST_OPERATOR_TOKEN", "operator-secret-token")
	t.Setenv("PENTEST_DB", "/var/lib/pentest/secret.db")
	t.Setenv("HOST_TEST_KEEP", "keep-value")
	t.Setenv("HOST_TEST_OVERRIDE", "old-value")

	env := buildHostProcessEnv(map[string]string{
		"HOST_TEST_OVERRIDE":      "new-value",
		"PENTEST_INTERFACE_TOKEN": "grant-token-only",
	})
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, forbidden := range []string{
		"\nPENTEST_AUTH_TOKEN=",
		"\nPENTEST_OPERATOR_TOKEN=",
		"\nPENTEST_DB=",
		"\nHOST_TEST_OVERRIDE=old-value\n",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("host process env leaked %q:\n%s", forbidden, joined)
		}
	}
	for _, required := range []string{
		"\nHOST_TEST_KEEP=keep-value\n",
		"\nHOST_TEST_OVERRIDE=new-value\n",
		"\nPENTEST_INTERFACE_TOKEN=grant-token-only\n",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("host process env missing %q:\n%s", required, joined)
		}
	}
}

func TestCommandAdapterStripsDaemonAuthTokenFromChildEnv(t *testing.T) {
	t.Setenv("PENTEST_AUTH_TOKEN", "daemon-secret-must-not-leak")
	outFile := filepath.Join(t.TempDir(), "child-env.txt")
	adapter := NewCommandAdapter(CommandAdapterConfig{
		Name:    "env-check",
		Program: "/bin/sh",
		Args:    []string{"-c", "printf 'auth=%s child=%s\\n' \"$PENTEST_AUTH_TOKEN\" \"$CHILD_VISIBLE_FLAG\" > \"" + outFile + "\""},
		Env:     map[string]string{"CHILD_VISIBLE_FLAG": "visible-123"},
	})
	err := adapter.Run(context.Background(), "goal", func(task.EventKind, task.EventPayload) {})
	if err != nil {
		t.Fatalf("command adapter run: %v", err)
	}
	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read child env output: %v", err)
	}
	if strings.Contains(string(raw), "daemon-secret-must-not-leak") {
		t.Fatalf("child process inherited PENTEST_AUTH_TOKEN: %q", string(raw))
	}
	if string(raw) != "auth= child=visible-123\n" {
		t.Fatalf("unexpected child env output: %q", string(raw))
	}
}

func TestDockerCLISandboxBridgeDockerPassesCreateEnvWithoutArgvLeak(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "captured.txt")
	fakeDocker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"printf 'ARGS:%s\\nENV:%s\\n' \"$*\" \"$OPENAI_API_KEY\" > \"" + outFile + "\"\n" +
		"echo container-123\n"
	if err := os.WriteFile(fakeDocker, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}

	transport := DockerCLISandboxBridgeDocker{
		ContainerCLI: fakeDocker,
		CreateEnv:    map[string]string{"OPENAI_API_KEY": "sk-secret-env-only"},
	}
	id, err := transport.Create(context.Background(), []string{"create", "-e", "OPENAI_API_KEY", "img"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id != "container-123" {
		t.Fatalf("container id = %q, want container-123", id)
	}
	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, "ARGS:create -e OPENAI_API_KEY img\n") {
		t.Fatalf("unexpected docker create args: %q", got)
	}
	if !strings.Contains(got, "ENV:sk-secret-env-only\n") {
		t.Fatalf("docker create did not receive CreateEnv: %q", got)
	}
}

func TestCommandAdapterRedactsStdoutAndStderrSecrets(t *testing.T) {
	opaqueSecret := "opaque-grant-secret-9f8e7d6c5b"
	adapter := NewCommandAdapter(CommandAdapterConfig{
		Name:    "redact-check",
		Program: "/bin/sh",
		Args:    []string{"-c", "echo \"out=$PENTEST_INTERFACE_TOKEN\"; echo \"err=$PENTEST_INTERFACE_TOKEN\" >&2"},
		Env:     map[string]string{"PENTEST_INTERFACE_TOKEN": opaqueSecret},
	})
	var stdoutText, stderrText strings.Builder
	err := adapter.Run(context.Background(), "goal", func(kind task.EventKind, payload task.EventPayload) {
		if kind != task.EventKindRuntimeOutput {
			return
		}
		text, _ := payload["text"].(string)
		switch payload["stream"] {
		case "stdout":
			stdoutText.WriteString(text)
		case "stderr":
			stderrText.WriteString(text)
		}
	})
	if err != nil {
		t.Fatalf("command adapter run: %v", err)
	}
	if strings.Contains(stdoutText.String(), opaqueSecret) || !strings.Contains(stdoutText.String(), "[REDACTED]") {
		t.Fatalf("stdout not redacted: %q", stdoutText.String())
	}
	if strings.Contains(stderrText.String(), opaqueSecret) || !strings.Contains(stderrText.String(), "[REDACTED]") {
		t.Fatalf("stderr not redacted: %q", stderrText.String())
	}
}

func TestDockerSandboxAdapterRedactsStdoutAndStderrSecrets(t *testing.T) {
	dir := t.TempDir()
	fakeDocker := filepath.Join(dir, "docker")
	opaqueSecret := "opaque-sandbox-secret-1a2b3c4d"
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  create) echo container-redact ;;\n" +
		"  start) echo \"out=" + opaqueSecret + "\"; echo \"err=" + opaqueSecret + "\" >&2 ;;\n" +
		"  rm) exit 0 ;;\n" +
		"esac\n"
	if err := os.WriteFile(fakeDocker, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}

	adapter := NewDockerSandboxAdapter(DockerSandboxConfig{
		Name:         "docker-redact-check",
		ContainerCLI: fakeDocker,
		Image:        "test-image",
		CreateArgs:   []string{"create", "test-image"},
		SecretValues: []string{opaqueSecret},
	})
	var stdoutText, stderrText strings.Builder
	err := adapter.Run(context.Background(), "goal", func(kind task.EventKind, payload task.EventPayload) {
		if kind != task.EventKindRuntimeOutput {
			return
		}
		text, _ := payload["text"].(string)
		switch payload["stream"] {
		case "stdout":
			stdoutText.WriteString(text)
		case "stderr":
			stderrText.WriteString(text)
		}
	})
	if err != nil {
		t.Fatalf("docker sandbox adapter run: %v", err)
	}
	if strings.Contains(stdoutText.String(), opaqueSecret) || !strings.Contains(stdoutText.String(), "[REDACTED]") {
		t.Fatalf("docker stdout not redacted: %q", stdoutText.String())
	}
	if strings.Contains(stderrText.String(), opaqueSecret) || !strings.Contains(stderrText.String(), "[REDACTED]") {
		t.Fatalf("docker stderr not redacted: %q", stderrText.String())
	}
}
