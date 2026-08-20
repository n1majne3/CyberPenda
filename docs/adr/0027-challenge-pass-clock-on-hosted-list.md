# Challenge Pass Clock on hosted list, adapters without rebuild

The hosted Runtime forgot first-pass budgets after compact. Official TSecBench `list` has no `started_at`. Appendix-only clocks fail.

The Hosted Challenge Client owns a Runtime Workdir clock file and annotates `list` stdout with `elapsed_min`, `budget_min`, `over_budget`, and `attempt_n`. The Runtime still decides abandon or close. Blackboard stays semantic handoff.

Challenge Platform HTTP details live in JSON adapters. Baked `tsecbench` is the default. Overlay `/data/adapters/<id>.json` plus `CYBERPENDA_CHALLENGE_ADAPTER` selects another platform without rebuilding the image.

Rejected: Hosted Controller scheduling; Blackboard timestamps; replacing the one-command Client with the official Python SDK for-loop; baking a new client binary per competition.
