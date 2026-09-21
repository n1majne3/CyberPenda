# Hosted 薄调度与派发时注入

Status: 设计稿。未实现,不改变现行 Hosted Controller 边界,不改变现行
产品代码。数据依据见
[docs/tsecbench/RUN-ANALYSIS.md](../tsecbench/RUN-ANALYSIS.md)(2026-09 四次
Hosted 跑分 + 模型评测模式的量化复盘)。
Domain terms: [CONTEXT.md](../../CONTEXT.md)。

## 1. 目的与范围

慢模型(step-5-preview、glm-5.3-flash 档)在 6 小时 Hosted 预算内的得分被
三个结构性成本压平在 63–67 分:

| 成本 | 实测(20699 / 16344 / 19393) |
| --- | --- |
| 编排器会话占用 | 46–55% 总 token;挂钟 73–79% 在 sleep 轮询 |
| 接力棒结构折损 | 执行流调用率 3.0–3.6 次/分,直连模式实测 11.2 次/分 |
| 棒次 prompt 膨胀 | 每调用未缓存 input 4.8K–6.8K token(直连 0.7K) |

本设计把挑战调度从"编排器模型的对话"降级为"文件 + 模板 + 事件":
调度状态落盘,prompt 在派发时组装注入,编排器模型只在策略时刻被有界调用。
目标是慢模型配置下把执行流调用率提升到直连档,同时保留家族经验复用。

对照证据:`agent/19291`(模型评测模式,MiniMax-M3)以直连会话在 69 分钟内
16/16 解出其子集(含 agent 模式未解的 a-18),证明该结构的有效性;
其 10 会话 2,260 万 token 解一题的重试成本,同时证明需要预算护栏。

## 2. 非目标

- 不改变 Hosted Controller 的领域边界:挑战调度仍属 Runtime,
  本设计是 Runtime 侧组件的内部重构(见第 9 节)。
- 不替代模型选型:本设计回收 harness 乘数(约 3 倍调用率),
  不改变模型基础吞吐。选型验收指标仍以 RUN-ANALYSIS 行动项第 1 条为准。
- 不改动 Blackboard、FGS 语义与 Hosted FGS 文件布局的既有约定;
  本设计新增的文件是 Hosted FGS 工作区文件的扩展。
- 不追求无人值守策略的完全确定性:保留有界 LLM 策略调用作为逃生门。

## 3. 现状与目标架构

现状(接力模式):常驻编排器会话持有全部调度状态,以 sleep 轮询收割,
逐棒现写注入 prompt,棒次以固定时钟(10–20 分钟)切割。

目标架构四个组件:

| 组件 | 形态 | 职责 |
| --- | --- | --- |
| 调度状态 `graph/ledger.json` | 磁盘文件 | 每题状态、尝试计数、已耗预算、容器占用、优先级 |
| 退场报告 `graph/attempts/<题号>/<k>.md` | 磁盘文件 | 第 k 次尝试的收束结论(承重墙,见第 7 节) |
| 派发组装器 | 确定性脚本,零模型调用 | 读 ledger + attempts + 家族 facts,按模板组装 execute prompt |
| 调度循环 | Runtime 侧文件事件监听 | 退场报告落盘即触发下一派发;规则不确定时发起一次有界策略调用 |

```
            ┌── ledger.json ──┐
家族 facts ─┤                 ├─→ dispatch.py ─→ execute 会话(直连,端到端)
attempts ───┘                 │        ↑
                              │        └─ 退场报告落盘
调度循环(文件事件)───────────┘
   └─ 不确定时 → 有界策略调用(紧凑 ledger 摘要 → 单次问答)
```

## 4. 设计决策

### D1 状态在磁盘,不在对话

调度状态以 `ledger.json` 为唯一事实源。任何组件(组装器、调度循环、
策略调用、事后复盘)从文件读状态,不依赖任何会话记忆。编排器模型的
上下文不再随跑分单调增长。

### D2 prompt 由模板组装,不由模型撰写

每根 execute prompt 由组装器填充:题面、目标、注入包、已排除路径、
全局技巧、submit 命令、预算与退场条件。编排器模型不再为"写 prompt"
支付 token 与延迟。

