# CyberPenda

CyberPenda is a local-first pentest agent for coordinating authorized security testing work inside a project.

The app combines a Go daemon, a React dashboard, sandboxed runtime launch support, project scope controls, approvals, a blackboard for facts and findings, evidence tracking, and report generation.

## Documentation

- [Product docs](docs/README.md)
- [Domain glossary](CONTEXT.md)

## Development

Run the backend and frontend together:

```sh
make dev
```

Build the embedded web UI:

```sh
make build-ui
```
