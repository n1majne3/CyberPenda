package runtime

import (
	"bufio"
	"context"
	"encoding/json"
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

// piSessionTailFile tracks the read position of one tailed session file.
type piSessionTailFile struct {
	file   *os.File
	reader *bufio.Reader
	offset int64
}

// tailPiSession polls sessionDir for *.jsonl session files and follows every
// one of them, emitting each new line as a runtime_output event. Pi writes a
// subagent's transcript to its own newer session file, so following only the
// newest file would strand the parent's settle records (subagents:record)
// while a child is active. Tracking every file keeps parent and child lines
// observable. When ctx is cancelled it performs one final read pass across all
// files so lines written just before the runtime exited are drained rather
// than dropped, then returns.
func tailPiSession(ctx context.Context, sessionDir string, observe func(string), emit func(task.EventKind, task.EventPayload)) {
	tailed := map[string]*piSessionTailFile{}
	// headerParent caches each discovered file's parentSession so nesting depth
	// is classified once per file without re-reading headers every pass. Keys
	// and parentSession values are canonicalized so a Windows 8.3 short path in
	// a header matches the walker's long-form path for the same file.
	headerParent := map[string]string{}
	closeAll := func() {
		for _, tf := range tailed {
			_ = tf.file.Close()
		}
	}
	defer closeAll()

	for {
		stopping := false
		select {
		case <-ctx.Done():
			stopping = true
		case <-time.After(100 * time.Millisecond):
		}

		// Discover session files, opening any we have not tailed yet. Follow a
		// root session (no parentSession header) and the session files of its
		// top-level subagents (parentSession naming a root); skip deeper nested
		// files, whose settle records are never emitted (the extension reports
		// top-level agents only), so tailing them would grow open file handles
		// without adding attribution.
		for _, path := range listSessionFiles(sessionDir) {
			key := normalizePiSessionPath(path)
			if _, ok := tailed[key]; ok {
				continue
			}
			parent, ok := headerParent[key]
			if !ok {
				parent = piSessionParent(path)
				headerParent[key] = parent
			}
			if parent != "" {
				// A top-level subagent's parent is a root session (itself no
				// parentSession). If the parent names its own parent, this file
				// is a nested (grandchild) transcript — skip it. The header's
				// parentSession is canonicalized before lookup so a short-path
				// or aliased form still resolves to the tailed root.
				parentKey := normalizePiSessionPath(parent)
				grandparent, ok := headerParent[parentKey]
				if !ok {
					grandparent = piSessionParent(parent)
					headerParent[parentKey] = grandparent
				}
				if grandparent != "" {
					continue
				}
			}
			f, err := os.Open(path)
			if err != nil {
				continue
			}
			tailed[key] = &piSessionTailFile{file: f, reader: bufio.NewReader(f)}
		}

		// Drain every tailed file in canonical-path order so emission is
		// deterministic. The map key is the canonical form; the on-disk path to
		// open is recovered from it for the truncation check below.
		keys := make([]string, 0, len(tailed))
		for key := range tailed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			tf := tailed[key]
			for {
				line, err := tf.reader.ReadString('\n')
				if len(line) > 0 {
					tf.offset += int64(len(line))
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
			// If a file shrank (truncated/rotated in place), restart it from
			// the beginning on the next pass. The canonical key is a clean
			// absolute path, so it is safe to stat/open directly.
			if info, statErr := os.Stat(key); statErr == nil && info.Size() < tf.offset {
				_ = tf.file.Close()
				f, err := os.Open(key)
				if err == nil {
					tailed[key] = &piSessionTailFile{file: f, reader: bufio.NewReader(f)}
				} else {
					delete(tailed, key)
				}
			}
		}

		// A final drain pass has now emitted the last available lines; stop.
		if stopping {
			return
		}
	}
}

// normalizePiSessionPath canonicalizes a session-file path so the same file
// reached under different spellings — a Windows 8.3 short name, a symlinked
// directory, or redundant separators — maps to one key. EvalSymlinks resolves
// short names and symlink aliases; Clean collapses separators. When the path
// does not exist yet (EvalSymlinks fails), the cleaned form is used.
func normalizePiSessionPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

// piSessionParent returns the parentSession named by a session file's header,
// or "" when the file is a root session (no parentSession) or its header is
// not yet readable. A missing/unreadable header yields "" so a file is not
// misclassified before its header lands.
func piSessionParent(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && len(line) == 0 {
		return ""
	}
	var header struct {
		Type          string `json:"type"`
		ParentSession string `json:"parentSession"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(line)), &header) != nil {
		return ""
	}
	if header.Type != "session" {
		return ""
	}
	return strings.TrimSpace(header.ParentSession)
}

// listSessionFiles returns every *.jsonl file under dir, including
// cwd-specific child directories, sorted lexicographically. Pi names files
// with a leading ISO timestamp, so the order is also chronological.
func listSessionFiles(dir string) []string {
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
		return nil
	}
	sort.Strings(paths)
	return paths
}
