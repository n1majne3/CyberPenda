.PHONY: dev build build-ui test test-backend clean

# Run the daemon and the Vite dev server together for local development.
# The Vite proxy forwards /api and /health to the daemon on :8787.
dev:
	@trap 'kill 0' EXIT; \
	go run ./cmd/pentestd -addr 127.0.0.1:8787 -db pentest.db & \
	cd web && npm run dev

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

test-backend:
	go test ./...

clean:
	rm -rf web/dist internal/daemon/webfs/dist pentestd
