package policy

import "testing"

const mb = 1 << 20

func TestDecide(t *testing.T) {
	c := Default()

	tests := []struct {
		name       string
		in         Input
		wantLow    int64
		wantWeight int
	}{
		{
			name:       "emergency protects everything it holds and boosts CPU",
			in:         Input{Current: 2000 * mb, File: 1500 * mb, Anon: 500 * mb, Emergency: true},
			wantLow:    2000 * mb,
			wantWeight: 1000,
		},
		{
			name:       "emergency protection is capped at MaxProtect",
			in:         Input{Current: 6000 * mb, Emergency: true},
			wantLow:    c.MaxProtect,
			wantWeight: 1000,
		},
		{
			name:       "cache-heavy app with high faults gets one step more protection",
			in:         Input{File: 2000 * mb, Anon: 100 * mb, FaultRate: 5000, CurrentLow: 512 * mb},
			wantLow:    512*mb + c.Step,
			wantWeight: 100,
		},
		{
			name:       "cache-heavy app with high pressure (low faults) also counts as struggling",
			in:         Input{File: 2000 * mb, Anon: 100 * mb, PressureAvg10: 20, CurrentLow: 0},
			wantLow:    c.Step,
			wantWeight: 100,
		},
		{
			name:       "heap-heavy app is not protected even when struggling (rule 2 is for cache)",
			in:         Input{File: 100 * mb, Anon: 3000 * mb, FaultRate: 5000, CurrentLow: 512 * mb},
			wantLow:    512*mb - c.Step,
			wantWeight: 100,
		},
		{
			name:       "calm app relaxes protection, never below zero",
			in:         Input{File: 2000 * mb, Anon: 100 * mb, CurrentLow: 100 * mb},
			wantLow:    0,
			wantWeight: 100,
		},
		{
			name:       "protection growth stops at MaxProtect",
			in:         Input{File: 2000 * mb, Anon: 100 * mb, FaultRate: 5000, CurrentLow: c.MaxProtect},
			wantLow:    c.MaxProtect,
			wantWeight: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decide(tt.in, c)
			if d.MemoryLow != tt.wantLow || d.CPUWeight != tt.wantWeight {
				t.Errorf("got low=%dMB weight=%d (%s), want low=%dMB weight=%d",
					d.MemoryLow/mb, d.CPUWeight, d.Reason, tt.wantLow/mb, tt.wantWeight)
			}
		})
	}
}
