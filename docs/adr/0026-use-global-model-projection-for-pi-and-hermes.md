# Use Global Model Projection for Pi and Hermes

Pi and Hermes can switch provider, model, and reasoning effort natively when configuration and credentials are already loaded. Every Pi or Hermes task therefore receives every global Model Provider that is launch-ready for that Runtime Plugin, together with model configuration and API credentials. Draft or otherwise unavailable providers are skipped without blocking unrelated launches. This accepts global credential exposure inside every Pi and Hermes Runtime, including Host Runner, instead of task allowlists or on-demand injection. Codex and Claude Code still reproject and restart when their Model Provider changes.

This supersedes ADR 0015, which stated the same policy for Pi only.

## Consequences

- The initially selected Model Provider is a starting selection, not a credential boundary.
- Every Pi or Hermes Runtime can read every global launch-ready Model Provider credential.
- Project and Runtime Profile boundaries do not reduce that credential set.
- A non-launch-ready Model Provider is omitted and does not block other tasks unless it is the initial selection.
- The projected set is fixed for that Runtime lifetime; later global changes apply after the next projection and restart.
