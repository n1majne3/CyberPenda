# Challenge Platform adapters

Put `<id>.json` in `/data/adapters/` and set `CYBERPENDA_CHALLENGE_ADAPTER=<id>`.
That overlay is read at process start. The image does not need a rebuild.

Baked default: `/opt/cyberpenda/adapters/tsecbench.json`.

Copy `internal/challengeadapter/adapters/tsecbench.json` and change
`base_url_env`, `token_env`, `token_header`, `path`, and `query`/`json`
templates (`{{code}}`, `{{candidate}}`).
