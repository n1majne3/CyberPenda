---
name: delete-comment
description: Find code comments, classify each one, delete it, record decisions in ADRs, file issues for hacks, and tighten the comment ratchet. Use for /delete-comment, or when the comment ratchet fails.
---

# Delete comments

This repository bans code comments. Code shows what it does. ADRs in `docs/adr/` record why.
Two ratchets enforce the ban:

- Go: `TestGoCommentRatchet` in `scripts/comment_ratchet_test.go`, with the baseline `scripts/comment-baseline.tsv` (comment lines per file).
- Web: the ESLint rule `local/no-comments` in `web/eslint-rules/no-comments.js`, with the bulk suppressions in `web/eslint-suppressions.json`.

A file can have fewer comments than its baseline, never more. A new file has a baseline of zero.
Tool directives are not comments: `//go:build`, `//go:embed`, `//nolint`, `// Code generated`, `// Output:`, `eslint-disable`, `@ts-expect-error`, and `/// <reference>`.

## Scope

Use the scope that the user gives: the full codebase, a package, a directory, or a list of files.
If the user gives no scope, use the files with the most comment lines in the baselines.

## 1. Find the comments

- Go: `go run ./scripts/comment-inventory.go` prints one JSON object per comment (file, start, end, text) for every tracked Go file. To give a file list, build it first, because `go run` does not accept `_test.go` arguments: `go build -o /tmp/comment-inventory ./scripts/comment-inventory.go && /tmp/comment-inventory <files...>`.
- Web: `echo '{}' > /tmp/empty.json && (cd web && npx eslint <paths> --suppressions-location /tmp/empty.json -f json)` lists every comment, including the suppressed ones.

## 2. Classify each comment

Read the comment and the code next to it. Choose one category:

| Category | Test | Action |
| --- | --- | --- |
| Tautology | The code, a name, or a type already says it. Test step labels and section headers are in this category. | Delete it. |
| Stale | It refers to removed code, old behavior, a closed issue, or a different file layout. | Delete it. |
| Excuse | It permits a shortcut: TODO, "for now", best-effort, a swallowed error, a known gap, or a workaround. | Delete it. File one GitHub issue for each hack. |
| Decision Record | It records an important, non-obvious decision: a security boundary, an invariant, an ordering or crash-safety rule, or a constraint from an external tool. | Move it into an ADR. Then delete it. |

If a comment explains code that is hard to read, change the code first. Use a better name, a named constant, or a small function. Then delete the comment.

## 3. Move decision records

- Add the decision to the existing ADR for that area if one exists. Otherwise create `docs/adr/NNNN-<slug>.md` with the next number. Use the format of the existing ADRs: Status, Context, Decision, Consequences.
- Name the code by package and symbol, not by line number.
- Use the terms in `GLOSSARY.md`. Write in ASD-STE100 Simplified Technical English.

## 4. File issues for excuses

Use `gh issue create` (see `docs/agents/issue-tracker.md`). Give the file, the symbol, the hack, and a proposed fix. Group comments that describe the same hack into one issue. Apply the `needs-triage` label.

## 5. Delete and tighten the ratchets

1. Delete the comments. Remove the blank lines that the deletion leaves. Remove empty JSX braces `{}` that held a comment.
2. Run `gofmt -l internal/ cmd/ scripts/` and `go vet ./...`.
3. Tighten the Go baseline: `UPDATE_COMMENT_BASELINE=1 go test ./scripts -run TestGoCommentRatchet`, then `go test ./scripts -run TestGoCommentRatchet`.
4. Tighten the web suppressions: `cd web && npx eslint . --prune-suppressions`, then `npx eslint .`.
5. Run the affected tests. A deleted comment must not change behavior.

Never increase a baseline to make a check pass. Never add an `eslint-disable` for `local/no-comments`.

## 6. Report

Give the user the count for each category, the ADRs that you changed, the issues that you filed, and the comment lines that remain in the baselines.
Get the remaining count with `awk -F'\t' '{s+=$2} END {print s}' scripts/comment-baseline.tsv` and `jq '[.[][]|.count]|add' web/eslint-suppressions.json`.
