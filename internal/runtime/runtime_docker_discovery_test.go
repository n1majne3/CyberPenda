package runtime_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/task"
)

func TestDockerSandboxAdapterRecordsContainerAndStopsByID(t *testing.T) {
	skipUnlessPOSIXProcessDoubles(t)
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "sandbox task", Runner: task.RunnerSandbox})
	continuation, err := tasks.CreateContinuation(created.ID, "codex-profile", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker.log")
	stoppedPath := filepath.Join(dir, "stopped")
	docker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(logPath) + "\n" +
		"case \"$1\" in\n" +
		"  image) exit 0 ;;\n" +
		"  create) echo ctr-owned ;;\n" +
		"  start) echo sandbox-started; while [ ! -f " + shellQuote(stoppedPath) + " ]; do sleep 0.05; done ;;\n" +
		"  stop) touch " + shellQuote(stoppedPath) + " ;;\n" +
		"  rm) exit 0 ;;\n" +
		"  *) exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(docker, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID:         created.ID,
			Goal:           created.Goal,
			ContinuationID: continuation.ID,
			Adapter: runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{
				Name:         "codex",
				ContainerCLI: docker,
				Image:        "image",
				CreateArgs:   []string{"create", "-i", "image", "codex", "run"},
			}),
		})
	}()

	waitForActive(t, harness, created.ID)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		active, err := tasks.ActiveContinuation(created.ID)
		if err != nil {
			t.Fatalf("active continuation: %v", err)
		}
		if active != nil && active.ContainerID == "ctr-owned" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	active, err := tasks.ActiveContinuation(created.ID)
	if err != nil {
		t.Fatalf("active continuation: %v", err)
	}
	if active == nil || active.ContainerID != "ctr-owned" {
		t.Fatalf("expected active continuation container id ctr-owned, got %#v", active)
	}

	if !harness.StopAndWait(created.ID, 2*time.Second) {
		t.Fatal("expected StopAndWait to stop docker sandbox")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read docker log: %v", err)
	}
	logText := string(raw)
	if !strings.Contains(logText, "stop ctr-owned") {
		t.Fatalf("expected docker stop by container id, got log:\n%s", logText)
	}
	if !strings.Contains(logText, "rm -f ctr-owned") {
		t.Fatalf("expected docker rm by container id, got log:\n%s", logText)
	}
}

func TestDockerSandboxAdapterCreatesMissingRequiredBridgeNetworkBeforeContainerStart(t *testing.T) {
	skipUnlessPOSIXProcessDoubles(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker.log")
	networkPath := filepath.Join(dir, "network-created")
	docker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(logPath) + "\n" +
		"if [ \"$1 $2\" = \"network inspect\" ]; then\n" +
		"  if [ -f " + shellQuote(networkPath) + " ]; then echo 'bridge|false'; exit 0; fi\n" +
		"  exit 1\n" +
		"fi\n" +
		"if [ \"$1 $2\" = \"network create\" ]; then touch " + shellQuote(networkPath) + "; echo network-id; exit 0; fi\n" +
		"case \"$1\" in\n" +
		"  image) exit 0 ;;\n" +
		"  create) echo ctr-owned ;;\n" +
		"  start) echo sandbox-started ;;\n" +
		"  rm) exit 0 ;;\n" +
		"  *) exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(docker, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}

	adapter := runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{
		Name:         "codex",
		ContainerCLI: docker,
		Image:        "image",
		CreateArgs:   []string{"create", "--network", "pentest-host-proxy-only", "image", "codex", "run"},
		RequiredNetwork: &runtime.DockerNetworkRequirement{
			Name:     "pentest-host-proxy-only",
			Driver:   "bridge",
			Internal: false,
		},
	})
	if err := adapter.Run(context.Background(), "sandbox task", func(task.EventKind, task.EventPayload) {}); err != nil {
		t.Fatalf("run docker sandbox: %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read docker log: %v", err)
	}
	logText := string(raw)
	createNetwork := "network create --driver bridge pentest-host-proxy-only"
	if !strings.Contains(logText, createNetwork) {
		t.Fatalf("expected missing bridge network to be created, got log:\n%s", logText)
	}
	if strings.Index(logText, createNetwork) > strings.Index(logText, "create --network pentest-host-proxy-only") {
		t.Fatalf("expected network creation before container creation, got log:\n%s", logText)
	}
}

