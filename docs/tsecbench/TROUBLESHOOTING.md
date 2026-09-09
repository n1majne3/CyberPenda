# TSecBench Hosted Troubleshooting

## Bootstrap validation fails

Check all required `CYBERPENDA_*` page variables. Optional
`CYBERPENDA_REASONING_EFFORT` must be `low`, `medium`, `high`, `xhigh`, or
`max`. Optional `CYBERPENDA_TASK_GOAL_APPENDIX` is appended to the required
Task Goal. Optional `CYBERPENDA_AUTO_COMPACT_THRESHOLD` must be an integer
from 1 to 100. Optional `CYBERPENDA_AUTO_COMPACT_WINDOW` must be an integer
from 1 to 1048576. Optional `CYBERPENDA_MAX_OUTPUT_TOKENS` must be an integer
from 1 to 1048576. Secret values must be present at deployment time, but they
must stay out of templates and command arguments. The Controller reports a
bounded error before it creates the Project.

## Runtime and protocol mismatch

Use the strict matrix in `README.md`. Codex requires `openai_responses`. Claude
Code requires `anthropic_messages`. Pi accepts `openai_chat_completions`,
`openai_responses`, or `anthropic_messages`. Hermes is retired and is not installed in the image.

## Claude request exceeds the 1M context

Compact is already on. The occasional HTTP 400 happens when a large tool
result jumps past the default compact point, then Claude still reserves
32000 completion tokens. Messages plus that reservation can exceed 1048576.

Set the compact window and the max output together. A larger max output
without an earlier compact window makes overflow more likely. DeepSeek max
output is 384K (393216). DeepSeek documents compact window 786432, but
786432 plus 393216 exceeds 1048576. For DeepSeek, use:

```
CYBERPENDA_CONTEXT_WINDOW=1048576
CYBERPENDA_AUTO_COMPACT_WINDOW=524288
CYBERPENDA_MAX_OUTPUT_TOKENS=393216
```

524288 plus 393216 is 917504, which stays under 1048576 and leaves a buffer
for one large tool result.

## Context window 或 max output 未生效

`CYBERPENDA_CONTEXT_WINDOW` 和 `CYBERPENDA_MAX_OUTPUT_TOKENS` 都接受
1 到 1048576 的整数。检查生成的 Claude `settings.json` 的 `env`，或
Pi `agent/models.json` 中所选模型的 `contextWindow` 和 `maxTokens`。
显式 Hosted 值优先于模型能力缓存。留空不表示固定使用 32000。
自动压缩窗口和百分比仅支持 Claude Code，Pi 不接收这些 Claude 变量。
Claude Code 对 Claude 模型 ID 和 `[1m]` ID 的窗口覆盖有限制，详见 README。
投影值不会扩大服务端实际允许的模型容量或输出上限。

## Model endpoint fails

The model URL must use HTTP and an already converted `.tsecbench.gw` host. It
must be the protocol Base URL, not an operation URL. Hosted Mode has no public
Internet access. The same rules apply to every
`CYBERPENDA_PI_ADDITIONAL_MODEL_N_BASE_URL` slot.

## Pi additional-model validation fails

`CYBERPENDA_PI_ADDITIONAL_MODEL_N` (N = 1-3) accepts Pi only. Check that:

- `CYBERPENDA_RUNTIME` is `pi`. Any additional-model variable on `codex` or
  `claude_code` (including the omitted-Runtime default) fails before the
  Project is created.
- Every set value is non-empty. These variables have no "blank keeps the
  default" behavior: omit the variable instead of leaving it blank.
- `_PROTOCOL`, `_BASE_URL`, and `_API_KEY` are set only together with their
  matching `_N` model id.
- The same model id is not configured twice with a different protocol, base
  URL, or API key. Identical repeats are projected once.

## Subagent selects the wrong model or provider

The `@tintinweb/pi-subagents` bare-model fallback ignores case and treats
dots and dashes as equal. `model-4.5` and `model-4-5`, or ids that differ
only in case, can resolve to the first equal-scoring registry entry on the
wrong provider. The Runtime must read
`$PI_CODING_AGENT_DIR/models.json`, take the exact `providerID/modelID`
pair, and pass it verbatim to the Agent tool's model argument — preserving
case, punctuation, and any slash in the model ID. Provider display names are
not registry IDs, and generated provider IDs cannot be guessed before
bootstrap. Put that instruction in `CYBERPENDA_TASK_GOAL_APPENDIX`.

## Model call returns 400 "role 'developer' is not allowed"

Pi sends the system prompt with the OpenAI-only `developer` role when a
model declares reasoning support and the gateway looks like a standard
OpenAI endpoint. OpenAI-compatible gateways such as Kimi reject that role.
CyberPenda projection pins `compat.supportsDeveloperRole: false` on every
projected model entry, so Pi uses the classic `system` role instead. If the
error appears on an image built before that projection, rebuild the image;
as a per-run alternative, switch the slot to the provider's
Anthropic-compatible endpoint through `_PROTOCOL=anthropic_messages` and
its Anthropic base URL — the Anthropic protocol has no developer role.

## Runtime fails

If the initial Runtime never becomes live, the Controller drains retained
Transcript entries and exits nonzero. It does not retry, switch Runtime,
resume, or perform Task Finish.

After a live Runtime has started, Transcript, stdout, observe, and later Task
status errors stay on standard error. The Controller keeps the Runtime until
TSecBench terminates the container. Read standard error for operational
diagnostics and standard output for the JSONL Hosted Transcript Stream.

## Challenge Platform API behavior changes

The Hosted-adapted `ctf-orchestrator` uses the Hosted Challenge Client for the
known `/openapi/v1` list, start, hint, submit, close, and abandon operations.
The Runtime inspects unexpected responses and decides if a compatible request
or retry is useful. The Controller does not apply an API retry policy.

## Local Mode cannot reach a challenge

Connect the TSecBench VPN on the host before the runner starts. The image does
not contain or configure a VPN client. Check the external env file has mode
`0600` and contains the required one-use values.

## The archive is rejected or too large

Upload the Docker `.tar.gz` file, not the whole Bundle directory. The release
script uses `docker save | gzip` and fails when the compressed archive is 3 GB
or larger. Verify `SHA256SUMS` after copying the Bundle.
