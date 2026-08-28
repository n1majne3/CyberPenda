package runtime

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"pentest/internal/adapters"
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
	mu         sync.Mutex
	record     func(NativeSessionMetadata) error
}

// NewPiSessionTailAdapter wraps inner with a Pi session jsonl tailer rooted
// at sessionDir (the per-cwd sessions directory under PI_CODING_AGENT_DIR).
func NewPiSessionTailAdapter(inner Adapter, sessionDir string) Adapter {
	return &piSessionTailAdapter{inner: inner, sessionDir: sessionDir}
}

func (a *piSessionTailAdapter) Name() string { return a.inner.Name() }

func (a *piSessionTailAdapter) SetMetadataRecorder(record func(NativeSessionMetadata) error) {
	a.mu.Lock()
	a.record = record
	a.mu.Unlock()
	if inner, ok := a.inner.(metadataRecordingAdapter); ok {
		inner.SetMetadataRecorder(record)
	}
}

func (a *piSessionTailAdapter) recordRuntimeLineMetadata(line string) {
	metadata := NativeSessionMetadataFromRuntimeLine(line)
	if metadata.NativeSessionID == "" && metadata.NativeSessionPath == "" && metadata.ContainerID == "" {
		return
	}
	a.mu.Lock()
	record := a.record
	a.mu.Unlock()
	if record != nil {
		_ = record(metadata)
	}
}

func (a *piSessionTailAdapter) Run(ctx context.Context, goal string, emit func(task.EventKind, task.EventPayload)) error {
	// Derive a child context so the tailer is always stopped when the inner
	// runtime exits, even on normal completion where the harness leaves the
	// parent context live. Without this the polling goroutine would leak for
	// the lifetime of the daemon on every finished Pi session.
	tailCtx, cancelTail := context.WithCancel(ctx)
	tailDone := make(chan struct{})
	go func() {
		defer close(tailDone)
		tailPiSession(tailCtx, a.sessionDir, a.recordRuntimeLineMetadata, emit)
	}()
	// The inner Run blocks until the runtime exits. Once it returns, stop the
	// tailer and wait for its final drain so the last session lines are emitted
	// before Run reports completion.
	err := a.inner.Run(ctx, goal, emit)
	cancelTail()
	<-tailDone
	return err
}

// tailPiSession polls sessionDir until a *.jsonl file appears, then follows it
// line-by-line, emitting each new line as a runtime_output event. When ctx is
// cancelled it performs one final read pass so lines written just before the
// runtime exited are drained rather than dropped, then returns.
func tailPiSession(ctx context.Context, sessionDir string, observe func(string), emit func(task.EventKind, task.EventPayload)) {
	currentPath := ""
	var reader *bufio.Reader
	var file *os.File
	var offset int64

	for {
		stopping := false
		select {
		case <-ctx.Done():
			stopping = true
		case <-time.After(100 * time.Millisecond):
		}

		// (Re)resolve the newest session file when we don't have one yet, or
		// when the current file has been rotated/replaced.
		latest, ok := newestSessionFile(sessionDir)
		if !ok {
			if stopping {
				if file != nil {
					_ = file.Close()
				}
				return
			}
			continue
		}
		if currentPath != latest {
			if file != nil {
				_ = file.Close()
			}
			f, err := os.Open(latest)
			if err != nil {
				if stopping {
					return
				}
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
				if trimmed := strings.TrimRight(line, "\n"); trimmed != "" {
					if observe != nil {
						observe(trimmed)
					}
					if runtimeoutput.ShouldIgnoreForStorage(trimmed) {
						continue
					}
					emit(task.EventKindRuntimeOutput, task.EventPayload(adapters.Redact(map[string]any{
						"stream": "pi_session",
						"text":   trimmed,
					})))
				}
			}
			if err != nil {
				break
			}
		}

		// A final drain pass has now emitted the last available lines; stop.
		if stopping {
			if file != nil {
				_ = file.Close()
			}
			return
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
// dir, including cwd-specific child directories. Pi names files with a leading
// ISO timestamp, so newest == most recent. ok is false when the directory or no
// matching file exists yet.
func newestSessionFile(dir string) (string, bool) {
	var paths []string
	if err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		return "", false
	}
	if len(paths) == 0 {
		return "", false
	}
	sort.Strings(paths)
	return paths[len(paths)-1], true
}
