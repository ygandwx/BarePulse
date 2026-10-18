#!/bin/sh
set -eu
: "${TEST_DATABASE_URL:?TEST_DATABASE_URL required; do not silently skip integration tests}"
test -z "$(gofmt -l backend frontend)"
go vet ./...
go test -race ./... -count=1
go build -trimpath -o bin/barepulse ./backend/cmd/barepulse
for f in frontend/*.js; do node --check "$f"; done