func TestDockerSandboxAdapterRejectsRequiredNetworkWithUnsafeConfiguration(t *testing.T) {
	skipUnlessPOSIXProcessDoubles(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker.log")
	docker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(logPath) + "\n" +
		"if [ \"$1 $2\" = \"network inspect\" ]; then echo 'bridge|true'; exit 0; fi\n" +
		"if [ \"$1\" = \"create\" ]; then echo unexpected-container-create; exit 0; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(docker, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}

	adapter := runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{
		Name:         "codex",
		ContainerCLI: docker,
		Image:        "image",
		CreateArgs:   []string{"create", "--network", "pentest-host-proxy-only", "image", "codex", "run"},
		RequiredNetwork: &runtime.DockerNetworkRequirement{
			Name:     "pentest-host-proxy-only",
			Driver:   "bridge",
			Internal: false,
		},
	})
	err := adapter.Run(context.Background(), "sandbox task", func(task.EventKind, task.EventPayload) {})
	if err == nil || !strings.Contains(err.Error(), "unsafe configuration") {
		t.Fatalf("expected unsafe network configuration error, got %v", err)
	}

	raw, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("read docker log: %v", readErr)
	}
	if strings.Contains(string(raw), "create --network") {
		t.Fatalf("container must not be created on unsafe network, got log:\n%s", string(raw))
	}
}

func TestDockerSandboxAdapterRemovesContainerWhenCanceledBeforeStart(t *testing.T) {
	skipUnlessPOSIXProcessDoubles(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker.log")
	docker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(logPath) + "\n" +
		"case \"$1\" in\n" +
		"  image) exit 0 ;;\n" +
		"  create) echo ctr-owned ;;\n" +
		"  start) echo unexpected-start ;;\n" +
		"  stop) exit 0 ;;\n" +
		"  rm) exit 0 ;;\n" +
		"  *) exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(docker, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	adapter := runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{
		Name:         "codex",
		ContainerCLI: docker,
		Image:        "image",
		CreateArgs:   []string{"create", "-i", "image", "codex", "run"},
	})
	recorder, ok := adapter.(interface {
		SetMetadataRecorder(func(runtime.NativeSessionMetadata) error)
	})
	if !ok {
		t.Fatal("expected docker sandbox adapter to record metadata")
	}
	recorder.SetMetadataRecorder(func(metadata runtime.NativeSessionMetadata) error {
		if metadata.ContainerID == "ctr-owned" {
			cancel()
		}
		return nil
	})

	if err := adapter.Run(ctx, "sandbox task", func(task.EventKind, task.EventPayload) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read docker log: %v", err)
	}
	logText := string(raw)
	if !strings.Contains(logText, "rm -f ctr-owned") {
		t.Fatalf("expected docker rm after pre-start cancel, got log:\n%s", logText)
	}
}

func TestDockerContainerStopConfirmationTreatsRemovedContainerAsExited(t *testing.T) {
	skipUnlessPOSIXProcessDoubles(t)
	dir := t.TempDir()
	cidFile := filepath.Join(dir, "container.cid")
	if err := os.WriteFile(cidFile, []byte("ctr-123\n"), 0o600); err != nil {
		t.Fatalf("write cidfile: %v", err)
	}
	logPath := filepath.Join(dir, "inspect.log")
	docker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(logPath) + "\n" +
		"exit 1\n"
	if err := os.WriteFile(docker, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}

	if err := runtime.ConfirmDockerContainerExited(docker, cidFile, time.Second); err != nil {
		t.Fatalf("confirm docker container exited: %v", err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read inspect log: %v", err)
	}
	if !strings.Contains(string(raw), "inspect -f {{.State.Running}} ctr-123") {
		t.Fatalf("expected docker inspect for cidfile container, got %s", string(raw))
	}
}

func TestDockerContainerStopConfirmationTimesOutWhileContainerRuns(t *testing.T) {
	dir := t.TempDir()
	cidFile := filepath.Join(dir, "container.cid")
	if err := os.WriteFile(cidFile, []byte("ctr-running\n"), 0o600); err != nil {
		t.Fatalf("write cidfile: %v", err)
	}
	docker, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve portable container CLI double: %v", err)
	}
	invocationMarker := filepath.Join(dir, "container-cli-called")
	t.Setenv(alwaysRunningContainerCLIEnv, "1")
	t.Setenv(containerCLIInvocationPathEnv, invocationMarker)

	err = runtime.ConfirmDockerContainerExited(docker, cidFile, time.Second)
	if got, want := fmt.Sprint(err), "container ctr-running did not stop before timeout"; got != want {
		t.Fatalf("stop confirmation error = %q, want %q", got, want)
	}
	if _, err := os.Stat(invocationMarker); err != nil {
		t.Fatalf("portable container CLI was not invoked: %v", err)
	}
}

func TestDockerContainerStopConfirmationReportsStableTimeoutWhenInspectExceedsDeadline(t *testing.T) {
	dir := t.TempDir()
	cidFile := filepath.Join(dir, "container.cid")
	if err := os.WriteFile(cidFile, []byte("ctr-slow\n"), 0o600); err != nil {
		t.Fatalf("write cidfile: %v", err)
	}
	docker, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve portable container CLI double: %v", err)
	}
	t.Setenv(slowContainerCLIEnv, "1")

	err = runtime.ConfirmDockerContainerExited(docker, cidFile, 30*time.Millisecond)
	if got, want := fmt.Sprint(err), "container ctr-slow did not stop before timeout"; got != want {
		t.Fatalf("stop confirmation error = %q, want %q", got, want)
	}
}

