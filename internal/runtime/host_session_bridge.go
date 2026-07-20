package runtime

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// HostSessionBridgeConfig describes a Task-owned host provider process. The
// process is launched with its own process group so Close can reap descendants
// without touching unrelated daemon processes.
type HostSessionBridgeConfig struct {
	TaskID       string
	Program      string
	Args         []string
	Workdir      string
	Env          map[string]string
	Diagnostics  func(string)
	ProtocolEmit func(SandboxBridgeEvent)
	// Starter is an optional process seam for tests. Production uses the real
	// local process group starter when Starter is nil.
	Starter HostProcessStarter
}

// HostProcessSpec is the concrete local process request.
type HostProcessSpec struct {
	Program string
	Args    []string
	Workdir string
	Env     map[string]string
}

// HostProcessHandle is one started host process with protocol IO and group
// cleanup. Wait is optional; KillProcessGroup must terminate the whole tree.
type HostProcessHandle struct {
	IO               SandboxBridgeIO
	ProcessGroupID   int
	KillProcessGroup func(context.Context) error
}

// HostProcessStarter is the process lifecycle seam used by HostSessionBridge.
type HostProcessStarter interface {
	Start(context.Context, HostProcessSpec) (HostProcessHandle, error)
}

var (
	ErrHostBridgeClosed       = errors.New("host session bridge is closed")
	ErrHostBridgeStarted      = errors.New("host session bridge is already started")
	ErrHostBridgeTaskMismatch = errors.New("host session bridge task mismatch")
	ErrHostBridgeInvalid      = errors.New("invalid host session bridge request")
)

// HostSessionBridge owns one host process and its bidirectional protocol for a
// Task. Continuations are only request-level pins and never create processes.
type HostSessionBridge struct {
	config HostSessionBridgeConfig

	mu            sync.Mutex
	writeMu       sync.Mutex
	state         string
	pgid          int
	stdin         io.WriteCloser
	stdout        io.ReadCloser
	wait          func() error
	killGroup     func(context.Context) error
	pending       map[string]*bridgePending
	completed     map[string]bridgeCompletion
	requests      map[string][sha256.Size]byte
	continuation  string
	closed        chan struct{}
	terminated    chan struct{}
	terminateOnce sync.Once
	closeOnce     sync.Once
	cleanupErr    error
}

// HostSessionBridgeRegistry owns at most one host bridge per Task.
type HostSessionBridgeRegistry struct {
	mu      sync.Mutex
	bridges map[string]*HostSessionBridge
}

func NewHostSessionBridgeRegistry() *HostSessionBridgeRegistry {
	return &HostSessionBridgeRegistry{bridges: map[string]*HostSessionBridge{}}
}

func (r *HostSessionBridgeRegistry) Bind(ctx context.Context, taskID, continuationID string, create func() (*HostSessionBridge, error)) (*HostSessionBridge, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || create == nil {
		return nil, ErrHostBridgeInvalid
	}
	r.mu.Lock()
	if existing := r.bridges[taskID]; existing != nil {
		r.mu.Unlock()
		if err := existing.BindContinuation(continuationID); err != nil {
			return nil, err
		}
		return existing, nil
	}
	bridge, err := create()
	if err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if bridge.TaskID() != taskID {
		r.mu.Unlock()
		_ = bridge.Close(ctx)
		return nil, ErrHostBridgeTaskMismatch
	}
	if err := bridge.BindContinuation(continuationID); err != nil {
		r.mu.Unlock()
		_ = bridge.Close(ctx)
		return nil, err
	}
	r.bridges[taskID] = bridge
	r.mu.Unlock()
	return bridge, nil
}

func (r *HostSessionBridgeRegistry) Get(taskID string) (*HostSessionBridge, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	bridge, ok := r.bridges[strings.TrimSpace(taskID)]
	return bridge, ok
}

func (r *HostSessionBridgeRegistry) CloseTask(ctx context.Context, taskID string) error {
	r.mu.Lock()
	bridge := r.bridges[strings.TrimSpace(taskID)]
	delete(r.bridges, strings.TrimSpace(taskID))
	r.mu.Unlock()
	if bridge == nil {
		return nil
	}
	return bridge.Close(ctx)
}

