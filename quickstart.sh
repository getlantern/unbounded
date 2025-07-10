#!/usr/bin/env bash

SESSION_NAME="unbounded-sandbox"

FREDDIE_DEFAULT=http://localhost:9000
EGRESS_DEFAULT=http://localhost:8000
NETSTATE_DEFAULT=http://localhost:8080/exec

PROXYPORT_DEFAULT=1080

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

    # make panes for each command, and set the layout
    for i in "${!commands[@]}"; do
        tmux split-window
        tmux select-layout tiled
    done

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


#build binaries then return to the right directory
cd cmd 
./build.sh desktop
./build.sh widget
cd ..

# Default commands that are used in many options
# start freddie for matchmaking
freddie_start="PORT=9000 go run ./freddie/cmd/main.go"

# Start egress server
egress_start="PORT=8000 go run ./egress/cmd/egress.go"

 # Start desktop proxy for outgoing connections with websocket
desktop_start="TAG=bob NETSTATED=$NETSTATE_DEFAULT FREDDIE=$FREDDIE_DEFAULT EGRESS=$EGRESS_DEFAULT PORT=$PROXYPORT_DEFAULT ./cmd/dist/bin/desktop"

# build and start native binary widget
widget_start="TAG=alice NETSTATED=$NETSTATE_DEFAULT FREDDIE=$FREDDIE_DEFAULT EGRESS=$EGRESS_DEFAULT ./cmd/dist/bin/widget"

 # start netstate
netstate_start="cd netstate/d && UNSAFE=1 go run ."

# different sequences of commands for different run options
# 'derek' option starts a specified number of censored peers
derek_commands=(
    "$netstate_start"
    "$freddie_start"
    "$egress_start"
    "$widget_start"

    # build and start up a number of censored peers
    "cd cmd && ./build.sh desktop && TAG=bob NETSTATED=$NETSTATE_DEFAULT FREDDIE=$FREDDIE_DEFAULT EGRESS=$EGRESS_DEFAULT ./derek.sh $peers" 
)

# Starts all the pieces of unbounded locally with websockets, which can be used by 
# setting firefoxy to use '127.0.0.1:1080' for http and https 
local_commands=(
    "$netstate_start"
    "$freddie_start"
    "$egress_start"
    "$desktop_start"
    "$widget_start"
)

# Starts the UI
ui_commands=(
    "$netstate_start"
    "$freddie_start"
    "$egress_start"

    # Build web widget and start ui
    "cd cmd && ./build_web.sh && cd ../ui && yarn && cp .env.development.example .env.development && yarn dev:web"
)

# start everything except egress, useful for testing egress server without restarting the other components
# to use run an egress server separately at localhost:8000 
custom_egress_commands=(
    "$netstate_start"
    "$freddie_start"
    "$desktop_start"
    "$widget_start"
)

# Start everything for webtransports
wt_commands=(
     # start netstate
    "$netstate_start"

    # start freddie for matchmaking
    "$freddie_start"

    #start egress server pointed to keys created below
    "TLS_CERT=localhost.crt TLS_KEY=localhost.key PORT=8000 go run ./egress/cmd/egress.go"

    # Start desktop proxy with webtransports enabled, and pointed to the correct certs
    "WEBTRANSPORT=1 CA=localhost.crt SERVER_NAME=localhost EGRESS=https://localhost:8001 TAG=bob NETSTATED=$NETSTATE_DEFAULT FREDDIE=$FREDDIE_DEFAULT PORT=$PROXYPORT_DEFAULT ./cmd/dist/bin/desktop"

    # build and start native binary widget with webtransports enabled
    "WEBTRANSPORT=1 CA=localhost.crt EGRESS=https://localhost:8001 TAG=alice NETSTATED=$NETSTATE_DEFAULT FREDDIE=$FREDDIE_DEFAULT ./cmd/dist/bin/widget"
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
    echo "webtransports not supported"
    usage;
    # #create a self-signed certificate for localhost
    # openssl req -x509 -newkey rsa:2048 -nodes -keyout localhost.key -out localhost.crt -subj '/CN=localhost' -addext 'subjectAltName = DNS:localhost'
    # echo "Using webtransports option"
    # commands=("${wt_commands[@]}")
else
    echo "Unknown option $1";
    usage;
fi

create_tmux_session "$SESSION_NAME" "${commands[@]}";