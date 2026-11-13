package main

// bluetooth_devices_ui.go — the Bluetooth page: the radio switch, the paired
// devices, a search for new ones and pairing, controllers first.
//
// It is built on the tool screens' frame (tbxui.go) the way the Devices page
// is (peerui.go): Home's backdrop, the page header, a focus list of glass rows,
// one details sheet, and its own footer that follows the focused row. Every
// bluetoothctl conversation (bluetooth_devices.go) runs as scene work, so
// leaving the page stops a search or a pairing; results come back to the main
// loop through tbxPost and are dropped when they belong to an earlier visit.
//
// A pad is only called ready when the kernel made a controller for it, and the
// page keeps looking (a cheap read of /proc/bus/input/devices every two
// seconds), so a pad that reconnects by itself with its PS button shows up as
// connected without another search.
//
// A paired device connects or disconnects with one press of A (or a tap); Y
// opens its sheet (details, Forget). The top bar's Bluetooth button opens the
// page with the focus on the first paired device, and anything that needs the
// radio — the switch, Connect, Pair, Search — first brings Bluetooth up
// (bluetooth_bringup.go), so the page works even where the firmware left the
// chip or bluetoothd stopped.

import (
	"context"
	"errors"
	"math/bits"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// btSceneName is the page's scene. It is not in any shipped config:
// btScreenOpen adds it in memory the first time.
const btSceneName = "Bluetooth"

// btWiFiNote is what the page says under the switch.
const btWiFiNote = "Bluetooth shares the radio with Wi-Fi on this handheld: Remote Play, Moonlight and video streams can stutter while it is on. Switch it off when nothing is connected."

type btScreenState struct {
	visit tbxVisit
	nav   tbxNav
	// gen counts visits; work posted for an earlier one is dropped.
	gen int
	// devices is everything the page knows, by address. Main loop only.
	devices map[string]*bzDevice
	listed  bool
	listErr string
	// scanning is a search in flight; scanCancel stops it (pairing does, to
	// have the radio to itself).
	scanning   bool
	scanUntil  time.Time
	scanCancel context.CancelFunc
	scans      int
	scanErr    string
	// busyMAC is the device an operation runs for; busyWord is what it is
	// doing for its row ("Pairing…"), busyVerb the same for a toast
	// ("pairing").
	busyMAC  string
	busyWord string
	busyVerb string
	// padOK are the addresses with a controller right now; noPad the pads
	// that connected and had time to get one, without the kernel making one;
	// connectedAt when a pad was seen connecting while the page was up.
	padOK       map[string]bool
	noPad       map[string]bool
	connectedAt map[string]time.Time
	padReadAt   time.Time
	padReading  bool
	radioBusy   bool
	// stackWord is what the Bluetooth bring-up is doing, for the switch's row
	// ("Starting the Bluetooth service…"); "" while nothing is being started.
	stackWord string
	// moved: the focus was moved, a row pressed or clicked during this visit,
	// so the first listing leaves the focus where the user put it.
	moved bool
	// links is the number of live links when the list was last read or the
	// page last acted (-1 before that); a different count later means a
	// device came or went without the page doing it, and the list is read
	// again (relisting). opSeq counts the page's own operations: a reading
	// that one started or ended during is dropped, because the operation's
	// result is newer than it. relistScan is the switch's follow-up (focus the
	// first device, or search when nothing is paired) waiting for a reading;
	// relistFails counts readings that failed for the same change, so a dead
	// daemon is not asked again every two seconds.
	links       int
	relisting   bool
	opSeq       int
	relistScan  bool
	relistFails int
	// noAdapter: the last listing found no adapter registered at all, so
	// there was nothing to list (the firmware's switch is off).
	noAdapter bool
	// showUnnamed lists devices that only announced their address.
	showUnnamed bool
	noteWrap    tbxWrapCache
	// focusAt is the focused row's place in the list on the last frame, so a
	// row that goes away (forgotten, paired, out of range) hands the focus to
	// the row that takes its place rather than to the switch at the top.
	focusAt int
}

var btScreen btScreenState

type btDevicesScene struct{ sceneBase }

func (btDevicesScene) render(r *sdl.Renderer, c *Config) bool {
	btRenderScreen(r, c)
	return true
}

func (btDevicesScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), btScreenAction)
}

func (btDevicesScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), btScreenAction)
}

func init() { registerScene(btDevicesScene{sceneBase{name: btSceneName}}) }

// btScreenOpen opens the Bluetooth page (Settings → Network, the Start menu,
// search, and the PS Remote Play panel's controller row). The scene is added
// to the config in memory, without an Icon so the Start menu lists the page
// through its own entry rather than twice.
func btScreenOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	btEnsureScene(config)
	idx := sceneIndexExact(config, btSceneName)
	if idx < 0 {
		return
	}
	// A panel the page was opened from must not float over it.
	qs.open = false
	chiakiPanel.open = false
	if idx == currentSceneIndex {
		return
	}
	changeSceneTo(config, idx)
}

// btEnsureScene gives the page its scene in memory, like ensureRommScene: the
// shipped configs carry none, and a hot reload (hotreload.go) must find it
// again so the user stays on the page. The page is drawn and driven by Go, so
// a scene of that name in a config keeps no elements of its own: element
// dispatch runs first and would take the keys from the page.
func btEnsureScene(config *Config) {
	if config == nil {
		return
	}
	if sceneIndexExact(config, btSceneName) < 0 {
		config.Scenes = append(config.Scenes, SceneConfig{Name: btSceneName})
	}
	for i := range config.Scenes {
		if config.Scenes[i].Name == btSceneName {
			config.Scenes[i].Elements = nil
			config.Scenes[i].Layout = ""
		}
	}
}

// btScreenEnter runs once per visit: a fresh list, read in the background.
// With the radio on, the focus then goes to the first paired device (the one
// connected, when one is), so A connects or disconnects it at once — the top
// bar's button opens the page for exactly that. Only when nothing is paired
// does the page start a search by itself: then it was opened to find
// something, and a search is a ten-second cost to Wi-Fi otherwise.
func btScreenEnter() {
	btScreen.gen++
	btScreen.nav.scroll = 0
	btScreen.nav.focus = ""
	btScreen.focusAt = 0
	btScreen.devices = map[string]*bzDevice{}
	btScreen.listed = false
	btScreen.listErr = ""
	btScreen.scanning = false
	btScreen.scanCancel = nil
	btScreen.scans = 0
	btScreen.scanErr = ""
	btScreen.busyMAC, btScreen.busyWord, btScreen.busyVerb = "", "", ""
	btScreen.padOK = map[string]bool{}
	btScreen.noPad = map[string]bool{}
	btScreen.connectedAt = map[string]time.Time{}
	btScreen.padReadAt = time.Now()
	btScreen.padReading = false
	btScreen.radioBusy = false
	btScreen.stackWord = ""
	btScreen.moved = false
	btScreen.links = -1
	btScreen.relisting = false
	btScreen.relistScan = false
	btScreen.relistFails = 0
	btScreen.noAdapter = false
	btScreen.showUnnamed = false
	if !btDevicesAvailable() {
		return
	}
	gen, seq := btScreen.gen, btScreen.opSeq
	// The first reading counts as a re-reading in flight, so none starts
	// beside it (the switch's follow-up waits for it in relistScan).
	btScreen.relisting = true
	started := runSceneTask(func(ctx context.Context) {
		// What the stack looks like, once per run, for the log a bug report
		// carries (bluetooth_diag.go).
		btLogDiagnosticsOnce()
		on, known := btProbeRadio()
		bzNoteRadio(on, known)
		var devs []bzDevice
		var err error
		// Without an adapter BlueZ lists nothing (it answers "No default
		// controller"), so there is nothing to ask: the page says Bluetooth is
		// off, and switching it on reads the devices.
		noAdapter := runtime.GOOS == "linux" && !btAdapterPresent()
		if !noAdapter {
			devs, err = bzList(ctx)
		}
		// Read after the listing: a pad connected before the page opened has
		// had its time to get a controller (btApplyPadReading).
		pads, anon := bzPadReading(bzReadInputDevices(), bits.UintSize)
		links := btSysfsLinkCount()
		if ctx.Err() != nil {
			return
		}
		tbxPost(func() {
			if btScreen.gen != gen {
				return
			}
			btScreen.relisting = false
			btScreen.listed = true
			btScreen.links = links
			btScreen.noAdapter = noAdapter
			btReplaceList(devs)
			btApplyPadReading(pads, anon)
			if err != nil {
				btScreen.listErr = bzErrText(err)
			}
			if btScreen.opSeq != seq || btScreen.relistScan {
				// The switch was used while the list was being read: this
				// reading may predate it, so the devices are read again (and
				// that reading carries the switch's follow-up).
				btRelist(false)
				return
			}
			btFocusFirstDevice(on && known, false)
			// A search the user started while the list was being read is
			// already the search this would start.
			if on && known && err == nil && len(btPairedList()) == 0 && !btScreen.scanning {
				btStartScan()
			}
		})
	})
	if !started {
		btScreen.relisting = false
	}
}

