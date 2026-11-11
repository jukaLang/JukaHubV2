package main

// psremote_console_opts.go — one console's options, and the stream rows of
// the PS Remote Play panel.
//
// Start on a console row opens its options page: Play (or Pair), the internet
// address for playing away from home, the PS login passcode the console may
// ask for, and Forget. The page is a third view of the same panel (beside the
// console list and the Account-ID card), so it draws with the panel's own card,
// rows and footer and goes back with B like the Account-ID card does.
//
// The console list itself gains three stream rows — Video codec, Select button
// and Rumble — stored with the other stream settings in psremote.json and
// passed to chiaki-stream through the session file (psremote_stream_opts.go).
//
// Actions, the page and the keyboard fields here are numbered apart from the
// iota lists in psremote_ui.go (as psremote_wake.go numbers its own), so a row
// added there can never take one of these values.

import (
	"errors"
	"net"
	"strconv"
	"strings"
)

// psPageConsoleOptions is the options page of the console psOptsHost names.
const psPageConsoleOptions psPanelPage = 200

// The two keyboard fields of the options page.
const (
	psEditRemoteHost psPanelEdit = 200 + iota
	psEditLoginPIN
)

// The options page's and the stream rows' actions.
const (
	psActionCodec psPanelAction = 200 + iota
	psActionSelectButton
	psActionRumble
	psActionOptsPlay
	psActionOptsPair
	psActionOptsRemoteHost
	psActionOptsLoginPIN
	psActionOptsClearPIN
	psActionOptsForget
	psActionOptsBack
	psActionLastRunOpen
)

// psOptsHost is the console the options page is about (its key in the list).
// Main thread only.
var psOptsHost string

// psOptsKeyboardHook stands in for the keyboard in tests (nil: the real one).
var psOptsKeyboardHook func(edit psPanelEdit, seed string)

// psOptsKeyboard opens the panel's keyboard on one of this page's fields.
func psOptsKeyboard(edit psPanelEdit, seed string) {
	if psOptsKeyboardHook != nil {
		psOptsKeyboardHook(edit, seed)
		return
	}
	psPanelOpenKeyboardWith(edit, seed)
}

// psPanelOpenConsoleOptions is Start on a console row: that console's options.
// Only where the footer offers it — a row that plays or pairs — and not on
// the console being woken: a busy row (a pairing or a wake running) has its
// own way out, B, and its options page would offer a Play that cannot start.
func psPanelOpenConsoleOptions() {
	if chiakiPanel.page != psPageConsoles {
		return
	}
	rows := chiakiRows()
	if chiakiPanel.cursor < 0 || chiakiPanel.cursor >= len(rows) {
		return
	}
	row := rows[chiakiPanel.cursor]
	if row.host == "" || (row.action != psActionPlay && row.action != psActionPair) {
		return
	}
	if run, waking := psWakeRunning(); waking && run.host == row.host {
		return
	}
	psPanelOpenConsoleOptionsFor(row.host)
}

// psPanelOpenConsoleOptionsFor opens the options page of one console.
func psPanelOpenConsoleOptionsFor(host string) {
	if _, ok := psPanelConsole(host); !ok {
		return
	}
	psOptsHost = host
	psPanelShowPage(psPageConsoleOptions)
	PlayActivateSound()
}

