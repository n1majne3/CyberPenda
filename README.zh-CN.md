# CyberPenda

[English](README.md) · [中文](README.zh-CN.md)

**本地优先的授权渗透测试 Agent**：Go 控制面 + React 控制台 + Codex / Claude Code / Pi 运行时，带项目范围与审批、Goal/Step/Fact 黑板，工具在沙箱内执行（不经 daemon 代理）。

> **仅可对你有权测试的系统使用。** 范围、审批与主机 Runner 激活是一等公民能力，不是事后补丁。

**[打开在线 Demo →](https://cyberpenda-demo.vercel.app)** · 只读样例项目（不含 `pentestd`、运行时或利用工具）

### TSecBench v1 成绩

[TSecBench](https://tsecbench.zc.tencent.com/) 官方 Agentic 跑分榜（**TSecBench v1**）：**第 3 名 · 93.82 / 100**

| Web | 二进制 | 利用 | 渗透 | 云 | 规避 |
| --- | --- | --- | --- | --- | --- |
| 98.12 | 100 | 100 | 71.43 | 100 | 100 |

榜单记录配置：`deepseek-v4.1-flash-expires-on-0910` · 有效耗时约 5h 31m。CyberPenda 提供该基准的 Hosted Mode 镜像，详见英文 README 的 [TSecBench Hosted evaluation](README.md#tsecbench-hosted-evaluation) 与 [docs/tsecbench/README.md](docs/tsecbench/README.md)。

![TSecBench v1 官方榜 — CyberPenda 第 3](docs/assets/tsecbench-v1-leaderboard.png)

### 适合谁

- 需要**本机** Agent 工作区、并希望 findings 可沉淀的红队 / AppSec 工程师
- 已在用 **Codex、Claude Code 或 Pi**，并需要带范围约束的渗透会话
- 不愿把目标凭证和浏览器会话交给云端 Agent 浏览器的人

### 为什么是这个仓库

| | CyberPenda | 常见 CLI skill 包 | 云端渗透 / AI 浏览器 |
| --- | --- | --- | --- |
| 运行位置 | 本机（SQLite + 本地 UI） | 终端 | 厂商云 |
| 运行时 | Codex、Claude Code、Pi 插件 | 通常单一模型/CLI | 仅内置 Agent |
| 范围与审批 | 一等项目控制 | 主要靠提示词 | 厂商策略 |
| 工具执行 | 沙箱 / 主机 Runner 内 | 临时 shell | 厂商沙箱 |

## 架构

| 组件 | 职责 |
| --- | --- |
| `pentestd` | 本地 HTTP daemon：SQLite、Runtime Harness、FGS 接收、内嵌 UI |
| React 控制台 | 项目面板、启动控制、黑板、结果、设置 |
| Sandbox runner | 默认 Runner — 隔离 runtime home、workdir 与进程环境（Docker/Podman） |
| Host runner | 显式选择启用；绝不会从沙箱自动降级 |
| Runtime Outbox | 按 continuation 作用域的 FGS 更新与持久 Receipt |
| `pentestctl` | 投影 Runtime 内的 FGS 发布与读取；保留遗留命令 |
| Runtime 插件 | 声明式适配器（Codex、Claude Code、Pi、fake） |
| Skills / 扩展 | 运行时无关 skill 包 + 运行时专用扩展包 |

默认数据落在本机：SQLite（`pentest.db`）、任务运行目录与托管产物根目录。

## 快速开始

### 前置条件

- Go（见 `go.mod`）
- Node.js 20.19+ 或 22.12+（UI 构建 / `make dev`）
- GNU Make（源码构建）
- **Sandbox Runner** 所需的 Linux 容器引擎：
  - **macOS（此处默认）：** [OrbStack](https://orbstack.dev/) + Docker 兼容 CLI
  - **Linux：** Docker Engine 或 Podman
  - **Windows：** 原生 `pentestd.exe` + Docker Desktop / Podman Desktop（Desktop WSL 中的 Linux 容器）

引擎矩阵见 [ADR 0025](docs/adr/0025-container-engine-support-matrix.md) 与 [docs/platform-engines.md](docs/platform-engines.md)。

### 本地开发

```sh
# 后端 :8787 + Vite UI（/api 代理）
make dev
```

打开前端打印的 Vite URL。更完整的构建、Compose、沙箱镜像与 Hosted 评测步骤见 [英文 README](README.md)。

### 构建自包含 daemon

```sh
make build      # 先构建 UI 到本地 embed 路径，再构建 pentestd
./pentestd
```

默认监听 `http://127.0.0.1:8787`。

## 文档

- [文档索引](docs/README.md)
- [领域词汇](CONTEXT.md)
- [FGS Runtime 协议](docs/specs/fgs-runtime-outbox-protocol.md)
- [ADRs](docs/adr/)
- 完整英文说明：[README.md](README.md)

## 许可 / 授权声明

CyberPenda **仅用于授权安全测试**。操作者须自行确保合法范围、凭证与交规。禁止在未经许可的系统上使用本软件。

本仓库以 [MIT License](LICENSE) 发布。
