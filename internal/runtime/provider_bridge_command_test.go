package runtime

import (
	"reflect"
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
