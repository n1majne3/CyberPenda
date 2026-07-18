---
name: scoreboard-driven-web-challenge
description: Drives black-box web challenge solving from a live scoreboard. Use when a task asks to solve many web app challenges, CTF-style objectives, Juice Shop scoreboard items, or other scoreboard-backed labs.
---

<objective>
Solve scoreboard-backed black-box web challenges by keeping the scoreboard as the source of truth. This skill prevents broad, unfocused security testing by forcing each action to map to a named remaining challenge, a proof signal, and durable progress state.
</objective>

<quick_start>
1. Open the scoreboard and record `total`, `solved`, and the visible remaining challenge names.
2. Create or update `progress:scoreboard` with solved/remaining counts and the next target set.
3. Work in small batches: choose 3-5 remaining challenges with shared evidence, endpoint, or vulnerability class.
4. For each challenge, run the smallest black-box probe that can change scoreboard state.
5. Re-read the scoreboard after each solve attempt and persist the delta before moving on.
</quick_start>

<black_box_boundary>
Allowed:
- Inspect HTTP responses, browser DOM, API traffic, cookies, local/session storage, source maps, and deployed frontend bundles such as `main.js` or `chunk-*.js`.
- Use strings and client-side routes from deployed assets as black-box observations.
- Automate browser/API actions against in-scope targets.

Forbidden unless the task scope explicitly allows it:
- Pulling or reading the target application's source repository.
- Reading online write-ups, solution lists, challenge answer keys, or upstream test fixtures.
- Treating a minified frontend bundle as server source code. It is client evidence, not authorization to inspect backend internals.
</black_box_boundary>

<workflow>
1. **Scoreboard baseline**: Capture the current scoreboard state. If only counts are visible, record counts and any visible challenge names. If categories are visible, group remaining challenges by category.
2. **Evidence map**: Build a map of endpoints, forms, routes, client hints, auth roles, and known accounts. Store it as `api:*`, `route:*`, `credential:*`, or `progress:*` facts when a trusted MCP is available.
3. **Batch selection**: Pick a narrow batch with one shared mechanism, for example login flaws, basket/order APIs, redirects, upload handling, XSS, JWT, or privacy endpoints.
4. **Probe loop**: For each candidate, run one bounded probe, observe HTTP/UI output, then refresh the scoreboard. Do not keep brute-forcing a candidate after 2-3 non-progress attempts; mark the hypothesis and move to the next batch.
5. **Persistence checkpoint**: After each solved challenge or failed batch, update `progress:scoreboard` and a `progress:<challenge-or-batch>` fact with attempted payloads, endpoint, result, and next action.
6. **Evidence capture**: Attach evidence for solved challenges when the proof is useful for resumption: HTTP request/response, screenshot, command output, or exact payload.
7. **Continuation summary**: Before stopping or handing off, submit a task summary with solved count, unsolved count, solved names if known, failed hypotheses, and the next 3 targets.
</workflow>

<challenge_prioritization>
Prefer challenges with:
- Direct scoreboard clue to an observed route, API, header, cookie, or frontend string.
- One-request validation and low state damage.
- Shared setup with already solved challenges, such as same account/session/cart.
- Client bundle hints that identify an endpoint or parameter name.

Defer challenges that require:
- Long brute-force runs, large wordlists, noisy scanning, or high request volume.
- Destructive account/data changes without a reset plan.
- External infrastructure beyond the task sandbox, unless scope and approvals allow it.
</challenge_prioritization>

<state_contract>
When trusted MCP tools are available, use them:
- `blackboard_change` for durable progress, API, route, credential, Fact, and Finding records.
- Create Finding records only when the task needs vulnerability records, not for every challenge clue.
- `blackboard_retain_evidence` for reproducible proof or screenshots worth preserving.
- Finish the bound Blackboard Continuation after every Attempt is terminal.

No scoreboard-driven run is complete if the solved count changed but durable progress state was not updated.
</state_contract>

<anti_patterns>
- Solving from memory of a known lab instead of evidence from the live target.
- Reading online solutions or the target source tree after a black-box-only instruction.
- Running generic scanners before mapping the scoreboard and target routes.
- Repeating the same failed payload family without a new observation.
- Reporting progress only in chat while leaving facts, evidence, and task summary empty.
</anti_patterns>

<success_criteria>
- Scoreboard count and remaining target set are known or explicitly marked as not visible.
- Each attempted challenge has an endpoint/action, payload or UI step, observed result, and next decision.
- Solves are verified by a fresh scoreboard read, not inferred from a 200 response.
- Durable progress facts and task summary are updated before stopping.
- Black-box boundaries are respected.
</success_criteria>
