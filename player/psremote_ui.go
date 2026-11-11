package main

// psremote_ui.go — the PS Remote Play panel.
//
// This is the whole user-facing client: it scans the LAN, pairs a console,
// remembers it, picks a quality and hands the display to `chiaki-stream`. There
// is no Python launcher and no language setting: the port's Python menus are
// gone, so everything the user reads here is JukaHub's own English UI.
//
// The panel keeps its console list in memory and repaints from it, because a
// scan runs in the background and the frame loop must never block on the
// network. Pairing is the same: a goroutine does the handshake and the panel
// shows a busy row until it reports back.
//
// A PS5, and a PS4 from firmware 7.0 on, also needs the PSN Account-ID before
// the PIN. That is the step people got stuck on — no console menu shows it —
// so pairing such a console without one opens the Account-ID card instead of
// a bare keyboard: a QR code for the phone's sign-in page, the PC's browser
// where there is one, or typing/pasting it (psn_signin.go does the sign-in).
// Once the Account-ID is saved the pairing goes straight on to the PIN, with
// the console's own menu path to find it.

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// psPanelEdit names the field the on-screen keyboard is editing.
type psPanelEdit int

const (
	psEditNone psPanelEdit = iota
	psEditHost
	psEditPIN
	psEditAccount
)

// psPanelPage is the view the panel shows: the console list, or the card that
// gets the PSN Account-ID.
type psPanelPage int

const (
	psPageConsoles psPanelPage = iota
	psPageAccount
)

type psPanelAction int

const (
	psActionNone psPanelAction = iota
	psActionPlay
	psActionPair
	psActionForget
	psActionScan
	psActionAddHost
	psActionAccount
	psActionQuality
	psActionVolume
	psActionClose
	psActionSignInPhone
	psActionSignInBrowser
	psActionTypeAccount
	psActionAccountHelp
	psActionAccountBack
)

type chiakiRow struct {
	label  string
	detail string
	status string
	action psPanelAction
	host   string
}

type chiakiPanelState struct {
	open   bool
	cursor int
	scroll int

	// consoles is the merged list: paired hosts from disk plus this scan.
	consoles []psConsole
	scanning bool
	scanErr  string
	busy     string

	// pairHost is the console the next PIN submits for; pairTarget is the
	// protocol target discovery reported, so PS4 firmware 8/9/10 all pair
	// correctly.
	pairHost   string
	pairTarget int
	code       string // last pairing error, kept visible in the panel

	host    string
	account string
	quality int
	volume  int

	// page is the view on screen; listCursor keeps the console list's place
	// while the Account-ID card is up.
	page       psPanelPage
	listCursor int
}

var chiakiPanel chiakiPanelState
var chiakiPanelRect sdl.Rect
var chiakiRowHits []struct {
	index int
	rect  sdl.Rect
}

// psPanelMu guards the fields a background scan or pairing writes.
var psPanelMu sync.Mutex

// psPanelEditField names the field the PS Remote keyboard is editing, so the
// same keyboard can serve the address, the Account-ID and the PIN.
var psPanelEditField psPanelEdit

// psPanelPairAfterAccount remembers that the Account-ID prompt was opened by a
// pairing press, so submitting it continues straight to the PIN instead of
// leaving the user to press the console row again.
var psPanelPairAfterAccount bool

// psSignInCode is the code on the Account-ID card: the phone sign-in link it
// encodes, its bitmap (the full-screen card draws that), the bare symbol, and
// the rects the last frame filled.
type psSignInCode struct {
	url    string
	bitmap [][]bool
	core   [][]bool
	origin sdl.Point
	module int32
	rects  []sdl.Rect
}

// psSignInQR is the card's code, rebuilt only when the Phone Companion's link
// changes. Main thread only.
var psSignInQR psSignInCode

// psSignInQRRect is the code as drawn, so a tap can enlarge it; empty when the
// card does not show one.
var psSignInQRRect sdl.Rect

// psSignInQRTitle heads the enlarged code, which is how psPanelAccountArrived
// knows the card on screen is this one.
const psSignInQRTitle = "Sign in with your phone"

// psSignInSteps sit beside the phone code: what the phone page walks through,
// so nothing on it comes as a surprise.
var psSignInSteps = []string{
	"1  Scan this with your phone (same Wi-Fi)",
	"2  Sign in to PlayStation on the page it opens",
	"3  Paste the address of the \"redirect\" page",
}

// psAccountHelpText is the longer answer to "Where do I find it?".
const psAccountHelpText = "The PSN Account-ID is a number Sony keeps for your account — not your sign-in " +
	"name and not the PIN, and no console menu shows it. Sign in once (phone or browser) and JukaHub " +
	"fetches it; desktop chiaki-ng also shows it after PSN Login."

func openChiakiPanel() {
	psOpenPanelFor("", 0)
}

// psOpenPanelFor opens the panel and, when the press came from the home PS4 or
// PS5 tile, starts that console instead of making the user pick again.
func psOpenPanelFor(host string, target int) {
	qs.open = false
	a11y.open = false
	smartHomePanel.open = false
	toolsPanel.open = false
	emuPanel.open = false
	gpPanel.open = false
	winAppsPanel = windowsAppsPanelState{}
	portsPanel = portsPanelState{}
	raPanel.open = false
	chiakiPanel = chiakiPanelState{open: true}
	chiakiPanelRect = sdl.Rect{}
	chiakiRowHits = nil
	psSignInQRRect = sdl.Rect{}
	psPanelEditField = psEditNone
	psPanelPairAfterAccount = false
	state := loadPSHosts()
	chiakiPanel.host = ""
	chiakiPanel.account = state.Settings.Account
	chiakiPanel.quality = state.Settings.Quality
	chiakiPanel.volume = state.Settings.Volume
	chiakiPanel.consoles = state.Hosts
	// The stream rows read their settings from this, not from the disk every
	// frame (psremote_stream_opts.go).
	psStreamSettingsSet(state.Settings)
	// A tile press names the kind of console it wants, so the cursor starts on
	// the first console of that kind instead of on a row of the wrong one.
	for i, c := range state.Hosts {
		if host != "" && c.Host != host {
			continue
		}
		if target >= psTargetPS5 && !(c.PS5 || c.Target >= psTargetPS5) {
			continue
		}
		if target > 0 && target < psTargetPS5 && (c.PS5 || c.Target >= psTargetPS5) {
			continue
		}
		if c.paired() {
			chiakiPanel.cursor = i
			break
		}
	}
	PlayActivateSound()
	psPanelScan()
	// A JukaHub without the native helpers fetches them now, while the user
	// is still finding the console's PIN (psremote_helpers_install.go).
	psHelpersAutoStart()
	// Check the stream helper now, off the main thread, so Play does not wait
	// for its one start (psremote_preflight.go keeps the answer).
	go psWarmStreamPreflight()
}

