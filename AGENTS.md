@CONTEXT.md

Always read CONTEXT.md, unconditionally.
Use TDD

## Workspace file safety

Treat every existing workspace file and directory as user-owned, including untracked content. Get the user's explicit authorization before deleting, overwriting, restoring, or cleaning it. A path name or `git status` state is not proof that content is a test artifact. To identify generated content, use a before-and-after workspace snapshot or a documented generator contract. If an accidental destructive change occurs, stop other work, disclose the exact action, and prioritize recovery.

Update `CONTEXT.md` when the user explicitly resolves a domain ambiguity.
Always talk in ASD-STE100 Simplified Technical English(or Chines if user talks in Chinese). Always read CONTEXT.md files, and use their ubiquitous language.
## Agent skills

### Issue tracker

GitHub Issues for this repo (`n1majne3/CyberPenda`), via the `gh` CLI. External PRs are not a triage surface. See `docs/agents/issue-tracker.md`.

### Triage labels

Five canonical roles mapped to their default label strings (all five now exist in the repo). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `CONTEXT.md` + `docs/adr/` at the repo root. See `docs/agents/domain.md`.
