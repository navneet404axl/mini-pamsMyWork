// Package policy is the "brain" of mini-PAMS: given what a cgroup is doing,
// decide how much memory protection and CPU weight it should get.
//
// It is a pure function (no file access), which makes it easy to test and
// easy to explain: same inputs, same decision.
package policy

// Config holds the thresholds and step sizes. Defaults are in Default().
type Config struct {
	FaultRateHigh   float64 // major faults/sec above which the cache is "being evicted"
	PressureHigh    float64 // memory.pressure avg10 (%) above which the app is "struggling"
	Step            int64   // bytes to raise or lower memory.low per tick
	MaxProtect      int64   // never protect more than this many bytes for one app
	NormalWeight    int     // cpu.weight in normal mode (100 is the kernel default)
	EmergencyWeight int     // cpu.weight in emergency mode
}

func Default() Config {
	return Config{
		FaultRateHigh:   200,
		PressureHigh:    5.0,
		Step:            256 << 20, // 256 MB
		MaxProtect:      4 << 30,   // 4 GB
		NormalWeight:    100,
		EmergencyWeight: 1000,
	}
}

// Input is everything the policy looks at for one app on one tick.
type Input struct {
	Current       int64   // bytes in use now
	File          int64   // page-cache bytes
	Anon          int64   // heap bytes
	FaultRate     float64 // major faults per second since the last tick
	PressureAvg10 float64 // % time stalled on memory
	Emergency     bool    // the app sent an "emergency" trigger (principle 2)
	CurrentLow    int64   // memory.low we set last tick
}

// Decision is what the controller should write back.
type Decision struct {
	MemoryLow int64
	CPUWeight int
	Reason    string
}

// Decide applies three rules, in priority order.
//
//  1. Emergency (PAMS principle 2): the app said it is critical, so protect
//     everything it currently holds and give it 10x CPU weight.
//
//  2. Cache under attack (approximating PAMS principle 1): the app is mostly
//     page cache AND its major faults or memory pressure are high. Linux is
//     evicting its cache, so raise its protection by one step, letting it
//     win the cache back gradually.
//
//  3. Calm: slowly lower protection by one step, so the memory can flow
//     back to other apps. This is what makes it *dynamic*, unlike the static
//     container limits in Figure 2 of the paper.
func Decide(in Input, c Config) Decision {
	if in.Emergency {
		return Decision{
			MemoryLow: min(in.Current, c.MaxProtect),
			CPUWeight: c.EmergencyWeight,
			Reason:    "emergency trigger",
		}
	}

	cacheHeavy := in.File > in.Anon
	struggling := in.FaultRate > c.FaultRateHigh || in.PressureAvg10 > c.PressureHigh

	if cacheHeavy && struggling {
		return Decision{
			MemoryLow: min(in.CurrentLow+c.Step, c.MaxProtect),
			CPUWeight: c.NormalWeight,
			Reason:    "cache being evicted: protect more",
		}
	}

	return Decision{
		MemoryLow: max(in.CurrentLow-c.Step, 0),
		CPUWeight: c.NormalWeight,
		Reason:    "calm: relax protection",
	}
}
