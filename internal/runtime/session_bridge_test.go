package runtime_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"pentest/internal/runtime"
)

type fakeBridgeDocker struct {
	mu sync.Mutex

	creates int
	starts  int
	stops   int
	removes int
	args    []string

	startErr error
	requestR *io.PipeReader
	requestW *io.PipeWriter
	outputR  *io.PipeReader
	outputW  *io.PipeWriter
	diagR    *io.PipeReader
	diagW    *io.PipeWriter
}

func newFakeBridgeDocker() *fakeBridgeDocker {
	requestR, requestW := io.Pipe()
	outputR, outputW := io.Pipe()
	diagR, diagW := io.Pipe()
	return &fakeBridgeDocker{requestR: requestR, requestW: requestW, outputR: outputR, outputW: outputW, diagR: diagR, diagW: diagW}
}

func (d *fakeBridgeDocker) Create(_ context.Context, args []string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.creates++
	d.args = append([]string(nil), args...)
	return "container-1", nil
}

func (d *fakeBridgeDocker) Start(context.Context, string) (runtime.SandboxBridgeIO, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.starts++
	if d.startErr != nil {
		return runtime.SandboxBridgeIO{}, d.startErr
	}
	return runtime.SandboxBridgeIO{Stdin: d.requestW, Stdout: d.outputR, Diagnostics: d.diagR}, nil
}

func (d *fakeBridgeDocker) Stop(context.Context, string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stops++
	return nil
}

func (d *fakeBridgeDocker) Remove(context.Context, string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.removes++
	return nil
}

func (d *fakeBridgeDocker) counts() (int, int, int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.creates, d.starts, d.stops, d.removes
}

func newStartedBridge(t *testing.T, docker *fakeBridgeDocker, options ...func(*runtime.SandboxBridgeConfig)) *runtime.SandboxSessionBridge {
	t.Helper()
	config := runtime.SandboxBridgeConfig{TaskID: "task-1", CreateArgs: []string{"create", "-i", "image", "bridge"}}
	for _, option := range options {
		option(&config)
	}
	bridge, err := runtime.NewSandboxSessionBridge(docker, config)
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	if err := bridge.Start(context.Background()); err != nil {
		t.Fatalf("start bridge: %v", err)
	}
	t.Cleanup(func() { _ = bridge.Close(context.Background()) })
	return bridge
}

func TestSandboxSessionBridgeRequiresStdinAndRejectsTTY(t *testing.T) {
	for _, args := range [][]string{
		{"create", "image"},
		{"create", "-i", "-t", "image"},
		{"create", "-it", "image"},
	} {
		_, err := runtime.NewSandboxSessionBridge(newFakeBridgeDocker(), runtime.SandboxBridgeConfig{TaskID: "task-1", CreateArgs: args})
		if !errors.Is(err, runtime.ErrSandboxBridgeNoInteractive) {
			t.Fatalf("args %#v: error = %v", args, err)
		}
	}
}

func TestDockerSandboxBridgeStartArgsAttachStdinWithoutTTY(t *testing.T) {
	args := runtime.DockerSandboxBridgeStartArgs("container-1")
	want := []string{"start", "-a", "-i", "container-1"}
	if len(args) != len(want) {
		t.Fatalf("args = %#v", args)
	}
	for index := range want {
		if args[index] != want[index] {
			t.Fatalf("args = %#v, want %#v", args, want)
		}
	}
}

