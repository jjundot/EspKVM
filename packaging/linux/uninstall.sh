#!/usr/bin/env bash
set -euo pipefail
[[ $EUID == 0 ]] || { echo 'Run with sudo.' >&2; exit 1; }
[[ $# -le 1 ]] || { echo 'usage: sudo ./uninstall.sh [USER]' >&2; exit 2; }
rm -f -- /etc/udev/rules.d/70-espkvm-input.rules
rm -f -- /usr/local/bin/espkvm /usr/local/bin/espkvmctl
if [[ $# == 1 ]]; then
 [[ $1 =~ ^[a-zA-Z_][a-zA-Z0-9_-]*\$?$ ]] || { echo 'Invalid username.' >&2; exit 2; }
 user_home=$(getent passwd "$1" | cut -d: -f6)
 [[ -n "$user_home" ]] || { echo "Unknown user: $1" >&2; exit 1; }
 rm -f -- "$user_home/.local/bin/espkvm"
fi
udevadm control --reload-rules
udevadm trigger --subsystem-match=input
echo 'Rules removed. Remove any remaining espkvm group memberships explicitly if no longer needed.'
[[ $# == 1 ]] || echo 'Pass the installed USER to also remove its ~/.local/bin/espkvm binary.'
