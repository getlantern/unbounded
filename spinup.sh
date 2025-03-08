#!/usr/bin/env bash

SESSION_NAME="unbounded-sandbox"
# FREDDIE_DEFAULT=9000
# PORT_EGRESS_DEFAULT=8000

# if [ -z "$FREDDIE" ]; then
#     echo "PORT_FREDDIE is not set. Defaulting to $FREDDIE_DEFAULT."
#     exit 1
# fi
# FREDDIE=9000

# if [ -z "$PORT_EGRESS" ]; then
#     echo "PORT_EGRESS is not set. Defaulting to $PORT_EGRESS_DEFAULT."
#     exit 1
# fi

create_tmux_session() {
    local session_name=$1
    shift
    local commands=("$@")

    tmux new-session -d -s "$session_name"
    
    # Split the window into four panes
    tmux split-window -h
    tmux split-window -v
    tmux select-pane -t 0
    tmux split-window -v

    # split four panes
    # TODO more?
    # Send commands to each pane
    for i in "${!commands[@]}"; do
        tmux select-pane -t "$i"
        tmux send-keys "${commands[$i]}" C-m
    done

    # Attach to the tmux session
    tmux attach-session -t "$session_name"
}

commands=(
    # build and start native binary desktop client
    "cd cmd && ./build.sh desktop && cd dist/bin && FREDDIE=http://localhost:9000 EGRESS=http://localhost:8000 ./desktop"

    # build and start native binary widget
    "cd cmd && ./build.sh widget && cd dist/bin && FREDDIE=http://localhost:9000 EGRESS=http://localhost:8000 ./widget"

    # build browser widget
    # "cd cmd && ./build_web.sh"

    # start freddie
    "cd freddie/cmd && PORT=9000 go run main.go"

    # start egress
    "cd egress/cmd && PORT=8000 go run egress.go"
)

create_tmux_session "$SESSION_NAME" "${commands[@]}"
