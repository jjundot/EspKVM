#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
output=${1:-"$root/out/flash/$(date -u +%Y%m%dT%H%M%SZ)"}
python3 "$root/tools/package_device.py" "$root/out/device" "$output"
printf 'Flash package: %s\n' "$output"
