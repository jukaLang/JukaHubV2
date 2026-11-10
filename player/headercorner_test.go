package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// writeSupply lays out one power_supply entry in a fake sysfs.
func writeSupply(t *testing.T, base, name string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, body := range files {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The Smart Pro's battery says "Discharging" on the cable; its USB input is
// what says a charger is plugged in. A battery that reports charging, a mains
// adapter and a name-only charger count too; an idle input does not.
func TestBatteryChargingIn(t *testing.T) {
	base := t.TempDir()
	writeSupply(t, base, "axp2202-battery", map[string]string{"type": "Battery", "capacity": "64", "status": "Discharging"})
	usb := writeSupply(t, base, "axp2202-usb", map[string]string{"type": "USB", "online": "0"})
	if batteryChargingIn(base) {
		t.Fatal("unplugged must not read as charging")
	}
	if err := os.WriteFile(filepath.Join(usb, "online"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !batteryChargingIn(base) {
		t.Fatal("an online USB input must read as charging (the Smart Pro case)")
	}

	full := t.TempDir()
	writeSupply(t, full, "BAT0", map[string]string{"type": "Battery", "capacity": "100", "status": "Not charging"})
	if !batteryChargingIn(full) {
		t.Fatal("\"Not charging\" is a battery held on the cable")
	}

	named := t.TempDir()
	writeSupply(t, named, "bq-battery", map[string]string{"capacity": "40", "status": "Unknown"})
	writeSupply(t, named, "bq-ac", map[string]string{"online": "1"})
	if !batteryChargingIn(named) {
		t.Fatal("an online input named like a charger must count without a type")
	}

	stray := t.TempDir()
	writeSupply(t, stray, "battery", map[string]string{"capacity": "40", "status": "Discharging", "online": "1"})
	writeSupply(t, stray, "tracker", map[string]string{"online": "1"})
	if batteryChargingIn(stray) {
		t.Fatal("neither a battery nor an unrelated device is a charger")
	}

	if batteryChargingIn(filepath.Join(base, "missing")) {
		t.Fatal("no power_supply directory means not charging")
	}

	// With a charger input present, its online reading decides: a fuel gauge
	// that latches "Full" at 100% must not show the bolt on battery, while a
	// battery that says "Charging" is charging whatever the input reports.
	latched := t.TempDir()
	writeSupply(t, latched, "axp2202-battery", map[string]string{"type": "Battery", "capacity": "100", "status": "Full"})
	writeSupply(t, latched, "axp2202-usb", map[string]string{"type": "USB", "online": "0"})
	if batteryChargingIn(latched) {
		t.Fatal("a latched Full with the charger input offline is not charging")
	}
	reports := t.TempDir()
	writeSupply(t, reports, "BAT0", map[string]string{"type": "Battery", "capacity": "60", "status": "Charging"})
	writeSupply(t, reports, "AC", map[string]string{"type": "Mains", "online": "0"})
	if !batteryChargingIn(reports) {
		t.Fatal("a battery that says Charging is charging")
	}
	held := t.TempDir()
	writeSupply(t, held, "BAT0", map[string]string{"type": "Battery", "capacity": "100", "status": "Not charging"})
	writeSupply(t, held, "AC", map[string]string{"type": "Mains", "online": "1"})
	if !batteryChargingIn(held) {
		t.Fatal("held full on an online charger is plugged in")
	}

	// Real sysfs entries are symlinks into the device tree.
	if runtime.GOOS != "windows" {
		linked := t.TempDir()
		devices := t.TempDir()
		target := writeSupply(t, devices, "usb0", map[string]string{"type": "USB", "online": "1"})
		if err := os.Symlink(target, filepath.Join(linked, "axp2202-usb")); err != nil {
			t.Fatal(err)
		}
		if !batteryChargingIn(linked) {
			t.Fatal("a charger reached through a symlink must count")
		}
	}
}

// The level's history behind batteryHoldingCharge: a cable that just went in,
// a level that holds and a level that rises are not falling; a drop below the
// peak since the cable went in is, and it stays so — no window ages it out,
// which is what made the battery saver flap on a charger that cannot keep up —
// until the level is back at the peak.
func TestBatteryTrend(t *testing.T) {
	var s batteryTrendState
	if s.falling() {
		t.Fatal("no reading cannot be falling")
	}
	s = s.note(10)
	if s.falling() {
		t.Fatal("one reading cannot show a drop")
	}
	s = s.note(10)
	if s.falling() {
		t.Fatal("a level that holds is not falling")
	}
	s = s.note(9)
	if !s.falling() {
		t.Fatal("a drop from the peak is falling")
	}
	s = s.note(9)
	if !s.falling() {
		t.Fatal("a drop must stay falling while the level stays below the peak")
	}
	s = s.note(10)
	if s.falling() {
		t.Fatal("back at the peak, the charger is keeping up again")
	}
	s = s.note(11)
	if s.falling() || s.peak != 11 {
		t.Fatalf("a rising level raises the peak: %+v", s)
	}
	s = s.note(-1)
	if s.newest != 11 || s.falling() {
		t.Fatalf("an unknown reading must not change the history: %+v", s)
	}
}

// A change of the cable state starts the history over: the drain before a
// cable went in never counts against it.
func TestBatteryTrendNoteResetsOnCableChange(t *testing.T) {
	batteryTrend.mu.Lock()
	savedCable, savedState := batteryTrend.cableIn, batteryTrend.state
	batteryTrend.mu.Unlock()
	defer func() {
		batteryTrend.mu.Lock()
		batteryTrend.cableIn, batteryTrend.state = savedCable, savedState
		batteryTrend.mu.Unlock()
	}()
	batteryTrendNote(50, false)
	batteryTrendNote(50, true)
	batteryTrendNote(49, true)
	batteryTrend.mu.Lock()
	falling := batteryTrend.state.falling()
	batteryTrend.mu.Unlock()
	if !falling {
		t.Fatal("a drop with the cable in must read as falling")
	}
	batteryTrendNote(49, false)
	batteryTrendNote(49, true)
	batteryTrend.mu.Lock()
	falling = batteryTrend.state.falling()
	batteryTrend.mu.Unlock()
	if falling {
		t.Fatal("a cable that went in again starts a fresh history")
	}
}

// A crowded bar gives up the network meter, the forecast and the offline badge
// before the volume button, and the button before the clock and the battery.
func TestStatusDropIndexKeepsTheVolumeButton(t *testing.T) {
	parts := []string{statusVolumePart, statusWiFiMarker, "21C / 12C", "12:40", "87%"}
	order := []string{}
	for len(parts) > 2 {
		i := statusDropIndex(parts)
		order = append(order, parts[i])
		parts = append(parts[:i:i], parts[i+1:]...)
	}
	want := []string{statusWiFiMarker, "21C / 12C", statusVolumePart}
	if len(order) != len(want) {
		t.Fatalf("dropped %q, want %q", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("dropped %q, want %q", order, want)
		}
	}

	offline := []string{statusVolumePart, "! Offline", "12:40", "87%"}
	if got := offline[statusDropIndex(offline)]; got != "! Offline" {
		t.Fatalf("offline bar dropped %q first, want the badge", got)
	}
}

// The speaker's target and the meter's split the gap between them, so a tap
// there belongs to exactly one; the meter still stops short of the forecast.
func TestStatusFingerRectSplitsTheGap(t *testing.T) {
	vol := sdl.Rect{X: 900, Y: 10, W: 28, H: 30}
	wifi := sdl.Rect{X: 940, Y: 10, W: 30, H: 30}
	weather := sdl.Rect{X: 984, Y: 10, W: 80, H: 30}
	forVol, forWiFi := statusGapNeighbours(vol, wifi)
	gv := statusFingerRect(vol, 56, forVol)
	gw := statusFingerRect(wifi, 56, forWiFi, weather)
	if gv.X+gv.W > gw.X {
		t.Fatalf("targets overlap: speaker %+v, meter %+v", gv, gw)
	}
	if gv.X+gv.W != gw.X {
		t.Fatalf("a strip of the gap belongs to neither: speaker %+v, meter %+v", gv, gw)
	}
	if gv.X > vol.X || gv.X+gv.W < vol.X+vol.W || gw.X > wifi.X || gw.X+gw.W < wifi.X+wifi.W {
		t.Fatalf("a target is smaller than its chip: %+v %+v", gv, gw)
	}
	if gw.X+gw.W > weather.X {
		t.Fatalf("the meter reaches the forecast: %+v", gw)
	}
	// With nothing on its left, a short chip still grows to the minimum.
	if lone := statusFingerRect(sdl.Rect{X: 600, Y: 10, W: 16, H: 30}, 56); lone.W < 44 {
		t.Fatalf("lone chip target %+v is under 44px", lone)
	}
}

// The pairing code joins Home's focus graph only while it is drawn: Right from
// the PS5 launcher and Down from the bell reach it, Down from it lands on the
// card under it, and with no code those presses go where they always went.
func TestHomeFocusReachesThePairingCode(t *testing.T) {
	f := NewHomeFocusController()
	f.SetQRLink(HomeFocusApps)
	f.Set(HomeFocusPS5)
	if got := f.Move(FocusRight); got != HomeFocusQR {
		t.Fatalf("Right from PS5 = %s, want the code", got)
	}
	if got := f.Move(FocusDown); got != HomeFocusApps {
		t.Fatalf("Down from the code = %s, want the linked card", got)
	}
	f.Set(HomeFocusBell)
	if got := f.Move(FocusDown); got != HomeFocusQR {
		t.Fatalf("Down from the bell = %s, want the code", got)
	}
	if got := f.Move(FocusUp); got != HomeFocusBell {
		t.Fatalf("Up from the code = %s, want the bell", got)
	}

	f.SetEnabled(HomeFocusQR, false)
	f.Set(HomeFocusPS5)
	if got := f.Move(FocusRight); got != HomeFocusClock {
		t.Fatalf("with no code Right from PS5 = %s, want the clock", got)
	}
	f.Set(HomeFocusBell)
	if got := f.Move(FocusDown); got != HomeFocusPS5 {
		t.Fatalf("with no code Down from the bell = %s, want PS5", got)
	}
}

// The speaker sits between the search pill and the meter; without a mixer the
// meter's Left goes on to the search pill as before.
func TestHomeFocusVolumeButton(t *testing.T) {
	f := NewHomeFocusController()
	f.Set(HomeFocusWiFi)
	if got := f.Move(FocusLeft); got != HomeFocusVolume {
		t.Fatalf("Left from the meter = %s, want the speaker", got)
	}
	if got := f.Move(FocusLeft); got != HomeFocusSearch {
		t.Fatalf("Left from the speaker = %s, want the search pill", got)
	}
	f.Set(HomeFocusVolume)
	if got := f.Move(FocusRight); got != HomeFocusWiFi {
		t.Fatalf("Right from the speaker = %s, want the meter", got)
	}
	f.SetEnabled(HomeFocusVolume, false)
	f.Set(HomeFocusWiFi)
	if got := f.Move(FocusLeft); got != HomeFocusSearch {
		t.Fatalf("with no speaker Left from the meter = %s, want the search pill", got)
	}
}

func TestCompanionPairingCaption(t *testing.T) {
	if got := companionPairingCaption("http://192.168.1.5:8765/?p=123456"); got != "PIN 123456 · 192.168.1.5:8765" {
		t.Fatalf("caption = %q", got)
	}
	if got := companionPairingCaption("http://10.0.0.2:8765/"); got != "10.0.0.2:8765" {
		t.Fatalf("caption without a PIN = %q", got)
	}
	if got := companionPairingCaption(""); got != "" {
		t.Fatalf("no link gave %q", got)
	}
}

// The new header items start with placeholders on the canvas (a tap before the
// first frame must still resolve to a control), and the layout's audit names
// neither of them: they are header chips, left out of the overlap check.
func TestHomeLayoutHeaderItemsInBounds(t *testing.T) {
	for _, c := range []struct{ w, h int32 }{{1280, 720}, {960, 720}} {
		layout := NewHomeLayout(c.w, c.h)
		for _, id := range []HomeFocusID{HomeFocusVolume, HomeFocusQR} {
			r := layout.Items[id].Rect
			if r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > c.w || r.Y+r.H > c.h {
				t.Errorf("%dx%d: %s placeholder %+v is off the canvas", c.w, c.h, id, r)
			}
		}
		for _, problem := range layout.BoundsViolations() {
			if strings.HasPrefix(problem, string(HomeFocusVolume)) || strings.HasPrefix(problem, string(HomeFocusQR)+":") ||
				strings.Contains(problem, " "+string(HomeFocusVolume)) || strings.Contains(problem, " "+string(HomeFocusQR)) {
				t.Errorf("%dx%d: %s", c.w, c.h, problem)
			}
		}
	}
}