// btFocusFirstDevice puts the focus on the first paired device — connected
// ones sort first — once the list is in. After the visit's first listing it
// does so only while the user has not touched the page (moved); after the
// switch turned the radio on (afterSwitch), only while the focus is still on
// the switch that was pressed. Never with the radio off: then the switch is
// the row to press.
func btFocusFirstDevice(radioOn, afterSwitch bool) {
	if !radioOn {
		return
	}
	f := btScreen.nav.focus
	if afterSwitch {
		if f != "radio" {
			return
		}
	} else if btScreen.moved || (f != "" && f != "radio") {
		return
	}
	if paired := btPairedList(); len(paired) > 0 {
		btScreen.nav.setFocus("dev:" + paired[0].MAC)
	}
}

// btRelist reads the devices again while the page is up: after the radio came
// on, when the number of live links changed without the page doing it (a pad
// woken with its PS button, a headset switched off), and when the top bar's
// button is pressed on the page. It does not start while the page is busy (a
// pairing, a connection, a search or the switch each end with their own word on
// the list), and a reading that one of those started or ended during is
// dropped: the operation's result is newer than it. scanIfEmpty is the switch's
// own call once the radio is on: the focus then moves to the first paired
// device, or a search starts when nothing is paired. That follow-up waits in
// relistScan for a reading that lands, so one already in flight carries it.
func btRelist(scanIfEmpty bool) {
	if scanIfEmpty {
		btScreen.relistScan = true
	}
	if btScreen.relisting || btScreen.busyMAC != "" || btScreen.scanning || btScreen.radioBusy || !btDevicesAvailable() || !btOnPage() {
		return
	}
	btScreen.relisting = true
	gen, seq := btScreen.gen, btScreen.opSeq
	started := runSceneTask(func(ctx context.Context) {
		on, known := btProbeRadio()
		bzNoteRadio(on, known)
		var devs []bzDevice
		var err error
		noAdapter := !btAdapterPresent()
		if !noAdapter {
			devs, err = bzList(ctx)
		}
		pads, anon := bzPadReading(bzReadInputDevices(), bits.UintSize)
		links := btSysfsLinkCount()
		if ctx.Err() != nil {
			return
		}
		tbxPost(func() {
			if btScreen.gen != gen {
				return
			}
			btScreen.relisting = false
			if btScreen.opSeq != seq || btScreen.busyMAC != "" || btScreen.scanning || btScreen.radioBusy {
				// Stale: the page acted while this was read. Nothing is
				// applied and the count stays, so a later tick reads again.
				switch {
				case btScreen.busyMAC != "" || btScreen.scanning:
					// The user has moved on: a follow-up would only get in
					// the way.
					btScreen.relistScan = false
				case !btScreen.radioBusy && btScreen.relistScan:
					btRelist(false)
				}
				return
			}
			if err != nil {
				// The list on screen stays: a failed reading says nothing
				// about what is paired. The same change is asked about once
				// more on the next tick, then left until the count changes
				// again (a dead daemon is not asked every two seconds).
				logDebug("bluetooth", "reading the devices again: %v", err)
				btScreen.relistFails++
				if btScreen.relistFails >= 2 {
					btScreen.links = links
				}
				if len(btPairedList()) == 0 || btScreen.noAdapter {
					// Nothing listed to keep (or the "off" card from a
					// reading without an adapter): say why instead.
					btScreen.listErr = bzErrText(err)
					btScreen.noAdapter = false
				}
				btScreen.relistScan = false
				return
			}
			btScreen.relistFails = 0
			btScreen.links = links
			btScreen.noAdapter = noAdapter
			was := map[string]bool{}
			for mac, d := range btScreen.devices {
				if d != nil && d.Connected {
					was[mac] = true
				}
			}
			btReplaceList(devs)
			// A pad that connected by itself reads "Checking…" for the time it
			// gets to make its controller, not "No input" at once.
			for mac, d := range btScreen.devices {
				if d != nil && d.Connected && !was[mac] {
					btScreen.connectedAt[mac] = time.Now()
				}
			}
			btApplyPadReading(pads, anon)
			btScreen.listed = true
			btScreen.listErr = ""
			if !btScreen.relistScan {
				return
			}
			btScreen.relistScan = false
			// Just switched on from its row: the next press is most likely
			// Connect, so the focus goes to the first paired device, or a
			// search starts when there is none.
			btFocusFirstDevice(on && known, true)
			if on && known && len(btPairedList()) == 0 && !btScreen.scanning {
				btStartScan()
			}
		})
	})
	if !started {
		btScreen.relisting = false
	}
}

// btReplaceList puts a listing in place of the page's list: the daemon's
// answer is the truth about what is paired.
func btReplaceList(devs []bzDevice) {
	next := make(map[string]*bzDevice, len(devs))
	for i := range devs {
		d := devs[i]
		next[d.MAC] = &d
	}
	btScreen.devices = next
}

// btMergeSeen copies what an incomplete listing says about a device onto dst:
// its name, hints, services and signal, but never the paired, trusted or
// connected state, which such a listing does not carry.
func btMergeSeen(dst *bzDevice, src bzDevice) {
	if src.Name != "" {
		dst.Name = src.Name
	}
	if src.Icon != "" {
		dst.Icon = src.Icon
	}
	if src.Class != 0 {
		dst.Class = src.Class
	}
	dst.HID = dst.HID || src.HID
	dst.Audio = dst.Audio || src.Audio
	if src.RSSI != 0 {
		dst.RSSI = src.RSSI
	}
	if src.Seen.After(dst.Seen) {
		dst.Seen = src.Seen
	}
}

// btMergeList folds the devices of a search that failed or was stopped part
// way into the page's list instead of replacing it: such a listing has no
// details, so every device in it reads as unpaired, and replacing would empty
// Paired (or move a paired pad to Nearby with a Pair pill).
func btMergeList(devs []bzDevice) {
	if btScreen.devices == nil {
		btScreen.devices = map[string]*bzDevice{}
	}
	for _, src := range devs {
		if d := btScreen.devices[src.MAC]; d != nil {
			btMergeSeen(d, src)
			// The paired listing is read first; when it made it, it counts.
			d.Paired = d.Paired || src.Paired
			continue
		}
		if d := bzEntry(btScreen.devices, src.MAC, src.Name != "", src.Paired); d != nil {
			*d = src
		}
	}
}

