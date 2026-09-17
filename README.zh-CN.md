# CyberPenda

[English](README.md) · [中文](README.zh-CN.md)

**本地优先的授权渗透测试 Agent**：Go 控制面、React 控制台，以及 Codex / Claude Code / Pi 运行时，带项目范围与审批、Goal/Step/Fact 黑板 Blackboard；工具在沙箱内执行（不经 daemon 代理）。

> **仅可对你有权测试的系统使用。** 范围、审批与主机 runner 激活是一等产品概念，有其用意。

**[打开在线 Demo →](https://cyberpenda-demo.vercel.app)** · 只读样例项目（不含 `pentestd`、运行时或利用工具）

### TSecBench v1 成绩

官方 [TSecBench](https://tsecbench.zc.tencent.com/) Agentic 排行榜（**TSecBench v1**）：**#3 · 93.82 / 100**

| Web | 二进制 | 利用 | 渗透 | 云 | 规避 |
| --- | --- | --- | --- | --- | --- |
| 98.12 | 100 | 100 | 71.43 | 100 | 100 |

榜单记录的运行配置：`deepseek-v4.1-flash-expires-on-0910` · 墙钟时间约 5h 31m。CyberPenda 为该基准发布 Hosted Mode 镜像，见 [TSecBench Hosted 评测](#tsecbench-hosted-评测)。

![TSecBench v1 官方排行榜 — CyberPenda #3](docs/assets/tsecbench-v1-leaderboard.png)

### 适合谁

- 需要**本机** Agent 工作区、并希望 findings 可持久沉淀的红队 / AppSec 工程师
- 已在使用 **Codex、Claude Code 或 Pi**，并需要带范围约束的渗透会话
- 不愿把目标凭证和浏览器会话交给云端 Agent 浏览器的人

### 为什么是这个仓库

| | CyberPenda | 常见 CLI skill 包 | 云端渗透 / AI 浏览器 |
| --- | --- | --- | --- |
| 运行位置 | 本机（SQLite + 本地 UI） | 终端 | 厂商云 |
| 运行时 | Codex、Claude Code、Pi 插件 | 通常单一模型/CLI | 仅内置 Agent |
| 范围与审批 | 一等项目控制 | 主要靠提示词 | 厂商策略 |
| 工具执行 | 沙箱 / 主机 runner 内 | 临时 shell | 厂商沙箱 |

## 架构

| 组件 | 职责 |
| --- | --- |
| `pentestd` | 本地 HTTP daemon：SQLite 存储、Runtime Harness、FGS 接收、内嵌 UI |
| React 控制台 | 项目面板、启动控制、黑板 Blackboard、结果、设置 |
| Sandbox runner | 默认 runner，隔离 runtime home、workdir 与进程环境（Docker/Podman） |
| Host runner | 显式选择启用；绝不会从沙箱自动降级为后备 |
| Runtime Outbox | 按 Continuation 作用域的 FGS 更新，并带持久 Receipt |
| `pentestctl` | 投影 Runtime 内的 FGS 发布与读取；保留遗留命令 |
| Runtime 插件 | 声明式适配器（Codex、Claude Code、Pi、fake） |
| Skills / 扩展 | 运行时无关的 skill 包 + 运行时专用扩展包 |

默认数据落在本机：SQLite（`pentest.db`）、任务运行目录与托管产物根目录。

## 快速开始

### 前置条件

- Go（见 `go.mod`）
- Node.js 20.19+ 或 22.12+（UI 构建 / `make dev`）
- GNU Make（源码构建）
- **Sandbox Runner** 所需的 Linux 容器引擎：
  - **macOS（此处默认）：** [OrbStack](https://orbstack.dev/) + Docker 兼容 CLI
  - **Linux：** Docker Engine 或 Podman
  - **Windows：** 原生 `pentestd.exe` + Docker Desktop / Podman Desktop（Desktop WSL 机器中的 Linux 容器）

Windows 原生 `make build` 使用 Node、npm、GNU Make 和 Go。不需要 Git Bash、WSL、`rsync` 或 POSIX coreutils。Desktop 上的 Linux 机器只服务于 Sandbox Runner，不参与应用构建。

引擎矩阵见 [ADR 0025](docs/adr/0025-container-engine-support-matrix.md) 与
[docs/platform-engines.md](docs/platform-engines.md)
（OrbStack、Podman、Windows 原生 daemon + Desktop WSL）。

### 本地开发

```sh
# 后端 :8787 + 带 /api 代理的 Vite UI
make dev
```

`make dev` 会先构建内嵌 UI，再启动 daemon，然后启动 Vite。
因此两个端口启动时都带有当前 UI 代码。Vite 会立即反映 UI 编辑；
要刷新 daemon 内嵌 UI，需重启 `make dev`。

打开前端打印的 Vite URL（API 与健康检查代理到 `http://127.0.0.1:8787`）。
未配置 `PENTEST_AUTH_TOKEN` 且 daemon 绑定回环地址时，
本地 UI 会自动获得 HttpOnly 浏览器会话。daemon 重启后，
直接打开黑板 Blackboard 链接也可使用。若已配置认证，请用
`?token=...` 打开 UI；它会把 token 存入 session storage，并从可见 URL 中移除。

### 构建自包含 daemon

Linux 与 macOS：

```sh
make build      # 先构建 UI 到本地 embed 路径，再构建 pentestd
./pentestd
```

Windows Command Prompt 或 PowerShell：

```powershell
make build      # 先构建 UI 到本地 embed 路径，再构建 pentestd.exe
.\pentestd.exe
```

`internal/daemon/webfs/dist` 下的 React 构建产物**不会**提交入库。Docker 与 `make build` 会重新生成。仓库中跟踪的 `dist/.gitkeep` 仅用于让裸 Go 测试时 `//go:embed` 仍然有效。

默认监听地址：`http://127.0.0.1:8787`。在回环地址且未配置
认证 token 时，可打开该地址或直接打开黑板 Blackboard 链接。UI
通过同源浏览器会话获得操作者访问权限。Runtime 客户端
仍使用各自的 Continuation Interface 能力。

### Docker Compose

```sh
export PENTEST_AUTH_TOKEN="$(openssl rand -hex 24)"
docker compose up -d
# 打开 http://127.0.0.1:8787/?token=<token>
```

镜像默认：

- 应用：`ghcr.io/n1majne3/cyberpenda:latest`
- 沙箱：`ghcr.io/n1majne3/cyberpenda-sandbox:latest`

Compose 会挂载 Docker socket，以便应用容器启动沙箱任务容器。任务数据使用命名卷 `cyberpenda-data`，子沙箱从同一卷挂载各任务子路径。启动前请设置 `PENTEST_AUTH_TOKEN`；非回环绑定必须启用认证。需要时可用 `CYBERPENDA_DATA_VOLUME` 覆盖卷名。

### 沙箱镜像（从源码构建）

```sh
make build-sandbox-image   # 默认打标签 ghcr.io/n1majne3/cyberpenda-sandbox:latest

# 选用本地开发标签
SANDBOX_IMAGE=pentest-sandbox:dev make build-sandbox-image
```

用 `SANDBOX_IMAGE=...` 覆盖源码构建标签。源码冒烟相关 Make 目标使用该标签；直接调用 daemon 与脚本时，除非设置 `PENTEST_SANDBOX_IMAGE=...`，否则使用已发布的 GHCR 镜像。

## TSecBench Hosted 评测

CyberPenda 还构建面向 [TSecBench Hosted Mode](https://tsecbench.zc.tencent.com) 的自包含 `linux/amd64` 镜像。镜像运行隔离的 Hosted Controller（`pentest-tsecbench-hosted`），以及挑战客户端与捆绑的 Codex / Claude Code / Pi 运行时之一。TSecBench 提供 VPN 隔离网络、挑战生命周期与计分；容器只负责解题并输出 JSONL 转录。

### 构建镜像

构建目标始终面向 `linux/amd64`，与宿主机架构无关：

```sh
make build-tsecbench-hosted-image
```

默认标签为 `cyberpenda-tsecbench-hosted:local`。可用 `TSECBENCH_HOSTED_IMAGE=...` 覆盖。

构建完成后可验证镜像并查看捆绑的运行时：

```sh
make smoke-tsecbench-hosted-image
make tsecbench-hosted-runtime-inventory
```

若需 TSecBench 上传包，运行 `Build TSecBench Hosted Bundle` GitHub Actions 工作流（或在导出镜像后执行 `make build-tsecbench-hosted-bundle TSECBENCH_BUNDLE_VERSION=v1`）。该包将镜像归档为一个 `.tar.gz` Docker 文件，并附带校验和与组件清单。上传、环境模板与基于 VPN 的本地验证见 [docs/tsecbench/README.md](docs/tsecbench/README.md)。

### Hosted 环境变量

Hosted Mode 下，TSecBench 会注入 `BENCHMARK_BASE_URL` 与一次性 `BENCHMARK_TOKEN`。其余 `CYBERPENDA_*` 值在 TSecBench 页面填写（密钥从不存入本仓库）：

| 变量 | 含义 |
| --- | --- |
| `CYBERPENDA_RUNTIME` | `codex`（默认）、`claude_code` 或 `pi` |
| `CYBERPENDA_MODEL_PROTOCOL` | Codex 用 `openai_responses`；Claude Code 用 `anthropic_messages`；Pi 可用 `openai_chat_completions`、`openai_responses` 或 `anthropic_messages` |
| `CYBERPENDA_MODEL_BASE_URL` | 网关 base URL，以 `.tsecbench.gw` 结尾；不要追加操作后缀 |
| `CYBERPENDA_MODEL` | 网关提供的模型 ID |
| `CYBERPENDA_MODEL_API_KEY` | 专用、可撤销的评测模型 API key |
| `CYBERPENDA_REASONING_EFFORT` | 可选；`low`、`medium`、`high`、`xhigh` 或 `max` |
| `CYBERPENDA_TASK_GOAL_APPENDIX` | 可选；追加到必需 Hosted Task Goal 的文本 |
| `CYBERPENDA_AUTO_COMPACT_THRESHOLD` | 可选；Claude Code 压缩阈值（1-100） |
| `CYBERPENDA_AUTO_COMPACT_WINDOW` | 可选；Claude Code 压缩窗口（1-1048576） |
| `CYBERPENDA_MAX_OUTPUT_TOKENS` | 可选；最大输出 token 数（1-1048576）；支持 Claude Code 与 Pi |
| `CYBERPENDA_CONTEXT_WINDOW` | 可选；总上下文容量（token，1-1048576）；支持 Claude Code 与 Pi |
| `CYBERPENDA_PI_ADDITIONAL_MODEL_N` | 可选；仅 Pi 的附加模型槽位（N = 1-3），可带 `_PROTOCOL`、`_BASE_URL`、`_API_KEY` 覆盖；投影到与父模型相同的 Pi profile |
| `CYBERPENDA_CHALLENGE_ADAPTER` | 可选；挑战适配器 id；默认为 `tsecbench` |

上下文容量与压缩窗口是分开的设置。显式的上下文
与输出上限优先于 Model Capability Cache。若某值为
空，CyberPenda 在可用时使用缓存值，否则使用 Runtime 默认值。
两项压缩设置仅适用于 Claude Code；Pi 使用其原生
压缩设置。

| Hosted 设置 | Claude Code 投影 | Pi 在 `models.json` 中的投影 |
| --- | --- | --- |
| `CYBERPENDA_CONTEXT_WINDOW` | `CLAUDE_CODE_MAX_CONTEXT_TOKENS` | `contextWindow` |
| `CYBERPENDA_MAX_OUTPUT_TOKENS` | `CLAUDE_CODE_MAX_OUTPUT_TOKENS` | `maxTokens` |

对于上下文 1048576 token、输出上限 393216 token 的模型，可使用：

```env
# Claude Code 与 Pi。请设为模型的实际上限。
CYBERPENDA_CONTEXT_WINDOW=1048576
CYBERPENDA_MAX_OUTPUT_TOKENS=393216

# 仅 Claude Code。留空则使用其原生默认值。
CYBERPENDA_AUTO_COMPACT_WINDOW=524288
CYBERPENDA_AUTO_COMPACT_THRESHOLD=80
```

Claude Code 对已识别的 Claude 模型 ID 以及带 `[1m]` 的 ID
在上下文覆盖上有原生限制。CyberPenda 不会为强制覆盖而关闭压缩。
这些限制见 [Hosted 配置指南](docs/tsecbench/README.md#hosted-mode)，
完整配置见 [环境模板](docs/tsecbench/tsecbench.env.example)。

#### Pi 附加模型

Pi 最多接受三个可选的附加模型槽位，以便
`@tintinweb/pi-subagents` 插件能在不同模型上运行子 agent。每个
槽位为 `CYBERPENDA_PI_ADDITIONAL_MODEL_N`（N = 1-3），外加可选的
`_PROTOCOL`、`_BASE_URL` 与 `_API_KEY` 覆盖。省略的覆盖项
继承 `CYBERPENDA_MODEL_PROTOCOL`、`CYBERPENDA_MODEL_BASE_URL` 与
`CYBERPENDA_MODEL_API_KEY`；无任何覆盖的槽位会加入父
provider 的目录。槽位稀疏且相互独立：可设置槽位 2 而不设置
槽位 1，且每个槽位只继承父级，从不继承其他
槽位。除非 `CYBERPENDA_RUNTIME=pi`，否则这些变量会被拒绝；
存在但为空的值无效（应完全不设置该变量）；没有模型 id
的覆盖也无效。`CYBERPENDA_CONTEXT_WINDOW` 与
`CYBERPENDA_MAX_OUTPUT_TOKENS` 作用于每个投影模型；reasoning
effort 仍是父会话设置。

槽位 1-3 均可用，前缀形式相同。`_1` 指定
模型 id，`_1_PROTOCOL`、`_1_BASE_URL`、`_1_API_KEY` 为其可选
覆盖；`_2` 与 `_3` 形状相同。槽位 base URL 遵守与
`CYBERPENDA_MODEL_BASE_URL` 相同的网关规则。

```env
# 槽位 1：无覆盖；继承协议、base URL 与 API key，
# 并加入父 provider 的目录。
CYBERPENDA_PI_ADDITIONAL_MODEL_1=pi-scout

# 槽位 2：第二个 Hosted 网关上的模型，使用专用密钥。
CYBERPENDA_PI_ADDITIONAL_MODEL_2=pi-researcher
CYBERPENDA_PI_ADDITIONAL_MODEL_2_BASE_URL=http://SECOND_HOST.tsecbench.gw/v1
CYBERPENDA_PI_ADDITIONAL_MODEL_2_API_KEY=SECOND_DEDICATED_KEY
CYBERPENDA_PI_ADDITIONAL_MODEL_2_PROTOCOL=openai_chat_completions
```

以相同协议、base URL 与 API key 投影同一模型 id
只会投影一次；同一模型 id 以不同协议、base
URL 或 API key 投影两次会导致 bootstrap 失败。共享完整有效
provider 元组的模型 id 共用一个投影 Model Provider。`CYBERPENDA_CONTEXT_WINDOW`
与 `CYBERPENDA_MAX_OUTPUT_TOKENS` 作用于每个投影模型；
`CYBERPENDA_REASONING_EFFORT` 仍是父会话设置。父
会话仍在 `CYBERPENDA_MODEL` 上启动；附加模型只拓宽
投影的 Pi 注册表。子 agent 调用规则（哪个角色使用
哪个模型、何时不要切换）写在 `CYBERPENDA_TASK_GOAL_APPENDIX`，切勿写入
API key。子 agent 模型选择器规则与发现流程见
[Hosted 配置指南](docs/tsecbench/README.md#hosted-mode)。

## 典型工作流

1. 创建 **Project**，选择 **Project Kind**，并定义 **Scope**。
2. 配置全局 **Model Provider** 及其 API key 环境变量。
3. 以目标、匹配的 **Task Type** 与 **Launch Selection** 启动 **Task**。
   仅在需要可复用的高级配置时选择 **Runtime Profile**。
4. 默认使用 **Sandbox Runner**。**Host Runner** 需显式激活。
5. 继续或引导同一 Task。查看其对话与 Runtime 活动。
6. 启用黑板 Blackboard 时，Runtime 通过 Outbox 发布 Goal、Step 与 Fact 更新。
   Harness 记录已接受状态与 Receipt。
7. 查看黑板 Blackboard，并从 Report 将已接受的 FGS 状态导出为 Markdown。

新的启动控件可选 FGS 或 Disabled。Task 默认 FGS；Non-Project
Session 默认 Disabled。启用时的线协议值仍为 `working_graph`；
新的 `interactive` 输入会映射到该值，且不改写历史快照。
FGS 同时处理历史上两种启用模式值。遗留的 Working Graph Intent
发布、编译与结算已退役。Disabled 的 Runtime
Owner 不会获得黑板 Blackboard 上下文或权限。历史黑板 Blackboard 记录
与 Evidence 文件会保留，不会转换为 FGS。

FGS 导出描述已接受的 Goal、Step 与 Fact。它不断言
CVSS 评分、已验证 Finding，或 Challenge Platform 验收。

在 Settings → Runtime Profiles 中，**View actual config** 按需加载已保存 profile 的
脱敏原生配置。同一视图还提供配置编辑
与导入。导入配置前请先保存表单更改。遗留 Model Provider
迁移仅对符合条件的 profile 显示。

Skills 支持托管导入。Runtime Profiles 支持本地 registry
扩展、显式扩展引用，以及外部 MCP 配置。
没有远程插件目录浏览器，也没有内置黑板 Blackboard MCP 服务器。

常规 Project Challenge Workflow 已退役。带有保留 Attempts 或
Operations 的 Task 会显示只读 Challenge 历史。旧写入路由返回 HTTP 410；
`--challenge-platform-config` 与 `PENTEST_CHALLENGE_PLATFORM_CONFIG` 已移除。
Task Policy 限制仅作为历史元数据保留，不再
在启动时强制执行或展示。已存储状态与 Evidence 保持不变；
未完成操作不会重放。请在原
Platform 上检查未完成工作。这些记录不阻塞 Task Finish，且 Finish 不确认 Platform
完成。Hosted 评测继续使用独立的 Hosted Challenge Client。

领域术语定义见 [CONTEXT.md](CONTEXT.md)。

## Make 目标

| 目标 | 说明 |
| --- | --- |
| `make dev` | 本地开发用 daemon + Vite 前端 |
| `make build-ui` | 将 React UI 构建到本地（gitignore）embed 路径 |
| `make build` | `build-ui` + 编译带内嵌 UI 的 `pentestd` |
| `make build-sandbox-image` | 构建本地沙箱容器镜像 |
| `make build-tsecbench-hosted-image` | 构建本地 TSecBench Hosted 镜像（`linux/amd64`） |
| `make smoke-tsecbench-hosted-image` | 对已构建 Hosted 镜像运行无能力冒烟测试 |
| `make tsecbench-hosted-runtime-inventory` | 打印已构建 Hosted 镜像中捆绑的运行时版本 |
| `make build-tsecbench-hosted-bundle TSECBENCH_BUNDLE_VERSION=v1` | 从已构建镜像导出 Hosted 上传包 |
| `make test` / `make test-backend` | Go 单元与集成测试 |
| `make test-ci` | 适合 CI 的测试（无 Docker、无 LLM 凭证） |
| `make test-concurrency` | Runtime 生命周期竞态检查（打乱顺序，单核/四核） |
| `make smoke-sandbox-fgs` | 实况冒烟：沙箱 → Runtime Outbox → 已接受 FGS |
| `make smoke-runtime-tasks` | Codex / Claude / Pi 实况冒烟（需 Docker + provider 凭证） |
| `make clean` | 删除已构建 UI 产物与 `pentestd` 二进制 |

## Daemon 标志与环境变量

常用 `pentestd` 选项（标志或环境变量）：

| 标志 | 环境变量 | 默认值 |
| --- | --- | --- |
| `-addr` | `PENTEST_LISTEN_ADDR` | `127.0.0.1:8787` |
| `-db` | `PENTEST_DB` | `pentest.db` |
| `-runtime-root` | `PENTEST_RUNTIME_ROOT` | （空 → daemon 默认） |
| `-sandbox-image` | `PENTEST_SANDBOX_IMAGE` | `ghcr.io/n1majne3/cyberpenda-sandbox:latest` |
| `-container-cli` | `PENTEST_CONTAINER_CLI` | `auto`（PATH 探测：先 `docker`，再 `podman`） |
| `-task-volume` | `PENTEST_TASK_VOLUME` | （空；Compose 会设置命名数据卷） |
| `-task-volume-root` | `PENTEST_TASK_VOLUME_ROOT` | 设置 `-task-volume` 时为 `/data` |
| `-auth-token` | `PENTEST_AUTH_TOKEN` | （非回环绑定必填） |
| `-runtime-plugin-dirs` | `PENTEST_RUNTIME_PLUGIN_DIRS` | 受信任插件目录 |
| `-runtime-extension-dirs` | `PENTEST_RUNTIME_EXTENSION_DIRS` | 受信任扩展目录 |

沙箱网络说明：

- 默认 bridge 适用于 OrbStack、Docker 与 rootful Podman。
- 可选启用 **Sandbox VPN TUN**（`run_controls.sandbox_vpn_tun`）会挂载 `/dev/net/tun` 并授予 `NET_ADMIN` 以支持 OpenVPN。它不能与 `host_proxy_only` 组合使用，且 **rootless Podman 对该选项会预检失败**。
- 在 Windows 上，daemon **以原生 Windows 进程运行**；仅沙箱容器运行在 Docker/Podman Desktop 的 **WSL/Linux VM** 中。任务 bind mount 使用 `--mount type=bind`，并将 Windows 路径规范化为 `C:/...` 形式，避免盘符破坏挂载解析。若创建因挂载失败，请在 Desktop File Sharing 中共享 runtime-root 所在盘符。

认证（已配置时）：API 路由使用 `Authorization: Bearer <token>` 或 `?token=`。

## Runtime CLI（`pentestctl`）

在已启用的 Runtime 内，Config Projection 提供 Owner、Continuation、
API、接口凭证与工作目录环境：

```sh
pentestctl working-graph emit --input update.json
pentestctl working-graph read --limit 100
pentestctl working-graph status
pentestctl working-graph history --key goal:inspect
```

`emit` 发布更新并最多等待 5 秒以获取 Receipt。接受时返回
`applied`；需要修复时返回拒绝 Receipt 并以非零退出码退出。使用 `--wait 0` 仅做发布，或用 `--wait 10s` 更改
等待时间（最长 30 秒）。超时返回 `published` 与非零
退出码：更新仍已发布，但接受状态未知。请先运行
`status`，并在依赖报告前修复任何原先被拒绝的更新。不要因为缺少 Receipt
而重新发布或撤回更新。
`--input -` 从 stdin 读取一个 JSON 对象。
read 与 history 命令使用受信任 HTTP 接口。Disabled Runtime
Owner 无法使用这些命令。

较旧的 `blackboard` 命令仍保留，用于遗留接口与
历史。它们不是 FGS 写入路径。`/mcp` 已退役并返回 404。

## 项目布局

```
cmd/pentestd/          Daemon 入口
cmd/pentestctl/        CLI 入口
internal/              领域服务、适配器、daemon HTTP、runner、存储
web/                   React + Vite 控制台
docker/                Daemon 与沙箱 Dockerfile
skills/                Daemon 拥有的运行时扩展库（未跟踪；内置源在 internal/skill/builtins/assets）
docs/                  产品文档与 ADR
scripts/               发布构建与实况冒烟
```

## 文档

- [文档索引](docs/README.md) — 当前工作流与历史参考
- [领域词汇表](CONTEXT.md) — 共享产品用语
- [FGS Runtime 协议](docs/specs/fgs-runtime-outbox-protocol.md) — 上报原则、Outbox 与 Receipt
- [ADR](docs/adr/) — 架构决策（skills 默认开启、model providers 与 profiles）

## 许可 / 授权

CyberPenda 仅用于**授权**安全测试。操作者对合法范围、凭证与约定规则负责。未经许可，请勿对任何系统使用本软件。
