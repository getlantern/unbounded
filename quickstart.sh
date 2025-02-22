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

    # start freddie
    "cd freddie/cmd && PORT=9000 go run main.go"

    # start egress
    "cd egress/cmd && PORT=8000 go run egress.go"
    
    # start ui
    "cd ui && yarn dev:web"

    # build and start native binary desktop client
    # "cd cmd && ./build.sh desktop && cd dist/bin && FREDDIE=http://localhost:9000 EGRESS=http://localhost:8000 ./desktop"
    # "cd cmd && ./build.sh desktop && FREDDIE=http://localhost:9000 EGRESS=http://localhost:8000 ./derek.sh 2"

    # build and start native binary widget
    # "cd cmd && ./build.sh widget && cd dist/bin && FREDDIE=http://localhost:9000 EGRESS=http://localhost:8000 ./widget"

    # build browser widget
    # "cd cmd && ./build_web.sh"
)

create_tmux_session "$SESSION_NAME" "${commands[@]}"


