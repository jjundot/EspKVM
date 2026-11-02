#!/usr/bin/env bash
set -euo pipefail
[[ $EUID == 0 ]] || { echo 'Run with sudo, passing the intended desktop username.' >&2; exit 1; }
[[ $# -ge 1 && $# -le 2 && $1 =~ ^[a-zA-Z_][a-zA-Z0-9_-]*\$?$ ]] || { echo 'usage: sudo ./install.sh USER [BIN_DIR]' >&2; exit 2; }
id "$1" >/dev/null
root=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
bin_dir=${2:-"$root/../../build/host/bin/linux"}
[[ -f "$bin_dir/espkvm" && -f "$bin_dir/espkvmctl" ]] || { echo 'Build the Linux host binaries before installing.' >&2; exit 1; }
getent group espkvm >/dev/null || groupadd --system espkvm
usermod -a -G espkvm "$1"
install -m 0644 "$root/70-espkvm-input.rules" /etc/udev/rules.d/70-espkvm-input.rules
install -m 0755 -- "$bin_dir/espkvm" /usr/local/bin/espkvm
install -m 0755 -- "$bin_dir/espkvmctl" /usr/local/bin/espkvmctl
udevadm control --reload-rules
udevadm trigger --subsystem-match=input
echo 'Log out and back in to acquire group membership. Group members can read physical input.'
