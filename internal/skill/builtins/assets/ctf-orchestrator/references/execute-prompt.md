# Execute Agent 派发模板(模板本体由 scripts/dispatch.py 持有)

派发必须**后台异步**。**仅 Codex:** V1 `spawn_agent`,`fork_context: false`。
**仅 Pi 与 Claude Code:** `Agent` 派发用 `subagent_type: "execute"`
(CyberPenda 已投影同一 Execute 类型,身份与收束纪律已内置)。

## 薄调度模式的派发流程

Decide **不手写 prompt**。prompt 由 `dispatch.py assemble` 生成到
`graph/outbox/<did>-<code>.prompt.md`,结构固定:

```
固定头(授权书 + 题面:目标/题目/题号/flag 数)     ← 该题内逐字节稳定
【本段目标】{里程碑:来自上一段 next_milestone}
【已排除的面】{该题此前退场报告蒸馏,硬顶截断}
【家族经验】{同家族已解题经验,总量硬顶,超限指针化}
工作方式(recon → submit-flag 流程 + /opt/knowledge 检索技巧)  ← 全局固定
【退场协议】(attempts/<code>/<k>.md 必填字段 + 预算)          ← 全局固定
```

排序原则:**固定内容在前、每段变化的内容在后**,跨段前缀稳定利于缓存。
注入硬顶:家族经验 ~8K 字符、已排除面 ~2.4K 字符;超限写
"(全文见 graph/attempts/…)"由 Execute 自行读取。

## Decide 的派发动作(逐字转发)

1. read `graph/outbox/<did>-<code>.prompt.md`;
2. Agent(execute)后台派发,prompt = 文件**逐字全文**,不改写;
3. `python3 scripts/dispatch.py mark <did> dispatched --ws $WS`。

漏做第 3 步:看门狗会在 grace 期后把该段记 infra_dead 并重派,浪费一棒。

## 模板变量说明(供人工核对 assemble 输出)

| 变量 | 来源 |
| --- | --- |
| 目标/题面/分值/flag 数 | challenges.json(platform list) |
| 里程碑 | 上一段退场报告 `next_milestone`;首段默认"解出本题"/多 flag 题"取第 1 枚" |
| 已排除的面 | 该题全部历史退场报告的「试过的面」节 |
| 家族经验 | 同家族已解题的末段退场报告 + graph/facts 同家族条目 |
| 预算 | ledger 按难度映射(easy 15 / medium 25 / hard 35 / 链题 60 分钟) |

## 退场协议要点(模板尾部原文,Execute 必须遵守)

1. 结束前把退场报告写到 `graph/attempts/<code>/<k>.md`,front-matter
   challenge / attempt / outcome(solved|stalled|budget_stop) / flags_gained /
   foothold / next_milestone 缺一不可;
2. 大输出落 `graph/data/<code>-<k>-*.{txt,json}`,报告只引用文件名;
3. 没有结果就如实写"已试X、观察到Y、未达成Z",禁绝对化结论;
4. 拿到 flag 立即 submit-flag,返回 JSON 原文粘进报告;
5. 只连接本题目标;写完报告立即结束,最终回复只需一句收束指针。

报告缺字段或 solved 无提交 JSON → 派发器校验失败 → 按 infra_dead 记账,
该段结论不被采信(等于白干)。
