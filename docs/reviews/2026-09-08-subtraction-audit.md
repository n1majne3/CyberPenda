# CyberPenda 项目减法审核

审核日期：2026-09-08。本文是删减建议，不是已批准的领域变更或数据迁移方案。

**结论：先结束新旧 Blackboard 长期并存，再收窄产品范围。**

建议保留的核心流程是：Project 与 Scope → Task → Runtime Harness → Goal／Step／Fact → 可检查的结果与导出。Hosted 评测继续作为独立交付。先减少旧协议、重复选择和附加管理功能，避免先删恢复、隔离、证据文件和测试能力。

本次检查了领域定义、产品文档、前端路由、主要服务、配置投影、存储迁移、CLI、Hosted 入口、镜像和 CI。结论来自静态调用关系和代码，不来自用户使用频率。没有启动真实 Runtime、调用 Challenge Platform 或读取用户数据库内容。

**规模依据**

统计仅包含 Git 跟踪的源码，排除常见测试文件名，包含注释与空行。行数用于衡量维护范围，不等于可删除行数。

| 范围 | 源码行数 | 含义 |
| --- | ---: | --- |
| Blackboard v2、旧迁移、Working Graph、旧契约及校验 | 21,288 | 最大的退役候选范围；仍包含历史读取和完整性逻辑 |
| FGS 核心服务 | 1,446 | 不含 daemon、runner、UI 和迁移集成，不能直接与旧系统比较效率 |
| daemon | 16,258 | 多功能交汇处，单纯拆文件不能减少行为组合 |
| Runtime 与 Runner | 14,364 | 多 Runtime、两类执行边界和恢复行为的维护成本 |
| Runtime Profile 页面 | 2,036 | 高级配置管理已经很重 |
| 五份直接使用退役模式字段的实验脚本 | 2,368 | 明确的过期入口 |

**1. 优先退役旧 Blackboard 的运行逻辑；历史读取单独保留。**

依据：

- [迁移 77](/Users/benjamin/tools/CyberPenda/internal/store/fgs_migration.go:67)把已有 Project 和 Session 的 Runtime 协议改为 FGS，没有转换或删除旧记录。
- [新 Project 创建](/Users/benjamin/tools/CyberPenda/internal/project/project.go:126)固定采用 FGS。
- [HTTP 写入保护](/Users/benjamin/tools/CyberPenda/internal/daemon/fgs.go:32)拒绝 FGS Project 的旧图写入。
- [Working Graph settlement](/Users/benjamin/tools/CyberPenda/internal/daemon/working_graph_settlement.go:15)仍同时维护 FGS Drain 和旧 Intent 编译路径。
- [Finish Readiness](/Users/benjamin/tools/CyberPenda/internal/finishreadiness/service.go:49)仍读取旧结论、Attempt、Finish Intent 和 Challenge 记录。

建议删除目标：旧 Working Graph 编译器、旧模式专用投影、只服务旧写入的结论流程和旧写入入口。先查清 Challenge Workflow 与历史任务恢复的依赖，再逐层退役。保留历史记录读取、证据文件访问、必要的 Trusted Origin 和升级能力。

这不是“把 internal/blackboardv2 整个目录删除”。新旧系统仍共用鉴权和完整性行为，且旧记录仍是用户数据。需要先形成明确的历史读取边界，再删除执行分支。

收益：减少整个项目中反复出现的协议判断、恢复分支和测试组合。优先级高，实施风险高。

**2. 把三个 Blackboard 模式收敛为“FGS 开启／关闭”。**

[启动界面](/Users/benjamin/tools/CyberPenda/web/src/components/RuntimeLaunchControls.tsx:354)仍展示 Interactive、Working Graph、Disabled，并为前两者给出不同承诺。但 [Runner 投影](/Users/benjamin/tools/CyberPenda/internal/runner/projection.go:174)对 FGS 的非禁用模式统一使用 FGS instructions；[接收器](/Users/benjamin/tools/CyberPenda/internal/daemon/fgs.go:134)也按 FGS 与非禁用状态接收更新。

建议先减少新建界面的选项，再让旧输入在兼容边界映射到明确的 FGS 开启状态。保留旧值用于解释历史，避免直接改写历史快照。还要检查 Skill 兼容规则，不能只把两个按钮改成一个按钮。

收益：减少一个已不符合主流程的产品区分。优先级高，风险中等。此项改变已定义的模式契约，本文不把建议当作用户确认。

**3. 将 Challenge Workflow 列为整块退役候选。**

它不是死代码。[daemon 入口](/Users/benjamin/tools/CyberPenda/cmd/pentestd/main.go:52)能加载平台配置，[服务](/Users/benjamin/tools/CyberPenda/internal/challengeworkflow/service.go:322)维护 claim、submit、abandon、finalize、重放与恢复，[记录器](/Users/benjamin/tools/CyberPenda/internal/challengeworkflow/blackboard_recorder.go:29)仍写旧 Objective、Attempt、Solution 和 Evidence。

