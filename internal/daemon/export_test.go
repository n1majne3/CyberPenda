package daemon

import "pentest/internal/runner"

// SetPreflightContainerRunnerForTest replaces the container CLI used by launch
// preflight's engine probes. Tests whose subject is not engine readiness use it
// to avoid paying a real `docker info`/`podman info` per launch; every other
// preflight check (scope, auth, runner, sandbox root) still runs unchanged.
func SetPreflightContainerRunnerForTest(server *Server, run runner.CommandRunner) {
	server.preflight = server.preflight.WithContainerRunner(run)
}
