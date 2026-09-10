# Challenge Platform adapters

Put `<id>.json` in `/data/adapters/` and set `CYBERPENDA_CHALLENGE_ADAPTER=<id>`.
That overlay is read at process start. The image does not need a rebuild.

Baked defaults: `/opt/cyberpenda/adapters/tsecbench.json` and
`/opt/cyberpenda/adapters/ichunqiu.json`.

Copy `internal/challengeadapter/adapters/tsecbench.json` and change
`base_url_env`, `token_env`, `token_header`, `path`, and `query`/`json`
templates (`{{code}}`, `{{candidate}}`).

## Optional manifest fields

- `token_query`: send the token as this URL query parameter instead of the
  `token_header` header. Use it when the platform authorizes by query string.
- `challenge_fields`: map output challenge fields to dotted source paths, for
  example `"unique_code": "question_id"` or
  `"container_addr": "connection.docker_url"`. Supported targets:
  `unique_code`, `description`, `is_completed`, `total_score`,
  `container_addr`. Unmapped source fields with matching JSON tags on the
  challenge struct (`file_url`, `category`, `interactive`, `capabilities`,
  `extensions`) pass through without a mapping entry.
- `submit_correct`: dotted path in the submit response whose truthy value
  (non-zero number, `true`, `"true"`, `"1"`) marks the answer correct. When
  the response envelope has a non-zero `code`, the driver returns
  `correct: false` with the platform `message` instead of an error.

## Response envelopes

The driver accepts a bare challenge array, `{"challenges": [...]}`, and
`{"code": 0, "data": [...]}`. A non-zero top-level `code` is an error for
`list` and `start`, and a rejected answer for `submit`.

