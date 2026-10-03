# Repository Guidelines

## Project Structure & Module Organization

Runlink is currently a planning-only repository. `VISION.md` defines the product, and `RATIONALE.md` explains its focus: `optional input -> task function -> output`, executed on the owner's machine for browser-only recipients.

- `architecture/README.md`: component overview, state ownership, and runtime flow.
- `architecture/adr/`: numbered architecture decision records (ADRs).
- `architecture/implementation.md`: build sequence, required proofs, and unresolved choices.

There are no application sources, tests, or asset directories yet. The planned Go layout is `cmd/runlink`, `cmd/runlink-server`, and shared `internal/` packages; browser code is planned as HTML, CSS, and vanilla JavaScript.

## Build, Test, and Development Commands

No build, test, lint, or local server commands are configured. For documentation changes:

- `git diff --check`: detect whitespace errors in tracked changes.
- `git diff -- architecture/`: review architecture edits.
- `git status --short`: inspect changed and untracked files.

Review new files separately because ordinary diffs omit untracked files. Before implementing features, follow the shared check gate planned in `architecture/implementation.md`: formatting/lint, Go vet, compilation, race-enabled tests, and both executable builds through `scripts/check.sh`.

## Style & Naming Conventions

Keep Markdown concise, with descriptive headings, relative links, and fenced code blocks. Match existing tables and Mermaid diagrams. No formatter or linter is installed.

Name ADRs `NNNN-short-title.md`, using the next number and lowercase hyphenated titles. Include Status, Context, Decision, and Consequences. Supersede changed decisions with a new linked ADR. Use `runlink` in commands and documentation.

## Testing Guidelines

No testing framework or coverage threshold is configured. Check document links, diagram consistency, and agreement between the overview, ADRs, and implementation plan. Accepted ADRs describe agreed design, not completed functionality.

For future implementation, derive behavioral and failure tests from the plan's required proofs. Report executed checks separately from planned acceptance criteria.

## Commit & Pull Request Guidelines

History uses Conventional Commits, especially `docs:`, such as `docs: add WIP architecture draft`. Keep commits focused.

PR descriptions should state the problem, changed behavior or decision, validation performed, and unresolved risks. Link relevant issues and ADRs; include screenshots for visual changes. Preserve unrelated edits and exclude credentials, recipient secrets, and private task data.
