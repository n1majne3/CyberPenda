# syntax=docker/dockerfile:1

# Build the isolated controller and the Pi protocol bridge for one linux/amd64
# Hosted Evaluation Run. This Dockerfile is separate from normal app and
# Sandbox images.
FROM --platform=$BUILDPLATFORM node:20-bookworm-slim AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web-build /src/web/dist internal/daemon/webfs/dist
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN test "${TARGETOS}/${TARGETARCH}" = "linux/amd64"
RUN test -s internal/daemon/webfs/dist/index.html
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -trimpath -ldflags="-s -w" \
      -o /out/pentest-tsecbench-hosted ./cmd/pentest-tsecbench-hosted \
  && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -trimpath -ldflags="-s -w" \
      -o /out/pentest-provider-bridge ./cmd/pentest-provider-bridge \
  && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -trimpath -ldflags="-s -w" \
      -o /out/pentest-tsecbench-client ./cmd/pentest-tsecbench-client

FROM --platform=$TARGETPLATFORM kalilinux/kali-rolling:latest AS runtime

# This bounded baseline is deliberately smaller than pentest-sandbox. Keep
# mobile support limited to static APK inspection; do not add large suites.
RUN rm -f /etc/apt/apt.conf.d/docker-clean \
  && echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' > /etc/apt/apt.conf.d/keep-cache
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    apt-get update \
  && SYSTEMD_OFFLINE=1 DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    ca-certificates curl wget git jq ripgrep file binutils unzip xz-utils \
    bash dash zsh coreutils findutils grep sed gawk procps \
    python3 python3-pip python3-venv python3-pwntools python3-pil tesseract-ocr \
    default-jre-headless jadx apktool bsdextrautils \
    golang-go build-essential gdb gdb-multiarch radare2 strace ltrace patchelf checksec \
    nasm upx-ucl yara foremost xxd qemu-user-static \
    python3-capstone python3-pefile python3-yara python3-unicorn \
    ropper python3-ropgadget \
    smali dex2jar \
    nmap netcat-openbsd socat dnsutils iproute2 iputils-ping openssh-client openssl \
    ffuf gobuster sqlmap hydra john smbclient ftp php-cli \
    libimage-exiftool-perl binwalk steghide imagemagick tcpdump \
    redis-tools default-mysql-client postgresql-client p7zip-full \
    python3-requests python3-bs4 python3-lxml python3-pycryptodome \
    python3-pyinstaller \
    nodejs npm chromium \
    libnss3 libnspr4 libdbus-1-3 libatk1.0-0 libatk-bridge2.0-0 libcups2 \
    libdrm2 libatspi2.0-0 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 \
    libgbm1 libxkbcommon0 libpango-1.0-0 libcairo2 libasound2t64 \
    fonts-unifont fonts-noto-color-emoji \
  && rm -rf /var/lib/apt/lists/*

# Kali qemu-user 11 ships qemu-<arch>, not qemu-<arch>-static.
RUN for bin in /usr/bin/qemu-*; do \
      base="${bin##*/}"; \
      case "$base" in *-static) continue ;; esac; \
      [ -e "/usr/bin/${base}-static" ] || ln -s "$base" "/usr/bin/${base}-static"; \
    done

# These Python RE tools are not in Kali Rolling apt. Do not use this path for unicorn/pwntools.
# uncompyle6 needs xdis<6.2. Latest pyinstxtractor-ng pins xdis==6.3.0, so install it with --no-deps.
RUN --mount=type=cache,target=/root/.cache/pip \
    pip3 install --no-cache-dir --break-system-packages \
      volatility3 \
      'uncompyle6>=3.9.2' \
      'xdis>=6.1.1,<6.2.0' \
 && pip3 install --no-cache-dir --break-system-packages --no-deps \
      pyinstxtractor-ng \
 && if ! command -v pyinstxtractor-ng >/dev/null; then \
      printf '%s\n' '#!/usr/bin/env python3' 'from pyinstxtractor_ng import main' \
        'raise SystemExit(main())' \
        > /usr/local/bin/pyinstxtractor-ng \
      && chmod 0755 /usr/local/bin/pyinstxtractor-ng; \
    fi

# Runtime CLIs intentionally resolve current releases when the image is built.
# The exact resolved versions are captured below for the delivery inventory.
ARG RUNTIME_RELEASE_CACHE_BUST=local
RUN --mount=type=cache,target=/root/.npm \
    test -n "${RUNTIME_RELEASE_CACHE_BUST}" \
  && npm install -g \
      @earendil-works/pi-coding-agent@latest \
      @tintinweb/pi-subagents@latest \
      @openai/codex@latest \
      @anthropic-ai/claude-code@latest \
      agent-browser@latest