func TestSandboxSessionBridgeKeepsOneTaskContainerAcrossContinuations(t *testing.T) {
	docker := newFakeBridgeDocker()
	bridge := newStartedBridge(t, docker)
	if bridge.TaskID() != "task-1" || bridge.ContainerID() != "container-1" {
		t.Fatalf("bridge identity = task %q, container %q", bridge.TaskID(), bridge.ContainerID())
	}

	requests := make(chan runtime.SandboxBridgeRequest, 2)
	go func() {
		scanner := bufio.NewScanner(docker.requestR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			requests <- request
			_, _ = docker.outputW.Write([]byte(`{"jsonrpc":"2.0","id":"` + request.ID + `","result":{"accepted":true}}` + "\n"))
		}
	}()

	for index, continuationID := range []string{"continuation-1", "continuation-2"} {
		if err := bridge.BindContinuation(continuationID); err != nil {
			t.Fatalf("bind continuation: %v", err)
		}
		id := "request-" + string(rune('1'+index))
		response, err := bridge.Send(context.Background(), runtime.SandboxBridgeRequest{ID: id, ContinuationID: continuationID, Method: "turn/send"})
		if err != nil {
			t.Fatalf("send %s: %v", continuationID, err)
		}
		if response.ID != id {
			t.Fatalf("response = %#v", response)
		}
		request := <-requests
		if request.TaskID != "task-1" || request.ContinuationID != continuationID || request.JSONRPC != "2.0" {
			t.Fatalf("request = %#v", request)
		}
	}
	creates, starts, _, _ := docker.counts()
	if creates != 1 || starts != 1 {
		t.Fatalf("container lifecycle = %d creates, %d starts", creates, starts)
	}
}

