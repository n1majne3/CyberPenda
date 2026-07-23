---
kind: external_dependency
name: 嵌入式纯 Go SQLite 驱动
slug: sqlite-modernc
category: external_dependency
category_hints:
    - vendor_identity
scope:
    - '**'
source_files:
    - go.mod
    - internal/store/store.go
    - internal/runtime/codex_session.go
---

项目使用 `modernc.org/sqlite` 作为唯一数据库后端，以纯 Go 实现零 C 依赖的嵌入式 SQLite。所有持久化数据（Blackboard v2、任务、凭据绑定等）均落盘到单个 `pentest.db` 文件，并通过单连接 + WAL 模式运行。

- 角色：本地优先的数据存储层
- 集成点：`internal/store` 统一打开/迁移/权限收紧；`internal/runtime/codex_session.go` 读取 Codex 状态库
- 稳定约束：只允许一个进程写入，并发写错误（`sqlite_busy`/`sqlite_locked`）由上层转为 HTTP 409/重试语义