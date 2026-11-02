#!/usr/bin/env bash
set -euo pipefail
[[ $EUID == 0 ]] || { echo 'Run with sudo.' >&2; exit 1; }
rm -f -- /etc/udev/rules.d/70-espkvm-input.rules
rm -f -- /usr/local/bin/espkvm /usr/local/bin/espkvmctl
udevadm control --reload-rules
udevadm trigger --subsystem-match=input
echo 'Rules removed. Remove any remaining espkvm group memberships explicitly if no longer needed.'
