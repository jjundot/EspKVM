#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
source "$root/tools/versions.env"
[[ $(go env GOVERSION) == "go$GO_VERSION" ]]
[[ $(protoc-gen-go --version) == "protoc-gen-go v$PROTOC_GEN_GO_VERSION" ]]
[[ $(python3 -m grpc_tools.protoc --version) == "libprotoc $PROTOC_VERSION" ]]
python3 -c "import importlib.metadata as m; assert m.version('nanopb') == '$NANOPB_VERSION'"
cmake --version
ninja --version
if [[ ${1:-} == --device ]]; then
 [[ $(idf.py --version) == "ESP-IDF v$IDF_VERSION" ]]
fi
