#!/bin/sh
# runlink passes the inputs as arguments, as greet.json maps them, and shows
# each stderr line to the recipient as progress.
echo "Choosing words for $1" >&2
sleep 1
case $2 in
  formal) echo "Good day, $1." ;;
  *) echo "Hi $1, good to see you!" ;;
esac
