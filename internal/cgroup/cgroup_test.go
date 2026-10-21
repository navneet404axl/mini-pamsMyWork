package cgroup

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeGroup builds a directory that looks like a real cgroup v2 folder,
// so we can test parsing without root or a real cgroup.
func fakeGroup(t *testing.T) Group {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "cache_app")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"memory.current": "3221225472\n", // 3 GB
		"memory.stat": "anon 536870912\nfile 2684354560\nkernel 1000\n" +
			"pgfault 99999\npgmajfault 4200\n",
		"memory.pressure": "some avg10=12.50 avg60=4.00 avg300=1.00 total=123456\n" +
			"full avg10=8.00 avg60=2.00 avg300=0.50 total=98765\n",
		"memory.low": "0\n",
		"cpu.weight": "100\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return New(root, "cache_app")
}

func TestReadStats(t *testing.T) {
	g := fakeGroup(t)
	s, err := g.ReadStats()
	if err != nil {
		t.Fatal(err)
	}
	if s.Current != 3<<30 {
		t.Errorf("Current = %d, want %d", s.Current, int64(3<<30))
	}
	if s.File != 2684354560 || s.Anon != 536870912 {
		t.Errorf("File/Anon = %d/%d", s.File, s.Anon)
	}
	if s.MajFaults != 4200 {
		t.Errorf("MajFaults = %d, want 4200", s.MajFaults)
	}
	if s.PressureAvg10 != 12.5 {
		t.Errorf("PressureAvg10 = %v, want 12.5 (must read the 'some' line, not 'full')", s.PressureAvg10)
	}
}

func TestWriteKnobs(t *testing.T) {
	g := fakeGroup(t)
	if err := g.SetMemoryLow(1 << 30); err != nil {
		t.Fatal(err)
	}
	if err := g.SetCPUWeight(1000); err != nil {
		t.Fatal(err)
	}
	low, _ := os.ReadFile(filepath.Join(g.Dir, "memory.low"))
	w, _ := os.ReadFile(filepath.Join(g.Dir, "cpu.weight"))
	if string(low) != "1073741824" || string(w) != "1000" {
		t.Errorf("memory.low=%q cpu.weight=%q", low, w)
	}
}

func TestReadTrigger(t *testing.T) {
	dir := t.TempDir()
	if ReadTrigger(dir, "cache_app") {
		t.Error("missing trigger file should mean normal")
	}
	os.WriteFile(filepath.Join(dir, "cache_app.trigger"), []byte("emergency\n"), 0644)
	if !ReadTrigger(dir, "cache_app") {
		t.Error("expected emergency")
	}
	os.WriteFile(filepath.Join(dir, "cache_app.trigger"), []byte("normal\n"), 0644)
	if ReadTrigger(dir, "cache_app") {
		t.Error("expected normal")
	}
}
