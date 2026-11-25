package main

// hometabs_smarthome.go — the Tabs Home's Smart Home tab, and its setup.
//
// The tab is a page of tiles, one per device the hub knows
// (smarthome_hub.go): TP-Link plugs, strips, switches and lights, Google's
// speakers, displays and TVs, Home Assistant's lights, switches, fans and
// covers. A on a light or a plug switches it; A on a speaker, and X on any
// device, opens its sheet — brightness, white temperature, volume, play and
// pause, mute, stop, Hide. A tile says the state ("On · 60%", "Playing ·
// …", "Volume 40%") or, while the device cannot be used, why ("Sign in with
// your TP-Link ID").
//
// After the devices come two tiles. Add devices is a folder that sets every
// source up:
//
//	Find devices          look round the Wi-Fi now (the tab also does it by
//	                      itself the first time and every quarter hour)
//	TP-Link Kasa & Tapo   the TP-Link ID, typed on the handheld or on the
//	                      phone; sign out; the Third-Party Compatibility fix
//	Google Home           nothing to set up: Cast devices appear by themselves
//	Home Assistant        address and token from the phone (a token is too
//	                      long for a D-pad)
//	Apple Home            how it works — a Matter pairing code into Home
//	                      Assistant — shown on the phone
//	Hidden devices        A brings one back
//
// Network & self-hosting is the Pi-hole / router / Tailscale panel that was
// all of Start → Smart Home before the tab.
//
// While the tab is up its page is built again every few seconds
// (homeTabsRebuild), and every build tells the hub it is on screen
// (shubKick); the hub's answers rebuild it at once (homeTabsTouch).

import (
	"context"
	"strconv"
	"strings"
)

// homeTabSmartHome is the Smart Home tab's ID (homeTabOrder).
const homeTabSmartHome = "smarthome"

// shubItemPrefix starts a device tile's Key: the item sheet knows a device by
// it (smartHomeSheetRows).
const shubItemPrefix = "sh:"

// homeTabsSmartHomeItems is the tab's page.
func homeTabsSmartHomeItems(c *Config) []homeTabItem {
	shubKick(c)
	v := shubSnapshot()
	items := make([]homeTabItem, 0, len(v.Devices)+3)
	for _, d := range v.Devices {
		items = append(items, shubTile(v.SignedIn, d))
	}
	if len(v.Devices) == 0 {
		items = append(items, shubFindTile(v))
	}
	setup := "TP-Link, Google Home, Home Assistant, Apple Home"
	if v.Scanning {
		setup = "Looking for devices…"
	}
	return append(items, homeTabItem{
		Key: "shs:setup", Title: "Add devices", Sub: setup, Glyph: "plus",
		Folder: true, FolderView: "smarthome:setup", Children: homeTabsSmartHomeSetup,
	}, homeTabItem{
		Key: "shs:network", Title: "Network & self-hosting", Sub: "Pi-hole, router, Tailscale, WireGuard", Glyph: "network",
		Activate: func(c *Config) { openSmartHomePanel(c) },
	})
}

// smartHomeOpen opens Smart Home where it lives: the Smart Home tab when the
// Tabs Home is up and shows it, else the panel (Start menu, search).
func smartHomeOpen(c *Config) {
	if homeTabsOn() && homeTabIndexIn(homeTabsVisible(c), homeTabSmartHome) >= 0 {
		homeTabsGo(c, homeTabSmartHome, 1)
		return
	}
	openSmartHomePanel(c)
}

// shubGlyph is a kind's tile icon.
func shubGlyph(kind string) string {
	switch kind {
	case shubKindPlug, shubKindOutlet:
		return "plug"
	case shubKindBulb, shubKindDimmer, shubKindLightStrip:
		return "light"
	case shubKindSwitch, shubKindFan:
		return "power"
	case shubKindSensor:
		return "thermometer"
	case shubKindSpeaker, shubKindGroup:
		return "volume"
	case shubKindScreen:
		return "tv"
	}
	return "home"
}

// shubNeedText is why a device cannot be used yet; "" when it can.
func shubNeedText(signedIn bool, d shubDevice) string {
	switch d.Need {
	case shubNeedSignIn:
		if signedIn {
			return "TP-Link ID not accepted"
		}
		return "Sign in with your TP-Link ID"
	case shubNeedTPAP:
		return "Needs Third-Party Compatibility"
	case shubNeedUnsupported:
		return "Can't be controlled here yet"
	case shubNeedOffline:
		return "Not answering"
	}
	return ""
}