func (r *HostSessionBridgeRegistry) CloseAll(ctx context.Context) error {
	r.mu.Lock()
	bridges := make([]*HostSessionBridge, 0, len(r.bridges))
	for _, bridge := range r.bridges {
		bridges = append(bridges, bridge)
	}
	r.bridges = map[string]*HostSessionBridge{}
	r.mu.Unlock()
	var cleanupErr error
	for _, bridge := range bridges {
		if err := bridge.Close(ctx); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

// NewHostSessionBridge creates an idle Task-owned host bridge.
func NewHostSessionBridge(config HostSessionBridgeConfig) (*HostSessionBridge, error) {
	if strings.TrimSpace(config.TaskID) == "" {
		return nil, fmt.Errorf("host bridge TaskID is required")
	}
	if strings.TrimSpace(config.Program) == "" {
		return nil, fmt.Errorf("host bridge program is required")
	}
	if config.Starter == nil {
		config.Starter = LocalHostProcessStarter{}
	}
	config.Args = append([]string(nil), config.Args...)
	return &HostSessionBridge{
		config:     config,
		state:      "idle",
		pending:    map[string]*bridgePending{},
		completed:  map[string]bridgeCompletion{},
		requests:   map[string][sha256.Size]byte{},
		closed:     make(chan struct{}),
		terminated: make(chan struct{}),
	}, nil
}

// Start launches the host process in a dedicated process group and attaches
// protocol IO. A failed start does not leave a live process group.
func (b *HostSessionBridge) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.state != "idle" {
		err := ErrHostBridgeStarted
		if b.state == "closed" {
			err = ErrHostBridgeClosed
		}
		b.mu.Unlock()
		return err
	}
	b.state = "starting"
	starter := b.config.Starter
	spec := HostProcessSpec{
		Program: b.config.Program,
		Args:    append([]string(nil), b.config.Args...),
		Workdir: b.config.Workdir,
		Env:     b.config.Env,
	}
	b.mu.Unlock()

	handle, err := starter.Start(ctx, spec)
	if err != nil {
		b.mu.Lock()
		b.state = "idle"
		b.mu.Unlock()
		return fmt.Errorf("start host bridge process: %w", err)
	}
	if handle.IO.Stdin == nil || handle.IO.Stdout == nil {
		if handle.KillProcessGroup != nil {
			_ = handle.KillProcessGroup(context.Background())
		}
		b.mu.Lock()
		b.state = "idle"
		b.mu.Unlock()
		return fmt.Errorf("start host bridge: stdin and stdout are required")
	}
	b.mu.Lock()
	b.stdin, b.stdout, b.wait, b.killGroup, b.pgid, b.state =
		handle.IO.Stdin, handle.IO.Stdout, handle.IO.Wait, handle.KillProcessGroup, handle.ProcessGroupID, "running"
	b.mu.Unlock()
	go b.readLoop(handle.IO.Stdout)
	if handle.IO.Diagnostics != nil && b.config.Diagnostics != nil {
		go b.diagnosticLoop(handle.IO.Diagnostics)
	}
	return nil
}

func (b *HostSessionBridge) ProcessGroupID() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pgid
}

func (b *HostSessionBridge) TaskID() string { return b.config.TaskID }

func (b *HostSessionBridge) BindContinuation(continuationID string) error {
	continuationID = strings.TrimSpace(continuationID)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == "closed" {
		return ErrHostBridgeClosed
	}
	b.continuation = continuationID
	return nil
}

