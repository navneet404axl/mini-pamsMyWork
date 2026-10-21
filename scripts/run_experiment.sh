#!/usr/bin/env bash
# Reproduce a mini Figure 1, then test mini-PAMS. Run as root:
#   sudo ./scripts/run_experiment.sh
#
# For each memory limit it runs four modes:
#   cache_alone : cache app alone              (like "Isolated RocksDB")
#   heap_alone  : heap app alone               (like "Isolated YOLO")
#   shared      : both together, plain Linux   (like "Shared" in Figure 1)
#   minipams    : both together + controller   (our prototype)
#
# The defaults suit a machine with ~8 GB of RAM. Override them like:
#   sudo DATA_GB=4 HOG_GB=3 LIMITS="6G 5G 4G" ./scripts/run_experiment.sh
set -euo pipefail

DATA_GB=${DATA_GB:-3}          # cache app's data file (its "database")
HOG_GB=${HOG_GB:-2}            # heap app's memory
LIMITS=${LIMITS:-"5G 4G 3G"}   # memory.max for the whole minipams group (the x-axis)
DURATION=${DURATION:-60}       # seconds per run
WARMUP=${WARMUP:-15}           # seconds excluded from averages
TRIGGER=${TRIGGER:-0}          # 1 = cache app sends an emergency trigger mid-run
DATA=${DATA:-/var/tmp/minipams_data.bin}   # must be on a real disk, NOT tmpfs

CG=/sys/fs/cgroup/minipams
BIN="$(cd "$(dirname "$0")/.." && pwd)/bin"
OUT="$(cd "$(dirname "$0")/.." && pwd)/results"
mkdir -p "$OUT"

[[ $EUID -eq 0 ]] || { echo "Please run with sudo." >&2; exit 1; }
[[ -d $CG/cache_app ]] || { echo "Run scripts/setup.sh first." >&2; exit 1; }

# Create the data file once.
if [[ ! -f $DATA ]]; then
  "$BIN/cacheapp" -create -file "$DATA" -size-gb "$DATA_GB"
fi

# Run a command inside a cgroup: the shell moves itself into the cgroup,
# then exec replaces it with the real program (which stays in the cgroup).
run_in() {
  local group=$1; shift
  bash -c 'echo $$ > "$1/cgroup.procs"; shift; exec "$@"' _ "$CG/$group" "$@"
}

# Put everything back to a clean, cold state before each run.
reset_state() {
  local limit=$1
  for g in cache_app heap_app; do
    echo 0   > "$CG/$g/memory.low"
    echo 100 > "$CG/$g/cpu.weight"
  done
  rm -f /run/minipams/*.trigger
  echo max > "$CG/memory.max"          # lift the limit while we clean up
  sync; echo 3 > /proc/sys/vm/drop_caches
  echo "$limit" > "$CG/memory.max"     # this is the "System Memory" x-axis
}

# Pre-load the data file into the page cache from inside cache_app, so the
# cached pages are charged to cache_app's cgroup (like RocksDB's warm cache).
warm_cache() {
  run_in cache_app cat "$DATA" > /dev/null
}

avg() { grep "^$2" "$1" | awk '{print $2}'; }

cache_cmd=("$BIN/cacheapp" -file "$DATA" -size-gb "$DATA_GB" -duration "$DURATION" -warmup "$WARMUP")
if [[ $TRIGGER == 1 ]]; then
  cache_cmd+=(-trigger-file /run/minipams/cache_app.trigger -trigger-after $((DURATION / 2)))
fi
heap_cmd=("$BIN/heaphog" -gb "$HOG_GB" -duration "$DURATION" -warmup "$WARMUP")

echo "limit,mode,cache_ops_per_sec,heap_mpages_per_sec" > "$OUT/results.csv"

for limit in $LIMITS; do
  echo "=== memory limit $limit ==="

  # cache_alone
  reset_state "$limit"; warm_cache
  run_in cache_app "${cache_cmd[@]}" > "$OUT/${limit}_cache_alone.csv"
  c=$(avg "$OUT/${limit}_cache_alone.csv" AVG_OPS_PER_SEC)
  echo "$limit,cache_alone,$c," >> "$OUT/results.csv"
  echo "  cache_alone : $c ops/s"

  # heap_alone
  reset_state "$limit"
  run_in heap_app "${heap_cmd[@]}" > "$OUT/${limit}_heap_alone.csv"
  h=$(avg "$OUT/${limit}_heap_alone.csv" AVG_MPAGES_PER_SEC)
  echo "$limit,heap_alone,,$h" >> "$OUT/results.csv"
  echo "  heap_alone  : $h Mpages/s"

  # shared (plain Linux) and minipams (with controller)
  for mode in shared minipams; do
    reset_state "$limit"; warm_cache
    ctl_pid=""
    if [[ $mode == minipams ]]; then
      "$BIN/minipams" -root "$CG" -trigger-dir /run/minipams > "$OUT/${limit}_controller.csv" 2>/dev/null &
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
echo "Done. Summary in $OUT/results.csv; per-second data and controller logs in $OUT/"
