# Quiet UI prototype

一个独立、可点击的 CyberPenda 降噪原型。先确认设计方向，再把组件改动移到正式 React UI。正式页面、API 与 Runtime 均未改动。

## 打开

从本目录运行 `python3 -m http.server 4040 --bind 0.0.0.0`，然后打开浏览器访问该端口。无构建步骤、CDN、字体下载或新增依赖。数据只在内存中，刷新会重置。

需要单文件时，运行 `python3 export.py > /new/path/cyberpenda-quiet.html`，再用浏览器打开输出文件。`export.py` 把本目录的 CSS 和 JavaScript 内联到 HTML，不改源码；导出文件无需提交。输出路径应是一个不存在的新文件。

默认展示 Project Overview。右上角网格按钮或右下角“所有页面”可以打开页面地图，并切换 Pentest、CTF 和 FGS 示例。右下角“设计说明”列出逐页建议。右上角太阳按钮切换深色。

## 设计选择

- 沿用 `web/src/index.css` 的中性色、细边框与低饱和绿色方向。原型调整了色值，不是正式设计 Token 的替换。
- 首屏只保留标题、一个主要操作、活跃工作和待处理事项。数字共用一条摘要，不再各占一张卡片。
- 常用字段保持可见；工具输出、配置细节和记录历史按需展开。删除和归档放在次级菜单。
- 不把 Scope、Testing limits、Task Type、Host 激活和 Finish Readiness 当作视觉噪声移除。
- 用文字区分状态，只在运行、待输入、Severity 等需要注意的位置用色。

## 逐页审查与原型映射

以下是对当前源码结构的审查，不是对正式应用的浏览器验证。路由来自 `web/src/App.tsx`；页面及共享组件来自 `web/src/pages/` 与 `web/src/components/`。已有的高级配置折叠、Recent activity 与 Scope readiness 继续保留其意图，不把它们描述为新能力。

| 正式路由 | 源码中的信息负担 / 设计机会 | 原型入口与处理 |
| --- | --- | --- |
| `/` | Project 创建与列表共占页面 | `#projects`：列表优先，创建用 Dialog，Project Kind 必选 |
| `/sessions` | 创建表单、启动配置和 Session 列表共占页面 | `#sessions`：先看列表，New Session 进入独立创建视图 |
| `/sessions/archived` | 生命周期操作容易与常用导航竞争 | `#archived`：独立归档列表，保留空态与恢复动作 |
| `/sessions/:sessionId` | Runtime Owner 顶部有多个操作与状态入口 | `#session`：对话优先，工具分组折叠，生命周期操作进 More |
| `/sessions/:sessionId/blackboard` | FGS graph、搜索、详情与历史并列 | `#session-board`：Goal / Step / Fact 三列，历史进详情 |
| `/profiles` | 列表、Profile 编辑、实际配置与高级项同时可用 | `#profiles`：保留主从布局，常用字段在前，MCP 与 raw config 折叠 |
| `/model-providers` | Endpoint、catalog、limits 与 migration 共享配置面 | `#providers`：名称、Endpoint 和凭证引用优先，其余展开查看 |
| `/credentials` | 多组筛选、来源和 Library actions 同屏 | `#credentials`：只强调引用、来源与使用方，不展示或收集密钥 |
| `/skills` | Profile 选择、Library、编辑与批量操作同屏 | `#skills`：列表与内容优先，Profile 关系与元数据次级显示 |
| `/projects/:projectId` | 数字卡、Current work 与 Recent activity 存在重复摘要 | `#overview`：一条指标摘要，活跃 Task 与待处理事项优先 |
| `/projects/:projectId/scope` | Defaults、Expansion 表单与授权边界共用长页面 | `#scope`：授权目标、排除项和 Testing limits 保持可见；Defaults 折叠 |
| `/projects/:projectId/tasks` | Task 生命周期和 Runtime activity 可产生多重状态标识 | `#tasks`：一条主要状态和一行上下文，搜索与状态筛选 |
| `/projects/:projectId/tasks/new` | 目标、Task Type、Runtime 选择和 Summary rail 竞争 | `#launch`：目标在前；沿用高级项折叠；保留 Type 与 Host 校验 |
| `/projects/:projectId/tasks/:taskId` | Header actions、Conversation、Timeline 与滚动控件并列 | `#task`：默认读对话，工具折叠，Timeline 按需查看；Finish 检查不跳过 |
| `/projects/:projectId/tasks/:taskId/challenges` | 退休流程仍有历史信息需要阅读 | `#challenges`：明确只读，不提供 replay 或 retry |
| `/projects/:projectId/facts` | 当前源码将此路径重定向到 Blackboard knowledge | `#facts`：专注的知识列表设计草图，保留返回 Blackboard；不建议新增正式路由 |
| `/projects/:projectId/blackboard` | Current Work、Knowledge、health、revision 与 missing Evidence 共屏 | `#blackboard`：当前工作优先，health 和历史收起 |
| `/projects/:projectId/blackboard/*` | 同一 Blackboard 的子视图；FGS 有独立语义 | `#blackboard` / `#facts`：覆盖 work 与 knowledge 方向；FGS 用独立示例数据，不转换记录 |
| `/projects/:projectId/findings` | Severity、status、CVSS 标识重复占据列表 | `#findings`：Severity 只用一个标识，状态用文字，Evidence 进详情 |
| `/projects/:projectId/evidence` | Artifact 类型、状态和内容元数据同时呈现 | `#evidence`：文件、来源、大小与保留时间；明确显式 retain 语义 |
| `/projects/:projectId/report` | 多组记录与标签更像数据列表而不是交付文档 | `#report`：文档预览为主，单一导出入口，未确认项次级显示 |
| `/projects/:projectId/solution` | Verified / candidate Flags、Answers、Procedures 与 Facts 同屏 | `#solution`：verified 在前，candidate 折叠，保留与 Report 的区别 |

## 可操作范围

页面导航、搜索、Task 状态筛选、展开详情、深色、页面地图、创建示例 Task / Session、消息输入、停止 / 恢复示例状态、Scope 临时保存、Profile 列表切换、示例配置项创建、归档 / 恢复与 Markdown 下载均只服务于原型预览。

没有 API、真实模型、扫描、文件上传、凭证读写或持久化。配置的 Save 只显示演示反馈，不改变正式配置。Project 创建展示对应 Kind 的固定样例；它不是完整 CRUD。Summary 数字是设计样例，不从演示列表重新计算。

没有调用 Gemini Neon：当前会话没有该模型入口，也没有已连接的 Gemini 服务。这个文件不代表该模型的输出。

## 验证边界

原型不依赖 daemon。`verify-cyberpenda` 的 isolated daemon / Doctor 流程用于正式产品验证，不适用于这个无 API 原型；未将原型行为当作正式 Runtime 或持久化验证。
