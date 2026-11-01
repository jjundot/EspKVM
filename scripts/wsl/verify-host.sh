#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
build=${1:-"$root/out/build/host-linux"}
cmake --build "$build" --target host test-host test-protocol
cmake --build "$build" --target proto
before=$(stat -c %Y "$root/sources/host/internal/protocol/pb/ek.pb.go")
touch "$root/sources/protocol/ek.options"
cmake --build "$build" --target proto
[[ $(stat -c %Y "$root/sources/host/internal/protocol/pb/ek.pb.go") == "$before" ]]
cmake --build "$build" --target proto | tee "$build/no-change.log"
! grep -q 'Generating' "$build/no-change.log"
cd "$root/sources/host"
go vet ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o "$root/out/bin/windows/" ./cmd/...
go test ./internal/protocol -run '^$' -fuzz FuzzSerial -fuzztime 5s