func closeChiakiPanel() {
	if !chiakiPanel.open {
		return
	}
	chiakiPanel = chiakiPanelState{}
	chiakiPanelRect = sdl.Rect{}
	chiakiRowHits = nil
	psSignInQRRect = sdl.Rect{}
	psPanelEditField = psEditNone
	psPanelPairAfterAccount = false
	// A Play or a PIN prompt waiting for the helpers' install must not act
	// by itself later; the toast says it was let go (the install goes on).
	psHelpersCancelHeld(false)
	PlayBackSound()
}

// psPanelSaveSettings persists quality, volume and the PSN Account-ID. The
// load and the save hold psHostsMu together, like every load-change-save of
// psremote.json: a pairing or a re-home a goroutine saved in between was
// otherwise written over with the list read before it.
func psPanelSaveSettings() {
	psHostsMu.Lock()
	defer psHostsMu.Unlock()
	state := loadPSHosts()
	state.Settings.Quality = chiakiPanel.quality
	state.Settings.Volume = chiakiPanel.volume
	state.Settings.Account = chiakiPanel.account
	if err := savePSHostsLocked(state); err != nil {
		logWarn("psremote", "saving settings failed: %v", err)
	}
}

// psPanelScan starts a background discovery sweep.
func psPanelScan() {
	psPanelMu.Lock()
	if chiakiPanel.scanning {
		psPanelMu.Unlock()
		return
	}
	chiakiPanel.scanning = true
	chiakiPanel.scanErr = ""
	psPanelMu.Unlock()
	go func() {
		found, err := psScanConsoles(context.Background(), 3*time.Second)
		psPanelMu.Lock()
		chiakiPanel.scanning = false
		if err != nil {
			chiakiPanel.scanErr = firstLine(err.Error())
		}
		psPanelMu.Unlock()
		// Every sweep is remembered, an empty one too: a paired console that
		// stayed silent is woken first, one in rest mode says so, and one that
		// answered from a new address takes its pairing along (psremote_wake.go).
		psNoteScanDone(found, err == nil)
		if len(found) > 0 {
			psPanelMergeDiscovered(found)
		}
	}()
}

// psPanelMergeDiscovered folds a scan into the list without touching stored
// credentials: an unpaired console is only ever a row, never a saved host.
func psPanelMergeDiscovered(found []psDiscovered) {
	state := loadPSHosts()
	psPanelMu.Lock()
	defer psPanelMu.Unlock()
	consoles := append([]psConsole{}, state.Hosts...)
	now := time.Now()
	for _, d := range found {
		console := psConsole{Host: d.Host, Name: d.Name, Target: d.Target, PS5: d.PS5, LastSeen: now}
		merged := false
		for i := range consoles {
			if consoles[i].Host != console.Host {
				continue
			}
			consoles[i].Name = console.Name
			consoles[i].Target = console.Target
			consoles[i].PS5 = consoles[i].PS5 || console.PS5
			consoles[i].LastSeen = now
			merged = true
			break
		}
		if !merged {
			consoles = append(consoles, console)
		}
	}
	chiakiPanel.consoles = consoles
}

// psPanelReload rereads the stored hosts after a pair or a forget.
func psPanelReload() {
	state := loadPSHosts()
	psPanelMu.Lock()
	chiakiPanel.consoles = state.Hosts
	psPanelMu.Unlock()
}

// psPanelPair runs the pairing handshake off the event thread.
func psPanelPair(host, pin string, target int) {
	host = strings.TrimSpace(host)
	if host == "" {
		return
	}
	psPanelMu.Lock()
	if busy := chiakiPanel.busy; busy != "" {
		psPanelMu.Unlock()
		// One handshake at a time. The other consoles' rows still say "Pair",
		// so a PIN typed for one of them used to vanish without a word.
		ShowToast(strings.TrimSuffix(busy, "…")+" — try this console when it finishes", ToastKindInfo)
		return
	}
	chiakiPanel.busy = "Pairing " + host + "…"
	chiakiPanel.code = ""
	psPanelMu.Unlock()

	isPS5 := target >= psTargetPS5
	account := chiakiPanel.account
	// Only an address no scan has identified may turn out to be a PS5.
	unidentified := psPanelPairKind(host) == 0
	go func() {
		var console psConsole
		var err error
		if isPS5 {
			// A known PS5 is asked for its PIN only once chiaki-regist is in
			// (psHelpersHoldPIN); this covers one that went missing since.
			if err = psHelpersForPS5Pairing(); err == nil {
				console, err = ps5Register(psRemoteEngine(), host, pin, account, 25*time.Second)
			}
		} else {
			// Try the generation discovery reported first, then the older ones:
			// a manually added address has no discovery reply to learn from.
			for _, candidate := range psPanelPS4Targets(target) {
				console, err = ps4Register(host, pin, account, candidate, 15*time.Second)
				if err == nil {
					break
				}
			}
			// A typed address may be a PS5; with an Account-ID saved and the
			// PS5 helper in place, try the PS5 handshake before giving up
			// (its PIN prompt waited for an install that could come). A PS4
			// a scan found never tries it, and keeps its own reason: the
			// PS5's replaces it only when a PS5 answered there and refused.
			if err != nil && unidentified && strings.TrimSpace(account) != "" {
				if _, regist := psHelpersPresent(); regist {
					c5, err5 := ps5Register(psRemoteEngine(), host, pin, account, 25*time.Second)
					switch {
					case err5 == nil:
						console, err = c5, nil
					case strings.Contains(err5.Error(), "failed at protocol"):
						err = err5
					}
				}
			}
		}
		psPanelMu.Lock()
		chiakiPanel.busy = ""
		if err != nil {
			toast, code, noHelpers := psPairNoHelpersText(err)
			if !noHelpers {
				code = firstLine(err.Error())
				toast = "Pairing failed — " + code
			}
			chiakiPanel.code = code
			psPanelMu.Unlock()
			ShowToast(toast, ToastKindError)
			return
		}
		psPanelMu.Unlock()
		upsertPSConsole(console)
		psPanelReload()
		ShowToast(console.label()+" paired", ToastKindSuccess)
	}()
}