// shubStateText is a device's state in a few words.
func shubStateText(d shubDevice) string {
	if !d.Online {
		return "Checking…"
	}
	switch d.Kind {
	case shubKindSpeaker, shubKindScreen, shubKindGroup:
		var parts []string
		switch d.Player {
		case "PLAYING", "BUFFERING":
			parts = append(parts, "Playing")
		case "PAUSED":
			parts = append(parts, "Paused")
		}
		what := d.Title
		if what == "" {
			what = d.App
		}
		if what != "" {
			parts = append(parts, what)
		}
		if len(parts) == 0 {
			parts = append(parts, "Idle")
		}
		if d.Muted {
			parts = append(parts, "Muted")
		} else if d.Level >= 0 {
			parts = append(parts, "Volume "+strconv.Itoa(d.Level)+"%")
		}
		return strings.Join(parts, " · ")
	case shubKindSensor:
		if d.Sensor != "" {
			return d.Sensor
		}
		return "Sensor"
	case shubKindHub:
		return "Hub"
	case shubKindCover:
		if d.On {
			return "Open"
		}
		return "Closed"
	}
	s := "Off"
	if d.On {
		s = "On"
		if d.CanLevel && d.Level > 0 {
			s += " · " + strconv.Itoa(d.Level) + "%"
		}
	}
	if d.Parent != "" {
		s += " · " + d.Parent
	}
	return s
}

// shubTile is a device's tile.
func shubTile(signedIn bool, d shubDevice) homeTabItem {
	sub := shubNeedText(signedIn, d)
	if sub == "" {
		sub = shubStateText(d)
	}
	it := homeTabItem{
		Key: shubItemPrefix + d.ID, Title: d.Name, Sub: sub, Glyph: shubGlyph(d.Kind), Dim: d.Need != shubNeedNothing,
	}
	if d.CanOn && d.On && d.Online && d.Need == shubNeedNothing && d.Kind != shubKindCover {
		it.Badge = "On"
	}
	id, name, sheet := d.ID, d.Name, it
	switch {
	case d.Need == shubNeedSignIn:
		it.Activate = shubSignInPrompt
	case d.Need == shubNeedTPAP:
		it.Activate = func(*Config) { shubTPAPHelp() }
	case d.Need == shubNeedOffline:
		it.Activate = func(*Config) {
			if shubScanNow() {
				ShowToast(name+" is not answering — looking for it again", ToastKindInfo)
				return
			}
			ShowToast("Still looking for "+name+"…", ToastKindInfo)
		}
	case d.Source == shubSourceGoogle || !d.CanOn:
		it.Activate = func(c *Config) { homeTabsItemSheetOpen(c, homeTabSmartHome, "", sheet) }
	default:
		it.Activate = func(*Config) { shubToggle(id) }
	}
	return it
}

// shubFindTile looks round the Wi-Fi.
func shubFindTile(v shubView) homeTabItem {
	sub := "Plugs, lights and speakers on this Wi-Fi"
	switch {
	case v.Scanning:
		sub = "Looking…"
	case v.Scanned && len(v.Devices) == 0 && len(v.Hidden) == 0:
		sub = "Nothing found yet · A looks again"
	}
	return homeTabItem{Key: "shs:find", Title: "Find devices", Sub: sub, Glyph: "radar", Activate: func(*Config) { shubFindNow() }}
}

// shubFindNow is Find devices.
func shubFindNow() {
	if shubScanNow() {
		ShowToast("Looking for devices on your Wi-Fi…", ToastKindInfo)
		return
	}
	ShowToast("Already looking…", ToastKindInfo)
}

// shubTPAPHelp says how to let JukaHub in to the newest TP-Link firmware.
func shubTPAPHelp() {
	ShowToast("In the Tapo app: Me › Tapo Lab › Third-Party Compatibility (Kasa app: Settings › Third-Party Compatibility). "+
		"Switch it on, wait a minute, then Find devices.", ToastKindInfo)
}

// shubGoogleHelp says what Google Home gives JukaHub.
func shubGoogleHelp() {
	shubScanNow()
	ShowToast("Nest speakers and displays, Chromecast and Google TV on this Wi-Fi appear by themselves — no sign-in. "+
		"Other makers' lights reach JukaHub through their own maker or Home Assistant.", ToastKindInfo)
}

