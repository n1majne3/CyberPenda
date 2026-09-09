# TSecBench Hosted Delivery Bundle

This Bundle contains one `linux/amd64` TSecBench Hosted Image. Upload only the
`.tar.gz` Docker archive to the TSecBench Hosted Mode page. You can verify it
first with `shasum -a 256 -c SHA256SUMS` or `sha256sum -c SHA256SUMS`.

The image retains the normal embedded CyberPenda Web UI resources. It does not
publish or expose the loopback daemon port. `COMPONENTS.txt` records the exact
versions of the four Runtime CLIs and the Claude Agent SDK used by the packaged
Claude bridge.

## Hosted Mode

Set the required values from `tsecbench.env.example` on the TSecBench page.
`CYBERPENDA_RUNTIME` defaults to `codex`. Optional `CYBERPENDA_REASONING_EFFORT`
is `low`, `medium`, `high`, `xhigh`, or `max`; an omitted value uses `high`.
Optional `CYBERPENDA_TASK_GOAL_APPENDIX` is appended to the required Task Goal.
Optional `CYBERPENDA_AUTO_COMPACT_THRESHOLD` is an integer from 1 to 100.
Optional `CYBERPENDA_AUTO_COMPACT_WINDOW` is an integer from 1 to 1048576.
Optional `CYBERPENDA_MAX_OUTPUT_TOKENS` is an integer from 1 to 1048576.
For DeepSeek on Claude Code, set max output to 393216 (384K) and set the
compact window to 524288 so messages plus 393216 stay under 1048576.
`CYBERPENDA_CONTEXT_WINDOW` 是可选的模型总容量，整数范围为 1 到 1048576。
它和 `CYBERPENDA_MAX_OUTPUT_TOKENS` 都写入 Model Catalog Limit Override，
优先于 Model Capability Cache。留空时使用缓存值；缓存也没有值时使用 Runtime 默认值。

| Hosted 输入 | Claude Code 投影 | Pi 投影 |
| --- | --- | --- |
| `CYBERPENDA_CONTEXT_WINDOW` | `CLAUDE_CODE_MAX_CONTEXT_TOKENS` | `models.json` → `contextWindow` |
| `CYBERPENDA_MAX_OUTPUT_TOKENS` | `CLAUDE_CODE_MAX_OUTPUT_TOKENS` | `models.json` → `maxTokens` |
| `CYBERPENDA_AUTO_COMPACT_WINDOW` | `CLAUDE_CODE_AUTO_COMPACT_WINDOW` | 不投影 |
| `CYBERPENDA_AUTO_COMPACT_THRESHOLD` | `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE` | 不投影 |

