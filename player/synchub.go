package main

// synchub.go — Sync: one page and one code for every way saves follow the
// player.
//
// JukaHub could already keep saves the same five ways — another JukaHub over
// Wi-Fi (ssync.go, devsync.go), Google Drive (gdrive_*.go), a WebDAV server
// (webdav_*.go), a RomM server (romm_autosync.go) and Syncthing (syncthing.go)
// — and each had its own page, switch and setup: Nearby devices on in the
// Settings of both handhelds and a code compared on both screens; Tools → Save
// sync for the clouds; Start → Save Manager → Syncthing; Tools → RomM server →
// Connection. This file makes them one:
//
//   - one page: the Save sync scene, retitled Sync (ssyncui.go keeps the
//     comparison and import views; this file draws the start view);
//   - one QR code at its top: the phone opens the companion's /sync page
//     (companion_sync.go), which connects any of the five — a JukaHub found on
//     this Wi-Fi, Google Drive, WebDAV, RomM with RomM's own pairing code, and
//     Syncthing — and each appears here as it arrives;
//   - one list of what is connected ("places"), each opening one sheet with
//     the same few answers: Sync now, Compare, its automatic switch, Forget;
//   - one Automatic sync switch over all of them (syncAutoKey) and one Sync
//     everything now;
//   - no Settings switch to find first: while this page is open, a phone uses
//     the /sync page or a pairing runs, the peer layer listens and looks for
//     other JukaHubs even with Nearby devices off (syncHubWantsPeers), and a
//     finished pairing switches Nearby devices on for good so the two keep
//     finding each other (syncHubAfterPairing). Without a pairing the device
//     goes quiet again a couple of seconds after the page closes.
//
// Continuity between paired devices with nothing pressed — a game's saves
// coming over before it starts and going back after it ends — is
// synchub_peers.go.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// syncAutoKey is the one Automatic sync switch, on unless switched off.
	// Each place keeps its own switch under it (Drive's and WebDAV's auto,
	// RomM's auto_sync); with this one off nothing syncs by itself.
	syncAutoKey = "syncAutomatic"
	// syncHubPhoneTTL is how long the phone's /sync page keeps the peer layer
	// awake after its last call.
	syncHubPhoneTTL = 3 * time.Minute
	// syncHubQRSide and syncHubQRSmall are the page's code: big while nothing
	// is connected, smaller once something is.
	syncHubQRSide  = int32(196)
	syncHubQRSmall = int32(132)
	// syncHubCodeTitle is the full-screen code's title.
	syncHubCodeTitle = "Sync · scan with your phone"
)

var (
	// syncAutoFlag mirrors the Automatic sync switch for the goroutines
	// (peerSyncFlags copies it every two seconds on the main loop).
	syncAutoFlag atomic.Bool
	// syncHubPhoneAt is when the phone's /sync page last called, in Unix
	// seconds (any goroutine).
	syncHubPhoneAt atomic.Int64
	// syncHubPhoneScanAt spaces out the scans the phone page asks for.
	syncHubPhoneScanAt atomic.Int64
)

func init() {
	syncAutoFlag.Store(true)
	deferStartupTask("sync-automatic", func() { syncAutoFlag.Store(syncAutoSetting(appConfig)) })
}

// syncAutoSetting is the stored Automatic sync switch.
func syncAutoSetting(c *Config) bool {
	return peerCustomBool(c, syncAutoKey, true)
}

// syncAutoSet is the switch on the page and on the phone. Main thread.
func syncAutoSet(c *Config, on bool) {
	if c == nil {
		return
	}
	setStoreBool(c, syncAutoKey, on)
	syncAutoFlag.Store(on)
	syncHub.builtAt = time.Time{}
	ssyncScreen.model.builtAt = time.Time{}
	if on {
		ShowToast("Automatic sync on — saves follow you to every device and cloud here", ToastKindSuccess)
		return
	}
	// A background sync waiting for its quiet time does not start.
	saveSyncCancelFull()
	ShowToast("Automatic sync off — saves move only when you press Sync now", ToastKindInfo)
}

// syncHubWantsPeers reports whether Sync needs the peer layer up although the
// Nearby devices switch is off: the page is open, the phone page is in use,
// or a pairing is running. Main thread (peerSyncFlags).
func syncHubWantsPeers(config *Config) bool {
	if peerCurrentScene(config) == ssyncSceneName {
		return true
	}
	if syncHubPhoneRecent(time.Now(), syncHubPhoneTTL) {
		return true
	}
	in, out := peerPairViews()
	return in.live || out.live
}

// syncHubPhoneRecent reports whether the phone page called within d.
func syncHubPhoneRecent(now time.Time, d time.Duration) bool {
	at := syncHubPhoneAt.Load()
	return at > 0 && now.Sub(time.Unix(at, 0)) < d
}

// syncHubNotePhone records a call from the phone page (the HTTP goroutine).
// The first call of a visit closes the full-screen code on the handheld — the
// phone has it — and wakes the peer layer so pairing can start at once.
func syncHubNotePhone() {
	now := time.Now().Unix()
	prev := syncHubPhoneAt.Swap(now)
	if now-prev < 20 {
		return
	}
	tbxPost(func() {
		phoneCardArrived(phoneCardSync)
		syncHub.builtAt = time.Time{}
		if safeModeAllows(SafeFeatureNetwork) {
			peerApplyEnabled(true)
		}
		// The page shows whether Syncthing is installed: its worker knows.
		syncthingStartWorker()
	})
}

// syncHubPhoneScan looks for JukaHubs on this Wi-Fi for the phone page, at
// most every few seconds (the HTTP goroutine).
func syncHubPhoneScan() {
	now := time.Now().UnixMilli()
	if now-syncHubPhoneScanAt.Load() < 2500 {
		return
	}
	syncHubPhoneScanAt.Store(now)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		peerScanAndMerge(ctx)
	}()
}

// syncHubAfterPairing runs on both devices when a pairing finished (main
// thread, peerPairNotifyDone): Nearby devices goes on for good — the two must
// keep finding each other for their saves to follow — and the first sync
// between them is scheduled for the next quiet moment.
func syncHubAfterPairing(config *Config) {
	if config != nil && !peerSettingEnabled(config) {
		setStoreBool(config, peerSettingKey, true)
		peerApplyEnabled(safeModeAllows(SafeFeatureNetwork))
	}
	syncHub.builtAt = time.Time{}
	ssyncScreen.model.builtAt = time.Time{}
	if syncAutoFlag.Load() {
		saveSyncScheduleFull()
	}
}

// ── Model ────────────────────────────────────────────────────────────────

// syncPlace is one row of the page: a connected place, a device or request
// found nearby, or a place that needs the player.
type syncPlace struct {
	id   string
	spec tbxRowSpec
}

// syncHubModel is what the start view draws, rebuilt a few times a second.
type syncHubModel struct {
	// link is the phone page's address (with the PIN), "" without Wi-Fi;
	// linkErr says why; caption is "PIN 123456 · 192.168.1.5:8080".
	link    string
	linkErr string
	caption string
	// phone is set while the phone page is in use.
	phone     bool
	places    []syncPlace
	found     []syncPlace
	connected int
	signIn    bool
	auto      bool
	peerOn    bool
	lan       bool
}

