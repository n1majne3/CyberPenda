# File size pre-commit check

Install the repository hooks with `make install-git-hooks`.
The target sets `core.hooksPath` to `.githooks`.
Git does not run the tracked hook until you install it.

Run the check directly with `bash .githooks/pre-commit`.
Run its commit-based tests with `bash scripts/check-file-size.test.sh`.

## Scope

The check reads added, copied, modified, renamed, and type-changed regular files from the Git index.
It checks the staged content, not unstaged edits in the working tree.
Deleted files, symbolic links, and unchanged files are outside its scope.
Markdown documents (`*.md`) are also outside its scope.

The estimate is bytes divided by four.
A file with 20,000 bytes passes.
A file with 20,001 bytes fails.
This estimate is not a model-specific tokenizer.

Git does not record whether an agent authored a file.
This check treats handwritten text as authored content, whether an agent or a person wrote it.
It excludes known lockfiles, generated content, vendored dependencies, build outputs, and binary assets.
The hook owns the exclusion list.
Do not exempt a handwritten contract or schema only because it is JSON.

## Split a file that fails

Group code by responsibility and name each file after its concept.
Do not create `part1` and `part2` files.
Keep the public API and behavior unchanged.
Move Go declarations into files in the same package.
Move TypeScript code into named modules and update their imports.
Split tests by behavior into separate `_test.go` or `.test.tsx` files.

Keep every resulting file at or below 20,000 bytes.
Run `gofmt` for Go changes and run the affected tests.
Stage the resulting files and run the check again.
Do not shorten code by minifying it or remove tests to meet the limit.

For an actual generated file that the hook does not recognize, use its generator's standard generated header or add a narrow exclusion with a generator reference.
Do not add a generated header to handwritten content to bypass the check.