Pi 的三种协议共用此投影。Pi 的自动压缩仍使用其原生设置。
模型总容量和自动压缩窗口是独立设置。
Claude Code 对普通自定义模型 ID 可直接使用 context window 值；
对含 `[1m]` 或被识别为 Claude 的 ID 有原生限制，见
[Claude Code 官方说明](https://code.claude.com/docs/en/model-config#correct-the-window-for-a-gateway-or-custom-model-id)。
CyberPenda 不会为强制覆盖容量而关闭自动压缩。

The strict Runtime and protocol matrix is:

- Codex: `openai_responses`
- Claude Code: `anthropic_messages`
- Pi: `openai_chat_completions`, `openai_responses`, or `anthropic_messages`

Pi projects through the same hosted bootstrap as Codex and Claude Code and
trusts only this run's projected project resources through `--approve`. Hermes
is retired and is not installed in the image.

Enter the converted HTTP gateway Base URL with the `.tsecbench.gw` host. Enter
a protocol Base URL. Do not append `/chat/completions`, `/responses`, or
`/messages`. Use a dedicated, revocable evaluation model API key.

### Pi additional models

Pi accepts three optional additional-model slots so `@tintinweb/pi-subagents`
can run subagents on another model. Each slot is
`CYBERPENDA_PI_ADDITIONAL_MODEL_N` (N = 1-3) with optional `_PROTOCOL`,
`_BASE_URL`, and `_API_KEY` overrides:

```env
# Inherits CYBERPENDA_MODEL_PROTOCOL, _BASE_URL, and _API_KEY.
CYBERPENDA_PI_ADDITIONAL_MODEL_1=pi-scout
# A second Hosted gateway with its own key.
CYBERPENDA_PI_ADDITIONAL_MODEL_2=pi-researcher
CYBERPENDA_PI_ADDITIONAL_MODEL_2_BASE_URL=http://second-model.tsecbench.gw/v1
CYBERPENDA_PI_ADDITIONAL_MODEL_2_API_KEY=SECOND_DEDICATED_KEY
```

Rules:

- Pi only. Any of these variables on `codex` or `claude_code` fails
  configuration validation before challenge work.
- Slots are sparse and independent. Slot 2 may be set while slot 1 is not,
  and every omitted override inherits the parent values, never another slot.
- A present-but-empty value is invalid: omit the variable instead of leaving
  it blank. An override without its model id is invalid too.
- Slot base URLs follow the same gateway rules as the parent: plain HTTP,
  `.tsecbench.gw` host, no user info, query, fragment, or operation suffix.
- A model id repeated with the same protocol, base URL, and API key is
  projected once. Different model ids that share the whole effective tuple
  share one projected Model Provider.
- `CYBERPENDA_CONTEXT_WINDOW` and `CYBERPENDA_MAX_OUTPUT_TOKENS` apply to
  every projected model, exactly as they apply to the parent.
- The parent Pi session keeps launching on `CYBERPENDA_MODEL`. Additional
  models only widen the projected registry; they are never a replacement.
- Do not put API keys in `CYBERPENDA_TASK_GOAL_APPENDIX`. Write the calling
  rules there instead: which role uses which model, and when not to switch.

### Subagent model selectors

The plugin's bare-model fallback ignores case and treats dots and dashes as
equal, so `model-4.5` and `model-4-5`, or ids differing only in case, can
select the wrong registry entry. The Runtime must pass the exact
`providerID/modelID` selector:

1. Read the projected registry at `$PI_CODING_AGENT_DIR/models.json` (also
   `~/.pi/agent/models.json` inside the Runtime home). Each key of
   `providers` is a provider ID; each `models[].id` under it is a model ID.
   Generated provider IDs cannot be guessed before bootstrap and provider
   display names are never registry IDs.
2. Pass `providerID/modelID` verbatim to the Agent tool's model argument.
   Preserve case, punctuation, and any slash inside the model ID exactly as
   the registry shows it.

A suggested appendix line:

```text
For subagents, read $PI_CODING_AGENT_DIR/models.json and pass the exact
"providerID/modelID" value as the Agent tool's model argument. Keep the
parent session on its own model; switch models only when the appendix says so.
```

TSecBench injects `BENCHMARK_BASE_URL` and the one-use `BENCHMARK_TOKEN`.
Hosted Mode uses the isolated TSecBench network. It does not start a VPN and
has no public Internet access. The Runtime uses only tools already in the
image. The image includes `tmux`.

The Hosted daemon publishes only the Hosted-adapted `ctf-orchestrator` Skill.
The Task runs with Blackboard disabled and uses the orchestrator FGS as its only
agent-managed semantic state. The Decide process owns list, start, hint, close,
and abandon. Execute agents may submit a candidate through the Hosted Challenge
Client, but they do not change the challenge lifecycle.

The Skill guards against Codex spawn-message delivery failures
(ADR 0034). Every session confirms the `graph/leader.lock` heartbeat before it
acts as Decide; a session that wakes without a fresh lock degrades to an
Execute worker, and a stale lock is taken over with a `ledger.tsv` watchdog
pass. Every dispatch expects the child's fact skeleton within 90 seconds and
re-dispatches on a miss. The lead never ends its turn while `ledger.tsv` has
unsettled agents. The real-Codex acceptance test
(`hosted_real_codex_acceptance_test.go`) probes spawn delivery and the
acknowledgement loop against each image build's Codex CLI.

The container standard output is a sequence-ordered JSONL Hosted Transcript
Stream. Operational logs use standard error. TSecBench owns the formal score
and completion state. The container-local Project, Task, FGS, Evidence,
database, and logs are diagnostic state only.

## Local Mode validation

Load the same image:

```sh
gzip -dc cyberpenda-tsecbench-hosted_VERSION_linux_amd64.tar.gz | docker load
```

Connect the TSecBench VPN on the host. Copy `tsecbench-local.env.example` to a
file outside this Bundle, add the required values, and restrict it before use:

```sh
chmod 600 /secure/path/tsecbench-local.env
./run-tsecbench-local-mode.sh --env-file /secure/path/tsecbench-local.env
```

The local validation checks list, start, submit, and close with the same Hosted
Image. It does not configure a VPN in the container and does not require the
Runtime to solve a challenge. See the runner help for the exact human inputs
and cleanup behavior.

`COMPONENTS.txt` records the exact Runtime and important tool versions resolved
in this image build.

## Build the Bundle

Run the `Build TSecBench Hosted Bundle` GitHub Actions workflow. Enter a Bundle
version such as `v1`. The native `ubuntu-latest` AMD64 runner builds the image,
runs its no-capability smoke test, exports the complete Bundle, verifies its
checksum and compressed size, and uploads one workflow artifact. Download that
artifact. Before you upload it, use that same image and complete one
successful Local Mode validation while the host is connected to the TSecBench VPN. Then
upload its `.tar.gz` Docker archive to the TSecBench page. The GitHub Actions
workflow cannot do this VPN-backed validation because it has no TSecBench
credential or VPN access.

Pull requests that change Hosted Image inputs also run this workflow. These
builds use `pr-<commit>` as the Bundle version. The workflow does not use a
TSecBench token, a model API key, or the TSecBench VPN.
