#!/usr/bin/env bash
# Ensure the local dev toolchain (node/npm via nvm, Go) is on PATH for
# non-interactive shells.
#
# Why this exists:
#   nvm and the Go toolchain are exported by ~/.bashrc, but Makefile recipes
#   and other non-interactive bash invocations never source ~/.bashrc (and
#   .bashrc returns early for non-interactive shells anyway), so `node`,
#   `npm`, and `go` are invisible and `make build` fails with "command not
#   found".
#
# This file is sourced via BASH_ENV (set in the Makefile) so every
# non-interactive bash recipe gets the toolchain on PATH. It is a no-op when a
# tool is already installed system-wide (e.g. actions/setup-node /
# actions/setup-go in CI).
#
# Safe to source multiple times: it never calls `exit` and never sets `set -e`.

# --- node / npm (via nvm) -------------------------------------------------
if ! command -v node >/dev/null 2>&1 || ! command -v npm >/dev/null 2>&1; then
	had_node=0
	command -v node >/dev/null 2>&1 && had_node=1

	NVM_DIR="${NVM_DIR:-$HOME/.nvm}"

	# Activate nvm if present (respects the `default` alias when it is valid).
	if [ -s "$NVM_DIR/nvm.sh" ]; then
		# shellcheck source=/dev/null
		. "$NVM_DIR/nvm.sh" >/dev/null 2>&1 || true
	fi

	# nvm.sh may fail to activate a version when the default alias is stale, so
	# fall back to the newest installed nvm node and put its bin dir on PATH.
	if [ "$had_node" -eq 0 ] && ! command -v node >/dev/null 2>&1; then
		node_root="$NVM_DIR/versions/node"
		if [ -d "$node_root" ]; then
			latest="$(ls -1v "$node_root" 2>/dev/null | tail -1)"
			if [ -n "$latest" ] && [ -x "$node_root/$latest/bin/node" ]; then
				export PATH="$node_root/$latest/bin:$PATH"
			fi
		fi
	fi
fi

# --- Go toolchain ---------------------------------------------------------
if ! command -v go >/dev/null 2>&1; then
	for go_bin in \
		"${GOROOT:-}/bin" \
		"/usr/local/go/bin" \
		"$HOME/go/bin" \
		/usr/lib/go/bin; do
		if [ -n "$go_bin" ] && [ -x "$go_bin/go" ]; then
			export PATH="$go_bin:$PATH"
			break
		fi
	done
fi
