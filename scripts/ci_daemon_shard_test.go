package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Every daemon test must land in exactly one shard, whatever the weights say.
func TestDaemonShardsPartitionAllTests(t *testing.T) {
	root := repoRoot(t)
	dir := t.TempDir()
	var names []string
	for i := 0; i < 37; i++ {
		names = append(names, fmt.Sprintf("TestCase%02d", i))
	}
	listFile := filepath.Join(dir, "list.txt")
	if err := os.WriteFile(listFile, []byte(strings.Join(names, "\n")+"\nBenchmarkIgnored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// One very slow test plus some unknown (default-weight) tests.
	timesFile := filepath.Join(dir, "times.tsv")
	if err := os.WriteFile(timesFile, []byte("# header\nTestCase00\t100\nTestCase01\t30\nTestCase02\t30\nTestGone\t50\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const shards = 4
	seen := map[string]int{}
	var slowShard []string
	for i := 0; i < shards; i++ {
		cmd := exec.Command("bash", filepath.Join(root, "scripts", "ci-go-test-daemon-shard.sh"), fmt.Sprint(i), fmt.Sprint(shards))
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"CI_DAEMON_TEST_LIST_FILE="+listFile,
			"CI_DAEMON_TEST_TIMES="+timesFile,
			"CI_DAEMON_SHARD_DRY_RUN=1",
		)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("shard %d: %v", i, err)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		for _, name := range lines[1:] {
			seen[name]++
		}
		if strings.Contains(string(out), "\nTestCase00\n") || strings.HasSuffix(string(out), "\nTestCase00") {
			slowShard = lines[1:]
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if seen[name] != 1 {
			t.Fatalf("%s assigned to %d shards, want exactly 1", name, seen[name])
		}
	}
	if len(seen) != len(names) {
		t.Fatalf("unexpected tests scheduled: %v", seen)
	}
	if len(slowShard) != 1 {
		t.Fatalf("the 100s test should own its shard alone, got %v", slowShard)
	}
}
