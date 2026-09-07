# Page Subagent Conversation Blocks independently

## Status

Accepted. Issue #272.

## Context

A Runtime Turn can last hours. Aggregating child items only inside the current
main Transcript window makes complete child history depend on client state.
Reading all owner Events on every poll would violate ADR-0024.

## Decision

The main Transcript returns a Subagent Conversation Block summary and an
owner-authorized history reference. The child has independent recent,
backward, and live pages bounded by item count and serialized size. Its visual
anchor remains the spawning tool call; update progress is a separate cursor.

New source Events update a durable child read model in the same transaction.
Stable item positions serve backward paging; immutable change cursors serve
live reads and exact version detail. Reads seek into owner/child indexes.
Source Event boundaries support consistent Hosted Transcript export while
new output arrives. Raw reasoning patches are stored once, with bounded
cumulative previews; full single-item detail reconstructs only that item.

This is forward-only. No migration replays old child output. Existing rows
without the new read model retain their prior projection. New indexed stream
wrappers preserve child reasoning identity without changing old replay.

## Consequences

Task and Session use the same child read model and page contract. The Web UI
keeps one bounded child window and reports new output while the operator reads
older content. Hosted export follows the child reference with a fixed source
boundary and emits bounded batches, retaining raw incremental reasoning and
exact credential masking.

The read model adds durable storage and write-time parsing. Source Events and
their child projection commit or roll back together, so a read does not need
recovery replay after restart. A one-time write-side adapter-context lookup
supports an owner that was already live when the software was upgraded.
