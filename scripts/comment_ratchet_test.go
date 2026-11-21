package scripts_test

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"pentest/internal/commentscan"
)

const commentBaselineFile = "scripts/comment-baseline.tsv"

func TestGoCommentRatchet(t *testing.T) {
	root := repoRoot(t)
	current := goCommentLines(t, root)
	if os.Getenv("UPDATE_COMMENT_BASELINE") == "1" {
		writeCommentBaseline(t, filepath.Join(root, commentBaselineFile), current)
		return
	}
	baseline := readCommentBaseline(t, filepath.Join(root, commentBaselineFile))

	var failures []string
	for path, lines := range current {
		if allowed := baseline[path]; lines > allowed {
			failures = append(failures, fmt.Sprintf("%s: %d comment lines, baseline allows %d", path, lines, allowed))
		}
	}
	for path, allowed := range baseline {
		if lines := current[path]; lines < allowed {
			failures = append(failures, fmt.Sprintf("%s: %d comment lines, baseline is stale at %d", path, lines, allowed))
		}
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		t.Fatalf("Go comment ratchet failed. Do not add comments: record decisions in docs/adr/ and see .claude/skills/delete-comment/SKILL.md.\n"+
			"After you delete comments, run: UPDATE_COMMENT_BASELINE=1 go test ./scripts -run TestGoCommentRatchet\n%s",
			strings.Join(failures, "\n"))
	}
}

func goCommentLines(t *testing.T, root string) map[string]int {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "--", "*.go").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	counts := map[string]int{}
	for _, path := range strings.Fields(string(out)) {
		src, err := os.ReadFile(filepath.Join(root, path))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		comments, err := commentscan.ScanGo(path, src)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if lines := commentscan.Lines(comments); lines > 0 {
			counts[path] = lines
		}
	}
	return counts
}

func readCommentBaseline(t *testing.T, path string) map[string]int {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open baseline: %v", err)
	}
	defer file.Close()
	baseline := map[string]int{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) != 2 {
			t.Fatalf("baseline line %q: want path<TAB>lines", scanner.Text())
		}
		lines, err := strconv.Atoi(fields[1])
		if err != nil || lines <= 0 {
			t.Fatalf("baseline line %q: want a positive line count", scanner.Text())
		}
		baseline[fields[0]] = lines
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	return baseline
}

func writeCommentBaseline(t *testing.T, path string, counts map[string]int) {
	t.Helper()
	paths := make([]string, 0, len(counts))
	for p := range counts {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "%s\t%d\n", p, counts[p])
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
}
