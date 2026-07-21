package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"pentest/internal/runtime"
)

func TestHostSessionBridgeForwardsJSONRPCWithoutTTY(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "echo-rpc.sh")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  printf '{"jsonrpc":"2.0","id":"%s","result":{"ok":true}}\n' "$id"
done
`), 0o700); err != nil {
		t.Fatal(err)
	}
	bridge, err := runtime.NewHostSessionBridge(runtime.HostSessionBridgeConfig{
		TaskID:  "task-host-1",
		Program: script,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer bridge.Close(context.Background())

	response, err := bridge.Send(context.Background(), runtime.SandboxBridgeRequest{
		ID: "req-1", Method: "thread/start", Params: json.RawMessage(`{"cwd":"/tmp"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != "req-1" || string(response.Result) != `{"ok":true}` {
		t.Fatalf("response = %#v", response)
	}
}

func TestHostSessionBridgeKillsProcessGroupOnClose(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "alive")
	childMarker := filepath.Join(root, "child-alive")
	script := filepath.Join(root, "hold-group.sh")
	// Parent holds; child holds. Closing the bridge must reap both via process group.
	if err := os.WriteFile(script, []byte(`#!/bin/sh
touch "`+marker+`"
( touch "`+childMarker+`"; while true; do sleep 0.1; done ) &
while true; do sleep 0.1; done
`), 0o700); err != nil {
		t.Fatal(err)
	}

	bridge, err := runtime.NewHostSessionBridge(runtime.HostSessionBridgeConfig{
		TaskID:  "task-host-kill",
		Program: script,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	pgid := bridge.ProcessGroupID()
	if pgid <= 0 {
		t.Fatalf("process group id = %d", pgid)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			if _, err := os.Stat(childMarker); err == nil {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("parent process did not start")
	}
	if _, err := os.Stat(childMarker); err != nil {
		t.Fatal("child process did not start")
	}

	if err := bridge.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// After close, no process in the group should remain.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pgid, 0); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process group %d still has live members after Close", pgid)
}

func TestHostSessionBridgeRegistryBindsOneProcessPerTask(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "echo-rpc.sh")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  printf '{"jsonrpc":"2.0","id":"%s","result":{"ok":true}}\n' "$id"
done
`), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := runtime.NewHostSessionBridgeRegistry()
	first, err := registry.Bind(context.Background(), "task-1", "c1", func() (*runtime.HostSessionBridge, error) {
		bridge, err := runtime.NewHostSessionBridge(runtime.HostSessionBridgeConfig{TaskID: "task-1", Program: script})
		if err != nil {
			return nil, err
		}
		if err := bridge.Start(context.Background()); err != nil {
			_ = bridge.Close(context.Background())
			return nil, err
		}
		return bridge, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Bind(context.Background(), "task-1", "c2", func() (*runtime.HostSessionBridge, error) {
		t.Fatal("create must not run when Task already owns a host bridge")
		return nil, errors.New("unreachable")
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("registry returned a different bridge for the same Task")
	}
	if err := registry.CloseTask(context.Background(), "task-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("task-1"); ok {
		t.Fatal("closed Task still registered")
	}
}

func TestCommandAdapterLaunchExportsHostConfig(t *testing.T) {
	adapter := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
		Name: "codex", Program: "/usr/bin/codex", Args: []string{"exec", "--json", "goal"},
		Workdir: "/tmp/workdir", Env: map[string]string{"CODEX_HOME": "/tmp/home"},
	})
	config, ok := runtime.CommandAdapterLaunch(adapter)
	if !ok {
		t.Fatal("expected host command adapter export")
	}
	if config.Program != "/usr/bin/codex" || config.Workdir != "/tmp/workdir" || config.Env["CODEX_HOME"] != "/tmp/home" {
		t.Fatalf("config = %#v", config)
	}
	if len(config.Args) != 3 || config.Args[0] != "exec" {
		t.Fatalf("args = %#v", config.Args)
	}
}

func TestHostSessionBridgeRejectsEmptyProgram(t *testing.T) {
	_, err := runtime.NewHostSessionBridge(runtime.HostSessionBridgeConfig{TaskID: "t"})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestHostProcessGroupMetadataRoundTrip(t *testing.T) {
	token := runtime.FormatHostProcessGroupID(4242)
	if token != "host-pgid:4242" {
		t.Fatalf("token = %q", token)
	}
	pgid, ok := runtime.ParseHostProcessGroupID(token)
	if !ok || pgid != 4242 {
		t.Fatalf("parse = %d ok=%v", pgid, ok)
	}
	if _, ok := runtime.ParseHostProcessGroupID("container-abc"); ok {
		t.Fatal("sandbox container id must not parse as host pgid")
	}
	if runtime.FormatHostProcessGroupID(0) != "" {
		t.Fatal("zero pgid must not emit durable token")
	}
}

type terminalHostStarter struct {
	stdout *io.PipeWriter
	stdin  *io.PipeReader
}

func (s *terminalHostStarter) Start(context.Context, runtime.HostProcessSpec) (runtime.HostProcessHandle, error) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	s.stdin, s.stdout = stdinR, stdoutW
	return runtime.HostProcessHandle{
		IO:             runtime.SandboxBridgeIO{Stdin: stdinW, Stdout: stdoutR},
		ProcessGroupID: 4242,
		KillProcessGroup: func(context.Context) error {
			_ = stdinR.Close()
			_ = stdoutW.Close()
			return nil
		},
	}, nil
}

func TestHostSessionBridgeProtocolExitSignalsTerminatedWithoutClosing(t *testing.T) {
	starter := &terminalHostStarter{}
	bridge, err := runtime.NewHostSessionBridge(runtime.HostSessionBridgeConfig{
		TaskID:  "task-host-term",
		Program: "host-provider",
		Starter: starter,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Unexpected protocol stream end (process death) without explicit Close.
	_ = starter.stdout.Close()

	select {
	case <-bridge.Terminated():
	case <-time.After(2 * time.Second):
		t.Fatal("Terminated did not fire after host process exit")
	}

	select {
	case <-bridge.Closed():
		t.Fatal("Closed fired on unexpected process exit; only Terminated should fire")
	case <-time.After(50 * time.Millisecond):
	}

	if err := bridge.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-bridge.Closed():
	case <-time.After(2 * time.Second):
		t.Fatal("Closed did not fire after explicit Close")
	}
}

func TestHostSessionBridgeExplicitCloseDoesNotSignalTerminated(t *testing.T) {
	starter := &terminalHostStarter{}
	bridge, err := runtime.NewHostSessionBridge(runtime.HostSessionBridgeConfig{
		TaskID:  "task-host-close",
		Program: "host-provider",
		Starter: starter,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-bridge.Closed():
	case <-time.After(2 * time.Second):
		t.Fatal("Closed did not fire after explicit Close")
	}
	select {
	case <-bridge.Terminated():
		t.Fatal("Terminated fired on explicit Close; Stop/Close must not be unexpected exit")
	case <-time.After(100 * time.Millisecond):
	}
}
