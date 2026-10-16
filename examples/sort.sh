#!/bin/sh
# runlink stores the recipient's file and passes its path as an argument, as
# sort.json maps it, then offers stdout as a download.
set -eu
lines=$(wc -l < "$1")
echo "Sorting $((lines)) lines" >&2
case $2 in
  descending) LC_ALL=C sort -r -- "$1" ;;
  *) LC_ALL=C sort -- "$1" ;;
esac
