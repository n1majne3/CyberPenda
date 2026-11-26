# Pi Steering replaces the in-flight Turn

## Status

Accepted

## Context

Pi 1.6.1 accepts a native steer command while a Runtime Turn is active, but its agent core puts that message in a steering queue. The loop reads the queue only before the next model request. It does not interrupt the model request that is already streaming. In a Turn with one assistant response and no tool step, the original response can therefore finish before the steering text enters the Pi conversation. The macOS Qwen corpus showed this order for one steer and for two consecutive steers.

An operator-visible Steer action that only reaches a later step is not honest when CyberPenda presents it as a change to the running Turn.

## Decision

For Pi Runtime Owners, native Harness Steering uses interrupt-then-replace when a Work Runtime Turn is active. The Harness aborts the active Pi generation, waits for its settlement, and starts a replacement Turn with the operator message in the same native Pi session. Pi's queue-based steer command remains provider input plumbing, but CyberPenda does not select it as the default operator Steer action.

This decision does not change Codex or Claude Code steering. Provider selection changes and explicit force-replace still use interrupt-then-replace. A Turn that is already idle still uses send-turn.

## Consequences

The operator sees the interrupt-and-send semantics that the control performs. The original Pi generation can stop before completion, and the replacement Turn becomes a new Runtime Continuation. Accepted Steering keeps the same durable request, fencing, settlement, and restart rules.

Pi cannot redirect tokens that are already inside one in-flight model request. CyberPenda can only abort that request and replace it. This is the supported meaning of operator Steer for Pi until Pi exposes a true in-flight interruption or mutation contract.
