# syntax=docker/dockerfile:1

# Build the isolated controller and the Pi protocol bridge for one linux/amd64
# Hosted Evaluation Run. This Dockerfile is separate from normal app and
# Sandbox images.
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN test "${TARGETOS}/${TARGETARCH}" = "linux/amd64"
RUN mkdir -p internal/daemon/webfs/dist \
  && printf '%s\n' '# Hosted Image has no exposed Web UI.' > internal/daemon/webfs/dist/.gitkeep
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -trimpath -ldflags="-s -w" \
      -o /out/pentest-tsecbench-hosted ./cmd/pentest-tsecbench-hosted \
  && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -trimpath -ldflags="-s -w" \
      -o /out/pentest-provider-bridge ./cmd/pentest-provider-bridge

FROM --platform=$TARGETPLATFORM kalilinux/kali-rolling:latest AS runtime

# This bounded baseline is deliberately smaller than pentest-sandbox. Do not
# add kali-linux-* meta-packages or mobile/reverse-engineering suites here.
RUN rm -f /etc/apt/apt.conf.d/docker-clean \
  && echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' > /etc/apt/apt.conf.d/keep-cache
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    apt-get update \
  && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    ca-certificates curl wget git jq ripgrep file binutils unzip xz-utils \
    bash dash zsh coreutils findutils grep sed gawk procps \
    python3 python3-pip python3-venv \
    golang-go build-essential gdb radare2 strace ltrace patchelf checksec \
    nmap netcat-openbsd socat dnsutils iproute2 iputils-ping openssh-client \
    nodejs npm chromium \
    libnss3 libnspr4 libdbus-1-3 libatk1.0-0 libatk-bridge2.0-0 libcups2 \
    libdrm2 libatspi2.0-0 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 \
    libgbm1 libxkbcommon0 libpango-1.0-0 libcairo2 libasound2t64 \
    fonts-unifont fonts-noto-color-emoji \
  && rm -rf /var/lib/apt/lists/*

# Runtime CLIs intentionally resolve current releases when the image is built.
# The exact resolved versions are captured below for the delivery inventory.
RUN --mount=type=cache,target=/root/.npm \
    npm install -g \
      @earendil-works/pi-coding-agent@latest \
      @openai/codex@latest \
      @anthropic-ai/claude-code@latest \
      agent-browser@latest

RUN pip3 install --no-cache-dir --break-system-packages pwntools

# Host Claude uses a persistent public Agent SDK Query. Install from the
# repository lock so the bridge and its dependency stay compatible.
WORKDIR /opt/pentest/claude-sdk-bridge
COPY cmd/pentest-claude-sdk-bridge/package.json cmd/pentest-claude-sdk-bridge/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --omit=dev
COPY cmd/pentest-claude-sdk-bridge/bridge.mjs cmd/pentest-claude-sdk-bridge/main.mjs ./
RUN printf '%s\n' '#!/bin/sh' 'exec node /opt/pentest/claude-sdk-bridge/main.mjs "$@"' \
      > /usr/local/bin/pentest-claude-sdk-bridge \
  && chmod 0755 /usr/local/bin/pentest-claude-sdk-bridge

COPY --from=go-build /out/pentest-tsecbench-hosted /usr/local/bin/pentest-tsecbench-hosted
COPY --from=go-build /out/pentest-provider-bridge /usr/local/bin/pentest-provider-bridge

# Keep Chromium headless and use no runtime browser download.
RUN mkdir -p /root/.agent-browser /opt/cyberpenda /data \
  && printf '%s\n' '{"executablePath":"/usr/bin/chromium"}' > /root/.agent-browser/config.json \
  && PI_VERSION="$(node -p "require('$(npm root -g)/@earendil-works/pi-coding-agent/package.json').version")" \
     CODEX_VERSION="$(node -p "require('$(npm root -g)/@openai/codex/package.json').version")" \
     CLAUDE_VERSION="$(node -p "require('$(npm root -g)/@anthropic-ai/claude-code/package.json').version")" \
     node -e 'const fs=require("fs"); fs.writeFileSync("/opt/cyberpenda/runtime-versions.json", JSON.stringify({schema:"cyberpenda-hosted-runtime-versions/v1",runtimes:{pi:{package:"@earendil-works/pi-coding-agent",version:process.env.PI_VERSION,binary:"pi"},codex:{package:"@openai/codex",version:process.env.CODEX_VERSION,binary:"codex"},claude_code:{package:"@anthropic-ai/claude-code",version:process.env.CLAUDE_VERSION,binary:"claude"}}},null,2)+"\n")' \
  && command -v pi \
  && command -v codex \
  && command -v claude \
  && command -v bash \
  && command -v git \
  && command -v curl \
  && command -v jq \
  && command -v rg \
  && command -v python3 \
  && command -v go \
  && command -v gcc \
  && command -v g++ \
  && command -v make \
  && command -v gdb \
  && command -v radare2 \
  && command -v strace \
  && command -v ltrace \
  && command -v patchelf \
  && command -v checksec \
  && command -v nmap \
  && command -v nc \
  && command -v socat \
  && command -v dig \
  && command -v ip \
  && command -v ss \
  && command -v ping \
  && command -v ssh \
  && command -v chromium \
  && command -v agent-browser \
  && command -v pentest-provider-bridge \
  && command -v pentest-claude-sdk-bridge \
  && command -v pentest-tsecbench-hosted \
  && python3 -c 'import pwn'

ENV CYBERPENDA_HOSTED_DATA_ROOT=/data \
    CYBERPENDA_PROVIDER_BRIDGE=/usr/local/bin/pentest-provider-bridge \
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
    AGENT_BROWSER_EXECUTABLE_PATH=/usr/bin/chromium \
    PI_OFFLINE=1

USER root
WORKDIR /data
ENTRYPOINT ["/usr/local/bin/pentest-tsecbench-hosted"]
