# CyberPenda verification map

Read this index, then the feature file for the behavior under test. These recipes cover the real Web UI and daemon. The API is a second, read-only view of persisted state, not a replacement for UI actions.

## Baseline preconditions

- Follow [the skill](../SKILL.md) to start an isolated instance and pass Doctor.
- Use its `ab` browser command, `$UI` address, and `$EVIDENCE` directory.
- Start with empty application data. The daemon creates its schema and Built-in Skills. No Model Provider, Project, Task, or Session is assumed.
- Use only the browser and instance created by this run. Keep Host Browser disabled.
- The fixtures use `verify.invalid` and a clearly fake API key. They authorize no security testing and cannot prove Runtime execution.

## Driving conventions

- Use role/name, label, or the listed stable IDs. Take a fresh snapshot after each page change before using a snapshot ref.
- Start each feature from its stated preconditions. For Project pages, create `Verification Project` with [Projects and Scope](projects-scope.md), open its dashboard, and set `PROJECT_URL=$(ab get url)`.
- For duplicate controls, use the listed landmark selector rather than an arbitrary first match.
- Drive every listed entry point relevant to the change. A working direct URL does not prove that the navigation link works.
- Use `ab eval` only for observation, such as `fetch` with the default GET method. Mutations go through the UI.

## Proof and failure reporting

- Record the feature ID and entry point, the commands, and their exit codes.
- Save a before-action snapshot, the submitted form or action, the result snapshot, and a screenshot. Inspect the screenshot.
- For mutations, reload or reopen the UI and save a read-only API response showing the stored value. A success notice alone is insufficient.
- Save `ab errors`, console output, and relevant network observations. A page that renders while its API calls fail is not a pass.
- Each feature file is a source-grounded recipe. It is not a claim that the feature was executed. The run report lists the feature IDs actually proved.
- A missing precondition is a blocked journey. Record the exact missing state and the attempted action. Do not replace a Runtime launch with an empty-page check and call it verified.
- Existing `web/e2e` browser tests use API route mocks. They are useful regression tests, but their results are not real-daemon persistence proof.
- Keep proof artifacts after instance cleanup. Do not delete application records individually as a substitute for isolated-instance cleanup.

## Features

- [Projects and Scope](projects-scope.md): create and cancel, explicit Project Kind, dashboard navigation, Scope save and persistence.
- [Model Providers](model-providers.md): offline manual configuration, Model Catalog, saved limits, and search.
- [Skills](skills.md): Built-in Skill inspection, search, and one reversible Global Skill Opt-Out.
- [Task Launch](task-launch.md): entry points, Task Type checks, Runtime Launch Controls, and a gated real Runtime launch.
- [Non-Project Sessions](sessions.md): launch entry points, archived views, and gated Session creation and lifecycle actions.
- [真实 LLM Steer 生存性](steering.md)：区分请求接受、派发结算、模型响应和后续输入能力；分别记录 Pi 原生 Steer 与 Claude Code 中断替换。

Other surfaces are outside this seed map: CLI Fallback, MCP Project Interface, Hosted evaluation, and detailed Blackboard, Evidence, and Report journeys. Add their feature recipes when a change requires them.
