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
    outbox/                # <did>-<code>.prompt.md + ENDGAME 标记
    escalations/QUEUE.md   # 升级问题队列(Decide 消费)
    facts/                 # (兼容)fact 文件,只追加;并入家族经验注入
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
outcome: stalled        # solved | stalled | budget_stop
flags_gained: 0
foothold: "admin 会话 cookie 仍存活"   # 无则空串
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
step: attempt_a-04_2        # 薄调度模式下用 attempt 标识
challenge: a-04
title: 一句话可判读的结论(含关键值)
---
content:只写新增客观事实;禁"此路不通/已穷尽/勿再试"类绝对结论。
```

规则不变:编号全局单调递增、一个 id 一个文件、只追加。
同家族(题号前缀)已解题的退场报告与 fact 会被派发器注入到新段 prompt,
注入总量硬顶(超限以文件指针引用)。