// btBusyText is the toast for a press while another operation runs.
func btBusyText() string {
	name := btScreen.busyMAC
	if d := btScreen.devices[btScreen.busyMAC]; d != nil {
		name = d.label()
	}
	verb := btScreen.busyVerb
	if verb == "" {
		verb = "busy with"
	}
	return "Still " + verb + " " + name + " — try again when that finishes"
}

// btClaimBusy starts an operation for mac, or says why it cannot start yet.
// word is the row's progress word ("Pairing…"), verb the toast's ("pairing").
// A search in flight is stopped: pairing and connecting want the radio. While
// the switch is turning the radio on or off nothing starts: a connection would
// race the switch for the radio.
func btClaimBusy(mac, word, verb string) bool {
	if btScreen.busyMAC != "" {
		ShowToast(btBusyText(), ToastKindInfo)
		return false
	}
	if btScreen.radioBusy {
		ShowToast("Bluetooth is switching — try again in a moment", ToastKindInfo)
		return false
	}
	if btScreen.scanCancel != nil {
		btScreen.scanCancel()
	}
	btScreen.busyMAC, btScreen.busyWord, btScreen.busyVerb = mac, word, verb
	btScreen.opSeq++
	return true
}

// btReleaseBusy ends the operation for mac on the main loop.
func btReleaseBusy(gen int, mac string) {
	if btScreen.gen == gen && btScreen.busyMAC == mac {
		btScreen.busyMAC, btScreen.busyWord, btScreen.busyVerb = "", "", ""
		btScreen.opSeq++
	}
}

// btClearBusy drops an operation that could not start.
func btClearBusy() {
	btScreen.busyMAC, btScreen.busyWord, btScreen.busyVerb = "", "", ""
	btScreen.opSeq++
}

// btNoteLinks takes the kernel's link count after one of the page's own
// operations as the count the list stands for: the change is the page's doing,
// and its result is already on the row, so it is not a reason to read again.
func btNoteLinks(gen, links int) {
	if btScreen.gen == gen {
		btScreen.links = links
	}
}

// btStageFunc reports a flow's progress words on the device's row.
func btStageFunc(gen int, mac string) func(string) {
	return func(word string) {
		tbxPost(func() {
			if btScreen.gen == gen && btScreen.busyMAC == mac {
				btScreen.busyWord = word
			}
		})
	}
}

// btRadioStageFunc reports the Bluetooth bring-up's progress words on the
// switch's row.
func btRadioStageFunc(gen int) func(string) {
	return func(word string) {
		tbxPost(func() {
			if btScreen.gen == gen {
				btScreen.stackWord = word
			}
		})
	}
}

// btClearStackWord takes the bring-up's words off the switch's row.
func btClearStackWord(gen int) {
	tbxPost(func() {
		if btScreen.gen == gen {
			btScreen.stackWord = ""
		}
	})
}

// btSecondsLeft is the time a search still has, for its row.
func btSecondsLeft() int {
	left := int(time.Until(btScreen.scanUntil).Round(time.Second) / time.Second)
	if left < 0 {
		return 0
	}
	return left
}

// btEventWorthPosting keeps signal-strength chatter off the main loop: only
// what changes a row is posted.
func btEventWorthPosting(ev bzEvent) bool {
	if ev.kind != bzEventChange {
		return true
	}
	switch ev.key {
	case "Name", "Alias", "Icon", "Class", "Paired", "Trusted", "Connected", "UUIDs":
		return true
	}
	return false
}

// btNoteConnection keeps the moment a device was seen connecting, so a pad's
// row reads "Checking…" until it has had bzInputWait to get its controller,
// and forgets it when the device disconnects.
func btNoteConnection(ev bzEvent) {
	if ev.kind != bzEventChange || ev.key != "Connected" {
		return
	}
	if btScreen.connectedAt == nil {
		btScreen.connectedAt = map[string]time.Time{}
	}
	delete(btScreen.noPad, ev.mac)
	if ev.value == "yes" {
		btScreen.connectedAt[ev.mac] = time.Now()
		return
	}
	delete(btScreen.connectedAt, ev.mac)
	delete(btScreen.padOK, ev.mac)
}

// btOnPage reports whether the scene work running now belongs to this visit of
// the page. A reading's result is applied by a closure the main loop runs on
// whatever screen is up (tbxPost), and the one frame between the result and
// the user leaving would otherwise let it start a search or a reading — a
// bluetoothctl session and Wi-Fi airtime — behind another screen.
func btOnPage() bool {
	return btScreen.visit.ctx != nil && btScreen.visit.ctx == sceneWorkContext()
}

// btStartScan starts one search, bringing Bluetooth up first when it is off
// or not started at all (btBringUp; nothing to do when it is on).
func btStartScan() {
	if !btDevicesAvailable() || !btOnPage() {
		return
	}
	switch {
	case btScreen.scanning:
		ShowToast("Already searching — "+strconv.Itoa(btSecondsLeft())+" s left", ToastKindInfo)
		return
	case btScreen.busyMAC != "":
		ShowToast(btBusyText(), ToastKindInfo)
		return
	case btScreen.radioBusy:
		return
	}
	base := sceneWorkContext()
	ctx, cancel := context.WithCancel(base)
	gen := btScreen.gen
	btScreen.scanning = true
	btScreen.scanErr = ""
	btScreen.scanUntil = time.Now().Add(bzScanDuration + 2*time.Second)
	btScreen.scanCancel = cancel
	btScreen.opSeq++
	started := runSceneTask(func(context.Context) {
		defer cancel()
		var devs []bzDevice
		// The bring-up waits the moment a freshly powered adapter needs
		// before it searches; the countdown starts once it is done.
		bringErr := btBringUp(ctx, btRadioStageFunc(gen))
		err := bringErr
		btClearStackWord(gen)
		tbxPost(func() {
			if btScreen.gen == gen && btScreen.scanning {
				btScreen.scanUntil = time.Now().Add(bzScanDuration + 2*time.Second)
			}
		})
		if err == nil && ctx.Err() != nil {
			// Stopped (a pairing took the radio) before the search began.
			err = ctx.Err()
		}
		if err == nil {
			devs, err = bzScan(ctx, bzScanDuration, func(ev bzEvent) {
				if !btEventWorthPosting(ev) {
					return
				}
				tbxPost(func() {
					if btScreen.gen == gen && btScreen.devices != nil {
						bzApplyEvent(btScreen.devices, ev, time.Now())
						btNoteConnection(ev)
					}
				})
			})
		}
		if base.Err() != nil {
			return
		}
		links := btSysfsLinkCount()
		tbxPost(func() {
			if btScreen.gen != gen {
				return
			}
			btScreen.scanning = false
			btScreen.scanCancel = nil
			btScreen.scans++
			btScreen.opSeq++
			switch {
			case err == nil:
				// A finished search's listing is the daemon's word on everything,
				// including that the list could be read: an error card from a
				// reading before the search (Bluetooth was off, the service not
				// started yet) no longer holds.
				if len(devs) > 0 {
					btReplaceList(devs)
				}
				btScreen.links = links
				btScreen.listed = true
				btScreen.listErr = ""
				btScreen.noAdapter = false
			case len(devs) > 0:
				btMergeList(devs)
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				btScreen.scanErr = bzErrText(err)
			}
			if bringErr != nil && !errors.Is(bringErr, context.Canceled) {
				// Bluetooth could not be started for the search: the Nearby
				// card says why too, but the press deserves an answer now.
				ShowToast("Could not search: "+bzErrText(bringErr), ToastKindError)
			}
		})
	})
	if !started {
		cancel()
		btScreen.scanning = false
		btScreen.scanCancel = nil
	}
}

