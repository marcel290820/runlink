#!/usr/bin/env bash
# Run the whole product on loopback until Ctrl-C: the frontend (A), the app (B),
# and a TURN stand-in (C). Arguments go to runlink-dev; see runlink-dev --help.
set -euo pipefail
# shellcheck source=scripts/env.sh
source "$(dirname "$0")/env.sh"
cd "$RUNLINK_ROOT"
# Regenerate the embedded browser assets so edits to their sources take effect.
python3 scripts/assets.py
scripts/build.sh
exec build/runlink-dev "$@"
