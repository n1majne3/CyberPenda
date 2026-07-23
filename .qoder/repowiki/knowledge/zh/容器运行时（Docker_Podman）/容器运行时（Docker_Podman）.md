---
kind: external_dependency
name: 容器运行时（Docker/Podman）
slug: docker-podman
category: external_dependency
category_hints:
    - client_constraint
scope:
    - '**'
source_files:
    - internal/daemon/server.go
    - docker-compose.yaml
    - README.md
---

Sandbox Runner 通过外部容器 CLI 启动隔离的 Sandbox 容器来运行 Task。CLI 名称通过 `-container-cli` / `PENTEST_CONTAINER_CLI` 配置，默认 `docker`，也接受 `podman`。Compose 挂载宿主机 Docker socket 使 app 容器能创建 sandbox 子容器。

- 角色：Sandbox Runner 的执行边界，隔离文件系统、依赖与进程环境
- 集成点：Daemon Config 中的 `ContainerCLI` 字段；Compose 挂载 `/var/run/docker.sock`
- 稳定约束：Sandbox Runner 失败不会自动回退到 Host Runner；沙箱镜像名可通过 `PENTEST_SANDBOX_IMAGE` 覆盖