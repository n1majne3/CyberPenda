# Bound navigation and Runtime Owner history projections

## Status

Accepted

## Context

One navigation request can still scan all historical Tasks, and a complete
Timeline and Transcript can make a long Task expensive to load and render.
Request count alone is not a useful bound.

## Decision

The **Project Navigation Projection** includes every Project, a fixed number of
recent Tasks per Project, the selected Task, and every Task with a live busy
Runtime. Its Task query work and response content do not grow with total
historical Task count; the formal bound is Project count times the fixed summary
size, plus active Runtime count and the selected Task. The **Runtime Owner
Workspace** initially loads and renders a recent **Runtime Owner History Window**
bounded by item count and serialized size. Older Timeline and Transcript
content remains available through backward paging.

## Consequences

Navigation cost grows with Project count and the fixed per-Project summary, not
with Task history, and an unchanged refresh does not resend the complete
projection. Runtime Owner history APIs need backward pagination, and the UI
must replace owner-local state on navigation and keep rendered history bounded.
Live tail updates do not move an operator who is reading older history; the UI
reports unseen new events until the operator returns to the tail.
