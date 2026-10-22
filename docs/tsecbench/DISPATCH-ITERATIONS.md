# Hosted 薄调度迭代记录(p1–p20)

Status: 持续维护的演进日志。构建、上传和实测分别记录；未实测版本不推断得分。
数据细节见本地 `/.tsecbench-analysis/RUN-ANALYSIS.md`(不入库)。
设计稿: [hosted-dispatch-time-injection.md](specs/hosted-dispatch-time-injection.md)、
[provider-resilience.md](specs/provider-resilience.md)。
Domain terms: [CONTEXT.md](../CONTEXT.md)。

基线: 旧编排器会话架构 step-5 最优 16,100(run 20699,135M token);
CyberPenda 历史最高 **21,800(94.95%,deepseek-flash,run 22289,即本系列 p6)**,
已公开(发布时官方榜第 14,最新榜单快照第 18)。其前一档为旧架构 21,670(93.82%,run 16290)。

| 版本 | 主题 | 关键改动 | 实测 run | 结果与教训 |
| --- | --- | --- | --- | --- |
| p1 | 薄调度原型 | ledger.json 唯一事实源;dispatch.py 组装器(init/assemble/validate/mark/harvest) | 21034 | 用了旧 skill 拷贝(internal/skill/builtins 而非 hostedcontroller assets)。**教训: hosted 真正运行的是 http_app.go 嵌入并 PUT 的那份拷贝;两份必须同步,pin 测试锁双份** (fae187f, 28ff1ce) |
| p2 | 派发时注入 | 退场报告 attempts/<code>/<k>.md 承重墙;家族 facts 注入;escalations;outbox/READY.tsv(注: 本版无独立提交,上述改动均随 p1 的 0c5684d 落地;READY.tsv 无代码写入,就绪真源 = ledger state=ready → stdout READY 行) | 21119 | 7,100 分但 481M token —— 编排器 sleep 轮询 1,142 次调用,每次带 350K 上下文。**教训: 对话内轮询 = token 炸弹** |
| p3 | 事件驱动等待 | Decide 阻塞等待 `timeout 240 bash -c 'until [ -s READY.tsv ]...'` 替代轮询(READY.tsv 臂无写入方,实际靠 escalations 臂或 240s 超时返回) | 21416 | 13,650/91M,但运行时挂起 2.5h(最后动作就是那次阻塞等待)。**教训: 单靠事件等待,运行时挂死无人察觉** |
| p4 | 事件+心跳双驱动 | dispatcher 侧 CRITICAL 看门狗(HEARTBEAT_STALE_SEC=600);started-marker 应答看门狗(SPAWN_ACK_SEC=150,系 p1 fae187f 引入,p4 未改动) | 21917 | 16,150/215M,追平旧架构最优。教训: subagent 正常会主动回调,心跳只是兜底,不必高频 |
| p5 | 成本杠杆 | 指针式注入(160 字摘要+细节读文件指针;INJECT_CAP_CHARS 弃用,常量仍为 8000 且此后无引用);SESSION_CHURN_SOFT_CAP=4;文件引用派发(prompt 落盘,Agent 调用只带路径) | 22095(step-5) / 22269(deepseek) | 22095: **17,150 step-5 新纪录**/173M。22269: 0 分 —— submit 模板是占位符,deepseek 严格审计命令不存在而停摆。**教训: 严格模型是模板审计者,模板必须是真实可执行命令** |
| p6 | submit 修复 | 真实提交命令 `printf '%s' 'flag{...}' \| pentest-tsecbench-client submit {code}` | 22289(deepseek-flash) | **21,800(94.95) 历史新高**,超自旧架构 21,670;holdout a-13/f2-05/c-06 经接力结构破;已公开(发布时榜 14,最新快照 18) |
| p7 | 原子取件与复活 | `dispatch.py take`(原子 pop+mark);easy-blocked 便宜复活(c-03 四代没轮到的教训);单一等待原语 | 22762(step-5) | 42min 冲 4,600 后**冻结 53min → timeout**。诊断: step-5 网关高峰挂起,全部 23 会话齐停、计费计数停在 560。p7 逻辑本身健康(a 族接力成型)。**教训: provider 侧失败是独立故障类,客户端必须能对抗** |
| p8 | provider 韧性 A 层 | 投影 `httpIdleTimeoutMs=120s` + `retry.provider{maxRetries:3,maxRetryDelayMs:30s}` 进 pi settings.json(pi 无 env 映射,settings.json 是唯一通道;0=禁用) | 23068(step-5) | **15,050/48 题,6h 零冻结**(对照 22762 同环境冻死 4,600,+227%)。AC4 结论性成立。附带: HEAD linux 构建断点修复(b91f7eb) |
| p9 | schema + 提速 | init fallback 链吃平台原生 schema(unique_code/description/total_score),Decide 不再运行时手写归一化适配层;stdout 过滤 message/reasoning 类(且在详情拉取前过滤);hosted 免 state-file 提醒句 | 23386(step-5) | 14,450/46 题,完整 6h 零冻结。**vs 23068: token -36%,调用 -26%,分/M token 91.2→136.7(+50%)**。终局前 2.8h 攻坚零得分但会话活动连续 —— 攻坚未出分,非冻结 |
| p10 | 探针棒 + 闲置槽回收 | 每题第一棒探针预算 8 分钟(PROBE_BUDGET_MIN,有立足点/flag 才转深挖全额,空探针沉底);harvest 新增闲置槽回收(非 running 题的活实例立即放槽);探针棒记 family_facts 观测(家族门控否决,防 c-03 类饿死)。四硬约束未动(配额 3/文件派发/常驻编排器/A 层) | 23567(step-5,新 key) | **17,600/57 题 —— step-5 新纪录(超 22095 的 17,150)**。T+60min 8,300 vs p9 同期 3,300(+152%),**反超直连 harness 同期 5,300**;~85min 走完 p9 的 120min 路程。会话 102(p9 50),token 258M(效率 68.2 分/M —— 探针换手以 token 换吞吐)。首验 23554 因旧 key 耗尽 wait_expired 未起跑。同架构 deepseek 正式跑 23972:**22,290/72 flag/61 题 —— 总纪录新王**(超 22289 的 21,800 +490),已公开,**官方榜第 8**;开局 347 分/min 史上最快,b-01/b-02 双链 6/6 收网,f2-05 再破;未破 c-03/a-18 |
| p11 | 围城续作 + 零进展升档 | 有立足点题预算下限抬到 SIEGE_BUDGET_MIN=60(围城棒:从上一棒里程碑直接续作,禁重验已排除面);零进展且无立足点 → 预算 +15/棒,封顶 90(ZERO_PROGRESS_STEP/BUDGET_CEILING);终盘 exclude_fresh(最后 3600s 不发新探针,火力全给围城)。靶子: 23972 的 c-03(24 会话零出分,难度误标)/a-18(21 会话 HMAC 链被切断)烧掉 23% 调用 | 24252(step-5×XBOW 晚间) | **21,100/74.6% 活满 6h 零死亡** —— p11 无罪判定成立(午间两死为环境时段性 provider 风险)。围城在 XBOW 硬尾略亏: 无立足点题吃不到围城预算,终盘停滞早于 p10 ~-1,800。v1 无此对照,p11 调度保留进 p12 |
| p12 | 静默看门狗(B 层实装) | hosted Wait 循环盯 transcript 游标: running 且 >480s 无推进 → steer 复活(CYBERPENDA_SILENCE_REVIVE_SEC,0 关),冷却 300s,上限 5 发;复活消息带 ctf-orchestrator 身份确认流程 | 24370(step-5×XBOW) | **08:12 全会话冻结 → 5 发全打光未救活,800 分僵尸收官(验证目的达成)**。尸检: ①最可能机制 = pi 扩展 UI 对话框帧阻塞 turn 且无人应答(用户 stdout 见 pending permission 事件;对话框问句原文未取到,流中断悬置为另一候选机制,25771 后知);②看门狗机制成立 —— 第 2 发 interrupt_then_replace 真复活了编排器 11 秒(跑了诊断+心跳),但 4/5 发 in_turn_steer 对 parked turn 必败;③5 发 ~65min 烧光,provider 恢复后无人敲门;④权限 id 提取链缺裸 id 键,daemon 权限阶梯登记不上,应答管道全程通畅但没人扣得动扳机 |
| p13 | 权限门根修 + 看门狗硬化 | F1 权限事件带身份(bare id 回退 + title/message/method)+ 对话框帧原始 runtime_output 转发(仅 select/confirm/input/editor,notify 不转发);F2 hosted 自动应答器(Wait 见 pending permission 即 POST respond,默认 allow,CYBERPENDA_PERMISSION_AUTO_RESPOND=off/deny 可关,幂等);F3 复活 steer 显式 force_replace(唯一被证实有效的模式);F4 复活预算 5→60 + 复活消息强制推进(禁未派 worker 即回阻塞等待);F6 create body 删 API_TIMEOUT_MS 死键(Claude Code 遗留,pi/本仓库均不读) | 24447(step-5×v1) | **17,650/49 flag —— step-5 v1 新纪录(超 p10 的 17,600)**,6h 零停顿。曲线: 3200@T+32 → 8500@T+86 → 11250@T+122(围城收网) → 14350@T+178 → 17,650 终局。T+14(24370 权限门死亡点)顺利通过。**验证判定: 通过** —— 权限门死类未再现(对话框是否被 F2 即时应答待容器日志/artifact 确认)。F5(--approve)暂缓;pi 不锁版本(项目决策)。24370 考古: 实跑 pi 0.99.2,park turn 于 3h00m41s 后自行释放(计时已证,释放者归属未证)。早先"编排器自修 dispatch.py flock 泄漏"一说撤回 —— dispatch.py 零锁原语且其引用的报错串在本仓库不存在(10-08 复核) |

