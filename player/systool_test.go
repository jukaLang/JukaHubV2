package main

// systool_test.go — the logic behind the system tool screens (Speed test,
// Benchmark, Hardware, Storage, Logs) and the in-memory clean-up of the
// controls those screens and the Terminal and Scheduled tasks no longer carry
// in their configs. Everything here is pure, so it runs without a renderer.

import (
	"strings"
	"testing"
	"time"
)

// sysToolTestElements lists a scene's elements as "type:variable".
func sysToolTestElements(scene SceneConfig) string {
	var parts []string
	for _, el := range scene.Elements {
		parts = append(parts, el.Type+":"+el.Variable)
	}
	return strings.Join(parts, ",")
}

// The screens draw their own controls, so a config button that is not drawn
// but can take focus (what used to swallow the Terminal's arrows) is dropped
// in memory. Panels stay; other scenes are not touched; a second pass changes
// nothing.
func TestSysToolMigrateScenesDropsReplacedControls(t *testing.T) {
	config := &Config{Scenes: []SceneConfig{
		{Name: "Terminal", Elements: []Element{
			{Type: "textlog", Variable: "terminal_text"},
			{Type: "chat", Variable: "terminal_input"},
			{Type: "button", Text: "Run", Trigger: "terminal_run"},
		}},
		{Name: "Cron", Elements: []Element{
			{Type: "button", Text: "Load", Trigger: "cron_load"},
			{Type: "button", Text: "Startup", Trigger: "startup_load"},
			{Type: "textlog", Variable: "cron_text"},
			{Type: "label", Text: " Startup Items "},
			{Type: "textlog", Variable: cronStartupVar},
		}},
		{Name: "NetSpeed", Elements: []Element{
			{Type: "textlog", Variable: "netspeed_text"},
			{Type: "button", Text: "Run", Trigger: "netspeed_run"},
		}},
		{Name: "DiskSpace", Elements: []Element{{Type: "piechart"}}},
		{Name: "Main", Elements: []Element{{Type: "button", Text: "Tools"}}},
	}}
	want := []struct{ scene, elements string }{
		{"Terminal", "textlog:terminal_text"},
		{"Cron", "textlog:cron_text"},
		{"NetSpeed", "textlog:netspeed_text"},
		{"DiskSpace", "piechart:"},
		{"Main", "button:"},
	}
	for pass := 1; pass <= 2; pass++ {
		sysToolMigrateScenes(config)
		for i, w := range want {
			if got := sysToolTestElements(config.Scenes[i]); got != w.elements {
				t.Errorf("pass %d: %s keeps %q, want %q", pass, w.scene, got, w.elements)
			}
		}
	}
	sysToolMigrateScenes(nil) // a missing config is not a crash
}

func TestSysToolCPUTimes(t *testing.T) {
	prev, ok := sysToolParseCPUTimes("cpu  100 0 50 800 50 0 0 0 0 0")
	if !ok || prev.total != 1000 || prev.busy != 150 {
		t.Fatalf("first read = %+v (%v), want total 1000 and busy 150", prev, ok)
	}
	cur, ok := sysToolParseCPUTimes("cpu  200 0 100 1500 100 0 0 0")
	if !ok {
		t.Fatal("the second read did not parse")
	}
	if got := sysToolCPUPercent(prev, cur); got < 16.6 || got > 16.7 {
		t.Errorf("usage = %.2f%%, want 16.67%% (150 busy of 900)", got)
	}
	if got := sysToolCPUPercent(cur, cur); got != 0 {
		t.Errorf("usage with no time between reads = %v, want 0", got)
	}
	for _, bad := range []string{"cpu0 1 2 3 4 5", "cpu 1 2 3", "cpu a b c d e", "intr 1 2 3 4 5"} {
		if _, ok := sysToolParseCPUTimes(bad); ok {
			t.Errorf("%q was read as the aggregate CPU line", bad)
		}
	}
}

func TestSysToolParseMeminfo(t *testing.T) {
	used, total, ok := sysToolParseMeminfo("MemTotal:        2048 kB\nMemFree:          256 kB\nMemAvailable:    1536 kB\n")
	if !ok || total != 2048*1024 || used != 512*1024 {
		t.Fatalf("meminfo = used %d of %d (%v), want 512 KiB of 2048 KiB", used, total, ok)
	}
	if _, _, ok := sysToolParseMeminfo("MemTotal: 2048 kB\nMemFree: 256 kB\n"); ok {
		t.Error("a meminfo without MemAvailable was accepted")
	}
	if _, _, ok := sysToolParseMeminfo("MemTotal: 100 kB\nMemAvailable: 200 kB\n"); ok {
		t.Error("more memory available than installed was accepted")
	}
}

