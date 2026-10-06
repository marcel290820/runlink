#!/usr/bin/env bash
# Sourced by the entry scripts. Every tool and cache stays in the selected workspace.
RUNLINK_ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
RUNLINK_TOOLS=${RUNLINK_TOOLS:-$RUNLINK_ROOT/.tools}
export RUNLINK_ROOT RUNLINK_TOOLS
# Python runs bootstrap, so bootstrap cannot pin it; enforce the documented floor.
python3 -c 'import sys; sys.exit(sys.version_info < (3, 12))' 2>/dev/null ||
  { printf 'Python 3.12 or newer is required\n' >&2; exit 1; }
export PATH="$RUNLINK_TOOLS/bin:$PATH"
export GOENV=off GOTOOLCHAIN=local GOPATH="$RUNLINK_TOOLS/gopath" GOCACHE="$RUNLINK_TOOLS/gocache" GOBIN="$RUNLINK_TOOLS/bin"
export npm_config_cache="$RUNLINK_TOOLS/npm-cache" npm_config_userconfig="$RUNLINK_TOOLS/npm-user.conf" \
  npm_config_globalconfig="$RUNLINK_TOOLS/npm-global.conf"
export PLAYWRIGHT_BROWSERS_PATH="$RUNLINK_TOOLS/browsers"
# Bootstrap extracts these on Ubuntu 26.04 amd64 for Chromium and the validators.
libraries="$RUNLINK_TOOLS/linux-libs/usr/lib/x86_64-linux-gnu"
if [[ -d $libraries ]]; then
  export LD_LIBRARY_PATH="$libraries${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
fi