// psPanelPlay starts the native client and releases the display.
func psPanelPlay(c psConsole) {
	if !c.paired() {
		ShowToast("Pair this console first", ToastKindWarning)
		return
	}
	// A missing chiaki-stream is installed first and this press made again
	// when it is there (psremote_helpers_install.go).
	if psHelpersHoldPlay(c) {
		return
	}
	engine := psRemoteEngine()
	if !engine.installed() {
		ShowToast("The streaming helper is missing — "+psRuntimeInstallHint(), ToastKindWarning)
		return
	}
	psPanelMu.Lock()
	settings := psRemoteSettings{Quality: chiakiPanel.quality, Volume: chiakiPanel.volume, Account: chiakiPanel.account}
	psPanelMu.Unlock()
	if err := psRemoteStream(c, settings); err != nil {
		ShowToast("Could not start Remote Play — "+firstLine(err.Error()), ToastKindError)
		return
	}
	// The panel closes only now: closing it first zeroed chiakiPanel, so the
	// stream ran at the default quality and volume, and the save below wrote
	// Quality 0, Volume 0 and an empty PSN Account-ID over the user's settings.
	psPanelSaveSettings()
	closeChiakiPanel()
	ShowToast(c.label()+" is starting — JukaHub closes while it runs", ToastKindInfo)
	requestAppExit()
}

// psPanelConsole returns a copy of a console in the panel's list.
func psPanelConsole(host string) (psConsole, bool) {
	psPanelMu.Lock()
	defer psPanelMu.Unlock()
	for _, c := range chiakiPanel.consoles {
		if c.Host == host {
			return c, true
		}
	}
	return psConsole{}, false
}

// psPanelPS4Targets is the pairing order for a PS4: the reported firmware
// first, then the older generations. A zero target (manually added address)
// starts at the current firmware.
func psPanelPS4Targets(target int) []int {
	switch target {
	case psTargetPS4FW8:
		return []int{psTargetPS4FW8}
	case psTargetPS4FW9:
		return []int{psTargetPS4FW9, psTargetPS4FW8}
	case psTargetPS4FW10, 0:
		return []int{psTargetPS4FW10, psTargetPS4FW9, psTargetPS4FW8}
	default:
		return []int{target}
	}
}

// psPanelRows builds the row list. It reads the background-written fields under
// the panel lock and returns plain values, so drawing never touches the lock.
func chiakiRows() []chiakiRow {
	if chiakiPanel.page == psPageAccount {
		return psAccountRows()
	}
	if chiakiPanel.page == psPageConsoleOptions {
		// One console's options (psremote_console_opts.go).
		return psConsoleOptsRows()
	}
	psPanelMu.Lock()
	consoles := append([]psConsole{}, chiakiPanel.consoles...)
	busy := chiakiPanel.busy
	scanning := chiakiPanel.scanning
	scanErr := chiakiPanel.scanErr
	code := chiakiPanel.code
	account := chiakiPanel.account
	quality := chiakiPanel.quality
	volume := chiakiPanel.volume
	psPanelMu.Unlock()

	rows := make([]chiakiRow, 0, len(consoles)+8)
	for _, c := range consoles {
		row := chiakiRow{label: c.label(), host: c.Host}
		menu := psPanelLinkDeviceName(psConsoleKind(c))
		switch {
		case busy != "" && busy == "Pairing "+c.Host+"…":
			// The whole busy text, so 192.168.1.6 is not busy while
			// 192.168.1.60 pairs.
			row.detail = "Pairing — keep " + menu + " open on the console"
			if wait := psHelperPairingWait(); wait != "" {
				// A PS5 pairing waits for a chiaki-regist that went missing
				// (psremote_helpers_install.go).
				row.detail = wait
			}
			row.status = "…"
			row.action = psActionNone
		case c.paired():
			// Ready, in rest mode, being woken, or silent (psremote_wake.go).
			row.detail, row.status, row.action = psPanelPairedRow(c.Host, quality)
			if note, ok := psHelperConsoleNote(c); ok {
				// A held Play, or what A does while chiaki-stream is missing.
				row.detail = note
			}
		default:
			row.detail = hintSentence("Not paired — press %s, then type the PIN from "+menu, "A", "Select")
			row.status = "Pair"
			row.action = psActionPair
			if note, ok := psHelperConsoleNote(c); ok {
				// A PIN prompt waiting for the pairing helper.
				row.detail = note
			}
		}
		rows = append(rows, row)
	}
	// Installing the native helpers, or why that failed; after the consoles,
	// whose row numbers the tile press's cursor relies on.
	rows = append(rows, psHelperRows()...)
	if scanning {
		rows = append(rows, chiakiRow{label: "Scanning the network…", detail: "Listening for PS4/PS5 discovery replies.", status: "…", action: psActionNone})
	} else if len(consoles) == 0 {
		rows = append(rows, chiakiRow{
			label:  "No consoles yet",
			detail: "Turn the console on, enable Remote Play, then scan or add its address.",
			status: "Empty",
			action: psActionNone,
		})
	}
	if scanErr != "" {
		rows = append(rows, chiakiRow{label: "Scan problem", detail: scanErr, status: "Info", action: psActionNone})
	}
	rows = append(rows,
		chiakiRow{label: "Scan for consoles", detail: "Search the LAN for PS4 and PS5 consoles.", status: "Scan", action: psActionScan},
		chiakiRow{label: "Add console by address", detail: "For a console discovery cannot see; type 192.168.1.x.", status: "Add", action: psActionAddHost},
		chiakiRow{label: "PSN Account-ID", detail: psAccountRowDetail(account), status: accountStatus(account), action: psActionAccount},
	)
	// Which pads the stream gets, and the way to pair a Bluetooth one.
	rows = append(rows, psControllersRows()...)
	rows = append(rows,
		chiakiRow{label: "Quality", detail: "Lower it if the stream stutters.", status: psQualityPresets[clampQuality(quality)].Label, action: psActionQuality},
	)
	// Video codec, Select button and Rumble (psremote_console_opts.go).
	rows = append(rows, psStreamSettingRows()...)
	rows = append(rows,
		chiakiRow{label: "Volume", detail: "Left/Right adjusts. The console's own audio mix is unchanged.", status: itoa(volume) + "%", action: psActionVolume},
	)
	// How the last stream ended, when it did not end well (psremote_lastrun.go).
	rows = append(rows, psLastRunRows()...)
	if code != "" {
		rows = append(rows, chiakiRow{label: "Last error", detail: code, status: "Error", action: psActionNone})
	}
	rows = append(rows, chiakiRow{label: "Close", action: psActionClose})
	return rows
}

func accountStatus(account string) string {
	if strings.TrimSpace(account) == "" {
		return "Not set"
	}
	if len(account) <= 8 {
		return account
	}
	return account[:4] + "…" + account[len(account)-2:]
}

func clampQuality(q int) int {
	if q < 0 || q >= len(psQualityPresets) {
		return 0
	}
	return q
}

