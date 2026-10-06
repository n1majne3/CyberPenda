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
| p11 | 围城续作 + 零进展升档 | 有立足点题预算下限抬到 SIEGE_BUDGET_MIN=60(围城棒:从上一棒里程碑直接续作,禁重验已排除面);零进展且无立足点 → 预算 +15/棒,封顶 90(ZERO_PROGRESS_STEP/BUDGET_CEILING);终盘 exclude_fresh(最后 3600s 不发新探针,火力全给围城)。靶子: 23972 的 c-03(24 会话零出分,难度误标)/a-18(21 会话 HMAC 链被切断)烧掉 23% 调用 | 24252(step-5×XBOW 晚间) | **21,100/74.6% 活满 6h 零死亡** —— p11 无罪判定成立(午间两死为环境时段性 provider 风险)。围城在 XBOW 硬尾略亏: 无立足点题吃不到围城预算,终盘停滞早于 p10 ~-1,800。v1 无此对照,p11 调度保留进 p12 |
| p12 | 静默看门狗(B 层实装) | hosted Wait 循环盯 transcript 游标: running 且 >480s 无推进 → steer 复活(CYBERPENDA_SILENCE_REVIVE_SEC,0 关),冷却 300s,上限 5 发;复活消息带 ctf-orchestrator 身份确认流程 | 24370(step-5×XBOW) | **08:12 全会话冻结 → 5 发全打光未救活,800 分僵尸收官(验证目的达成)**。尸检: ①真根因 = pi 扩展 UI 对话框帧(extension_ui_request confirm 类)阻塞 turn,hosted 无人应答;②看门狗机制成立 —— 第 2 发 interrupt_then_replace 真复活了编排器 11 秒(跑了诊断+心跳),但 4/5 发 in_turn_steer 对 parked turn 必败;③5 发 ~65min 烧光,provider 恢复后无人敲门;④权限 id 提取链缺裸 id 键,daemon 权限阶梯登记不上,应答管道全程通畅但没人扣得动扳机 |
| p13 | 权限门根修 + 看门狗硬化 | F1 权限事件带身份(bare id 回退 + title/message/method)+ 对话框帧原始 runtime_output 转发(仅 select/confirm/input/editor,notify 不转发);F2 hosted 自动应答器(Wait 见 pending permission 即 POST respond,默认 allow,CYBERPENDA_PERMISSION_AUTO_RESPOND=off/deny 可关,幂等);F3 复活 steer 显式 force_replace(唯一被证实有效的模式);F4 复活预算 5→60 + 复活消息强制推进(禁未派 worker 即回阻塞等待);F6 create body 删 API_TIMEOUT_MS 死键(Claude Code 遗留,pi/本仓库均不读) | 24447(step-5×v1) | **17,650/49 flag —— step-5 v1 新纪录(超 p10 的 17,600)**,6h 零停顿。曲线: 3200@T+32 → 8500@T+86 → 11250@T+122(围城收网) → 14350@T+178 → 17,650 终局。T+14(24370 权限门死亡点)顺利通过。**验证判定: 通过** —— 权限门死类未再现(对话框是否被 F2 即时应答待容器日志/artifact 确认)。F5(--approve)暂缓;F7 锁 pi@1.0.0+pi-subagents@0.19.0 待实施(24370 考古实为 pi 0.99.2;3h00m41s 固定超时释放 park turn,编排器曾自修 dispatch.py flock 泄漏 —— p14 候选) |

## 平台侧事故记录(非我们缺陷)

- 23204/23240(p9 前两次发射): 晚高峰沙箱队列拥堵 —— 排队 75min 未分到 / 部署超时 22min 死亡。深夜队列 10min 起跑。
- 单账号同时只能有一个 run;model 评测与 agent 评测共用该槽(23253 占槽期间 p9 无法发射)。
- tsecbench JWT 实测可跨 6h+(此前「约 50 分钟失效」实为浏览器 localStorage 会话被清,非 token 短命);LevelDB 提取 token + API 直连可解除浏览器依赖。

## 结构性结论

1. 薄调度换来的不是 token 节省(fast 模型上 token 中性),是**可观测性、可移植性与 holdout 突破**(p6 三题连破)。
2. provider 韧性两层缺一不可: A 层(pi idle-timeout+重试)保 provider 路径;常驻编排器+双驱动保调度恢复路径(Execute 子代理由编排器会话 spawn,编排器休眠则无人补位)。
3. 对比直连 harness(23253, 会话重叠峰值 12, 18,800): 平台并发容器配额为 3,双方同限 —— 直连的会话重叠高不是车道多,而是**车道换手快**(短 pass、快进快出,容器占用时间短)。我们时间轴落后 16–63%(按累计分 6000–14000 里程碑到点复算;更早里程碑受起步慢热影响,偏离更大),但每会话效率更高(1.09 会话/题 vs 1.8)、token 效率持平。**差距在每 pass 占槽时长,不在架构**。

## 待办

- p13 双模型验证收官: step-5(24447)= 17,650 纪录,6h 零停顿; **GLM-5.3(25408)= 11,100/34 flag**。
  05:34 冻结 = BigModel 429 限额耗尽(429 不入平台计量,故调用数纹丝不动;会话 activity 仍在报 = 重试中活着),
  06:06 恢复 = 用户手动 reset 限额(必要条件);限额恢复后的下一发看门狗复活弹大概率完成了 turn 重启
  (429 风暴耗尽 pi 重试后 turn park,无外部输入不自愈 —— 24370 教训),复活后首旗 06:22。
  判定: 权限门死类未再现;看门狗在"限额恢复后重启 parked turn"上大概率有效(artifact 可实锤)。
- F7 锁 pi 版本: Dockerfile 锁 pi@1.0.0 + pi-subagents@0.19.0(24370 考古实为 pi 0.99.2;对话框行为非 1.0 回归)。
- p14 候选(按证据排序): ①看门狗识别"全 429 风暴"形态 —— 连续复活弹无成功调用时降频/暂停弹药,
  避免长限额窗口耗尽 60 发(25408 实证:限额耗尽期每发复活 turn 也撞 429 后 park,纯烧弹药)
  ②模型调用计数差分作活性信号(25408 定位冻结靠的就是它) ③dispatch.py 锁卫生(24370 flock 泄漏)
  ④攻坚预算(无立足点硬题尾巴)。
- 网络纪律: 外网模型端点一律走 <域名>.tsecbench.gw 网关(直连被沙箱挡,25405 秒死实锤)。
- deepseek 冲榜(p13×v1): 验证已过,等令发车。
