#!/usr/bin/env bash
set -euo pipefail
[[ $EUID == 0 ]] || { echo 'Run with sudo, passing the intended desktop username.' >&2; exit 1; }
[[ $# -ge 1 && $# -le 2 && $1 =~ ^[a-zA-Z_][a-zA-Z0-9_-]*\$?$ ]] || { echo 'usage: sudo ./install.sh USER [BIN_DIR]' >&2; exit 2; }
id "$1" >/dev/null
user_home=$(getent passwd "$1" | cut -d: -f6)
[[ -n "$user_home" && -d "$user_home" ]] || { echo "Cannot find home directory for $1." >&2; exit 1; }
user_group=$(id -gn "$1")
root=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
bin_dir=${2:-"$root/../../build/host/bin"}
case $(uname -m) in
 x86_64) binary=espkvm_x86_64_linux.exe ;;
 aarch64|arm64) binary=espkvm_aarch64_linux.exe ;;
 *) echo "Unsupported Linux architecture: $(uname -m)" >&2; exit 1 ;;
esac
[[ -f "$bin_dir/$binary" ]] || { echo "Build $binary before installing." >&2; exit 1; }
getent group espkvm >/dev/null || groupadd --system espkvm
usermod -a -G espkvm "$1"
install -m 0644 "$root/70-espkvm-input.rules" /etc/udev/rules.d/70-espkvm-input.rules
install -d -m 0755 -o "$1" -g "$user_group" "$user_home/.local/bin"
install -m 0755 -o "$1" -g "$user_group" -- "$bin_dir/$binary" "$user_home/.local/bin/espkvm"
udevadm control --reload-rules
udevadm trigger --subsystem-match=input
echo "Installed to $user_home/.local/bin/espkvm so config.json can be kept beside the executable."
echo 'Log out and back in to acquire group membership. Group members can read physical input.'
