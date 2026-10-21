# 真实 LLM Steer 生存性

验证 Steer 后 Runtime 是否仍能接收下一条输入。HTTP 202、pending 徽标或一次 `applied` 都不是充分证明。Pi 原生 Steer 与 Claude Code 中断替换必须分开记录。

## Sub-features

- `steer-baseline`：Sandbox 中的真实 Runtime 先完成一次普通模型回复。
- `steer-busy`：确认模型响应已经开始且 Runtime busy 后，经 UI 发出 Steer，得到本轮唯一 marker。
- `steer-next-input`：Steer 之后发送新的 probe，并从 assistant 消息中读到对应回复；Runtime 保持 live，结束后回到 idle。
- `steer-boundary`：在接近 Turn 完成时操作，记录实际命中的原生 Steer、替换或 idle Send；受控 action_required 不得被隐藏重试。
- `steer-persistence`：刷新同一 Session，Steer 和 probe 的 assistant 回复仍在。

## How to get to it (user POV)

- 在 `/sessions` 的 Runtime Launch Controls 选择 Pi 或 Claude Code、真实 Model Provider 和 Sandbox Runner，保留 Host Browser 关闭。
- 在 Session Conversation 输入框填入指令；主发送按钮根据实际能力显示 `Steer current turn`、`Interrupt and send`、`Send` 或 `Resume and send`。
- `Queue message` 是独立路径，不算作原生 Steer 的替代证明。

## Driving it with agent-browser

Preconditions：获得真实模型调用授权。使用新的隔离实例，保留 Podman 容器前后快照。必须先启动真实 Runtime；启动失败时停止此分支，不把它记作 Steer 失败。

- **Sandbox 镜像**：provider bridge 编译进 Sandbox 镜像，所以 bridge 的源码改动不会自动进入 Runtime。验证 bridge 改动时先把它派生成本地镜像（`FROM <base>` 加 `COPY` 新二进制到 `/usr/local/bin/pentest-provider-bridge`），再用 `instance.py launch "$RUN" --sandbox-image <image>` 启动。核实 `run.json` 的 `sandbox_image` 和 `daemon.log` 的 `sandbox image:` 行，否则测的还是旧 bridge。

- **配置**：在本轮 `scratch/model-access.json` 保存操作员明确提供的 `api_key` 和 `model`。这个私有输入不会被浏览器动作日志记录，也不进入技能包。网关只能通过 loopback 访问时，使用 `uv run --no-project python -I .claude/skills/verify-cyberpenda/scripts/loopback-relay.py "$RUN" 172.30.128.1 172.30.128.0/20 3425` 建立仅 WSL 虚拟网卡上的临时 TCP 转发。先核实当前 WSL 网卡地址和子网；这条命令只适用于已核实的本机网络。它只透传真实请求，不模拟或修改模型响应。
- **Model Provider**：relay 运行后执行 `uv run --no-project python -I .claude/skills/verify-cyberpenda/scripts/live-state.py "$RUN" setup`。它通过正式管理 API 创建本轮 Provider 和凭据绑定，并在此独立实例中停用 Skills，隔离额外 Skill 指令。使用 Disabled Blackboard Mode。此结论不覆盖 FGS 或启用 Skills 的会话。
- **基线启动**：`ab open "$UI/sessions"`，使用 `#launch-runtime`、`#launch-model-provider` 和 `#launch-model`，在 `#session-initial-input` 请求仅返回 `BASELINE_PI_READY` 或 `BASELINE_CLAUDE_READY`，禁止工具和子代理。经 `Launch session` 启动，记录 Session ID。要求真正的 assistant 回复，不接受用户输入中的同名 marker。
- **状态证据**：`uv run --no-project python -I .claude/skills/verify-cyberpenda/scripts/live-state.py "$RUN" snapshot --session "$SESSION_ID" --label baseline` 保存 owner、Timeline 和 Transcript。`wait --session "$SESSION_ID" --label baseline-ready --state idle --text BASELINE_PI_READY --timeout 120` 可等待 Pi 的基线条件；Claude 使用对应 marker。所有标签在同一 run 内必须唯一。
- **Busy Steer**：从 idle 发送以本轮唯一 `WORK` marker 开始的有限长文本生成目标，禁止工具。等待实际 assistant 流式内容和 `live + busy`，而不只是 Harness 登记。记录 `ab get attr '[data-testid="composer-send"]' aria-label`。在 `textarea[aria-label="Session message"]` 填入本轮唯一 Steer marker 指令，使用同一个真实 UI 按钮提交。记录实际 mode、request ID 和状态变化。
- **接受与完成分开**：先要求 Steer 在 45 秒内结算，再要求模型在 120 秒内回复唯一 marker，并达到 `live + idle`。若超时，保存现场，不自动 Resume 或 Stop 来掩盖失败。检查 Timeline 的 Provider Turn 和 settlement；`applied` 本身不证明模型已处理。用 `live-state.py steer` 等待新 request ID 的终态，用 `uv run --no-project python -I -` 只读打开 `$RUN/scratch/pentest.db` 的 `accepted_steering`，核对 `state`、`expected_provider_turn_id` 与 `error_code`。
- **后续输入**：经同一输入框发送新的唯一 probe。主按钮必须是正常 `Send`，不是 `Resume and send`。从 assistant 消息中断言 probe 回复，再检查 live→idle，记录 native session、Continuation 和 Sandbox 容器身份；不能用新建 Runtime 冒充原会话存活。
- **重复与边界**：每个 Runtime 至少完成 3 次独立 busy-Steer + 后续 probe。接近完成边界的额外尝试单独报告实际 mode；没有命中该时序，就不宣称已经覆盖 race。对 action_required 只使用 UI 明确提供的恢复动作，并把恢复与无故障路径分开统计。
- **刷新**：刷新同一 Session，保存 Transcript、截图、浏览器错误及最后 owner 状态。只有 assistant 的 marker、后续 probe 和仍可用状态同时成立，才算一轮成功。检查是否出现意外工具调用。

