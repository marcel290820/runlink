#!/usr/bin/env bash
# The shared local and CI gate; docs/development.md describes each step.
set -euo pipefail
# shellcheck source=scripts/env.sh
source "$(dirname "$0")/env.sh"
cd "$RUNLINK_ROOT"

[[ $(go version) == 'go version go1.27.1 '* ]] || { printf 'Run scripts/bootstrap.sh for pinned Go\n' >&2; exit 1; }
[[ $(node --version) == v24.21.0 ]] || { printf 'Run scripts/bootstrap.sh for pinned Node\n' >&2; exit 1; }

# Sources: generated assets, docs, formatting, lint, and syntax.
python3 scripts/assets.py --check
python3 scripts/docs-check.py
unformatted=$(gofmt -l cmd internal)
[[ -z $unformatted ]] || { printf 'Run gofmt on:\n%s\n' "$unformatted" >&2; exit 1; }
shellcheck scripts/*.sh
for script in scripts/*.mjs internal/frontend/*.mjs internal/frontend/assets/*.mjs internal/frontend/assets/*.js; do
  node --check "$script"
done
python3 -m compileall -q scripts infra/templates/configure-turn.py
node scripts/config-check.mjs
actionlint

# Go: vet, race-enabled tests, both executables.
go vet ./...
go test -race -count=1 ./...
scripts/build.sh

# Behavior of the built artifacts and rendered configuration.
python3 scripts/lifecycle.py
node scripts/browser.test.mjs
python3 scripts/infra-check.py
python3 scripts/turn-check.py
python3 scripts/release-test.py

# Advisories and whitespace.
govulncheck ./...
npm audit --audit-level=high
git diff --check
printf 'Runlink shared checks passed\n'
