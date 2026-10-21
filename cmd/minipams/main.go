// minipams: a small user-space controller inspired by PAMS (Kannan et al.,
// HotStorage '25). It is NOT PAMS; it is a prototype to learn how far
// existing Linux knobs can go toward PAMS principles 1 and 2.
//
// Every -interval it, for each app's cgroup:
//  1. reads its signals   (memory.current, memory.stat, memory.pressure, trigger file)
//  2. decides             (internal/policy.Decide)
//  3. turns the knobs     (memory.low, cpu.weight)
//  4. logs one CSV line   (so you can plot what the controller did)
//
// Usage (as root):
//
//	minipams -root /sys/fs/cgroup/minipams -apps cache_app,heap_app \
//	         -trigger-dir /run/minipams -interval 1s > controller.csv
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"minipams/internal/cgroup"
	"minipams/internal/policy"
)

// appState remembers what we need from the previous tick.
type appState struct {
	group      cgroup.Group
	lastFaults int64 // pgmajfault counter last tick, to turn it into a rate
	low        int64 // memory.low we last wrote
	first      bool  // no previous fault reading yet
}

func main() {
	root := flag.String("root", "/sys/fs/cgroup/minipams", "parent cgroup directory")
	apps := flag.String("apps", "cache_app,heap_app", "comma-separated child cgroup names")
	triggerDir := flag.String("trigger-dir", "/run/minipams", "directory holding <app>.trigger files")
	interval := flag.Duration("interval", time.Second, "how often to run the loop")
	dryRun := flag.Bool("dry-run", false, "decide and log, but don't write any knobs")
	flag.Parse()

	cfg := policy.Default()

	var states []*appState
	for _, name := range strings.Split(*apps, ",") {
		states = append(states, &appState{group: cgroup.New(*root, name), first: true})
	}

	// Stop cleanly on Ctrl+C and put the knobs back to their defaults, so a
	// stopped controller doesn't leave stale protection behind.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	fmt.Println("t,app,current_mb,file_mb,anon_mb,majfault_per_s,pressure_avg10,emergency,memory_low_mb,cpu_weight,reason")

	tick := time.NewTicker(*interval)
	defer tick.Stop()
	start := time.Now()

	for {
		select {
		case <-sig:
			if !*dryRun {
				for _, st := range states {
					st.group.SetMemoryLow(0)
					st.group.SetCPUWeight(cfg.NormalWeight)
				}
			}
			log.Println("minipams: stopped, knobs reset")
			return
		case <-tick.C:
		}

		t := time.Since(start).Seconds()
		for _, st := range states {
			// 1. Read signals.
			s, err := st.group.ReadStats()
			if err != nil {
				log.Printf("%s: read stats: %v", st.group.Name, err)
				continue
			}
			rate := 0.0
			if !st.first {
				rate = float64(s.MajFaults-st.lastFaults) / interval.Seconds()
			}
			st.lastFaults, st.first = s.MajFaults, false
			emergency := cgroup.ReadTrigger(*triggerDir, st.group.Name)

			// 2. Decide.
			d := policy.Decide(policy.Input{
				Current:       s.Current,
				File:          s.File,
				Anon:          s.Anon,
				FaultRate:     rate,
				PressureAvg10: s.PressureAvg10,
				Emergency:     emergency,
				CurrentLow:    st.low,
			}, cfg)

			// 3. Act.
			if !*dryRun {
				if err := st.group.SetMemoryLow(d.MemoryLow); err != nil {
					log.Printf("%s: set memory.low: %v", st.group.Name, err)
				}
				if err := st.group.SetCPUWeight(d.CPUWeight); err != nil {
					log.Printf("%s: set cpu.weight: %v", st.group.Name, err)
				}
			}
			st.low = d.MemoryLow

			// 4. Log.
			fmt.Printf("%.0f,%s,%d,%d,%d,%.0f,%.2f,%t,%d,%d,%s\n",
				t, st.group.Name, s.Current>>20, s.File>>20, s.Anon>>20,
				rate, s.PressureAvg10, emergency, d.MemoryLow>>20, d.CPUWeight, d.Reason)
		}
	}
}
