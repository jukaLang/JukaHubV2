package main

// alarm_ring_test.go — the takeover, the snooze and the ringtone catalogue.

import (
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// A snoozed alarm stays quiet until its snooze ends, rings once after it, and is
// not replayed hours later if the device was asleep through the snooze.
func TestAlarmDueHonoursSnooze(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 11, 4, 7, 30, 10, 0, loc)
	a := alarmEntry{Hour: 7, Minute: 30, Enabled: true, FiredOn: alarmDate(now)}

	if alarmDue(a, now) {
		t.Fatal("an alarm that already rang today rang again")
	}

	a.SnoozeUntil = now.Add(9 * time.Minute).Format(time.RFC3339)
	if alarmDue(a, now.Add(5*time.Minute)) {
		t.Fatal("a snoozed alarm rang before the snooze ended")
	}
	if !alarmDue(a, now.Add(9*time.Minute+5*time.Second)) {
		t.Fatal("a snoozed alarm did not ring when the snooze ended")
	}
	if alarmDue(a, now.Add(3*time.Hour)) {
		t.Fatal("a snooze the device slept through was replayed hours later")
	}

	// Re-arming the alarm clears a stale snooze.
	alarmArmFrom(&a, now)
	if a.SnoozeUntil != "" {
		t.Fatalf("re-armed alarm kept snooze %q", a.SnoozeUntil)
	}
}

// Every ringtone in the picker is real: an id, a name, at least one note, and
// no duplicate ids. Cycling wraps in both directions.
func TestAlarmSoundCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range alarmSoundList {
		if s.ID == "" || s.Name == "" || len(s.Notes) == 0 {
			t.Fatalf("incomplete ringtone: %+v", s)
		}
		if seen[s.ID] {
			t.Fatalf("duplicate ringtone id %q", s.ID)
		}
		seen[s.ID] = true
		for _, n := range s.Notes {
			if n.Freq <= 0 || n.MS <= 0 || n.Gap < 0 {
				t.Fatalf("%s has a bad note: %+v", s.ID, n)
			}
		}
	}
	if alarmSoundByID("").ID != alarmDefaultSound {
		t.Fatal("an alarm with no saved sound did not fall back to the default")
	}
	if alarmSoundByID("no-such-sound").ID != alarmDefaultSound {
		t.Fatal("an unknown sound id did not fall back to the default")
	}
	if got := alarmNextSound(alarmDefaultSound, 1); got == alarmDefaultSound {
		t.Fatal("cycle forward did not move off the default")
	}
	if got := alarmNextSound(alarmSoundList[0].ID, -1); got != alarmSoundList[len(alarmSoundList)-1].ID {
		t.Fatalf("cycle backward wrapped to %q", got)
	}
	for _, s := range alarmSoundList {
		if alarmSoundName(s.ID) != s.Name {
			t.Fatalf("%s shows %q", s.ID, alarmSoundName(s.ID))
		}
	}
}

// Snooze silences the ring, arms the same alarm again and marks it; dismiss
// silences it and clears the snooze so the next ring is tomorrow's.
func TestAlarmSnoozeAndDismiss(t *testing.T) {
	t.Chdir(t.TempDir())
	prevStore := alarmStore
	prevUI := alarmUI
	t.Cleanup(func() {
		alarmStore = prevStore
		alarmUI = prevUI
		alarmRingResetForTest()
	})
	alarmRingResetForTest()
	alarmStore = alarmList{Alarms: []alarmEntry{{Hour: 7, Minute: 30, Enabled: true, ID: 1, Sound: "bell"}}}

	if !alarmBegin(alarmStore.Alarms[0]) {
		t.Fatal("alarmBegin refused a quiet alarm")
	}
	if !alarmFiringActive() {
		t.Fatal("the ring overlay is not active after alarmBegin")
	}
	if alarmBegin(alarmStore.Alarms[0]) {
		t.Fatal("a second alarm took the screen while one was ringing")
	}

	alarmSnoozeFiring()
	if alarmFiringActive() {
		t.Fatal("snooze left the overlay up")
	}
	snoozed := alarmStore.Alarms[0].SnoozeUntil
	if snoozed == "" {
		t.Fatal("snooze did not arm the alarm again")
	}
	until, err := time.Parse(time.RFC3339, snoozed)
	if err != nil {
		t.Fatalf("snooze time %q is not RFC3339: %v", snoozed, err)
	}
	if !alarmDue(alarmStore.Alarms[0], until.Add(time.Second)) {
		t.Fatal("the snoozed alarm is not due when its snooze ends")
	}
	if alarmDue(alarmStore.Alarms[0], until.Add(-time.Minute)) {
		t.Fatal("the snoozed alarm is due before its snooze ends")
	}

	if !alarmBegin(alarmStore.Alarms[0]) {
		t.Fatal("the snoozed alarm could not ring again")
	}
	alarmDismissFiring()
	if alarmFiringActive() {
		t.Fatal("dismiss left the overlay up")
	}
	if alarmStore.Alarms[0].SnoozeUntil != "" {
		t.Fatal("dismiss kept a pending snooze")
	}
}