// psConsoleOptsRows is the options page. Its rows carry no host on purpose:
// Y forgets the focused row's console, and on this page only the Forget row
// should be able to do that.
func psConsoleOptsRows() []chiakiRow {
	c, ok := psPanelConsole(psOptsHost)
	if !ok {
		return []chiakiRow{
			{label: "This console is no longer in the list", detail: "It was forgotten or moved to a new address.", status: "Info", action: psActionNone},
			{label: "Back", action: psActionOptsBack},
		}
	}
	name := psStreamName(c)
	rows := make([]chiakiRow, 0, 6)
	if c.paired() {
		rows = append(rows, chiakiRow{label: "Play", detail: "Stream " + name + " now", status: "Play", action: psActionOptsPlay})
	} else {
		rows = append(rows, chiakiRow{label: "Pair", detail: "Needed once before the first stream", status: "Pair", action: psActionOptsPair})
	}

	remote := chiakiRow{label: "Internet address", action: psActionOptsRemoteHost}
	switch {
	case !c.paired():
		remote.detail = "Pair first — the address is kept with the pairing"
		remote.status = "—"
		remote.action = psActionNone
	case strings.TrimSpace(c.RemoteHost) != "":
		remote.detail = "Used when " + name + " is not on this Wi-Fi · " + psOptsShownHost(c.RemoteHost, true)
		remote.status = "Edit"
	default:
		remote.detail = "For playing away from home: a router port forward or a VPN address"
		remote.status = "Add"
	}
	rows = append(rows, remote)

	pin := chiakiRow{label: "Login passcode", action: psActionOptsLoginPIN}
	switch {
	case !c.paired():
		pin.detail = "Pair first — the passcode is kept with the pairing"
		pin.status = "—"
		pin.action = psActionNone
	case psLoginPINValid(c.LoginPIN):
		pin.detail = "Saved · entered for you when the console asks for it"
		pin.status = "Saved"
	default:
		pin.detail = "Only if your PlayStation profile asks for a 4-digit passcode"
		pin.status = "Not saved"
	}
	rows = append(rows, pin)
	if c.paired() && psLoginPINValid(c.LoginPIN) {
		rows = append(rows, chiakiRow{label: "Remove the saved passcode", detail: "The console asks for it on screen again", status: "Remove", action: psActionOptsClearPIN})
	}
	if c.paired() {
		rows = append(rows, chiakiRow{label: "Forget this console", detail: "Deletes its pairing keys from this handheld", status: "Forget", action: psActionOptsForget})
	}
	return append(rows, chiakiRow{label: "Back", action: psActionOptsBack})
}

// psConsoleOptsHeader is the options page's title band: the console, where
// JukaHub reaches it, and whether it is paired.
func psConsoleOptsHeader() UIPageHeader {
	c, ok := psPanelConsole(psOptsHost)
	if !ok {
		return UIPanelHeader("Console options", "", "gamepad")
	}
	subtitle := psOptsShownHost(c.Host, false)
	if strings.TrimSpace(c.RemoteHost) != "" {
		subtitle += " · away: " + psOptsShownHost(c.RemoteHost, true)
	}
	if c.paired() {
		subtitle += " · paired"
	} else {
		subtitle += " · not paired"
	}
	return UIPanelHeader(c.label(), subtitle, "gamepad")
}

// psConsoleOptsFooter is the options page's footer.
func psConsoleOptsFooter() string {
	return hintLine(FooterHint{"A", "Select"}, FooterHint{"B", "Back"})
}

// psShotInternetHost stands in for a console's internet address in screenshot
// mode, the way shotLANAddress stands in for LAN addresses (screenshotmode.go):
// a home's public address or DNS name must not end up in a published picture.
const psShotInternetHost = "myhome.example.net"

// psOptsShownHost is an address as the options page prints it: itself, or in
// screenshot mode a stand-in (internet says which kind).
func psOptsShownHost(host string, internet bool) string {
	if !shotModeOn() || strings.TrimSpace(host) == "" {
		return host
	}
	if internet {
		return psShotInternetHost
	}
	return shotLANAddress
}

// psStreamSettingRows are the console list's stream rows, inserted after
// Quality.
func psStreamSettingRows() []chiakiRow {
	s := psPanelStreamSettings()
	rumble := "On"
	if s.NoRumble {
		rumble = "Off"
	}
	return []chiakiRow{
		{label: "Video codec", detail: "PS5 only (a PS4 sends H.264) · H.264 is lighter, H.265 sharper on the Smart Pro S",
			status: psCodecRowStatus(s.Codec, DetectDevice()), action: psActionCodec},
		{label: "Select button", detail: psSelectRowDetail(s.SelectButton), status: psSelectRowStatus(s.SelectButton), action: psActionSelectButton},
		{label: "Rumble", detail: "Game vibration on the handheld's motor and on connected pads", status: rumble, action: psActionRumble},
	}
}

