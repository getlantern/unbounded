#!/usr/bin/env bash

cleanup() {
    echo
    echo "Interrupt received, shutting down tmux server..."
    tmux kill-server
    exit 0
}

trap cleanup SIGINT

runtime=0
while true; do
    echo -ne "\r(${runtime}s) Running tmux session, interrupt (ctrl + c) to kill all..."
    sleep 1
    runtime=$((runtime + 1))
done