// syncHub is the start view's own state. Main thread only.
var syncHub struct {
	model   syncHubModel
	builtAt time.Time
	// qrLink is the address the code was made for.
	qrLink string
	qr     [][]bool
	// companionAt spaces out the companion checks.
	companionAt time.Time
	// sheetSig names the pairing sheet that is up (syncHubPairSheet).
	sheetSig string
}

// syncHubEnter runs once per visit of the page (ssyncScreenEnter).
func syncHubEnter() {
	syncHub.builtAt = time.Time{}
	syncHub.companionAt = time.Time{}
	syncHub.sheetSig = ""
	// The peer layer at once, without waiting for the two-second flag sync:
	// the other JukaHubs on this Wi-Fi should be listed by the time the page
	// is read (peerSyncFlags keeps it up while the page is open).
	if safeModeAllows(SafeFeatureNetwork) {
		peerApplyEnabled(true)
	}
	// Syncthing's state is read by its worker; starting it costs a goroutine
	// that wakes every half minute and changes nothing while it is off.
	syncthingStartWorker()
	runSceneTask(syncHubScanLoop)
}

// syncHubScanLoop looks for JukaHubs on this Wi-Fi while the page is up, and
// keeps the paired devices' "synced …" times fresh.
func syncHubScanLoop(ctx context.Context) {
	for {
		times := map[string]time.Time{}
		for _, p := range peerPairedList() {
			times[p.ID] = ssyncLastSync(p.ID)
		}
		peerLastSync.mu.Lock()
		// A quiet background pass found the two alike without writing the
		// record the file time comes from (syncHubNoteSynced): keep the later.
		for id, t := range peerLastSync.m {
			if last, ok := times[id]; ok && t.After(last) {
				times[id] = t
			}
		}
		peerLastSync.m = times
		peerLastSync.mu.Unlock()
		peerScanAndMerge(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// syncHubFrame runs every frame the page is up, in any of its views: the
// phone code follows the companion's address, and a pairing shows its sheet.
func syncHubFrame(config *Config) {
	now := time.Now()
	if now.After(syncHub.companionAt) {
		syncHub.companionAt = now.Add(3 * time.Second)
		companionStart()
		if link := syncPhoneURL(companion.url, ""); link != syncHub.qrLink {
			syncHub.qrLink = link
			syncHub.qr = tbxQRModules(link)
			syncHub.builtAt = time.Time{}
		}
	}
	syncHubPairSheet(config)
}

// syncHubPairSheet opens the pairing sheet for the session that needs this
// screen: one started here, or another device's request once its code is up.
// The sheet closed with B cancels the pairing (as on the Devices page).
func syncHubPairSheet(config *Config) {
	in, out := peerPairViews()
	var v peerPairView
	sig := ""
	switch {
	case out.live:
		v = out
		sig = fmt.Sprintf("out:%d:%d:%t", out.token, out.stage, out.localOK)
	case in.live && in.stage == peerPairCompare:
		v = in
		sig = fmt.Sprintf("in:%d:%d:%t", in.token, in.stage, in.localOK)
	}
	if sig == syncHub.sheetSig {
		if sig != "" && !tbxSheetVisible(config) {
			syncHub.sheetSig = ""
			peerPairCancelView(v)
		}
		return
	}
	syncHub.sheetSig = sig
	if sig == "" {
		if tbxSheetVisible(config) && strings.HasPrefix(tbxSheet.spec.token, "pair-") {
			tbxCloseSheet()
		}
		return
	}
	tbxOpenSheet(config, peerPairSheetSpec(v))
}

// syncHubAutoPill is a place's automatic state for its pill: "Auto" when it
// syncs by itself, "Manual" when its own switch or the page's is off.
func syncHubAutoPill(own bool) (string, tbxTone) {
	if own && syncAutoFlag.Load() {
		return "Auto", tbxToneGood
	}
	return "Manual", tbxToneNeutral
}

// syncHubLast is a place's last result with its age, or fallback.
func syncHubLast(now time.Time, line string, at time.Time, fallback string) string {
	if line == "" {
		return fallback
	}
	if !at.IsZero() {
		line += " · " + peerAgo(now, at)
	}
	return line
}

// syncHubBuildModel prepares every string the start view draws. Main thread
// (it reads the config and the sign-in on screen).
func syncHubBuildModel(now time.Time, config *Config) syncHubModel {
	m := syncHubModel{
		auto:   syncAutoSetting(config),
		peerOn: peerEnabledFlag.Load(),
		lan:    partyLANAddress() != "",
		phone:  syncHubPhoneRecent(now, 45*time.Second),
	}
	m.link = syncPhoneURL(companion.url, "")
	if m.link == "" {
		m.linkErr = companion.err
	} else {
		m.caption = companionPairingCaption(companion.url)
	}
	add := func(id string, spec tbxRowSpec) { m.places = append(m.places, syncPlace{id: id, spec: spec}) }

	// Paired JukaHubs.
	paired := map[string]bool{}
	for _, p := range peerPairedList() {
		paired[p.ID] = true
		m.connected++
		model := setOr(p.Model, "JukaHub")
		spec := tbxRowSpec{icon: "devices", title: p.Name}
		switch {
		case peerIsUnpairedThere(p.ID):
			spec.subtitle = "It forgot this device — forget it here, then pair again"
			spec.pill, spec.tone = "Pair again", tbxToneWarn
		case peerOnline(p.ID):
			spec.subtitle = model + " · on this Wi-Fi now"
			spec.pill, spec.tone = "Online", tbxToneGood
		default:
			spec.subtitle = model + " · syncs when both are on the same Wi-Fi"
			spec.pill, spec.tone = "Away", tbxToneNeutral
		}
		if last := peerLastSyncOf(p.ID); !last.IsZero() {
			spec.note = "synced " + peerAgo(now, last)
		}
		add("sh:dev:"+p.ID, spec)
	}

	// Google Drive.
	g := gdriveSettingsGet()
	switch {
	case gdriveSignIn.active:
		m.signIn = true
		m.connected++
	case g.signedIn() && g.Expired:
		m.connected++
		add("gd-signin", tbxRowSpec{icon: "cloud", title: "Google Drive · sign in again", pill: "Sign in", tone: tbxToneWarn,
			subtitle: g.accountLabel() + ": the sign-in expired or was removed — the saves on Drive are safe"})
	case g.signedIn():
		m.connected++
		pill, tone := syncHubAutoPill(g.Auto)
		add("sh:gdrive", tbxRowSpec{icon: "cloud", title: "Google Drive · " + g.accountLabel(), pill: pill, tone: tone,
			subtitle: syncHubLast(now, g.LastResult, g.LastAt, "Signed in · not synced yet")})
	}

	// WebDAV.
	if d := webdavSettingsGet(); d.connected() {
		m.connected++
		if d.Refused {
			add("sh:wdfix", tbxRowSpec{icon: "phone", title: "WebDAV · send the password again", pill: "Phone", tone: tbxToneWarn,
				subtitle: d.label() + ": the server refused the user name or password — the saves there are safe"})
		} else {
			pill, tone := syncHubAutoPill(d.Auto)
			add("sh:webdav", tbxRowSpec{icon: "cloud", title: "WebDAV · " + d.label(), pill: pill, tone: tone,
				subtitle: syncHubLast(now, d.LastResult, d.LastAt, "Connected · not synced yet")})
		}
	}

	// RomM.
	if r := rommServerSettingsGet(); strings.TrimSpace(r.URL) != "" {
		m.connected++
		host := cloudShortURL(strings.TrimPrefix(r.URL, "http://"))
		switch {
		case strings.TrimSpace(r.Token) == "":
			add("sh:romfix", tbxRowSpec{icon: "rom", title: "RomM · " + host, pill: "Token", tone: tbxToneWarn,
				subtitle: "Connect it with a pairing code from your phone, or add a client API token"})
		default:
			pill, tone := syncHubAutoPill(r.AutoSync == nil || *r.AutoSync)
			sub := "Saves sync when a game starts and when it ends"
			if busy, line := rommSyncAllStatus(); busy {
				sub, pill, tone = setOr(line, "Syncing every save…"), "Syncing", tbxToneAccent
			} else if pill == "Manual" {
				sub = "Only Sync all saves copies anything"
			}
			add("sh:romm", tbxRowSpec{icon: "rom", title: "RomM · " + host, subtitle: sub, pill: pill, tone: tone})
		}
	}

	// Syncthing, and the requests and devices it hears.
	st := syncthingSnapshotNow()
	stWant := syncthingEnabled(config)
	if stWant || (st.Installed && len(st.Devices) > 0) {
		m.connected++
		sub := "Folders stay in step with your computer or phone by themselves"
		if n := len(st.Devices); n > 0 {
			sub = pluralizeCount(n, "device", "devices") + " · " + syncthingShortList(st.Devices)
		} else if st.Phase == syncthingPhaseRunning {
			sub = "No device yet — add this handheld's ID in Syncthing on your computer"
		}
		status := syncthingStatusText(st, stWant)
		tone := tbxToneNeutral
		switch status {
		case "Up to date":
			tone = tbxToneGood
		case "Not running", "Not installed":
			tone = tbxToneWarn
		}
		add("sh:st", tbxRowSpec{icon: "sync", title: "Syncthing", subtitle: sub, pill: status, tone: tone})
		if st.Phase == syncthingPhaseRunning {
			for _, p := range st.Pending {
				spec := tbxRowSpec{icon: "sync", title: setOr(p.Name, syncthingShortID(p.ID)) + " (Syncthing)", pill: "Accept",
					tone: tbxToneAccent, subtitle: "Asks to sync with this device · " + syncthingShortID(p.ID)}
				m.found = append(m.found, syncPlace{id: "sh:stp:" + p.ID, spec: spec})
			}
		}
	}

	// JukaHubs on this Wi-Fi that are not paired yet, then Syncthing devices
	// heard on it.
	if m.peerOn {
		for _, d := range peerSeenList() {
			if paired[d.ID] {
				continue
			}
			spec := tbxRowSpec{icon: "devices", title: d.Name, pill: "Pair", tone: tbxToneAccent,
				subtitle: setOr(d.Model, "JukaHub") + " · on this Wi-Fi — pair once, then your saves follow you"}
			m.found = append(m.found, syncPlace{id: "sh:near:" + d.ID, spec: spec})
		}
	}
	if st.Phase == syncthingPhaseRunning {
		known := map[string]bool{}
		for _, d := range st.Devices {
			known[d.ID] = true
		}
		for _, p := range st.Pending {
			known[p.ID] = true
		}
		for _, id := range st.Nearby {
			if known[id] {
				continue
			}
			spec := tbxRowSpec{icon: "sync", title: "Syncthing device " + syncthingShortID(id), pill: "Pair",
				tone: tbxToneNeutral, subtitle: "On this Wi-Fi · asks it to share your save folders"}
			m.found = append(m.found, syncPlace{id: "sh:stn:" + id, spec: spec})
		}
	}
	return m
}

// syncthingShortList names up to two Syncthing devices and how many are
// connected.
func syncthingShortList(devs []syncthingDevice) string {
	var names []string
	connected := 0
	for _, d := range devs {
		if d.Connected {
			connected++
		}
		if len(names) < 2 {
			names = append(names, setOr(d.Name, syncthingShortID(d.ID)))
		}
	}
	out := strings.Join(names, ", ")
	if len(devs) > len(names) {
		out += fmt.Sprintf(" and %d more", len(devs)-len(names))
	}
	return out + fmt.Sprintf(" · %d connected", connected)
}

// syncHubConnectedCount is how many places are connected, cheaply, for the
// tile, the header and Settings. Main thread.
func syncHubConnectedCount(config *Config) int {
	n := peerPairedCount()
	if gdriveSettingsGet().signedIn() {
		n++
	}
	if webdavSettingsGet().connected() {
		n++
	}
	if strings.TrimSpace(rommServerSettingsGet().URL) != "" {
		n++
	}
	if syncthingEnabled(config) {
		n++
	}
	return n
}

// syncHubHeaderStatus is the page header's status chip when no job runs.
func syncHubHeaderStatus(config *Config) (string, sdl.Color) {
	if syncHubConnectedCount(config) == 0 {
		return "", ColorTextTertiary()
	}
	if syncAutoSetting(config) {
		return "Automatic", ToastSuccessInk()
	}
	return "Manual", ColorTextTertiary()
}

// syncHubTileValue is the Tools tile's line: what is connected.
func syncHubTileValue(config *Config) string {
	var names []string
	for _, p := range peerPairedList() {
		names = append(names, p.Name)
	}
	if gdriveSettingsGet().signedIn() {
		names = append(names, "Google Drive")
	}
	if webdavSettingsGet().connected() {
		names = append(names, "WebDAV")
	}
	if strings.TrimSpace(rommServerSettingsGet().URL) != "" {
		names = append(names, "RomM")
	}
	if syncthingEnabled(config) {
		names = append(names, "Syncthing")
	}
	if len(names) == 0 {
		return "Your saves on every JukaHub and cloud — set up with one code"
	}
	if len(names) > 3 {
		names = append(names[:3], fmt.Sprintf("%d more", len(names)-3))
	}
	line := strings.Join(names, " · ")
	if !syncAutoSetting(config) {
		line += " · manual"
	}
	return line
}

// syncHubSettingValue is Settings → Network → Sync's read-out.
func syncHubSettingValue(c *Config) string {
	n := syncHubConnectedCount(c)
	switch {
	case n == 0:
		return "Not set up"
	case !syncAutoSetting(c):
		return fmt.Sprintf("%d connected · manual", n)
	}
	return fmt.Sprintf("%d connected · automatic", n)
}

// ── Drawing ──────────────────────────────────────────────────────────────

// syncHubRenderHome draws the start view's top: the phone code, Sync
// everything now and the switch, what is connected and what is nearby, and
// the other ways to set up. It returns the y below and the next row number;
// ssyncRenderHome draws Device Sync's switches and the offline export after.
func syncHubRenderHome(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top, y int32, row int) (int32, int) {
	now := time.Now()
	if now.Sub(syncHub.builtAt) > 250*time.Millisecond {
		syncHub.model = syncHubBuildModel(now, config)
		syncHub.builtAt = now
	}
	m := &syncHub.model
	nav := &ssyncScreen.nav
	x, w := frame.x, frame.w
	rowH := tbxRowHeight(fonts)
	addRow := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		if top+y+rowH >= frame.bodyTop && top+y <= frame.bodyBot {
			tbxDrawRow(renderer, config, fonts, peerScr(rect, top), spec, nav.focused(id))
		}
		nav.add(id, rect, row, spec.dim)
		row++
		y += rowH + 8
	}
	section := func(title, icon string) {
		tbxDrawSection(renderer, config, title, icon, x, top+y, w)
		y += tbxSectionHeight
	}

	// The code.
	heroH := syncHubDrawHero(renderer, config, fonts, frame, top, y, m, nav.focused("sh:add"))
	nav.add("sh:add", sdl.Rect{X: x, Y: y, W: w, H: heroH}, row, false)
	row++
	y += heroH + 14

	if m.connected > 0 {
		allSub := "Every device on this Wi-Fi and every cloud below, one after another"
		addRow("sh:all", tbxRowSpec{icon: "sync", title: "Sync everything now", subtitle: allSub, pill: "Sync", tone: tbxToneAccent})
		auto := tbxRowSpec{icon: "sync", title: "Automatic sync", pill: "On", tone: tbxToneGood,
			subtitle: "Before a game its saves come over, after it they go back — with everything below"}
		if !m.auto {
			auto.pill, auto.tone = "Off", tbxToneNeutral
			auto.subtitle = "Off — saves move only when you press Sync now · Syncthing has its own switch"
		}
		addRow("sh:auto", auto)
		y += 6
		section("Connected", "sync")
		if m.signIn {
			// Google's code for the phone, while a sign-in waits for it
			// (cloudsync_ui.go draws and runs it).
			y = cloudDrawSignInCard(renderer, config, fonts, frame, top, y)
			if len(gdriveSignIn.qr) > 0 {
				addRow("gd-qr", tbxRowSpec{icon: "qr", title: "Show Google's code full screen", pill: "Enlarge", tone: tbxToneAccent,
					subtitle: "Bigger and brighter for a phone camera"})
			}
			addRow("gd-cancel", tbxRowSpec{icon: "close", title: "Cancel the Google sign-in", pill: "Cancel", tone: tbxToneNeutral,
				subtitle: "Nothing is stored until the phone says yes"})
		}
		for _, p := range m.places {
			addRow(p.id, p.spec)
		}
		y += 6
	}

	if len(m.found) > 0 || (m.peerOn && m.lan) {
		section("Found on this Wi-Fi", "wifi")
		for _, p := range m.found {
			addRow(p.id, p.spec)
		}
		if len(m.found) == 0 {
			hint := "Open Sync on your other JukaHub — it appears here, ready to pair"
			renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, w), ColorTextTertiary(), x+4, top+y)
			y += fonts.smallH + 16
		}
		y += 6
	}

	section("Other ways", "info")
	addRow("sh:manual", tbxRowSpec{icon: "settings", title: "Set up on this screen instead", pill: "Open", tone: tbxToneNeutral,
		subtitle: "No phone at hand: sign in to Google, the RomM page, Syncthing's own code"})
	return y, row
}

