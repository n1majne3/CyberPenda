---
kind: external_dependency
name: 模型上下文协议（MCP）服务端
slug: model-context-protocol
category: external_dependency
category_hints:
    - vendor_identity
scope:
    - '**'
source_files:
    - go.mod
    - internal/mcpserver/server.go
    - internal/runner/mcp.go
    - docs/product/mvp.md
---

Daemon 内置 MCP 服务器，暴露六个 Blackboard v2 语义工具（fact/search/deprecate/relation/finding/evidence/report/task-summary），供受信任的 Runtime 在 `/mcp` 端点调用。MCP 客户端 SDK 来自 `github.com/modelcontextprotocol/go-sdk`。

- 角色：Runtime → Daemon 的受信任写入通道，与 CLI/HTTP 共享同一语义层
- 集成点：`internal/mcpserver` 注册工具、`internal/runner/mcp.go` 向 Runtime 注入可信 MCP 配置
- 稳定用法：仅白名单工具可用；沙箱内 URL 会改写为 host 可达地址并附带 auth token query 参数