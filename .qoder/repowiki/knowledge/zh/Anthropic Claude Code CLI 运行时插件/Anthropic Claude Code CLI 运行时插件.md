---
kind: external_dependency
name: Anthropic Claude Code CLI 运行时插件
slug: anthropic-claude-code-cli
category: external_dependency
category_hints:
    - vendor_identity
scope:
    - '**'
source_files:
    - internal/runtimeplugin/builtin.go
    - CONTEXT.md
---

内置 `claude_code` 运行时插件，通过本地 `claude` 二进制执行 Anthropic Claude Code CLI，支持 `anthropic_messages` 协议、持久会话恢复与 MCP 配置。非交互默认参数 `--dangerously-skip-permissions --permission-mode bypassPermissions` 由 Harness 自动注入。

- 角色：外部 AI 编程代理的运行时适配器之一
- 稳定约束：Provider 切换需重启 Runtime；Anthropic 端点在 Endpoint Backfill 时会去掉 base_url 末尾路径段以适配其版本化 messages 操作路径