与此同时，[Hosted Controller](/Users/benjamin/tools/CyberPenda/internal/hostedcontroller/controller.go:29)使用独立 Hosted Challenge Client，调度归 Runtime。两条路径有不同职责，不能把一个直接替换成另一个。

建议：如果没有持续使用“由 daemon 管理挑战操作、策略和恢复”的需求，退役普通产品里的 Challenge Workflow 页面、写接口与专用执行服务，保留 Hosted Client 路径及历史操作读取。这也会减少旧 Blackboard 退役的阻力。

立即可简化的入口：[Task 页面](/Users/benjamin/tools/CyberPenda/web/src/pages/TaskDetailPage.tsx:844)只检查是否为 Task 就显示 Challenges，没有按 CTF 类型和已配置平台限制。至少应只在可用时显示。

完整退役会失去 daemon 侧的操作幂等、策略检查和恢复，必须先明确这些能力是否还需要。优先级高，取舍置信度中等。

**4. 退役五份旧模式实验脚本，保留当前验收路径。**

以下脚本直接发送 `blackboard_conclusion_mode`：

- [compare-challenges-assisted-vs-interactive.py](/Users/benjamin/tools/CyberPenda/scripts/compare-challenges-assisted-vs-interactive.py:180)
- [compare-cybergym-assisted-vs-interactive.py](/Users/benjamin/tools/CyberPenda/scripts/compare-cybergym-assisted-vs-interactive.py:360)
- [compare-scoreboard-assisted-vs-interactive.py](/Users/benjamin/tools/CyberPenda/scripts/compare-scoreboard-assisted-vs-interactive.py:174)
- [validate-juice-shop-assisted-live.py](/Users/benjamin/tools/CyberPenda/scripts/validate-juice-shop-assisted-live.py:48)
- [validate-juice-shop-assisted-multiturn-live.py](/Users/benjamin/tools/CyberPenda/scripts/validate-juice-shop-assisted-multiturn-live.py:429)

[当前 API](/Users/benjamin/tools/CyberPenda/internal/daemon/blackboard_mode_contract.go:8)明确拒绝这个字段。它们研究的 Assisted 行为也已经退役，因此不建议仅改字段名继续保留。

建议将研究结果保留为历史记录，退出这些脚本的受支持执行范围；获准后删除脚本及仅验证旧行为的测试。保留当前 FGS sandbox smoke、真实 Runtime smoke、Hosted 合同与交付验证。优先级高，置信度高。

**5. 删除外部插件目录浏览，保留显式导入和投影。**

[目录服务](/Users/benjamin/tools/CyberPenda/internal/runtimeextension/catalog.go:37)抓取 Pi 页面和 Claude 插件目录，其中 Pi 依赖 HTML 属性和正则解析。[Runtime Profile 页面](/Users/benjamin/tools/CyberPenda/web/src/pages/RuntimeProfilesPage.tsx:244)加载目录并维护候选、筛选和已选包的展示。

这个功能主要帮助发现第三方插件，与 Project、Task、FGS 的核心流程关系较弱，却引入外部页面变化、网络失败和缓存行为。

建议保留本地库、包引用或来源导入、启用与投影；移除远程目录抓取和内嵌浏览，发现入口可指向上游。不要连带删除 Skills 或外部 MCP 配置。优先级中等，置信度中高。

**6. 让旧迁移工具退出日常配置界面。**

[Runtime Profile 页面](/Users/benjamin/tools/CyberPenda/web/src/pages/RuntimeProfilesPage.tsx:607)仍嵌有旧 Model Provider 迁移面板；后端同时保留迁移服务。旧 Profile 模型字段迁移、配置导入、原始配置余项不是同一件事。

建议把一次性旧模型字段迁移放到明确的升级工具中。普通页面保留直接 Launch Selection 与少量高级 Profile 配置。

[Profile Config Import](/Users/benjamin/tools/CyberPenda/internal/runtimeprofile/profile_config_import.go)本身超过千行，但它有原生配置导入价值。先降级为高级工具，不因体积大就删除；也不能删除 Custom Config File 后丢失用户已有设置。优先级中等。

**7. Session 是最大的可选产品分支之一，应按产品目标决定。**

[非 Project 路由](/Users/benjamin/tools/CyberPenda/web/src/App.tsx:229)、[Session 服务](/Users/benjamin/tools/CyberPenda/internal/session/session.go)及其 HTTP 层维护独立生命周期、导航、归档和 Blackboard 身份。主要 Session 文件约 5,681 行，不含共享内核及其他集成。

如果产品只服务有 Scope 的安全测试 Project，建议停止扩展 Non-Project Mode，并评估取消新建入口、保留历史读取与导出。

如果用户确实需要临时工作和通用 Runtime 会话，应保留 Session。Task 与 Session 已共用 Runtime Owner Workspace，不能把它们简单称为两套重复聊天 UI。本文没有使用频率证据，不建议立即删除。优先级低于旧协议退役。

**8. 减少默认打包的 Runtime，先从 Hosted Hermes 评估。**

