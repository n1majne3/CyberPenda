---
kind: logging_system
name: 基于标准库 log.Logger 的守护进程结构化日志系统
category: logging_system
scope:
    - '**'
source_files:
    - internal/daemon/logging.go
    - internal/daemon/server.go
    - cmd/pentestd/main.go
    - internal/adapters/adapters.go
    - internal/runtime/docker_sandbox.go
    - internal/runtime/output_reader.go
---

## 1. 使用的系统与框架
- 日志框架：Go 标准库 `log`（`*log.Logger`），未引入第三方结构化日志库。
- 输出目标：默认写入 stderr，由进程管理器或容器运行时收集；可通过 `Config.Logger` 注入自定义 logger 以替换 sink。
- 结构化字段：通过固定前缀 + key=value 片段实现“伪结构化”（如 `task phase=... runner=... provider=... id=... goal=...`），便于 grep/awk 解析。
- 级别策略：无显式 level 字段，仅按调用点语义区分 HTTP 请求、任务生命周期、沙箱事件、配置冲突等类别。

## 2. 核心文件与包
- `internal/daemon/logging.go` — 所有 daemon 层日志记录方法集中定义（HTTP 请求、任务阶段、Docker 沙箱事件、Custom Args 冲突等）。
- `internal/daemon/server.go` — `Server.logger *log.Logger` 字段声明及 `Config.Logger` 注入点。
- `cmd/pentestd/main.go` — 启动入口，使用默认 `log.Printf` 输出启动信息，并将 provider-bridge 诊断行也经 `log.Printf` 输出。
- `internal/adapters/adapters.go` — `Redact` / `NewRedactor` 提供敏感值脱敏能力，被日志路径广泛复用。
- `internal/runtime/docker_sandbox.go`、`internal/runtime/output_reader.go`、`internal/runtime/runtime.go` — 运行时侧通过 `adapters.Redact` 对事件 payload 脱敏后统一 emit，不直接写日志。

## 3. 架构与约定
- **单 Logger 注入**：`daemon.Config.Logger` 为可选 `*log.Logger`；当 nil 时回退到 `log.Default()`，使 `make dev` 下日志与启动信息混排。测试可通过注入空 logger 禁用输出。
- **分类化记录函数**：`logRequest`、`logTask`、`logTaskLaunchStage`、`logDockerSandboxEvent`、`logCustomArgConflict`、`logPreflightCustomArgConflict` 等封装了每类事件的字段模板，避免散落 `Printf`。
- **噪声抑制**：`isNoisyPoll` 识别 UI 高频轮询 GET（`/events`、`/transcript`、`/timeline`、`/tasks/{id}`），成功响应直接丢弃日志，错误仍保留。
- **安全脱敏**：所有可能包含密钥的 payload 在落盘/输出前必须经 `adapters.Redact` 处理；该工具同时支持“形状匹配模式”（Bearer、sk-、AKIA 等前缀）和已知 opaque token 列表。
- **字段一致性**：任务相关日志统一携带 `runner`、`provider`、`id`、`goal` 等键；沙箱事件额外带 `phase`、`image`、`stream`、`detail`，便于跨组件关联。
- **运行时与 daemon 解耦**：`runtime` 包只负责构造并 emit 标准化 `task.EventPayload`，由上层（daemon 或测试 harness）决定如何持久化/打印，避免 runtime 耦合具体 logger。

## 4. 开发者应遵循的规则
1. **不要直接在各处散用 `fmt.Print`/`os.Stdout`**：业务诊断一律走 `server.logXxx` 系列方法，保证字段一致且可被噪声过滤。
2. **任何可能含密钥的 map/slice/string 在写入日志前必须调用 `adapters.Redact`**：包括 Custom Args、launch command、sandbox stream text 等。
3. **新增日志类别时，在 `logging.go` 中新增对应 `logXxx` 方法**，并在调用方传入必要上下文（task ID、runner、profile 等），保持 key=value 风格。
4. **如需变更日志格式或增加 level**，应在 `logging.go` 内集中修改，而非在各 handler 中各自拼接字符串。
5. **测试中若需验证日志行为**，通过注入 `*log.Logger` 到 `Config.Logger`，或使用 `testing.T.Log` 捕获，不要依赖全局 `log.Default()`。
6. **CLI 子命令（pentestctl、provider-bridge）** 使用 `os.Stderr` 输出错误与提示，不属于 daemon 日志体系，无需经过 `adapters.Redact`（它们本身不接触用户数据）。