func chiakiPanelMove(delta int) {
	rows := chiakiRows()
	if len(rows) == 0 {
		return
	}
	chiakiPanel.cursor += delta
	if chiakiPanel.cursor < 0 {
		chiakiPanel.cursor = len(rows) - 1
	}
	if chiakiPanel.cursor >= len(rows) {
		chiakiPanel.cursor = 0
	}
	PlayNavSound()
}

// chiakiPanelAdjust handles Left/Right on the rows that have a value.
func chiakiPanelAdjust(delta int) bool {
	rows := chiakiRows()
	if chiakiPanel.cursor < 0 || chiakiPanel.cursor >= len(rows) {
		return false
	}
	switch rows[chiakiPanel.cursor].action {
	case psActionQuality:
		chiakiPanel.quality = (chiakiPanel.quality + delta + len(psQualityPresets)) % len(psQualityPresets)
		psPanelSaveSettings()
		PlayNavSound()
		return true
	case psActionVolume:
		chiakiPanel.volume += delta * 5
		if chiakiPanel.volume < 0 {
			chiakiPanel.volume = 0
		}
		if chiakiPanel.volume > 100 {
			chiakiPanel.volume = 100
		}
		psPanelSaveSettings()
		PlayNavSound()
		return true
	case psActionCodec, psActionSelectButton, psActionRumble:
		return psPanelCycleStreamSetting(rows[chiakiPanel.cursor].action, delta, false)
	}
	return false
}

func chiakiPanelActivate() {
	rows := chiakiRows()
	if chiakiPanel.cursor < 0 || chiakiPanel.cursor >= len(rows) {
		return
	}
	chosen := rows[chiakiPanel.cursor]
	switch chosen.action {
	case psActionPlay:
		if console, ok := psPanelConsole(chosen.host); ok {
			// A console that just answered ready streams at once; a resting
			// one is woken first and streams once it is up (psremote_wake.go).
			psPanelPlayOrWake(console)
		}
		return
	case psActionControllers:
		psPanelOpenControllers()
		return
	case psActionPair:
		psPanelBeginPair(chosen.host)
	case psActionScan:
		PlayActivateSound()
		psPanelScan()
	case psActionAddHost:
		psPanelOpenKeyboard(psEditHost)
	case psActionAccount:
		// The Account-ID's own row is not a step of a pairing, even if an
		// earlier pairing was abandoned on its card.
		psPanelOpenAccountPage(false)
	case psActionSignInPhone:
		psPanelShowSignInQR()
	case psActionSignInBrowser:
		psPanelSignInBrowser()
	case psActionTypeAccount:
		psPanelOpenKeyboard(psEditAccount)
	case psActionAccountHelp:
		PlayActivateSound()
		ShowToast(psAccountHelpText, ToastKindInfo)
	case psActionAccountBack:
		psPanelBack()
	case psActionQuality:
		chiakiPanel.quality = (chiakiPanel.quality + 1) % len(psQualityPresets)
		psPanelSaveSettings()
		PlayActivateSound()
		ShowToast("Stream quality set to "+psQualityPresets[chiakiPanel.quality].Label, ToastKindInfo)
	case psActionVolume:
		chiakiPanelAdjust(1)
	case psActionClose:
		closeChiakiPanel()
	case psActionInstallHelpers:
		psPanelInstallHelpers()
	default:
		// The stream rows and the console options page
		// (psremote_console_opts.go).
		psPanelActivateExtra(chosen)
	}
}

// psPanelBeginPair asks for what the pairing needs. A console that pairs with
// the PSN Account-ID and has none yet gets the Account-ID card first, because
// the console rejects the PIN otherwise and the error would look like a wrong
// PIN. With the Account-ID saved it goes straight on to the PIN.
func psPanelBeginPair(host string) {
	if host == "" {
		return
	}
	// A pairing abandoned on its Account-ID card must not carry over into
	// this one.
	psPanelPairAfterAccount = false
	console, _ := psPanelConsole(host)
	chiakiPanel.pairHost = host
	chiakiPanel.pairTarget = console.Target
	if chiakiPanel.pairTarget == 0 {
		chiakiPanel.pairTarget = psTargetPS4FW10
	}
	isPS5 := console.PS5 || chiakiPanel.pairTarget >= psTargetPS5
	if isPS5 && IsOnline() {
		// The handshake needs chiaki-regist: a missing one installs while
		// the Account-ID is being found (a no-op when it is there or
		// already installing). Offline, the PIN prompt says what is needed.
		psHelpersStart(psStartPress)
	}
	if psTargetNeedsAccount(isPS5, chiakiPanel.pairTarget) && strings.TrimSpace(chiakiPanel.account) == "" {
		psPanelOpenAccountPage(true)
		return
	}
	psPanelPromptPIN("")
}

// psTargetNeedsAccount reports whether pairing needs the PSN Account-ID first.
// Every PS5 does, and so does every PS4 from firmware 7.0 on: those register
// with the account's id (chiaki's "Np-AccountId"), and the console refuses an
// id that is not one of its accounts. Only a PS4 that discovery reported on
// older firmware (target 800) pairs without one. An address typed by hand is
// assumed to be current hardware.
func psTargetNeedsAccount(ps5 bool, target int) bool {
	return ps5 || target != psTargetPS4FW8
}

// psConsoleKind is 5 for a PS5, 4 for a console discovery reported as a PS4,
// and 0 for an address nothing has identified yet.
func psConsoleKind(c psConsole) int {
	switch {
	case c.PS5 || c.Target >= psTargetPS5:
		return 5
	case c.Target > 0:
		return 4
	}
	return 0
}

// psPanelPairKind is psConsoleKind for the console a pairing is for. A typed
// address that no scan has answered for is not in the list: kind 0.
func psPanelPairKind(host string) int {
	if c, ok := psPanelConsole(host); ok {
		return psConsoleKind(c)
	}
	return 0
}

// psPanelLinkDeviceName is the console menu that shows the pairing PIN.
func psPanelLinkDeviceName(kind int) string {
	switch kind {
	case 5:
		return "Link Device"
	case 4:
		return "Add Device"
	}
	return "Link Device (PS4: Add Device)"
}

// psPanelLinkDeviceHint is where the console shows its pairing PIN, in the
// console's own menu words, so the next thing the user does is on the console.
func psPanelLinkDeviceHint(kind int) string {
	switch kind {
	case 5:
		return "On the PS5: Settings → System → Remote Play → Link Device — type the 8 numbers shown there"
	case 4:
		return "On the PS4: Settings → Remote Play Connection Settings → Add Device — type the 8 numbers shown there"
	}
	return "On a PS5: Settings → System → Remote Play → Link Device; on a PS4: Settings → Remote Play " +
		"Connection Settings → Add Device — type the 8 numbers shown there"
}

