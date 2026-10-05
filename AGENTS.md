# Repository Guidelines

## Project Structure & Module Organization

Runlink has a development and shipping scaffold; product task protocols remain planned. `VISION.md` defines the product, and `RATIONALE.md` explains its focus: `optional input -> task function -> output`, executed on the owner's machine for browser-only recipients.

- `architecture/README.md`: component overview, state ownership, and runtime flow.
- `architecture/adr/`: numbered architecture decision records (ADRs).
- `architecture/implementation.md`: build sequence, required proofs, and unresolved choices.

The Go layout is `cmd/runlink`, `cmd/runlink-server`, and shared `internal/` packages. Browser assets live in `internal/frontend/assets` as HTML, CSS, and vanilla JavaScript. `scripts/` owns the shared gate and release tooling; `infra/` contains OpenTofu, mocked tests, and cloud-init templates. `docs/` records actual development commands, operational steps, and verification limits.

## Build, Test, and Development Commands

Run `scripts/bootstrap.sh`, `scripts/build.sh`, `scripts/check.sh`, and `scripts/dev.sh` as documented in README.md. Tools and caches remain in the workspace `.tools/` or an explicitly selected permitted `RUNLINK_TOOLS` location. For documentation changes:

- `git diff --check`: detect whitespace errors in tracked changes.
- `git diff -- architecture/`: review architecture edits.
- `git status --short`: inspect changed and untracked files.

Review new files separately because ordinary diffs omit untracked files. Before implementing features, follow the shared check gate in `scripts/check.sh`: formatting/lint, Go vet, compilation, race-enabled tests, and both executable builds through `scripts/check.sh`.

## Style & Naming Conventions

Keep Markdown concise, with descriptive headings, relative links, and fenced code blocks. Match existing tables and Mermaid diagrams. Use pinned workspace formatters/linters through the shared gate.

Name ADRs `NNNN-short-title.md`, using the next number and lowercase hyphenated titles. Include Status, Context, Decision, and Consequences. Supersede changed decisions with a new linked ADR. Use `runlink` in commands and documentation.

## Testing Guidelines

Go behavior tests, Chromium checks, release fixture checks, and mocked infrastructure tests are configured without a coverage threshold. Check document links, diagram consistency, and agreement between the overview, ADRs, and implementation plan. Accepted ADRs describe agreed design, not completed functionality.

For future implementation, derive behavioral and failure tests from the plan's required proofs. Report executed checks separately from planned acceptance criteria.

## Commit & Pull Request Guidelines

History uses Conventional Commits, especially `docs:`, such as `docs: add WIP architecture draft`. Keep commits focused.

PR descriptions should state the problem, changed behavior or decision, validation performed, and unresolved risks. Link relevant issues and ADRs; include screenshots for visual changes. Preserve unrelated edits and exclude credentials, recipient secrets, and private task data.