// syncHeroLine is one line beside the phone code: font 0 is the title, 1 the
// small text and 2 the caption.
type syncHeroLine struct {
	text  string
	font  int
	color sdl.Color
}

// syncHubDrawHero draws the phone code with what it does, and returns its
// height. Without Wi-Fi it says so instead (A then opens Wi-Fi).
func syncHubDrawHero(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top, y int32, m *syncHubModel, focused bool) int32 {
	side := syncHubQRSide
	if m.connected > 0 {
		side = syncHubQRSmall
	}
	hasCode := m.link != "" && len(syncHub.qr) > 0
	h := fonts.titleH + 3*fonts.smallH + fonts.tinyH + 64
	if hasCode && side+32 > h {
		h = side + 32
	}
	card := peerScr(sdl.Rect{X: frame.x, Y: y, W: frame.w, H: h}, top)
	if card.Y > frame.bodyBot || card.Y+card.H < frame.bodyTop {
		return h
	}
	UIListRow(renderer, card, glassRowRadius, focused, false)
	textX := card.X + 22
	if hasCode {
		tile := sdl.Rect{X: card.X + 16, Y: card.Y + (card.H-side)/2, W: side, H: side}
		fillRoundedRect(renderer, tile.X, tile.Y, tile.W, tile.H, 14, sdl.Color{R: 255, G: 255, B: 255, A: 255})
		drawCompanionQR(renderer, insetRect(tile, 8), syncHub.qr)
		textX = tile.X + tile.W + 24
	}
	textW := card.X + card.W - 20 - textX
	if textW < 80 {
		textW = 80
	}
	var lines []syncHeroLine
	line := func(text string, font int, color sdl.Color) {
		lines = append(lines, syncHeroLine{text: text, font: font, color: color})
	}
	switch {
	case hasCode && m.connected == 0:
		line("Add a device or a cloud", 0, ColorTextPrimary())
		line("Scan with your phone — one page connects any of these:", 1, ColorTextMuted())
		line("Another JukaHub · Google Drive · Nextcloud or WebDAV · RomM · Syncthing", 1, ColorTextSecondary())
	case hasCode:
		line("Add another device or cloud", 0, ColorTextPrimary())
		line("Scan with your phone: another JukaHub, Google Drive, WebDAV, RomM or Syncthing", 1, ColorTextMuted())
	default:
		line("Connect to Wi-Fi to add devices and clouds", 0, ColorTextPrimary())
		line(setOr(m.linkErr, "Your phone sets everything up over Wi-Fi"), 1, ColorTextMuted())
		line("A opens Wi-Fi", 1, ColorTextSecondary())
	}
	if hasCode {
		if m.phone {
			line("Your phone has the page open — finish there", 1, glassAccent())
		}
		caption := "A shows the code full screen"
		if m.caption != "" {
			caption = m.caption + " · " + caption
		}
		line(caption, 2, ColorTextTertiary())
	}
	block := int32(0)
	for i, l := range lines {
		switch l.font {
		case 0:
			block += fonts.titleH
		case 1:
			block += fonts.smallH
		default:
			block += fonts.tinyH
		}
		if i > 0 {
			block += 6
		}
	}
	ty := card.Y + (card.H-block)/2
	for _, l := range lines {
		font, fh := fonts.small, fonts.smallH
		switch l.font {
		case 0:
			font, fh = fonts.title, fonts.titleH
		case 2:
			font, fh = fonts.tiny, fonts.tinyH
		}
		renderText(renderer, config, font, ellipsize(font, l.text, textW), l.color, textX, ty)
		ty += fh + 6
	}
	return h
}