// btStartPair pairs a device from the Nearby list.
func btStartPair(mac string) {
	d := btScreen.devices[mac]
	if d == nil || !btClaimBusy(mac, "Pairing…", "pairing") {
		return
	}
	dev, gen := *d, btScreen.gen
	PlayActivateSound()
	ShowToast("Pairing "+dev.label()+" — keep it in pairing mode", ToastKindInfo)
	started := runSceneTask(func(ctx context.Context) {
		stage := btStageFunc(gen, dev.MAC)
		if err := btBringUp(ctx, stage); err != nil {
			if ctx.Err() == nil {
				btPostOutcome(gen, dev, bzPairOutcome{Device: dev}, err, true)
			}
			return
		}
		out, err := bzPair(ctx, dev.MAC, dev.Name, stage)
		if ctx.Err() != nil {
			return
		}
		btPostOutcome(gen, dev, out, err, true)
	})
	if !started {
		btClearBusy()
	}
}

// btStartConnect connects a paired device, switching Bluetooth on first when it
// is off: pressing Connect says what the user wants, and "switch it on first"
// would only send them to the row above to say it again.
func btStartConnect(mac string) {
	d := btScreen.devices[mac]
	if d == nil || !btClaimBusy(mac, "Connecting…", "connecting") {
		return
	}
	dev, gen := *d, btScreen.gen
	started := runSceneTask(func(ctx context.Context) {
		stage := btStageFunc(gen, dev.MAC)
		if err := btBringUp(ctx, stage); err != nil {
			if ctx.Err() == nil {
				btPostOutcome(gen, dev, bzPairOutcome{Device: dev, Paired: dev.Paired}, err, false)
			}
			return
		}
		stage("Connecting…")
		out, err := bzConnect(ctx, dev, stage)
		if ctx.Err() != nil {
			return
		}
		btPostOutcome(gen, dev, out, err, false)
	})
	if !started {
		btClearBusy()
	}
}

// btToggleConnection is A (or a click) on a paired device: it connects one
// that is not connected and disconnects one that is. The device's sheet (Y)
// keeps the details and Forget.
func btToggleConnection(mac string) {
	d := btScreen.devices[mac]
	if d == nil {
		return
	}
	PlayActivateSound()
	if d.Connected {
		btStartDisconnect(mac)
		return
	}
	btStartConnect(mac)
}

// btPostOutcome reports a pairing or a connection on the main loop: the row
// updates, and one toast says what happened in words that match the evidence.
// It runs on the operation's goroutine, so it also reads the link count the
// operation left (btNoteLinks).
func btPostOutcome(gen int, dev bzDevice, out bzPairOutcome, err error, pairing bool) {
	links := btSysfsLinkCount()
	tbxPost(func() {
		btQuickKick()
		if btScreen.gen == gen {
			btNoteLinks(gen, links)
			btReleaseBusy(gen, dev.MAC)
			if err == nil {
				d := btScreen.devices[dev.MAC]
				if d == nil {
					cp := out.Device
					d = &cp
					btScreen.devices[dev.MAC] = d
				} else {
					bzMergeInfo(d, out.Device)
				}
				d.Paired = d.Paired || out.Paired
				d.Connected = out.Connected
				delete(btScreen.noPad, dev.MAC)
				if out.Connected {
					btScreen.connectedAt[dev.MAC] = time.Now()
				}
				// The flow waited bzInputWait for the controller: either way
				// that is evidence.
				if out.Controller {
					btScreen.padOK[dev.MAC] = true
				} else if out.Connected && d.kind() == bzKindController {
					btScreen.noPad[dev.MAC] = true
				}
				if out.Paired && btScreen.nav.focus == "near:"+dev.MAC {
					btScreen.nav.focus = "dev:" + dev.MAC
				}
			}
		}
		msg, kind := btOutcomeMessage(dev, out, err, pairing)
		ShowToast(msg, kind)
	})
}

// btPadWakeHint follows "it did not answer" for a controller: a pad that is
// off connects when its own button wakes it.
const btPadWakeHint = ". Press its PS or Home button to wake it, then try again"

// btOutcomeMessage is the one toast a pairing or a connection ends with. A pad
// is only called ready when the kernel made a controller for it.
func btOutcomeMessage(dev bzDevice, out bzPairOutcome, err error, pairing bool) (string, ToastKind) {
	name := dev.label()
	if out.Device.Name != "" {
		name = out.Device.Name
	}
	isPad := out.Device.kind() == bzKindController || dev.kind() == bzKindController
	if err != nil {
		verb := "connect"
		if pairing {
			verb = "pair"
		}
		msg := "Could not " + verb + " " + name + ": " + bzErrText(err)
		if isPad && !pairing && bzIsNoAnswer(err) {
			msg += btPadWakeHint
		}
		return msg, ToastKindError
	}
	switch {
	case !out.Connected && out.ConnectErr != nil:
		msg := "Paired " + name + ", but it did not connect: " + bzErrText(out.ConnectErr)
		if !pairing {
			msg = "Could not connect " + name + ": " + bzErrText(out.ConnectErr)
		}
		switch {
		case isPad && bzIsProfileUnavailable(out.ConnectErr):
			msg += ". Classic-Bluetooth controllers may not work on this firmware; a USB cable or a 2.4 GHz adapter does."
		case isPad && bzIsNoAnswer(out.ConnectErr):
			msg += btPadWakeHint
		}
		return msg, ToastKindWarning
	case !out.Connected:
		return "Paired " + name + " — press its PS or Home button to connect it", ToastKindInfo
	case isPad && out.Controller:
		return name + " is connected — ready for games and Remote Play", ToastKindSuccess
	case isPad:
		return name + " connected, but the system did not create a controller for it. This firmware may not support that pad over Bluetooth — a USB cable or a 2.4 GHz adapter works instead.", ToastKindWarning
	}
	return name + " is connected", ToastKindSuccess
}

// btStartDisconnect drops a connection; the device stays paired.
func btStartDisconnect(mac string) {
	d := btScreen.devices[mac]
	if d == nil || !btClaimBusy(mac, "Disconnecting…", "disconnecting") {
		return
	}
	dev, gen := *d, btScreen.gen
	started := runSceneTask(func(ctx context.Context) {
		err := bzDisconnect(ctx, dev.MAC)
		if ctx.Err() != nil {
			return
		}
		links := btSysfsLinkCount()
		tbxPost(func() {
			btQuickKick()
			btNoteLinks(gen, links)
			btReleaseBusy(gen, dev.MAC)
			if err != nil {
				ShowToast("Could not disconnect "+dev.label()+": "+bzErrText(err), ToastKindError)
				return
			}
			if btScreen.gen == gen {
				if d := btScreen.devices[dev.MAC]; d != nil {
					d.Connected = false
				}
				delete(btScreen.padOK, dev.MAC)
				delete(btScreen.noPad, dev.MAC)
				delete(btScreen.connectedAt, dev.MAC)
			}
			ShowToast(dev.label()+" disconnected", ToastKindInfo)
		})
	})
	if !started {
		btClearBusy()
	}
}

// btStartForget unpairs a device.
func btStartForget(mac string) {
	d := btScreen.devices[mac]
	if d == nil || !btClaimBusy(mac, "Forgetting…", "forgetting") {
		return
	}
	dev, gen := *d, btScreen.gen
	started := runSceneTask(func(ctx context.Context) {
		err := bzForget(ctx, dev.MAC)
		if ctx.Err() != nil {
			return
		}
		links := btSysfsLinkCount()
		tbxPost(func() {
			btQuickKick()
			btNoteLinks(gen, links)
			btReleaseBusy(gen, dev.MAC)
			if err != nil {
				ShowToast("Could not forget "+dev.label()+": "+bzErrText(err), ToastKindError)
				return
			}
			if btScreen.gen == gen {
				delete(btScreen.devices, dev.MAC)
				delete(btScreen.padOK, dev.MAC)
				delete(btScreen.noPad, dev.MAC)
				delete(btScreen.connectedAt, dev.MAC)
			}
			ShowToast(dev.label()+" forgotten — pair it again from Nearby to use it", ToastKindInfo)
		})
	})
	if !started {
		btClearBusy()
	}
}

