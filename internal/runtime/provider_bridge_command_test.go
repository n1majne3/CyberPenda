package runtime

import (
	"reflect"
	"strings"
	"testing"
)

func TestRewriteDockerCreateCommandKeepsNonPTYCreateAndReplacesProvider(t *testing.T) {
	got, err := RewriteDockerCreateCommand([]string{"create", "--cidfile", "/tmp/x", "image", "codex", "exec", "--model", "gpt", "goal"}, "codex", []string{"/usr/local/bin/pentest-provider-bridge", "--provider", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"create", "-i", "--cidfile", "/tmp/x", "image", "/usr/local/bin/pentest-provider-bridge", "--provider", "codex"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rewritten create args = %#v, want %#v", got, want)
	}
	for _, arg := range got {
		if arg == "-t" || arg == "--tty" {
			t.Fatal("bridge rewrite introduced a terminal")
		}
	}
}

func TestRewriteDockerCreateCommandReplacesBarePiCommand(t *testing.T) {
	got, err := RewriteDockerCreateCommand(
		[]string{"create", "--cidfile", "/tmp/x", "image", "pi", "--model", "deepseek-v4-flash", "-p", "goal"},
		"pi",
		[]string{"/usr/local/bin/pentest-provider-bridge", "--provider", "pi", "--", "pi", "--mode", "rpc", "--session-id", "task-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"create", "-i", "--cidfile", "/tmp/x", "image",
		"/usr/local/bin/pentest-provider-bridge", "--provider", "pi", "--", "pi", "--mode", "rpc", "--session-id", "task-1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rewritten create args = %#v, want %#v", got, want)
	}
}

func TestRewriteDockerCreateCommandRejectsPiBootstrapShellWrapper(t *testing.T) {
	// Persistent sessions must not ship the one-shot sh -c bootstrap wrapper:
	// the bridge rewrite looks for a bare "pi" image command token.
	_, err := RewriteDockerCreateCommand(
		[]string{"create", "image", "sh", "-c", "exec pi '--model' 'm' '-p' 'goal'"},
		"pi",
		[]string{"/usr/local/bin/pentest-provider-bridge", "--provider", "pi"},
	)
	if err == nil {
		t.Fatal("expected rewrite to reject pi sh -c bootstrap wrapper")
	}
	if !strings.Contains(err.Error(), `provider command "pi" was not found`) {
		t.Fatalf("error = %v, want missing pi command", err)
	}
}
