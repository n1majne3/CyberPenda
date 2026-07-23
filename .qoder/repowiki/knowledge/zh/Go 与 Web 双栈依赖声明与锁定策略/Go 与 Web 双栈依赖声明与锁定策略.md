---
kind: dependency_management
name: Go 与 Web 双栈依赖声明与锁定策略
category: dependency_management
scope:
    - '**'
source_files:
    - go.mod
    - go.sum
    - web/package.json
    - web/package-lock.json
    - Makefile
    - scripts/ensure-web-deps.sh
---

本仓库采用 Go 单模块 + Web 子目录的混合依赖管理模式，分别由 go.mod/go.sum 和 web/package.json/web/package-lock.json 管理。

- Go 依赖：根级 go.mod 使用 module pentest 单一模块名，Go 版本固定为 1.25.0；仅显式 require modernc.org/sqlite，其余均为 indirect 传递依赖，未使用 vendor 目录、GOPRIVATE、replace 或私有代理，完全依赖 golang.org 公共索引与 sum 数据库。
- Web 依赖：前端位于 web/ 子目录，通过 package.json 声明 React 19、Vite 8、Tailwind 等依赖，并使用 package-lock.json 锁定版本；Makefile 的 ensure-web-deps 目标在构建前运行 scripts/ensure-web-deps.sh，检测 node_modules/.bin/vite、.package-lock.json 时间戳以及 Rolldown 原生绑定是否可用，必要时执行 npm ci 修复。
- 无跨语言统一锁文件：Go 与 Node 各自维护独立 lockfile，没有统一的依赖升级脚本或跨语言一致性检查。
- 无私有注册表/代理配置：仓库中未发现 GOPROXY、GONOSUMDB、GOPRIVATE 或 npm registry 覆盖，所有第三方包均从公开源拉取。

开发者约定：
- 新增 Go 依赖后需提交更新后的 go.sum，避免引入间接依赖漂移。
- 修改 web/package.json 后应重新生成 package-lock.json，确保 CI 与本地 ensure-web-deps 行为一致。
- 不要将 node_modules 或 Go vendor 纳入版本控制（已有 .gitignore）。