func (b *HostSessionBridge) Send(ctx context.Context, request SandboxBridgeRequest) (SandboxBridgeResponse, error) {
	request.JSONRPC = "2.0"
	request.ID = strings.TrimSpace(request.ID)
	request.TaskID = strings.TrimSpace(request.TaskID)
	if request.ID == "" || strings.TrimSpace(request.Method) == "" {
		return SandboxBridgeResponse{}, ErrHostBridgeInvalid
	}
	if request.TaskID == "" {
		request.TaskID = b.config.TaskID
	}
	if request.TaskID != b.config.TaskID {
		return SandboxBridgeResponse{}, ErrHostBridgeTaskMismatch
	}
	b.mu.Lock()
	if b.state != "running" {
		b.mu.Unlock()
		return SandboxBridgeResponse{}, ErrHostBridgeClosed
	}
	if request.ContinuationID == "" {
		request.ContinuationID = b.continuation
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		b.mu.Unlock()
		return SandboxBridgeResponse{}, ErrHostBridgeInvalid
	}
	fingerprint := sha256.Sum256(encoded)
	if prior, ok := b.requests[request.ID]; ok && prior != fingerprint {
		b.mu.Unlock()
		return SandboxBridgeResponse{}, ErrSandboxBridgeRequestConflict
	}
	if done, ok := b.completed[request.ID]; ok {
		b.mu.Unlock()
		return done.response, done.err
	}
	if prior, ok := b.pending[request.ID]; ok {
		b.mu.Unlock()
		select {
		case <-prior.done:
			return prior.result.response, prior.result.err
		case <-ctx.Done():
			return SandboxBridgeResponse{}, ctx.Err()
		}
	}
	pending := &bridgePending{done: make(chan struct{})}
	b.pending[request.ID] = pending
	b.requests[request.ID] = fingerprint
	stdin := b.stdin
	b.mu.Unlock()

	frame := append(encoded, '\n')
	b.writeMu.Lock()
	_, err = stdin.Write(frame)
	b.writeMu.Unlock()
	if err != nil {
		b.finish(request.ID, SandboxBridgeResponse{}, fmt.Errorf("write host bridge request: %w", err))
	}
	select {
	case <-pending.done:
		return pending.result.response, pending.result.err
	case <-ctx.Done():
		return SandboxBridgeResponse{}, ctx.Err()
	}
}

func (b *HostSessionBridge) readLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event SandboxBridgeEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.ID != "" {
			var responseErr error
			if len(event.Error) > 0 && string(event.Error) != "null" {
				responseErr = &SandboxBridgeRPCError{RequestID: event.ID}
			}
			b.finish(event.ID, SandboxBridgeResponse{
				JSONRPC: event.JSONRPC, ID: event.ID, Result: event.Result, Error: event.Error,
			}, responseErr)
			continue
		}
		if b.config.ProtocolEmit != nil {
			b.config.ProtocolEmit(event)
		}
	}
	b.mu.Lock()
	// Only unexpected process/protocol exit signals Terminated. Explicit Close
	// sets state to closed first; the resulting stream end must not look like
	// an unexpected exit.
	unexpected := b.state == "running"
	if unexpected {
		b.state = "failed"
	}
	b.mu.Unlock()
	b.failPending(fmt.Errorf("host bridge protocol stream ended"))
	if unexpected {
		b.signalTerminated()
	}
}

func (b *HostSessionBridge) diagnosticLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		if b.config.Diagnostics != nil {
			// Match sandbox bridge redaction so host provider diagnostics cannot
			// leak secrets into daemon logs.
			b.config.Diagnostics(redactedDockerString(scanner.Text()))
		}
	}
}

func (b *HostSessionBridge) finish(id string, response SandboxBridgeResponse, err error) {
	b.mu.Lock()
	pending, ok := b.pending[id]
	if !ok {
		b.mu.Unlock()
		return
	}
	delete(b.pending, id)
	b.completed[id] = bridgeCompletion{response: response, err: err}
	b.mu.Unlock()
	pending.result = bridgeCompletion{response: response, err: err}
	close(pending.done)
}

func (b *HostSessionBridge) failPending(err error) {
	b.mu.Lock()
	pending := make(map[string]*bridgePending, len(b.pending))
	for id, item := range b.pending {
		pending[id] = item
		b.completed[id] = bridgeCompletion{err: err}
		delete(b.pending, id)
	}
	b.mu.Unlock()
	for _, item := range pending {
		item.result = bridgeCompletion{err: err}
		close(item.done)
	}
}