// btToggleRadio switches the radio off-thread. Switching it on goes through the
// Bluetooth bring-up, which also starts what the firmware left stopped (the
// chip, bluetoothd); once it is on, the devices are read again, and a search
// starts only when nothing is paired. While a search is bringing Bluetooth up,
// or a device is being paired or connected, the switch waits: a second
// bring-up, or "off" under a connection being made, would only fight it.
func btToggleRadio() {
	if btScreen.radioBusy {
		return
	}
	if btScreen.stackWord != "" {
		ShowToast("Bluetooth is starting — one moment", ToastKindInfo)
		return
	}
	if btScreen.busyMAC != "" {
		ShowToast(btBusyText(), ToastKindInfo)
		return
	}
	on, known := btRadioCachedState()
	want := !(on && known)
	if !want && btScreen.scanCancel != nil {
		btScreen.scanCancel()
	}
	if !want {
		// A start Quick Settings left running must not bring the radio back
		// on behind this press.
		btCancelBringUpAsync()
	}
	btScreen.radioBusy = true
	btScreen.opSeq++
	gen := btScreen.gen
	PlayToggleSound()
	started := runSceneTask(func(ctx context.Context) {
		var err error
		if want {
			err = btBringUp(ctx, btRadioStageFunc(gen))
		} else {
			err = setBluetoothEnabled(false)
		}
		now, nowKnown := btProbeRadio()
		now, nowKnown = btRadioAfterSwitch(want, err, now, nowKnown)
		bzNoteRadio(now, nowKnown)
		links := btSysfsLinkCount()
		if ctx.Err() != nil {
			return
		}
		tbxPost(func() {
			if btScreen.gen != gen {
				return
			}
			btScreen.radioBusy = false
			btScreen.stackWord = ""
			btScreen.opSeq++
			btNoteLinks(gen, links)
			switch {
			case err != nil:
				ShowToast("Could not switch Bluetooth: "+firstLine(err.Error()), ToastKindError)
				return
			case !nowKnown:
				// The switch answered, the radio did not: claim nothing.
				ShowToast("Bluetooth did not report its state — try again", ToastKindWarning)
				return
			case !now:
				for _, d := range btScreen.devices {
					d.Connected = false
				}
				btScreen.padOK = map[string]bool{}
				btScreen.noPad = map[string]bool{}
				btScreen.connectedAt = map[string]time.Time{}
				if want {
					ShowToast("Bluetooth did not switch on — try again", ToastKindWarning)
					return
				}
				ShowToast("Bluetooth is off", ToastKindInfo)
				return
			}
			ShowToast("Bluetooth is on", ToastKindInfo)
			btRelist(true)
		})
	})
	if !started {
		btScreen.radioBusy = false
	}
}

// btRadioAfterSwitch is the radio's state after a switch to want: the probe's
// reading, except that a radio switched off whose adapter then went away (no
// "Powered:" line to read, so the probe cannot tell) is off.
func btRadioAfterSwitch(want bool, err error, now, known bool) (bool, bool) {
	if err == nil && !want && !known {
		return false, true
	}
	return now, known
}

// btRefreshPads reads which pads have a controller, at most every two
// seconds while the page is up. A pad that reconnected by itself shows as
// connected, and one whose controller went away as disconnected. The same
// reading counts the kernel's live links: when that number changes without the
// page doing anything (a headset switched off, a keyboard woken), the list is
// read again, so every kind of device follows, not only pads.
func btRefreshPads() {
	if btScreen.padReading || time.Since(btScreen.padReadAt) < 2*time.Second || btScreen.devices == nil {
		return
	}
	btScreen.padReading = true
	btScreen.padReadAt = time.Now()
	gen := btScreen.gen
	started := runSceneTask(func(ctx context.Context) {
		pads, anon := bzPadReading(bzReadInputDevices(), bits.UintSize)
		links := btSysfsLinkCount()
		if ctx.Err() != nil {
			return
		}
		tbxPost(func() {
			if btScreen.gen != gen {
				return
			}
			btScreen.padReading = false
			btScreen.padReadAt = time.Now()
			btApplyPadReading(pads, anon)
			if btScreen.listed && btScreen.links >= 0 && links != btScreen.links {
				btRelist(false)
			}
		})
	})
	if !started {
		btScreen.padReading = false
	}
}

// btApplyPadReading folds a reading of the attached pads into the list. A
// connected pad with a controller is ready. One without is only called "No
// input" once it has had bzInputWait to get one: a pad seen connecting a
// moment ago (connectedAt) reads "Checking…" until then. Controllers whose
// address the kernel did not record (anon) count for connected pads that have
// none, in address order. A pad whose controller went away has disconnected.
func btApplyPadReading(pads map[string]bool, anon int) {
	if btScreen.noPad == nil {
		btScreen.noPad = map[string]bool{}
	}
	now := time.Now()
	ok := make(map[string]bool, len(pads))
	for mac := range pads {
		ok[mac] = true
	}
	macs := make([]string, 0, len(btScreen.devices))
	for mac := range btScreen.devices {
		macs = append(macs, mac)
	}
	sort.Strings(macs)
	for _, mac := range macs {
		d := btScreen.devices[mac]
		if d == nil {
			continue
		}
		pad := d.kind() == bzKindController
		switch {
		case pads[mac]:
			d.Connected = true
			delete(btScreen.noPad, mac)
		case d.Connected && pad && anon > 0:
			anon--
			ok[mac] = true
			delete(btScreen.noPad, mac)
		case btScreen.padOK[mac]:
			d.Connected = false
			delete(btScreen.noPad, mac)
			delete(btScreen.connectedAt, mac)
		case d.Connected && pad:
			if at, seen := btScreen.connectedAt[mac]; !seen || now.Sub(at) >= bzInputWait {
				btScreen.noPad[mac] = true
			}
		}
	}
	btScreen.padOK = ok
}

// ── Lists ─────────────────────────────────────────────────────────────────