// psPanelPromptPIN opens the PIN keyboard with the console's menu path in a
// toast. prefix leads the toast ("Account-ID saved. "). A PS5 whose pairing
// helper is still to install is asked once it is in: the PIN its Link Device
// shows lasts five minutes. So is an address typed by hand while that install
// can come now, since it may be a PS5 (psremote_helpers_install.go).
func psPanelPromptPIN(prefix string) {
	kind := psPanelPairKind(chiakiPanel.pairHost)
	ps5 := kind == 5 || chiakiPanel.pairTarget >= psTargetPS5
	if (ps5 || kind == 0) && psHelpersHoldPIN(chiakiPanel.pairHost, prefix, ps5) {
		return
	}
	ShowToast(prefix+psPanelLinkDeviceHint(kind), ToastKindInfo)
	psPanelOpenKeyboard(psEditPIN)
}

// psAccountRowDetail is the console list's line about the Account-ID.
func psAccountRowDetail(account string) string {
	if strings.TrimSpace(account) == "" {
		return "Needed once to pair · sign in with PlayStation to get it"
	}
	return "Saved · used when you pair a PS5 or PS4"
}

// ── The Account-ID card ─────────────────────────────────────────────────

// psPanelShowPage switches the panel's view. The console list gets its cursor
// back when the card closes.
func psPanelShowPage(page psPanelPage) {
	if chiakiPanel.page == page {
		return
	}
	if page == psPageConsoles {
		chiakiPanel.cursor = chiakiPanel.listCursor
	} else {
		chiakiPanel.listCursor = chiakiPanel.cursor
		chiakiPanel.cursor = 0
	}
	chiakiPanel.page = page
	chiakiPanel.scroll = 0
	chiakiRowHits = nil
	psSignInQRRect = sdl.Rect{}
}

// psPanelOpenAccountPage shows the Account-ID card. pairing says a console
// press led here, so a saved Account-ID goes straight on to that console's
// PIN. The phone page lives on the Phone Companion server, which is started
// here if it is not running yet (the same start the pairing card uses).
func psPanelOpenAccountPage(pairing bool) {
	psPanelPairAfterAccount = pairing
	companionStart()
	psSignInQRRefresh()
	psPanelShowPage(psPageAccount)
	PlayActivateSound()
}

// psPanelBack is B on the panel: the Account-ID card goes back to the console
// list (a pairing waiting on it stops there), the list closes the panel.
func psPanelBack() {
	if chiakiPanel.page != psPageConsoles {
		psPanelPairAfterAccount = false
		psPanelShowPage(psPageConsoles)
		PlayBackSound()
		return
	}
	// B first lets go of a Play or a PIN prompt waiting for the helpers'
	// install, then stops a wake that is still waiting; the next B closes.
	if psHelpersCancelHeld(true) {
		return
	}
	if psWakeCancelRunning() {
		return
	}
	closeChiakiPanel()
}

// psSignInBrowserAvailable reports whether this device has a web browser to
// hand the sign-in to: a PC does, a TrimUI handheld has none that can run
// Sony's login page, so it signs in on the phone.
func psSignInBrowserAvailable() bool {
	return !IsTrimuiDevice()
}

// psSignInOfflineText says why the card has no phone code: the Phone
// Companion's own reason (safe mode, no Wi-Fi), or the usual one.
func psSignInOfflineText() string {
	if companion.err != "" {
		return companion.err
	}
	return "Connect this device to Wi-Fi to sign in with your phone"
}

// psAccountRows is the Account-ID card: the PC's browser sign-in where there
// is one, the phone sign-in (its code is drawn above the rows), typing or
// pasting, and the one line that says what the thing is.
func psAccountRows() []chiakiRow {
	busy, lastErr := psnSignInState()
	rows := make([]chiakiRow, 0, 6)
	if psSignInBrowserAvailable() {
		rows = append(rows, chiakiRow{
			label:  "Sign in in your web browser",
			detail: "Then paste the address of the page that says \"redirect\"",
			status: "Open",
			action: psActionSignInBrowser,
		})
	}
	phone := chiakiRow{label: "Sign in with your phone", status: "QR", action: psActionSignInPhone}
	switch {
	case busy:
		phone.detail = "Checking your sign-in with Sony…"
		phone.status = "…"
	case len(psSignInQR.bitmap) == 0:
		phone.detail = psSignInOfflineText()
		phone.status = "Offline"
	default:
		phone.detail = hintSentence("Scan the code above with your phone · %s shows it bigger", "A", "Enlarge")
	}
	rows = append(rows, phone,
		chiakiRow{label: "Type or paste it", detail: "The 12-character Account-ID, or the \"redirect\" page address", status: "Type", action: psActionTypeAccount},
		chiakiRow{label: "Where do I find it?", detail: "Not your sign-in name or the PIN — Sony gives it out after a sign-in", status: "Info", action: psActionAccountHelp},
	)
	if lastErr != "" && !busy {
		rows = append(rows, chiakiRow{label: "Last sign-in", detail: lastErr, status: "Error", action: psActionNone})
	}
	return append(rows, chiakiRow{label: "Back", action: psActionAccountBack})
}

// psSignInQRRefresh keeps the card's code on the Phone Companion's current
// link: a new PIN or a DHCP move changes the link and with it the code. Per
// frame it is one string compare; the code is rebuilt only on a change.
func psSignInQRRefresh() {
	link := ""
	if companion.srv != nil && companion.err == "" {
		link = psnPageURLFrom(companion.url)
	}
	if link == psSignInQR.url {
		return
	}
	psSignInQR = psSignInCode{url: link}
	if link == "" {
		return
	}
	code, err := qrcode.New(link, qrcode.Low)
	if err != nil {
		logWarn("psremote", "sign-in QR code: %v", err)
		return
	}
	psSignInQR.bitmap = code.Bitmap()
	psSignInQR.core = qrCoreModules(psSignInQR.bitmap)
}

// psPanelShowSignInQR is A on "Sign in with your phone": the code full screen,
// at the size and brightness a phone camera reads from arm's length — or, with
// no code yet, another try at starting the Phone Companion.
func psPanelShowSignInQR() {
	if len(psSignInQR.bitmap) == 0 {
		companionStart()
		psSignInQRRefresh()
		if len(psSignInQR.bitmap) == 0 {
			ShowToast(psSignInOfflineText(), ToastKindWarning)
			return
		}
	}
	qrZoomOpenCardDetail(psSignInQR.bitmap, psSignInQRTitle, psnPageCaption(psSignInQR.url))
}

