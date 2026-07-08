package terminal

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
)

// realistic-ish terminal output: a timestamped, colored log line.
var benchLine = []byte("\x1b[90m2026-07-08 16:40:02\x1b[0m \x1b[32mINFO\x1b[0m module=server msg=\"handled request\" dur=1.2ms status=200\r\n")

// BenchmarkEmulatorWrite: how fast the emulator ingests terminal output.
func BenchmarkEmulatorWrite(b *testing.B) {
	em := vt.NewSafeEmulator(120, 40)
	b.SetBytes(int64(len(benchLine)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		em.Write(benchLine)
	}
}

// BenchmarkEmulatorRender: cost of one screen snapshot (what renderLoop does).
func BenchmarkEmulatorRender(b *testing.B) {
	em := vt.NewSafeEmulator(120, 40)
	for i := 0; i < 60; i++ {
		em.Write(benchLine)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = em.Render()
	}
}

// TestEmulatorMemory reports heap per emulator with a full 10k-line scrollback.
func TestEmulatorMemory(t *testing.T) {
	if os.Getenv("HYDRA_BENCH") == "" {
		t.Skip("set HYDRA_BENCH=1")
	}
	const N = 20
	runtime.GC()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)

	ems := make([]*vt.SafeEmulator, N)
	for i := range ems {
		e := vt.NewSafeEmulator(120, 40)
		for j := 0; j < 10000; j++ { // fill the whole scrollback ring
			e.Write(benchLine)
		}
		ems[i] = e
	}
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	total := float64(m1.HeapAlloc-m0.HeapAlloc) / 1e6
	t.Logf("%d emulators, full 10k scrollback each: %.1f MB total, %.2f MB/emulator", N, total, total/N)
	runtime.KeepAlive(ems)
}

// TestDisplayLatency measures keystroke -> visible-on-screen round trip
// through a real head (PTY + shell echo + emulator + 33ms render cache).
func TestDisplayLatency(t *testing.T) {
	if os.Getenv("HYDRA_BENCH") == "" {
		t.Skip("set HYDRA_BENCH=1")
	}
	s, err := New("lat", os.TempDir(), "", 120, 40)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	time.Sleep(600 * time.Millisecond) // let the shell come up

	var samples []time.Duration
	for i := 0; i < 30; i++ {
		tok := fmt.Sprintf("MK%dQ", i)
		start := time.Now()
		s.Send([]byte(tok))
		for !strings.Contains(s.Render(), tok) {
			if time.Since(start) > 2*time.Second {
				t.Fatal("timeout waiting for echo")
			}
			time.Sleep(500 * time.Microsecond)
		}
		samples = append(samples, time.Since(start))
		s.Send([]byte{0x15}) // Ctrl+U clear line
		time.Sleep(40 * time.Millisecond)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	var sum time.Duration
	for _, d := range samples {
		sum += d
	}
	t.Logf("keystroke->screen latency over %d samples: min=%v median=%v p90=%v max=%v avg=%v",
		len(samples), samples[0], samples[len(samples)/2], samples[len(samples)*9/10], samples[len(samples)-1], sum/time.Duration(len(samples)))
}

// TestStressHeads spawns N heads all flooding output and samples aggregate
// CPU and memory. Run: HYDRA_STRESS=15 go test ./internal/terminal -run Stress -v
func TestStressHeads(t *testing.T) {
	nStr := os.Getenv("HYDRA_STRESS")
	if nStr == "" {
		t.Skip("set HYDRA_STRESS=N")
	}
	n, _ := strconv.Atoi(nStr)
	if n < 1 {
		n = 1
	}
	mgr := NewManager()
	defer mgr.CloseAll()

	for i := 0; i < n; i++ {
		// `yes` floods stdout as fast as the reader drains it — worst case.
		if _, err := mgr.Spawn(fmt.Sprintf("h%d", i), os.TempDir(), "yes hydra_stress_line_"+strconv.Itoa(i), 120, 40); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(1 * time.Second) // spin up

	u0, s0 := procCPU()
	rss0 := procRSS()
	start := time.Now()
	time.Sleep(5 * time.Second)
	u1, s1 := procCPU()
	elapsed := time.Since(start).Seconds()
	hz := float64(clockTicks())

	cpuCores := (float64(u1+s1-u0-s0) / hz) / elapsed
	// prove the UI-thread render path stays cheap under this load:
	rStart := time.Now()
	for _, h := range mgr.List() {
		_ = h.Render()
	}
	renderAll := time.Since(rStart)

	t.Logf("%d flooding heads: CPU=%.1f cores, RSS=%d MB (+%d MB), Render() all heads=%v",
		n, cpuCores, procRSS(), procRSS()-rss0, renderAll)
}

// ---- /proc helpers (Linux) ----

func procCPU() (utime, stime int64) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, 0
	}
	f := strings.Fields(string(data))
	if len(f) < 15 {
		return 0, 0
	}
	utime, _ = strconv.ParseInt(f[13], 10, 64)
	stime, _ = strconv.ParseInt(f[14], 10, 64)
	return
}

func procRSS() int64 {
	f, err := os.Open("/proc/self/statm")
	if err != nil {
		return 0
	}
	defer f.Close()
	var size, resident int64
	fmt.Fscan(bufio.NewReader(f), &size, &resident)
	return resident * int64(os.Getpagesize()) / (1 << 20)
}

func clockTicks() int64 {
	// USER_HZ is 100 on virtually all Linux; avoid cgo getconf.
	return 100
}
