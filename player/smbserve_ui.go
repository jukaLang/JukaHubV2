package main

// smbserve_ui.go — the SMB share's tile and sheet on Tools → Network tools
// (nettoolsscreen.go hands the "smb" tile to these): on/off, the Windows and
// Mac addresses with a QR code, the account, the read-only switch, a new
// password and who is connected. All of it runs on the render thread.

import (
	"fmt"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// smbServeTileValue is the tile's line and tone.
func smbServeTileValue() (string, toolsHubTone) {
	if smbServeSupported() != nil {
		return "Handheld only — the SD card as a Windows / Mac drive", toolsHubToneNeutral
	}
	if smbSrv.Running() {
		return "On · " + smbSrv.Address() + netToolsConnected(smbSrv.clients.count()), toolsHubToneGood
	}
	return "Off — the SD card as a Windows / Mac network drive", toolsHubToneNeutral
}

// toggleSMBServer starts or stops the share and says so.
func toggleSMBServer() {
	if smbSrv.Running() {
		smbSrv.Stop()
		ShowToast("SMB share stopped", ToastKindInfo)
		return
	}
	if err := smbServeSupported(); err != nil {
		ShowToast(firstLine(err.Error()), ToastKindInfo)
		return
	}
	if toolsIPAddress() == "" {
		ShowToast("Connect to Wi-Fi first — the SMB share needs a network address", ToastKindWarning)
		return
	}
	smbSrv.readOnly.Store(smbServeReadOnlySetting())
	if err := smbSrv.Start(); err != nil {
		ShowToast("SMB share failed: "+firstLine(err.Error()), ToastKindError)
		return
	}
	PlaySuccessSound()
	// The password is on the sheet only: toasts are kept in the history.
	ShowToast("SMB share on "+smbSrv.Address()+" — user "+smbServeUser+", password on Network tools", ToastKindSuccess)
}

// smbServePress is A on the tile: start a stopped share, then show the
// sheet (Stop is on the sheet, so a stray press never cuts a copy off).
func smbServePress() {
	PlayActivateSound()
	if err := smbServeSupported(); err != nil {
		ShowToast(firstLine(err.Error()), ToastKindInfo)
		return
	}
	if !smbSrv.Running() {
		toggleSMBServer()
		if !smbSrv.Running() {
			return
		}
	}
	netToolsShowSheet("smb")
}

// smbServeSheet fills the Network tools sheet for the share.
func smbServeSheet(s *toolsHubSheet) {
	var b strings.Builder
	running := smbSrv.Running()
	access := "Read and write"
	if smbServeReadOnlySetting() {
		access = "Read-only"
	}
	if running {
		fmt.Fprintf(&b, "Status:   Running\nWindows:  %s\nMac:      %s\n", smbSrv.Address(), smbSrv.MacAddress())
		if name := smbSrv.NetBIOSName(); name != "" {
			fmt.Fprintf(&b, "By name:  \\\\%s\\%s\n", name, smbServeShareName)
		}
	} else {
		b.WriteString("Status:   Stopped\n")
	}
	fmt.Fprintf(&b, "User:     %s\nPassword: %s\nFolder:   %s\nAccess:   %s\n",
		smbServeUser, smbSrv.Password(), "the whole SD card", access)
	if running {
		fsClientLines(&b, smbSrv.Clients())
	}
	b.WriteString("\nWindows: type the Windows address in File Explorer's address bar, sign in with this user and password and tick Remember. " +
		"Mac: Finder → Go → Connect to Server, then the Mac address. Linux: the same smb:// address in the file manager. " +
		"The password stays until you make a new one. Signing is required; the share is not encrypted, so use it on networks you trust.")
	b.WriteString("\nThe share switches itself off after " + fsIdleLimitText() + " nobody uses it, and when the device joins another network.")
	s.subInk = sdl.Color{}
	if running {
		s.subtitle = "Running on " + toolsIPAddress()
		s.subInk = StatusInk(ColorToastSuccess)
	} else {
		s.subtitle = "Stopped"
		if e := smbSrv.LastError(); e != "" {
			s.subtitle = "Stopped · " + firstLine(e)
			s.subInk = StatusInk(ColorToastWarn)
		}
	}
	s.setText(b.String())
	s.busy, s.empty = "", ""
	s.pills = s.pills[:0]
	hints := make([]FooterHint, 0, 3)
	if running {
		s.pills = append(s.pills,
			toolsHubPill{id: "toggle", label: "Stop server", danger: true},
			toolsHubPill{id: "smbqr", label: "Show QR code"},
			toolsHubPill{id: "copyaddr", label: "Copy address"})
		hints = append(hints, FooterHint{Button: "A", Label: "Select"}, FooterHint{Button: "X", Label: "Copy address"})
	} else {
		s.pills = append(s.pills, toolsHubPill{id: "toggle", label: "Start server", accent: true})
		hints = append(hints, FooterHint{Button: "A", Label: "Select"})
	}
	ro := "Make read-only"
	if smbServeReadOnlySetting() {
		ro = "Allow writing"
	}
	s.pills = append(s.pills,
		toolsHubPill{id: "smbro", label: ro},
		toolsHubPill{id: "smbpass", label: "New password"},
		toolsHubPill{id: "close", label: "Close"})
	hints = append(hints, FooterHint{Button: "B", Label: "Close"})
	s.hint = hintLine(hints...)
}

// smbServeSheetAction runs the share's own sheet actions; false leaves the
// action (Close) to the page.
func smbServeSheetAction(action string) bool {
	switch action {
	case "toggle":
		PlayActivateSound()
		toggleSMBServer()
	case "copyaddr":
		netToolsCopy(smbSrv.Address(), "Address copied")
	case "smbqr":
		addr := smbSrv.MacAddress()
		if addr == "" {
			ShowToast("Start the share first", ToastKindInfo)
			return true
		}
		qrZoomOpenCardDetail(tbxQRModules(addr), "SMB share", smbSrv.Address()+" · user "+smbServeUser)
	case "smbro":
		PlayActivateSound()
		on := !smbServeReadOnlySetting()
		if appConfig != nil {
			if appConfig.Variables.Custom == nil {
				appConfig.Variables.Custom = map[string]interface{}{}
			}
			appConfig.Variables.Custom[smbServeROKey] = on
			saveUserConfigDebounced(userConfigFromVariables(appConfig))
		}
		smbSrv.readOnly.Store(on)
		if on {
			ShowToast("SMB share is read-only now", ToastKindInfo)
		} else {
			ShowToast("SMB share accepts writing again", ToastKindInfo)
		}
	case "smbpass":
		PlayActivateSound()
		if err := smbSrv.NewPassword(); err != nil {
			ShowToast("New password failed: "+firstLine(err.Error()), ToastKindError)
		} else {
			ShowToast("New SMB password — connected PCs must sign in again", ToastKindSuccess)
		}
	default:
		return false
	}
	netTools.sheetKey = netToolsSheetKey{}
	netToolsSyncSheet()
	return true
}
