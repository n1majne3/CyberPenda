# Project all global model providers into Pi runtimes

Pi can switch provider, model, and reasoning effort natively when their configuration and credentials are already loaded. Every Pi task will therefore receive all global Model Providers that are launch-ready for Pi, together with their model configuration and configured API credentials, allowing each turn to switch without reprojection or a runtime restart. Draft or otherwise unavailable providers are skipped without blocking unrelated Pi launches. This deliberately accepts global credential exposure inside every Pi runtime instead of using task allowlists or on-demand credential injection; Codex and Claude Code continue to reproject and restart when their Model Provider changes.

## Consequences

- A Pi task's initially selected Model Provider is its starting selection, not a credential boundary.
- Every Pi runtime can read and use every global launch-ready Model Provider credential.
- Project and Runtime Profile boundaries do not limit which launch-ready global Model Provider a Pi turn may select.
- A non-launch-ready Model Provider is omitted from Pi projection and does not block other Pi tasks.
- The projected provider, model, and credential set is fixed for the lifetime of that Pi runtime; later global changes become available after its next projection and restart rather than through hot reload.
