# Hosted 薄调度迭代记录(p1–p9)

Status: 持续维护的演进日志。每版 = 一次 bundle 发布 + 至少一次 TSecBench 实测。
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

## 平台侧事故记录(非我们缺陷)

- 23204/23240(p9 前两次发射): 晚高峰沙箱队列拥堵 —— 排队 75min 未分到 / 部署超时 22min 死亡。深夜队列 10min 起跑。
- 单账号同时只能有一个 run;model 评测与 agent 评测共用该槽(23253 占槽期间 p9 无法发射)。
- tsecbench JWT 实测可跨 6h+(此前「约 50 分钟失效」实为浏览器 localStorage 会话被清,非 token 短命);LevelDB 提取 token + API 直连可解除浏览器依赖。

## 结构性结论

1. 薄调度换来的不是 token 节省(fast 模型上 token 中性),是**可观测性、可移植性与 holdout 突破**(p6 三题连破)。
2. provider 韧性两层缺一不可: A 层(pi idle-timeout+重试)保 provider 路径;常驻编排器+双驱动保调度恢复路径(Execute 子代理由编排器会话 spawn,编排器休眠则无人补位)。
3. 对比直连 harness(23253, 会话重叠峰值 12, 18,800): 平台并发容器配额为 3,双方同限 —— 直连的会话重叠高不是车道多,而是**车道换手快**(短 pass、快进快出,容器占用时间短)。我们时间轴落后 16–63%(按累计分 6000–14000 里程碑到点复算;更早里程碑受起步慢热影响,偏离更大),但每会话效率更高(1.09 会话/题 vs 1.8)、token 效率持平。**差距在每 pass 占槽时长,不在架构**。

## 待办

- p10 候选(配额 3 为平台上限,不动 DEFAULT_QUOTA): 压缩每 pass 占槽时长 —— 两段式 pass(短探针 5–8min: 找到立足点才转深挖,否则立即放槽);起步前家族事实门控(无新事实的同族重复尝试不开容器);零进展快速放槽已在 p7,需再收紧。
- provider-resilience B/C 层(守护看门狗 + skill 降级): A 层两连验后降为纵深。