// An alarm that comes due while another rings takes the ring over instead of
// being lost behind it, and the snooze count belongs to one alarm's morning:
// it survives the re-rings and a dismiss clears it.
func TestAlarmRetargetAndSnoozeCount(t *testing.T) {
	t.Chdir(t.TempDir())
	prevStore := alarmStore
	t.Cleanup(func() {
		alarmStore = prevStore
		alarmRingResetForTest()
	})
	alarmRingResetForTest()
	first := alarmEntry{Hour: 7, Minute: 0, Enabled: true, ID: 1, Label: "Wake up"}
	second := alarmEntry{Hour: 7, Minute: 2, Enabled: true, ID: 2, Label: "Pills"}
	alarmStore = alarmList{Alarms: []alarmEntry{first, second}}

	if _, ok := alarmRetarget(second); ok {
		t.Fatal("a ring was handed over while nothing was ringing")
	}
	if !alarmBegin(first) {
		t.Fatal("alarmBegin refused a quiet alarm")
	}
	if _, ok := alarmRetarget(first); ok {
		t.Fatal("the ringing alarm took its own ring over")
	}
	prev, ok := alarmRetarget(second)
	if !ok || prev.ID != first.ID {
		t.Fatalf("retarget = %+v, %v; want the first alarm's ring handed over", prev, ok)
	}
	alarmRing.mu.Lock()
	ringing := alarmRing.entry.ID
	alarmRing.mu.Unlock()
	if ringing != second.ID {
		t.Fatalf("the card shows alarm %d, want the newer one", ringing)
	}

	snoozes := func() int {
		alarmRing.mu.Lock()
		defer alarmRing.mu.Unlock()
		return alarmRing.snoozes
	}
	for want := 1; want <= 2; want++ {
		alarmSnoozeFiring()
		if !alarmBegin(alarmStore.Alarms[1]) {
			t.Fatal("the snoozed alarm could not ring again")
		}
		if got := snoozes(); got != want {
			t.Fatalf("after %d snoozes the card counts %d", want, got)
		}
	}
	alarmDismissFiring()
	if !alarmBegin(alarmStore.Alarms[1]) {
		t.Fatal("the dismissed alarm could not ring again")
	}
	if got := snoozes(); got != 0 {
		t.Fatalf("a dismiss left the snooze count at %d", got)
	}
}

// A snooze that carries an alarm past midnight does not cost the next evening's
// ring: the re-ring at 00:07 does not mark the new day as done.
func TestAlarmSnoozeAcrossMidnight(t *testing.T) {
	evening := time.Date(2026, 11, 4, 23, 58, 0, 0, time.UTC)
	a := alarmEntry{Hour: 23, Minute: 58, Enabled: true, ID: 3}
	if !alarmDue(a, evening) {
		t.Fatal("the alarm is not due at its own time")
	}
	alarmMarkRang(&a, evening)
	if a.FiredOn != alarmDate(evening) || a.RangOn != alarmDate(evening) {
		t.Fatalf("the evening ring marked fired %q, rang %q", a.FiredOn, a.RangOn)
	}

	reRing := time.Date(2026, 11, 5, 0, 7, 0, 0, time.UTC)
	a.SnoozeUntil = reRing.Format(time.RFC3339)
	if !alarmDue(a, reRing.Add(time.Second)) {
		t.Fatal("the snooze did not ring after midnight")
	}
	alarmMarkRang(&a, reRing.Add(time.Second))
	if a.SnoozeUntil != "" || a.RangOn != alarmDate(reRing) {
		t.Fatalf("after the re-ring: snooze %q, rang %q", a.SnoozeUntil, a.RangOn)
	}
	if a.FiredOn == alarmDate(reRing) {
		t.Fatal("the re-ring after midnight closed the new day's own ring")
	}
	if !alarmDue(a, time.Date(2026, 11, 5, 23, 58, 0, 0, time.UTC)) {
		t.Fatal("the next evening's ring was skipped")
	}
}

