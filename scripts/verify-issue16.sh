#!/bin/sh

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

go test -race -count=5 \
	./internal/adapter \
	./internal/event \
	./internal/agent \
	./internal/pty \
	./internal/session \
	./internal/api \
	./internal/client \
	./internal/config \
	./internal/daemon \
	./cmd/drove

go test -race ./...
go vet ./...
make build

if [ ! -d web/node_modules ]; then
	npm --prefix web ci
fi
npm --prefix web run typecheck
npm --prefix web run build

git diff --check
