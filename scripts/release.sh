#!/usr/bin/env bash
set -euo pipefail
# shellcheck source=scripts/env.sh
source "$(dirname "$0")/env.sh"
exec python3 "$RUNLINK_ROOT/scripts/release.py" "$@"