// homeTabsSmartHomeSetup is the Add devices folder.
func homeTabsSmartHomeSetup(c *Config) []homeTabItem {
	shubKick(c)
	v := shubSnapshot()
	tp := "Plugs, strips and lights · Tapo needs your TP-Link ID"
	if v.SignedIn {
		tp = "Signed in as " + shubMaskUser(v.User)
	}
	google := "Nest speakers, Chromecast, Google TV · no sign-in"
	if v.Google > 0 {
		google = pluralizeCount(v.Google, "speaker or TV found", "speakers and TVs found") + " · no sign-in"
	}
	ha := "Connect it from your phone"
	if v.HAOn {
		ha = "Connected"
		if v.HAErr != "" {
			ha = v.HAErr
		}
	}
	items := []homeTabItem{
		shubFindTile(v),
		{
			Key: "shs:tplink", Title: "TP-Link Kasa & Tapo", Sub: tp, Glyph: "plug",
			Folder: true, FolderView: "smarthome:tplink", Children: homeTabsSmartHomeTPLink,
		},
		{Key: "shs:google", Title: "Google Home", Sub: google, Glyph: "volume", Activate: func(*Config) { shubGoogleHelp() }},
		{Key: "shs:ha", Title: "Home Assistant", Sub: ha, Glyph: "server", Activate: func(*Config) { smartHomePhoneOpen("ha") }},
		{
			Key: "shs:apple", Title: "Apple Home", Sub: "Through Home Assistant · steps on your phone", Glyph: "home",
			Activate: func(*Config) { smartHomePhoneOpen("apple") },
		},
	}
	if len(v.Hidden) > 0 {
		items = append(items, homeTabItem{
			Key: "shs:hidden", Title: "Hidden devices", Sub: pluralizeCount(len(v.Hidden), "device", "devices"), Glyph: "folder",
			Folder: true, FolderView: "smarthome:hidden", Children: homeTabsSmartHomeHidden,
		})
	}
	return items
}

// homeTabsSmartHomeTPLink is the TP-Link folder.
func homeTabsSmartHomeTPLink(*Config) []homeTabItem {
	v := shubSnapshot()
	items := []homeTabItem{
		{
			Key: "shs:tp:keyboard", Title: "Sign in on this device", Sub: "The e-mail and password of your Tapo or Kasa app",
			Glyph: "keyboard", Activate: shubSignInPrompt,
		},
		{
			Key: "shs:tp:phone", Title: "Sign in from your phone", Sub: "Scan the code, type it there", Glyph: "phone",
			Activate: func(*Config) { smartHomePhoneOpen("tplink") },
		},
	}
	if v.SignedIn {
		items = append(items, homeTabItem{
			Key: "shs:tp:out", Title: "Sign out", Sub: shubMaskUser(v.User), Glyph: "exit",
			Activate: func(*Config) {
				shubSignOut()
				ShowToast("Signed out of your TP-Link ID", ToastKindInfo)
			},
		})
	}
	return append(items, homeTabItem{
		Key: "shs:tp:help", Title: "A device will not connect?", Sub: "Third-Party Compatibility in the Tapo or Kasa app",
		Glyph: "info", Activate: func(*Config) { shubTPAPHelp() },
	})
}

// homeTabsSmartHomeHidden is the Hidden devices folder.
func homeTabsSmartHomeHidden(*Config) []homeTabItem {
	v := shubSnapshot()
	items := make([]homeTabItem, 0, len(v.Hidden))
	for _, d := range v.Hidden {
		id, name := d.ID, d.Name
		items = append(items, homeTabItem{
			Key: "shh:" + d.ID, Title: d.Name, Sub: "Hidden · A shows it again", Glyph: shubGlyph(d.Kind), Dim: true,
			Activate: func(*Config) {
				shubSetHidden(id, false)
				ShowToast(name+" is back in Smart Home", ToastKindSuccess)
			},
		})
	}
	return items
}

// smartHomeEmptyText is what an empty Smart Home folder says (only Hidden
// devices can empty).
func smartHomeEmptyText(key string) (title, line, glyph string, ok bool) {
	if !strings.HasPrefix(key, "smarthome:") {
		return "", "", "", false
	}
	return "Nothing hidden", "Press X on a device and choose Hide to tidy it away here.", "home", true
}

