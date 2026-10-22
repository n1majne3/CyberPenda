# Execute Agent 派发模板(模板本体由 scripts/dispatch.py 持有)

派发必须**后台异步**,禁止同步等到子线程结束。**仅 Codex:** V1 `spawn_agent`,
`fork_context: false`。
**仅 Pi 与 Claude Code:** `Agent` 派发用 `subagent_type: "execute"`
(CyberPenda 已投影同一 Execute 类型,身份与收束纪律已内置)。
**派发 prompt 省略「收束纪律」整段,只保留模板生成的变量段**——
Execute 类型自带收束纪律,转发 outbox prompt 时不要再附加纪律文本。

## 薄调度模式的派发流程

Decide **不手写 prompt**。prompt 由 `dispatch.py assemble` 生成到
`graph/outbox/<did>-<code>.prompt.md`,结构固定:

```
固定头(授权书 + 题面:目标/题目/题号/flag 数)     ← 该题内逐字节稳定
【实例归属】{dispatch ID / 实例代次，Fact 与报告必须携带}
【本段目标】{里程碑:来自上一段 next_milestone}
【已排除的面】{本题报告最新在前，保留来源指针}
【本题 Fact】{本题检查点最新在前，附文件路径}
【家族经验】{同家族已解题经验,总量硬顶,超限指针化}
【平台 hint】{已保存的提示和来源路径}
工作方式(recon → submit-flag 流程 + /opt/knowledge 检索技巧)  ← 全局固定
【退场协议】(attempts/<code>/<k>.md 必填字段 + 预算)          ← 全局固定
```

排序原则:**固定内容在前、每段变化的内容在后**,跨段前缀稳定利于缓存。
注入硬顶:本题 Fact ~6K 字符、家族经验 ~8K 字符、已排除面 ~2.4K 字符;超限写
"(全文见 graph/attempts/…)"由 Execute 自行读取。

## Decide 的派发动作（文件引用）

1. `python3 scripts/dispatch.py take --ws "$WS"`，原子取出并标记 ready 派发。
2. 使用返回的 did/code/prompt 路径记录 Execute 身份，后台异步派发。
3. prompt 只给文件路径和“第一步读取文件，再遵守完整指令”。禁止粘贴全文。
4. Agent 返回原生 ID 后执行 `bind <did> <agent-id>`。每次 Runtime 完成回调后执行
   `finish <did> --agent-id <agent-id> --status completed|failed|stopped`，再由 harvest 收割。
   STOP.tsv 出现时，先停止并确认原生 Execute 终态。报告存在本身不能触发放槽。

## 模板变量说明(供人工核对 assemble 输出)

| 变量 | 来源 |
| --- | --- |
| 目标地址 | 最新 start 返回的全部 container_addr，必要时以 list 补齐 |
| 题面/分值/flag 数 | challenges.json 与平台快照 |
| 里程碑 | 上一段退场报告 `next_milestone`;无里程碑时按平台已得 Flag 数继续未完成阶段 |
| 已排除的面 | 该题历史退场报告的「试过的面」节，最新在前 |
| 家族经验 | 同家族已解题的末段退场报告 + graph/facts 同家族条目 |
| 预算 | ledger 按难度映射(easy 15 / medium 25 / hard 35 / 链题 60 分钟) |

## 退场协议要点(模板尾部原文,Execute 必须遵守)

1. 结束前先写 Markdown 正文，再按派发 prompt 中的 `dispatch.py report` 命令生成并校验退场报告。该命令自动填来源和转义字符串。退场报告写到 `graph/attempts/<code>/<k>.md`,front-matter
   challenge / attempt / outcome(solved|stalled|budget_stop) / flags_gained /
   foothold / next_milestone 缺一不可；Hosted 还需 dispatch / instance，值由 prompt 给出;
2. 大输出落 `graph/data/<code>-<k>-*.{txt,json}`,报告只引用文件名;
3. 没有结果就如实写"已试X、观察到Y、未达成Z",禁绝对化结论;
4. 拿到 flag 立即 submit-flag,返回 JSON 原文粘进报告;
5. 进行中只写 Fact，最终报告只在结束前写一次。Fact 不能替代退场报告；只连接本题目标;写完报告立即结束,最终回复只需一句收束指针。

报告缺字段或 solved 无提交 JSON → 派发器校验失败 → 按 infra_dead 记账,
该段报告结论不被采信。合法且在退出时封存的 Fact 仍可用于接力。
