#!/usr/bin/env bash
set -ux
cd go
go build -race -o ../dist/bin/$1 --ldflags="-X 'main.clientType=$1'"
cd ..