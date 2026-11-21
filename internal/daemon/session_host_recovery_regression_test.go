//go:build unix

package daemon

import (
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/session"
	"pentest/internal/store"
)

func TestSessionRestartCleansHostProcessGroup(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   session.RuntimeStatus
		metadata bool
	}{
		{"running", session.RuntimeStatusRunning, true},
		{"terminal", session.RuntimeStatusStopped, true},
		{"missing identity", session.RuntimeStatusRunning, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("sleep", "60")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan struct{})
			go func() { _ = cmd.Wait(); close(exited) }()
			t.Cleanup(func() { _ = cmd.Process.Kill(); <-exited })
			root := t.TempDir()
			config := Config{DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"), SessionRoot: filepath.Join(root, "sessions"), DisableBuiltinSkills: true}
			db, err := store.Open(config.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			sessions := session.NewService(db, config.SessionRoot)
			found, err := sessions.Create(session.CreateRequest{Input: "Recover Host Session"})
			if err != nil {
				t.Fatal(err)
			}
			c, err := sessions.CreateContinuation(found.ID, "profile", "codex", session.RunnerHost)
			if err != nil {
				t.Fatal(err)
			}
			identity := ""
			if tc.metadata {
				identity = runtime.FormatHostProcessGroupID(cmd.Process.Pid)
			}
			if _, err := sessions.UpdateContinuationRuntimeMetadata(c.ID, identity, "native-id", "native-path"); err != nil {
				t.Fatal(err)
			}
			if _, err := sessions.UpdateContinuationStatus(c.ID, tc.status); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			s, err := NewServer(config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			latest, err := s.sessions.LatestContinuation(found.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantStatus := tc.status
			if tc.status == session.RuntimeStatusRunning {
				wantStatus = session.RuntimeStatusInterrupted
			}
			if latest.Status != wantStatus || latest.NativeSessionID != "native-id" || latest.NativeSessionPath != "native-path" {
				t.Fatalf("recovered=%#v", latest)
			}
			if tc.status == session.RuntimeStatusRunning && tc.metadata {
				select {
				case <-exited:
				case <-time.After(time.Second):
					t.Fatal("startup left the prior Host Session process running")
				}
			} else if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("unowned process was stopped: %v", err)
			}
		})
	}
}
