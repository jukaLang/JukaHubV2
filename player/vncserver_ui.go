package main

// vncserver_ui.go — the VNC and Telnet servers on Tools → Network tools,
// beside FTP and SFTP under "Servers & sharing" (nettoolsscreen.go calls the
// vncTelnet* hooks below from its tile, sheet, press and action switches),
// and what stops both servers: the startup lock and JukaHub's exit.

import (
	"fmt"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func init() {
	// The exit path stops them twice over: with the services, and in the
	// last cleanup. Stopping is idempotent.
	bootOnShutdown(vncTelnetShutdown)
	Cleanup = append(Cleanup, vncTelnetShutdown)
}

// vncTelnetShutdown stops both servers.
func vncTelnetShutdown() {
	vncSrv.Stop()
	telnetSrv.Stop()
}

// vncTelnetLockStop stops both servers when the startup lock comes up: a
// viewer would control, and a shell reach, what the lock is guarding.
func vncTelnetLockStop() {
	if vncSrv.Running() {
		vncSrv.Stop()
	}
	if telnetSrv.Running() {
		telnetSrv.Stop()
	}
}

// vncTelnetSecurityNames lists the running servers for the security
// overview's "servers" row (sectrust.go).
func vncTelnetSecurityNames() []string {
	var names []string
	if vncSrv.Running() {
		names = append(names, fmt.Sprintf("VNC :%d", vncSrv.Port()))
	}
	if telnetSrv.Running() {
		names = append(names, fmt.Sprintf("Telnet :%d (not encrypted)", telnetSrv.Port()))
	}
	return names
}

// vncTelnetSecurityButton is the security overview's Stop button for these
// two servers, beside the file servers' ones (sectrust.go); nil while
// neither runs.
func vncTelnetSecurityButton() []tbxSheetButton {
	var names []string
	if vncSrv.Running() {
		names = append(names, "VNC")
	}
	if telnetSrv.Running() {
		names = append(names, "Telnet")
	}
	if len(names) == 0 {
		return nil
	}
	label := "Stop " + strings.Join(names, " and ")
	return []tbxSheetButton{{label: label, primary: true, run: func() {
		vncTelnetLockStop()
		sectrustScreen.model.builtAt = time.Time{}
		ShowToast(strings.Join(names, " and ")+" stopped", ToastKindInfo)
	}}}
}

// vncTelnetOwns reports whether a Network tools tile is one of these two.
func vncTelnetOwns(id string) bool { return id == "vnc" || id == "telnet" }

// vncTelnetRunning reports whether the tile's server runs.
func vncTelnetRunning(id string) bool {
	switch id {
	case "vnc":
		return vncSrv.Running()
	case "telnet":
		return telnetSrv.Running()
	}
	return false
}

// vncTelnetTile is the tile's value line and tone.
func vncTelnetTile(id string) (string, toolsHubTone) {
	switch id {
	case "vnc":
		if vncSrv.Running() {
			return "On · " + vncSrv.ViewerAddress() + netToolsConnected(vncSrv.clients.count()), toolsHubToneGood
		}
		return "Off — see and control the screen from a VNC app", toolsHubToneNeutral
	case "telnet":
		if telnetSrv.Running() {
			return "On · " + telnetSrv.Address() + netToolsConnected(telnetSrv.clients.count()) + " · not encrypted", toolsHubToneWarn
		}
		return "Off — a shell for a PC on this network, not encrypted", toolsHubToneNeutral
	}
	return "", toolsHubToneNeutral
}

// vncTelnetSheetSeq is part of the sheet's rebuild key: the servers' own
// changes, and Screen Share going on or off (the passwords hide then).
func vncTelnetSheetSeq() uint64 {
	seq := vncSheetSeq.Load() << 1
	if castIsEnabled() {
		seq |= 1
	}
	return seq
}

// vncSheetPassword is the VNC password as the sheet shows it: the saved one
// (made now if there is none yet, so it can be written down before the
// start), or the stand-in in screenshot mode. While Screen Share is on it is
// hidden, like the Phone Companion PIN (castMaskPIN): a Screen Share viewer
// only watches, and a password read off the picture would hand over
// control. Main thread.
func vncSheetPassword() string {
	if shotModeOn() {
		return vncShotPassword
	}
	pass, err := vncPasswordFor(appConfig)
	if err != nil {
		return "unavailable (" + firstLine(err.Error()) + ")"
	}
	return castMaskPIN(pass)
}

// vncGamesLine says how games appear to a viewer on this device.
func vncGamesLine() string {
	switch {
	case castGamesSupported() && vncPadSupported():
		return "shown and played: keys go to the game as the pad's buttons"
	case castGamesSupported():
		return "shown; keys reach JukaHub's own screens only"
	}
	return "JukaHub's own screens only on this device"
}

// vncTelnetSheet fills the server sheet for "vnc" and "telnet"; false for
// any other tile, which netToolsServerSheet fills itself.
func vncTelnetSheet(s *toolsHubSheet, id string) bool {
	var b strings.Builder
	running := false
	switch id {
	case "vnc":
		running = vncSrv.Running()
		if running {
			fmt.Fprintf(&b, "Status:   Running\nAddress:  %s\nPassword: %s\n", vncSrv.ViewerAddress(), vncSheetPassword())
			fsClientLines(&b, vncSrv.Clients())
		} else {
			fmt.Fprintf(&b, "Status:   Stopped\nPassword: %s\n", vncSheetPassword())
		}
		fmt.Fprintf(&b, "Games:    %s\n", vncGamesLine())
		b.WriteString("Keys:     arrows D-pad, Enter A, Esc B, X and Y, PgUp/PgDn L1/R1, Home/End L2/R2, F10 Start; in a game Tab is Select and F1 Menu\n")
		b.WriteString("\nOpen a VNC app on this network — RealVNC Viewer, TigerVNC, macOS Screen Sharing, bVNC or AVNC on a phone — " +
			"and enter the address and this password: it shows the screen and controls the handheld. Keys work as on a keyboard, " +
			"the mouse moves JukaHub's pointer while mouse mode is on, and text copied on the viewer lands on JukaHub's clipboard. " +
			"The password is kept until New password replaces it, which also disconnects every viewer. " +
			"VNC is not encrypted and anyone with the password controls the handheld: use it at home.")
	case "telnet":
		running = telnetSrv.Running()
		if running {
			// Hidden while Screen Share is on, as the VNC password is.
			pass := castMaskPIN(telnetSrv.Password())
			if shotModeOn() {
				pass = telnetShotPassword
			}
			fmt.Fprintf(&b, "Status:   Running\nCommand:  %s\nPassword: %s\nShell:    %s\n", telnetSrv.Command(), pass, telnetSrv.Shell())
			fsClientLines(&b, telnetSrv.Clients())
		} else {
			b.WriteString("Status:   Stopped\n")
		}
		b.WriteString("\nTelnet is not encrypted: anyone on this network can read the session, the password included. " +
			"Use it at home only — SSH is the safe choice. The password is new every time the server starts and is never saved or logged; " +
			"three wrong tries end the connection.")
	default:
		return false
	}
	b.WriteString("\nThe server switches itself off after " + fsIdleLimitText() + " with nobody connected, and when the device joins another network.")
	s.subInk = sdl.Color{}
	if running {
		s.subtitle = "Running on " + toolsIPAddress()
		s.subInk = StatusInk(ColorToastSuccess)
	} else {
		s.subtitle = "Stopped"
	}
	s.setText(b.String())
	s.busy, s.empty = "", ""
	s.pills = s.pills[:0]
	hints := make([]FooterHint, 0, 3)
	if running {
		s.pills = append(s.pills,
			toolsHubPill{id: "toggle", label: "Stop server", danger: true},
			toolsHubPill{id: "copyaddr", label: "Copy address"})
		hints = append(hints, FooterHint{Button: "A", Label: "Select"}, FooterHint{Button: "X", Label: "Copy address"})
	} else {
		s.pills = append(s.pills, toolsHubPill{id: "toggle", label: "Start server", accent: true})
		hints = append(hints, FooterHint{Button: "A", Label: "Select"})
	}
	if id == "vnc" {
		s.pills = append(s.pills, toolsHubPill{id: "newpass", label: "New password"})
	}
	s.pills = append(s.pills, toolsHubPill{id: "close", label: "Close"})
	hints = append(hints, FooterHint{Button: "B", Label: "Close"})
	s.hint = hintLine(hints...)
	return true
}

// vncTelnetToggle starts or stops a server and says how it went.
func vncTelnetToggle(config *Config, id string) {
	name, running := "VNC", vncSrv.Running()
	if id == "telnet" {
		name, running = "Telnet", telnetSrv.Running()
	}
	if running {
		if id == "telnet" {
			telnetSrv.Stop()
		} else {
			vncSrv.Stop()
		}
		ShowToast(name+" server stopped", ToastKindInfo)
		return
	}
	if toolsIPAddress() == "" {
		ShowToast("Connect Wi-Fi first — "+name+" needs a network address", ToastKindWarning)
		return
	}
	var err error
	if id == "telnet" {
		err = telnetSrv.Start(config)
	} else {
		err = vncSrv.Start(config)
	}
	if err != nil {
		ShowToast(name+" failed: "+firstLine(err.Error()), ToastKindError)
		return
	}
	PlaySuccessSound()
	if id == "telnet" {
		ShowToast("Telnet on "+telnetSrv.Address()+" — not encrypted", ToastKindWarning)
		return
	}
	ShowToast("VNC on "+vncSrv.ViewerAddress(), ToastKindSuccess)
}

// vncTelnetPress is A on the tile: start a stopped server and show its
// sheet, or show a running one's (Stop is on the sheet, so a stray press
// never cuts a viewer off). False for other tiles.
func vncTelnetPress(config *Config, id string) bool {
	if !vncTelnetOwns(id) {
		return false
	}
	PlayActivateSound()
	if !vncTelnetRunning(id) {
		vncTelnetToggle(config, id)
		if !vncTelnetRunning(id) {
			return true
		}
	}
	netToolsShowSheet(id)
	return true
}

// vncTelnetAction runs a sheet action for these two servers; false leaves it
// to netToolsSheetAction.
func vncTelnetAction(config *Config, id, action string) bool {
	if !vncTelnetOwns(id) {
		return false
	}
	switch action {
	case "toggle":
		PlayActivateSound()
		vncTelnetToggle(config, id)
	case "copyaddr":
		addr := vncSrv.ViewerAddress()
		if id == "telnet" {
			addr = telnetSrv.Command()
		}
		netToolsCopy(addr, "Address copied")
		return true
	case "newpass":
		PlayActivateSound()
		if err := vncNewPassword(config); err != nil {
			ShowToast("No new password: "+firstLine(err.Error()), ToastKindError)
		} else {
			ShowToast("New VNC password — viewers signed in with the old one were disconnected", ToastKindSuccess)
		}
	default:
		return false
	}
	netTools.sheetKey = netToolsSheetKey{}
	netToolsSyncSheet()
	return true
}