| p14 | 复活弹无上限 + 包面收紧 | SilenceReviveMax=0(冷却 300s 仍节流;配额风暴期排队弹药为已接受代价);pi-web-access 仅 hosted 投影裁剪(本地/Sandbox 保留 —— 5204319 修正 59454c4 的全局误删);锁卫生裁定无需改动(dispatch.py 零锁原语) | 25771(step-5×v1) | **14,150 后 11:45-11:48 六活跃会话流中断全灭**(worker 侧 incomplete SSE response 实证;编排器止于 tentative 空工具调用),dispatcher 空转 8min 被 churn-cap 压停,冻结 2h45m 至 timeout。看门狗是否发弹外部不可观测(日志已焚,机制判定悬置);25405/25408/25719 见 GLM 段 |
| p15 | 看门狗与排水解耦 | 看门狗独立 goroutine(原子状态);drain 内层循环 64 轮上限;loopback client 30s 超时(此前 http.DefaultClient 零超时);6b76da6 修 Wait 错误返回路径死锁(defer <-watchdogDone 挂死,失败测试钉死) | 26080(step-5×v1) | **17,850/53 flag —— step-5 v1 新纪录(超 p13 的 17,650)**,6h 满程零冻结: 37/37 拍调用严格递增(125→4379),模型工作至最后一帧。p15 判定: 无回归通过;复活路径未遇实战(无冻结可救)。运行日志全程落盘 35MB(monitor.py 新纪律,断线重连零丢失) |

