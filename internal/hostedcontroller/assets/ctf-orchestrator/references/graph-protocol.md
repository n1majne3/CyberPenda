# FGS 图协议与薄调度文件(ledger / attempts / outbox / escalations)

目录布局:

```
$WS/
  deadline                 # epoch 秒;仅任务明示时限时存在
  challenges.json          # platform.sh list 原始输出
  platform.sh              # 平台适配层(list|start|close|hint|submit)
  scripts/dispatch.py      # 薄调度器(从 ctf-orchestrator skill 复制)
  ledger.tsv               # (旧接力模式遗留;薄调度模式不再维护)
  graph/
    ledger.json            # 唯一调度事实源(dispatch.py 维护,禁止手改)
    attempts/<code>/<k>.md # 退场报告:每段 Execute 的收束结论
    outbox/                # prompt + READY.tsv + STOP.tsv + ACTIVE.tsv + ENDGAME
    escalations/QUEUE.md   # 升级问题队列(Decide 消费)
    facts/                 # 本题接力检查点；不能替代退场报告
    data/                  # 大块输出,退场报告里引用文件名
```

## ledger.json

由 dispatch.py 独占写入。关键字段:

```json
{
  "challenges": {
    "a-04": {
      "code": "a-04", "name": "…", "score": 300, "difficulty": "medium",
      "flags_total": 1, "flags_correct": 1,
      "state": "solved",
      "attempt": 2, "restarts": 1, "infra_deaths": 0,
      "budget_min": 25, "spent_min": 41,
      "foothold": "", "milestone": null, "instance": null,
      "family_grew": false
    }
  },
  "dispatches": {
    "d012": {"code": "a-04", "attempt": 2, "state": "dispatched",
             "hard_stop": 1758…, "prompt": "graph/outbox/d012-a-04.prompt.md"}
  },
  "seq": 12
}
```

state 取值:`pending → running → solved | partial | blocked | exhausted`。
默认预算:easy 15 / medium 25 / hard 35 / 多 flag 链题 60 分钟(累计容器时间)。
重试护栏:非零进展的题第 3 次 stalled 后 exhausted;
同题 infra 死亡 ≥4 次升级决策。

## 退场报告 attempts/<code>/<k>.md

```markdown
---
challenge: a-04
attempt: 2
outcome: stalled
flags_gained: 0
foothold: "admin 会话 cookie 仍存活"
next_milestone: "用 admin 会话读 /admin/config 拿 flag"
---
## 试过的面(下一段不要重复)
- SQLi 于 /search,union 被 WAF 拦
## 证据指针
- graph/data/a04-2-sqli.txt
```

校验规则(dispatch.py validate,派发前自动执行):

- front-matter 必填 challenge / attempt / outcome / next_milestone,取值合法;
- `solved` 必须在正文含提交返回 JSON(`"correct": true` 或 `"code": "duplicate"`);
- 校验失败 → 该段按 **infra_dead** 记账:结论不采信、已排除面不注入、
  不占重试上限。

## fact 文件(兼容层)

```markdown
---
id: fact_007
step: attempt_a-04_2
challenge: a-04
title: 一句话可判读的结论(含关键值)
---
content:只写新增客观事实;禁"此路不通/已穷尽/勿再试"类绝对结论。
```

规则不变:编号全局单调递增、一个 id 一个文件、只追加。
本题 Fact 与最新退场报告优先注入。显式家族或可区分的旧版编号家族可共享经验；全题集共用前缀不表示家族。
注入总量硬顶(超限以文件指针引用)。


## 平台计分与停止确认

- 每轮先读一份平台快照，兼容 `unique_code` / `flag_count` / `correct_flag_count` / `is_completed`。
- 报告的 `solved` 只表达 Execute 的结论。1/6 枚 Flag 仍是 partial；全部完成以平台为准。
- Fact、报告中的 `flags_gained` 不能累加到平台计数。重复收割不能重复加分。
- 空 foothold 使用 `""`。未完成报告必须给出下一里程碑；完成报告可为空。
- 所有 Hosted 派发均需 bind 原生 Execute ID，并在 Runtime 终态后 finish；未确认时保留槽位。报告文件不能代替终态。
- 平台读取失败时不启动或释放实例。释放失败继续保留占槽记录，下一轮先对账。

## P19 实例与退出协议

- 所有 dispatcher 命令与 loop 的每轮 harvest 使用同一个 `graph/dispatch.lock` 文件锁。
- `bind <did> <agent-id>` 记录原生 Execute 身份；`finish <did> --agent-id <agent-id> --status completed|failed|stopped`
  由 Decide 在观察到 Runtime 终态后调用。这个确认依赖主控的真实观察，不是 Runtime 自动签发的证明。
- `finish --status not_started` 只用于确定未启动的派发失败；有绑定或开工标记时拒绝。
- finish 封存 `graph/attempts/<did>.sealed.md`。harvest 只收割该快照；迟到写入不会改变接力内容。
- Hosted 报告与 Fact 必须带 `dispatch: dNNN` 和 `instance: <prompt 中的实例代次>`。
  未绑定的 Fact、实例失效派发的 Fact、未封存或无效的报告不自动注入。
- 实例代次是本地标识。平台停止、题目消失或地址集合改变时，旧派发失效并请求停止。
  平台若在两次快照之间复用完全相同的地址，客户端无法仅凭地址证明身份；Execute 发现服务内容改变时必须停止并报告。
- `none`、`null`、`false`、`无`、`无立足点` 均按空 foothold 处理。
- start 超时先 list 对账，不自动重试 start。待确认请求保留槽位；可用后继续派发；
  120 秒后仍明确 stopped 的请求交主控决策。手动 assemble 同样要求当前可用且未完成的实例。

## hint 接力

- 上一棒无新增 Flag 的续作，或本棒 10 分钟无 Flag 进展，产生一次 hint 审查。
- `decide <eid> --decision hint --reason "理由"` 只对当前可用且未完成的题请求一次。
  先保存可能计费的请求记录，再调用平台；未知结果不能自动重试。
- 原始响应保存在 `graph/hints/<code>.json`，后续 prompt 注入内容和路径。
  Decide 需通知活动 Execute 读取它。continue 和 abandon 同样保存决策；abandon 仍需等待 Execute 退出。
- QUEUE.md 由 ledger 投影，仅保留 open 条目；不要手工追加或删除。