# Hermes Agent: latest release at image-build time. Root install.sh uses FHS
# (/usr/local/bin/hermes). Skip node-deps: that stage's `npm install` is fatal
# on timeout. ACP is a Python extra in python-deps. Hosted Tool Baseline
# already has Chromium and agent-browser, so skip browser and computer-use.
# xz-utils is in the apt layer for any later Node *.tar.xz unpack.
RUN set -eux; \
    curl -fsSL https://hermes-agent.nousresearch.com/install.sh -o /tmp/hermes-install.sh; \
    for stage in repository venv python-deps path config; do \
      bash /tmp/hermes-install.sh --non-interactive --skip-setup --skip-browser --skip-computer-use --stage "$stage"; \
    done; \
    rm -f /tmp/hermes-install.sh; \
    command -v hermes; \
    hermes --version | tee /opt/hermes-release.txt; \
    hermes acp --help >/dev/null

# Hosted Mode has no public PyPI. Hermes lazy-installs provider.anthropic
# (anthropic==0.87.0) on the first messages session/new. openai is already
# in python-deps for responses and chat_completions. The venv has no pip
# binary; install with the Hermes uv.
RUN /root/.hermes/bin/uv pip install --python /usr/local/lib/hermes-agent/venv/bin/python \
      'anthropic==0.87.0' \
 && /usr/local/lib/hermes-agent/venv/bin/python -c 'import anthropic, openai'

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
COPY --from=go-build /out/pentest-tsecbench-client /usr/local/bin/pentest-tsecbench-client
COPY internal/challengeadapter/adapters/ /opt/cyberpenda/adapters/
COPY docker/tsecbench-hosted/tsecbench-local-validate.py /usr/local/bin/tsecbench-local-validate
RUN chmod 0755 /usr/local/bin/tsecbench-local-validate \
  && ln -s /usr/local/bin/pentest-tsecbench-client /usr/local/bin/pentest-challenge-client

