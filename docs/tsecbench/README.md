# TSecBench Hosted Delivery Bundle

This Bundle contains one `linux/amd64` TSecBench Hosted Image. Upload only the
`.tar.gz` Docker archive to the TSecBench Hosted Mode page. You can verify it
first with `shasum -a 256 -c SHA256SUMS` or `sha256sum -c SHA256SUMS`.

## Hosted Mode

Set the five values from `tsecbench.env.example` on the TSecBench page.
`CYBERPENDA_RUNTIME` defaults to `pi`. The strict Runtime and protocol matrix is:

- Pi: `openai_chat_completions`, `openai_responses`, or `anthropic_messages`
- Codex: `openai_responses`
- Claude Code: `anthropic_messages`

Enter the converted HTTP gateway Base URL with the `.tsecbench.gw` host. Enter
a protocol Base URL. Do not append `/chat/completions`, `/responses`, or
`/messages`. Use a dedicated, revocable evaluation model API key.

TSecBench injects `BENCHMARK_BASE_URL` and the one-use `BENCHMARK_TOKEN`.
Hosted Mode uses the isolated TSecBench network. It does not start a VPN and
has no public Internet access. The Runtime uses only tools already in
the image.

The container standard output is a sequence-ordered JSONL Hosted Transcript
Stream. Operational logs use standard error. TSecBench owns the formal score
and completion state. The container-local Project, Task, Blackboard, Evidence,
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