// ── Input ────────────────────────────────────────────────────────────────

// syncHubHint is the A hint for the start view's own rows.
func syncHubHint(id string) (FooterHint, bool) {
	switch {
	case id == "sh:add":
		if syncHub.model.link == "" {
			return FooterHint{Button: "A", Label: "Wi-Fi"}, true
		}
		return FooterHint{Button: "A", Label: "Enlarge"}, true
	case id == "sh:all":
		return FooterHint{Button: "A", Label: "Sync now"}, true
	case id == "sh:auto":
		return FooterHint{Button: "A", Label: "On or off"}, true
	case id == "sh:manual", id == "sh:romfix":
		return FooterHint{Button: "A", Label: "Open"}, true
	case id == "sh:wdfix":
		return FooterHint{Button: "A", Label: "Phone"}, true
	case strings.HasPrefix(id, "sh:near:"), strings.HasPrefix(id, "sh:stn:"):
		return FooterHint{Button: "A", Label: "Pair"}, true
	case strings.HasPrefix(id, "sh:stp:"):
		return FooterHint{Button: "A", Label: "Accept"}, true
	case strings.HasPrefix(id, "sh:"):
		return FooterHint{Button: "A", Label: "Options"}, true
	}
	return FooterHint{}, false
}

// syncHubActivate runs one of the start view's own rows; false for any other
// id. Rows that start a job check the job slot themselves, so the code, the
// switch and pairing work while a sync runs.
func syncHubActivate(config *Config, id string) bool {
	if !strings.HasPrefix(id, "sh:") {
		return false
	}
	switch {
	case id == "sh:add":
		syncHubOpenCode(config)
	case id == "sh:auto":
		PlayToggleSound()
		syncAutoSet(config, !syncAutoSetting(config))
	case id == "sh:all":
		syncHubStartAll(config)
	case id == "sh:manual":
		PlayActivateSound()
		syncHubOpenManual(config)
	case strings.HasPrefix(id, "sh:dev:"):
		syncHubOpenDeviceSheet(config, strings.TrimPrefix(id, "sh:dev:"))
	case id == "sh:gdrive":
		syncHubOpenDriveSheet(config)
	case id == "sh:webdav":
		syncHubOpenWebdavSheet(config)
	case id == "sh:wdfix":
		cloudPhoneOpen(phoneCardWebDAV, "WebDAV · type it on your phone",
			"Scan it, enter the server's WebDAV address, your user name and an app password, then tap Save")
	case id == "sh:romm":
		syncHubOpenRommSheet(config)
	case id == "sh:romfix":
		PlayActivateSound()
		rommCloudOpen(config)
	case id == "sh:st":
		syncHubOpenSyncthingSheet(config)
	case strings.HasPrefix(id, "sh:near:"):
		dev, ok := peerSeenDeviceByID(strings.TrimPrefix(id, "sh:near:"))
		if !ok {
			ShowToast("That device stopped answering — try again in a moment", ToastKindWarning)
			return true
		}
		PlayActivateSound()
		peerPairStart(dev)
	case strings.HasPrefix(id, "sh:stp:"):
		syncHubSyncthingAdd(strings.TrimPrefix(id, "sh:stp:"), "", false)
	case strings.HasPrefix(id, "sh:stn:"):
		syncHubSyncthingAdd(strings.TrimPrefix(id, "sh:stn:"), "", true)
	}
	return true
}