# Keep Chromium headless and use no runtime browser download.
RUN mkdir -p /root/.agent-browser /opt/cyberpenda /data /root/.pi/agent \
  && printf '%s\n' '{"packages":["npm:@tintinweb/pi-subagents","npm:pi-subagents"]}' > /root/.pi/agent/settings.json \
  && printf '%s\n' '{"executablePath":"/usr/bin/chromium"}' > /root/.agent-browser/config.json \
  && HERMES_VERSION="$(hermes --version 2>/dev/null | sed -n '1p' | awk '{print $NF}')" \
  && test -n "${HERMES_VERSION}" \
  && PI_VERSION="$(node -p "require('$(npm root -g)/@earendil-works/pi-coding-agent/package.json').version")" \
     CODEX_VERSION="$(node -p "require('$(npm root -g)/@openai/codex/package.json').version")" \
     CLAUDE_VERSION="$(node -p "require('$(npm root -g)/@anthropic-ai/claude-code/package.json').version")" \
     CLAUDE_AGENT_SDK_VERSION="$(node -p "require('/opt/pentest/claude-sdk-bridge/node_modules/@anthropic-ai/claude-agent-sdk/package.json').version")" \
     HERMES_VERSION="${HERMES_VERSION}" \
     node -e 'const fs=require("fs"); fs.writeFileSync("/opt/cyberpenda/runtime-versions.json", JSON.stringify({schema:"cyberpenda-hosted-runtime-versions/v1",runtimes:{pi:{package:"@earendil-works/pi-coding-agent",version:process.env.PI_VERSION,binary:"pi"},codex:{package:"@openai/codex",version:process.env.CODEX_VERSION,binary:"codex"},claude_code:{package:"@anthropic-ai/claude-code",version:process.env.CLAUDE_VERSION,binary:"claude"},hermes:{package:"hermes-agent",version:process.env.HERMES_VERSION,binary:"hermes"}},components:{claude_agent_sdk:{package:"@anthropic-ai/claude-agent-sdk",version:process.env.CLAUDE_AGENT_SDK_VERSION}}},null,2)+"\n")' \
  && missing_hosted_tool() { echo "missing hosted tool: $1" >&2; exit 1; } \
  && command -v pi || missing_hosted_tool pi \
  && command -v codex || missing_hosted_tool codex \
  && command -v claude || missing_hosted_tool claude \
  && command -v hermes || missing_hosted_tool hermes \
  && command -v bash || missing_hosted_tool bash \
  && command -v git || missing_hosted_tool git \
  && command -v curl || missing_hosted_tool curl \
  && command -v jq || missing_hosted_tool jq \
  && command -v rg || missing_hosted_tool rg \
  && command -v python3 || missing_hosted_tool python3 \
  && command -v go || missing_hosted_tool go \
  && command -v gcc || missing_hosted_tool gcc \
  && command -v g++ || missing_hosted_tool g++ \
  && command -v make || missing_hosted_tool make \
  && command -v gdb || missing_hosted_tool gdb \
  && command -v radare2 || missing_hosted_tool radare2 \
  && command -v strace || missing_hosted_tool strace \
  && command -v ltrace || missing_hosted_tool ltrace \
  && command -v patchelf || missing_hosted_tool patchelf \
  && command -v checksec || missing_hosted_tool checksec \
  && command -v nmap || missing_hosted_tool nmap \
  && command -v nc || missing_hosted_tool nc \
  && command -v socat || missing_hosted_tool socat \
  && command -v dig || missing_hosted_tool dig \
  && command -v ip || missing_hosted_tool ip \
  && command -v ss || missing_hosted_tool ss \
  && command -v ping || missing_hosted_tool ping \
  && command -v ssh || missing_hosted_tool ssh \
  && command -v openssl || missing_hosted_tool openssl \
  && command -v chromium || missing_hosted_tool chromium \
  && command -v agent-browser || missing_hosted_tool agent-browser \
  && command -v tesseract || missing_hosted_tool tesseract \
  && command -v java || missing_hosted_tool java \
  && command -v jadx || missing_hosted_tool jadx \
  && command -v apktool || missing_hosted_tool apktool \
  && command -v column || missing_hosted_tool column \
  && command -v ffuf || missing_hosted_tool ffuf \
  && command -v gobuster || missing_hosted_tool gobuster \
  && command -v sqlmap || missing_hosted_tool sqlmap \
  && command -v hydra || missing_hosted_tool hydra \
  && command -v john || missing_hosted_tool john \
  && command -v smbclient || missing_hosted_tool smbclient \
  && command -v php || missing_hosted_tool php \
  && command -v exiftool || missing_hosted_tool exiftool \
  && command -v binwalk || missing_hosted_tool binwalk \
  && command -v steghide || missing_hosted_tool steghide \
  && command -v convert || missing_hosted_tool convert \
  && command -v tcpdump || missing_hosted_tool tcpdump \
  && command -v redis-cli || missing_hosted_tool redis-cli \
  && command -v mysql || missing_hosted_tool mysql \
  && command -v psql || missing_hosted_tool psql \
  && command -v 7z || missing_hosted_tool 7z \
  && command -v gdb-multiarch || missing_hosted_tool gdb-multiarch \
  && command -v nasm || missing_hosted_tool nasm \
  && command -v upx || missing_hosted_tool upx \
  && command -v yara || missing_hosted_tool yara \
  && command -v foremost || missing_hosted_tool foremost \
  && command -v xxd || missing_hosted_tool xxd \
  && command -v qemu-x86_64 || missing_hosted_tool qemu-x86_64 \
  && command -v qemu-x86_64-static || missing_hosted_tool qemu-x86_64-static \
  && command -v ropper || missing_hosted_tool ropper \
  && command -v ROPgadget || missing_hosted_tool ROPgadget \
  && command -v smali || missing_hosted_tool smali \
  && command -v vol || missing_hosted_tool vol \
  && command -v uncompyle6 || missing_hosted_tool uncompyle6 \
  && command -v pydisasm || missing_hosted_tool pydisasm \
  && command -v pyi-archive_viewer || missing_hosted_tool pyi-archive_viewer \
  && command -v pyinstxtractor-ng || missing_hosted_tool pyinstxtractor-ng \
  && command -v pentest-challenge-client || missing_hosted_tool pentest-challenge-client \
  && command -v pentest-provider-bridge || missing_hosted_tool pentest-provider-bridge \
  && command -v pentest-claude-sdk-bridge || missing_hosted_tool pentest-claude-sdk-bridge \
  && command -v pentest-tsecbench-hosted || missing_hosted_tool pentest-tsecbench-hosted \
  && command -v pentest-tsecbench-client || missing_hosted_tool pentest-tsecbench-client \
  && command -v tsecbench-local-validate || missing_hosted_tool tsecbench-local-validate \
  && python3 -c 'import pwn, capstone, pefile, yara, unicorn, volatility3, xdis, uncompyle6, PyInstaller; from PIL import Image'

ENV CYBERPENDA_HOSTED_DATA_ROOT=/data \
    CYBERPENDA_PROVIDER_BRIDGE=/usr/local/bin/pentest-provider-bridge \
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
    IS_SANDBOX=1 \
    AGENT_BROWSER_EXECUTABLE_PATH=/usr/bin/chromium \
    PI_OFFLINE=1

USER root
WORKDIR /data
ENTRYPOINT ["/usr/local/bin/pentest-tsecbench-hosted"]
