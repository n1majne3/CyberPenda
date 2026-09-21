# TSecBench API 参考

基址 `https://tsecbench.zc.tencent.com`。所有接口需
`authorization: Bearer <localStorage.token>`,缺头时返回
`{"detail":"该跑分记录不可查看"}`。

## 端点

| 端点 | 内容 |
| --- | --- |
| `GET /api/v1/leaderboard/agent/<run_id>` | 跑分主数据:矩阵、雷达、run_events、score_events、status、set_id |
| `GET /api/v1/leaderboard/agent/<run_id>/score-timeline` | 得分事件序列(每次 correct/wrong 计分) |
| `GET /api/v1/leaderboard/agent/<run_id>/llm/sessions?from=<T>&to=<T>&page=<N>&page_size=50` | 会话清单,分页 |
| `GET /api/v1/leaderboard/agent/<run_id>/llm/sessions/<session_id>?from=<T>&to=<T>&page=<N>&page_size=50` | 单会话逐事件明细,分页 |
| `GET /api/v1/leaderboard/agent/<run_id>/llm/model-usage` | 模型调用与 token 汇总 |
| `GET /api/v1/leaderboard?set_id=<ID>&page=1&page_size=50` | 榜单(平台仅公开前 20 名,越界页返回同样 20 条) |

`from`/`to` 为 UTC ISO 时间,来自目标 agent 页面自身发出的会话请求(见 SKILL.md 第一步)。
`set_id` 从 agent 主数据的 `set_id` 字段取(TSecBench v1 = 4)。

## agent 主数据字段

- `status`: `finished` / `timeout`(6 小时预算是否优雅用完)。
- `radar_data`: 六维度 flag 口径百分比。
- `matrix[]`: 每维度 `challenges[]`,`state` = solved / partial / none,
  含 `correct_flag_count` / `total_flag_count` / `difficulty`。
- `run_events[]`: `operation_type` ∈ instance_launch / instance_close /
  answer_correct / answer_wrong / task_start,含 `extra.container_addr`。
- `score_events[]`: 得分明细,等价于 score-timeline。
- `duration_seconds` 固定 21600;`elapsed_seconds` 是实际用时。

## 会话清单字段(rows[])

- `session_id`、`model`、`protocol`(如 anthropic_messages)。
- `closed_reason`: `timeout`(step 预算到点)/ `compacted`(上下文压缩)。
- `first_captured_at` / `last_active_at`(UTC)→ 会话时长。
- `event_count`、`range_call_count`(模型调用数)。
- `total_usage`: `{input, output, cache_read, cache_write, reasoning}`。
  观测值:reasoning 常为 0 但思考实际计入 output;cache_write 恒 0,
  cache_read 仍是缓存命中口径。
- `title`: 首条用户消息截断,可用来识别接力棒会话(含"你只负责下面这【一个 step】")
  或编排器会话(含 "REQUIRED SKILL INVOCATION")。

## 会话明细字段(steps[])

- `event_type`: assistant_message / tool_result / session_note /
  system_note / user_message。
- `items[]` 按 `kind`: `tool_call`(含 `name`、`args`)、`tool_result`、
  `thinking`、`assistant_text`、`user_text`、`system_note`。
  `char_len` 是该条文本长度;tool_call 的 char_len 恒为 0。
- `usage`: 该次调用的 input/output/cache_read。
- `captured_at`: **按"模型响应到达"批量落盘**,不是逐事件时间。
  一个批次内多条事件共享同一秒;distinct captured_at 数 ≈ 模型响应数。
- `error`: session_note 常见 `Harness 写入历史前修改了上一轮响应`,属常态记录,非故障。
- `stop_reason` / `state`: 模型调用错误如 `incomplete SSE response` 出现在末条。