// psPanelSignInBrowser is the PC's sign-in: Sony's page opens in the system
// browser, and the Account-ID field opens here for the "redirect" address the
// browser ends on. Pasting it there trades it for the Account-ID.
func psPanelSignInBrowser() {
	if err := openExternalURL(psnLoginURL); err != nil {
		ShowToast("Could not open the web browser — "+firstLine(err.Error()), ToastKindError)
		return
	}
	PlayActivateSound()
	ShowToast("Sign in, then copy the address of the page that says \"redirect\" and paste it here", ToastKindInfo)
	psPanelOpenKeyboardWith(psEditAccount, "")
}

// psPanelExchangeCode trades a pasted sign-in code for the Account-ID off the
// main thread. The answer comes back through the Phone Companion queue, the
// same way the phone's sign-in does, so one function applies both.
func psPanelExchangeCode(code string) {
	if !psnBeginSignIn() {
		ShowToast("A sign-in is already being checked — wait a moment", ToastKindInfo)
		return
	}
	ShowToast("Checking your sign-in with Sony…", ToastKindInfo)
	go func() {
		account, err := psnExchangeCode(context.Background(), code)
		psnEndSignIn(err)
		if err != nil {
			logWarn("psremote", "PSN sign-in failed: %v", err)
			ShowToast(err.Error(), ToastKindError)
			return
		}
		enqueueCompanion(companionEvent{Kind: companionEventPSN, Text: account})
	}()
}

// psPanelAccountSaved finishes a new Account-ID on the card: back to the
// console list, and on to the PIN when a pairing was waiting for it.
func psPanelAccountSaved() {
	psnForgetLastError()
	pairing := psPanelPairAfterAccount && chiakiPanel.pairHost != ""
	psPanelPairAfterAccount = false
	psPanelShowPage(psPageConsoles)
	if pairing {
		psPanelPromptPIN("Account-ID saved. ")
		return
	}
	ShowToast("PSN Account-ID saved", ToastKindSuccess)
}

// psPanelAccountArrived applies, on the main loop (drainCompanionQueue), an
// Account-ID that a sign-in produced elsewhere: on the phone page, or from a
// "redirect" address pasted here. It is saved whether or not the panel is
// open, and a pairing waiting on the Account-ID card goes straight on to the
// PIN.
func psPanelAccountArrived(account string) {
	account = strings.TrimSpace(account)
	if _, offline, err := ps4DecodeAccountID(account); err != nil || offline {
		return
	}
	if !chiakiPanel.open {
		// Held from the load to the save, as in psPanelSaveSettings.
		psHostsMu.Lock()
		state := loadPSHosts()
		state.Settings.Account = account
		err := savePSHostsLocked(state)
		psHostsMu.Unlock()
		if err != nil {
			logWarn("psremote", "saving the Account-ID failed: %v", err)
			ShowToast("Could not save the PSN Account-ID", ToastKindError)
			return
		}
		psnForgetLastError()
		ShowToast("PSN Account-ID saved — pair the console in PS Remote Play", ToastKindSuccess)
		return
	}
	// The phone answered while the keyboard was still open on the Account-ID
	// field: that field has nothing left to ask.
	if virtualKeyboardActive && vkTarget == vkTargetPSRemote && psPanelEditField == psEditAccount {
		vkCloseKeyboard()
		psPanelEditField = psEditNone
	}
	// Nor is the enlarged sign-in code: the phone is done with it, and the PIN
	// prompt that may follow would open behind it, at full backlight.
	if qrZoomActive() && qrZoom.title == psSignInQRTitle {
		qrZoomCloseCard()
	}
	chiakiPanel.account = account
	psPanelSaveSettings()
	if chiakiPanel.page != psPageAccount {
		// Nothing is waiting on the card (the user went back to the list, or
		// is typing the PIN already): the new Account-ID is simply kept.
		psnForgetLastError()
		ShowToast("PSN Account-ID saved", ToastKindSuccess)
		return
	}
	psPanelAccountSaved()
}

// psDrawSignInBand draws the phone code and the steps beside it. The code is
// drawn the way the Home header's is: the bare symbol inside a two-module
// quiet zone, in whole-pixel modules, as one FillRects call.
func psDrawSignInBand(renderer *sdl.Renderer, config *Config, font, medium *ttf.Font, band sdl.Rect) {
	side := band.H
	core := psSignInQR.core
	n := int32(len(core))
	module := headerQRModule(side, n)
	if module < 1 || font == nil || medium == nil {
		return
	}
	tile := sdl.Rect{X: band.X, Y: band.Y, W: side, H: side}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, tile.X, tile.Y, tile.W, tile.H, max32(6, side/16), sdl.Color{R: 255, G: 255, B: 255, A: 255})
	code := module * n
	origin := sdl.Point{X: tile.X + (side-code)/2, Y: tile.Y + (side-code)/2}
	if psSignInQR.rects == nil || psSignInQR.origin != origin || psSignInQR.module != module {
		psSignInQR.rects = qrModuleRects(core, origin.X, origin.Y, module)
		psSignInQR.origin, psSignInQR.module = origin, module
	}
	if len(psSignInQR.rects) > 0 {
		_ = renderer.SetDrawColor(0, 0, 0, 255)
		_ = renderer.FillRects(psSignInQR.rects)
	}
	psSignInQRRect = tile

	x := tile.X + side + 20
	maxW := band.X + band.W - x
	if maxW <= 0 {
		return
	}
	bottom := tile.Y + side
	y := tile.Y + 4
	medH := int32(medium.Height())
	if y+medH <= bottom {
		renderText(renderer, config, medium, ellipsize(medium, "Sign in with your phone", maxW), ColorTextPrimary(), x, y)
		y += medH + 8
	}
	lineH := int32(font.Height()) + 6
	for _, step := range psSignInSteps {
		if y+lineH > bottom {
			return
		}
		renderText(renderer, config, font, ellipsize(font, step, maxW), ColorTextSecondary(), x, y)
		y += lineH
	}
	if y+lineH <= bottom {
		renderText(renderer, config, font, ellipsize(font, "Your Account-ID then arrives here by itself.", maxW), ColorTextTertiary(), x, y+4)
	}
}

// psPanelForget drops a console and its credentials.
func psPanelForget() {
	rows := chiakiRows()
	if chiakiPanel.cursor < 0 || chiakiPanel.cursor >= len(rows) {
		return
	}
	host := rows[chiakiPanel.cursor].host
	if host == "" {
		return
	}
	removePSConsole(host)
	psPanelReload()
	ShowToast("Forgot "+host, ToastKindInfo)
}

// vkPSRemoteBuffer is the keyboard's buffer for the PS Remote fields.
var vkPSRemoteBuffer string

// vkSeedText fills the PS Remote keyboard buffer before it opens, so an address
// or an Account-ID can be corrected instead of retyped.
func vkSeedText(text string) { vkPSRemoteBuffer = text }

