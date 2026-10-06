#!/usr/bin/env bash
# Agent stop and commit hooks look here; CI and developers run scripts/check.sh directly.
exec "$(dirname "$0")/../scripts/check.sh" "$@"
