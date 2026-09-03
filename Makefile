.PHONY: dev ensure-web-deps build build-ui ensure-embed-stub install-git-hooks build-sandbox-image build-sandbox-smoke-image build-tsecbench-hosted-image smoke-tsecbench-hosted-image tsecbench-hosted-runtime-inventory build-tsecbench-hosted-bundle test test-ci test-ci-windows test-backend smoke-sandbox-mcp smoke-runtime-tasks clean

# Run the daemon and the Vite dev server together for local development.
# The Vite proxy forwards /api and /health to the daemon on :8787.
SANDBOX_IMAGE ?= ghcr.io/n1majne3/cyberpenda-sandbox:latest
SANDBOX_SMOKE_IMAGE ?= cyberpenda-sandbox-smoke:ci
TSECBENCH_HOSTED_IMAGE ?= cyberpenda-tsecbench-hosted:local

# macOS /bin/sh (bash 3.2) has no `wait -n`, so poll: if either child dies,
# surface the failure instead of silently running the other alone (which hid
# backend bind errors behind the foreground Vite output).
dev: ensure-web-deps
	@set -e; \
	trap 'kill 0' EXIT INT TERM; \
	go run ./cmd/pentestd -addr 127.0.0.1:8787 -db pentest.db -sandbox-image $(SANDBOX_IMAGE) & \
	backend_pid=$$!; \
	echo "dev: backend pid=$$backend_pid — waiting for http://127.0.0.1:8787/health …"; \
	ready=0; \
	for _ in $$(seq 1 120); do \
		if curl -sf http://127.0.0.1:8787/health >/dev/null 2>&1; then ready=1; break; fi; \
		if ! kill -0 $$backend_pid 2>/dev/null; then \
			echo "dev: backend exited before becoming ready"; \
			exit 1; \
		fi; \
		sleep 0.25; \
	done; \
	if [ "$$ready" -ne 1 ]; then \
		echo "dev: backend did not become ready within 30s"; \
		exit 1; \
	fi; \
	echo "dev: backend ready"; \
	( cd web && npm run dev ) & \
	frontend_pid=$$!; \
	echo "dev: frontend pid=$$frontend_pid"; \
	while kill -0 $$backend_pid 2>/dev/null && kill -0 $$frontend_pid 2>/dev/null; do \
		sleep 0.5; \
	done; \
	if ! kill -0 $$backend_pid 2>/dev/null; then \
		echo "dev: backend exited — see errors above"; \
	else \
		echo "dev: frontend exited"; \
	fi; \
	exit 1

# Build the self-contained pentest sandbox image (no external base-image dependency).
build-sandbox-image:
	docker build -t $(SANDBOX_IMAGE) -f docker/pentest-sandbox/Dockerfile .

# Build the small container-side HTTP probe used by pull-request live smoke.
# The full Runtime stage is validated by Buildx without loading its large image.
build-sandbox-smoke-image:
	docker build --target smoke -t $(SANDBOX_SMOKE_IMAGE) -f docker/pentest-sandbox/Dockerfile .

# Build the separate one-use TSecBench Hosted Image for its required platform.
build-tsecbench-hosted-image:
	docker buildx build --platform linux/amd64 --load -t $(TSECBENCH_HOSTED_IMAGE) -f docker/tsecbench-hosted/Dockerfile .

# Exercise the built image without added capabilities, networking, or mounts.
smoke-tsecbench-hosted-image:
	CYBERPENDA_TSECBENCH_HOSTED_IMAGE=$(TSECBENCH_HOSTED_IMAGE) go test ./scripts -run TestTSecBenchHostedImageSmokeWhenAnImageIsConfigured -count=1

# Print the exact current Runtime versions stored in the built image.
tsecbench-hosted-runtime-inventory:
	docker run --rm --network none --entrypoint cat $(TSECBENCH_HOSTED_IMAGE) /opt/cyberpenda/runtime-versions.json

# Export and verify the upload-ready TSecBench Hosted Delivery Bundle.
# Usage: make build-tsecbench-hosted-bundle TSECBENCH_BUNDLE_VERSION=v1
build-tsecbench-hosted-bundle:
	@test -n "$(TSECBENCH_BUNDLE_VERSION)" || (echo "TSECBENCH_BUNDLE_VERSION is required" >&2; exit 2)
	scripts/build-tsecbench-hosted-bundle.sh "$(TSECBENCH_BUNDLE_VERSION)" "$(TSECBENCH_HOSTED_IMAGE)"

# Prove the configured sandbox image can reach the daemon Blackboard v2 HTTP
# boundary and write a semantic fact.
smoke-sandbox-mcp:
	@PENTEST_SANDBOX_IMAGE=$(SANDBOX_IMAGE) bash scripts/smoke-sandbox-mcp-live.sh

smoke-runtime-tasks:
	@PENTEST_SANDBOX_IMAGE=$(SANDBOX_IMAGE) python3 scripts/smoke-runtime-tasks-live.py

juice-shop-live:
	@PENTEST_SANDBOX_IMAGE=$(SANDBOX_IMAGE) python3 scripts/run-juice-shop-live.py

# Repair first-checkout, stale-lockfile, and npm optional-native-dependency
# installs before starting Vite or building the embedded UI.
ensure-web-deps:
	@node scripts/web-build-cli.mjs ensure-deps

# Build the React UI and copy it into the embed location (local, not committed).
build-ui:
	@node scripts/web-build-cli.mjs build-ui

# Ensure dist/ exists for //go:embed when no UI has been built yet.
ensure-embed-stub:
	@node scripts/web-build-cli.mjs ensure-embed-stub

# Enable repository-owned hooks for this checkout.
install-git-hooks:
	git config core.hooksPath .githooks

# Build the daemon binary with the UI embedded.
build: build-ui
	go build ./cmd/pentestd

# Run all Go tests.
test: test-backend

# CI default: unit/integration tests only (no Docker, no LLM credentials).
test-ci: test-backend

test-backend: ensure-embed-stub
	go test -timeout 20m ./cmd/... ./internal/... ./scripts

# Windows CI: daemon code only. The scripts package pins POSIX-only
# deliverables (Makefile text, bash, chmod 600, LF endings); its tests are
# Linux-only by design. internal/daemon and internal/runner are excluded
# until their POSIX shell-script test doubles and sandbox container-path
# semantics are ported (tracked in issue #231). POSIX-only test doubles in
# the remaining packages skip in place on Windows.
test-ci-windows: ensure-embed-stub
	go test -timeout 20m $$(go list ./cmd/... ./internal/... | grep -vxF 'pentest/internal/daemon' | grep -vxF 'pentest/internal/runner')

# Live smokes (local):
#   make smoke-sandbox-mcp     — sandbox image + daemon Blackboard HTTP, no LLM
#   make smoke-runtime-tasks   — Codex/Claude/Pi task smoke; needs Docker + provider creds
# Optional filters: PENTEST_SMOKE_ONLY=codex|claude_code|pi|pi_sandbox

clean:
	rm -rf web/dist pentestd
	# Drop generated embed assets; restore tracked //go:embed placeholder only.
	rm -rf internal/daemon/webfs/dist
	$(MAKE) ensure-embed-stub