// btPairedList is the Paired section, in the page's order.
func btPairedList() []bzDevice {
	var out []bzDevice
	for _, d := range btScreen.devices {
		if d != nil && d.Paired {
			out = append(out, *d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return bzLess(out[i], out[j]) })
	return out
}

// btNearbyList is the Nearby section, and how many unnamed devices it hides.
func btNearbyList() ([]bzDevice, int) {
	var out []bzDevice
	unnamed := 0
	for _, d := range btScreen.devices {
		if d == nil || d.Paired {
			continue
		}
		if d.Name == "" {
			unnamed++
			if !btScreen.showUnnamed {
				continue
			}
		}
		out = append(out, *d)
	}
	sort.SliceStable(out, func(i, j int) bool { return bzLess(out[i], out[j]) })
	return out, unnamed
}

// ── Rows ──────────────────────────────────────────────────────────────────

// btNoHardware reports a computer without a Bluetooth adapter: nothing the
// page can switch on (the handhelds' "no adapter" is the firmware's switch
// being off, which the bring-up undoes).
func btNoHardware() bool {
	return btScreen.noAdapter && !IsTrimuiDevice()
}

// btRadioBusy reports the switch or the bring-up at work: the switch's row
// shows the work, and A on it has nothing to do until that ends.
func btRadioBusy() bool {
	return btScreen.radioBusy || btScreen.stackWord != ""
}

// btRadioRow is the switch row.
func btRadioRow(on, known bool) tbxRowSpec {
	spec := tbxRowSpec{icon: "bluetooth", title: "Bluetooth"}
	switch {
	case btScreen.stackWord != "":
		// The bring-up runs for the switch or a search here; Connect and
		// Pair report theirs on the device's own row.
		spec.subtitle = btScreen.stackWord
		spec.pill, spec.tone = "…", tbxToneNeutral
	case btScreen.radioBusy:
		spec.subtitle = "Switching…"
		spec.pill, spec.tone = "…", tbxToneNeutral
	case btNoHardware():
		spec.subtitle = "No Bluetooth adapter on this computer"
		spec.pill, spec.tone = "None", tbxToneNeutral
	case !known:
		spec.subtitle = "Its state is unknown — choose to switch it on"
		spec.pill, spec.tone = "Turn on", tbxToneNeutral
	case on:
		spec.subtitle = "On — controllers, headphones and keyboards can connect"
		spec.pill, spec.tone = "On", tbxToneGood
	default:
		spec.subtitle = "Off — switch it on to pair a controller"
		spec.pill, spec.tone = "Off", tbxToneNeutral
	}
	return spec
}

// btPairedRow is one paired device.
func btPairedRow(d bzDevice) tbxRowSpec {
	kind := d.kind()
	spec := tbxRowSpec{icon: kind.icon(), title: d.label()}
	switch {
	case btScreen.busyMAC == d.MAC:
		spec.subtitle = kind.word() + " · " + btScreen.busyWord
		spec.pill, spec.tone = btScreen.busyWord, tbxToneAccent
	case d.Connected && kind == bzKindController && btScreen.padOK[d.MAC]:
		spec.subtitle = "Controller · connected — works in games and Remote Play"
		spec.pill, spec.tone = "Connected", tbxToneGood
	case d.Connected && kind == bzKindController && btScreen.noPad[d.MAC]:
		spec.subtitle = "Connected, but no controller appeared — Options has the details"
		spec.pill, spec.tone = "No input", tbxToneWarn
	case d.Connected && kind == bzKindController:
		spec.subtitle = "Controller · connected — checking that the system sees it"
		spec.pill, spec.tone = "Checking…", tbxToneNeutral
	case d.Connected:
		spec.subtitle = kind.word() + " · connected"
		spec.pill, spec.tone = "Connected", tbxToneGood
	case kind == bzKindController:
		spec.subtitle = "Controller · press its PS or Home button, or choose Connect"
		spec.pill, spec.tone = "Connect", tbxToneAccent
	default:
		spec.subtitle = kind.word() + " · not connected"
		spec.pill, spec.tone = "Connect", tbxToneAccent
	}
	return spec
}

// btNearbyRow is one device a search found.
func btNearbyRow(d bzDevice) tbxRowSpec {
	kind := d.kind()
	spec := tbxRowSpec{icon: kind.icon(), title: d.label(), pill: "Pair", tone: tbxToneAccent}
	spec.subtitle = kind.word() + " · not paired"
	if d.Name == "" {
		spec.subtitle = "Sent no name · not paired"
		spec.dim = true
	}
	if btScreen.busyMAC == d.MAC {
		spec.subtitle = kind.word() + " · " + btScreen.busyWord
		spec.pill = btScreen.busyWord
		spec.dim = false
	}
	return spec
}

// btScanRow is the search row.
func btScanRow() tbxRowSpec {
	spec := tbxRowSpec{icon: "radar", title: "Search for devices", pill: "Search", tone: tbxToneAccent,
		subtitle: "Finds controllers and headphones in pairing mode nearby (10 s)"}
	if btScreen.scans > 0 {
		spec.title = "Search again"
	}
	switch {
	case btScreen.scanning && btScreen.stackWord != "":
		// Bluetooth is still being started for the search: the countdown
		// begins once it is on.
		spec.subtitle = btScreen.stackWord + " The search starts when it is on."
		spec.pill, spec.tone = "…", tbxToneNeutral
	case btScreen.scanning:
		spec.subtitle = "Searching… " + strconv.Itoa(btSecondsLeft()) + " s left — put the pad in pairing mode now"
		spec.pill, spec.tone = "Searching", tbxToneGood
	}
	return spec
}

// ── Drawing ───────────────────────────────────────────────────────────────

// btHeader is the page bar, with the radio's or the work's state opposite the
// title.
func btHeader() UIPageHeader {
	status, ink := "", ColorTextTertiary()
	on, known := btRadioCachedState()
	switch {
	case !btDevicesAvailable():
		// Where the page opens the system's own settings it works, if not
		// here: "Unavailable" would contradict its one row.
		if btSystemSettingsWhere() == "" {
			status = "Unavailable"
		}
	case btNoHardware():
		status = "No adapter"
	case btScreen.radioBusy || btScreen.stackWord != "":
		status, ink = "Switching on", ColorTextSecondary()
		if btScreen.radioBusy && on && known {
			status = "Switching off"
		}
	case btScreen.busyMAC != "":
		status, ink = strings.TrimSuffix(btScreen.busyWord, "…"), ColorTextSecondary()
	case btScreen.scanning:
		status, ink = "Searching", ToastSuccessInk()
	case known && on:
		status, ink = "On", ToastSuccessInk()
	case known:
		status = "Off"
	}
	return tbxHeader("Bluetooth", "Pair controllers for games and Remote Play, and headphones", "bluetooth", status, ink)
}

// btRenderScreen draws the page.
func btRenderScreen(renderer *sdl.Renderer, config *Config) {
	if btScreen.visit.begin() {
		btScreenEnter()
	}
	if btDevicesAvailable() {
		btRefreshPads()
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, btHeader())
	body := frame.bodyRect()
	nav := &btScreen.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	btRenderBody(renderer, config, fonts, frame)
	tbxUnclip(renderer)
	btKeepFocusPlace(nav)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	peerEndFrame(renderer, config, btScreenHints())
}

// btKeepFocusPlace runs after the rows are laid out. When the focused row is
// gone — a device forgotten, paired (its Nearby row moves up to Paired), out of
// range, or a reading that replaced the list — the focus goes to the row now
// in its place. Left to the list's own rule it would jump to the first row,
// the Bluetooth switch, where the next A, meant for a device, switches
// Bluetooth off.
func btKeepFocusPlace(nav *tbxNav) {
	if i := nav.index(); i >= 0 {
		btScreen.focusAt = i
		return
	}
	if btScreen.focusAt <= 0 || len(nav.items) == 0 {
		return
	}
	i := btScreen.focusAt
	if i > len(nav.items)-1 {
		i = len(nav.items) - 1
	}
	for i > 0 && nav.items[i].disabled {
		i--
	}
	btScreen.focusAt = i
	nav.setFocus(nav.items[i].id)
}

func btRenderBody(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame) {
	nav := &btScreen.nav
	top := frame.bodyTop - nav.scroll
	x, w := frame.x, frame.w
	rowH := tbxRowHeight(fonts)
	y := int32(0)
	row := 0
	addRow := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		tbxDrawRow(renderer, config, fonts, peerScr(rect, top), spec, nav.focused(id))
		nav.add(id, rect, row, false)
		row++
		y += rowH + 8
	}
	card := func(icon, title, text string, h int32) {
		rect := sdl.Rect{X: x, Y: y, W: w, H: h}
		UICard(renderer, peerScr(rect, top), DefaultUICardStyle())
		tbxDrawEmpty(renderer, config, peerScr(rect, top), icon, title, text)
		y += h + 8
	}
	section := func(title, icon string) {
		y += 14
		tbxDrawSection(renderer, config, title, icon, x, top+y, w)
		y += tbxSectionHeight
	}

	if !btDevicesAvailable() {
		card("bluetooth", "Bluetooth pairing is not available here", btUnavailableText(), 180)
		// Windows and macOS (and a Linux desktop with its own settings app)
		// pair devices in their own settings: one press takes the user there.
		if where := btSystemSettingsWhere(); where != "" {
			addRow("ossettings", tbxRowSpec{icon: "settings", title: "Open Bluetooth settings",
				subtitle: where, pill: "Open", tone: tbxToneAccent})
		}
		nav.extend(y)
		return
	}

	on, known := btRadioCachedState()
	addRow("radio", btRadioRow(on, known))
	// The cost of the radio, under its switch: quiet, but always there.
	lines := btScreen.noteWrap.get(fonts.small, btWiFiNote, w-28)
	for _, line := range lines {
		renderText(renderer, config, fonts.small, line, ColorTextTertiary(), x+14, top+y)
		y += fonts.smallH + 2
	}
	y += 6

	section("Paired", "bluetooth")
	paired := btPairedList()
	switch {
	case len(paired) > 0:
		for _, d := range paired {
			addRow("dev:"+d.MAC, btPairedRow(d))
		}
	case !btScreen.listed:
		card("bluetooth", "Reading your devices…", "", 110)
	case btScreen.listErr != "":
		card("warning", "Could not read the paired devices", btScreen.listErr, 132)
	case btNoHardware():
		card("bluetooth", "No Bluetooth adapter", "This computer has no Bluetooth adapter the system can use. Plug one in, then come back to this page.", 132)
	case known && !on && btScreen.noAdapter:
		// No adapter, no list: BlueZ only shows the devices of an adapter it
		// has. Switching on brings the chip up and reads them.
		card("bluetooth", "Bluetooth is off", "Switch it on above to see and connect your paired devices.", 132)
	default:
		card("gamepad", "No paired devices yet", "Put a controller in pairing mode, then pick it under Nearby.", 132)
	}

	section("Nearby", "radar")
	nearby, unnamed := btNearbyList()
	for _, d := range nearby {
		addRow("near:"+d.MAC, btNearbyRow(d))
	}
	if len(nearby) == 0 {
		// A search under way, then why the last one did not run (Bluetooth
		// could not be started for it), then the radio being off.
		switch {
		case btScreen.scanning:
			card("radar", "Searching…", "Put the controller in pairing mode now — it appears here within a few seconds.", 132)
		case btScreen.scanErr != "":
			card("warning", "The search did not run", btScreen.scanErr, 132)
		case btNoHardware():
			// The Paired card above already says there is no adapter.
		case known && !on:
			card("bluetooth", "Bluetooth is off", "Switch it on to search for controllers and headphones.", 132)
		case btScreen.scans > 0:
			card("radar", "Nothing new found", "Hold the pad's pairing buttons until its light blinks, then search again.", 132)
		}
	}
	if unnamed > 0 {
		label := "Show " + strconv.Itoa(unnamed) + " unnamed device"
		if unnamed > 1 {
			label += "s"
		}
		sub := "Devices that only sent their address — rarely a controller"
		if btScreen.showUnnamed {
			label, sub = "Hide unnamed devices", "Back to the devices that sent a name"
		}
		addRow("unnamed", tbxRowSpec{icon: "list", title: label, subtitle: sub})
	}
	addRow("scan", btScanRow())
	addRow("howto", tbxRowSpec{icon: "info", title: "Put a controller in pairing mode",
		subtitle: "DualSense, DualShock 4, Xbox and 8BitDo — the buttons to hold", pill: "How", tone: tbxToneNeutral})
	nav.extend(y)
}

