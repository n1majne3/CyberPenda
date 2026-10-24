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

state 取值:`pending → running → solved | partial | blocked | exhausted | deferred`。
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

优先使用 `dispatch.py report <did> --ws "$WS" --outcome stalled --foothold '' --next-milestone '下一步' --body-file <正文文件>`。
命令从 ledger 填充 challenge / attempt / dispatch / instance，并按 JSON 字符串规则转义所有标量。
先写 Markdown 正文，再生成一次最终报告。已有报告或已退出派发不能被该命令覆盖。
手写报告须在结束前运行 validate；`front_matter_missing` 表示缺少分隔符，
`front_matter_invalid:<字段>` 表示该字段无法解析。修复错误后重新校验，直到返回 ok。

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
- summary 使用当前平台快照，分别给出 Flag 数、整题数和原始总分。list 未提供实得分，
  所以实得分、hint 实际扣分和综合分均为 null，等待平台正式结果。不能用全解题的 total_score 推导满分。
- 空 foothold 使用 `""`。未完成报告必须给出下一里程碑；完成报告可为空。
- 所有 Hosted 派发均需 bind 原生 Execute ID，并在 Runtime 终态后 finish；未确认时保留槽位。报告文件不能代替终态。
- 平台读取失败时不启动或释放实例。释放失败继续保留占槽记录，下一轮先对账。

## 实例与退出协议

- 所有 dispatcher 命令与 loop 的每轮 harvest 使用同一个 `graph/dispatch.lock` 文件锁。
- `bind <did> <agent-id>` 记录原生 Execute 身份；`finish <did> --agent-id <agent-id> --status completed|failed|stopped`
  由 Decide 在观察到 Runtime 终态后调用。这个确认依赖主控的真实观察，不是 Runtime 自动签发的证明。
- `finish --status not_started` 只用于确定未启动的派发失败；有绑定或开工标记时拒绝。
- finish 封存 `graph/attempts/<did>.sealed.md` 和 `graph/facts/sealed/<did>/` 下的合法 Fact。
  harvest 只收割报告快照；结束棒的 Fact 只读封存副本。迟到新增或改写不会改变接力内容。
  报告格式失败或缺失不代表 Fact 来源失效；每个 Fact 独立核对 challenge / dispatch / instance，实例失效仍拒绝。
- Hosted Pi 的 bind 同时记录 Runtime 进程身份。finish 必须从该进程的控制接口确认原生执行已结束，模型传入的 status 只用于核对。
- Pi 的 stop 发出原生取消请求；取消已接受不等于工具已经退出。wait 返回 settled 后才能 finish。接口不可用或进程已更换时保留实例。
- wait 同时检查调度事件和全部已绑定 Execute 的完成状态。它最多等待 240 秒，等待期间不占 ledger 锁。
- Hosted 报告与 Fact 必须带 `dispatch: dNNN` 和 `instance: <prompt 中的实例代次>`。
  未绑定的 Fact、实例失效派发的 Fact、未封存或无效的报告不自动注入。
- 实例代次是本地标识。平台停止、题目消失或地址集合改变时，旧派发失效并请求停止。
  平台若在两次快照之间复用完全相同的地址，客户端无法仅凭地址证明身份；Execute 发现服务内容改变时必须停止并报告。
- `none`、`null`、`false`、`无`、`无立足点` 均按空 foothold 处理。
- start 超时先 list 对账，不自动重试 start。待确认请求保留槽位；可用后继续派发；
  120 秒后仍明确 stopped 的请求交主控决策。手动 assemble 同样要求当前可用且未完成的实例。

## 同实例接力与续投审查

- 上一棒正常完成且已确认退出、报告有效、实例代次与地址仍一致，有立足点或新增 Flag 时，
  harvest 按配额与正常选题排序决定是否保留实例接力。新题优先；无资格时释放闲置实例。
- 到重试或会话软顶的未完成题生成 `kind: retry` 审查，每个最新派发只生成一次。
  `decide <eid> --decision continue --reason "理由" --plan-file graph/retry-plan.json`
  或 `resume <code> --reason "理由" --plan-file graph/retry-plan.json` 排队一棒。
  `resume_request` 保存理由、计划和证据摘要；实际 assemble 才消费。下一棒 prompt 完整携带计划，
  派发记录保留 `resume_reason` / `resume_plan`。排队中重复请求保留第一份计划。
  历史 attempt/restarts 保持单调记录，主控不手改计数。平台完成、题目消失、暂缓或进入收尾时审查过期。
