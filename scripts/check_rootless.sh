#!/usr/bin/env bash
# 1-minute smoke test: can this machine run mini-PAMS without sudo?
# Run:  ./scripts/check_rootless.sh
set -uo pipefail
SELF="$(readlink -f "$0")"

if [[ -z "${MINIPAMS_INSIDE:-}" ]]; then
  exec systemd-run --user --scope -p Delegate=yes --quiet env MINIPAMS_INSIDE=1 "$SELF"
fi

ok()   { echo "  [OK]   $*"; }
fail() { echo "  [FAIL] $*"; FAILED=1; }
FAILED=0

SCOPE="/sys/fs/cgroup$(cut -d: -f3 /proc/self/cgroup)"
echo "Scope: $SCOPE"
mkdir -p "$SCOPE/leader" && echo $$ > "$SCOPE/leader/cgroup.procs" && ok "moved shell into leader group" || fail "leader group"
echo "+memory +cpu" > "$SCOPE/cgroup.subtree_control" 2>/dev/null && ok "enabled memory+cpu for children" || fail "cannot enable controllers"
T="$SCOPE/test"
mkdir -p "$T" && ok "created child cgroup" || fail "mkdir child"
echo 1G  > "$T/memory.max"  2>/dev/null && ok "wrote memory.max"  || fail "memory.max"
echo 512M > "$T/memory.low" 2>/dev/null && ok "wrote memory.low"  || fail "memory.low"
echo 500 > "$T/cpu.weight"  2>/dev/null && ok "wrote cpu.weight"  || fail "cpu.weight"
[[ -r "$T/memory.pressure" ]] && ok "memory.pressure readable (PSI on)" || fail "no memory.pressure"
grep -q pgmajfault "$T/memory.stat" && ok "memory.stat has pgmajfault" || fail "memory.stat"
bash -c 'echo $$ > "$1/cgroup.procs"; exec true' _ "$T" && ok "can move a process into the child" || fail "move process"
[[ -n "$(swapon --show --noheadings 2>/dev/null)" ]] && ok "swap present" || fail "no swap"
rmdir "$T" 2>/dev/null

echo
[[ $FAILED == 0 ]] && echo "ALL GOOD: run ./scripts/run_rootless.sh after the midterm." || echo "Some checks failed: send this output to Claude."
