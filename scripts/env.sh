#!/usr/bin/env bash
# Source from entry scripts; every cache and tool stays in the selected workspace.
RUNLINK_ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
RUNLINK_TOOLS=${RUNLINK_TOOLS:-$RUNLINK_ROOT/.tools}
export RUNLINK_ROOT RUNLINK_TOOLS
export PATH="$RUNLINK_TOOLS/bin:$PATH"
export GOENV=off GOTOOLCHAIN=local GOPATH="$RUNLINK_TOOLS/gopath" GOCACHE="$RUNLINK_TOOLS/gocache"
export GOBIN="$RUNLINK_TOOLS/bin" npm_config_cache="$RUNLINK_TOOLS/npm-cache"
export npm_config_userconfig="$RUNLINK_TOOLS/npm-user.conf" npm_config_globalconfig="$RUNLINK_TOOLS/npm-global.conf"
export PLAYWRIGHT_BROWSERS_PATH="$RUNLINK_TOOLS/browsers"
if [[ -d "$RUNLINK_TOOLS/linux-libs/usr/lib/x86_64-linux-gnu" ]]; then
  export LD_LIBRARY_PATH="$RUNLINK_TOOLS/linux-libs/usr/lib/x86_64-linux-gnu${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
fi
