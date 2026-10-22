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

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// psPanelEdit names the field the on-screen keyboard is editing.
type psPanelEdit int

const (
	psEditNone psPanelEdit = iota
	psEditHost
	psEditPIN
	psEditAccount
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
	psPanelEditField = psEditNone
	psPanelPairAfterAccount = false
	state := loadPSHosts()
	chiakiPanel.host = ""
	chiakiPanel.account = state.Settings.Account
	chiakiPanel.quality = state.Settings.Quality
	chiakiPanel.volume = state.Settings.Volume
	chiakiPanel.consoles = state.Hosts
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
}

func closeChiakiPanel() {
	if !chiakiPanel.open {
		return
	}
	chiakiPanel = chiakiPanelState{}
	chiakiPanelRect = sdl.Rect{}
	chiakiRowHits = nil
	psPanelEditField = psEditNone
	psPanelPairAfterAccount = false
	PlayBackSound()
}

// psPanelSaveSettings persists quality, volume and the PSN Account-ID.
func psPanelSaveSettings() {
	state := loadPSHosts()
	state.Settings.Quality = chiakiPanel.quality
	state.Settings.Volume = chiakiPanel.volume
	state.Settings.Account = chiakiPanel.account
	if err := savePSHosts(state); err != nil {
		log.Printf("[psremote] saving settings failed: %v", err)
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
	if chiakiPanel.busy != "" {
		psPanelMu.Unlock()
		return
	}
	chiakiPanel.busy = "Pairing " + host + "…"
	chiakiPanel.code = ""
	psPanelMu.Unlock()

	isPS5 := target >= psTargetPS5
	account := chiakiPanel.account
	go func() {
		var console psConsole
		var err error
		if isPS5 {
			console, err = ps5Register(psRemoteEngine(), host, pin, account, 25*time.Second)
		} else {
			// Try the generation discovery reported first, then the older ones:
			// a manually added address has no discovery reply to learn from.
			for _, candidate := range psPanelPS4Targets(target) {
				console, err = ps4Register(host, pin, account, candidate, 15*time.Second)
				if err == nil {
					break
				}
			}
			// A typed address may be a PS5; with an Account-ID saved, try the
			// PS5 handshake before giving up.
			if err != nil && strings.TrimSpace(account) != "" {
				console, err = ps5Register(psRemoteEngine(), host, pin, account, 25*time.Second)
			}
		}
		psPanelMu.Lock()
		chiakiPanel.busy = ""
		if err != nil {
			chiakiPanel.code = firstLine(err.Error())
			psPanelMu.Unlock()
			ShowToast("Pairing failed — "+firstLine(err.Error()), ToastKindError)
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
	psPanelSaveSettings()
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
		switch {
		case busy != "" && strings.Contains(busy, c.Host):
			row.detail = "Pairing — keep Link Device open"
			row.status = "…"
			row.action = psActionNone
		case c.paired():
			row.detail = "Ready to stream · " + psQualityPresets[clampQuality(quality)].Label
			row.status = "Play"
			row.action = psActionPlay
		default:
			row.detail = "Not paired — press A, then type the PIN shown on the console"
			row.status = "Pair"
			row.action = psActionPair
		}
		rows = append(rows, row)
	}
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
		chiakiRow{label: "PSN Account-ID", detail: "Needed once for a PS5, from Link Device on the console.", status: accountStatus(account), action: psActionAccount},
		chiakiRow{label: "Quality", detail: "Lower it if the stream stutters.", status: psQualityPresets[clampQuality(quality)].Label, action: psActionQuality},
		chiakiRow{label: "Volume", detail: "Left/Right adjusts. The console's own audio mix is unchanged.", status: itoa(volume) + "%", action: psActionVolume},
	)
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
			closeChiakiPanel()
			psPanelPlay(console)
		}
		return
	case psActionPair:
		psPanelBeginPair(chosen.host)
	case psActionScan:
		PlayActivateSound()
		psPanelScan()
	case psActionAddHost:
		psPanelOpenKeyboard(psEditHost)
	case psActionAccount:
		psPanelOpenKeyboard(psEditAccount)
	case psActionQuality:
		chiakiPanel.quality = (chiakiPanel.quality + 1) % len(psQualityPresets)
		psPanelSaveSettings()
		PlayActivateSound()
		ShowToast("Stream quality set to "+psQualityPresets[chiakiPanel.quality].Label, ToastKindInfo)
	case psActionVolume:
		chiakiPanelAdjust(1)
	case psActionClose:
		closeChiakiPanel()
	}
}