func TestSysToolParseLogLine(t *testing.T) {
	cases := []struct {
		line, clock, where, text string
		level                    int
	}{
		{"[legacy] 15:04:05 terminal.go:123: Started job", "15:04:05", "terminal.go:123", "Started job", sysToolLogInfo},
		{"09:30:00 sched.go:9: schtasks failed: exit status 1", "09:30:00", "sched.go:9", "schtasks failed: exit status 1", sysToolLogError},
		{"[legacy] 10:00:00 Error: something", "10:00:00", "", "Error: something", sysToolLogError},
		{"battery warning at 15%", "", "", "battery warning at 15%", sysToolLogWarn},
	}
	for _, c := range cases {
		r := sysToolParseLogLine(c.line)
		if r.clock != c.clock || r.where != c.where || r.text != c.text || r.level != c.level || r.raw != c.line {
			t.Errorf("parse(%q) = clock %q where %q text %q level %d, want %q %q %q %d",
				c.line, r.clock, r.where, r.text, r.level, c.clock, c.where, c.text, c.level)
		}
	}
}

func TestSysToolLogLevel(t *testing.T) {
	cases := []struct {
		text  string
		level int
	}{
		{"panic: runtime error: index out of range", sysToolLogError},
		{"[NET] request timed out", sysToolLogWarn},
		{"could not open logs.txt", sysToolLogWarn},
		{"Loaded 12 apps", sysToolLogInfo},
	}
	for _, c := range cases {
		if got := sysToolLogLevel(c.text); got != c.level {
			t.Errorf("level(%q) = %d, want %d", c.text, got, c.level)
		}
	}
}

func TestSysToolNetStats(t *testing.T) {
	in := []float64{40, 10, 30, 20}
	if got := sysToolNetMedian(in); got != 25 {
		t.Errorf("median of %v = %v, want 25", in, got)
	}
	if in[0] != 40 || in[3] != 20 {
		t.Errorf("the median sorted the caller's samples: %v", in)
	}
	if got := sysToolNetMedian([]float64{3, 1, 2}); got != 2 {
		t.Errorf("median of three = %v, want 2", got)
	}
	if got := sysToolNetMedian(nil); got != 0 {
		t.Errorf("median of nothing = %v, want 0", got)
	}
	if got := sysToolNetJitter([]float64{10, 14, 12}); got != 3 {
		t.Errorf("jitter = %v, want 3 (changes of 4 and 2)", got)
	}
	if got := sysToolNetJitter([]float64{5}); got != 0 {
		t.Errorf("jitter of one sample = %v, want 0", got)
	}
}

func TestSysToolNetRatingAndFormat(t *testing.T) {
	cases := []struct {
		mbps   float64
		text   string
		rating string
	}{
		{2.5, "2.50", "Enough for music and web pages"},
		{8, "8.00", "Good for HD video"},
		{12.34, "12.3", "Great for HD streaming"},
		{42.26, "42.3", "Great for 4K streaming and big downloads"},
		{250.4, "250", "Blazing fast"},
	}
	for _, c := range cases {
		if got := sysToolNetMbps(c.mbps); got != c.text {
			t.Errorf("format(%v) = %q, want %q", c.mbps, got, c.text)
		}
		if got := sysToolNetRating(c.mbps); got != c.rating {
			t.Errorf("rating(%v) = %q, want %q", c.mbps, got, c.rating)
		}
	}
}

func TestSysToolDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "under a minute"},
		{12 * time.Minute, "12m"},
		{3*time.Hour + 12*time.Minute, "3h 12m"},
		{50 * time.Hour, "2d 2h"},
	}
	for _, c := range cases {
		if got := sysToolDuration(c.d); got != c.want {
			t.Errorf("duration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// The history behind a sparkline keeps the latest samples, oldest first.
func TestSysToolRing(t *testing.T) {
	var r sysToolRing
	var buf [sysToolRingSize]float64
	if got := r.ordered(&buf); len(got) != 0 || r.peak() != 0 {
		t.Fatalf("an empty ring has %v (peak %v)", got, r.peak())
	}
	for i := 1; i <= 3; i++ {
		r.push(float64(i))
	}
	if got := r.ordered(&buf); len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("three samples = %v, want [1 2 3]", got)
	}
	last := sysToolRingSize + 5
	for i := 4; i <= last; i++ {
		r.push(float64(i))
	}
	got := r.ordered(&buf)
	if len(got) != sysToolRingSize || got[0] != 6 || got[len(got)-1] != float64(last) {
		t.Fatalf("a wrapped ring holds %d samples from %v to %v, want %d from 6 to %d",
			len(got), got[0], got[len(got)-1], sysToolRingSize, last)
	}
	if r.peak() != float64(last) {
		t.Errorf("peak = %v, want %d", r.peak(), last)
	}
}

// A Hardware sample is formatted once, when it arrives, and a round without
// readings keeps what the tiles showed.
func TestSysToolHWApplyFormatsOnce(t *testing.T) {
	var d sysToolHWData
	d.info.cores = 4
	sysToolHWApply(&d, sysToolHWSample{
		cpuPct:    42.4,
		cpuOK:     true,
		memUsed:   512 << 20,
		memTotal:  1 << 30,
		memOK:     true,
		tempC:     55,
		tempOK:    true,
		freqMHz:   1800,
		battery:   15,
		charging:  true,
		uptime:    3*time.Hour + 5*time.Minute,
		uptimeOK:  true,
		load:      "0.50",
		diskRead:  true,
		diskTotal: 100,
		diskFree:  25,
	})
	checks := []struct{ name, got, want string }{
		{"cpu", d.cpuVal, "42"},
		{"cpu line", d.cpuSub, "1.8 GHz · load 0.50"},
		{"memory", d.memVal, "50"},
		{"temperature", d.tempVal, "55.0"},
		{"temperature line", d.tempSub, "Warm"},
		{"battery", d.batVal, "15"},
		{"battery line", d.batSub, "Charging"},
		{"storage", d.diskVal, "75"},
		{"status", d.status, "Live · up 3h 5m"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if !strings.Contains(d.memSub, " of ") || !strings.Contains(d.diskSub, " free of ") {
		t.Errorf("memory line %q / storage line %q do not say how much of what", d.memSub, d.diskSub)
	}
	if d.cpuHist.n != 1 || d.memHist.n != 1 || d.tempHist.n != 1 {
		t.Errorf("history lengths = %d %d %d, want one sample each", d.cpuHist.n, d.memHist.n, d.tempHist.n)
	}

	sysToolHWApply(&d, sysToolHWSample{battery: -1})
	if d.cpuVal != "42" || d.cpuHist.n != 1 {
		t.Errorf("a round without a CPU reading changed the tile to %q (history %d)", d.cpuVal, d.cpuHist.n)
	}
	if d.batOK || d.batVal != "—" || d.tempOK || d.tempVal != "—" {
		t.Errorf("missing battery/temperature shown as %q/%q, want dashes", d.batVal, d.tempVal)
	}
	if d.diskVal != "75" {
		t.Errorf("a round that did not read the storage changed it to %q", d.diskVal)
	}
}

// The end of a run that a newer one replaced is not news: the previous visit's
// cancelled run winding down must not mark the new run as stopped.
func TestSysToolBenchIgnoresAReplacedRunsEnd(t *testing.T) {
	sysToolBench.mu.Lock()
	prev := sysToolBench.d
	sysToolBench.mu.Unlock()
	prevServed := sysToolBenchServed.Load()
	t.Cleanup(func() {
		sysToolBenchUpdate(func(d *sysToolBenchData) { *d = prev })
		sysToolBenchServed.Store(prevServed)
	})

	stale := sysToolBenchBegin()
	fresh := sysToolBenchBegin()
	sysToolBenchEnd(stale, true)
	sysToolBench.mu.Lock()
	running, cancelled := sysToolBench.d.running, sysToolBench.d.cancelled
	sysToolBench.mu.Unlock()
	if !running || cancelled {
		t.Fatalf("the replaced run's end stopped the new one: running=%v cancelled=%v", running, cancelled)
	}

	sysToolBenchEnd(fresh, false)
	sysToolBench.mu.Lock()
	running, at := sysToolBench.d.running, sysToolBench.d.at
	sysToolBench.mu.Unlock()
	if running || at.IsZero() {
		t.Fatalf("the current run's end was not recorded: running=%v at=%v", running, at)
	}
}

// Every one of these screens says how to leave it, in every state its footer
// can be in right now; a scene that is not one of them is left to the generic
// footer.
func TestSysToolScreensAlwaysOfferBack(t *testing.T) {
	for name := range sysToolModuleScenes {
		hints, ok := sysToolFooterHints(name)
		if !ok {
			t.Errorf("%s has no footer of its own", name)
			continue
		}
		back := false
		for _, h := range hints {
			if h.Button == "B" {
				back = true
			}
		}
		if !back {
			t.Errorf("%s's footer does not offer B: %+v", name, hints)
		}
	}
	if _, ok := sysToolFooterHints("Main"); ok {
		t.Error("the generic Main scene was given a tool screen's footer")
	}
}