// shubSignInPrompt asks for the TP-Link ID on the handheld's keyboard: the
// e-mail address, then the password (masked), checked and kept by
// shubSignIn.
func shubSignInPrompt(*Config) {
	osPromptOpen("TP-Link ID", "you@example.com", "Next", shubSnapshot().User, 254, func(typed string) {
		user, err := shubCleanUser(typed)
		if err != nil {
			if typed != "" {
				ShowToast(companionCloudSentence(err), ToastKindWarning)
			}
			return
		}
		osPromptOpenSecret("TP-Link password", "Password", "Sign in", 256, func(pass string) {
			if pass == "" {
				return
			}
			ShowToast("Checking your TP-Link ID…", ToastKindInfo)
			go func() {
				msg, err := shubSignIn(context.Background(), user, pass)
				if err != nil {
					ShowToast(companionCloudSentence(err), ToastKindWarning)
					return
				}
				ShowToast(msg, ToastKindSuccess)
			}()
		})
	})
}

// smartHomeSheetRows are the item sheet's rows for a Smart Home device; false
// when the item is not one (hometabs_collections_ui.go builds its own rows
// then). A row that changes the device keeps the sheet up and rebuilds it,
// so its words follow ("Turn off" becomes "Turn on").
func smartHomeSheetRows(it homeTabItem) ([]homeTabsSheetRow, bool) {
	id, ok := strings.CutPrefix(it.Key, shubItemPrefix)
	if !ok {
		return nil, false
	}
	d, ok := shubDeviceByID(id)
	if !ok {
		return nil, true
	}
	stay := func(fn func()) func(*Config) {
		return func(*Config) {
			fn()
			homeTabsSheetBuild()
		}
	}
	var rows []homeTabsSheetRow
	switch d.Need {
	case shubNeedSignIn:
		rows = append(rows, homeTabsSheetRow{label: "Sign in with your TP-Link ID", run: func(c *Config) {
			homeTabsSheetClose()
			shubSignInPrompt(c)
		}})
	case shubNeedTPAP:
		rows = append(rows, homeTabsSheetRow{label: "How to let JukaHub in", note: "Third-Party Compatibility", run: func(*Config) {
			homeTabsSheetClose()
			shubTPAPHelp()
		}})
	case shubNeedOffline:
		rows = append(rows, homeTabsSheetRow{label: "Look for it again", run: func(*Config) {
			homeTabsSheetClose()
			shubFindNow()
		}})
	case shubNeedNothing:
		if d.Source == shubSourceGoogle {
			rows = append(rows, shubCastRows(d, stay)...)
		} else {
			rows = append(rows, shubLightRows(d, stay)...)
		}
	}
	name := d.Name
	rows = append(rows, homeTabsSheetRow{label: "Hide from Smart Home", run: func(*Config) {
		homeTabsSheetClose()
		shubSetHidden(id, true)
		ShowToast(name+" is hidden · Add devices › Hidden devices brings it back", ToastKindInfo)
	}})
	return rows, true
}

// ── The Smart Home panel's rows (smart_home_ui.go) ───────────────────────

// shubSourceLabel names where a device comes from, and its model.
func shubSourceLabel(d shubDevice) string {
	src := "TP-Link"
	if d.Source == shubSourceGoogle {
		src = "Google Cast"
	}
	if d.Model != "" {
		return src + " · " + d.Model
	}
	return src
}

// smartHomePanelDeviceRows are the panel's rows for the hub's TP-Link and
// Google devices (Home Assistant's entities have rows of their own there).
func smartHomePanelDeviceRows() []smartHomeRow {
	v := shubSnapshot()
	rows := make([]smartHomeRow, 0, len(v.Devices))
	for _, d := range v.Devices {
		if d.Source == shubSourceHA {
			continue
		}
		row := smartHomeRow{
			label: d.Name, kind: smartHomeRowDevice, entityID: d.ID, detail: shubSourceLabel(d),
			enabled: d.Need == shubNeedNothing || d.Need == shubNeedSignIn, on: d.CanOn && d.On,
			toggle: d.CanOn && d.Need == shubNeedNothing,
		}
		row.value = shubNeedText(v.SignedIn, d)
		switch d.Need {
		case shubNeedNothing:
			row.value = shubStateText(d)
		case shubNeedSignIn:
			row.detail = "A signs in with your TP-Link ID"
		case shubNeedTPAP:
			row.detail = "Tapo app: Me › Tapo Lab › Third-Party Compatibility (Kasa app: Settings) — then Refresh"
		default:
			row.detail = "Not answering — Refresh looks for it again"
		}
		rows = append(rows, row)
	}
	return rows
}

