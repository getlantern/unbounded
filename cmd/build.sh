#!/usr/bin/env bash
set -xe

if [ "$1" = "action-mode-cli" ]; then
  binaryName="action-mode-cli"
  clientType="action-mode-cli"
  proxyMode=""
elif [ "$1" = "widget" ]; then
  binaryName="widget"
  clientType="$1"
  proxyMode="$2"
else
  binaryName="$1-$2"
  clientType="$1"
  proxyMode="$2"
fi

go build -race -o ./dist/bin/"$binaryName" --ldflags="-X 'main.clientType=$clientType' -X 'main.proxyMode=$proxyMode'"