func TestDockerContainerCleanupTreatsMissingContainerAsSuccess(t *testing.T) {
	skipUnlessPOSIXProcessDoubles(t)
	dir := t.TempDir()
	docker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"echo 'Error response from daemon: No such container: ctr-missing' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(docker, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}

	if err := runtime.StopDockerContainer(docker, "ctr-missing", 2*time.Second); err != nil {
		t.Fatalf("expected missing container stop to succeed, got %v", err)
	}
	if err := runtime.RemoveDockerContainer(docker, "ctr-missing"); err != nil {
		t.Fatalf("expected missing container rm to succeed, got %v", err)
	}
}

func TestDiscoverCodexSessionReturnsNewestSavedSession(t *testing.T) {
	providerHome := filepath.Join(t.TempDir(), "codex")
	oldPath := filepath.Join(providerHome, "sessions", "2026", "07", "03", "older.jsonl")
	newPath := filepath.Join(providerHome, "sessions", "2026", "07", "04", "newer.jsonl")
	for path, sessionID := range map[string]string{
		oldPath: "sess-old",
		newPath: "sess-new",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir session dir: %v", err)
		}
		line := `{"type":"session_meta","payload":{"session_id":"` + sessionID + `"}}` + "\n"
		if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
			t.Fatalf("write session file: %v", err)
		}
	}
	if err := os.Chtimes(newPath, time.Now(), time.Now().Add(time.Second)); err != nil {
		t.Fatalf("chtimes newer session: %v", err)
	}

	metadata, err := runtime.DiscoverCodexSession(providerHome)
	if err != nil {
		t.Fatalf("discover codex session: %v", err)
	}
	if metadata.NativeSessionID != "sess-new" {
		t.Fatalf("expected newest session id sess-new, got %q", metadata.NativeSessionID)
	}
	if metadata.NativeSessionPath != newPath {
		t.Fatalf("expected newest session path %q, got %q", newPath, metadata.NativeSessionPath)
	}
}

func TestDiscoverCodexSessionFallsBackToNewestSQLiteThread(t *testing.T) {
	providerHome := filepath.Join(t.TempDir(), "codex")
	if err := os.MkdirAll(providerHome, 0o700); err != nil {
		t.Fatalf("mkdir provider home: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(providerHome, "state_5.sqlite"))
	if err != nil {
		t.Fatalf("open codex state sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL, updated_at INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create threads table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO threads (id, rollout_path, updated_at) VALUES (?, ?, ?), (?, ?, ?)`,
		"thread-old", "rollouts/old.jsonl", int64(100),
		"thread-new", "rollouts/new.jsonl", int64(200),
	); err != nil {
		t.Fatalf("insert threads: %v", err)
	}

	metadata, err := runtime.DiscoverCodexSession(providerHome)
	if err != nil {
		t.Fatalf("discover codex session: %v", err)
	}
	if metadata.NativeSessionID != "thread-new" {
		t.Fatalf("expected newest thread id thread-new, got %q", metadata.NativeSessionID)
	}
	if metadata.NativeSessionPath != filepath.Join(providerHome, "state_5.sqlite") {
		t.Fatalf("expected sqlite path, got %q", metadata.NativeSessionPath)
	}
}

func TestDiscoverPiSessionReturnsNewestPersistedSession(t *testing.T) {
	providerHome := filepath.Join(t.TempDir(), "pi")
	oldPath := filepath.Join(providerHome, "agent", "sessions", "--task-workdir--", "2026-07-03T00-00-00-000Z_old.jsonl")
	newPath := filepath.Join(providerHome, "agent", "sessions", "--task-workdir--", "2026-07-04T00-00-00-000Z_new.jsonl")
	for path, sessionID := range map[string]string{
		oldPath: "sess-old",
		newPath: "sess-new",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir session dir: %v", err)
		}
		line := `{"type":"session","version":3,"id":"` + sessionID + `","cwd":"/task/workdir"}` + "\n"
		if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
			t.Fatalf("write session file: %v", err)
		}
	}
	if err := os.Chtimes(newPath, time.Now(), time.Now().Add(time.Second)); err != nil {
		t.Fatalf("chtimes newer session: %v", err)
	}

	metadata, err := runtime.DiscoverPiSession(providerHome)
	if err != nil {
		t.Fatalf("discover pi session: %v", err)
	}
	if metadata.NativeSessionID != "sess-new" {
		t.Fatalf("expected newest session id sess-new, got %q", metadata.NativeSessionID)
	}
	if metadata.NativeSessionPath != newPath {
		t.Fatalf("expected newest session path %q, got %q", newPath, metadata.NativeSessionPath)
	}
}
