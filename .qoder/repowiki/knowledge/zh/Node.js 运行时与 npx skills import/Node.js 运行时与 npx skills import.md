---
kind: external_dependency
name: Node.js 运行时与 npx skills import
slug: nodejs-npx
category: external_dependency
category_hints:
    - client_constraint
scope:
    - '**'
source_files:
    - internal/skill/importer.go
    - README.md
    - web/package.json
---

Skill 包导入通过受控的 `npx skills import` 命令完成，传入结构化 `--package` / `--ref` / `--source-url` 参数，禁止任意 shell 注入。Node.js 同时用于 web 前端构建（Vite/Tailwind）。

- 角色：Skill 包的受控安装入口与前端构建工具链
- 集成点：`internal/skill/importer.go` 的 `NPXImporter`；web/ 下的 Vite 构建脚本
- 稳定约束：import 命令形状固定，调用方不得拼接用户输入到命令行；生产默认 `npx` 二进制名，测试可注入 stub