// syncHubOpenCode shows the phone code full screen, or Wi-Fi without one.
func syncHubOpenCode(config *Config) {
	if syncHub.model.link == "" || len(syncHub.qr) == 0 {
		PlayActivateSound()
		wifiScrOpen(config)
		return
	}
	detail := "Connect another JukaHub, Google Drive, WebDAV, RomM or Syncthing from one page"
	if c := syncHub.model.caption; c != "" {
		detail += " · " + c
	}
	if qrZoomOpenCardDetail(syncHub.qr, syncHubCodeTitle, detail) {
		phoneCard.kind, phoneCard.title = phoneCardSync, syncHubCodeTitle
	}
}

// syncHubBusy says a job holds the page, and reports whether one does.
func syncHubBusy() bool {
	if ssyncWorkSnapshot().busy {
		ShowToast("Wait for the current sync to finish (B stops it)", ToastKindInfo)
		return true
	}
	return false
}

// syncHubOpenManual lists the setups that need no phone.
func syncHubOpenManual(config *Config) {
	drive := "Google Drive"
	if gdriveSettingsGet().signedIn() {
		drive = "Drive options"
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Set up on this screen",
		subtitle: "When no phone is at hand, or it cannot reach this device",
		icon:     "sync",
		body: "## Another JukaHub\nOpen Sync on both. Each lists the other under Found on this Wi-Fi: pick it, check that both " +
			"screens show the same code and choose Codes match on both.\n\n" +
			"## Google Drive\nSign in with a code shown here and approved on any phone or computer.\n\n" +
			"## RomM\nThe RomM page takes the server address and a client API token.\n\n" +
			"## Syncthing\nIts page shows this device's ID as a code for Syncthing on Android or your computer.\n\n" +
			"## WebDAV\nNeeds an address and a password: the phone page is the only way to type them.",
		buttons: []tbxSheetButton{
			{label: "Close"},
			{label: "Syncthing", run: syncHubOpenSyncthingPage},
			{label: "RomM", run: func() { rommCloudOpen(config) }},
			{label: drive, primary: true, run: func() {
				if gdriveSettingsGet().signedIn() {
					syncHubOpenDriveSheet(config)
					return
				}
				gdriveStartSignIn(config)
			}},
		},
		focus: 3,
	})
}

// syncHubOpenSyncthingPage opens Syncthing's own page (Save Manager).
func syncHubOpenSyncthingPage() {
	openSaveManagerPanel()
	if savePanel.open {
		syncthingPanelEnter()
	}
}

// ── Place sheets ─────────────────────────────────────────────────────────

// syncHubAutoLine says what a place does by itself.
func syncHubAutoLine(own bool) string {
	switch {
	case !syncAutoFlag.Load():
		return "Automatic sync is off on this page: only Sync now copies anything."
	case own:
		return "By itself: a game's saves come down before it starts and go up after it ends; the rest follows half a minute later."
	}
	return "Its own switch is off: only Sync now copies anything."
}

