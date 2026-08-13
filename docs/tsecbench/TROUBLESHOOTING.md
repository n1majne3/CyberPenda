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

Use the strict matrix in `README.md`. Pi supports the three packaged protocols.
Codex requires `openai_responses`. Claude Code requires `anthropic_messages`.

## Claude request exceeds the 1M context

Compact is already on. The occasional HTTP 400 happens when a large tool
result jumps past the default compact point, then Claude still reserves
32000 completion tokens. Messages plus that reservation can exceed 1048576.

Set the compact window and the max output together. A larger max output
without an earlier compact window makes overflow more likely. DeepSeek max
output is 384K (393216). DeepSeek documents compact window 786432, but
786432 plus 393216 exceeds 1048576. For DeepSeek, use:

```
CYBERPENDA_AUTO_COMPACT_WINDOW=524288
CYBERPENDA_MAX_OUTPUT_TOKENS=393216
```

524288 plus 393216 is 917504, which stays under 1048576 and leaves a buffer
for one large tool result.

## Model endpoint fails

The model URL must use HTTP and an already converted `.tsecbench.gw` host. It
must be the protocol Base URL, not an operation URL. Hosted Mode has no public
Internet access.

## Runtime fails

If the initial Runtime never becomes live, the Controller drains retained
Transcript entries and exits nonzero. It does not retry, switch Runtime,
resume, or perform Task Finish.

After a live Runtime has started, Transcript, stdout, observe, and later Task
status errors stay on standard error. The Controller keeps the Runtime until
TSecBench terminates the container. Read standard error for operational
diagnostics and standard output for the JSONL Hosted Transcript Stream.

## Challenge Platform API behavior changes

The hosted Skill describes the known `/openapi/v1` list, start, hint, submit,
and close requests. The Runtime inspects unexpected responses and decides if a
compatible request or retry is useful. The Controller does not apply an API
retry policy.

## Local Mode cannot reach a challenge

Connect the TSecBench VPN on the host before the runner starts. The image does
not contain or configure a VPN client. Check the external env file has mode
`0600` and contains the required one-use values.

## The archive is rejected or too large

Upload the Docker `.tar.gz` file, not the whole Bundle directory. The release
script uses `docker save | gzip` and fails when the compressed archive is 3 GB
or larger. Verify `SHA256SUMS` after copying the Bundle.
