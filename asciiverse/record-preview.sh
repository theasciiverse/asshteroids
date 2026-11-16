#!/bin/sh
# Records the catalog preview, asciiverse/asshteroids.cast, to hub/RECORDING.md:
# 100x30, the -demo autopilot flying one ship among six computer pilots,
# then a key to end it. It is not opened to the network, so no addresses
# show. Runs asciinema in a detached tmux pane, so nothing from this
# terminal gets in; a throwaway HOME keeps the state folder untouched.
#
#   go build -o asshteroids ./cmd/asshteroids && asciiverse/record-preview.sh
set -eu
cd "$(dirname "$0")/.."
out=asciiverse/asshteroids.cast
tmp=$(mktemp -d)
trap 'tmux kill-session -t asshteroids-rec 2>/dev/null || true; rm -rf "$tmp"' EXIT
rm -f "$out"
tmux new-session -d -s asshteroids-rec -x 100 -y 30 \
  "env -i PATH=$PWD:/usr/bin:/bin HOME=$tmp USER=player LOGNAME=player \
     TERM=xterm-256color COLORTERM=truecolor LANG=C.UTF-8 \
   asciinema rec --window-size 100x30 --idle-time-limit 2 \
     --capture-env TERM,COLORTERM,LANG --title ASSHTEROIDS \
     --command 'asshteroids -demo -bots 6 -name Pilot' $out"
sleep ${SECONDS_LONG:-32}
tmux send-keys -t asshteroids-rec q
sleep 2
ls -la "$out"