// psPanelEditSeed is what the field held when the keyboard opened for it. An
// empty Account-ID clears the saved one only from a field that showed it: the
// browser sign-in opens the field empty for the paste, and Enter there before
// pasting wiped an Account-ID saved earlier.
var psPanelEditSeed string

// vkOpenPSRemote opens the on-screen keyboard aimed at the PS Remote panel.
func vkOpenPSRemote() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetPSRemote
	vkLayer = vkLayerLetters
	if psPanelEditField == psEditPIN || psPanelEditField == psEditLoginPIN {
		// A PIN is digits only: the layer without letters under the number
		// row, the way the Memory panel opens for its values.
		vkLayer = vkLayerSymbols
	}
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// psPanelOpenKeyboard seeds and opens the on-screen keyboard for one field.
func psPanelOpenKeyboard(edit psPanelEdit) {
	switch edit {
	case psEditHost:
		psPanelOpenKeyboardWith(edit, chiakiPanel.host)
	case psEditAccount:
		psPanelOpenKeyboardWith(edit, chiakiPanel.account)
	case psEditPIN:
		psPanelOpenKeyboardWith(edit, "")
	}
}

// psPanelOpenKeyboardWith opens the keyboard for one field holding seed. A
// submission the panel cannot use (an address with a space in it, a PIN that is
// not 8 digits, an Account-ID that is not base64) comes back through here with
// the typed text still in the field, so a slip is a correction rather than a
// retype — and it is caught before the handshake spends its timeouts on it.
func psPanelOpenKeyboardWith(edit psPanelEdit, seed string) {
	if edit == psEditNone {
		return
	}
	psPanelEditField = edit
	psPanelEditSeed = seed
	vkSeedText(seed)
	vkOpenPSRemote()
}

// psPanelApplyEdit commits a keyboard submission.
func psPanelApplyEdit(text string) {
	edit := psPanelEditField
	psPanelEditField = psEditNone
	text = strings.TrimSpace(text)
	switch edit {
	case psEditHost:
		if text == "" {
			return
		}
		if !psPanelValidHost(text) {
			ShowToast("Type the console's address, like 192.168.1.51", ToastKindWarning)
			psPanelOpenKeyboardWith(psEditHost, text)
			return
		}
		chiakiPanel.host = text
		// A typed address is paired straight away; the panel asks for the PIN.
		psPanelBeginPair(text)
	case psEditAccount:
		if text == "" {
			if psPanelPairAfterAccount || strings.TrimSpace(psPanelEditSeed) == "" {
				// An empty field in the middle of a pairing is a way back to
				// the card's other choices, not a cleared setting: the console
				// refuses the PIN without the Account-ID. A field that opened
				// empty (the browser sign-in's paste) had nothing to clear.
				return
			}
			chiakiPanel.account = ""
			psPanelSaveSettings()
			psPanelShowPage(psPageConsoles)
			ShowToast("Account-ID cleared", ToastKindInfo)
			return
		}
		// The field takes the Account-ID itself, the number other tools show,
		// or the address of the "redirect" page a browser sign-in ends on.
		in, err := psnParseAccountInput(text)
		if err != nil {
			ShowToast(err.Error(), ToastKindWarning)
			psPanelOpenKeyboardWith(psEditAccount, text)
			return
		}
		if in.Code != "" {
			// Sony turns the code into the Account-ID; the answer comes back
			// through psPanelAccountArrived. The one-time code does not stay
			// behind in the keyboard's buffer.
			vkSeedText("")
			psPanelExchangeCode(in.Code)
			return
		}
		chiakiPanel.account = in.AccountID
		psPanelSaveSettings()
		psPanelAccountSaved()
	case psEditPIN:
		pin := psPanelNormalizePIN(text)
		if pin == "" {
			return
		}
		// A PS5 hands the PIN to the native helper unchecked, so a typo used to
		// surface only after the 25 s handshake timed out.
		if _, err := ps4ParsePin(pin); err != nil {
			ShowToast("The PIN is the 8 numbers "+psPanelLinkDeviceName(psPanelPairKind(chiakiPanel.pairHost))+" shows", ToastKindWarning)
			psPanelOpenKeyboardWith(psEditPIN, text)
			return
		}
		psPanelPair(chiakiPanel.pairHost, pin, chiakiPanel.pairTarget)
	default:
		// The internet address and the login passcode of the console options
		// page (psremote_console_opts.go).
		psPanelApplyOptsEdit(edit, text)
	}
}

// psPanelNormalizePIN drops the spaces and dashes a person types when copying
// the PIN the way the console groups it ("1234 5678").
func psPanelNormalizePIN(text string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, strings.TrimSpace(text))
}