// btUnavailableText explains a system without bluetoothctl.
func btUnavailableText() string {
	switch {
	case IsWindows():
		return "On Windows, pair controllers and headphones in Settings → Bluetooth & devices; JukaHub uses any controller Windows connects."
	case runtime.GOOS == "darwin":
		return "On a Mac, pair controllers and headphones in System Settings → Bluetooth; JukaHub uses any controller macOS connects."
	}
	return "This system has no bluetoothctl, which JukaHub pairs devices with. A USB cable or a 2.4 GHz USB adapter connects a controller without it."
}

// btSystemSettingsCache remembers where the system's own Bluetooth settings
// are: finding a Linux desktop's tool walks PATH, and the page asks every frame.
var btSystemSettingsCache struct {
	once  sync.Once
	where string
}

// btSystemSettingsWhere names the system's own Bluetooth settings for the
// unavailable page's row, or "" where openSystemSettingsPage has none to open.
func btSystemSettingsWhere() string {
	c := &btSystemSettingsCache
	c.once.Do(func() {
		switch runtime.GOOS {
		case "windows":
			c.where = "Windows Settings → Bluetooth & devices"
		case "darwin":
			c.where = "System Settings → Bluetooth"
		case "linux":
			if name, _ := linuxDesktopSettingsCommand("bluetooth"); name != "" {
				c.where = "Your desktop's Bluetooth settings"
			}
		}
	})
	return c.where
}

// btOpenSystemSettings opens the system's own Bluetooth settings.
func btOpenSystemSettings() {
	PlayActivateSound()
	if err := openSystemSettingsPage("bluetooth"); err != nil {
		ShowToast("Could not open the Bluetooth settings: "+firstLine(err.Error()), ToastKindError)
		return
	}
	ShowToast("Bluetooth settings opened — devices you connect there work in JukaHub", ToastKindInfo)
}

