#!/usr/bin/env bash
# The Derek Option
# Usage: ./derek.sh 100
# (spin up 100 censored peers)

trap "kill 0" EXIT
set -ue

for (( i=1; i <=$1; i++ ))
do
  TAG="$TAG-$i" PORT=$((1080 + $i)) dist/bin/desktop &
done

wait
