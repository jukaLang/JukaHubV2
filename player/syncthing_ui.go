package main

// syncthing_ui.go — the Syncthing page of the Save Manager.
//
// It is a mode of the Save Manager overlay (saveModeSyncthing), so it reuses
// that panel's card, scrolling, mouse and controller handling; this file only
// supplies the rows, what A and X do on them, and the footer. Every row is
// built from syncthingSnapshotNow() — a copy under a mutex — and every action
// is posted to the Syncthing worker, so nothing here waits on the network.
//
// The flow it is built for: switch Syncthing on (the first time downloads it),
// show "This device" as a QR code, add it in Syncthing-Fork on Android or
// Syncthing on a PC/Mac, then press A on the pairing request that appears here.
// Every save folder is shared with that device at once; on the other side the
// user accepts the offered folders and picks where they go.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/skip2/go-qrcode"
)

// saveModeSyncthing is the Save Manager mode for this page. A value well clear
// of the panel's own iota list, so either can grow.
const saveModeSyncthing saveManagerMode = 100

type syncthingRowKind int

const (
	syncthingRowPower syncthingRowKind = iota
	syncthingRowStatus
	syncthingRowThisDevice
	syncthingRowPending
	syncthingRowNearby
	syncthingRowDevice
	syncthingRowFolders
	syncthingRowConflicts
	syncthingRowInfo
	syncthingRowLAN
	syncthingRowRemove
)

// syncthingRowRef says what a row of the last syncthingPanelRows call is.
type syncthingRowRef struct {
	kind syncthingRowKind
	id   string
	name string
}

// syncthingRowRefs parallels the rows syncthingPanelRows returned last. Main
// thread only, like savePanel.
var syncthingRowRefs []syncthingRowRef

// syncthingQR caches the device-ID code so it is drawn once per ID.
var syncthingQR struct {
	id      string
	modules [][]bool
}

// syncthingPanelEnter opens the page from the Save Manager's Syncthing row.
func syncthingPanelEnter() {
	savePanel.mode = saveModeSyncthing
	savePanel.cursor = 0
	savePanel.scroll = 0
	savePanel.confirm = ""
	syncthingPanelOpen.Store(true)
	syncthingStartWorker()
	syncthingKick()
	PlayActivateSound()
}

// syncthingPanelLeave goes back to the Save Manager's main list, on the
// Syncthing row.
func syncthingPanelLeave() {
	savePanel.mode = saveModeMain
	savePanel.cursor = 0
	for i, row := range saveManagerRows() {
		if row.action == saveActionSync {
			savePanel.cursor = i
		}
	}
	savePanel.scroll = 0
	savePanel.confirm = ""
	syncthingPanelOpen.Store(false)
	PlayBackSound()
}

// syncthingPanelClosed is the whole overlay closing: status polls slow down.
func syncthingPanelClosed() { syncthingPanelOpen.Store(false) }

// syncthingMainRowValue is the right-hand text of the Save Manager's
// Syncthing row.
func syncthingMainRowValue() string {
	syncthingStartWorker()
	return syncthingStatusText(syncthingSnapshotNow(), syncthingEnabled(appConfig))
}

// syncthingPanelSubtitle is the page header's second line.
func syncthingPanelSubtitle() string {
	s := syncthingSnapshotNow()
	line := "Built-in Syncthing · " + syncthingStatusText(s, syncthingEnabled(appConfig))
	if s.Version != "" {
		line += " · " + s.Version
	}
	return line
}

