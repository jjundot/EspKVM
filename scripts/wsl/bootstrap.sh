#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
if ! command -v go >/dev/null && [[ -x /opt/tools/go/bin/go ]]; then export PATH="/opt/tools/go/bin:$PATH"; fi
command -v go >/dev/null || { echo 'Install Go, then put go on PATH.' >&2; exit 1; }
source "$root/tools/versions.env"
[[ $(go env GOVERSION) == "go$GO_VERSION" ]] || { echo "Expected Go $GO_VERSION; got $(go env GOVERSION)" >&2; exit 1; }
mkdir -p "$root/build/tools/bin" "$root/build/deps"
python3 -m venv "$root/build/tools/proto"
"$root/build/tools/proto/bin/python" -m pip install -r "$root/tools/requirements-proto.txt"
GOBIN="$root/build/tools/bin" go install "google.golang.org/protobuf/cmd/protoc-gen-go@v$PROTOC_GEN_GO_VERSION"
if [[ ! -d "$root/build/deps/nanopb/.git" ]]; then
 git clone --depth 1 --branch 0.4.9.1 https://github.com/nanopb/nanopb.git "$root/build/deps/nanopb"
fi
[[ $(git -C "$root/build/deps/nanopb" rev-parse HEAD) == cad3c18ef15a663e30e3e43e3a752b66378adec1 ]] || { echo 'nanopb runtime revision mismatch' >&2; exit 1; }
printf 'export PATH="%s/build/tools/bin:%s/build/tools/proto/bin:$PATH"\n' "$root" "$root"
echo 'For firmware install ESP-IDF v5.2.3 and source its export.sh separately.'
