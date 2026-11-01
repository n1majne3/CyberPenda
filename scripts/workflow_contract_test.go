package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflowContract struct {
	On          map[string]any `yaml:"on"`
	Concurrency struct {
		Group  string `yaml:"group"`
		Cancel any    `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Jobs map[string]struct {
		Steps []struct {
			Run string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func readWorkflowContract(t *testing.T, name string) workflowContract {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var workflow workflowContract
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow
}

// Select a job structurally so unrelated job order and YAML formatting are free
// to change. Existing contract assertions can inspect this normalized job.
func workflowJobText(t *testing.T, source, name string) string {
	t.Helper()
	var workflow struct {
		Jobs map[string]any `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(source), &workflow); err != nil {
		t.Fatal(err)
	}
	job, ok := workflow.Jobs[name]
	if !ok {
		t.Fatalf("missing workflow job %q", name)
	}
	data, err := yaml.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCIReplacesOlderRunsForTheSameRef(t *testing.T) {
	w := readWorkflowContract(t, "ci.yml")
	if !strings.Contains(w.Concurrency.Group, "github.ref") || !strings.Contains(w.Concurrency.Group, "github.workflow") {
		t.Fatal("CI must scope concurrency to the same workflow and ref")
	}
	// GitHub keeps only one pending run per group, so a shared main group would
	// drop older main commits. main must get a per-commit group.
	want := "github.ref == 'refs/heads/main' && github.sha || github.ref"
	if !strings.Contains(w.Concurrency.Group, want) {
		t.Fatalf("CI concurrency group must be per-sha on main (%q): got %q", want, w.Concurrency.Group)
	}
	cancel := fmt.Sprint(w.Concurrency.Cancel)
	// PRs cancel superseded runs; main must not, so every main commit is fully validated.
	if cancel == "true" {
		t.Fatal("CI must not cancel-in-progress unconditionally; main commits must run to completion")
	}
	if !strings.Contains(cancel, "github.ref") || !strings.Contains(cancel, "refs/heads/main") {
		t.Fatalf("CI cancel-in-progress must keep main runs: got %q", cancel)
	}
}

func TestHostedBundleIsManualOnly(t *testing.T) {
	w := readWorkflowContract(t, "build-tsecbench-hosted.yml")
	if _, ok := w.On["workflow_dispatch"]; !ok || len(w.On) != 1 {
		t.Fatal("Hosted delivery must have only an explicit manual trigger")
	}
}

func TestRuntimeSmokeRequiresCredentialsBeforeAnyBuild(t *testing.T) {
	w := readWorkflowContract(t, "smoke-runtime-manual.yml")
	if _, ok := w.On["workflow_dispatch"]; !ok || len(w.On) != 1 {
		t.Fatal("live Runtime smoke must be manual")
	}
	var preflight string
	for _, step := range w.Jobs["smoke-runtime-tasks"].Steps {
		if step.Run != "" {
			preflight = step.Run
			break
		}
	}
	if preflight == "" || strings.Contains(preflight, "make") {
		t.Fatal("credential preflight must precede build commands")
	}
	for _, tc := range []struct {
		name, anthropic, openai string
		valid                   bool
	}{
		{"neither", "", "", false}, {"missing Anthropic", "", "test-key", false},
		{"missing OpenAI", "test-key", "", false}, {"both", "test-key", "test-key", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", preflight)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "ANTHROPIC_AUTH_TOKEN=" + tc.anthropic, "OPENAI_API_KEY=" + tc.openai}
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.valid {
				t.Fatalf("credential preflight: err=%v, output=%s", err, out)
			}
			if strings.Contains(string(out), "test-key") {
				t.Fatal("preflight disclosed a credential")
			}
		})
	}
}

// ci-result is the single gate for branch protection; every CI job must feed it
// so a skipped or failed job can never be silently ignored.
func TestCIResultGateCoversEveryJob(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Jobs map[string]struct {
			Needs any    `yaml:"needs"`
			If    string `yaml:"if"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &w); err != nil {
		t.Fatal(err)
	}
	gate, ok := w.Jobs["ci-result"]
	if !ok {
		t.Fatal("CI must define the ci-result gate job")
	}
	if !strings.Contains(gate.If, "always()") {
		t.Fatal("ci-result must run even when upstream jobs are skipped")
	}
	needs := map[string]bool{}
	if list, ok := gate.Needs.([]any); ok {
		for _, n := range list {
			needs[fmt.Sprint(n)] = true
		}
	}
	for name := range w.Jobs {
		if name != "ci-result" && !needs[name] {
			t.Errorf("ci-result must need job %q", name)
		}
	}
}
