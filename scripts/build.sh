#!/usr/bin/env bash
set -euo pipefail
# shellcheck source=scripts/env.sh
source "$(dirname "$0")/env.sh"
cd "$RUNLINK_ROOT"
mkdir -p build
go build -trimpath -o build/ ./cmd/...