// The key that moves the focus onto the label does not type into it: Space on
// the minute row, B back from the ringtone row. Enter types nothing, so the
// first letter after it still lands.
func TestAlarmLabelIgnoresTheKeyThatOpenedIt(t *testing.T) {
	prevUI := alarmUI
	t.Cleanup(func() {
		alarmUI = prevUI
		alarmPlayStop()
	})
	key := func(sym sdl.Keycode) {
		handleAlarmInput(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym}}, &Config{})
	}
	text := func(s string) {
		var e sdl.TextInputEvent
		copy(e.Text[:], s)
		handleAlarmTextInput(&e)
	}

	alarmUI = alarmScene{tab: 2, editID: -1, editSound: alarmDefaultSound}
	key(sdl.K_SPACE)
	text(" ")
	if alarmUI.tab != 3 || alarmUI.editLabel != "" {
		t.Fatalf("Space from the minute row: tab %d, label %q", alarmUI.tab, alarmUI.editLabel)
	}

	alarmUI = alarmScene{tab: 4, editID: -1, editSound: alarmDefaultSound, editLabel: "Gym"}
	key(sdl.K_b)
	text("b")
	if alarmUI.tab != 3 || alarmUI.editLabel != "Gym" {
		t.Fatalf("B back from the ringtone row: tab %d, label %q", alarmUI.tab, alarmUI.editLabel)
	}

	alarmUI = alarmScene{tab: 2, editID: -1, editSound: alarmDefaultSound}
	key(sdl.K_RETURN)
	key(sdl.K_w)
	text("w")
	if alarmUI.editLabel != "w" {
		t.Fatalf("the first letter after Enter was dropped: label %q", alarmUI.editLabel)
	}
}

// The overlay must paint the red takeover and record its two buttons, so a tap
// lands on what the frame actually drew.
func TestAlarmRingPanelRendersButtons(t *testing.T) {
	// Snoozing from the tap below saves jukaalarm.json; keep that out of the
	// repository's own alarm file.
	t.Chdir(t.TempDir())
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)

	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = probeW, probeH
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })

	prevStore := alarmStore
	t.Cleanup(func() { alarmStore = prevStore; alarmRingResetForTest() })
	alarmRingResetForTest()
	alarmStore = alarmList{Alarms: []alarmEntry{{Hour: 7, Minute: 30, Enabled: true, ID: 7, Label: "Wake up", Sound: "chime"}}}
	if !alarmBegin(alarmStore.Alarms[0]) {
		t.Fatal("alarmBegin refused")
	}

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderAlarmRing(renderer, probeBootConfig())

	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v", err)
	}
	if got := f.countNonBG(); got < 200 {
		t.Fatalf("alarm overlay drew almost nothing (%d lit samples)", got)
	}
	if alarmRingSnoozeRect.W == 0 || alarmRingDismissRect.W == 0 {
		t.Fatal("the overlay did not record its Snooze and Dismiss buttons")
	}

	// A tap on Dismiss must end the ring; a tap on the wash must not.
	alarmHandleMouse(alarmRingSnoozeRect.X+alarmRingSnoozeRect.W/2, alarmRingSnoozeRect.Y+alarmRingSnoozeRect.H/2)
	if alarmFiringActive() {
		t.Fatal("a tap on Snooze left the overlay up")
	}
	if !strings.Contains(alarmStore.Alarms[0].SnoozeUntil, "T") {
		t.Fatalf("snooze from a tap stored %q", alarmStore.Alarms[0].SnoozeUntil)
	}
}