// syncHubOpenDeviceSheet is one paired JukaHub.
func syncHubOpenDeviceSheet(config *Config, id string) {
	rec, ok := peerPairedRecord(id)
	if !ok {
		return
	}
	PlayActivateSound()
	now := time.Now()
	status := "Not on this Wi-Fi right now"
	if peerOnline(id) {
		status = "On this Wi-Fi now"
	} else if !rec.LastSeen.IsZero() {
		status = "Last seen " + peerAgo(now, rec.LastSeen)
	}
	var what []string
	for _, s := range devsyncCatSpecs {
		if s.name != dsCatSaves && s.name != dsCatStates && devsyncCatOn(s.name) {
			what = append(what, s.label)
		}
	}
	body := "## " + setOr(rec.Model, "JukaHub") + " · ID " + peerShortID(rec.ID) + "\n" + status + "\n\n"
	if last := ssyncLastSync(id); !last.IsZero() {
		body += "Last sync " + peerAgo(now, last) + "\n\n"
	}
	body += "## By itself\n"
	if syncAutoFlag.Load() {
		body += "While both are on the same Wi-Fi, a game's saves come from whichever device has them newer before it starts, " +
			"and go back after it ends. Saves changed on both since the last sync are never chosen for you: Compare shows both.\n\n"
	} else {
		body += "Automatic sync is off on this page: only Sync now copies anything.\n\n"
	}
	body += "## Sync now and Compare\nSync now copies saves and save states (as switched under What syncs) with no questions: " +
		"a file changed on both keeps both copies. Compare lists every file first and lets you pick"
	if len(what) > 0 {
		body += ", and also moves " + strings.Join(what, ", ")
	}
	body += ". A safety snapshot is taken on both devices first and nothing is ever deleted."
	tbxOpenSheet(config, tbxSheetSpec{
		title:    rec.Name,
		subtitle: status,
		icon:     "devices",
		body:     body,
		buttons: []tbxSheetButton{
			{label: "Forget", run: func() { peerConfirmForget(config, rec) }},
			{label: "Compare", run: func() { ssyncStartCompare(rec.ID) }},
			{label: "Sync now", primary: true, run: func() { syncHubSyncPeerNow(rec) }},
		},
		focus: 2,
	})
}

// syncHubOpenDriveSheet is Google Drive.
func syncHubOpenDriveSheet(config *Config) {
	s := gdriveSettingsGet()
	if !s.signedIn() {
		return
	}
	PlayActivateSound()
	body := "## Account\n" + s.accountLabel() + "\n\n## Last sync\n" +
		syncHubLast(time.Now(), s.LastResult, s.LastAt, "Not yet") + "\n\n## Automatic\n" + syncHubAutoLine(s.Auto) +
		"\n\nJukaHub sees only the files it made (\"JukaHub Saves\" in My Drive). A file changed on both sides keeps both copies."
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Google Drive",
		subtitle: s.accountLabel(),
		icon:     "cloud",
		body:     body,
		buttons: []tbxSheetButton{
			{label: "Sign out", run: func() { cloudConfirmSignOut(config) }},
			{label: syncHubAutoButton(s.Auto), run: func() { syncHubToggleCloudAuto(true) }},
			{label: "Compare", run: gdriveStartCompare},
			{label: "Sync now", primary: true, run: func() {
				syncHubRunCloud(gdrivePeerName, gdriveSyncAll, gdriveNoteResult, gdriveErrorText)
			}},
		},
		focus: 3,
	})
}

// syncHubOpenWebdavSheet is the WebDAV server.
func syncHubOpenWebdavSheet(config *Config) {
	d := webdavSettingsGet()
	if !d.connected() {
		return
	}
	PlayActivateSound()
	auto := syncHubAutoLine(d.Auto)
	if d.Auto && syncAutoFlag.Load() && gdriveAutoReady() {
		auto = "Google Drive does the syncs around games while it is signed in; Sync now and Compare use this server."
	}
	body := "## Server\n" + d.URL + "\n\n## Last sync\n" + syncHubLast(time.Now(), d.LastResult, d.LastAt, "Not yet") +
		"\n\n## Automatic\n" + auto + "\n\nChange the address or the password from your phone (the code at the top of Sync)."
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "WebDAV",
		subtitle: d.label(),
		icon:     "cloud",
		body:     body,
		buttons: []tbxSheetButton{
			{label: "Forget", run: func() { cloudConfirmForgetWebdav(config) }},
			{label: syncHubAutoButton(d.Auto), run: func() { syncHubToggleCloudAuto(false) }},
			{label: "Compare", run: webdavStartCompare},
			{label: "Sync now", primary: true, run: func() {
				syncHubRunCloud(webdavPeerName, webdavSyncAll, webdavNoteResult, webdavErrorText)
			}},
		},
		focus: 3,
	})
}

// syncHubAutoButton is a place's own automatic switch as a button.
func syncHubAutoButton(on bool) string {
	if on {
		return "Auto: On"
	}
	return "Auto: Off"
}

// syncHubToggleCloudAuto flips Drive's (drive) or WebDAV's own switch. The
// file is written off the main thread, as the cards always did.
func syncHubToggleCloudAuto(drive bool) {
	PlayToggleSound()
	name := "WebDAV"
	on := !webdavSettingsGet().Auto
	if drive {
		name, on = "Google Drive", !gdriveSettingsGet().Auto
	}
	go func() {
		var err error
		if drive {
			err = gdriveSettingsUpdate(func(s *gdriveSettings) { s.Auto = on })
		} else {
			err = webdavSettingsUpdate(func(s *webdavSettings) { s.Auto = on })
		}
		tbxPost(func() {
			syncHub.builtAt = time.Time{}
			ssyncScreen.model.builtAt = time.Time{}
			switch {
			case err != nil:
				ShowToast("Could not save the setting: "+firstLine(err.Error()), ToastKindError)
			case on:
				ShowToast(name+" syncs by itself again", ToastKindSuccess)
			default:
				ShowToast(name+": only Sync now copies anything", ToastKindInfo)
			}
		})
	}()
}

// syncHubOpenRommSheet is the RomM server.
func syncHubOpenRommSheet(config *Config) {
	r := rommServerSettingsGet()
	if strings.TrimSpace(r.URL) == "" {
		return
	}
	PlayActivateSound()
	own := r.AutoSync == nil || *r.AutoSync
	body := "## Server\n" + r.URL + "\n\n## Automatic\n" + syncHubAutoLine(own) +
		"\n\nRomM keeps RetroArch's saves and states per game; Games on its page downloads games, covers and BIOS."
	primary := tbxSheetButton{label: "Sync all saves", primary: true, run: func() { rommSyncAllStart(rommServerSettingsGet()) }}
	if busy, _ := rommSyncAllStatus(); busy {
		primary = tbxSheetButton{label: "Stop syncing", primary: true, run: rommSyncAllCancel}
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "RomM",
		subtitle: cloudShortURL(strings.TrimPrefix(r.URL, "http://")),
		icon:     "rom",
		body:     body,
		buttons: []tbxSheetButton{
			{label: "Forget", run: func() { syncHubConfirmForgetRomm(config) }},
			{label: syncHubAutoButton(own), run: func() { syncHubToggleRommAuto(!own) }},
			{label: "Games", run: func() { rommCloudOpen(config) }},
			primary,
		},
		focus: 3,
	})
}

