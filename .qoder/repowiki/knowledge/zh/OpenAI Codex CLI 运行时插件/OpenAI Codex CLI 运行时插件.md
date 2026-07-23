---
kind: external_dependency
name: OpenAI Codex CLI 运行时插件
slug: openai-codex-cli
category: external_dependency
category_hints:
    - vendor_identity
scope:
    - '**'
source_files:
    - internal/runtimeplugin/builtin.go
    - CONTEXT.md
---

内置 `codex` 运行时插件，通过本地 `codex` 二进制执行 OpenAI Codex CLI，支持 `openai_responses` 协议、持久会话恢复与 MCP 配置。非交互默认参数 `--dangerously-bypass-approvals-and-sandbox` 由 Harness 自动注入。

- 角色：外部 AI 编程代理的运行时适配器之一
- 稳定约束：必须显式选择，不会自动回退；Provider 切换需要重启 Runtime 并生成新的 Task Runtime Configuration Version