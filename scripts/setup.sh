#!/usr/bin/env bash
# One-time setup for mini-PAMS. Run as root:  sudo ./scripts/setup.sh
#
# What it does:
#   1. checks that this machine uses cgroup v2 (modern Linux does)
#   2. creates the cgroups:  /sys/fs/cgroup/minipams/{cache_app,heap_app}
#   3. makes sure swap exists (without swap, heap memory can never be
#      reclaimed, so the kernel has no choice but to evict page cache, and
#      no controller could change that)
#   4. builds the three Go programs into ./bin
set -euo pipefail

CG=/sys/fs/cgroup
ROOT=$CG/minipams

if [[ $EUID -ne 0 ]]; then
  echo "Please run with sudo." >&2; exit 1
fi

# 1. cgroup v2 check: in v2 the root has a cgroup.controllers file.
if [[ ! -f $CG/cgroup.controllers ]]; then
  echo "cgroup v2 is not mounted at $CG (this looks like cgroup v1)." >&2
  echo "Use Ubuntu 22.04+ or another distro with cgroup v2 by default." >&2
  exit 1
fi
for c in memory cpu; do
  grep -qw "$c" $CG/cgroup.controllers || { echo "controller '$c' not available" >&2; exit 1; }
done

# 2. Create the hierarchy. A cgroup can only control its children's memory
#    and CPU if those controllers are enabled in its cgroup.subtree_control.
echo "+memory +cpu" > $CG/cgroup.subtree_control
mkdir -p "$ROOT"
echo "+memory +cpu" > "$ROOT/cgroup.subtree_control"
mkdir -p "$ROOT/cache_app" "$ROOT/heap_app"
mkdir -p /run/minipams           # where apps write their trigger files
echo "cgroups ready: $ROOT/{cache_app,heap_app}"

# 3. Swap.
if [[ -z "$(swapon --show --noheadings)" ]]; then
  echo "No swap found: creating a 4G swap file at /swapfile_minipams"
  fallocate -l 4G /swapfile_minipams
  chmod 600 /swapfile_minipams
  mkswap /swapfile_minipams >/dev/null
  swapon /swapfile_minipams
fi
swapon --show

# 4. Build.
cd "$(dirname "$0")/.."
go build -o bin/ ./cmd/...
echo "built: $(ls bin | tr '\n' ' ')"