// syncHubToggleRommAuto sets RomM's own automatic switch.
func syncHubToggleRommAuto(on bool) {
	PlayToggleSound()
	next := rommServerSettingsGet()
	next.AutoSync = &on
	if err := rommServerSettingsSave(next); err != nil {
		ShowToast("RomM: "+firstLine(err.Error()), ToastKindError)
		return
	}
	rommCloud.settings = next
	syncHub.builtAt = time.Time{}
	if on {
		ShowToast("RomM syncs saves by itself again", ToastKindSuccess)
		return
	}
	ShowToast("RomM: only Sync all saves copies anything", ToastKindInfo)
}

// syncHubConfirmForgetRomm asks before forgetting the server.
func syncHubConfirmForgetRomm(config *Config) {
	r := rommServerSettingsGet()
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Forget this RomM server?",
		subtitle: r.URL,
		icon:     "rom",
		body: "This device forgets the address and the token and stops syncing with the server. The saves and games on the " +
			"server stay there, and the ones on this device stay here.",
		buttons: []tbxSheetButton{
			{label: "Keep"},
			{label: "Forget", primary: true, run: func() { syncHubForgetRomm(false) }},
		},
	})
}

// syncHubForgetRomm empties the RomM connection (the page's or the phone's).
// Main thread.
func syncHubForgetRomm(fromPhone bool) {
	rommSyncAllCancel()
	if err := rommServerSettingsSave(rommServerSettings{}); err != nil {
		ShowToast("RomM: "+firstLine(err.Error()), ToastKindError)
		return
	}
	rommCloud.settings = rommServerSettings{}
	if !rommCloud.busy {
		rommCloud.gen++
		rommCloud.games, rommCloud.firmware = nil, nil
		rommCloud.total = 0
	}
	syncHub.builtAt = time.Time{}
	msg := "RomM server forgotten"
	if fromPhone {
		msg += " (from your phone)"
	}
	ShowToast(msg, ToastKindInfo)
}

// syncHubOpenSyncthingSheet is Syncthing, with this device's ID as a code a
// Syncthing app can scan.
func syncHubOpenSyncthingSheet(config *Config) {
	s := syncthingSnapshotNow()
	want := syncthingEnabled(config)
	PlayActivateSound()
	body := "## State\n" + syncthingStatusText(s, want) + "\n\n"
	if s.MyID != "" {
		body += "## This device\n" + s.MyID + "\n\nIn Syncthing on your computer or on Android (Syncthing-Fork): Add device, then " +
			"scan the code here or type the ID. Accept the request that then appears under Found on this Wi-Fi.\n\n"
	}
	if len(s.Devices) > 0 {
		body += "## Devices\n"
		for _, d := range s.Devices {
			state := "away"
			if d.Connected {
				state = "connected"
			}
			body += setOr(d.Name, syncthingShortID(d.ID)) + " · " + state + "\n"
		}
		body += "\n"
	}
	body += "Syncthing pauses while a game runs, below 15 % battery and without Wi-Fi. It has its own switch: the page's " +
		"Automatic sync does not stop it."
	power := "Turn on"
	if want && s.Installed {
		power = "Turn off"
	}
	buttons := []tbxSheetButton{
		{label: power, run: func() { syncthingTogglePower(syncthingSnapshotNow()) }},
		{label: "More", run: syncHubOpenSyncthingPage},
	}
	if s.Phase == syncthingPhaseRunning {
		buttons = append(buttons, tbxSheetButton{label: "Sync now", primary: true, run: syncHubSyncthingRescan})
	}
	spec := tbxSheetSpec{
		title:    "Syncthing",
		subtitle: syncthingStatusText(s, want),
		icon:     "sync",
		body:     body,
		buttons:  buttons,
		focus:    len(buttons) - 1,
	}
	if s.MyID != "" {
		spec.qr, spec.qrLabel = tbxQRModules(s.MyID), "Syncthing ID"
	}
	tbxOpenSheet(config, spec)
}

// syncHubSyncthingRescan asks Syncthing to look at the save folders now.
func syncHubSyncthingRescan() {
	syncthingPost("rescan", func(ctx context.Context, api syncthingAPI) (string, error) {
		return "Syncthing: syncing the save folders now", api.call(ctx, "POST", "/rest/db/scan", nil, nil)
	})
}

// syncHubSyncthingAdd adds a Syncthing device — a pairing request, one heard
// on this Wi-Fi, or an ID typed on the phone — and shares every save folder
// with it. Main thread.
func syncHubSyncthingAdd(id, name string, ask bool) {
	if !syncthingLooksLikeID(id) {
		ShowToast("That is not a Syncthing device ID", ToastKindWarning)
		return
	}
	if name == "" {
		for _, p := range syncthingSnapshotNow().Pending {
			if p.ID == id {
				name = p.Name
			}
		}
	}
	PlayActivateSound()
	syncthingPost("pair", func(ctx context.Context, api syncthingAPI) (string, error) {
		if err := syncthingAcceptDevice(ctx, api, id, name); err != nil {
			return "", err
		}
		if ask {
			return "Asked " + syncthingShortID(id) + " to sync — accept JukaHub on that device", nil
		}
		return "Syncthing: syncing with " + setOr(name, syncthingShortID(id)) + " — every save folder is shared", nil
	})
	syncHub.builtAt = time.Time{}
}

// syncHubSyncthingOn switches Syncthing on, downloading it the first time.
// Main thread (the phone page's "Turn on Syncthing").
func syncHubSyncthingOn(config *Config) {
	syncthingSetSetting(config, syncthingEnabledKey, true)
	syncthingWantOn.Store(true)
	syncHub.builtAt = time.Time{}
	if !syncthingInstalled() {
		if syncthingSnapshotNow().Phase != syncthingPhaseInstalling {
			syncthingInstallStart()
		}
		return
	}
	syncthingStartWorker()
	syncthingKick()
	ShowToast("Syncthing on — starting", ToastKindSuccess)
}

// syncHubSyncthingOff switches Syncthing off (its folders and devices stay).
func syncHubSyncthingOff(config *Config) {
	syncthingSetSetting(config, syncthingEnabledKey, false)
	syncthingWantOn.Store(false)
	syncthingStartWorker()
	syncthingKick()
	syncHub.builtAt = time.Time{}
	ShowToast("Syncthing off", ToastKindInfo)
}

// ── Jobs ─────────────────────────────────────────────────────────────────

// syncHubNoteSynced records a finished sync with a paired device for the
// rows' "synced …" (main thread).
func syncHubNoteSynced(id string) {
	peerLastSync.mu.Lock()
	if peerLastSync.m == nil {
		peerLastSync.m = map[string]time.Time{}
	}
	peerLastSync.m[id] = time.Now()
	peerLastSync.mu.Unlock()
}

// syncHubJobEnded puts the page back after a job and says how it went. Main
// thread.
func syncHubJobEnded(line string, kind ToastKind, copied bool) {
	ssyncWorkEnd()
	ssyncScreen.cmp = nil
	ssyncScreen.cmpRows = nil
	ssyncScreen.mode = ssyncModeHome
	ssyncScreen.model.builtAt = time.Time{}
	syncHub.builtAt = time.Time{}
	if line == "" {
		return
	}
	ssyncScreen.resultLine = line
	if copied {
		trophyEvent(tkSaveBackup)
	}
	ShowToast(line, kind)
}