// smartHomePanelDevice is A on a device row: switch it, pause or play a
// speaker, or sign in.
func smartHomePanelDevice(c *Config, id string) {
	d, ok := shubDeviceByID(id)
	if !ok {
		return
	}
	switch {
	case d.Need == shubNeedSignIn:
		closeSmartHomePanel()
		shubSignInPrompt(c)
	case d.Need != shubNeedNothing:
		shubFindNow()
	case d.Source == shubSourceGoogle:
		switch d.Player {
		case "PLAYING", "BUFFERING":
			shubCastDo(id, "pause")
		case "PAUSED":
			shubCastDo(id, "play")
		default:
			ShowToast("Nothing is playing on "+d.Name, ToastKindInfo)
		}
	case d.CanOn:
		shubToggle(id)
	default:
		ShowToast(d.Name+": "+shubStateText(d), ToastKindInfo)
	}
}

// shubLightRows are a plug's, a switch's or a light's rows.
func shubLightRows(d shubDevice, stay func(func()) func(*Config)) []homeTabsSheetRow {
	id := d.ID
	var rows []homeTabsSheetRow
	if d.CanOn {
		label, note := "Turn on", "Off"
		if d.On {
			label, note = "Turn off", "On"
		}
		if d.Kind == shubKindCover {
			label, note = "Open", "Closed"
			if d.On {
				label, note = "Close", "Open"
			}
		}
		rows = append(rows, homeTabsSheetRow{label: label, note: note, run: stay(func() { shubToggle(id) })})
	}
	if d.CanLevel {
		note := ""
		if d.Level > 0 {
			note = strconv.Itoa(d.Level) + "%"
		}
		rows = append(rows,
			homeTabsSheetRow{label: "Brighter", note: note, run: stay(func() { shubStepLevel(id, 10) })},
			homeTabsSheetRow{label: "Dimmer", run: stay(func() { shubStepLevel(id, -10) })},
			homeTabsSheetRow{label: "Full brightness", run: stay(func() { shubSetLevel(id, 100) })},
			homeTabsSheetRow{label: "Night light", note: "10%", run: stay(func() { shubSetLevel(id, 10) })},
		)
	}
	if d.CanTemp {
		rows = append(rows,
			homeTabsSheetRow{label: "Warm white", note: "2700 K", run: stay(func() { shubSetTemp(id, 2700) })},
			homeTabsSheetRow{label: "Daylight", note: "5000 K", run: stay(func() { shubSetTemp(id, 5000) })},
		)
	}
	return rows
}

// shubCastRows are a speaker's, a display's or a TV's rows.
func shubCastRows(d shubDevice, stay func(func()) func(*Config)) []homeTabsSheetRow {
	id := d.ID
	what := d.Title
	if what == "" {
		what = d.App
	}
	var rows []homeTabsSheetRow
	switch d.Player {
	case "PLAYING", "BUFFERING":
		rows = append(rows, homeTabsSheetRow{label: "Pause", note: what, run: stay(func() { shubCastDo(id, "pause") })})
	case "PAUSED":
		rows = append(rows, homeTabsSheetRow{label: "Play", note: what, run: stay(func() { shubCastDo(id, "play") })})
	}
	vol := ""
	if d.Level >= 0 {
		vol = strconv.Itoa(d.Level) + "%"
	}
	rows = append(rows,
		homeTabsSheetRow{label: "Volume up", note: vol, run: stay(func() { shubStepLevel(id, 5) })},
		homeTabsSheetRow{label: "Volume down", run: stay(func() { shubStepLevel(id, -5) })},
	)
	if d.Muted {
		rows = append(rows, homeTabsSheetRow{label: "Unmute", run: stay(func() { shubCastDo(id, "unmute") })})
	} else {
		rows = append(rows, homeTabsSheetRow{label: "Mute", run: stay(func() { shubCastDo(id, "mute") })})
	}
	if d.App != "" {
		rows = append(rows, homeTabsSheetRow{label: "Stop " + d.App, run: stay(func() { shubCastDo(id, "stop") })})
	}
	return rows
}
