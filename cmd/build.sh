#!/usr/bin/env bash
set -xe
go build -race -o ./dist/bin/"$1" --ldflags="-X 'main.clientType=$1'"