func TestSandboxSessionBridgeUsesBoundContinuationAndRejectsRequestIDReuse(t *testing.T) {
	docker := newFakeBridgeDocker()
	bridge := newStartedBridge(t, docker)
	if err := bridge.BindContinuation("continuation-1"); err != nil {
		t.Fatalf("bind continuation: %v", err)
	}
	seen := make(chan runtime.SandboxBridgeRequest, 1)
	go func() {
		scanner := bufio.NewScanner(docker.requestR)
		if scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			seen <- request
			_, _ = docker.outputW.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":\"request-1\",\"result\":{}}\n"))
		}
	}()
	if _, err := bridge.Send(context.Background(), runtime.SandboxBridgeRequest{ID: "request-1", Method: "turn/send"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if request := <-seen; request.ContinuationID != "continuation-1" {
		t.Fatalf("bound continuation = %q", request.ContinuationID)
	}
	_, err := bridge.Send(context.Background(), runtime.SandboxBridgeRequest{ID: "request-1", Method: "turn/interrupt"})
	if !errors.Is(err, runtime.ErrSandboxBridgeRequestConflict) {
		t.Fatalf("request id reuse error = %v", err)
	}
}

func TestSandboxSessionBridgeDuplicateRequestIsWrittenOnce(t *testing.T) {
	docker := newFakeBridgeDocker()
	bridge := newStartedBridge(t, docker)
	writes := make(chan struct{}, 2)
	go func() {
		scanner := bufio.NewScanner(docker.requestR)
		for scanner.Scan() {
			writes <- struct{}{}
			_, _ = docker.outputW.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":\"request-1\",\"result\":{}}\n"))
		}
	}()
	request := runtime.SandboxBridgeRequest{ID: "request-1", TaskID: "task-1", Method: "turn/interrupt"}
	first, err := bridge.Send(context.Background(), request)
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	second, err := bridge.Send(context.Background(), request)
	if err != nil {
		t.Fatalf("duplicate send: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("responses differ: %#v %#v", first, second)
	}
	<-writes
	select {
	case <-writes:
		t.Fatal("duplicate request wrote a second frame")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestSandboxSessionBridgeRetryAfterCallerTimeoutDoesNotResend(t *testing.T) {
	docker := newFakeBridgeDocker()
	bridge := newStartedBridge(t, docker)
	requestRead := make(chan runtime.SandboxBridgeRequest, 1)
	go func() {
		scanner := bufio.NewScanner(docker.requestR)
		if scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			requestRead <- request
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	request := runtime.SandboxBridgeRequest{ID: "request-1", Method: "turn/interrupt"}
	if _, err := bridge.Send(ctx, request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first send error = %v", err)
	}
	got := <-requestRead
	retried := make(chan error, 1)
	go func() { _, err := bridge.Send(context.Background(), request); retried <- err }()
	_, _ = docker.outputW.Write([]byte(`{"jsonrpc":"2.0","id":"` + got.ID + `","result":{}}` + "\n"))
	if err := <-retried; err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestSandboxSessionBridgeRejectsForeignTaskBeforeWriting(t *testing.T) {
	docker := newFakeBridgeDocker()
	bridge := newStartedBridge(t, docker)
	_, err := bridge.Send(context.Background(), runtime.SandboxBridgeRequest{ID: "request-1", TaskID: "task-2", Method: "turn/send"})
	if !errors.Is(err, runtime.ErrSandboxBridgeTaskMismatch) {
		t.Fatalf("error = %v", err)
	}
}

func TestSandboxSessionBridgeSeparatesProtocolNotificationsAndDiagnostics(t *testing.T) {
	docker := newFakeBridgeDocker()
	events := make(chan runtime.SandboxBridgeEvent, 1)
	diagnostics := make(chan string, 1)
	newStartedBridge(t, docker, func(config *runtime.SandboxBridgeConfig) {
		config.ProtocolEmit = func(event runtime.SandboxBridgeEvent) { events <- event }
		config.Diagnostics = func(line string) { diagnostics <- line }
	})
	_, _ = docker.outputW.Write([]byte("{\"jsonrpc\":\"2.0\",\"method\":\"turn/event\",\"params\":{\"kind\":\"started\"}}\n"))
	_, _ = docker.diagW.Write([]byte("provider diagnostic\n"))
	if event := <-events; event.Method != "turn/event" {
		t.Fatalf("event = %#v", event)
	}
	if diagnostic := <-diagnostics; diagnostic != "provider diagnostic" {
		t.Fatalf("diagnostic = %q", diagnostic)
	}
}

func TestSandboxSessionBridgeCleansFailedStartAndCloseIsIdempotent(t *testing.T) {
	docker := newFakeBridgeDocker()
	docker.startErr = errors.New("attach failed")
	bridge, err := runtime.NewSandboxSessionBridge(docker, runtime.SandboxBridgeConfig{TaskID: "task-1", CreateArgs: []string{"create", "-i", "image"}})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	if err := bridge.Start(context.Background()); err == nil {
		t.Fatal("start unexpectedly succeeded")
	}
	creates, starts, stops, removes := docker.counts()
	if creates != 1 || starts != 1 || stops != 1 || removes != 1 {
		t.Fatalf("failed start lifecycle = create %d start %d stop %d remove %d", creates, starts, stops, removes)
	}
	if err := bridge.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := bridge.Close(context.Background()); err != nil {
		t.Fatalf("repeat close: %v", err)
	}
	_, _, stops, removes = docker.counts()
	if stops != 1 || removes != 1 {
		t.Fatalf("repeat cleanup = stop %d remove %d", stops, removes)
	}
}

func TestSandboxSessionBridgeRegistryBindsByTaskNotContinuation(t *testing.T) {
	docker := newFakeBridgeDocker()
	bridge := newStartedBridge(t, docker)
	registry := runtime.NewSandboxSessionBridgeRegistry()
	created := 0
	factory := func() (*runtime.SandboxSessionBridge, error) {
		created++
		return bridge, nil
	}
	first, err := registry.Bind(context.Background(), "task-1", "continuation-1", factory)
	if err != nil {
		t.Fatalf("first bind: %v", err)
	}
	second, err := registry.Bind(context.Background(), "task-1", "continuation-2", factory)
	if err != nil {
		t.Fatalf("second bind: %v", err)
	}
	if first != second || created != 1 {
		t.Fatalf("registry identity = first %p second %p created %d", first, second, created)
	}
	if got, ok := registry.Get("task-1"); !ok || got != bridge {
		t.Fatalf("registry lookup = %p, %t", got, ok)
	}
	if err := registry.CloseTask(context.Background(), "task-1"); err != nil {
		t.Fatalf("close task: %v", err)
	}
	if _, ok := registry.Get("task-1"); ok {
		t.Fatal("closed task still owns bridge")
	}
}
