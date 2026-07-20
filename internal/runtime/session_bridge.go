package runtime

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// SandboxBridgeIO is the daemon-owned, non-PTY channel attached to one
// provider process. Stdout is protocol-only; diagnostics are intentionally
// separate and are never parsed as provider messages.
type SandboxBridgeIO struct {
	Stdin       io.WriteCloser
	Stdout      io.ReadCloser
	Diagnostics io.ReadCloser
	Wait        func() error
}

// SandboxBridgeDocker is the small Docker lifecycle seam used by the bridge.
// Implementations must attach stdin/stdout without allocating a terminal.
type SandboxBridgeDocker interface {
	Create(context.Context, []string) (string, error)
	Start(context.Context, string) (SandboxBridgeIO, error)
	Stop(context.Context, string) error
	Remove(context.Context, string) error
}

// SandboxBridgeConfig describes a Task-owned provider container. CreateArgs
// must be docker create arguments and include -i/--interactive, but never -t.
type SandboxBridgeConfig struct {
	TaskID       string
	CreateArgs   []string
	Diagnostics  func(string)
	ProtocolEmit func(SandboxBridgeEvent)
}

// SandboxBridgeRequest is one task-bound JSON-RPC control frame. Params may
// contain provider-specific data; it is sent on the private bridge only and is
// never copied into lifecycle events.
type SandboxBridgeRequest struct {
	JSONRPC        string          `json:"jsonrpc"`
	ID             string          `json:"id"`
	TaskID         string          `json:"task_id"`
	ContinuationID string          `json:"continuation_id,omitempty"`
	Method         string          `json:"method"`
	Params         json.RawMessage `json:"params,omitempty"`
}

// SandboxBridgeResponse is a JSON-RPC response correlated by request ID.
type SandboxBridgeResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// SandboxBridgeEvent is a provider notification or response observed on the
// protocol stream. Raw protocol payload is available to the bridge consumer,
// while persisted Task events should use only redacted correlation fields.
type SandboxBridgeEvent struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      string          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

var (
	ErrSandboxBridgeClosed          = errors.New("sandbox session bridge is closed")
	ErrSandboxBridgeStarted         = errors.New("sandbox session bridge is already started")
	ErrSandboxBridgeTaskMismatch    = errors.New("sandbox session bridge task mismatch")
	ErrSandboxBridgeInvalid         = errors.New("invalid sandbox session bridge request")
	ErrSandboxBridgeNoInteractive   = errors.New("sandbox bridge requires interactive stdin without a terminal")
	ErrSandboxBridgeRequestConflict = errors.New("sandbox bridge request id is already bound to different content")
)

// SandboxBridgeRPCError reports a provider RPC failure without exposing its
// potentially sensitive wire payload through daemon errors.
type SandboxBridgeRPCError struct{ RequestID string }

func (e *SandboxBridgeRPCError) Error() string {
	return fmt.Sprintf("sandbox bridge request %q failed", e.RequestID)
}

type bridgePending struct {
	done   chan struct{}
	result bridgeCompletion
}

type bridgeCompletion struct {
	response SandboxBridgeResponse
	err      error
}

// SandboxSessionBridge owns one container and its bidirectional protocol for a
// Task. Continuations are only request-level pins and never create containers.
type SandboxSessionBridge struct {
	docker SandboxBridgeDocker
	config SandboxBridgeConfig

	mu           sync.Mutex
	writeMu      sync.Mutex
	state        string
	containerID  string
	stdin        io.WriteCloser
	stdout       io.ReadCloser
	wait         func() error
	pending      map[string]*bridgePending
	completed    map[string]bridgeCompletion
	requests     map[string][sha256.Size]byte
	continuation   string
	closed         chan struct{}
	terminated     chan struct{}
	terminateOnce  sync.Once
	closeOnce      sync.Once
	cleanupErr     error
}

// DockerCLISandboxBridgeDocker is the production Docker CLI transport. The
// attached command uses -a and -i only; no API exposes attach or exec above
// this daemon-owned lifecycle seam.
type DockerCLISandboxBridgeDocker struct {
	ContainerCLI string
}

