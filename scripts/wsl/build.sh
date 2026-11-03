#!/usr/bin/env bash
# VS Code calls this directly in WSL Remote, or through wsl.exe on Windows.
set -euo pipefail
action=${1:-build}
target=${2:-device}
[[ "$action" == build || "$action" == rebuild ]] || exit 2
[[ "$target" == host || "$target" == device ]] || exit 2
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)
git_sha1=$(git -C "$root" rev-parse HEAD)
export EK_GIT_SHA1="$git_sha1"
source "$root/tools/versions.env"
work=$root
case "$root" in
 /mnt/[a-z]/*)
  command -v rsync >/dev/null || { echo 'Install rsync in WSL.' >&2; exit 1; }
  key=$(printf '%s' "$root" | sha256sum | cut -c1-16)
  cache_root="$HOME/.cache/esp-kvm/vscode/$key"
  mkdir -p "$cache_root"
  exec 9>"$cache_root/task.lock"
  flock 9
  if [[ -f "$cache_root/origin" && $(cat "$cache_root/origin") != "$root" ]]; then
   echo 'Build cache belongs to another checkout.' >&2; exit 1
  fi
  printf '%s' "$root" > "$cache_root/origin"
  work="$cache_root/source"
  mkdir -p "$work"
  # Deletion is restricted to this checkout's isolated source mirror. Keep
  # build/device/config/deps and IDF-managed components across source synchronization.
  [[ $(realpath "$work") == "$(realpath "$cache_root")/source" ]] || exit 1
  rsync -a --delete --exclude=/.git/ --exclude=/build/ --exclude=/ref/ --exclude=/tmp/ \
   --exclude=managed_components/ --exclude=__pycache__/ \
   --exclude=/sources/host/internal/protocol/pb/ \
   --exclude=/sources/device/sdkconfig --exclude=/sources/device/sdkconfig.old "$root/" "$work/"
  if [[ -f "$root/build/device/config/hid-selftest.sdkconfig" ]]; then
   mkdir -p "$work/build/device/config"
   rsync -au "$root/build/device/config/hid-selftest.sdkconfig" "$work/build/device/config/"
  fi
 ;;
 *) mkdir -p "$root/build"; exec 9>"$root/build/task.lock"; flock 9 ;;
esac
printf 'Build workspace: %s\n' "$work"
export PATH="$root/build/tools/bin:$HOME/.local/bin:/opt/tools/go/bin:$PATH"
proto_python=${PROTO_PYTHON:-}
if [[ -z "$proto_python" ]]; then
 for candidate in "$root/build/tools/proto/bin/python" "$HOME/.local/opt/ek/proto312/bin/python"; do
  if [[ -x "$candidate" ]]; then proto_python=$candidate; break; fi
 done
fi
if [[ -z "$proto_python" ]]; then proto_python=$(command -v python3); fi

if [[ "$target" == device ]]; then
 if [[ -z ${IDF_PATH:-} ]]; then
  for candidate in "$HOME/.local/opt/ek/esp-idf-v$IDF_VERSION" "$HOME/esp/esp-idf" /opt/esp/idf; do
   if [[ -f "$candidate/tools/idf.py" ]]; then export IDF_PATH=$candidate; break; fi
  done
 fi
 [[ -n ${IDF_PATH:-} && -f ${IDF_PATH:-}/tools/idf.py ]] || { echo "ESP-IDF $IDF_VERSION not found; set IDF_PATH." >&2; exit 1; }
 if [[ -z ${IDF_TOOLS_PATH:-} ]]; then
  if [[ -d "$HOME/.local/opt/ek/idf-tools-v$IDF_VERSION" ]]; then
   export IDF_TOOLS_PATH="$HOME/.local/opt/ek/idf-tools-v$IDF_VERSION"
  else export IDF_TOOLS_PATH="$HOME/.espressif"; fi
 fi
 if [[ -z ${IDF_PYTHON_ENV_PATH:-} ]]; then
  for candidate in "$IDF_TOOLS_PATH"/python_env/idf"${IDF_VERSION%.*}"_py*_env; do
   if [[ -x "$candidate/bin/python" ]]; then export IDF_PYTHON_ENV_PATH=$candidate; fi
  done
 fi
 [[ -n ${IDF_PYTHON_ENV_PATH:-} && -x ${IDF_PYTHON_ENV_PATH:-}/bin/python ]] || { echo 'ESP-IDF Python environment not found.' >&2; exit 1; }
 # Resolve compiler versions from the selected SDK's pinned tool manifest.
 tool_paths=$("$IDF_PYTHON_ENV_PATH/bin/python" - "$IDF_PATH/tools/tools.json" "$IDF_TOOLS_PATH" <<'PY'
import json, pathlib, sys
for tool in json.loads(pathlib.Path(sys.argv[1]).read_text())['tools']:
    if tool['name'] not in ('xtensa-esp-elf', 'esp32ulp-elf', 'esp-rom-elfs'):
        continue
    version = next(v['name'] for v in tool['versions'] if v['status'] == 'recommended')
    base = pathlib.Path(sys.argv[2]) / 'tools' / tool['name'] / version
    if not base.is_dir():
        raise SystemExit('Missing IDF tool: ' + str(base))
    if tool['name'] == 'esp-rom-elfs':
        print('rom\t' + str(base))
    for parts in tool.get('export_paths', []):
        print('bin\t' + str(base.joinpath(*parts)))
PY
 )
 while IFS=$'\t' read -r kind path; do
  case "$kind" in bin) PATH="$path:$PATH";; rom) export ESP_ROM_ELF_DIR=$path;; esac
 done <<< "$tool_paths"
 export PATH="$IDF_PYTHON_ENV_PATH/bin:$IDF_PATH/tools:$PATH"
 export ESP_IDF_VERSION=$IDF_VERSION
 cd "$work"
 cmake --preset device -DPython3_EXECUTABLE="$proto_python" -DEK_GIT_SHA1="$git_sha1"
 if [[ "$action" == rebuild ]]; then
  cmake --build --preset device --target device rebuild-hid-selftest
 else cmake --build --preset device --target device hid-selftest; fi
 for image in firmware hid-selftest; do
  package=$("$proto_python" tools/package_device.py "build/device/$image" "build/device/flash/$image" --auto)
  if [[ "$root" != "$work" ]]; then
   mkdir -p "$root/build/device/flash/$image/${package##*/}" "$root/build/device/config"
   rsync -a "$package/" "$root/build/device/flash/$image/${package##*/}/"
  fi
  printf 'Flash package: %s/build/device/flash/%s/%s\n' "$root" "$image" "${package##*/}"
 done
 if [[ "$root" != "$work" ]]; then
  rsync -a build/device/config/hid-selftest.sdkconfig "$root/build/device/config/"
 fi
else
 cd "$work"
 cmake --preset host -DPython3_EXECUTABLE="$proto_python" -DEK_GIT_SHA1="$git_sha1"
 build_args=(--build --preset host --target host)
 if [[ "$action" == rebuild ]]; then build_args+=(--clean-first); fi
 cmake "${build_args[@]}"
 if [[ "$root" != "$work" ]]; then
  mkdir -p "$root/build/host/bin"
  [[ $(realpath "$root/build/host/bin") == "$root/build/host/bin" ]] || { echo 'Refusing to sync outside build/host/bin.' >&2; exit 1; }
  rsync -a --delete --exclude='config.json' --exclude='config.json.*' build/host/bin/ "$root/build/host/bin/"
 fi
 printf 'Host binaries: %s/build/host/bin\n' "$root"
fi
