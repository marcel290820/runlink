#!/usr/bin/env bash
# Run the app and the trusted frontend on loopback until Ctrl-C or either exits.
set -euo pipefail
# shellcheck source=scripts/env.sh
source "$(dirname "$0")/env.sh"
"$RUNLINK_ROOT/scripts/build.sh"
cd "$RUNLINK_ROOT"
mkdir -p var/app
chmod 700 var/app
app_pid='' frontend_pid=''
# shellcheck disable=SC2329 # Invoked by the EXIT trap.
stop_all() {
  [[ -z $app_pid ]] || kill "$app_pid" 2>/dev/null || true
  [[ -z $frontend_pid ]] || kill "$frontend_pid" 2>/dev/null || true
  wait || true
}
trap stop_all EXIT
trap 'exit 0' INT TERM
build/runlink-server --listen 127.0.0.1:8081 --state-dir var/app &
app_pid=$!
build/runlink-server --role frontend --listen 127.0.0.1:8080 --upstream http://127.0.0.1:8081 &
frontend_pid=$!
printf 'Runlink loader: http://127.0.0.1:8080\n'
# Bash 3.2 on macOS has no wait -n.
while kill -0 "$app_pid" 2>/dev/null && kill -0 "$frontend_pid" 2>/dev/null; do
  sleep 0.25
done
exit 1
