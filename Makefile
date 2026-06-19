.PHONY: dev build build-ui build-sandbox-image test test-ci test-backend smoke-sandbox-mcp smoke-runtime-tasks clean

# Run the daemon and the Vite dev server together for local development.
# The Vite proxy forwards /api and /health to the daemon on :8787.
SANDBOX_IMAGE ?= pentest-sandbox:latest

dev:
	@trap 'kill 0' EXIT; \
	go run ./cmd/pentestd -addr 127.0.0.1:8787 -db pentest.db -sandbox-image $(SANDBOX_IMAGE) & \
	cd web && npm run dev

# Build the pentest sandbox image (requires gemini_kali-gemini-kali:latest base).
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