// psPanelCycleStreamSetting steps one stream row. A press (toast) says what it
// became; Left/Right only move it.
func psPanelCycleStreamSetting(action psPanelAction, delta int, announce bool) bool {
	var said string
	var err error
	switch action {
	case psActionCodec:
		var s psRemoteSettings
		s, err = psSaveStreamSettings(func(s *psRemoteSettings) { s.Codec = psNextCodec(s.Codec, delta) })
		said = "Video codec: " + psCodecRowStatus(s.Codec, DetectDevice())
	case psActionSelectButton:
		var s psRemoteSettings
		s, err = psSaveStreamSettings(func(s *psRemoteSettings) {
			if psResolveSelectButton(s.SelectButton) == psSelectCreate {
				s.SelectButton = psSelectTouchpad
			} else {
				s.SelectButton = psSelectCreate
			}
		})
		if psResolveSelectButton(s.SelectButton) == psSelectTouchpad {
			said = "Select sends the touchpad click · Menu + Select sends Create"
		} else {
			said = "Select sends Create · Menu + Select sends the touchpad click"
		}
	case psActionRumble:
		var s psRemoteSettings
		s, err = psSaveStreamSettings(func(s *psRemoteSettings) { s.NoRumble = !s.NoRumble })
		said = "Rumble on"
		if s.NoRumble {
			said = "Rumble off"
		}
	default:
		return false
	}
	if err != nil {
		// The row still shows what is stored, and this says why it did not
		// change.
		PlayBackSound()
		ShowToast("Could not save the stream setting — "+firstLine(err.Error()), ToastKindError)
		return true
	}
	if announce {
		PlayActivateSound()
		ShowToast(said, ToastKindInfo)
	} else {
		PlayNavSound()
	}
	return true
}

// psPanelActivateExtra is A on the rows this file adds.
func psPanelActivateExtra(chosen chiakiRow) {
	switch chosen.action {
	case psActionCodec, psActionSelectButton, psActionRumble:
		psPanelCycleStreamSetting(chosen.action, 1, true)
	case psActionOptsPlay:
		c, ok := psPanelConsole(psOptsHost)
		if !ok {
			return
		}
		// Play from the options page is Play from the list: back to the list,
		// so whatever the list's Play does (waking a resting console first)
		// happens the same way.
		psPanelShowPage(psPageConsoles)
		psPanelActivateHost(c.Host)
	case psActionOptsPair:
		host := psOptsHost
		psPanelShowPage(psPageConsoles)
		psPanelBeginPair(host)
	case psActionOptsRemoteHost:
		c, ok := psPanelConsole(psOptsHost)
		if !ok {
			return
		}
		psOptsKeyboard(psEditRemoteHost, c.RemoteHost)
	case psActionOptsLoginPIN:
		psOptsKeyboard(psEditLoginPIN, "")
	case psActionOptsClearPIN:
		if err := psUpdateConsole(psOptsHost, func(c *psConsole) { c.LoginPIN = "" }); err != nil {
			ShowToast("Could not change the console — "+err.Error(), ToastKindError)
			return
		}
		PlayActivateSound()
		ShowToast("Saved passcode removed", ToastKindInfo)
	case psActionOptsForget:
		c, ok := psPanelConsole(psOptsHost)
		if !ok {
			return
		}
		removePSConsole(c.Host)
		psPanelReload()
		psPanelShowPage(psPageConsoles)
		ShowToast("Forgot "+c.label(), ToastKindInfo)
	case psActionOptsBack:
		psPanelBack()
	case psActionLastRunOpen:
		if host := psLastRunNoteHost(); host != "" {
			psPanelOpenConsoleOptionsFor(host)
		}
	}
}

// psPanelActivateHost runs A on the console list's row for host.
func psPanelActivateHost(host string) {
	for i, row := range chiakiRows() {
		if row.host == host {
			chiakiPanel.cursor = i
			chiakiPanelActivate()
			return
		}
	}
}