## p16–p18 补记

- **p16**（`302fd13d`）：移植软顶跳过、partial 排序、配额错误分类和实例对账；增加 90 分钟攻坚棒。
- **p17**（`20bf32d0`）：增加持续无进展后的进程内 Runtime 恢复。它继承此前全部调度规则。
- **p17 / run 35075 / V3**：[平台结果](https://tsecbench.zc.tencent.com/agent/35075) 为 12,100 点、35.8 综合分、45/100 Flag、7/50 题全解。用户导出的 112 条事件有 33 次启动、18 道不同题；前 3 小时只启动 5 道不同题。V3 不开放 AgentTrace，这些事件不能证明每个代码缺陷在该次运行中的具体触发次数。

**p18：接力与平台计分修复。** 基于 dispatcher CLI、公开文件协议和模拟 Hosted Challenge Client 输出验证：

1. Fact 是中途检查点，不能替代派发要求的退场报告。Pi 与 Claude Code 使用同一份 Execute 纪律。
2. 续作优先注入本题 Fact 和最新报告，附来源指针；不再只注入其他题的记录或先截取最旧报告。中断后保留未完成里程碑，直到新报告替换它；每段都显示已确认 Flag 进度。
3. 无报告的旧题记为 blocked，不伪装成 pending 新题。持续取得新 Flag 的链题不会被三次重试或四棒软顶提前封存。
4. `foothold: ""`、null、false 按空值处理。新实例清除旧活会话假设，派发包含全部当前入口地址。
5. 每轮使用一份平台快照，识别 `unique_code`、`flag_count`、`correct_flag_count`、`is_completed`。1/6 枚 Flag 不算整题 solved；报告不累加平台分数，重复收割不重复加分。
6. 全题集共用的 `tch3` 前缀不表示技术家族。显式 `family` 优先，旧版多前缀题集保留兼容分组。
7. READY.tsv 现在由 ledger 实际投影并在 take 后清空。超时或无效报告产生 STOP.tsv；Decide 确认旧 Execute 已停止后才放槽。释放失败保留占槽记录；平台读取失败暂停平台写操作。

验证入口：`go test ./scripts -run TestDispatchCLIRegressions` 覆盖两份 dispatch.py；`dispatch.py selftest` 覆盖既有预算与排序行为；Execute 投影测试覆盖报告协议。发布使用 GitHub Actions 的 Hosted Image 构建、完整后端检查、镜像 smoke 和真实 Pi 模拟平台验收。

p18 交付时按用户要求只构建和上传，没有启动评测。之后用户提供 [run 37292](https://tsecbench.zc.tencent.com/agent/37292)：
题集为 **bctf-2026**，16,260 点、73.21 综合分、31/39 Flag、29/37 题全解。
这不是 p17 的 V3，分数不能直接比较。82 次启动覆盖 37 道题，7 道题从重试中获益。

## p19：退出确认、实例归属与 hint 接力

p18 的 CloudDB d012 在 18:04:30 写下“进行中”的合法退场报告；18:04:55 实例被关，
Execute 却运行到 18:10:06。原 IP 随后分给其他题，旧 Execute 把 SENTINEL 的响应写进 CloudDB 历史。
另一棒 d062 在没有实例时仍被手动 assemble，并把 `foothold: none` 当作有效立足点。
22:48 的 start 超时后平台实际已成功启动，说明失败响应不能代表实例不存在。
Fact 在 d065/d071 的读取证据表明接力通道确实工作；缺口在退出和实例归属。

本版改动：

1. Hosted 派发新增 `bind` 与 `finish`。主控绑定原生 Execute ID，并在 Runtime 终态后确认退出。
   报告存在不再触发收割；finish 封存报告，迟到写入不能覆盖接力结论。STOP.tsv 保留槽位直到确认退出。
2. 手动 assemble 同样要求当前可用、未完成的实例，并拒绝同题并发 Execute。
   每棒记录全部地址和本地实例代次；平台停止、消失或地址改变时要求停止旧 Execute。
3. 报告与 Fact 带 dispatch / instance；无来源、实例失效或未封存的记录不自动注入。
   本轮刚封存的报告也能在同一轮补位时注入。`none` 和中文空值不再触发围城预算。
4. start 先记录不确定状态，再请求平台；超时后 list 对账，不自动重试写操作。
   已启动的实例继续派发，pending 实例继续占槽；120 秒后仍明确 stopped 时交主控决策。
5. 上一棒无新增 Flag 的续作，或当前棒 10 分钟无 Flag 进展，生成一次 hint 审查。
   主控比较扣分与收益后选择 hint / continue / abandon。原始 hint 保存并注入后续棒，活动 Execute 由主控发信通知。
   未知请求结果不自动重购；Client 在 hint 前验证当前实例可用且题目未完成。
6. CLI 与 loop 使用同一文件锁；升级队列由 ledger 投影，关闭后不再永久唤醒主控。
   abandon 等待退出且保留封存决定；同一次 infra 死亡不会在看门狗和收割时重复计数。
7. Client 保留成功 HTTP 响应中的 `code: duplicate`，防止重复 Flag 被误读成未解出；平台计数仍是唯一计分依据。

限制：finish 依赖主控对 Runtime 终态的真实观察，不是 Runtime 自动签发的证明。
平台没有实例 ID 时，本地代次不能识别两次快照之间完全相同地址的复用；Execute 必须报告目标内容改变。
本版不会构造未经平台确认的链题依赖图，也没有新的跑分结论。按用户授权构建并上传，**不启动评测**。

验证：CLI → 模拟平台回归覆盖早写报告、迟到写入、无实例派发、实例复用、延迟启动、hint 决策与放弃后退出；
Client → HTTP 测试覆盖 hint 资格与 duplicate 字段；两份脚本须保持一致，并运行既有 selftest、后端检查和镜像验收。

## p20：Runtime 退出校验、完成唤醒与历史提取

p19 / [run 37970](https://tsecbench.zc.tencent.com/agent/37970) 为 bctf-2026、15,680 点、31/39 Flag、29/37 题。
对比 p18，同样完成数量，调用从 5,177 降至 3,409，失败提交从 20 降至 3；
但首次覆盖全部 37 题从 28m13s 延长至 73m38s。13 次 hint 合计扣 280 点。
完整主会话与 d045 记录证明：两次单 Execute 等待分别占用 31m49s 和 24m45s；
d045 仍在 running 时主控调用 finish，提前放槽。其工具批次悬挂 3h24m12s，包含无 timeout 的全盘 grep；
仅凭批次日志不能确定每条命令的耗时。前两棒报告的嵌套标题还使历史摘要提取为空。

本版改动：

1. Hosted Pi 投影独立控制扩展，bind 记录 Runtime 身份，finish 查询原生 AgentRecord 并等待执行 Promise 结束。
   stopped 只表示取消请求已接受时仍拒绝 finish；接口不可用、进程改变、终态不符都保留实例。
2. `dispatch.py stop` 使用 Pi 公共取消 RPC；`wait` 在工具内部同时检查全部 Execute 和调度事件，
   任意完成可返回，最多等 240 秒，等待时不占 ledger 锁。Pi 的 Execute 派发强制后台运行，get_subagent_result 强制非阻塞。
3. Hosted Pi 的每次 bash 调用默认和最大 timeout 均为 300 秒，保留更短的显式 timeout。
   该限制同时作用于主会话和 Execute，后台 tmux 仍由 Execute 登记与管理。
4. 历史提取保留「试过的面」内更深层标题，到同级或更高标题才结束；空节保留报告路径，避免误报首段无历史。

验证入口：CLI → 模拟平台与 Runtime socket；Pi 扩展协议；真实 Pi → 模拟模型 → 原生 Execute →
拒绝提前 finish → 取消 shell → 等待退出 → finish。GitHub Actions 的镜像验收同时执行真实取消用例。
终态强校验限 Hosted Pi；其他 Runtime 保留原生工具观察终态流程。没有调整 hint 策略，也没有 P20 跑分结论。
交付要求仍为构建并上传，**不启动评测**。

## p20 双题集复盘与 p21

p20 / [BCTF 38396](https://tsecbench.zc.tencent.com/agent/38396)：18,724/22,210 点，35/39 Flag，33/37 题，5,283 次调用。
p20 / [TSecBench 38532](https://tsecbench.zc.tencent.com/agent/38532)：**Tsecbench v1**，22,555/23,000 点，74/74 Flag，63/63 题，4,071 次调用。
两场均为 deepseek-flash；逐 run 镜像摘要、完整模型配置和随机种子未知，不能视为严格控制实验。
BCTF 同题集对比 p19：新增完成 4 题，但调用增加约 55%，且一题从已解变为未解。v1 全解不证明 V3 问题已解决。
两次单跑不足以证明没有过拟合。原始得分也不能跨题集直接排序。

本次证据覆盖两场全部平台事件、两份完整主会话和 13 份关键子会话，共 6,645 条会话记录。
另有 166 份子会话未逐条核验。BCTF d076 的报告有 front-matter 分隔符，但 foothold 中的双引号未转义；
解析失败被误报成 front_matter_missing。harvest 把派发置为 invalid，合法 Fact 随之失去自动注入资格。
v1 全题完成并收尾后仍收到两次看门狗恢复消息；主控还将原始总分 23,000 写成满分，忽略正式实得分中的 445 点差额。

**p21：报告生成、独立 Fact 封存、可信终局和计分口径。**

1. `dispatch.py report` 从派发账本填充来源，可靠转义文本，校验后一次写入最终报告。
   已有报告、已退出或实例失效的派发不能通过该命令改写。手写报告校验区分缺少分隔符和具体标量字段错误。
2. finish 同时封存合法 Fact。报告缺失或格式错误不再连带丢弃 Fact；每份 Fact 仍单独核对题目、派发和实例代次。
   下一棒读取封存副本，退出后的新增、改写不能污染接力。实例失效的来源仍全部排除。
3. Hosted Controller 在静默恢复前只读核对：平台全题完成且实例均为 stopped、主控 live/idle、
   派发已收割且已绑定 Pi Execute 留有 Runtime settled 记录。证据缺失、空平台快照、读取失败或仍在收尾时保留恢复能力。
   每次候选恢复都重新核对，不永久关闭看门狗，不启动、停止或修改平台实例。
4. `dispatch.py summary` 从当前平台快照分别给出 Flag 数、整题数和原始总分。
   当前 list 不提供可验证的实得分、hint 实际扣分或综合分，因此输出 null 并要求等待正式结果；不按题目总分猜测满分。
   hint 获取与扣分决策策略保持 p19/p20 行为。

回归边界：CLI → 模拟平台/Unix socket、Hosted Controller → daemon HTTP/平台 HTTP/dispatcher 文件协议、真实 Pi 取消与退出流程。
包含坏标量、中文/引号/换行、无报告中断、嵌套标题、独立 Fact、迟到写入、实例失效、全题完成与未完成链题、
平台读取失败、实例占用、取消未退出、主控忙以及两种完成计数。两份脚本保持相同。
本版只修复通用流程，没有题号分支或题目解法。仍按用户授权构建并上传，**不启动评测**。

限制：终局抑制依赖本版 Pi finish 写入的退出确认；其他 Runtime 或旧账本缺少该证据时保守保留看门狗。
退出确认说明原生 Execute Promise 已结束，不证明所有远端或后台副作用都已停止。
summary 不代替平台正式评分。尚无 p21 跑分结论。

## 平台侧事故记录(非我们缺陷)

- 23204/23240(p9 前两次发射): 晚高峰沙箱队列拥堵 —— 排队 75min 未分到 / 部署超时 22min 死亡。深夜队列 10min 起跑。
- 单账号同时只能有一个 run;model 评测与 agent 评测共用该槽(23253 占槽期间 p9 无法发射)。
- tsecbench JWT 实测可跨 6h+(此前「约 50 分钟失效」实为浏览器 localStorage 会话被清,非 token 短命);LevelDB 提取 token + API 直连可解除浏览器依赖。

## 结构性结论

1. 薄调度换来的不是 token 节省(fast 模型上 token 中性),是**可观测性、可移植性与 holdout 突破**(p6 三题连破)。
2. provider 韧性两层缺一不可: A 层(pi idle-timeout+重试)保 provider 路径;常驻编排器+双驱动保调度恢复路径(Execute 子代理由编排器会话 spawn,编排器休眠则无人补位)。
3. 对比直连 harness(23253, 会话重叠峰值 12, 18,800): 平台并发容器配额为 3,双方同限 —— 直连的会话重叠高不是车道多,而是**车道换手快**(短 pass、快进快出,容器占用时间短)。我们时间轴落后 16–63%(按累计分 6000–14000 里程碑到点复算;更早里程碑受起步慢热影响,偏离更大),但每会话效率更高(1.09 会话/题 vs 1.8)、token 效率持平。**差距在每 pass 占槽时长,不在架构**。

## 待办

- p14 完成: 复活弹无上限(冷却 300s 仍是节流阀;配额风暴期排队弹药是已接受代价);
  pi-web-access 仅在 hosted 投影裁剪(本地/Sandbox 保留,5204319 修正 59454c4 的全局误删);
  dispatch.py 锁卫生裁定"无需改动"(全资产零锁原语,报错串不在本仓库)。
- p15 完成: 看门狗独立 goroutine + drain 有界 64 轮 + loopback client 30s 超时
  (ff5e5f3);6b76da6 修复其错误返回路径死锁(defer <-watchdogDone 挂死)。
- 25771(p14×step-5) 14,150 后 11:45-11:48 六活跃会话流中断全灭(worker 侧
  "incomplete SSE response" 实证;编排器止于 tentative 空工具调用);dispatcher 空转
  8 分钟被 churn-cap 压停。**冻结后看门狗是否发弹外部不可观测**,两种机制未决:
  (a)控制器 Wait 循环挂死 → p15 正中;(b)pi 会话被 tentative turn 卡死,steer 建不起
  新 turn → p15 救不了。判定钥匙 = 容器 stdout(GET transcript 行与 sent 行),待取。
- p16 候选(按证据): ①看门狗 429 风暴降频 ②调用计数差分活性信号 ③攻坚预算(无立足点 hard 题)。
- 网络纪律: 外网模型端点一律走 <域名>.tsecbench.gw 网关(直连被沙箱挡,25405 秒死)。
- deepseek 冲榜: 待 25771 机制判定后发车。