// syncHubSyncPeerNow syncs with one paired device without the comparison:
// newest wins, a file changed on both keeps both copies.
func syncHubSyncPeerNow(rec peerRecord) {
	if syncHubBusy() {
		return
	}
	ctx, ok := ssyncWorkStart("Syncing saves with " + rec.Name + "…")
	if !ok {
		return
	}
	PlayActivateSound()
	go func() {
		res, err := syncPeerRunFull(ctx, rec, ssyncWorkProgress)
		tbxPost(func() {
			if err != nil {
				syncHubJobEnded("", ToastKindInfo, false)
				if !errors.Is(err, context.Canceled) {
					ShowToast("Could not sync with "+rec.Name+": "+peerErrorText(err, rec.Name), ToastKindWarning)
				}
				return
			}
			syncHubNoteSynced(rec.ID)
			kind := ToastKindSuccess
			if res.Failed > 0 {
				kind = ToastKindWarning
			}
			syncHubJobEnded("Saves synced with "+rec.Name+" · "+ssyncResultText(res), kind, res.Pulled+res.Pushed > 0)
		})
	}()
}

// syncHubRunCloud syncs with Google Drive or the WebDAV server without the
// comparison (the place sheet's Sync now).
func syncHubRunCloud(name string, run func(context.Context) (ssyncResult, error), note func(string), errText func(error) string) {
	if syncHubBusy() {
		return
	}
	if !IsOnline() {
		ShowToast("Connect to Wi-Fi first — "+name+" is on the internet", ToastKindWarning)
		return
	}
	ctx, ok := ssyncWorkStart("Syncing saves with " + name + "…")
	if !ok {
		return
	}
	PlayActivateSound()
	go func() {
		res, err := run(ctx)
		if err == nil {
			note(ssyncResultText(res))
		}
		tbxPost(func() {
			if err != nil {
				syncHubJobEnded("", ToastKindInfo, false)
				if !errors.Is(err, context.Canceled) {
					ShowToast("Could not sync with "+name+": "+errText(err), ToastKindWarning)
				}
				return
			}
			kind := ToastKindSuccess
			if res.Failed > 0 {
				kind = ToastKindWarning
			}
			syncHubJobEnded("Saves synced with "+name+" · "+ssyncResultText(res), kind, res.Pulled+res.Pushed > 0)
		})
	}()
}

// syncHubStartAll is Sync everything now: every paired device on this Wi-Fi,
// then Google Drive and the WebDAV server, one after another under one job;
// RomM's Sync all and a Syncthing rescan start beside it, as their own jobs.
// Main thread (the page and the phone).
func syncHubStartAll(config *Config) {
	var peers []peerRecord
	if peerEnabledFlag.Load() {
		for _, p := range peerPairedList() {
			if peerAddrFor(p.ID) != "" {
				peers = append(peers, p)
			}
		}
	}
	drive := gdriveSettingsGet().ready()
	dav := webdavSettingsGet().ready()
	romm := rommServerSettingsGet()
	rommOK := strings.TrimSpace(romm.URL) != "" && strings.TrimSpace(romm.Token) != ""
	stOK := syncthingSnapshotNow().Phase == syncthingPhaseRunning
	away := peerPairedCount() - len(peers)
	if len(peers) == 0 && !drive && !dav && !rommOK && !stOK {
		if away > 0 {
			ShowToast("Your paired devices are not on this Wi-Fi right now — nothing else is connected", ToastKindInfo)
			return
		}
		ShowToast("Nothing to sync with yet — scan the code with your phone to add a device or a cloud", ToastKindInfo)
		return
	}
	if (drive || dav) && !IsOnline() && len(peers) == 0 && !rommOK && !stOK {
		ShowToast("Connect to Wi-Fi first — your clouds are on the internet", ToastKindWarning)
		return
	}
	if rommOK {
		if busy, _ := rommSyncAllStatus(); !busy {
			rommSyncAllStart(romm)
		}
	}
	if stOK {
		syncHubSyncthingRescan()
	}
	if len(peers) == 0 && !drive && !dav {
		PlayActivateSound()
		return
	}
	ctx, ok := ssyncWorkStart("Syncing with everything…")
	if !ok {
		ShowToast("Wait for the current sync to finish (B stops it)", ToastKindInfo)
		return
	}
	PlayActivateSound()
	online := IsOnline()
	go func() {
		var names, problems []string
		var total ssyncResult
		addUp := func(res ssyncResult) {
			total.Pulled += res.Pulled
			total.Pushed += res.Pushed
			total.Conflicts += res.Conflicts
			total.Failed += res.Failed
		}
		var synced []string
		for _, p := range peers {
			if ctx.Err() != nil {
				break
			}
			devsyncWorkLabel("Syncing saves with " + p.Name + "…")
			res, err := syncPeerRunFull(ctx, p, ssyncWorkProgress)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					problems = append(problems, p.Name+": "+peerErrorText(err, p.Name))
				}
				continue
			}
			addUp(res)
			names = append(names, p.Name)
			synced = append(synced, p.ID)
		}
		clouds := []struct {
			on      bool
			name    string
			run     func(context.Context) (ssyncResult, error)
			note    func(string)
			errText func(error) string
		}{
			{drive, gdrivePeerName, gdriveSyncAll, gdriveNoteResult, gdriveErrorText},
			{dav, webdavPeerName, webdavSyncAll, webdavNoteResult, webdavErrorText},
		}
		for _, c := range clouds {
			if !c.on || ctx.Err() != nil {
				continue
			}
			if !online {
				problems = append(problems, c.name+": no internet")
				continue
			}
			devsyncWorkLabel("Syncing saves with " + c.name + "…")
			res, err := c.run(ctx)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					problems = append(problems, c.name+": "+c.errText(err))
				}
				continue
			}
			c.note(ssyncResultText(res))
			addUp(res)
			names = append(names, c.name)
		}
		stopped := ctx.Err() != nil
		tbxPost(func() {
			for _, id := range synced {
				syncHubNoteSynced(id)
			}
			line, kind := syncHubAllSummary(names, problems, total, stopped)
			syncHubJobEnded(line, kind, total.Pulled+total.Pushed > 0)
		})
	}()
}

// syncHubAllSummary says how Sync everything now went. Pure.
func syncHubAllSummary(names, problems []string, total ssyncResult, stopped bool) (string, ToastKind) {
	kind := ToastKindSuccess
	line := ""
	switch {
	case len(names) == 0 && stopped:
		return "Sync stopped", ToastKindInfo
	case len(names) == 0:
		line, kind = "Could not sync", ToastKindWarning
	default:
		line = "Synced with " + strings.Join(names, ", ") + " · " + ssyncResultText(total)
	}
	if stopped {
		line += " · stopped"
		kind = ToastKindInfo
	}
	if total.Failed > 0 {
		kind = ToastKindWarning
	}
	if len(problems) > 0 {
		line += " — " + strings.Join(problems, "; ")
		kind = ToastKindWarning
	}
	return line, kind
}