// psPanelBeginPair asks for what the pairing needs. A PS5 without an
// Account-ID gets that field first, because the console rejects the PIN
// otherwise and the error would look like a wrong PIN.
func psPanelBeginPair(host string) {
	if host == "" {
		return
	}
	console, _ := psPanelConsole(host)
	chiakiPanel.pairHost = host
	chiakiPanel.pairTarget = console.Target
	if chiakiPanel.pairTarget == 0 {
		chiakiPanel.pairTarget = psTargetPS4FW10
	}
	isPS5 := console.PS5 || chiakiPanel.pairTarget >= psTargetPS5
	if isPS5 && strings.TrimSpace(chiakiPanel.account) == "" {
		ShowToast("PS5 pairing needs the PSN Account-ID from Link Device", ToastKindInfo)
		psPanelPairAfterAccount = true
		psPanelOpenKeyboard(psEditAccount)
		return
	}
	psPanelOpenKeyboard(psEditPIN)
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

// vkOpenPSRemote opens the on-screen keyboard aimed at the PS Remote panel.
func vkOpenPSRemote() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetPSRemote
	vkLayer = vkLayerLetters
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
		psPanelEditField = edit
		vkSeedText(chiakiPanel.host)
	case psEditAccount:
		psPanelEditField = edit
		vkSeedText(chiakiPanel.account)
	case psEditPIN:
		psPanelEditField = edit
		vkSeedText("")
	}
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
		chiakiPanel.host = text
		// A typed address is paired straight away; the panel asks for the PIN.
		psPanelBeginPair(text)
	case psEditAccount:
		chiakiPanel.account = text
		psPanelSaveSettings()
		if psPanelPairAfterAccount {
			psPanelPairAfterAccount = false
			psPanelOpenKeyboard(psEditPIN)
			return
		}
		ShowToast("Account-ID saved", ToastKindSuccess)
	case psEditPIN:
		if text == "" {
			return
		}
		psPanelPair(chiakiPanel.pairHost, text, chiakiPanel.pairTarget)
	}
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
	case sdl.CONTROLLER_BUTTON_Y:
		psPanelForget()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeChiakiPanel()
	}
	return true
}

func handleChiakiKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !chiakiPanel.open {
		return false
	}
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
	case sdl.K_DELETE:
		psPanelForget()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeChiakiPanel()
	}
	return true
}

func handleChiakiMouse(mx, my int32) bool {
	if !chiakiPanel.open {
		return false
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

	const padX = int32(24)
	// The focused row's stroke and the detail line's descenders both need room;
	// a tighter row clipped "Ready to stream" at the bottom edge.
	rowH := 10 + medH + 4 + smallH + 10
	headerH := 18 + medH + 4 + smallH + 14
	footerH := 20 + smallH

	panelW := int32(820)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	maxRows := (screenHeight - 40 - headerH - footerH) / rowH
	if maxRows < 3 {
		maxRows = 3
	}
	visible := int32(len(rows))
	if visible > maxRows {
		visible = maxRows
	}
	panelH := headerH + visible*rowH + footerH
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

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	renderText(renderer, config, medium, "PS Remote Play", ColorTextPrimary(), panelX+padX, panelY+18)
	subtitle := psPanelSubtitle()
	renderText(renderer, config, font, subtitle, ColorTextTertiary(), panelX+padX, panelY+18+medH+4)

	focusColor := installHighlight()
	rowW := panelW - 2*padX
	chiakiRowHits = chiakiRowHits[:0]
	for i := int32(0); i < visible; i++ {
		idx := chiakiPanel.scroll + int(i)
		row := rows[idx]
		y := panelY + headerH + i*rowH
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
			renderText(renderer, config, font, row.detail, ColorTextTertiary(), r.X+16, r.Y+8+medH+4)
		}
		if row.status != "" {
			statusColor := ColorTextSecondary()
			switch row.status {
			case "Play", "Pair", "Scan", "Add":
				statusColor = focusColor
			case "Error":
				statusColor = ColorDanger
			}
			w, _, _ := font.SizeUTF8(row.status)
			renderText(renderer, config, font, row.status, statusColor, r.X+r.W-int32(w)-16, r.Y+8+medH-smallH)
		}
	}

	hint := "A select    Y forget    B close"
	renderText(renderer, config, font, hint, ColorTextTertiary(), panelX+padX, panelY+panelH-footerH+6)
}

// psPanelSubtitle is the one line that tells the truth about the client's
// readiness: the helper binary, or exactly which file is missing.
func psPanelSubtitle() string {
	if engine := psRemoteEngine(); !engine.installed() {
		return "Helper missing — " + psRuntimeInstallHint()
	}
	return "JukaHub client · PS4 + PS5 · native libchiaki"
}
