#!/usr/bin/env bash
set -xe

if [ "$1" = "widget" ]; then
  binaryName="widget"
else
  binaryName="$1-$2"
fi

go build -race -o ./dist/bin/"$binaryName" --ldflags="-X 'main.clientType=$1' -X 'main.proxyMode=$2'"
