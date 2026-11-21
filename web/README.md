# CyberPenda web dashboard

The React dashboard for `pentestd`. Vite builds it, and `make build` embeds the build in the daemon.

## Commands

Run these commands in `web/`:

| Command | Purpose |
| --- | --- |
| `npm run dev` | Start the Vite dev server on port 5173. It sends `/api` and `/health` to the daemon on `127.0.0.1:8787`. Use `make dev` at the repository root to start both. |
| `npm run build` | Type-check and build into `dist/`. |
| `npm run build:demo` | Build the read-only demo (`VITE_DEMO_MODE=true`, `src/demo/`). Vercel uses this build (`vercel.json`). |
| `npm run lint` | Run ESLint. The `local/no-comments` rule bans code comments; `eslint-suppressions.json` is the ratchet for the remaining ones. |
| `npm run test` | Run the Vitest unit tests (jsdom). |
| `npm run test:browser` | Run the Playwright specs in `e2e/` against a Vite dev server on port 4173. |

## Layout

- `src/pages/`: one component for each route in `src/App.tsx`.
- `src/components/`: shared UI, the Workspace Sidebar, and the Runtime Owner Workspace transcript.
- `src/lib/`: the daemon API client, Blackboard v2 parsing, and Runtime Owner adapters.
- `src/demo/`: the fixture data for the demo build.

Use the terms in the repository `GLOSSARY.md`.