// btScreenHints is the footer: the focused row's own action first.
func btScreenHints() []FooterHint {
	if !btDevicesAvailable() {
		// The explanation card, and at most the row that opens the system's
		// own settings.
		if btSystemSettingsWhere() != "" {
			return []FooterHint{{Button: "A", Label: "Open"}, {Button: "B", Label: "Back"}}
		}
		return []FooterHint{{Button: "B", Label: "Back"}}
	}
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	id := btScreen.nav.focus
	switch {
	case id == "radio":
		// While the switch or a bring-up is at work A has nothing to do
		// there, and with no adapter nothing to switch.
		if btRadioBusy() || btNoHardware() {
			break
		}
		label := "Turn on"
		if on, known := btRadioCachedState(); on && known {
			label = "Turn off"
		}
		hints = append(hints, FooterHint{Button: "A", Label: label})
	case strings.HasPrefix(id, "dev:"):
		label := "Connect"
		if d := btScreen.devices[strings.TrimPrefix(id, "dev:")]; d != nil && d.Connected {
			label = "Disconnect"
		}
		hints = append(hints, FooterHint{Button: "A", Label: label}, FooterHint{Button: "Y", Label: "Options"})
	case strings.HasPrefix(id, "near:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Pair"})
	case id == "unnamed":
		label := "Show"
		if btScreen.showUnnamed {
			label = "Hide"
		}
		hints = append(hints, FooterHint{Button: "A", Label: label})
	case id == "howto":
		hints = append(hints, FooterHint{Button: "A", Label: "Open"})
	case id == "scan":
		hints = append(hints, FooterHint{Button: "A", Label: "Search"})
	}
	if id != "scan" {
		hints = append(hints, FooterHint{Button: "X", Label: "Search"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

// ── Input ─────────────────────────────────────────────────────────────────

// btScreenAction handles one action on the page (the sheet has had its turn).
// Up at the top is left unhandled so the top bar can take it.
func btScreenAction(config *Config, act tbxAction) bool {
	nav := &btScreen.nav
	switch act {
	case tbxActConfirm:
		if nav.focus == "radio" && !btScreen.moved && !btScreen.listed {
			if on, known := btRadioCachedState(); on && known {
				// The page has only just opened — often from the top bar,
				// to connect something — and the devices are still being
				// read, so the focus is on the switch: A there now would
				// switch Bluetooth off under a connected pad or headset.
				// Switching it on is never held back.
				ShowToast("Reading your devices…", ToastKindInfo)
				return true
			}
		}
		// A deliberate press: the first listing leaves the focus alone.
		btScreen.moved = true
		btScreenActivate(config, nav.focus)
		return true
	case tbxActX:
		btScreen.moved = true
		if btDevicesAvailable() {
			PlayActivateSound()
			btStartScan()
		}
		return true
	case tbxActY:
		// Y is a paired device's sheet: its details and Forget (A connects
		// or disconnects it directly).
		btScreen.moved = true
		if mac, ok := strings.CutPrefix(nav.focus, "dev:"); ok {
			btOpenDeviceSheet(config, mac)
		}
		return true
	case tbxActBack:
		return false
	}
	btScreen.moved = true
	return tbxNavAction(nav, act)
}

// btScreenActivate runs the focused row's action.
func btScreenActivate(config *Config, id string) {
	switch {
	case id == "radio":
		btToggleRadio()
	case id == "scan":
		PlayActivateSound()
		btStartScan()
	case id == "unnamed":
		PlayToggleSound()
		btScreen.showUnnamed = !btScreen.showUnnamed
	case id == "howto":
		PlayActivateSound()
		btOpenHowToSheet(config)
	case id == "ossettings":
		btOpenSystemSettings()
	case strings.HasPrefix(id, "dev:"):
		btToggleConnection(strings.TrimPrefix(id, "dev:"))
	case strings.HasPrefix(id, "near:"):
		btStartPair(strings.TrimPrefix(id, "near:"))
	}
}

// btScenesClick routes a click or tap on the page (main.go calls it beside the
// peer pages' hook): the open sheet first, then the rows. A miss is left for
// the bars.
func btScenesClick(mx, my int32, config *Config) bool {
	if tbxCurrentScene(config) != btSceneName {
		return false
	}
	if tbxSheetVisible(config) {
		tbxSheetMouse(mx, my)
		return true
	}
	it, ok := btScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	btScreen.moved = true
	btScreen.nav.focus = it.id
	btScreenActivate(config, it.id)
	return true
}

// btScenesWheel scrolls the page or its open sheet.
func btScenesWheel(config *Config, y int32) bool {
	if tbxCurrentScene(config) != btSceneName || y == 0 {
		return false
	}
	if tbxSheetVisible(config) {
		if y > 0 {
			tbxSheetAction(tbxActUp)
		} else {
			tbxSheetAction(tbxActDown)
		}
		return true
	}
	btScreen.nav.scrollBy(-y * 48)
	return true
}

// ── Sheets ────────────────────────────────────────────────────────────────

// btOpenDeviceSheet shows a paired device and what can be done with it.
func btOpenDeviceSheet(config *Config, mac string) {
	d := btScreen.devices[mac]
	if d == nil {
		return
	}
	if btScreen.busyMAC == mac {
		ShowToast(btBusyText(), ToastKindInfo)
		return
	}
	dev := *d
	PlayActivateSound()
	kind := dev.kind()
	status := "Not connected"
	if dev.Connected {
		status = "Connected"
	}
	var b strings.Builder
	b.WriteString("## " + kind.word() + "\n" + status)
	if dev.Trusted {
		b.WriteString(" · reconnects by itself")
	}
	b.WriteString("\n")
	if kind == bzKindController {
		b.WriteString("\n## Games and Remote Play\n")
		switch {
		case btScreen.padOK[mac]:
			b.WriteString("The system made a controller for it: it works in games and in PS Remote Play, next to the built-in controls.\n")
		case dev.Connected && btScreen.noPad[mac]:
			b.WriteString("It is connected, but the system did not create a controller for it. This firmware may not support this pad over Bluetooth (classic-Bluetooth pads such as the DualShock 4 and the DualSense are the usual case). A USB cable or a 2.4 GHz USB adapter works instead.\n")
		case dev.Connected:
			b.WriteString("It is connected. JukaHub is still checking that the system made a controller for it — this takes a few seconds.\n")
		default:
			b.WriteString("Press its PS or Home button and it connects by itself, or choose Connect.\n")
		}
	}
	b.WriteString("\n## Wi-Fi\nBluetooth shares the radio with Wi-Fi here. If a Remote Play or video stream stutters, choose a lower stream quality, or use the pad on a USB cable.")
	primaryLabel := "Connect"
	primaryRun := func() { btStartConnect(mac) }
	if dev.Connected {
		primaryLabel = "Disconnect"
		primaryRun = func() { btStartDisconnect(mac) }
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    dev.label(),
		subtitle: kind.word() + " · " + status,
		icon:     kind.icon(),
		body:     b.String(),
		buttons: []tbxSheetButton{
			{label: "Forget", run: func() { btConfirmForget(config, dev) }},
			{label: primaryLabel, primary: true, run: primaryRun},
		},
		focus: 1,
	})
}

// btConfirmForget asks before unpairing.
func btConfirmForget(config *Config, dev bzDevice) {
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Forget " + dev.label() + "?",
		subtitle: "Unpair",
		icon:     dev.kind().icon(),
		body: "The handheld drops its pairing with " + dev.label() + ". To use it again, put it in pairing mode " +
			"and pair it from Nearby.",
		buttons: []tbxSheetButton{
			{label: "Keep"},
			{label: "Forget", primary: true, run: func() { btStartForget(dev.MAC) }},
		},
	})
}

// btOpenHowToSheet is the pairing-mode cheat sheet.
func btOpenHowToSheet(config *Config) {
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Pairing mode",
		subtitle: "Make the controller visible, then pick it under Nearby",
		icon:     "gamepad",
		body: "## PlayStation\n" +
			"• DualSense (PS5): hold Create and the PS button until the light bar blinks quickly\n" +
			"• DualShock 4 (PS4): hold Share and the PS button until the light bar flashes\n\n" +
			"## Xbox\n" +
			"• Xbox Wireless Controller: switch it on, then hold the small pair button on top until the Xbox button flashes fast\n\n" +
			"## 8BitDo and others\n" +
			"• Choose the controller's mode first (its manual lists the button combinations), then hold its pair button until the light blinks\n\n" +
			"## Good to know\n" +
			"• A PlayStation pad remembers one system at a time: pairing it here unpairs it from the console until you plug it into the console with a cable again\n" +
			"• On some firmware a pairing does not survive a restart — pair again here if a pad stops connecting\n" +
			"• Bluetooth and Wi-Fi share one radio on this handheld; a stream can stutter while a pad is connected",
		buttons: []tbxSheetButton{{label: "Search now", primary: true, run: btStartScan}},
	})
}

// btSettingsValue is Settings → Network → Bluetooth devices' read-out: the
// Bluetooth pads attached right now, or nothing (a pad on a cable is not one
// of this row's devices).
func btSettingsValue(*Config) string { return btBluetoothPadSummary() }

// btStartMenuInsert adds the page to the Start menu's Device group, after
// Wi-Fi (or Quick Settings), where this system can pair devices.
func btStartMenuInsert(device []startMenuEntry) []startMenuEntry {
	if !btDevicesAvailable() {
		return device
	}
	entry := startMenuEntry{name: "Bluetooth", icon: "B", glyph: "bluetooth", action: func(c *Config) { btScreenOpen(c) }}
	at := len(device)
	for i, e := range device {
		switch e.name {
		case "Bluetooth":
			return device
		case "Wi-Fi Networks", "Quick Settings":
			at = i + 1
		}
	}
	out := make([]startMenuEntry, 0, len(device)+1)
	out = append(out, device[:at]...)
	out = append(out, entry)
	return append(out, device[at:]...)
}
