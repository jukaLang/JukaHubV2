package main

import (
	"math"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func TestBootOSDecide(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	// A TrimUI that booted 40 s ago with "Start JukaHub at login" on, whose
	// autostart hook has started JukaHub in this boot.
	base := func() bootOSInputs {
		return bootOSInputs{TrimUI: true, Uptime: 40 * time.Second, UptimeOK: true, AutoStart: true, HookRan: true, Now: now}
	}
	check := func(name string, in bootOSInputs, want bool, reason string) {
		t.Helper()
		got, why := bootOSDecide(in)
		if got != want || why != reason {
			t.Errorf("%s: got (%v, %q), want (%v, %q)", name, got, why, want, reason)
		}
	}

	check("first start since the boot", base(), true, "started with the device")

	in := base()
	in.LastLaunch = now.Add(-10 * time.Second)
	check("JukaHub already ran in this boot (a relaunch)", in, false, "")

	in = base()
	in.LastLaunch = now.Add(-time.Hour)
	check("last start before the boot", in, true, "started with the device")

	in = base()
	in.LastLaunch = now.Add(48 * time.Hour)
	check("clock set back since the last start", in, true, "started with the device")

	in = base()
	in.Uptime = 10 * time.Minute
	check("long after the boot", in, false, "")

	in = base()
	in.AutoStart = false
	check("autostart off", in, false, "")

	in = base()
	in.TrimUI = false
	check("a desktop's uptime proves nothing", in, false, "")

	in = base()
	in.UptimeOK = false
	check("uptime unknown", in, false, "")

	in = base()
	in.Setting = bootOSAnimationQuick
	in.Env = "1"
	in.Marker = true
	check("Always quick wins over every request", in, false, "")

	in = base()
	in.TrimUI = false
	in.Env = " TRUE "
	check("JUKAHUB_BOOT", in, true, "JUKAHUB_BOOT")

	in = base()
	in.TrimUI = false
	in.Env = "0"
	check("JUKAHUB_BOOT=0 is not a request", in, false, "")

	in = base()
	in.TrimUI = false
	in.Args = []string{"-x", "--boot"}
	check("--boot", in, true, "--boot")

	in = base()
	in.Bench = true
	check("the bench measures the ordinary start", in, false, "")

	in = base()
	in.Bench = true
	in.Env = "yes"
	check("the bench with an explicit request", in, true, "JUKAHUB_BOOT")

	in = base()
	in.TrimUI = false
	in.Setting = bootOSAnimationAlways
	check("Full every time", in, true, "full boot on every start")

	in = base()
	in.Setting = bootOSAnimationAlways
	in.Bench = true
	check("Full every time is not for the bench", in, false, "")

	in = base()
	in.TrimUI = false
	in.Marker = true
	check("the autostart hook's marker", in, true, "autostart hook")

	// JukaHub restarting itself inherits the hook's marker, the environment
	// and the arguments: none of them makes the restart a boot.
	in = base()
	in.LastLaunch = now.Add(-10 * time.Second)
	in.Marker = true
	check("a respawn while the hook's marker is still there", in, false, "")

	in = base()
	in.LastLaunch = now.Add(-10 * time.Second)
	in.Env = "1"
	check("a respawn that inherited JUKAHUB_BOOT", in, false, "")

	in = base()
	in.TrimUI, in.UptimeOK = false, false
	in.Args = []string{"--boot"}
	in.LastLaunch = now.Add(-30 * time.Second)
	check("a desktop restart that kept --boot", in, false, "")

	in = base()
	in.TrimUI, in.UptimeOK = false, false
	in.Args = []string{"--boot"}
	in.LastLaunch = now.Add(-time.Hour)
	check("a desktop login with --boot", in, true, "--boot")

	in = base()
	in.LastLaunch = now.Add(-10 * time.Second)
	in.Setting = bootOSAnimationAlways
	check("Full every time means every start, relaunches included", in, true, "full boot on every start")

	// The uptime rule needs the hook's once-per-boot claim: a start from the
	// menu after the hook stood aside (a queued resume, last boot's start that
	// never finished) makes none.
	in = base()
	in.HookRan = false
	check("the hook did not start JukaHub this boot", in, false, "")

	// The kernel's boot id answers "since this boot?" outright.
	in = base()
	in.BootID, in.LastBootID = "b-2", "b-2"
	in.LastLaunch = now.Add(-30 * 24 * time.Hour)
	in.Marker = true
	check("same boot id: a restart, though the network set the clock forward since", in, false, "")

	in = base()
	in.BootID, in.LastBootID = "b-2", "b-1"
	in.LastLaunch = now.Add(-10 * time.Second)
	in.Marker = true
	check("another boot's id: the boot, though its last start looks recent", in, true, "autostart hook")

	in = base()
	in.BootID = "b-2"
	in.LastLaunch = now.Add(-10 * time.Second)
	check("no recorded id (an older build's record): the clock decides", in, false, "")
}

func TestBootOSContinuityReason(t *testing.T) {
	for _, r := range []string{bootOSReasonEnv, bootOSReasonArg, bootOSReasonMarker, bootOSReasonUptime} {
		if !bootOSContinuityReason(r) {
			t.Errorf("%q comes straight after the firmware's logo", r)
		}
	}
	for _, r := range []string{bootOSReasonAlways, ""} {
		if bootOSContinuityReason(r) {
			t.Errorf("%q can follow the menu, not the firmware's logo", r)
		}
	}
}

func TestBootOSRestartDropsTheBootRequest(t *testing.T) {
	args := bootOSRestartArgs([]string{"--safe-mode", "--boot", "x", "-boot"})
	if len(args) != 2 || args[0] != "--safe-mode" || args[1] != "x" {
		t.Fatalf("restart args %q", args)
	}
	env := bootOSRestartEnv([]string{"PATH=/bin", "JUKAHUB_BOOT=1", "HOME=/root", "JUKAHUB_BOOTX=1"})
	if len(env) != 3 || env[0] != "PATH=/bin" || env[1] != "HOME=/root" || env[2] != "JUKAHUB_BOOTX=1" {
		t.Fatalf("restart env %q", env)
	}
}

func TestBootOSInkOn(t *testing.T) {
	boot := sdl.Color{R: 238, G: 243, B: 255, A: 255}
	themeDark := sdl.Color{R: 18, G: 22, B: 30, A: 255}
	themeLight := sdl.Color{R: 230, G: 236, B: 246, A: 255}
	lightBase := sdl.Color{R: 245, G: 247, B: 250, A: 255}
	scale := func(c sdl.Color, d float64) sdl.Color {
		return sdl.Color{R: uint8(float64(c.R) * d), G: uint8(float64(c.G) * d), B: uint8(float64(c.B) * d), A: 255}
	}
	// A light theme: the boot's light ink over the dark start, the theme's
	// dark ink once the backdrop is light, one switch in between, and the word
	// never less legible than the better of the two.
	switched := 0
	prev := boot
	for d := 0.0; d <= 1.0001; d += 0.01 {
		bg := scale(lightBase, d)
		ink := bootOSInkOn(boot, themeDark, bg, d, true)
		if ink != boot && ink != themeDark {
			t.Fatalf("dawn %.2f: a blend of the inks %+v", d, ink)
		}
		if ink != prev {
			switched++
			prev = ink
		}
		best := math.Max(bootOSContrast(boot, bg), bootOSContrast(themeDark, bg))
		if got := bootOSContrast(ink, bg); got < best-1e-9 {
			t.Fatalf("dawn %.2f: contrast %.2f, the other ink has %.2f", d, got, best)
		}
	}
	if switched != 1 {
		t.Fatalf("the ink switched %d times across the dawn, want once", switched)
	}
	if got := bootOSInkOn(boot, themeDark, scale(lightBase, 0), 0, true); got != boot {
		t.Fatalf("on black the boot's ink stays, got %+v", got)
	}
	if got := bootOSInkOn(boot, themeDark, lightBase, 1, true); got != themeDark {
		t.Fatalf("on the light backdrop the theme's ink, got %+v", got)
	}
	// A dark theme: both inks are light, so they blend with the dawn.
	if got := bootOSInkOn(boot, themeLight, sdl.Color{A: 255}, 0, false); got != boot {
		t.Fatalf("dark theme at the start: %+v", got)
	}
	if got := bootOSInkOn(boot, themeLight, sdl.Color{A: 255}, 1, false); got != themeLight {
		t.Fatalf("dark theme at full dawn: %+v", got)
	}
	if c := bootOSContrast(sdl.Color{A: 255}, sdl.Color{R: 255, G: 255, B: 255, A: 255}); math.Abs(c-21) > 1e-6 {
		t.Fatalf("black on white is %.3f:1, want 21:1", c)
	}
}

func TestBootOSParseUptime(t *testing.T) {
	d, ok := bootOSParseUptime("35.51 120.20\n")
	if !ok || math.Abs(d.Seconds()-35.51) > 1e-6 {
		t.Fatalf("got %v %v, want 35.51s", d, ok)
	}
	for _, bad := range []string{"", "   ", "abc 1", "-1 0", "NaN 0", "+Inf 0"} {
		if _, ok := bootOSParseUptime(bad); ok {
			t.Errorf("%q parsed as an uptime", bad)
		}
	}
}

func TestLaunchPredatesBoot(t *testing.T) {
	savedUp, savedID := bootOSUptimeFn, bootOSBootIDFn
	defer func() { bootOSUptimeFn, bootOSBootIDFn = savedUp, savedID }()
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

	bootOSBootIDFn = func() string { return "" }
	bootOSUptimeFn = func() (time.Duration, bool) { return time.Minute, true }
	if launchPredatesBoot(now.Add(-30*time.Second), "", now) {
		t.Error("a launch 30 s into a one-minute boot belongs to this boot")
	}
	if !launchPredatesBoot(now.Add(-2*time.Minute), "", now) {
		t.Error("a launch two minutes ago predates a one-minute boot")
	}
	if launchPredatesBoot(time.Time{}, "", now) {
		t.Error("no launch on record is not a launch before the boot")
	}

	bootOSUptimeFn = func() (time.Duration, bool) { return 0, false }
	if launchPredatesBoot(now.Add(-2*time.Minute), "", now) {
		t.Error("an unknown uptime must keep the old warm-start rule")
	}

	// With both boot ids the clock is not consulted at all.
	bootOSBootIDFn = func() string { return "b-2" }
	bootOSUptimeFn = func() (time.Duration, bool) { return time.Minute, true }
	if launchPredatesBoot(now.Add(-30*24*time.Hour), "b-2", now) {
		t.Error("a launch with this boot's id is this boot's, whatever the clock says")
	}
	if !launchPredatesBoot(now.Add(-10*time.Second), "b-1", now) {
		t.Error("a launch with another boot's id predates this boot")
	}
	if launchPredatesBoot(now.Add(-30*time.Second), "", now) {
		t.Error("a record without an id falls back to the uptime")
	}
}

func TestBootOSDelayedScoreLandsTheBellsOnTheBlooms(t *testing.T) {
	out := bootOSDelayedScore(chimeStartScore, bootOSChimeDelay)
	if len(out) != len(chimeStartScore) {
		t.Fatalf("%d notes, want %d", len(out), len(chimeStartScore))
	}
	for i := range out {
		if math.Abs(out[i].at-chimeStartScore[i].at-bootOSChimeDelay) > 1e-12 ||
			out[i].kind != chimeStartScore[i].kind || out[i].freq != chimeStartScore[i].freq {
			t.Fatalf("note %d: %+v from %+v", i, out[i], chimeStartScore[i])
		}
	}
	if out[0].at == chimeStartScore[0].at {
		t.Fatal("the shared score was changed in place")
	}
	// The four climbing bells (the sparkle's are far quieter) strike on the
	// beats that ignite the four blooms.
	bells := 0
	for _, n := range out {
		if n.kind != chimeBell || n.level < 0.1 {
			continue
		}
		if bells < len(bootOSPowerBeats.bells) && math.Abs(n.at*1000-bootOSPowerBeats.bells[bells]) > 0.5 {
			t.Errorf("bell %d at %.0f ms, bloom at %.0f ms", bells, n.at*1000, bootOSPowerBeats.bells[bells])
		}
		bells++
	}
	if bells != 4 {
		t.Fatalf("%d climbing bells, want 4", bells)
	}
}

func TestBootOSPanePointRunsRoundTheRim(t *testing.T) {
	pane := sdl.FRect{X: 10, Y: 20, W: 120, H: 120}
	const rad = 27.6
	x0, y0 := bootOSPanePoint(pane, rad, 0)
	if x1, y1 := bootOSPanePoint(pane, rad, 1); math.Hypot(x1-x0, y1-y0) > 1e-9 {
		t.Fatalf("the lap does not close: (%.3f,%.3f) vs (%.3f,%.3f)", x0, y0, x1, y1)
	}
	const n = 2000
	perimeter := 4*(120-2*rad) + 2*math.Pi*rad
	step := perimeter / n
	px, py := x0, y0
	for i := 1; i <= n; i++ {
		x, y := bootOSPanePoint(pane, rad, float64(i)/n)
		if d := math.Hypot(x-px, y-py); d > step*1.001+1e-9 {
			t.Fatalf("jump of %.3f px at sample %d (step %.3f)", d, i, step)
		}
		if x < 10-1e-9 || x > 130+1e-9 || y < 20-1e-9 || y > 140+1e-9 {
			t.Fatalf("sample %d at (%.3f,%.3f) leaves the pane", i, x, y)
		}
		if sdf := bootRoundRectSDF(x-70, y-80, 60, 60, rad); math.Abs(sdf) > 1e-6 {
			t.Fatalf("sample %d at (%.3f,%.3f) is %.6f px off the rim", i, x, y, sdf)
		}
		px, py = x, y
	}
	ax, ay := bootOSPanePoint(pane, rad, 0.3)
	if bx, by := bootOSPanePoint(pane, rad, -0.7); math.Hypot(ax-bx, ay-by) > 1e-9 {
		t.Fatalf("u wraps: 0.3 → (%.3f,%.3f), -0.7 → (%.3f,%.3f)", ax, ay, bx, by)
	}
	// A square pane: an eighth of the lap is the middle of the top edge.
	if x, y := bootOSPanePoint(pane, 0, 0.125); math.Abs(x-70) > 1e-9 || math.Abs(y-20) > 1e-9 {
		t.Fatalf("square pane: (%.3f,%.3f), want (70,20)", x, y)
	}
	// The real pane is the layout's.
	sprite := bootLogoPlace(1280, 720, 0).Sprite
	logoPane, logoRad := bootLogoPaneRect(sprite)
	for i := 0; i < 64; i++ {
		x, y := bootOSPanePoint(logoPane, logoRad, float64(i)/64)
		if x < float64(sprite.X) || x > float64(sprite.X+sprite.W) || y < float64(sprite.Y) || y > float64(sprite.Y+sprite.H) {
			t.Fatalf("the light leaves the logo at (%.1f,%.1f)", x, y)
		}
	}
}

func TestBootOSTimelineSettles(t *testing.T) {
	for _, powerOn := range []bool{true, false} {
		for _, motion := range []bool{true, false} {
			prev := bootOSTimeline(0, powerOn, motion, false)
			if prev.logo != 0 || prev.word != 0 {
				t.Fatalf("powerOn=%v motion=%v: a boot without the firmware's logo starts from nothing, got %+v", powerOn, motion, prev)
			}
			for ms := 10.0; ms <= 4000; ms += 10 {
				f := bootOSTimeline(ms, powerOn, motion, false)
				if f.logo < prev.logo || f.word < prev.word || f.dawn < prev.dawn || f.track < prev.track || f.emerge < prev.emerge-1e-12 {
					t.Fatalf("powerOn=%v motion=%v: went back at %.0f ms: %+v after %+v", powerOn, motion, ms, f, prev)
				}
				prev = f
			}
			end := bootOSTimeline(4000, powerOn, motion, false)
			if end.logo != 1 || end.word != 1 || end.dawn != 1 || end.track != 1 || end.emerge != 1 {
				t.Errorf("powerOn=%v motion=%v: not settled at 4 s: %+v", powerOn, motion, end)
			}
			for i, b := range end.blob {
				if math.Abs(b-1) > 1e-3 {
					t.Errorf("powerOn=%v motion=%v: bloom %d at %.3f after 4 s", powerOn, motion, i, b)
				}
			}
		}
	}
	// The firmware's logo is on screen: the boot starts from that picture.
	if f := bootOSTimeline(0, true, true, true); f.logo != 1 || f.word != 1 || f.logoScale != 1 {
		t.Fatalf("continuity must start with the logo and the wordmark up, got %+v", f)
	}
	// Each bloom ignites on its own bell, left to right across the screen.
	at := func(i int) float64 {
		for ms := 0.0; ms < 3000; ms++ {
			if bootOSTimeline(ms, true, true, true).blob[i] > 0.5 {
				return ms
			}
		}
		return -1
	}
	order := []int{3, 0, 2, 1} // bootBlobs by their resting x
	for k := 1; k < len(order); k++ {
		if at(order[k]) <= at(order[k-1]) {
			t.Errorf("bloom %d ignites at %.0f ms, not after bloom %d at %.0f ms", order[k], at(order[k]), order[k-1], at(order[k-1]))
		}
	}
}

func TestBootOSTimelineReducedMotionDoesNotMove(t *testing.T) {
	for _, powerOn := range []bool{true, false} {
		for ms := 0.0; ms <= 4000; ms += 5 {
			f := bootOSTimeline(ms, powerOn, false, false)
			if f.logoScale != 1 || f.comet >= 0 || f.glint != 0 || f.emerge != 1 {
				t.Fatalf("powerOn=%v at %.0f ms: reduced motion moved something: %+v", powerOn, ms, f)
			}
		}
	}
	// With motion the light runs exactly once round the pane.
	for _, powerOn := range []bool{true, false} {
		laps, was := 0, false
		for ms := 0.0; ms <= 4000; ms += 5 {
			running := bootOSTimeline(ms, powerOn, true, false).comet >= 0
			if running && !was {
				laps++
			}
			was = running
		}
		if laps != 1 {
			t.Errorf("powerOn=%v: %d laps of light, want 1", powerOn, laps)
		}
	}
}

func TestBootOSAnimationSetting(t *testing.T) {
	if got := bootOSAnimationSetting(nil); got != bootOSAnimationPower {
		t.Fatalf("no config: %q", got)
	}
	c := &Config{}
	if got := bootOSAnimationSetting(c); got != bootOSAnimationPower || bootOSAnimationCurrent(c) != 0 {
		t.Fatalf("unset: %q", got)
	}
	for value, want := range map[string]string{" ALWAYS ": bootOSAnimationAlways, "quick": bootOSAnimationQuick, "sideways": bootOSAnimationPower} {
		c.Variables.Custom = map[string]interface{}{bootOSAnimationKey: value}
		if got := bootOSAnimationSetting(c); got != want {
			t.Errorf("%q → %q, want %q", value, got, want)
		}
	}
	opts := bootOSAnimationOptions(c)
	if len(opts) != 3 || opts[0].Value != bootOSAnimationPower || opts[1].Value != bootOSAnimationAlways || opts[2].Value != bootOSAnimationQuick {
		t.Fatalf("options %+v", opts)
	}
	for i, o := range opts {
		c.Variables.Custom = map[string]interface{}{bootOSAnimationKey: o.Value}
		if got := bootOSAnimationCurrent(c); got != i {
			t.Errorf("%q is stop %d, want %d", o.Value, got, i)
		}
		if bootOSAnimationDetail(c) == "" || o.Label == "" || o.Note == "" {
			t.Errorf("%q: empty text", o.Value)
		}
	}
}

func TestBootOSPreviewProgress(t *testing.T) {
	prev := -1.0
	for ms := 0.0; ms <= 3000; ms += 20 {
		frac, label := bootOSPreviewProgress(ms)
		if frac < prev || frac < 0 || frac > 1 || label == "" {
			t.Fatalf("at %.0f ms: %.3f %q after %.3f", ms, frac, label, prev)
		}
		prev = frac
	}
	if frac, label := bootOSPreviewProgress(bootOSPreviewSettleMS); frac != 1 || label != "Ready" {
		t.Fatalf("settled preview: %.3f %q", frac, label)
	}
}

func TestBootOSTexturePixels(t *testing.T) {
	dot := bootOSDotPixels(bootOSDotSize)
	if len(dot) != bootOSDotSize*bootOSDotSize*4 {
		t.Fatalf("dot is %d bytes", len(dot))
	}
	alphaAt := func(pix []byte, n, x, y int) byte { return pix[(y*n+x)*4+3] }
	if c := alphaAt(dot, bootOSDotSize, bootOSDotSize/2, bootOSDotSize/2); c < 240 {
		t.Errorf("the dot's centre is only %d", c)
	}
	if c := alphaAt(dot, bootOSDotSize, 0, 0); c != 0 {
		t.Errorf("the dot's corner shows (%d): a scaled copy would show its square", c)
	}

	const side, pad = 180, 13
	ring := bootOSRingPixels(side, pad)
	n := side + 2*pad
	if len(ring) != n*n*4 {
		t.Fatalf("ring is %d bytes", len(ring))
	}
	pane, _ := bootLogoPaneRect(sdl.Rect{X: pad, Y: pad, W: side, H: side})
	rimX, rimY := int(float64(pane.X)+float64(pane.W)/2), int(pane.Y)
	if c := alphaAt(ring, n, rimX, rimY); c < 200 {
		t.Errorf("the rim's top is only %d", c)
	}
	if c := alphaAt(ring, n, n/2, n/2); c != 0 {
		t.Errorf("the pane's middle glows (%d)", c)
	}
	if c := alphaAt(ring, n, 0, 0); c != 0 {
		t.Errorf("the ring texture's corner shows (%d)", c)
	}
}
