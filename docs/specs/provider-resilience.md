# Provider 失败韧性(Provider Resilience)

Status: 设计稿。未实现,不改变现行 Hosted Controller 边界,不改变现行
产品代码。动机数据见本地 `/.tsecbench-analysis/RUN-ANALYSIS.md`
(run 22762,dispatch-p7 验证跑,step-5-preview,2026-09-26)。
Domain terms: [CONTEXT.md](../../CONTEXT.md)。

## 1. 目的与范围

Run 22762 在 T+42min 冲到 4600 分(历届最快开局,a 族接力成型)后,
于 22:51(+08 晚高峰)整体冻结:23 个会话同一时刻停摆,平台计费调用数
精确停在 560,score-timeline 归零,冻结持续 53 分钟以上仍未恢复。
证据指向 step-5 网关高峰期挂起连接 —— 不是 dispatcher 死锁(单线程停摆
应伴心跳 CRITICAL,且不会让编排器与全部 worker 齐停)。

根因(机制级):

1. CyberPenda **自身不发任何 LLM HTTP 请求**。整条 provider 路径是
   stdio JSON-RPC 到外部 `pi` 二进制;真正的 HTTPS 调用在 pi 内部
   (`@earendil-works/pi-coding-agent`,不在本仓库)。
2. 我们传入的 `CYBERPENDA_API_TIMEOUT_MS` 在本仓库**零代码读取**;
   它只是经环境传入 pi 进程环境,生不生效取决于 pi 内部,且 pi 全 dist
   没有任何 `*TIMEOUT*` env 读取。
3. pi **自带** HTTP idle-timeout 防御(`httpIdleTimeoutMs`,默认 5 分钟,
   undici dispatcher),但存在三个失效点:
   - `httpIdleTimeoutMs===0` 会被替换为 `2147483647`(≈68 年,即禁用);
   - 该设置**无 env 映射**,只能经 `settings.json` 或
     `setHttpIdleTimeoutMs()` 改变;
   - `retry.provider.maxRetries` 默认 `0`,即使 idle-timeout 触发也不重试,
     且一次失败的 turn 不会终止 run(harness 只等 `ctx.Done()` 或进程死),
     会话就此挂住。

目标:让单个挂起的 provider 请求被**检测、放弃、并重试**,编排器继续调度,
而非整 run 冻死、wall-clock 空烧。

## 2. 非目标

- 不做 **Model Runtime Projection** 之外的模型流量代理:本设计不向
  LLM 路径插入任何 CyberPenda 侧 proxy(见 CONTEXT.md "Model Runtime
  Projection" 禁用 LLM proxy / gateway request)。A 层是**投影配置**,
  让 pi 自己超时重试。
- 不实现平台级 run 重启:**Hosted Evaluation Run 不可重启**
  (non-restartable)。B/C 层的"恢复"指**运行时进程内恢复**
  (终止并重建 Runtime 进程会话),不是重新发起一次 Hosted run。
- 不改变模型选型与薄调度(dispatch-time injection)语义:本设计只回收
  "provider 挂起"这一类失败,不改变调度策略、注入内容或派发纪律。
- 不追求对所有 provider 失败的完全覆盖:网络分区、鉴权过期、配额耗尽
  各有归属,本设计针对**挂起/停滞**(连接开着但无字节流动)这一类。

## 3. 现状与目标架构

现状:LLM 路径上**零超时、零重试、零停滞检测**。bridge RPC 等待无 timer;
harness `Run` 只等 `ctx.Done()` 或 bridge Closed/Terminated;托管 `Wait`
循环连 `failed` 都不退出,只等平台终止(SIGTERM 还被忽略)。
**Runtime Activity** 活性判断明确"只看进程健康,流逝时间不算活性证据"——
挂起但活着的 pi 会永远报 `live/busy`。

目标三层(解耦,按优先级):

| 层 | 位置 | 动作 | 性质 |
| --- | --- | --- | --- |
| A. pi settings 投影 | `projectPiSettings` | 投影 `httpIdleTimeoutMs` + `retry.provider` 让 pi 自检测挂起流→断开→指数退避重试 | 根治 |
| B. 守护层停滞看门狗 | daemon runtime 活性 | N 分钟无任何 provider 事件→判僵死→Runtime 进程内恢复 | 兜底 |
| C. 编排器 skill 降级 | dispatch.py | 停滞检测从"只写 CRITICAL 日志"升级为可触发换题/降级 | 最后手段 |

```
provider 网关挂起(无字节)
        │
   A 层:pi undici idle-timeout(120s)触发 → 断开 → 重试(maxRetries=3)
        │ 全部重试失败
        ▼
   B 层:daemon 看门狗(N min 无 provider 事件)→ 判僵死 → 进程内恢复
        │ 恢复失败/反复僵死
        ▼
   C 层:dispatch.py 停滞检测 → 记账 infra_dead → 换题/降级 → CRITICAL 日志
```

## 4. 设计决策

### D1 A 层:用 pi 内建 idle-timeout + retry,零新代码路径

pi 的 anthropic_messages 路径已是
`retryProviderRequest(request, {maxRetries, maxRetryDelayMs, signal})`,
接受调用点 `options.timeoutMs`/`options.signal`;`httpIdleTimeoutMs`
经 `setHttpIdleTimeoutMs` 重建带 idle 超时的 undici dispatcher。
`settings.json` 是 projection-owned(`projectPiSettings`,
projection.go:1749),host `~/.pi/agent/settings.json` 从不读。

**决策**:在 `projectPiSettings` 的 settings map 上增加两个投影键,
默认值保守:

```json
{
  "httpIdleTimeoutMs": 120000,
  "retry": { "provider": { "maxRetries": 3, "maxRetryDelayMs": 30000 } }
}
```

- `httpIdleTimeoutMs=120000`(2 分钟):比默认 5 分钟更敏感,又留足
  慢模型思考间隙;**严禁为 0**(0 = 禁用)。取 2 分钟的理由:step-5
  正常流式响应的字节间隔远小于此;22762 的冻结是 53 分钟零字节,2 分钟
  足以判定死亡又不误伤长思考。
- `maxRetries=3` + `maxRetryDelayMs=30000`:有限退避,避免对网关形成
  重试风暴;`isRetryableProviderError` 已按 `x-should-retry` header 与
  5xx/429 分类,idle-timeout 类错误归入可重试。

**这是唯一能在请求半途真正放弃并重试的层**;B/C 都只是事后检测。

### D2 B 层:守护层停滞看门狗,补"流逝时间不算活性"的洞

现行 **Runtime Activity** 只看进程健康(runtime_activity.go),
挂起但活着的 pi 永远报 `live`。决策:新增一个**独立于进程活性**的
停滞信号 —— 以"最近一次 provider 事件(turn/byte/subagent 回调)落盘
时间"为活性证据,超过阈值(默认 180s,可经 Hosted Model Configuration
覆盖)判为停滞,触发 Runtime 进程内恢复(终止并重建该 Runtime 进程的
会话),并记 **infra_dead** 事件。