## Gotchas

- Windows 原生 daemon 的 Skill 目录符号链接权限错误发生在模型调用前；它是启动阻断，不是 Steer 缺陷。记录 `provider_session_setup_failed` 的完整错误。
- Pi 走原生 `pi/steer`；Claude Code 采用 interrupt→settlement→新输入。不要混合两者的成功率或 Continuation 预期。
- Pi 的 Steer 结算要核对 `accepted_steering.expected_provider_turn_id` 与 bridge 返回的 `turn_id`。2026-10-21 曾在 `cmd/pentest-provider-bridge` 的 `pi/steer` 分支发现活动 Turn 被改写为 Steer 请求 ID，导致同轮 Steer 被判 `target_turn_changed` / `action_required`，而模型其实已按 Steer 回复；该缺陷已修复，修复后三轮原生 Steer 全部 `applied` 且无提示。判定标准是数据库里的这两个 ID 和界面提示，不是单一徽标。
- 同轮 Steer 的 marker 要来自 Steer 请求之后的 assistant 消息；Pi 会先把提示词本身作为 assistant 消息回显，那不是 Steer 生效。
- Claude Code 的每次替换都会新建 Continuation；容器与原生会话 ID 不变才说明原 Runtime 存活。
- 切换 Provider、model 或 Reasoning Effort 可能触发替换；原生 Steer 轮次保持这三项不变。
- marker 必须来自 assistant 的 message，不能匹配用户 prompt、reasoning、错误日志或工具结果。
- 排队消息、idle Send、显式恢复、新 Runtime 都要单独分类，不能算作 busy 原生 Steer 成功。
- `live-state.py` 的 `wait` 只匹配 assistant 的 `message`；`--text` 留空表示不要求 marker（用于只等 `busy`）。`snapshot` 无条件保存一次观测。`steer` 等待 `native_steer_request_id` 变成并非 `--not-request` 的终态。
- `steer-summary.py RUN` 离线汇总已保存的观测，不访问应用。
- `loopback-relay.py` 不记录载荷。结束时先停止本轮 Runtime，创建 `$RUN/RELAY_STOP` 并确认 `evidence/relay-cleanup.json`，再关闭浏览器和实例。用前后快照及本轮 container.cid 识别并清理容器；不碰原有容器或镜像。
- 清理前保留状态与错误证据。`instance.py stop` 会移除新建 scratch，包括私有 key 和会话文件；如果遇到挂起，先保存必要的脱敏诊断，再清理。
- 有界重复测试不是无限稳定性的证明；报告平台、镜像及 Runtime 版本、模型配置、成功/失败/受控恢复次数和未覆盖场景。