// Close terminates the host process group exactly once and waits for exit.
func (b *HostSessionBridge) Close(ctx context.Context) error {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.state = "closed"
		stdin, wait, killGroup := b.stdin, b.wait, b.killGroup
		b.mu.Unlock()
		if stdin != nil {
			_ = stdin.Close()
		}
		if killGroup != nil {
			if err := killGroup(ctx); err != nil {
				b.cleanupErr = err
			}
		}
		if wait != nil {
			_ = wait()
		}
		b.failPending(ErrHostBridgeClosed)
		close(b.closed)
	})
	return b.cleanupErr
}

func (b *HostSessionBridge) Closed() <-chan struct{} { return b.closed }

// Terminated is closed once when the host process protocol stream ends
// unexpectedly. It is distinct from Closed, which fires only after explicit
// process-group cleanup.
func (b *HostSessionBridge) Terminated() <-chan struct{} { return b.terminated }

func (b *HostSessionBridge) signalTerminated() {
	b.terminateOnce.Do(func() {
		close(b.terminated)
	})
}

// HostProcessGroupMetadataPrefix marks a durable Host process-group identity
// stored in NativeSessionMetadata.ContainerID so daemon restart can reap the
// tree without inventing a second metadata channel.
const HostProcessGroupMetadataPrefix = "host-pgid:"

// FormatHostProcessGroupID returns the durable metadata token for a host
// process group.
func FormatHostProcessGroupID(pgid int) string {
	if pgid <= 0 {
		return ""
	}
	return fmt.Sprintf("%s%d", HostProcessGroupMetadataPrefix, pgid)
}

// ParseHostProcessGroupID extracts a host process group id from durable
// metadata. ok is false when the value is not a host process-group token.
func ParseHostProcessGroupID(value string) (pgid int, ok bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, HostProcessGroupMetadataPrefix) {
		return 0, false
	}
	raw := strings.TrimSpace(strings.TrimPrefix(value, HostProcessGroupMetadataPrefix))
	if raw == "" {
		return 0, false
	}
	var n int
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	if n <= 0 {
		return 0, false
	}
	return n, true
}

// KillHostProcessGroup terminates every process in the group. Used by live
// bridge Close and by daemon-restart reconciliation of durable host metadata.
func KillHostProcessGroup(ctx context.Context, pgid int) error {
	if pgid <= 0 {
		return fmt.Errorf("invalid host process group id")
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := syscall.Kill(-pgid, 0); err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			return ctx.Err()
		case <-timer.C:
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			return nil
		case <-ticker.C:
		}
	}
}

// LocalHostProcessStarter launches a host process with Setpgid so Close can
// signal the entire descendant tree.
type LocalHostProcessStarter struct{}

func (LocalHostProcessStarter) Start(_ context.Context, spec HostProcessSpec) (HostProcessHandle, error) {
	if strings.TrimSpace(spec.Program) == "" {
		return HostProcessHandle{}, fmt.Errorf("host process program is required")
	}
	cmd := exec.Command(spec.Program, spec.Args...)
	cmd.Dir = spec.Workdir
	cmd.Env = os.Environ()
	for key, value := range spec.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return HostProcessHandle{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return HostProcessHandle{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return HostProcessHandle{}, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return HostProcessHandle{}, err
	}
	pgid := cmd.Process.Pid
	if actual, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		pgid = actual
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var waitOnce sync.Once
	var waitErr error
	wait := func() error {
		waitOnce.Do(func() {
			waitErr = <-waitCh
			// Root exit must still reap descendants that may outlive the leader.
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		})
		return waitErr
	}
	killGroup := func(ctx context.Context) error {
		// Best-effort graceful stop, then force the whole group.
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_ = wait()
			close(done)
		}()
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			<-done
			return ctx.Err()
		case <-timer.C:
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			<-done
			return nil
		}
	}
	return HostProcessHandle{
		IO: SandboxBridgeIO{
			Stdin: stdin, Stdout: stdout, Diagnostics: stderr, Wait: wait,
		},
		ProcessGroupID:   pgid,
		KillProcessGroup: killGroup,
	}, nil
}