边界:看门狗只作用于 Hosted 评估路径;普通 Task/Session 的活性语义
不变,避免误伤交互式长思考。

### D3 C 层:skill 从"只记录"升级为"可降级"

dispatch.py 现有 `check_heartbeat`(HEARTBEAT_STALE_SEC=600)只在
leader.lock 陈旧时写 CRITICAL 日志,无法动作。决策:停滞检测命中后,
除记 CRITICAL 外,把当前卡点题目记账为 `infra_dead`,触发换题/复活
次优题,把"冻死等待"转为"有损前进"。这是对 A/B 都失效时的逃生门,
不改变正常调度纪律。

### D4 可观测性优先

每一层的检测与动作都落 **Hosted Operational Log**(stderr)与 CRITICAL
日志,带时间戳与判定依据(idle 秒数、重试次数、判定阈值)。22762 的教训
是"2.5 小时静默死在无人观察上";韧性机制若不可观测,等于没有。

## 5. 验收标准

| 编号 | 标准 | 验证方式 |
| --- | --- | --- |
| AC1 | settings.json 投影出 `httpIdleTimeoutMs` 与 `retry.provider` | 单测断言 `projectPiSettings` 输出含两键且值正确 |
| AC2 | `httpIdleTimeoutMs` 永不为 0 | 单测:投影值 > 0;对显式 0 输入报错或纠正 |
| AC3 | 挂起流在阈值内被断开并重试 | 集成:模拟无字节 provider,断言 pi 侧重试发生 |
| AC4 | 整 run 不再因单次挂起冻结 | step-5 低谷验证跑:观察停滞恢复、分数继续增长 |
| AC5 | 每层动作有 stderr/CRITICAL 日志 | 日志审查 |

## 6. 实施顺序

1. **A 层先行**(零新代码路径,下次验证跑即可见效):改
   `projectPiSettings` 投影两键 + 单测(AC1/AC2)。
2. B 层看门狗(独立 PR):daemon 停滞信号 + 进程内恢复(AC4 兜底)。
3. C 层 skill 降级(独立 PR):dispatch.py infra_dead 换题。
4. 全程 AC5 可观测性。

## 7. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| `httpIdleTimeoutMs` 语义未在真实 step-5 挂起上验证 | A 层上线后第一次验证跑即观测;失败回退到 B/C |
| 慢模型长思考被 2min idle 误判 | 阈值可经 Hosted Model Configuration 覆盖;默认 2min 已大于正常流式字节间隔 |
| 重试风暴打挂网关 | `maxRetries=3` + `maxRetryDelayMs=30s` 有限退避;`x-should-retry:false` 尊重网关意愿 |
| B 层误伤交互式长 Task | 看门狗只作用于 Hosted 评估路径,普通 Task/Session 活性语义不变 |
