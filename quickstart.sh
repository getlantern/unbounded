#!/usr/bin/env bash

SESSION_NAME="unbounded-sandbox"
# FREDDIE_DEFAULT=9000
# PORT_EGRESS_DEFAULT=8000

declare peers
default_peers=2
if [ -z "$1" ]; then
    echo "No quantity of peers specified. Assuming default: $default_peers"
    peers=$default_peers
else
    peers=$1
fi

create_tmux_session() {
    local session_name=$1
    shift
    local commands=("$@")

    tmux new-session -d -s "$session_name"
    
    tmux split-window -h
    tmux split-window -v
    tmux split-window -v
    tmux select-pane -t 1
    tmux split-window -v
    tmux select-pane -t 0

    # Send commands to each pane
    for i in "${!commands[@]}"; do
        tmux select-pane -t $(( i + 1 ))
        tmux send-keys "${commands[$i]}" C-m
    done

    tmux select-pane -t 0
    chmod +x quickstop.sh
    tmux send-keys "./quickstop.sh" C-m

    # Attach to the tmux session
    tmux attach-session -t "$session_name"
}

commands=(

    # start freddie for matchmaking
    "cd freddie/cmd && PORT=9000 go run main.go"

    # start egress
    "cd egress/cmd && PORT=8000 go run egress.go"
    
    # start ui in hot reload
    "cd ui && yarn dev:web"

    # build and start up a number of censored peers
    "cd cmd && ./build.sh desktop && FREDDIE=http://localhost:9000 EGRESS=http://localhost:8000 ./derek.sh $peers"

    # build and start native binary widget
    # "cd cmd && ./build.sh widget && cd dist/bin && FREDDIE=http://localhost:9000 EGRESS=http://localhost:8000 ./widget"

    # build browser widget
    # "cd cmd && ./build_web.sh"
)

create_tmux_session "$SESSION_NAME" "${commands[@]}"


