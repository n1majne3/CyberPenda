package runtime

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pentest/internal/runtimeoutput"
	"pentest/internal/task"
)

// piSessionTailAdapter wraps a runtime Adapter and, in parallel with the
// runtime process, tails the Pi session jsonl file so the daemon sees Pi's
// real-time progress. Pi writes its activity to a session file rather than
// stdout, so without this tail the task timeline is empty until Pi exits.
//
// Each appended jsonl line is re-emitted as a runtime_output event carrying
// the raw JSON text and stream "pi_session". The existing transcript parser
// then converts those lines exactly like provider stdout output.
type piSessionTailAdapter struct {
	inner      Adapter
	sessionDir string
}

// NewPiSessionTailAdapter wraps inner with a Pi session jsonl tailer rooted
// at sessionDir (the per-cwd sessions directory under PI_CODING_AGENT_DIR).
func NewPiSessionTailAdapter(inner Adapter, sessionDir string) Adapter {
	return &piSessionTailAdapter{inner: inner, sessionDir: sessionDir}
}

func (a *piSessionTailAdapter) Name() string { return a.inner.Name() }

func (a *piSessionTailAdapter) Run(ctx context.Context, goal string, emit func(task.EventKind, task.EventPayload)) error {
	tailDone := make(chan struct{})
	go func() {
		defer close(tailDone)
		tailPiSession(ctx, a.sessionDir, emit)
	}()
	// The inner Run blocks until the runtime exits; when it does, ctx is
	// cancelled by the harness and the tail goroutine winds down.
	return a.inner.Run(ctx, goal, emit)
}

// tailPiSession polls sessionDir until a *.jsonl file appears, then follows it
// line-by-line, emitting each new line as a runtime_output event. It returns
// when ctx is cancelled.
func tailPiSession(ctx context.Context, sessionDir string, emit func(task.EventKind, task.EventPayload)) {
	currentPath := ""
	var reader *bufio.Reader
	var file *os.File
	var offset int64

	for {
		select {
		case <-ctx.Done():
			if file != nil {
				_ = file.Close()
			}
			return
		case <-time.After(100 * time.Millisecond):
		}

		// (Re)resolve the newest session file when we don't have one yet, or
		// when the current file has been rotated/replaced.
		latest, ok := newestSessionFile(sessionDir)
		if !ok {
			continue
		}
		if currentPath != latest {
			if file != nil {
				_ = file.Close()
			}
			f, err := os.Open(latest)
			if err != nil {
				continue
			}
			file = f
			currentPath = latest
			offset = 0
			reader = bufio.NewReader(file)
		}

		// Read all complete lines currently available.
		for {
			line, err := reader.ReadString('\n')
			if len(line) > 0 {
				offset += int64(len(line))
				if trimmed := strings.TrimRight(line, "\n"); trimmed != "" && !runtimeoutput.ShouldIgnoreForStorage(trimmed) {
					emit(task.EventKindRuntimeOutput, task.EventPayload{
						"stream": "pi_session",
						"text":   trimmed,
					})
				}
			}
			if err != nil {
				break
			}
		}

		// If the file shrank (truncated), reset to its current end next round.
		if info, err := os.Stat(currentPath); err == nil && info.Size() < offset {
			offset = 0
			if file != nil {
				_ = file.Close()
			}
			f, err := os.Open(currentPath)
			if err == nil {
				file = f
				reader = bufio.NewReader(file)
			}
		}
	}
}

// newestSessionFile returns the lexicographically newest *.jsonl file under
// dir (Pi names files with a leading ISO timestamp, so newest == most recent).
// ok is false when the directory or no matching file exists yet.
func newestSessionFile(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", false
	}
	sort.Strings(names)
	return filepath.Join(dir, names[len(names)-1]), true
}