[Hosted 镜像](/Users/benjamin/tools/CyberPenda/docker/tsecbench-hosted/Dockerfile:103)安装并验证 Hermes 及其 Python 环境，而 [Hosted Controller](/Users/benjamin/tools/CyberPenda/internal/hostedcontroller/controller.go:64)提供的 Runtime 选择是 Codex、Claude Code、Pi。

建议先确认评测中的 Runtime 是否会把 Hermes 当作辅助工具。如果没有这项用途，移除 Hosted 镜像中的 Hermes 是比删除全项目 Hermes 适配器更小的改动。镜像大小收益需要构建后测量。

普通产品可将低使用率 Runtime 标为可选支持，但不能凭代码量选定删除 Pi 或 Hermes。Pi 还是当前 Hosted Acceptance Configuration 的一部分。Windows、Podman 和 Host Runner 也不应在没有使用需求依据时直接删除。

此项会修改现有 Hosted Tool Baseline 的明确约定，只列为待决策候选。

**9. 压缩默认文档，历史决策按需读取。**

CONTEXT.md 有 1,725 行、186,319 字节，包含术语、运行约束、Hosted 参数和大量重复的已解决问题。文中还同时保留较早的 Interactive 默认描述与后来的 Working Graph／Disabled 默认描述。

[README](/Users/benjamin/tools/CyberPenda/README.md)仍介绍内置 `/mcp` 与七个语义工具，但 [测试](/Users/benjamin/tools/CyberPenda/internal/daemon/mcp_test.go:27)已经要求该端点返回 404。[文档索引](/Users/benjamin/tools/CyberPenda/docs/README.md)仍把旧 Blackboard v2 文档列为唯一规范。

建议让 CONTEXT.md 只承载当前核心术语和有效不变量，产品入口文档只描述当前流程；将被取代的细节留在带状态的 ADR 和历史文档。保留单一领域上下文，不引入多个互相竞争的 CONTEXT.md。

这能减少每次 Agent 工作的输入负担，也能防止开发者重新实现已退役功能。本文没有更改 CONTEXT.md，因为本次审核没有新增用户确认的领域决策。

**10. 从源码扫描范围中移出运行数据。**

[默认 Runtime Root](/Users/benjamin/tools/CyberPenda/internal/daemon/server.go:472)位于数据库旁的 `runs`，默认开发配置因此容易将任务文件留在源码仓库。`.gitignore` 排除它们，但 Go 的包扫描仍会进入这些目录。

本次运行只读的 `go mod tidy -diff` 时，实际扫描到了 `runs/.../workdir/src-vul` 下的 Pigweed 和 libheif 源码，并尝试解析它们的依赖。检查还因默认缓存不可写和离线缺少依赖而失败，因此没有得到可采信的完整依赖差异。

建议把新任务运行数据默认放到源码目录之外，测试必须使用隔离临时目录。已有运行目录应先清点并提供显式迁移，不能按名称或忽略状态删除。

`go.mod` 中的 MCP SDK 没有发现 Go 源码导入，是依赖清理候选；必须在干净的源码副本中完成 tidy 和构建后才能确认连带依赖的删除范围。两套 JSON Schema 库目前都有实际调用，不应当作未使用依赖直接删。

**不建议优先削减的部分**

- Scope、Scope Snapshot、Host Runner Activation、Credential 隔离和 Trusted Origin。
- Runtime Owner 的持久会话、Stop／Resume、Accepted Steering 的持久结算。
- Transcript 的有界历史、Subagent Conversation Block 与历史详情读取。
- FGS 的 Outbox、Receipt、重放处理、修复和语义历史。
- 原始证据文件、历史记录与仍有引用的数据迁移代码。
- Fake Runtime、关键并发测试、恢复测试和真实集成 smoke。
- Markdown 结果导出。当前 FGS 导出并不等价于旧版带 CVSS 的 Pentest Report；退役旧 Report 前需要保留历史报告能力，并明确新交付要求。

**建议实施顺序与验收**

| 批次 | 删减内容 | 必须证明 |
| --- | --- | --- |
| 第一批 | 旧实验入口、过期文档、外部插件目录浏览、无效 Challenges 入口 | 当前 FGS、Runtime smoke 和显式扩展导入仍可用 |
| 第二批 | 新建模式收敛、旧 Working Graph 编译和执行分支退役 | 新建、继续、停止、恢复都使用 FGS；Disabled 无 Blackboard 授权；历史数据可读 |
| 第三批 | Challenge Workflow 退役、旧语义写入服务缩减 | 现有平台操作有明确结算办法；历史证据和任务来源可读取；Hosted Client 路径不受影响 |
| 第四批 | Session 范围、默认 Runtime 组合、高级配置工具 | 先依据实际用途作产品决定，再执行对应迁移和验收 |

行为变更应先增加有意义的失败测试；低风险文档与入口删减不需要复制实现的测试。修改 Runtime 生命周期测试前遵循项目异步测试约定。不要为了让退役后的构建变绿而直接批量删除仍保护有效行为的测试。

本次只新增审核报告，没有删除、覆盖或改写产品代码、配置、数据库和用户运行文件。没有运行全量测试，也没有宣称这些候选已经完成安全删除验证。
