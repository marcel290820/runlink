#!/bin/sh
# runlink stores the recipient's files and passes the order, then each file's
# path, as arguments, as sort.json maps them, then offers stdout as a download.
set -eu
order=$1
shift
lines=$(cat -- "$@" | wc -l)
echo "Sorting $((lines)) lines from $# files" >&2
case $order in
  descending) LC_ALL=C sort -r -- "$@" ;;
  *) LC_ALL=C sort -- "$@" ;;
esac
