.PHONY: dev build build-ui build-sandbox-image test test-ci test-backend smoke-sandbox-mcp smoke-runtime-tasks clean

# Run the daemon and the Vite dev server together for local development.
# The Vite proxy forwards /api and /health to the daemon on :8787.
SANDBOX_IMAGE ?= pentest-sandbox:latest

# macOS /bin/sh (bash 3.2) has no `wait -n`, so poll: if either child dies,
# surface the failure instead of silently running the other alone (which hid
# backend bind errors behind the foreground Vite output).
dev:
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

# Prove the configured sandbox image can reach daemon MCP and write a fact.
smoke-sandbox-mcp:
	@bash scripts/smoke-sandbox-mcp-live.sh

smoke-runtime-tasks:
	@python3 scripts/smoke-runtime-tasks-live.py

juice-shop-live:
	@python3 scripts/run-juice-shop-live.py

# Build the React UI and copy it into the embed location.
build-ui:
	cd web && npm install && npm run build
	rm -rf internal/daemon/webfs/dist
	cp -r web/dist internal/daemon/webfs/dist

# Build the daemon binary with the UI embedded.
build: build-ui
	go build -o pentestd ./cmd/pentestd

# Run all Go tests.
test: test-backend

# CI default: unit/integration tests only (no Docker, no LLM credentials).
test-ci: test-backend

test-backend:
	go test ./...

# Live smokes (local):
#   make smoke-sandbox-mcp     — sandbox image + daemon MCP, no LLM
#   make smoke-runtime-tasks   — Codex/Claude/Pi task smoke; needs Docker + provider creds
# Optional filters: PENTEST_SMOKE_ONLY=codex|claude_code|pi|pi_sandbox

clean:
	rm -rf web/dist internal/daemon/webfs/dist pentestd