**注入包预算硬顶:3–4K token。** 家族 fact 按相关度取 top-k;
超限内容以 `graph/data/...` 文件指针引用,由 execute 会话按需读取。
依据:19393 每调用未缓存 input 6.8K 直接压垮调用率。

### D3 会话直连,按里程碑续投

单 flag 题一个 execute 会话端到端:解出、或耗尽该题会话预算、
或自判卡死写退场报告后结束。不设固定时钟切割。

多 flag 链(b 家族)以**里程碑**续投:每枚 flag 为一段,
退场报告必须声明"距下一枚 flag 的最短路径",组装器据此开下一段。
禁止 12 分钟时钟切割,也禁止整链一口气。

### D4 卡死即重启,不续拖

会话触发压缩或超时后,不延续该会话;下一段以全新上下文开始,
历史只经退场报告与家族 fact 流入。依据:20699/19393 的压缩会话
(17/25 条)产出显著劣于新鲜会话;19291 以全新上下文重试解出 a-18。

### D5 编排器模型降为有界策略调用

仅当调度规则触发 `uncertain` 标记时发生。输入是紧凑 ledger 摘要
(非对话历史),输出是单次决策(追加/收割/放弃/换题),决策落回
ledger 后立即结束。预期每场 20–40 次,单次输入 <2K token。

触发 `uncertain` 的典型情形:
- 部分得分的题是否继续追投(foothold 存活但预算过半);
- 平台侧异常(instance 启动失败、配额与 ledger 不一致);
- 同分值下多个候选且规则平手。

### D6 重试与预算护栏

- 每题重启上限:3 次(第 4 次触发 `uncertain` 升级决策);
- 每题 token 追投上限:500 万(超限标记 `exhausted`,除非策略调用
  明确追加);
- 全场保留收尾保护:结束前 5 分钟停止新派发,只收割与提交。

依据:a-18 十会话 2,260 万 token 的盲重试;20699 末分钟派发浪费。

## 5. 数据契约

### ledger.json(示意)

```json
{
  "challenge_id": "a-18",
  "score": 500, "flags_total": 1, "flags_correct": 0,
  "state": "running",            // pending|running|blocked|partial|solved|exhausted
  "attempt": 3,                   // 当前第几段
  "budget_spent_tokens": 4120000,
  "instance": {"addr": "10.0.162.193:80", "since": "…"},
  "milestone": "next_flag_1",     // 多 flag 题的下一段目标
  "history": ["attempt/1.md", "attempt/2.md"]
}
```

### attempts/<题号>/<k>.md(退场报告,必填字段)

```markdown
---
challenge: a-18
attempt: 3
outcome: stalled              // solved|stalled|budget_stop|infra_dead
flags_gained: 0
foothold: "webshell 存活于 /tmp/sh.php,容器内"
next_milestone: "读取 DB 配置表获取 flag1"
---
## 试过的面(下一段不要重复)
- ...
## 证据指针
- graph/data/a18-3-*.txt
```

### dispatch 模板(骨架)

```
对授权靶场开展端到端渗透测试,入口目标为 {target}。授权范围……(现行授权书原文)
题目:{description}
题目编号:{challenge_id}(本题共 {flags_total} 个 flag)

【本段目标】{milestone 或 "解出本题"}

【已排除的面】(来自退场报告,勿重复)
{excluded_avens}

【家族经验】(已解同家族题的蒸馏,≤3-4K token,超限以文件指针引用)
{family_facts}

【全局技巧】离线知识库 /opt/knowledge,动手前 `pentest-knowledge-lookup <关键词>`。

工作方式:(现行 submit-flag 流程原文)

【退场协议——本段结束前必须完成】
1. 无论解出与否,写 {attempts_path}/{k}.md,必填字段缺一不可;
2. outcome 取 solved / stalled / budget_stop / infra_dead 之一;
3. 找到 flag 立即提交,回写提交返回 JSON 原文到退场报告;
4. 写完报告才算结束。不写报告的会话按 infra_dead 处理,不保留其结论。
```