func syncthingOnOff(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

// syncthingPanelRows builds the page's rows and records what each one is.
func syncthingPanelRows() []saveManagerRow {
	s := syncthingSnapshotNow()
	want := syncthingEnabled(appConfig)
	status := syncthingStatusText(s, want)
	running := s.Phase == syncthingPhaseRunning
	var rows []saveManagerRow
	refs := syncthingRowRefs[:0]
	add := func(ref syncthingRowRef, row saveManagerRow) {
		row.action = saveActionSync
		rows = append(rows, row)
		refs = append(refs, ref)
	}

	power := saveManagerRow{label: "Syncthing", value: syncthingOnOff(want), enabled: s.Phase != syncthingPhaseInstalling,
		detail: "Keeps save folders in sync with your phone, PC or another JukaHub.",
		hint:   hintLine(FooterHint{"A", "Switch"})}
	if !s.Installed && s.Phase != syncthingPhaseChecking {
		power.value = "Not installed"
		power.detail = "A downloads the official Syncthing from GitHub and checks its SHA-256."
		power.hint = hintLine(FooterHint{"A", "Install"})
	}
	if s.Phase == syncthingPhaseInstalling {
		power.value = "Installing…"
	}
	add(syncthingRowRef{kind: syncthingRowPower}, power)

	statusDetail := "Pauses while a game runs, below 15% battery and without Wi-Fi."
	if s.Detail != "" {
		statusDetail = s.Detail
	} else if running {
		connected := 0
		for _, d := range s.Devices {
			if d.Connected {
				connected++
			}
		}
		statusDetail = fmt.Sprintf("%s · %d connected · %s", pluralizeCount(len(s.Devices), "device", "devices"),
			connected, pluralizeCount(len(s.Folders), "folder", "folders"))
		if s.NeedBytes > 0 {
			statusDetail += " · " + humanBytes(s.NeedBytes) + " to go"
		}
	}
	add(syncthingRowRef{kind: syncthingRowStatus}, saveManagerRow{label: "Status", value: status, detail: statusDetail,
		hint: hintLine(FooterHint{"A", "Sync now"}), enabled: running})

	if s.MyID != "" {
		add(syncthingRowRef{kind: syncthingRowThisDevice}, saveManagerRow{label: "This device", value: syncthingShortID(s.MyID),
			detail:  "A shows its QR code: add it in Syncthing-Fork (Android) or Syncthing on a PC.",
			hint:    hintLine(FooterHint{"A", "QR code"}),
			enabled: true})
	}
	for _, p := range s.Pending {
		name := p.Name
		if name == "" {
			name = syncthingShortID(p.ID)
		}
		add(syncthingRowRef{kind: syncthingRowPending, id: p.ID, name: name}, saveManagerRow{
			label:   "Pairing request · " + name,
			value:   syncthingShortID(p.ID),
			detail:  "A pairs and shares every save folder with it · X dismisses",
			hint:    hintLine(FooterHint{"A", "Accept"}, FooterHint{"X", "Dismiss"}),
			enabled: running})
	}
	for _, id := range s.Nearby {
		add(syncthingRowRef{kind: syncthingRowNearby, id: id}, saveManagerRow{
			label:   "Nearby Syncthing · " + syncthingShortID(id),
			value:   "Not paired",
			detail:  "A asks it to pair: accept \"JukaHub\" on that device.",
			hint:    hintLine(FooterHint{"A", "Pair"}),
			enabled: running})
	}
	for _, d := range s.Devices {
		state := "Offline"
		if d.Connected {
			state = "Connected"
		}
		add(syncthingRowRef{kind: syncthingRowDevice, id: d.ID, name: d.Name}, saveManagerRow{
			label:   d.Name,
			value:   state,
			detail:  "Paired · ID " + syncthingShortID(d.ID) + " · X twice unpairs",
			hint:    hintLine(FooterHint{"X", "Unpair"}),
			enabled: running})
	}

	folderNames := make([]saveRoot, 0, len(s.Folders))
	for _, f := range s.Folders {
		folderNames = append(folderNames, saveRoot{Name: f.Name, Path: f.Path})
	}
	folderDetail := saveFolderNames(folderNames)
	if len(s.Folders) == 0 {
		folderDetail = "No save folders yet — start a game once."
	}
	add(syncthingRowRef{kind: syncthingRowFolders}, saveManagerRow{label: "Shared save folders", value: fmt.Sprintf("%d", len(s.Folders)),
		detail: folderDetail, hint: hintLine(FooterHint{"A", "Share again"}), enabled: running})

	if s.Conflicts > 0 {
		add(syncthingRowRef{kind: syncthingRowConflicts}, saveManagerRow{label: "Conflicts", value: fmt.Sprintf("%d", s.Conflicts),
			detail:  "Files named *.sync-conflict-* sit beside the save: keep the right one in Files.",
			enabled: true})
	}
	if s.OtherOffers > 0 {
		add(syncthingRowRef{kind: syncthingRowInfo}, saveManagerRow{label: "Other folders offered", value: fmt.Sprintf("%d", s.OtherOffers),
			detail: "This device has no save folder with those IDs, so they are not accepted."})
	}
	if s.Foreign {
		add(syncthingRowRef{kind: syncthingRowInfo}, saveManagerRow{label: "Another Syncthing runs here", value: "Port 8384",
			detail: "Left alone. If it shares these save folders too, keep only one of the two."})
	}
	add(syncthingRowRef{kind: syncthingRowLAN}, saveManagerRow{label: "LAN only", value: syncthingOnOff(syncthingLANOnlySetting(appConfig)),
		detail:  "On: same Wi-Fi only. Off: also over the internet (relays), e.g. a phone on mobile data.",
		hint:    hintLine(FooterHint{"A", "Switch"}),
		enabled: true})
	if s.Installed {
		add(syncthingRowRef{kind: syncthingRowRemove}, saveManagerRow{label: "Remove Syncthing",
			detail:  "Deletes the program and this device's ID; the save files stay. A twice.",
			hint:    hintLine(FooterHint{"A", "Remove"}),
			enabled: s.Phase != syncthingPhaseInstalling})
	}
	syncthingRowRefs = refs
	return rows
}

// syncthingRowAt is the ref for the cursor's row.
func syncthingRowAt(i int) (syncthingRowRef, bool) {
	if i < 0 || i >= len(syncthingRowRefs) {
		return syncthingRowRef{}, false
	}
	return syncthingRowRefs[i], true
}

// syncthingPanelActivate is A on the page.
func syncthingPanelActivate() {
	rows := syncthingPanelRows()
	ref, ok := syncthingRowAt(savePanel.cursor)
	if !ok || savePanel.cursor >= len(rows) {
		return
	}
	s := syncthingSnapshotNow()
	if !rows[savePanel.cursor].enabled {
		switch {
		case ref.kind == syncthingRowInfo:
		case s.Phase == syncthingPhaseInstalling:
			ShowToast("Syncthing is installing — one moment", ToastKindInfo)
		case !syncthingEnabled(appConfig):
			ShowToast("Switch Syncthing on first", ToastKindInfo)
		default:
			ShowToast("Syncthing is not running yet", ToastKindInfo)
		}
		return
	}
	switch ref.kind {
	case syncthingRowPower:
		syncthingTogglePower(s)
	case syncthingRowStatus:
		syncthingPost("rescan", func(ctx context.Context, api syncthingAPI) (string, error) {
			return "Syncing save folders now", api.call(ctx, http.MethodPost, "/rest/db/scan", nil, nil)
		})
	case syncthingRowThisDevice:
		if syncthingQR.id != s.MyID || syncthingQR.modules == nil {
			code, err := qrcode.New(s.MyID, qrcode.Medium)
			if err != nil {
				ShowToast("Cannot draw the QR code: "+err.Error(), ToastKindError)
				return
			}
			syncthingQR.id = s.MyID
			syncthingQR.modules = code.Bitmap()
		}
		qrZoomOpenCardDetail(syncthingQR.modules, "Syncthing · This device", s.MyID)
	case syncthingRowPending, syncthingRowNearby:
		id, name, nearby := ref.id, ref.name, ref.kind == syncthingRowNearby
		syncthingPost("pair", func(ctx context.Context, api syncthingAPI) (string, error) {
			if err := syncthingAcceptDevice(ctx, api, id, name); err != nil {
				return "", err
			}
			if nearby {
				return "Asked " + syncthingShortID(id) + " to pair — accept JukaHub on that device", nil
			}
			return "Paired with " + name + " — every save folder is shared", nil
		})
	case syncthingRowDevice:
		ShowToast(ref.name+": "+ref.id, ToastKindInfo)
	case syncthingRowFolders:
		syncthingPost("share", func(ctx context.Context, api syncthingAPI) (string, error) {
			n, err := syncthingShareFolders(ctx, api, syncthingSnapshotNow().MyID)
			if err != nil {
				return "", err
			}
			return "Sharing " + pluralizeCount(n, "save folder", "save folders"), nil
		})
	case syncthingRowConflicts:
		ShowToast("Open Files, find the *.sync-conflict-* copy beside the save, keep one", ToastKindInfo)
	case syncthingRowLAN:
		on := !syncthingLANOnlySetting(appConfig)
		syncthingSetSetting(appConfig, syncthingLANOnlyKey, on)
		syncthingLANOnly.Store(on)
		if s.Phase == syncthingPhaseRunning {
			foreign := s.Foreign
			syncthingPost("lan", func(ctx context.Context, api syncthingAPI) (string, error) {
				return "", syncthingApplyOptions(ctx, api, on, foreign)
			})
		}
		if on {
			ShowToast("Syncthing: same Wi-Fi only", ToastKindSuccess)
		} else {
			ShowToast("Syncthing: also over the internet", ToastKindSuccess)
		}
	case syncthingRowRemove:
		if savePanel.confirm != "syncthing-remove" {
			savePanel.confirm = "syncthing-remove"
			ShowToast("Press A again to remove Syncthing (saves stay)", ToastKindWarning)
			return
		}
		savePanel.confirm = ""
		syncthingSetSetting(appConfig, syncthingEnabledKey, false)
		syncthingWantOn.Store(false)
		syncthingPost("remove", func(ctx context.Context, api syncthingAPI) (string, error) {
			return "Syncthing removed — your saves are untouched", syncthingRemoveAll()
		})
	}
}

// syncthingTogglePower is A on the Syncthing row: on (installing first when
// needed) or off.
func syncthingTogglePower(s syncthingSnapshot) {
	if s.Phase == syncthingPhaseChecking {
		syncthingStartWorker()
		syncthingKick()
		ShowToast("One moment — checking for Syncthing", ToastKindInfo)
		return
	}
	on := !syncthingEnabled(appConfig) || !s.Installed
	syncthingSetSetting(appConfig, syncthingEnabledKey, on)
	syncthingWantOn.Store(on)
	if on && !s.Installed {
		syncthingInstallStart()
		return
	}
	syncthingStartWorker()
	syncthingKick()
	if on {
		ShowToast("Syncthing on — starting", ToastKindSuccess)
	} else {
		ShowToast("Syncthing off", ToastKindInfo)
	}
}

// syncthingPanelDismiss is X on the page: dismiss a pairing request, or unpair
// a device (pressed twice).
func syncthingPanelDismiss() {
	ref, ok := syncthingRowAt(savePanel.cursor)
	if !ok {
		return
	}
	switch ref.kind {
	case syncthingRowPending:
		id := ref.id
		syncthingPost("dismiss", func(ctx context.Context, api syncthingAPI) (string, error) {
			return "Pairing request dismissed", api.call(ctx, http.MethodDelete, "/rest/cluster/pending/devices?device="+url.QueryEscape(id), nil, nil)
		})
	case syncthingRowDevice:
		if savePanel.confirm != "syncthing-forget:"+ref.id {
			savePanel.confirm = "syncthing-forget:" + ref.id
			ShowToast("Press X again to unpair "+ref.name, ToastKindWarning)
			return
		}
		savePanel.confirm = ""
		id, name := ref.id, ref.name
		syncthingPost("unpair", func(ctx context.Context, api syncthingAPI) (string, error) {
			return "Unpaired " + name, api.call(ctx, http.MethodDelete, "/rest/config/devices/"+url.PathEscape(id), nil, nil)
		})
	}
}

// syncthingPanelHints is the page's footer for the focused row.
func syncthingPanelHints() []FooterHint {
	switch savePanel.confirm {
	case "syncthing-remove":
		return []FooterHint{{"A Again", "Remove"}, {"B", "Cancel"}}
	case "":
	default:
		return []FooterHint{{"X Again", "Unpair"}, {"B", "Cancel"}}
	}
	hints := []FooterHint{{"A", "Select"}}
	if ref, ok := syncthingRowAt(savePanel.cursor); ok {
		switch ref.kind {
		case syncthingRowPending:
			hints = []FooterHint{{"A", "Accept"}, {"X", "Dismiss"}}
		case syncthingRowDevice:
			hints = []FooterHint{{"X", "Unpair"}}
		case syncthingRowThisDevice:
			hints = []FooterHint{{"A", "QR code"}}
		}
	}
	return append(hints, FooterHint{"B", "Back"})
}
