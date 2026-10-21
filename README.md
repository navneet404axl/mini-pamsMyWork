# mini-PAMS

A small prototype inspired by **PAMS** from *"Are Edge MicroDCs Equipped to Tackle Memory Contention?"* (Kannan et al., HotStorage '25).

It is **not** PAMS. It's a learning project that asks: *how far can existing Linux knobs go toward PAMS principles 1 and 2, and where do they fall short?*

---

## What it does, in one picture

```
   cache_app (like RocksDB)          heap_app (like YOLO)
   random reads → page cache          big heap, always touched
            \                               /
             \____ /sys/fs/cgroup/minipams ____/   ← memory.max = the "System Memory" x-axis
                            ▲
                            │ every second: read signals → decide → turn knobs
                       minipams (controller)
```

1. **Reproduce the problem:** shrink the group's memory and watch `cache_app` collapse while `heap_app` barely notices. That's a mini Figure 1.
2. **Try a fix:** run the `minipams` controller, which protects `cache_app`'s memory when its cache is being evicted (principle 1, approximated) and boosts any app that signals an emergency (principle 2).
3. **Measure the trade-off:** protecting the cache pushes the kernel to swap out heap memory instead, so `heap_app` slows down. The interesting result is the *shape* of that trade-off.

---

## Files

| File | Role | Paper equivalent |
|---|---|---|
| `cmd/cacheapp/main.go` | Random 4 KB reads from a big file through the page cache; prints ops/sec | RocksDB |
| `cmd/heaphog/main.go` | Allocates N GB of heap and keeps touching every page; prints pages/sec | YOLO inference |
| `internal/cgroup/cgroup.go` | Reads cgroup stats files, writes knob files | OS-level memory tracking (Section 3) |
| `internal/policy/policy.go` | Pure decision function: stats in → `memory.low` + `cpu.weight` out | The PAMS decision engine (simplified) |
| `cmd/minipams/main.go` | The loop: read → decide → act → log CSV | PAMS controller |
| `scripts/setup.sh` | Creates cgroups, ensures swap, builds | — |
| `scripts/run_experiment.sh` | Runs all modes for each memory limit, writes `results/results.csv` | Section 2.3 experiments |
| `*_test.go` | Unit tests with a fake cgroup folder | — |

Suggested reading order: `cacheapp` → `heaphog` → `cgroup.go` → `policy.go` → `minipams/main.go` → the two scripts.

---

## The Linux pieces you need to understand

**cgroup v2.** A folder under `/sys/fs/cgroup`. Put a process's PID into its `cgroup.procs` file and the process is inside the group. Everything else is text files:

| File | Read or write | Meaning |
|---|---|---|
| `memory.max` | write | Hard limit for the group. We set it on the *parent* `minipams` to create memory pressure, like the paper shrinking system memory. |
| `memory.current` | read | Bytes the group uses now |
| `memory.stat` | read | Breakdown: `file` = page cache, `anon` = heap, `pgmajfault` = faults that needed the disk |
| `memory.pressure` | read | PSI: % of recent time tasks were stalled waiting for memory |
| `memory.low` | write | "Try not to reclaim below this." Best-effort protection. |
| `cpu.weight` | write | Relative CPU share (default 100) |

**Why `memory.low` works here.** When `minipams` hits its `memory.max`, the kernel reclaims memory from inside `minipams`. For the direct children of the group being reclaimed from, the kernel honours each child's own `memory.low`. So raising `cache_app`'s `memory.low` tells the kernel: *take memory from `heap_app` first.*

**Why swap is required.** Heap memory can only be reclaimed by writing it to swap. With no swap, page cache is the *only* thing the kernel can take, and no controller could protect it. With swap, the kernel normally still prefers dropping cache (that's the Figure 1 asymmetry), and `memory.low` lets us change that preference.

**Why page cache is charged to `cache_app`.** Linux charges a cached page to the cgroup of the process that first read it. That's why the script warms the cache with `cat` running *inside* `cache_app`.

---

## The policy (`internal/policy/policy.go`)

Three rules, checked in order, once per second for each app:

1. **Emergency trigger** (principle 2): the app wrote `emergency` to `/run/minipams/<app>.trigger` → protect everything it holds (`memory.low = memory.current`) and set `cpu.weight = 1000`.
2. **Cache under attack** (principle 1, approximated): the app is mostly page cache (`file > anon`) **and** it is struggling (major faults > 200/s **or** pressure > 5%) → raise `memory.low` by 256 MB, up to 4 GB.
3. **Calm** → lower `memory.low` by 256 MB, so memory flows back to others. This is what makes it *dynamic*, unlike the static container limits of Figure 2.

Why step by 256 MB instead of jumping straight to full protection? Gradual changes avoid wild swings, and the calm rule gives memory back when it's no longer needed.

---

## Running it

**Requirements:** Linux with cgroup v2 (Ubuntu 22.04+), root access, Go 1.22+, about 8 GB RAM, and a real disk (not tmpfs) for the data file. A cheap cloud VM works.

```bash
sudo ./scripts/setup.sh
sudo ./scripts/run_experiment.sh                 # ~15 min with defaults
sudo TRIGGER=1 ./scripts/run_experiment.sh       # also test the emergency trigger
```

Bigger machine? Scale up: `sudo DATA_GB=4 HOG_GB=3 LIMITS="6G 5G 4G" ./scripts/run_experiment.sh`

Unit tests (no root needed): `go test ./...`

### Running without sudo (Rutgers iLab desktops, Ubuntu 24.04)

systemd gives each logged-in user their own cgroup subtree with the `memory` and `cpu` controllers, so mini-PAMS can run there without root:

```bash
cd /tmp && tar xzf ~/mini-pams.tar.gz && cd mini-pams   # work on the local disk
./scripts/check_rootless.sh      # 1-minute smoke test: every line should say [OK]
./scripts/run_rootless.sh        # the full experiment (~25 min with defaults)
```

What's different from the root version:
- It runs inside a **delegated systemd scope** (`systemd-run --user --scope -p Delegate=yes`), and builds `minipams/{cache_app,heap_app}` inside it.
- Instead of `drop_caches` (root only), it evicts **just the data file** from the page cache with `cacheapp -evict` (`posix_fadvise(DONTNEED)`).
- Data file and trigger files live in `/tmp` (local NVMe). Never put the data file on your home drive: it's a network drive, and you'd be measuring the network instead of the page cache.
- Defaults are sized for ~30 GB RAM: `DATA_GB=4 HOG_GB=3 LIMITS="6G 5G 4G"`.

It's a shared lab machine, so keep runs to sensible sizes and don't leave things running when you leave.

**Outputs in `results/`:**
- `results.csv`: one row per (limit, mode): average cache ops/sec and heap Mpages/sec
- `<limit>_<mode>_cache.csv`, `..._heap.csv`: per-second numbers
- `<limit>_controller.csv`: every decision the controller made, and why

---

## What to look for in the results

- **cache_alone vs shared:** the cache app should drop sharply as the limit shrinks. That's your Figure 1.
- **heap_alone vs shared:** the heap app should barely change. The asymmetry.
- **shared vs minipams:** the cache app should recover some throughput, and the heap app should lose some (it's being swapped now). Is the trade worth it?
- **controller CSV:** watch `memory_low_mb` climb when `majfault_per_s` spikes.

---

## Honest limitations (good things to discuss with Prof. Kannan)

1. **`memory.low` can't tell page cache from heap.** It protects a cgroup's memory as a whole. If one app used both kinds, we couldn't protect just its cache. That's exactly the gap PAMS principle 1 targets with per-region priority labels.
2. **Rule 2 only protects cache-heavy apps.** A heap app that is struggling gets nothing. Real PAMS would rank all memory types by current need.
3. **Fixed thresholds** (200 faults/s, 5% pressure, 256 MB steps) were picked by hand. That's principle 3's job: learning them, or predicting spikes before they happen. Not implemented.
4. **Triggers are trusted blindly.** Any app could claim "emergency" forever (see question 8 in the Kannan questions doc).
5. **The controller runs in user space** and reacts once per second. A kernel or eBPF version could react faster, which is the direction the paper mentions.

How to describe it: *"I built a small user-space prototype to understand the problem, approximating principles 1 and 2 with cgroup v2 knobs. Here's where it helps, and here's where existing knobs fall short."* Never "I built PAMS."