// psPanelApplyOptsEdit commits the options page's keyboard fields. Both are
// submit-only: nothing is stored until the keyboard's Save.
func psPanelApplyOptsEdit(edit psPanelEdit, text string) {
	host := psOptsHost
	switch edit {
	case psEditRemoteHost:
		var port string
		text, port = psSplitRemoteHost(text)
		c, _ := psPanelConsole(host)
		if text == "" {
			if strings.TrimSpace(c.RemoteHost) == "" {
				return
			}
			if err := psUpdateConsole(host, func(c *psConsole) { c.RemoteHost = "" }); err != nil {
				ShowToast("Could not change the console — "+err.Error(), ToastKindError)
				return
			}
			ShowToast("Internet address removed", ToastKindInfo)
			return
		}
		if problem := psRemoteHostProblem(text, c.Host); problem != "" {
			ShowToast(problem, ToastKindWarning)
			psOptsKeyboard(psEditRemoteHost, text)
			return
		}
		if err := psUpdateConsole(host, func(c *psConsole) { c.RemoteHost = text }); err != nil {
			ShowToast("Could not change the console — "+err.Error(), ToastKindError)
			return
		}
		if port != "" && port != strconv.Itoa(psRemotePortStream) {
			// The port is not something the client can change, so a forward to
			// another one (19295 → 9295) would fail without a word later.
			ShowToast("Internet address saved without :"+port+" — Remote Play always uses TCP 9295 and UDP "+
				"9296-9297, so forward those ports to the console", ToastKindWarning)
			return
		}
		ShowToast("Internet address saved — used when the console is not on this Wi-Fi", ToastKindSuccess)
	case psEditLoginPIN:
		pin := psNormalizeLoginPIN(text)
		if pin == "" {
			return
		}
		if !psLoginPINValid(pin) {
			ShowToast("The login passcode is the 4 numbers your PlayStation profile asks for", ToastKindWarning)
			psOptsKeyboard(psEditLoginPIN, text)
			return
		}
		if err := psUpdateConsole(host, func(c *psConsole) { c.LoginPIN = pin }); err != nil {
			ShowToast("Could not change the console — "+err.Error(), ToastKindError)
			return
		}
		ShowToast("Login passcode saved — JukaHub enters it when the console asks", ToastKindSuccess)
	}
}

// psRemotePortStream is the console's Remote Play port, the one a typed
// "host:port" usually names.
const psRemotePortStream = 9295

// psCleanRemoteHost takes what people paste for an address: a URL's host part,
// without a scheme, path or the Remote Play port.
func psCleanRemoteHost(text string) string {
	host, _ := psSplitRemoteHost(text)
	return host
}

// psSplitRemoteHost is psCleanRemoteHost that also returns the port it left
// out ("" when none was typed), so the panel can say when it was not 9295. An
// IPv4-mapped IPv6 address comes back as plain IPv4.
func psSplitRemoteHost(text string) (host, port string) {
	text = strings.TrimSpace(text)
	if i := strings.Index(text, "://"); i >= 0 {
		text = text[i+3:]
	}
	if i := strings.IndexAny(text, "/?#"); i >= 0 {
		text = text[:i]
	}
	host = text
	if strings.HasPrefix(text, "[") {
		if end := strings.Index(text, "]"); end > 0 {
			host = text[1:end]
			if rest := text[end+1:]; strings.HasPrefix(rest, ":") && psAllDigits(rest[1:]) {
				port = rest[1:]
			}
		}
	} else if h, p, ok := strings.Cut(text, ":"); ok && !strings.Contains(p, ":") && psAllDigits(p) {
		host, port = h, p
	}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			host = v4.String()
		}
	}
	return host, port
}

// psRemoteHostProblem is why an internet address cannot be used, or "".
func psRemoteHostProblem(host, lan string) string {
	if !psPanelValidHost(host) {
		return "Type just the address, like myhome.duckdns.org or 203.0.113.7 — the Remote Play ports are fixed"
	}
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		// The wake and the home-or-away choice speak IPv4 only, so an IPv6
		// address would never be streamed to.
		return "IPv6 addresses do not work here yet — type the console's IPv4 address or a host name"
	}
	if strings.EqualFold(host, strings.TrimSpace(lan)) {
		return "That is the console's home address — type the address it has from outside (port forward or VPN)"
	}
	return ""
}

// psAllDigits reports whether s is a non-empty run of ASCII digits.
func psAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// psUpdateConsole changes one stored console and saves the list. Only a
// paired console is stored, so only a paired one has options to keep.
func psUpdateConsole(host string, update func(*psConsole)) error {
	// The load and the save hold psHostsMu together, so a pairing or an address
	// move saved in between is not written over (psremote.go).
	psHostsMu.Lock()
	state := loadPSHosts()
	for i := range state.Hosts {
		if state.Hosts[i].Host != host {
			continue
		}
		update(&state.Hosts[i])
		err := savePSHostsLocked(state)
		psHostsMu.Unlock()
		if err != nil {
			return err
		}
		psPanelReload()
		return nil
	}
	psHostsMu.Unlock()
	return errors.New("pair it first")
}
