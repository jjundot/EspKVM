#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
output=${1:-"$root/build/device/flash/firmware/$(date -u +%Y%m%dT%H%M%SZ)"}
python3 "$root/tools/package_device.py" "$root/build/device/firmware" "$output"
printf 'Flash package: %s\n' "$output"
