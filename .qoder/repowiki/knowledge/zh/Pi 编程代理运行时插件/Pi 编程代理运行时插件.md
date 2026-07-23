---
kind: external_dependency
name: Pi 编程代理运行时插件
slug: pi-coding-agent
category: external_dependency
category_hints:
    - vendor_identity
scope:
    - '**'
source_files:
    - internal/runtimeplugin/builtin.go
    - CONTEXT.md
---

内置 `pi` 运行时插件，通过本地 `pi` 二进制执行 Pi coding agent，支持 `openai_chat_completions`、`openai_responses`、`anthropic_messages` 三种协议，具备全局模型投影能力——每个 Pi 任务启动时自动将所有已就绪的 Model Provider 及其 API Key 暴露给 Pi 进程，后续 Runtime Turn 可在不重启的情况下切换 Provider。

- 角色：外部 AI 编程代理的运行时适配器之一
- 稳定约束：Pi Global Model Projection 在 Runtime 启动时解析一次，之后变更需重新投影并重启；Pi 的 Provider 切换走原生控制面而非 Config Projection