- resume 先核对平台与旧 Execute 的退出状态，只排队，不直接 start；后续仍受配额和全局时限约束。
- `CYBERPENDA_EVALUATION_DEADLINE_UNIX` 与工作目录 `deadline` 同时存在时取较早者。
  每棒预算裁切到结束前 5 分钟，prompt 显示绝对时限；未配置时明确未知。


## 续投计划与暂缓

续投先读最新封存报告及引用的原始证据，再写计划 JSON。`after_dispatch` 填该题最新派发 ID，
没有历史派发时用空字符串；其他题的新派发不改变它。

```json
{
  "after_dispatch": "d012",
  "hypothesis": "历史阴性观测可能缺少请求前提",
  "test": "先验证有效基线，再比较仅一个条件不同的请求",
  "stop_when": "有效对照下仍无可区分信号时收束；测量无效时只修测量",
  "evidence": ["graph/data/response-pair.json"]
}
```

- evidence 是 1–5 个工作目录内的非空文件，单文件最多 8 MiB。假设、测试、停止判据各最多 2000 字符。
- 命令核对最新派发、文件存在性和 SHA256；这是格式与来源检查，不证明假设正确或证据充分。
- 同样的假设、测试、停止判据与证据内容已经正常完成且没有新增 Flag 时，拒绝再投。改理由或改文件名不能解锁；
  投递失败、异常或取消退出仍可按原计划恢复；新观测或新的可证伪测试经 Decide 审查后可再投。避免把同一失败方向只换一种说法。
- Execute 先读证据并核对摘要，若内容已变则通知 Decide 重新审查。
- 没有新证据或可区分测试时，用 `defer <code> --reason "当前限制" --reconsider-when "可观察的变化"`。
  审查队列也支持 `decide <eid> --decision defer` 加同样两参数。
  defer 只接受没有活动/待派 Execute 和待确认 start 的题；它不取消 Execute。
  闲置实例由 harvest 按正常放槽规则释放，保持 dispatcher 运行。
- 暂缓状态持久保存在该题 `deferred` 中。重启调度器、普通 harvest、相同平台快照不会自动续投。
  新证据满足复审条件后用 resume 排队；时间流逝本身不是新证据。
- summary 的 `scheduling_state` 区分 active / runnable / review_required / deferred / complete / closing，
  `deferred` 返回每题的理由及复审条件，`deadline_unix` 未知时为 null。
  只有全题完成或进入时限收尾，且无活动/待派 Execute、无待确认 start、平台实例全部 stopped，`may_finish` 才为 true。
  `deferred` 与空队列都不是全场完成。保持等待循环、心跳和平台对账；watchdog 恢复时也沿用此规则。

## 负面 Fact 与实例检查点

历史报告以“历史观测与待复核结论”注入，不能因进入历史区就获得确定性。
报告与本题 Fact 按实例代次标注：同实例做最小活性检查后复用；不同实例重新确认会话与落地物；
代次缺失则关系未知。相同 IP 不等于相同实例。
准备排除假设、记录阴性结果或归因平台故障时，按派发 prompt 的“负面结论的证据条件”执行，
分列观测、前提、正负对照、判据、适用范围和证据文件。测量返回错误服务时先修测量。
纠正以新 Fact 引用旧记录和原始响应。模板只约束流程，不自动证明模型遵守或结论正确。

## hint 接力

- 上一棒无新增 Flag 的续作，或本棒 10 分钟无 Flag 进展，产生一次 hint 审查。
- `decide <eid> --decision hint --reason "理由"` 只对当前可用且未完成的题请求一次。
  先保存可能计费的请求记录，再调用平台；未知结果不能自动重试。
- 原始响应保存在 `graph/hints/<code>.json`，后续 prompt 注入内容和路径。
  Decide 需通知活动 Execute 读取它。continue 和 abandon 同样保存决策；abandon 仍需等待 Execute 退出。
- QUEUE.md 由 ledger 投影，仅保留 open 条目；不要手工追加或删除。
