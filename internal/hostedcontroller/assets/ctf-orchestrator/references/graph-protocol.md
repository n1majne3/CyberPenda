# FGS 图协议（Fact-Goal-Step）

目录布局：

```text
$WS/
  ledger.tsv              # code<TAB>agent_id<TAB>budget_min<TAB>hard_stop_epoch
  queue.tsv               # 预排序题目队列（code, score, difficulty, flags, note）
  challenges.json         # 最近一次 Hosted Challenge Client list 原始 JSON
  deadline                # 仅在任务说明给出总时限时存在
  state.md
  graph/
    facts/                # 每条事实一个文件，只追加不修改
      001-端口面.md
    data/                 # 大块输出（扫描/源码/dump），fact 里引用文件名
    steps.yaml            # 步骤池（唯一可变文件，Decide 维护）
    goals.yaml            # 链题子目标链（可选，链题必用）
    tmux-registry.md      # 活动中的 tmux 会话清单
```

`ledger.tsv` 只管理 Execute agent 生命周期。不得写入 `elapsed_min`、`budget_min`、
`over_budget` 或 `attempt_n`；这些 challenge pass 字段只来自 Hosted Challenge Client list
的 Challenge Pass Clock 投影。

## fact 文件格式

```markdown
---
id: fact_007
step: step_012
challenge: a-04
title: 一句话可判读的结论（含关键值）
---
content：只写本 step 新增的客观事实（做了什么/观察到什么/依据）。
新凭证/新端点单独成行并加粗。大输出引用 graph/data/stepXXX-xxx.txt。
没有结果时如实写：已试 X，观察到 Y，未达成 Z。
禁止写“此路不通/已穷尽/勿再试”等否定或绝对结论。
```

规则：

- 编号全局单调递增，一个 step 至少产一条 fact（成败都要）。
- **fact id 全局唯一，一个 id 恰好一个文件**：agent 产 fact_022，Decide 的成果摘要不得复用 022——
  要么只更新 steps.yaml（to: 挂 agent 的 fact id + 摘要写 action 注记），要么另起新编号新文件。
- title 里放可检索的关键信息（URL、凭证名、漏洞编号），Decide 靠 title 扫图。

## steps.yaml 格式

```yaml
- id: step_012
  challenge: a-04            # 所属题（跨题横向可为 null）
  action: 对上传接口做扩展名黑名单绕过，与 step_011 的 JS 泄露面不重叠
  from: [fact_005]           # 依赖的 facts
  priority: high             # high / normal / low
  budget_min: 12
  state: open                # open / dispatched / done / blocked
  to: null                   # 完成后挂产出的 fact id
```

派生规则：

- 每条含新凭证/新端点的 fact 至少派生一个后续 step。
- `blocked` 的 step 释放资源重试一次后仍恒定失败才允许沉底，且不删记录。
- 同一 challenge 的 `dispatched` step 数 ≤3（攻击面互斥）。

## goals.yaml（链题）

```yaml
- challenge: b-02
  goal: 全链路渗透达成最终目标
  subgoals:
    - id: g1
      text: 外网立足点
      state: done            # open / active / done
      evidence: fact_003
    - id: g2
      text: 内网横向到目标主机
      state: active
      from: [g1]
```

## tmux-registry.md 格式

```text
| 会话名 | 目标 | 启动时间 | 命令摘要 | 登记step |
| stepXXX-sshbrute | 10.0.0.2:22 admin | 03:41 | hydra -L u -P p rockyou | step_029 |
```

收割 agent 的动作：`tmux capture-pane -p -t <会话> | tail -50`，命中即提交/记录并写 fact，
未命中写“仍在跑，已尝试 N 条”。
