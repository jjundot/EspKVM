#!/usr/bin/env bash
# Keep the user's Windows checkout intact; compile a snapshot on WSL ext4.
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
work=$(mktemp -d "$HOME/esp-kvm-build.XXXXXX")
printf 'Build snapshot: %s\n' "$work"
tar -C "$root" --exclude=.git --exclude=build --exclude=ref --exclude=tmp -cf - . | tar -C "$work" -xf -
export PATH="/opt/tools/go/bin:$HOME/.local/opt/ek/proto312/bin:$HOME/.local/bin:$PATH"
cmake -S "$work" -B "$work/build/cmake/host-linux" -G Ninja -DPython3_EXECUTABLE="$HOME/.local/opt/ek/proto312/bin/python"
if [[ ${1:-} == --verify ]]; then
 bash "$work/scripts/wsl/verify-host.sh" "$work/build/cmake/host-linux"
else
 cmake --build "$work/build/cmake/host-linux" --target host test-host test-protocol
fi
