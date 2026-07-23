---
kind: external_dependency
name: 镜像分发仓库 GHCR
slug: ghcr
category: external_dependency
category_hints:
    - vendor_identity
scope:
    - '**'
source_files:
    - cmd/pentestd/main.go
    - Makefile
    - docker-compose.yaml
    - .github/workflows/release.yml
    - .github/workflows/publish-sandbox.yml
---

CyberPenda 的发布产物（应用镜像与沙箱镜像）通过 GitHub Container Registry 分发，默认标签为 `ghcr.io/n1majne3/cyberpenda:latest` 和 `ghcr.io/n1majne3/cyberpenda-sandbox:latest`。Daemon 启动时以常量形式硬编码该默认值，可通过 `-sandbox-image` / `PENTEST_SANDBOX_IMAGE` 覆盖；Compose、CI 工作流与 smoke 脚本均引用同一地址。

- 角色：运行时容器镜像的官方分发点（app + sandbox）
- 集成点：`cmd/pentestd/main.go` 默认常量、`Makefile` 变量、`docker-compose.yaml`、`.github/workflows/*` 发布流程
- 稳定用法：镜像名可被环境变量覆盖，但仓库域名固定为 GHCR；构建流水线将产物推送到 `ghcr.io/${image_name}`