# Runtime Extensions

This directory contains trusted local runtime extension manifests loaded by
`make dev` through `-runtime-extension-dirs runtime-extensions`.

Each top-level `*.json` file is a manifest. Its `source.path` points at a local
bundle that is copied into the task-local runtime boundary when a runtime profile
enables the extension.

