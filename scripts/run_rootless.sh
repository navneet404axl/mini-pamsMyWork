#!/usr/bin/env bash
# mini-PAMS experiment WITHOUT sudo, for machines like the Rutgers iLab
# desktops (Ubuntu 24.04, cgroup v2, systemd).
#
#   ./scripts/run_rootless.sh
#
# How it works without root:
#   1. systemd gives every logged-in user their own cgroup subtree with the
#      memory and cpu controllers ("delegation"). We ask systemd for a new
#      delegated scope and run this whole script inside it.
#   2. Inside that scope we own the cgroup folders, so we can create
#      minipams/{cache_app,heap_app} and write memory.max, memory.low and
#      cpu.weight ourselves.
#   3. Instead of "echo 3 > drop_caches" (root only, clears everything), we
#      evict just our data file from the page cache (cacheapp -evict).
#   4. Swap must already exist on the machine (check with: free -h).
#
# Same four modes and outputs as run_experiment.sh. Defaults suit a machine
# with ~30 GB RAM; override like:  DATA_GB=3 HOG_GB=2 LIMITS="5G 4G 3G" ./scripts/run_rootless.sh
set -euo pipefail

SELF="$(readlink -f "$0")"
PROJECT="$(cd "$(dirname "$SELF")/.." && pwd)"

# --- Step 1: re-run ourselves inside a delegated systemd user scope -------
if [[ -z "${MINIPAMS_INSIDE:-}" ]]; then
  echo "Starting a delegated systemd scope..."
  exec systemd-run --user --scope -p Delegate=yes --quiet \
       env MINIPAMS_INSIDE=1 "$SELF" "$@"
fi

DATA_GB=${DATA_GB:-4}
HOG_GB=${HOG_GB:-3}
LIMITS=${LIMITS:-"6G 5G 4G"}
DURATION=${DURATION:-60}
WARMUP=${WARMUP:-15}
TRIGGER=${TRIGGER:-0}
DATA=${DATA:-/tmp/minipams_data.bin}       # local NVMe, NOT the network home drive
TRIG_DIR=${TRIG_DIR:-/tmp/minipams_trig}

# --- Step 2: build our cgroup tree inside the scope ------------------------
# /proc/self/cgroup looks like "0::/user.slice/.../run-xxxx.scope"
SCOPE="/sys/fs/cgroup$(cut -d: -f3 /proc/self/cgroup)"
echo "Delegated scope: $SCOPE"

# cgroup v2 rule: a group that hands controllers to children must not hold
# processes itself. So first move this shell into a "leader" child group.
mkdir -p "$SCOPE/leader"
echo $$ > "$SCOPE/leader/cgroup.procs"

echo "+memory +cpu" > "$SCOPE/cgroup.subtree_control"
CG="$SCOPE/minipams"
mkdir -p "$CG"
echo "+memory +cpu" > "$CG/cgroup.subtree_control"
mkdir -p "$CG/cache_app" "$CG/heap_app" "$TRIG_DIR"

[[ -n "$(swapon --show --noheadings 2>/dev/null)" ]] || echo "WARNING: no swap found; memory.low can't redirect reclaim without it."

# --- Step 3: build the programs --------------------------------------------
cd "$PROJECT"
go build -o bin/ ./cmd/...
BIN="$PROJECT/bin"
OUT="$PROJECT/results"
mkdir -p "$OUT"

if [[ ! -f $DATA ]]; then
  "$BIN/cacheapp" -create -file "$DATA" -size-gb "$DATA_GB"
fi

run_in() {
  local group=$1; shift
  bash -c 'echo $$ > "$1/cgroup.procs"; shift; exec "$@"' _ "$CG/$group" "$@"
}

reset_state() {
  local limit=$1
  for g in cache_app heap_app; do
    echo 0   > "$CG/$g/memory.low"
    echo 100 > "$CG/$g/cpu.weight"
  done
  rm -f "$TRIG_DIR"/*.trigger
  echo max > "$CG/memory.max"
  "$BIN/cacheapp" -evict -file "$DATA"     # cold start, without root
  echo "$limit" > "$CG/memory.max"
}

warm_cache() { run_in cache_app cat "$DATA" > /dev/null; }
avg() { grep "^$2" "$1" | awk '{print $2}'; }

cache_cmd=("$BIN/cacheapp" -file "$DATA" -size-gb "$DATA_GB" -duration "$DURATION" -warmup "$WARMUP")
if [[ $TRIGGER == 1 ]]; then
  cache_cmd+=(-trigger-file "$TRIG_DIR/cache_app.trigger" -trigger-after $((DURATION / 2)))
fi
heap_cmd=("$BIN/heaphog" -gb "$HOG_GB" -duration "$DURATION" -warmup "$WARMUP")

echo "limit,mode,cache_ops_per_sec,heap_mpages_per_sec" > "$OUT/results.csv"

for limit in $LIMITS; do
  echo "=== memory limit $limit ==="

  reset_state "$limit"; warm_cache
  run_in cache_app "${cache_cmd[@]}" > "$OUT/${limit}_cache_alone.csv"
  c=$(avg "$OUT/${limit}_cache_alone.csv" AVG_OPS_PER_SEC)
  echo "$limit,cache_alone,$c," >> "$OUT/results.csv"
  echo "  cache_alone : $c ops/s"

  reset_state "$limit"
  run_in heap_app "${heap_cmd[@]}" > "$OUT/${limit}_heap_alone.csv"
  h=$(avg "$OUT/${limit}_heap_alone.csv" AVG_MPAGES_PER_SEC)
  echo "$limit,heap_alone,,$h" >> "$OUT/results.csv"
  echo "  heap_alone  : $h Mpages/s"

  for mode in shared minipams; do
    reset_state "$limit"; warm_cache
    ctl_pid=""
    if [[ $mode == minipams ]]; then
      "$BIN/minipams" -root "$CG" -trigger-dir "$TRIG_DIR" > "$OUT/${limit}_controller.csv" 2>/dev/null &
      ctl_pid=$!
    fi
    run_in heap_app  "${heap_cmd[@]}"  > "$OUT/${limit}_${mode}_heap.csv"  &
    hp=$!
    run_in cache_app "${cache_cmd[@]}" > "$OUT/${limit}_${mode}_cache.csv" &
    cp=$!
    wait "$hp" "$cp"
    if [[ -n $ctl_pid ]]; then kill -INT "$ctl_pid"; wait "$ctl_pid" || true; fi

    c=$(avg "$OUT/${limit}_${mode}_cache.csv" AVG_OPS_PER_SEC)
    h=$(avg "$OUT/${limit}_${mode}_heap.csv" AVG_MPAGES_PER_SEC)
    echo "$limit,$mode,$c,$h" >> "$OUT/results.csv"
    printf "  %-11s : cache %s ops/s, heap %s Mpages/s\n" "$mode" "$c" "$h"
  done
done

echo max > "$CG/memory.max"
echo
echo "Done. Summary in $OUT/results.csv"
