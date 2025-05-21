#!/usr/bin/env bash

SESSION_NAME="unbounded-sandbox"

FREDDIE_DEFAULT=http://localhost:9000
EGRESS_DEFAULT=http://localhost:8000
NETSTATE_DEFAULT=http://localhost:8080/exec

PROXYPORT_DEFAULT=1080

# command to generate a self-signed localhost certificate for webtransports
# openssl req -x509 -newkey rsa:2048 -nodes -keyout localhost.key -out localhost.crt -subj '/CN=localhost' -addext "subjectAltName = DNS:localhost"

usage () {
    echo "Usage: $0 run_type [peers]"
    echo "  options:"
    echo "    run_type: local | ui | derek | egress" 
    echo "    peers: number of peers (only for derek), default is 2"
    echo "    example:"
    echo "      $0 local"
    echo "      $0 ui"
    echo "      $0 derek 5"
exit 1
}

if [ $# -ne 1 ]; then
   usage;
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

declare peers
default_peers=2 
if [ "$1" == "derek" ]; then
    if [ -z "$2" ]; then
        echo "no number of peers specified, using default $default_peers"
        peers=$default_peers
    else 
       peers=$2
        echo "Using $peers peers"
    fi
fi

# different sequences of commands for different 
# 'derek' option starts a specified number of censored peers
derek_commands=(
    # start freddie for matchmaking
    "PORT=9000 go run ./freddie/cmd"

    # start egress
    "PORT=8000 go run ./egress/cmd"

    # build and start up a number of censored peers
    "cd cmd && ./build.sh desktop && TAG=bob NETSTATED=$NETSTATE_DEFAULT FREDDIE=$FREDDIE_DEFAULT EGRESS=$EGRESS_DEFAULT ./derek.sh $peers"

    # build and start native binary widget # build and start native binary widget
    "cd cmd && ./build.sh widget && cd dist/bin && TAG=alice NETSTATED=$NETSTATE_DEFAULT FREDDIE=$FREDDIE_DEFAULT EGRESS=$EGRESS_DEFAULT ./widget"

    # start netstate
    "cd netstate/d && UNSAFE=1 go run ."
)

# Starts all the pieces of unbounded locally, which can be used by 
# setting firefoxy to use '127.0.0.1:1080' for http and https 
local_commands=(
    # start freddie for matchmaking
    "PORT=9000 go run ./freddie/cmd"

    # start egress
    "PORT=8000 go run ./egress/cmd"

    # Start desktop proxy for outgoing connections
    "cd cmd && ./build.sh desktop && cd dist/bin && TAG=bob NETSTATED=$NETSTATE_DEFAULT FREDDIE=$FREDDIE_DEFAULT EGRESS=$EGRESS_DEFAULT PORT=$PROXYPORT_DEFAULT ./desktop"

    # build and start native binary widget
    "cd cmd && ./build.sh widget && cd dist/bin && TAG=alice NETSTATED=$NETSTATE_DEFAULT FREDDIE=$FREDDIE_DEFAULT EGRESS=$EGRESS_DEFAULT ./widget"

    # start netstate
    "cd netstate/d && UNSAFE=1 go run ."
)

# Starts the UI
ui_commands=(
    # start freddie for matchmaking
    "PORT=9000 go run ./freddie/cmd"

    # start egress
    "PORT=8000 go run ./egress/cmd"

    # Build web widget and start ui
    "cd cmd && ./build_web.sh && cd ../ui && yarn && cp .env.development.example .env.development && yarn dev:web"

    # start netstate
    "cd netstate/d && UNSAFE=1 go run ."
)

# start everything except egress, useful for testing egress server without restarting the other components
# to use run an egress server separately at localhost:8000 
custom_egress_commands=(
    # start freddie for matchmaking
    "PORT=9000 go run ./freddie/cmd"

    # Start desktop proxy for outgoing connections: set firefox to use '127.0.0.1:PROXYPORT_DEFAULT' for http and https
    "cd cmd && ./build.sh desktop && cd dist/bin && TAG=bob NETSTATED=$NETSTATE_DEFAULT FREDDIE=$FREDDIE_DEFAULT EGRESS=$EGRESS_DEFAULT PORT=$PROXYPORT_DEFAULT ./desktop"

    # build and start native binary widget
    "cd cmd && ./build.sh widget && cd dist/bin && TAG=alice NETSTATED=$NETSTATE_DEFAULT FREDDIE=$FREDDIE_DEFAULT EGRESS=$EGRESS_DEFAULT ./widget"

    # start netstate
    "cd netstate/d && UNSAFE=1 go run ."
)

# Start everything for webtransports
wt_commands=(
    # start freddie for matchmaking
    "PORT=9000 go run ./freddie/cmd"

    "TLS_CERT=localhost.crt TLS_KEY=localhost.key PORT=8000 go run ./egress/cmd/egress.go 2>&1 | tee -a egress.txt"

    # Start desktop proxy for outgoing connections: set firefox to use '127.0.0.1:PROXYPORT_DEFAULT' for http and https
    "cd cmd && ./build.sh desktop && cd dist/bin && CA=../../../localhost.crt SERVER_NAME=localhost TAG=bob NETSTATED=$NETSTATE_DEFAULT FREDDIE=$FREDDIE_DEFAULT EGRESS=https://localhost:8001 PORT=$PROXYPORT_DEFAULT WEBTRANSPORT=1 ./desktop"

    # build and start native binary widget
    "cd cmd && ./build.sh widget && cd dist/bin && CA=../../../localhost.crt TAG=alice NETSTATED=$NETSTATE_DEFAULT FREDDIE=$FREDDIE_DEFAULT EGRESS=https://localhost:8001 WEBTRANSPORT=1 ./widget"

    # start netstate
    "cd netstate/d && UNSAFE=1 go run ."
)


if [ "$1" == "derek" ]; then
    echo "Using 'derek' option"
    commands=("${derek_commands[@]}")
elif [ "$1" == "local" ]; then
    echo "Using local option"
    commands=("${local_commands[@]}")
elif [ "$1" == "ui" ]; then
    echo "Using ui option"
    commands=("${ui_commands[@]}")
elif [ "$1" == "egress" ]; then
    echo "Using custom egress option"
    commands=("${custom_egress_commands[@]}")
elif [ "$1" == "wt" ]; then
    #create a self-signed certificate for localhost
    openssl req -x509 -newkey rsa:2048 -nodes -keyout localhost.key -out localhost.crt -subj '/CN=localhost' -addext 'subjectAltName = DNS:localhost'
    echo "Using webtransports option"
    commands=("${wt_commands[@]}")
else
    echo "Unknown option $1";
    usage;
fi

create_tmux_session "$SESSION_NAME" "${commands[@]}";