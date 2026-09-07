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
