#!/usr/bin/env bash
# Use a temporary daemon and a real container to check FGS delivery without a model.
set -euo pipefail

IMAGE="${PENTEST_SANDBOX_IMAGE:-ghcr.io/n1majne3/cyberpenda-sandbox:latest}"
export PENTEST_SANDBOX_IMAGE="${IMAGE}"
export PENTEST_SANDBOX_FGS_SMOKE=1
cd "$(dirname "$0")/.."
go test -count=1 -timeout 3m ./internal/daemon -run '^TestSandboxFGSOutboxLive$' -v
