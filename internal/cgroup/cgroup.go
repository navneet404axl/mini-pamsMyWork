// Package cgroup reads the signals mini-PAMS watches and writes the knobs it
// turns, for one cgroup v2 directory such as /sys/fs/cgroup/minipams/cache_app.
//
// Everything in cgroup v2 is a plain text file: reading a file reports a
// statistic, writing a file changes a setting. That is why this package is
// just file reads and writes.
package cgroup

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Stats is one snapshot of a cgroup's memory situation.
type Stats struct {
	Current       int64   // memory.current: total bytes the group uses right now
	File          int64   // memory.stat "file": page-cache bytes (RocksDB-style memory)
	Anon          int64   // memory.stat "anon": heap bytes (YOLO-style memory)
	MajFaults     int64   // memory.stat "pgmajfault": cumulative faults that needed disk
	PressureAvg10 float64 // memory.pressure "some avg10": % of time stalled on memory, last 10s
}

// Group is one cgroup directory.
type Group struct {
	Name string
	Dir  string
}

func New(root, name string) Group {
	return Group{Name: name, Dir: filepath.Join(root, name)}
}

// ReadStats reads memory.current, memory.stat and memory.pressure.
func (g Group) ReadStats() (Stats, error) {
	var s Stats
	var err error

	if s.Current, err = readInt(filepath.Join(g.Dir, "memory.current")); err != nil {
		return s, err
	}

	stat, err := readKeyValues(filepath.Join(g.Dir, "memory.stat"))
	if err != nil {
		return s, err
	}
	s.File = stat["file"]
	s.Anon = stat["anon"]
	s.MajFaults = stat["pgmajfault"]

	if s.PressureAvg10, err = readPressure(filepath.Join(g.Dir, "memory.pressure")); err != nil {
		return s, err
	}
	return s, nil
}

// SetMemoryLow writes memory.low: "try not to reclaim this group's memory
// below this many bytes". It is best-effort protection: if nothing else can
// be reclaimed, the kernel will still take it (unlike memory.min).
func (g Group) SetMemoryLow(bytes int64) error {
	return os.WriteFile(filepath.Join(g.Dir, "memory.low"), []byte(strconv.FormatInt(bytes, 10)), 0644)
}

// SetCPUWeight writes cpu.weight (1..10000, default 100). A group with
// weight 1000 gets 10x the CPU share of a group with weight 100 when both
// want the CPU.
func (g Group) SetCPUWeight(w int) error {
	return os.WriteFile(filepath.Join(g.Dir, "cpu.weight"), []byte(strconv.Itoa(w)), 0644)
}

// ReadTrigger returns true if the app has written "emergency" to its trigger
// file (PAMS principle 2). A missing file means "normal".
func ReadTrigger(triggerDir, name string) bool {
	b, err := os.ReadFile(filepath.Join(triggerDir, name+".trigger"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(b)) == "emergency"
}

// --- small file-parsing helpers -------------------------------------------

func readInt(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
}

// readKeyValues parses files made of "key value" lines, like memory.stat.
func readKeyValues(path string) (map[string]int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	m := map[string]int64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
			m[fields[0]] = v
		}
	}
	return m, sc.Err()
}

// readPressure parses memory.pressure, which looks like:
//
//	some avg10=1.23 avg60=0.50 avg300=0.10 total=123456
//	full avg10=0.80 avg60=0.30 avg300=0.05 total=98765
//
// "some" = at least one task in the group was stalled waiting for memory.
func readPressure(path string) (float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "some" {
			continue
		}
		v := strings.TrimPrefix(fields[1], "avg10=")
		return strconv.ParseFloat(v, 64)
	}
	return 0, fmt.Errorf("no 'some' line in %s", path)
}