func (d DockerCLISandboxBridgeDocker) cli() string {
	if cli := strings.TrimSpace(d.ContainerCLI); cli != "" {
		return cli
	}
	return "docker"
}

func (d DockerCLISandboxBridgeDocker) Create(ctx context.Context, args []string) (string, error) {
	out, err := exec.CommandContext(ctx, d.cli(), args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (d DockerCLISandboxBridgeDocker) Start(_ context.Context, containerID string) (SandboxBridgeIO, error) {
	// The attach process is Task-owned and must outlive any request or
	// Continuation context passed to Start. Close owns its termination.
	cmd := exec.Command(d.cli(), DockerSandboxBridgeStartArgs(containerID)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return SandboxBridgeIO{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return SandboxBridgeIO{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return SandboxBridgeIO{}, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return SandboxBridgeIO{}, err
	}
	return SandboxBridgeIO{Stdin: stdin, Stdout: stdout, Diagnostics: stderr, Wait: cmd.Wait}, nil
}

// DockerSandboxBridgeStartArgs exposes the security-sensitive attach argv to
// regression tests. It deliberately contains interactive attach but no TTY.
func DockerSandboxBridgeStartArgs(containerID string) []string {
	return []string{"start", "-a", "-i", strings.TrimSpace(containerID)}
}

func (d DockerCLISandboxBridgeDocker) Stop(_ context.Context, containerID string) error {
	return StopDockerContainer(d.cli(), containerID, dockerStopGrace)
}

func (d DockerCLISandboxBridgeDocker) Remove(_ context.Context, containerID string) error {
	return RemoveDockerContainer(d.cli(), containerID)
}

// SandboxSessionBridgeRegistry owns bridges by Task identity. Binding a new
// Continuation returns the same bridge and therefore cannot create a second
// provider container.
type SandboxSessionBridgeRegistry struct {
	mu      sync.Mutex
	bridges map[string]*SandboxSessionBridge
}

func NewSandboxSessionBridgeRegistry() *SandboxSessionBridgeRegistry {
	return &SandboxSessionBridgeRegistry{bridges: map[string]*SandboxSessionBridge{}}
}

// Bind stores the first bridge for a Task, or reuses the existing one. A bridge
// created by a losing concurrent caller is closed before Bind returns.
func (r *SandboxSessionBridgeRegistry) Bind(ctx context.Context, taskID, continuationID string, create func() (*SandboxSessionBridge, error)) (*SandboxSessionBridge, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || create == nil {
		return nil, ErrSandboxBridgeInvalid
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
		return nil, ErrSandboxBridgeTaskMismatch
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

func (r *SandboxSessionBridgeRegistry) Get(taskID string) (*SandboxSessionBridge, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	bridge, ok := r.bridges[strings.TrimSpace(taskID)]
	return bridge, ok
}

// CloseTask drops ownership before cleanup, so no new control can acquire the
// stale bridge while Docker shutdown is in progress.
func (r *SandboxSessionBridgeRegistry) CloseTask(ctx context.Context, taskID string) error {
	r.mu.Lock()
	bridge := r.bridges[strings.TrimSpace(taskID)]
	delete(r.bridges, strings.TrimSpace(taskID))
	r.mu.Unlock()
	if bridge == nil {
		return nil
	}
	return bridge.Close(ctx)
}

// CloseAll fails closed on daemon shutdown/startup reconciliation by dropping
// every in-memory owner and cleaning each provider container.
func (r *SandboxSessionBridgeRegistry) CloseAll(ctx context.Context) error {
	r.mu.Lock()
	bridges := make([]*SandboxSessionBridge, 0, len(r.bridges))
	for _, bridge := range r.bridges {
		bridges = append(bridges, bridge)
	}
	r.bridges = map[string]*SandboxSessionBridge{}
	r.mu.Unlock()
	var cleanupErr error
	for _, bridge := range bridges {
		if err := bridge.Close(ctx); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

// NewSandboxSessionBridge creates an idle Task-owned bridge.
func NewSandboxSessionBridge(docker SandboxBridgeDocker, config SandboxBridgeConfig) (*SandboxSessionBridge, error) {
	if docker == nil {
		return nil, fmt.Errorf("sandbox bridge Docker is required")
	}
	if strings.TrimSpace(config.TaskID) == "" {
		return nil, fmt.Errorf("sandbox bridge TaskID is required")
	}
	if err := validateSandboxBridgeCreateArgs(config.CreateArgs); err != nil {
		return nil, err
	}
	args := append([]string(nil), config.CreateArgs...)
	config.CreateArgs = args
	return &SandboxSessionBridge{
		docker: docker, config: config, state: "idle",
		pending: map[string]*bridgePending{}, completed: map[string]bridgeCompletion{},
		requests: map[string][sha256.Size]byte{},
		closed: make(chan struct{}), terminated: make(chan struct{}),
	}, nil
}

// Start creates and attaches the one container. A failed attach removes the
// just-created container, preventing orphan ownership during crash windows.
func (b *SandboxSessionBridge) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.state != "idle" {
		err := ErrSandboxBridgeStarted
		if b.state == "closed" {
			err = ErrSandboxBridgeClosed
		}
		b.mu.Unlock()
		return err
	}
	b.state = "starting"
	b.mu.Unlock()

	id, err := b.docker.Create(ctx, b.config.CreateArgs)
	if err != nil {
		b.mu.Lock()
		b.state = "idle"
		b.mu.Unlock()
		return fmt.Errorf("create sandbox bridge container: %w", err)
	}
	id = strings.TrimSpace(id)
	if id == "" {
		b.mu.Lock()
		b.state = "idle"
		b.mu.Unlock()
		return fmt.Errorf("create sandbox bridge container returned empty id")
	}
	ioConn, err := b.docker.Start(ctx, id)
	if err != nil {
		_ = b.docker.Stop(context.Background(), id)
		_ = b.docker.Remove(context.Background(), id)
		b.mu.Lock()
		b.state = "idle"
		b.mu.Unlock()
		return fmt.Errorf("start sandbox bridge container: %w", err)
	}
	if ioConn.Stdin == nil || ioConn.Stdout == nil {
		_ = b.docker.Stop(context.Background(), id)
		_ = b.docker.Remove(context.Background(), id)
		b.mu.Lock()
		b.state = "idle"
		b.mu.Unlock()
		return fmt.Errorf("start sandbox bridge: stdin and stdout are required")
	}
	b.mu.Lock()
	b.containerID, b.stdin, b.stdout, b.wait, b.state = id, ioConn.Stdin, ioConn.Stdout, ioConn.Wait, "running"
	b.mu.Unlock()
	go b.readLoop(ioConn.Stdout)
	if ioConn.Diagnostics != nil && b.config.Diagnostics != nil {
		go b.diagnosticLoop(ioConn.Diagnostics)
	}
	return nil
}

// ContainerID returns the stable container identity after Start.
func (b *SandboxSessionBridge) ContainerID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.containerID
}

// TaskID returns the immutable Task owner.
func (b *SandboxSessionBridge) TaskID() string { return b.config.TaskID }

// BindContinuation associates future requests with a new Continuation without
// replacing the Task-owned session or container.
func (b *SandboxSessionBridge) BindContinuation(continuationID string) error {
	continuationID = strings.TrimSpace(continuationID)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == "closed" {
		return ErrSandboxBridgeClosed
	}
	b.continuation = continuationID
	return nil
}

// Send writes one framed request and waits for its response. Request IDs are
// idempotency keys: retries never write a second frame.
func (b *SandboxSessionBridge) Send(ctx context.Context, request SandboxBridgeRequest) (SandboxBridgeResponse, error) {
	request.JSONRPC = "2.0"
	request.ID = strings.TrimSpace(request.ID)
	request.TaskID = strings.TrimSpace(request.TaskID)
	if request.ID == "" || strings.TrimSpace(request.Method) == "" {
		return SandboxBridgeResponse{}, ErrSandboxBridgeInvalid
	}
	if request.TaskID == "" {
		request.TaskID = b.config.TaskID
	}
	if request.TaskID != b.config.TaskID {
		return SandboxBridgeResponse{}, ErrSandboxBridgeTaskMismatch
	}
	b.mu.Lock()
	if b.state != "running" {
		b.mu.Unlock()
		return SandboxBridgeResponse{}, ErrSandboxBridgeClosed
	}
	if request.ContinuationID == "" {
		request.ContinuationID = b.continuation
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		b.mu.Unlock()
		return SandboxBridgeResponse{}, ErrSandboxBridgeInvalid
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
		b.finish(request.ID, SandboxBridgeResponse{}, fmt.Errorf("write sandbox bridge request: %w", err))
	}
	select {
	case <-pending.done:
		return pending.result.response, pending.result.err
	case <-ctx.Done():
		return SandboxBridgeResponse{}, ctx.Err()
	}
}

// Events received without an ID are delivered to ProtocolEmit. Responses are
// delivered to the corresponding Send call and are not emitted as raw events.
func (b *SandboxSessionBridge) readLoop(reader io.Reader) {
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
			b.finish(event.ID, SandboxBridgeResponse{JSONRPC: event.JSONRPC, ID: event.ID, Result: event.Result, Error: event.Error}, responseErr)
			continue
		}
		if b.config.ProtocolEmit != nil {
			b.config.ProtocolEmit(event)
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	b.mu.Lock()
	// Only unexpected protocol/process exit signals Terminated. Explicit Close
	// sets state to closed first; the resulting stream end must not look like
	// an unexpected exit.
	unexpected := b.state == "running"
	if unexpected {
		b.state = "failed"
	}
	ids := make([]string, 0, len(b.pending))
	for id := range b.pending {
		ids = append(ids, id)
	}
	b.mu.Unlock()
	for _, id := range ids {
		b.finish(id, SandboxBridgeResponse{}, fmt.Errorf("sandbox bridge protocol closed: %w", err))
	}
	if unexpected {
		b.signalTerminated()
	}
}

func (b *SandboxSessionBridge) diagnosticLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		if b.config.Diagnostics != nil {
			b.config.Diagnostics(redactedDockerString(scanner.Text()))
		}
	}
}

func (b *SandboxSessionBridge) finish(id string, response SandboxBridgeResponse, err error) {
	b.mu.Lock()
	pending, ok := b.pending[id]
	if !ok {
		b.mu.Unlock()
		return
	}
	completion := bridgeCompletion{response: response, err: err}
	delete(b.pending, id)
	b.completed[id] = completion
	pending.result = completion
	close(pending.done)
	b.mu.Unlock()
}

// Close stops and removes the container exactly once. It is safe to call after
// a failed Start or concurrently with protocol shutdown.
func (b *SandboxSessionBridge) Close(ctx context.Context) error {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.state = "closed"
		id, stdin, wait := b.containerID, b.stdin, b.wait
		b.mu.Unlock()
		if stdin != nil {
			_ = stdin.Close()
		}
		if id != "" {
			if err := b.docker.Stop(ctx, id); err != nil {
				b.cleanupErr = err
			}
			if err := b.docker.Remove(ctx, id); err != nil && b.cleanupErr == nil {
				b.cleanupErr = err
			}
		}
		if wait != nil {
			_ = wait()
		}
		close(b.closed)
	})
	return b.cleanupErr
}

// Closed is closed when explicit cleanup has completed.
func (b *SandboxSessionBridge) Closed() <-chan struct{} { return b.closed }

// Terminated is closed once when the protocol stream ends unexpectedly. It is
// distinct from Closed: Terminated does not perform container cleanup and does
// not fire solely because Close was invoked.
func (b *SandboxSessionBridge) Terminated() <-chan struct{} { return b.terminated }

func (b *SandboxSessionBridge) signalTerminated() {
	b.terminateOnce.Do(func() {
		close(b.terminated)
	})
}

func validateSandboxBridgeCreateArgs(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("sandbox bridge create args are required")
	}
	interactive, terminal := false, false
	for _, arg := range args {
		switch arg {
		case "-i", "--interactive":
			interactive = true
		case "-t", "--tty", "-it", "-ti":
			terminal = true
		}
	}
	if !interactive || terminal {
		return ErrSandboxBridgeNoInteractive
	}
	return nil
}