// psPanelValidHost accepts what a console address can be: an IP address or a
// plain host name. A space, a slash or a pasted URL is caught here instead of
// as a pairing failure three handshake timeouts later.
func psPanelValidHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if strings.HasPrefix(host, ".") || strings.HasPrefix(host, "-") {
		return false
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

func ctrlChiakiPanel(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		chiakiPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		chiakiPanelMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		chiakiPanelAdjust(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		chiakiPanelAdjust(1)
	case sdl.CONTROLLER_BUTTON_A:
		chiakiPanelActivate()
	case sdl.CONTROLLER_BUTTON_X:
		// Wake the focused console without streaming (psremote_wake.go).
		psPanelWakeFocused()
	case sdl.CONTROLLER_BUTTON_Y:
		psPanelForget()
	case sdl.CONTROLLER_BUTTON_START:
		// The focused console's options (psremote_console_opts.go).
		psPanelOpenConsoleOptions()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		psPanelBack()
	}
	return true
}

func handleChiakiKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !chiakiPanel.open {
		return false
	}
	// Space on the Add, Account-ID or Pair rows opens the keyboard: its space
	// must not be typed into the field it opened (vkopenertext.go).
	wasOpen := virtualKeyboardActive
	defer vkDropOpenerText(e, wasOpen)
	switch e.Keysym.Sym {
	case sdl.K_UP:
		chiakiPanelMove(-1)
	case sdl.K_DOWN:
		chiakiPanelMove(1)
	case sdl.K_LEFT:
		chiakiPanelAdjust(-1)
	case sdl.K_RIGHT:
		chiakiPanelAdjust(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		chiakiPanelActivate()
	case sdl.K_x:
		// The pad's X: wake the focused console without streaming.
		psPanelWakeFocused()
	case sdl.K_o:
		// The pad's Start: the focused console's options.
		psPanelOpenConsoleOptions()
	case sdl.K_y, sdl.K_DELETE:
		// The footer says "Y Forget", and in the keyboard vocabulary Y is the
		// letter key: it was Delete only, so the hint named a key that did
		// nothing.
		psPanelForget()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		psPanelBack()
	}
	return true
}

func handleChiakiMouse(mx, my int32) bool {
	if !chiakiPanel.open {
		return false
	}
	if chiakiPanel.page == psPageAccount && psSignInQRRect.W > 0 && pointInRect(mx, my, psSignInQRRect) {
		psPanelShowSignInQR()
		return true
	}
	for _, hit := range chiakiRowHits {
		if pointInRect(mx, my, hit.rect) {
			chiakiPanel.cursor = hit.index
			chiakiPanelActivate()
			return true
		}
	}
	if chiakiPanelRect.W > 0 && pointInRect(mx, my, chiakiPanelRect) {
		return true
	}
	closeChiakiPanel()
	return true
}

// renderChiakiPanel draws the client panel. It follows the same visual language
// as the Ports and Tools panels so it does not look bolted on.
func renderChiakiPanel(renderer *sdl.Renderer, config *Config) {
	if !chiakiPanel.open || renderer == nil {
		return
	}
	onAccount := chiakiPanel.page == psPageAccount
	if onAccount {
		// The code follows the Phone Companion's link (a new PIN, a DHCP move).
		psSignInQRRefresh()
	}
	rows := chiakiRows()
	if chiakiPanel.cursor >= len(rows) {
		chiakiPanel.cursor = len(rows) - 1
	}
	if chiakiPanel.cursor < 0 {
		chiakiPanel.cursor = 0
	}

	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	const padX = OverlayPadX
	// The focused row's stroke and the detail line's descenders both need room;
	// a tighter row clipped "Ready to stream" at the bottom edge.
	rowH := 10 + medH + 4 + smallH + 10

	// The title band is the shared page header, built before the card is sized so
	// the panel and the bar that fills it measure the same height.
	header := UIPanelHeader("PS Remote Play", psPanelSubtitle(), "gamepad")
	if onAccount {
		header = UIPanelHeader("PSN Account-ID", psAccountSubtitle(chiakiPanel.account), "gamepad")
	}
	if chiakiPanel.page == psPageConsoleOptions {
		header = psConsoleOptsHeader()
	}
	headerH := UIPanelHeaderBand(config, header, 14)
	footerH := 20 + smallH

	panelW := int32(820)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	// The Account-ID card puts the phone code above its rows, as large as the
	// screen allows while three rows still fit under it.
	var qrSide, bandH int32
	if onAccount && len(psSignInQR.core) > 0 {
		qrSide = min32(200, screenHeight-40-headerH-footerH-3*rowH-16)
		if qrSide >= 96 {
			bandH = qrSide + 16
		} else {
			qrSide = 0
		}
	}
	maxRows := (screenHeight - 40 - headerH - bandH - footerH) / rowH
	if maxRows < 3 {
		maxRows = 3
	}
	visible := int32(len(rows))
	if visible > maxRows {
		visible = maxRows
	}
	panelH := headerH + bandH + visible*rowH + footerH
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	chiakiPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	if chiakiPanel.cursor < chiakiPanel.scroll {
		chiakiPanel.scroll = chiakiPanel.cursor
	}
	if chiakiPanel.cursor >= chiakiPanel.scroll+int(visible) {
		chiakiPanel.scroll = chiakiPanel.cursor - int(visible) + 1
	}
	if chiakiPanel.scroll > len(rows)-int(visible) {
		chiakiPanel.scroll = len(rows) - int(visible)
	}
	if chiakiPanel.scroll < 0 {
		chiakiPanel.scroll = 0
	}

	drawOverlayScrim(renderer)

	drawOverlayCard(renderer, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)

	header = overlayHeaderOnCard(header, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	UIPageHeaderDraw(renderer, config, header)

	psSignInQRRect = sdl.Rect{}
	if bandH > 0 {
		psDrawSignInBand(renderer, config, font, medium, sdl.Rect{X: panelX + padX, Y: panelY + headerH, W: panelW - 2*padX, H: qrSide})
	}

	focusColor := installHighlight()
	rowW := panelW - 2*padX
	chiakiRowHits = chiakiRowHits[:0]
	for i := int32(0); i < visible; i++ {
		idx := chiakiPanel.scroll + int(i)
		row := rows[idx]
		y := panelY + headerH + bandH + i*rowH
		r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
		chiakiRowHits = append(chiakiRowHits, struct {
			index int
			rect  sdl.Rect
		}{index: idx, rect: r})

		focused := idx == chiakiPanel.cursor
		rowBg := WithAlpha(ColorSurfaceRow, 190)
		if focused {
			rowBg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
		if focused {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
		}

		renderText(renderer, config, medium, row.label, ColorTextPrimary(), r.X+16, r.Y+8)
		if row.detail != "" {
			// A sign-in error can be a long sentence; the row never grows.
			renderText(renderer, config, font, ellipsize(font, row.detail, r.W-32), ColorTextTertiary(), r.X+16, r.Y+8+medH+4)
		}
		if row.status != "" {
			statusColor := ColorTextSecondary()
			switch row.status {
			case "Play", "Pair", "Scan", "Add", "QR", "Open", "Type", "Wake", "Install", "Retry":
				statusColor = focusColor
			case "Error":
				statusColor = DangerInk()
			}
			w, _, _ := font.SizeUTF8(row.status)
			renderText(renderer, config, font, row.status, statusColor, r.X+r.W-int32(w)-16, r.Y+8+medH-smallH)
		}
	}

	// The footer follows the focused row: a console offers Play, Wake and
	// Forget, and B says Stop while a wake is waiting (psremote_wake.go).
	hint := psPanelConsoleHints(rows, chiakiPanel.cursor)
	if onAccount {
		hint = hintLine(FooterHint{"A", "Select"}, FooterHint{"B", "Back"})
	}
	if chiakiPanel.page == psPageConsoleOptions {
		hint = psConsoleOptsFooter()
	}
	renderText(renderer, config, font, hint, ColorTextTertiary(), panelX+padX, panelY+panelH-footerH+6)
}

// psAccountSubtitle is the Account-ID card's subtitle: what it is for, or the
// one already saved.
func psAccountSubtitle(account string) string {
	if strings.TrimSpace(account) == "" {
		return "Needed once to pair a PS5 or PS4 · JukaHub keeps only this number"
	}
	return "Saved: " + accountStatus(account) + " · sign in again to replace it"
}

// psPanelSubtitle is the one line that tells the truth about the client's
// readiness: where it cannot stream, or the helpers' install while one is
// missing (psremote_helpers_install.go).
func psPanelSubtitle() string {
	if !psRemoteSupported() {
		return "Streaming runs on the TrimUI handhelds"
	}
	if line := psHelperSubtitle(); line != "" {
		return line
	}
	return "JukaHub client · PS4 + PS5 · native libchiaki"
}