## 6. 调度规则(默认策略,全部可被 D5 覆盖)

1. 配额:3 个并发靶场容器(不变)。
2. 选题优先级:未开过的新题 > 部分得分且有 foothold 的题 >
   blocked 且家族经验已增长的题 > 追投已过 2 次的题。
   (对照 20699 教训:blocked 的 easy 题全場零重试。)
3. 退场报告落盘 → 校验通过 → 立即释放配额并派发下一段(秒级,
   替代 sleep 150–180 轮询)。
4. 结束前 5 分钟:只收割、只提交、不派发。

## 7. 退场协议校验(承重墙)

组装器在下一段派发前校验上一份退场报告:

- front-matter 四个必填字段(challenge/attempt/outcome/next_milestone)齐全;
- `solved` 必须附提交返回 JSON 且 `correct:true` 或 `code:duplicate`;
- 校验失败 → 该段按 `infra_dead` 记账:不占 blocked 预算、不采信其
  "已排除的面"、不计入重启上限(沿用现行 ctf-orchestrator 对 infra
  死亡的既定规则)。

设计理由:整个知识传递链只依赖这一份文件。校验缺位时,直连重试会
退化为 19291 式失忆盲试。

## 8. 与领域模型的映射(姿势 A)

- 调度循环与组装器是 **ctf-orchestrator Skill** 的 Runtime 侧组件,
  不是 Hosted Controller 的新职责。Hosted Controller 仍只做 bootstrap
  与观察,"Challenge scheduling and execution belong to the Runtime"
  边界不变。
- execute 会话沿用 **Execute Agent Type**(Pi 投影),仅 prompt 组装
  方式与生命周期规则变化。
- `graph/ledger.json`、`graph/attempts/` 是 **Hosted FGS** 工作区文件
  的扩展,不进 CyberPenda Blackboard,不改变 Disabled Blackboard Mode
  语义。
- 有界策略调用是 **Harness Control Turn** 语义之外的 Runtime 自有
  行为:它由 Skill 脚本发起,输入输出都落盘,不占用 Work Runtime Turn
  的会话历史。

若未来选择姿势 B(调度权移交 Hosted Controller),需先修订 CONTEXT.md
的 Hosted Controller 定义,另立 ADR。本设计不做此修订。

## 9. 验收指标

以同一模型重跑对照(建议 glm-5.3-flash,已有 65.91/63.81 两个基线):

| 指标 | 基线(16344/19393) | 目标 |
| --- | --- | --- |
| 执行流调用率 | 2.3–3.6 次/分 | ≥ 8 次/分 |
| 编排侧 token 占比 | 46–55% | ≤ 10% |
| 收割→再派发延迟 | 最高 180s | ≤ 10s |
| 每调用未缓存 input | 4.8K–6.8K | ≤ 3K |
| 得分 | 63.8–65.9 | ≥ 75 |
| blocked 题零重试数 | 全部 blocked 题 | 0(规则 2 兜底) |

## 10. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| 固定规则在异常时刻犯蠢 | `uncertain` 升级通路(D5);规则显式列出异常触发条件 |
| 退场协议被 execute 忽略 | 双保险:prompt 强制 + 派发前校验(第 7 节) |
| 注入包再度膨胀 | 组装器硬编码 3–4K 上限,超限指针化(D2) |
| 重试无底洞 | D6 上限 + 收尾保护 |
| 里程碑切分把长链切碎 | 退场报告强制 next_milestone;组装器连续两段无进展即触发 `uncertain` |

## 11. 分阶段落地

- P0(纯 prompt/文件层,零代码):直连会话 + 退场报告 + 手动派发脚本。
  在一次 Hosted 跑分上验证验收指标的前两行。
- P1(调度循环):文件事件监听 + 自动派发 + ledger 自动记账 +
  退场校验。替换 sleep 轮询。
- P2(策略调用):`uncertain` 触发器 + 有界调用通路 + 收尾保护。
  完成后编排器模型调用收敛到 20–40 次/场。

P0 不改任何产品代码,可先行单独验证。
