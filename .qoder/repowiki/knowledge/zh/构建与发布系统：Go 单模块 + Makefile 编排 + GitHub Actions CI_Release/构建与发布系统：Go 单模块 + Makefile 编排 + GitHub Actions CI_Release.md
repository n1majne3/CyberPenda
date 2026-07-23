---
kind: build_system
name: 构建与发布系统：Go 单模块 + Makefile 编排 + GitHub Actions CI/Release
category: build_system
scope:
    - '**'
source_files:
    - Makefile
    - go.mod
    - scripts/build-release-binaries.sh
    - .github/workflows/ci.yml
    - .github/workflows/release.yml
    - .github/workflows/publish-sandbox.yml
    - docker/pentestd/Dockerfile
    - docker/pentest-sandbox/Dockerfile
    - scripts/ensure-web-deps.sh
---

## 1. 构建系统与工具链

- **语言与依赖**：单一 Go 模块 `pentest`（go.mod），Go 版本锁定为 1.25.0，仅依赖 modernc.org/sqlite（纯 Go SQLite）及少量第三方库。
- **前端工程**：React 19 + Vite 8 + Tailwind，位于 `web/`，通过 npm ci 安装依赖、Vite 构建产物嵌入到 Go 二进制中。
- **统一入口**：Makefile 提供 `dev`、`build`、`test`、`build-sandbox-image`、`smoke-*` 等目标，屏蔽 Go/Vite/Docker 差异。

## 2. 本地开发流程

- `make dev`：并行启动 `cmd/pentestd`（监听 :8787）与 `web` 的 Vite dev server，通过 `/health` 轮询确保后端就绪后再启动前端；任一进程退出即终止另一个。
- `make build-ui`：先执行 `scripts/ensure-web-deps.sh` 修复缺失/过期的 node_modules 与 Rolldown 原生绑定，再 `npm run build` 并将 `web/dist` rsync 到 `internal/daemon/webfs/dist`。
- `make check-ui-sync`：在提交前校验已嵌入的 UI 是否与 fresh build 一致，失败时保留新文件供 review。
- `make install-git-hooks`：将 `.githooks/pre-push` 设为仓库 hooks，防止未同步的嵌入式 UI 被推送。

## 3. 测试体系

- `make test-backend` / `make test-ci`：`go test ./...`，CI 默认只跑单元测试与集成测试，不拉 Docker 镜像、不调用 LLM。
- Live smoke 测试：`make smoke-sandbox-mcp` 与 `make smoke-runtime-tasks` 分别驱动沙箱 MCP 连通性与真实 Provider（Codex/Claude/Pi）任务，需要 Docker 与 provider 凭据。
- Node 侧测试：`cmd/pentest-claude-sdk-bridge/bridge.test.mjs` 由 CI 的 web job 直接以 `node --test` 运行。

## 4. 交叉编译与发布

- `scripts/build-release-binaries.sh <version> [dist-dir]`：
  - 默认构建 `linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64, windows/arm64` 六平台。
  - 通过 `GOOS/GOARCH CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${version}"` 生成静态二进制。
  - Windows 输出 `.exe` 并打包为 zip，其余平台打包为 tar.gz；最后在同一目录生成 `SHA256SUMS`。
  - 可通过环境变量 `PENTEST_RELEASE_TARGETS` 覆盖目标列表。
- Release 工件命名：`cyberpenda_<version>_<os>_<arch>.tar.gz|zip`。

## 5. CI 流水线（GitHub Actions）

- **`.github/workflows/ci.yml`**
  - `test`：setup-go（从 go.mod 读取版本）→ `make test-ci`。
  - `web`：setup-node@4 + cache → `npm ci` → bridge 测试 → `npm run lint` → `make check-ui-sync`。
  - `app-image`：docker/setup-buildx + build-push（仅 linux/amd64，push=false）验证 Dockerfile。
  - `sandbox-smoke-changes`：基于 diff 检测是否需要触发 sandbox smoke。
  - `smoke-sandbox-mcp`：按需构建 sandbox 镜像并通过 `scripts/with-pentestd-live.sh` 拉起 pentestd 执行 live smoke。
- **`.github/workflows/release.yml`**（tag `v*` 触发）
  - `build-release-binaries`：Node 20 构建嵌入 UI → 调用 `scripts/build-release-binaries.sh "${GITHUB_REF_NAME}" dist/release` → 上传 artifact。
  - `publish-release`：使用 `gh release create/upload --verify-tag --generate-notes` 发布 GitHub Release。
  - `publish-app-image`：多架构（linux/amd64, linux/arm64）构建 pentestd 镜像，使用 docker/metadata-action 生成 semver/latest 标签并 push 到 GHCR。
- **`.github/workflows/publish-sandbox.yml`**（workflow_dispatch 手动触发）
  - 矩阵式在 ubuntu-latest 与 ubuntu-24.04-arm 上分别构建 sandbox 镜像，输出 digest 作为 artifact。
  - 第二 job 下载所有 digest，用 `docker buildx imagetools create` 合成多架构 manifest 并推送到 `ghcr.io/<repo>-sandbox`。

## 6. 容器化策略

- **应用镜像 `docker/pentestd/Dockerfile`**：
  - 多阶段：`node:20-bookworm-slim` 构建前端 → `golang:1.25-bookworm` 嵌入 dist 并交叉编译 → `alpine:3.22` 运行时镜像。
  - 暴露 8787，挂载 `/data` 持久化 DB 与 runs，HEALTHCHECK 探测 `/health`。
  - 通过 `--platform=$BUILDPLATFORM` 支持跨平台构建。
- **沙箱镜像 `docker/pentest-sandbox/Dockerfile`**：
  - 基于 `kalilinux/kali-rolling`，预装 Kali headless、nmap/sqlmap/nuclei/httpx 等渗透工具，以及 Claude Code、OpenAI Codex、Pi Coding Agent CLI。
  - 内置 `pentest-provider-bridge`（Go）与 `pentest-claude-sdk-bridge`（Node）用于非 PTY Provider 协议桥接。
  - 预置 agent-browser + Chromium，按 Skill 规范复制 skills 到 `/opt/pentest/skills`。
  - 可选 host-proxy-only entrypoint 配合 iptables 实现出站边界控制。

## 7. 版本与注入约定

- 二进制版本通过 `-X main.version=<version>` ldflags 注入，release 脚本与 Dockerfile 均传递版本号。
- Web 前端构建产物必须与源码同步提交至 `internal/daemon/webfs/dist`，由 `check-ui-sync` 与 pre-push hook 共同保障。

## 8. 开发者应遵循的规则

1. **新增 Go 依赖**：更新 `go.mod`/`go.sum`，确保 `CGO_ENABLED=0` 下仍可交叉编译（当前 sqlite 为纯 Go，满足）。
2. **修改前端代码**：运行 `make build-ui` 或 `make dev`，确保 `internal/daemon/webfs/dist` 与 `web/dist` 一致，否则 CI 会失败。
3. **发布新版本**：打 `v*` tag 后自动触发 release workflow；如需自定义构建目标，设置 `PENTEST_RELEASE_TARGETS`。
4. **更新沙箱镜像**：手动触发 `publish-sandbox.yml` 并指定 `image_tag`；若仅修复 bridge 源码，可复用缓存层避免重建整个 Kali 环境。
5. **本地 live 测试**：确保 Docker 可用且配置了所需 Provider 凭据，再通过 `make smoke-*` 运行。
