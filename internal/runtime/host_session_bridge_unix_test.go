//go:build unix

package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"pentest/internal/runtime"
)

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

