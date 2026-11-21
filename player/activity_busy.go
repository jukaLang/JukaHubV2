package main

// activity_busy.go — "something is happening" chips for work that has no
// progress to report.
//
// The activity strip (activity.go) lists installs, downloads and unpacking,
// each with its own progress source. Plenty of slow work has none: the Wi-Fi
// radio coming up and joining its network, Bluetooth's bring-up ladder
// (bluetooth_bringup.go). Before this, a switch pressed in Quick Settings went
// quiet for seconds and only the result said anything. activityBegin is the
// general way in: a caller names what it is doing and gets an end function;
// while any entry is open the strip shows a chip with a turning spinner and
// the label. The registry is goroutine-safe (the work runs on goroutines, the
// strip reads on the render thread) and end is idempotent, so a defer and an
// early end on an error path can both call it.
//
// Rules the chips keep:
//   - An entry younger than activityBusyShowAfter is not drawn: a radio that
//     was already on answers in milliseconds, and a chip that flashes for one
//     frame says nothing.
//   - The same label open twice (two bring-ups racing) is one chip.
//   - An entry older than activityBusyMaxAge is dropped even if its end never
//     came (a goroutine that panicked past its defer): the strip must never
//     claim work that stopped long ago.
//   - The chips have no X and no tap: the work is the system's, not a job the
//     app can stop half-way.
//
// Wi-Fi: setWifiEnabled(true) (systemcontrol.go) is the one place every radio
// switch goes through — Quick Settings, the Home tile, the Wi-Fi page, the
// quick menu, the FN switch, voice. The radio itself answers at once; what the
// player waits for is the link, so the chip stays until the interface is up,
// the radio reads off again (the switch failed) or activityWifiLinger passes.
//
// Bluetooth: btBringUp's slow path (after its "already on" check) holds a chip
// for exactly as long as it runs.

import (
	"sync"
	"time"
)

const (
	// activityBusyWifiLabel and activityBusyBTLabel are the two chips this
	// file's hooks open.
	activityBusyWifiLabel = "Turning on Wi-Fi…"
	activityBusyBTLabel   = "Turning on Bluetooth…"
	// activityBusyShowAfter keeps instant work off the strip.
	activityBusyShowAfter = 250 * time.Millisecond
	// activityBusyMaxAge is the longest any entry is believed.
	activityBusyMaxAge = 2 * time.Minute
	// activityWifiLinger bounds the wait for the Wi-Fi link after the radio
	// switch: no saved network in range means no link, and the chip must not
	// say "turning on" for ever.
	activityWifiLinger = 12 * time.Second
	// activityWifiPoll is how often the link is looked at meanwhile.
	activityWifiPoll = 400 * time.Millisecond
)

// activityBusyEntry is one open piece of work.
type activityBusyEntry struct {
	id    uint64
	label string
	since time.Time
}

// activityBusy is the registry. Any goroutine, under mu.
var activityBusy struct {
	mu      sync.Mutex
	next    uint64
	entries []activityBusyEntry
}

// activityBegin opens a chip named label and returns the function that closes
// it. Safe from any goroutine; calling end more than once is harmless.
func activityBegin(label string) (end func()) {
	activityBusy.mu.Lock()
	activityBusy.next++
	id := activityBusy.next
	activityBusy.entries = append(activityBusy.entries, activityBusyEntry{id: id, label: label, since: time.Now()})
	activityBusy.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { activityBusyEnd(id) }) }
}

// activityBusyEnd removes one entry.
func activityBusyEnd(id uint64) {
	activityBusy.mu.Lock()
	defer activityBusy.mu.Unlock()
	for i, e := range activityBusy.entries {
		if e.id == id {
			activityBusy.entries = append(activityBusy.entries[:i], activityBusy.entries[i+1:]...)
			return
		}
	}
}

// activityBusyLabels is what the strip shows now: one label per kind of work,
// oldest first, past activityBusyShowAfter and within activityBusyMaxAge.
// Stale entries are pruned on the way. pending reports an entry that is open
// but not shown yet, so the frame after it gets drawn.
func activityBusyLabels(now time.Time) (labels []string, pending bool) {
	activityBusy.mu.Lock()
	defer activityBusy.mu.Unlock()
	kept := activityBusy.entries[:0]
	seen := map[string]bool{}
	for _, e := range activityBusy.entries {
		age := now.Sub(e.since)
		if age > activityBusyMaxAge {
			continue
		}
		kept = append(kept, e)
		if age < activityBusyShowAfter {
			pending = true
			continue
		}
		if !seen[e.label] {
			seen[e.label] = true
			labels = append(labels, e.label)
		}
	}
	activityBusy.entries = kept
	return labels, pending
}

// activityBusyItems are the strip's rows for the open entries (activityRows).
// Render thread: it asks for frames while a chip turns, or is about to show.
func activityBusyItems() []activityItem {
	labels, pending := activityBusyLabels(time.Now())
	if len(labels) > 0 || pending {
		requestFrameEvery(66)
	}
	items := make([]activityItem, 0, len(labels))
	for _, label := range labels {
		items = append(items, activityItem{
			label: label,
			frac:  -1,
			tint:  installHighlight(),
			spin:  true,
		})
	}
	return items
}

// activityWifiBringUp is setWifiEnabled(true)'s hook: the chip opens now, and
// the returned function — run when the switch returns — leaves it up until the
// link is (activityWifiWatch).
func activityWifiBringUp() (handOff func()) {
	end := activityBegin(activityBusyWifiLabel)
	return func() { go activityWifiWatch(end) }
}

// activityWifiWatch closes the Wi-Fi chip once the interface is up, the radio
// reads off (the switch did not take) or activityWifiLinger has passed. Its
// own goroutine; rfkill and nmcli are asked off the render thread.
func activityWifiWatch(end func()) {
	defer end()
	defer func() {
		if r := recover(); r != nil {
			logWarn("activity", "watching the Wi-Fi link failed: %v", r)
		}
	}()
	if on, known := wifiEnabled(); known && !on {
		return
	}
	deadline := time.Now().Add(activityWifiLinger)
	for n := 1; time.Now().Before(deadline); n++ {
		if wifiLinkIsUp() {
			return
		}
		time.Sleep(activityWifiPoll)
		// A switch turned off again meanwhile (on, then off at once) ends the
		// chip too; the radio is asked every few polls, not every one (nmcli
		// is a process each time).
		if n%5 == 0 {
			if on, known := wifiEnabled(); known && !on {
				return
			}
		}
	}
}
