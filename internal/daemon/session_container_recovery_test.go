package daemon_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"pentest/internal/daemon"
	"pentest/internal/session"
	"pentest/internal/store"
)

func sessionContainerRecoveryFixture(t *testing.T, script string) (daemon.Config, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("container CLI test double requires a POSIX shell")
	}
	root := t.TempDir()
	cli := filepath.Join(root, "container-cli")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	config := daemon.Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SessionRoot: filepath.Join(root, "sessions"), ContainerCLI: cli, DisableBuiltinSkills: true,
	}
	db, err := store.Open(config.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sessions := session.NewService(db, config.SessionRoot)
	created, err := sessions.Create(session.CreateRequest{Input: "Recover this Session"})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := sessions.CreateContinuation(created.ID, "profile", "codex", session.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.UpdateContinuationRuntimeMetadata(continuation.ID, "session-container", "native-session", "/native/session.jsonl"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.UpdateContinuationStatus(continuation.ID, session.RuntimeStatusRunning); err != nil {
		t.Fatal(err)
	}
	return config, created.ID
}

func TestSessionRestartRemovesItsContainerBeforeAllowingRecovery(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "container-calls")
	config, sessionID := sessionContainerRecoveryFixture(t, "echo \"$*\" >> "+shellQuote(logPath)+"\n")
	server, err := daemon.NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Session restart did not clean its container: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "stop session-container\nrm -f session-container" {
		t.Fatalf("container cleanup = %q", got)
	}
	db, err := store.Open(config.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sessions := session.NewService(db, config.SessionRoot)
	latest, err := sessions.LatestContinuation(sessionID)
	if err != nil || latest == nil || latest.Status != session.RuntimeStatusInterrupted {
		t.Fatalf("recovered Continuation = %#v, %v", latest, err)
	}
	if latest.NativeSessionID != "native-session" || latest.NativeSessionPath != "/native/session.jsonl" {
		t.Fatalf("recovery lost native Session identity: %#v", latest)
	}
}

func TestSessionRestartKeepsContainerCleanupRetryableAfterFailure(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
	}{
		{name: "stop and kill fail", script: "exit 1\n"},
		{name: "remove fails", script: "if [ \"$1\" = rm ]; then exit 1; fi\nexit 0\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, sessionID := sessionContainerRecoveryFixture(t, test.script)
			server, err := daemon.NewServer(config)
			if server != nil {
				_ = server.Close()
				t.Fatal("daemon started before the Session container was removed")
			}
			if err == nil || !strings.Contains(err.Error(), sessionID) || !strings.Contains(err.Error(), "session-container") {
				t.Fatalf("cleanup error does not identify the Session and container: %v", err)
			}
			db, err := store.Open(config.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			sessions := session.NewService(db, config.SessionRoot)
			active, readErr := sessions.ActiveContinuation(sessionID)
			_ = db.Close()
			if readErr != nil || active == nil || active.ContainerID != "session-container" {
				t.Fatalf("cleanup failure lost the active container identity: %#v, %v", active, readErr)
			}

			// A later startup retries the same durable container identity.
			calls := filepath.Join(t.TempDir(), "retry-calls")
			config.ContainerCLI = filepath.Join(t.TempDir(), "recovered-container-cli")
			if err := os.WriteFile(config.ContainerCLI, []byte("#!/bin/sh\necho \"$*\" >> "+shellQuote(calls)+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			server, err = daemon.NewServer(config)
			if err != nil {
				t.Fatalf("retry startup: %v", err)
			}
			_ = server.Close()
			raw, err := os.ReadFile(calls)
			if err != nil || !strings.Contains(string(raw), "rm -f session-container") {
				t.Fatalf("startup did not retry container removal: %q, %v", raw, err)
			}
		})
	}
}

func TestSessionRestartAcceptsAnAlreadyRemovedContainer(t *testing.T) {
	config, _ := sessionContainerRecoveryFixture(t, "echo 'Error: No such container: session-container' >&2\nexit 1\n")
	server, err := daemon.NewServer(config)
	if err != nil {
		t.Fatalf("missing container blocked Session recovery: %v", err)
	}
	_ = server.Close()
}

func TestSessionRestartDoesNotRemoveATerminalContinuationsContainer(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "container-calls")
	config, sessionID := sessionContainerRecoveryFixture(t, "echo \"$*\" >> "+shellQuote(calls)+"\n")
	db, err := store.Open(config.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewService(db, config.SessionRoot)
	active, err := sessions.ActiveContinuation(sessionID)
	if err != nil || active == nil {
		_ = db.Close()
		t.Fatalf("active Continuation = %#v, %v", active, err)
	}
	_, err = sessions.UpdateContinuationStatus(active.ID, session.RuntimeStatusStopped)
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	server, err := daemon.NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	_ = server.Close()
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatalf("startup touched a terminal Continuation's container: %v", err)
	}
}
