package main

// cloudsync_ui.go — the cloud cards at the top of Save sync's start view
// (ssyncui.go draws the page; this file draws and runs the cards).
//
// Google Drive:
//   - no OAuth client yet: "Set up Google sign-in", a code that opens the
//     phone page where the player sends the client file from Google Cloud
//     Console (companion_cloud.go); the sign-in starts by itself afterwards;
//   - signed out: "Sign in to Google Drive", which shows a code and a QR code
//     of google.com/device on the page (Y/A on "Show the code full screen"
//     enlarges it) while the phone approves;
//   - signed in: the account and the last result with "Compare" (the same
//     comparison and Sync now as a paired device), the automatic-sync switch
//     and Sign out.
//
// WebDAV (Nextcloud, ownCloud, Koofr, InfiniCLOUD, a NAS) for players with no
// Google project: connected from the phone too, then the same Compare, switch
// and Forget. Last, a row that explains the route to Google Drive through a
// computer (Syncthing into Google Drive for desktop).
//
// A comparison with either cloud carries gdrivePeerID, which tells the page's
// shared code "a cloud copy, no clock"; gdriveStartSync tells the two apart by
// name.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// gdriveSignIn is the sign-in on screen. Main thread only; gen tells a
// finished sign-in's late answers apart from the next one's.
var gdriveSignIn struct {
	active  bool
	gen     int
	code    string
	url     string
	qr      [][]bool
	expires time.Time
	cancel  context.CancelFunc
}

// cloudScreen holds the WebDAV server's side of a WebDAV comparison on screen
// (ssyncScreen.gdriveRemote does it for Drive). Main thread only.
var cloudScreen struct {
	webdav *webdavRemote
}

// cloudDriveModel is what the cards draw, rebuilt with the page's model.
type cloudDriveModel struct {
	configured bool
	source     string
	clientID   string
	settings   gdriveSettings
	webdav     webdavSettings
}

func cloudBuildDriveModel(config *Config) cloudDriveModel {
	source, id := gdriveClientSourceOf(config)
	m := cloudDriveModel{configured: source != "", source: source, clientID: id, settings: gdriveSettingsGet(), webdav: webdavSettingsGet()}
	// The phone page's copy, while it is at hand (companion_cloud.go).
	cloudGoogleCache.Store(cloudGoogleStatusNow(config))
	return m
}

// cloudShortURL is the address as the player types it.
func cloudShortURL(u string) string {
	u = strings.TrimPrefix(u, "https://")
	return strings.TrimPrefix(u, "www.")
}

// cloudShortClientID is a client ID short enough for a row: its project
// number and the first letters after it.
func cloudShortClientID(id string) string {
	id = strings.TrimSuffix(id, gdriveClientIDSuffix)
	if len(id) > 22 {
		id = id[:22] + "…"
	}
	return id
}

// cloudRenderDrive draws the cards and returns the y below them and the next
// row number.
func cloudRenderDrive(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top, y int32, row int, dm cloudDriveModel) (int32, int) {
	nav := &ssyncScreen.nav
	x, w := frame.x, frame.w
	rowH := tbxRowHeight(fonts)
	addRow := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		tbxDrawRow(renderer, config, fonts, peerScr(rect, top), spec, nav.focused(id))
		nav.add(id, rect, row, false)
		row++
		y += rowH + 8
	}
	tbxDrawSection(renderer, config, "Google Drive", "cloud", x, top+y, w)
	y += tbxSectionHeight
	s := dm.settings
	switch {
	case gdriveSignIn.active:
		y = cloudDrawSignInCard(renderer, config, fonts, frame, top, y)
		if len(gdriveSignIn.qr) > 0 {
			addRow("gd-qr", tbxRowSpec{icon: "qr", title: "Show the code full screen", pill: "Enlarge", tone: tbxToneAccent,
				subtitle: "Bigger and brighter for a phone camera"})
		}
		addRow("gd-cancel", tbxRowSpec{icon: "close", title: "Cancel sign-in", pill: "Cancel", tone: tbxToneNeutral,
			subtitle: "Nothing is stored until the phone says yes"})
	case !s.signedIn() && !dm.configured:
		addRow("gd-setup", tbxRowSpec{icon: "phone", title: "Set up Google sign-in", pill: "Set up", tone: tbxToneAccent,
			subtitle: "Once, about 10 minutes: make a free Google client on a computer, then send it from your phone"})
	case !s.signedIn():
		addRow("gd-signin", tbxRowSpec{icon: "cloud", title: "Sign in to Google Drive", pill: "Sign in", tone: tbxToneAccent,
			subtitle: "Saves follow you to every device — scan a code with your phone, no typing here"})
		if dm.source == "config" {
			addRow("gd-client", tbxRowSpec{icon: "phone", title: "Your own Google client", pill: "Change", tone: tbxToneNeutral,
				subtitle: cloudShortClientID(dm.clientID) + " · change or remove it from your phone"})
		}
	case s.Expired:
		addRow("gd-signin", tbxRowSpec{icon: "cloud", title: "Google Drive · sign in again", pill: "Sign in", tone: tbxToneWarn,
			subtitle: s.accountLabel() + ": the sign-in expired or was removed — the saves on Drive are safe"})
		addRow("gd-signout", tbxRowSpec{icon: "logout", title: "Sign out", pill: "Sign out", tone: tbxToneNeutral,
			subtitle: "Forget this Google account on this device"})
	default:
		sub := "Not synced yet — compare to see what goes up"
		if s.LastResult != "" {
			sub = s.LastResult
			if !s.LastAt.IsZero() {
				sub += " · " + peerAgo(time.Now(), s.LastAt)
			}
		}
		addRow("gd-sync", tbxRowSpec{icon: "cloud", title: "Google Drive · " + s.accountLabel(), subtitle: sub, pill: "Compare", tone: tbxToneAccent})
		auto := tbxRowSpec{icon: "sync", title: "Sync automatically when a game starts and ends", pill: "Off", tone: tbxToneNeutral,
			subtitle: "Off — only Compare and Sync now copy anything"}
		if s.Auto {
			auto.pill, auto.tone = "On", tbxToneGood
			auto.subtitle = "Before a game its saves come down (up to 6 s); after it they go up, then the rest syncs"
		}
		addRow("gd-auto", auto)
		addRow("gd-signout", tbxRowSpec{icon: "logout", title: "Sign out", pill: "Sign out", tone: tbxToneNeutral,
			subtitle: "Your saves stay on Drive, which keeps each file's earlier versions for 30 days"})
	}

	y += 14
	tbxDrawSection(renderer, config, "WebDAV and other clouds", "cloud", x, top+y, w)
	y += tbxSectionHeight
	d := dm.webdav
	switch {
	case !d.connected():
		addRow("wd-setup", tbxRowSpec{icon: "phone", title: "Connect a WebDAV server", pill: "Set up", tone: tbxToneAccent,
			subtitle: "Nextcloud, ownCloud, Koofr, InfiniCLOUD or a NAS — no Google project; type it on your phone"})
	case d.Refused:
		addRow("wd-setup", tbxRowSpec{icon: "phone", title: "WebDAV · send the password again", pill: "Phone", tone: tbxToneWarn,
			subtitle: d.label() + ": the server refused the user name or password — the saves there are safe"})
		addRow("wd-forget", tbxRowSpec{icon: "logout", title: "Forget this server", pill: "Forget", tone: tbxToneNeutral,
			subtitle: "This device stops syncing with it; the saves on the server stay"})
	default:
		sub := "Not synced yet — compare to see what goes up"
		if d.LastResult != "" {
			sub = d.LastResult
			if !d.LastAt.IsZero() {
				sub += " · " + peerAgo(time.Now(), d.LastAt)
			}
		}
		addRow("wd-sync", tbxRowSpec{icon: "cloud", title: "WebDAV · " + d.label(), subtitle: sub, pill: "Compare", tone: tbxToneAccent})
		auto := tbxRowSpec{icon: "sync", title: "Sync automatically when a game starts and ends", pill: "Off", tone: tbxToneNeutral,
			subtitle: "Off — only Compare and Sync now copy anything"}
		if d.Auto {
			auto.pill, auto.tone = "On", tbxToneGood
			auto.subtitle = "Before a game its saves come down (up to 6 s); after it they go up, then the rest syncs"
			if s.ready() && s.Auto {
				auto.subtitle = "Google Drive does the game syncs while it is signed in; Compare syncs with WebDAV"
			}
		}
		addRow("wd-auto", auto)
		addRow("wd-edit", tbxRowSpec{icon: "phone", title: "Change the server or password", pill: "Phone", tone: tbxToneNeutral,
			subtitle: "Opens the same page on your phone"})
		addRow("wd-forget", tbxRowSpec{icon: "logout", title: "Forget this server", pill: "Forget", tone: tbxToneNeutral,
			subtitle: "This device stops syncing with it; the saves on the server stay"})
	}
	addRow("cloud-pc", tbxRowSpec{icon: "info", title: "Google Drive through a computer", pill: "How", tone: tbxToneNeutral,
		subtitle: "No Google project at all: Syncthing to a folder inside Google Drive for desktop"})
	return y + 14, row
}

// cloudDrawSignInCard draws the code, the address and the QR code.
func cloudDrawSignInCard(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top, y int32) int32 {
	const qrSide = 196
	card := sdl.Rect{X: frame.x, Y: y, W: frame.w, H: qrSide + 32}
	UICard(renderer, peerScr(card, top), DefaultUICardStyle())
	cy := top + card.Y
	textX := card.X + 20
	if gdriveSignIn.code == "" {
		renderText(renderer, config, fonts.body, "Asking Google for a code…", ColorTextSecondary(), textX, cy+(card.H-fonts.bodyH)/2)
		return y + card.H + 10
	}
	if len(gdriveSignIn.qr) > 0 {
		tile := sdl.Rect{X: card.X + 16, Y: cy + 16, W: qrSide, H: qrSide}
		fillRoundedRect(renderer, tile.X, tile.Y, tile.W, tile.H, 14, sdl.Color{R: 255, G: 255, B: 255, A: 255})
		drawCompanionQR(renderer, insetRect(tile, 8), gdriveSignIn.qr)
		textX = tile.X + tile.W + 24
	}
	textW := card.X + card.W - 20 - textX
	big := FontForRole(config, TypeTitle)
	if big == nil {
		big = fonts.title
	}
	bigH := int32(big.Height())
	ty := cy + 18
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, "On your phone or computer, open", textW), ColorTextMuted(), textX, ty)
	ty += fonts.smallH + 4
	renderTextRaw(renderer, config, fonts.body, ellipsize(fonts.body, cloudShortURL(gdriveSignIn.url), textW), ColorTextPrimary(), textX, ty)
	ty += fonts.bodyH + 10
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, "and enter this code", textW), ColorTextMuted(), textX, ty)
	ty += fonts.smallH + 4
	renderTextRaw(renderer, config, big, gdriveSignIn.code, glassAccent(), textX, ty)
	ty += bigH + 10
	wait := "Waiting for the phone…"
	if left := time.Until(gdriveSignIn.expires); left > time.Minute {
		wait += " the code works for " + pluralizeCount(int(left/time.Minute), "more minute", "more minutes")
	}
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, wait, textW), ColorTextTertiary(), textX, ty)
	return y + card.H + 10
}

// cloudHint is the A hint for the cards' rows.
func cloudHint(id string) (FooterHint, bool) {
	switch id {
	case "gd-setup", "wd-setup":
		return FooterHint{Button: "A", Label: "Set up"}, true
	case "gd-signin":
		return FooterHint{Button: "A", Label: "Sign in"}, true
	case "gd-client", "wd-edit":
		return FooterHint{Button: "A", Label: "Phone"}, true
	case "gd-qr":
		return FooterHint{Button: "A", Label: "Enlarge"}, true
	case "gd-cancel":
		return FooterHint{Button: "A", Label: "Cancel"}, true
	case "gd-sync", "wd-sync":
		return FooterHint{Button: "A", Label: "Compare"}, true
	case "gd-auto", "wd-auto":
		return FooterHint{Button: "A", Label: "Toggle"}, true
	case "gd-signout":
		return FooterHint{Button: "A", Label: "Sign out"}, true
	case "wd-forget":
		return FooterHint{Button: "A", Label: "Forget"}, true
	case "cloud-pc":
		return FooterHint{Button: "A", Label: "How"}, true
	}
	return FooterHint{}, false
}

// cloudActivate runs a card row; false when id is not the cards'.
func cloudActivate(config *Config, id string) bool {
	switch id {
	case "gd-setup", "gd-client":
		cloudSetupGoogleFromPhone()
	case "gd-signin":
		gdriveStartSignIn(config)
	case "gd-qr":
		qrZoomOpenCardDetail(gdriveSignIn.qr, "Sign in to Google Drive",
			"Open "+cloudShortURL(gdriveSignIn.url)+" and enter "+gdriveSignIn.code)
	case "gd-cancel":
		PlayBackSound()
		gdriveCancelSignIn()
	case "gd-sync":
		PlayActivateSound()
		gdriveStartCompare()
	case "gd-auto":
		PlayToggleSound()
		on := !gdriveSettingsGet().Auto
		go func() {
			err := gdriveSettingsUpdate(func(s *gdriveSettings) { s.Auto = on })
			cloudSettingSaved(err)
		}()
	case "gd-signout":
		PlayActivateSound()
		cloudConfirmSignOut(config)
	case "wd-setup", "wd-edit":
		cloudPhoneOpen(phoneCardWebDAV, "WebDAV · type it on your phone",
			"Scan it, enter the server's WebDAV address, your user name and an app password, then tap Save")
	case "wd-sync":
		PlayActivateSound()
		webdavStartCompare()
	case "wd-auto":
		PlayToggleSound()
		on := !webdavSettingsGet().Auto
		go func() {
			err := webdavSettingsUpdate(func(s *webdavSettings) { s.Auto = on })
			cloudSettingSaved(err)
		}()
	case "wd-forget":
		PlayActivateSound()
		cloudConfirmForgetWebdav(config)
	case "cloud-pc":
		PlayActivateSound()
		cloudOpenComputerSheet(config)
	default:
		return false
	}
	return true
}

// cloudSettingSaved refreshes the page after a switch was written (any
// goroutine).
func cloudSettingSaved(err error) {
	tbxPost(func() {
		ssyncScreen.model.builtAt = time.Time{}
		if err != nil {
			ShowToast("Could not save the setting: "+firstLine(err.Error()), ToastKindError)
		}
	})
}

// cloudSetupGoogleFromPhone shows the code that opens the phone page's
// Google card.
func cloudSetupGoogleFromPhone() {
	cloudPhoneOpen(phoneCardGoogle, "Google sign-in · set it up on your phone",
		"Scan it and follow the steps: make the client on a computer, then send its file from the phone")
}

// cloudOpenComputerSheet explains Google Drive through a computer.
func cloudOpenComputerSheet(config *Config) {
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Google Drive through a computer",
		subtitle: "No Google project, no sign-in on this device",
		icon:     "info",
		body: "A PC or Mac that runs Google Drive for desktop can carry your saves to Drive:\n\n" +
			"• On the computer, install Google Drive for desktop and Syncthing (syncthing.net)\n" +
			"• Here, open Start → Save Manager → Syncthing, switch it on and pair the computer\n" +
			"• On the computer, accept the save folders Syncthing offers and put them inside the Google Drive folder\n\n" +
			"Saves move whenever both are on the same network, or online through Syncthing's relays; the computer must be on. " +
			"Google Drive itself has no WebDAV, so the WebDAV card cannot reach it.",
		qr:      tbxQRModules(cloudDocURL),
		qrLabel: "CLOUD_SYNC.md",
		buttons: []tbxSheetButton{
			{label: "Close"},
			{label: "Open Save Manager", primary: true, run: openSaveManagerPanel},
		},
	})
}

// cloudDocURL is the setup guide, for the sheet's QR code.
const cloudDocURL = "https://github.com/jukaLang/JukaHubV2/blob/main/docs/CLOUD_SYNC.md"

// cloudConfirmSignOut asks before signing out.
func cloudConfirmSignOut(config *Config) {
	s := gdriveSettingsGet()
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Sign out of Google Drive?",
		subtitle: s.accountLabel(),
		icon:     "logout",
		body: "This device forgets the account and stops syncing with it. The saves already on Drive stay there, " +
			"and the saves on this device stay here.\n\nSign in again any time to carry on where you left off.",
		buttons: []tbxSheetButton{
			{label: "Keep"},
			{label: "Sign out", primary: true, run: func() {
				saveSyncCancelFull()
				gdriveSignOut()
				ssyncScreen.model.builtAt = time.Time{}
				ShowToast("Signed out of Google Drive", ToastKindInfo)
			}},
		},
	})
}

// cloudConfirmForgetWebdav asks before forgetting the WebDAV server.
func cloudConfirmForgetWebdav(config *Config) {
	d := webdavSettingsGet()
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Forget this WebDAV server?",
		subtitle: d.label(),
		icon:     "logout",
		body: "This device forgets the address and the password and stops syncing with the server. The saves on the " +
			"server stay there, and the saves on this device stay here.",
		buttons: []tbxSheetButton{
			{label: "Keep"},
			{label: "Forget", primary: true, run: func() {
				saveSyncCancelFull()
				webdavForget()
				// A comparison still on screen holds the password: its Sync
				// now must not reach the forgotten server.
				cloudScreen.webdav = nil
				ssyncScreen.model.builtAt = time.Time{}
				ShowToast("WebDAV server forgotten", ToastKindInfo)
			}},
		},
	})
}

// ── Sign-in ─────────────────────────────────────────────────────────────────

// gdriveStartSignIn asks Google for a code, shows it, and waits for the
// phone. Without a client it opens the setup code instead. Main thread.
func gdriveStartSignIn(config *Config) {
	if gdriveSignIn.active {
		return
	}
	client := gdriveClientFrom(config)
	if client.ID == "" {
		cloudSetupGoogleFromPhone()
		return
	}
	if !IsOnline() {
		ShowToast("Connect to Wi-Fi first — signing in needs the internet", ToastKindWarning)
		return
	}
	PlayActivateSound()
	gdriveBeginSignIn(client, nil)
}

// gdriveBeginSignIn shows the code and waits for the phone's approval. pre is
// a code already asked for with this client (the phone's setup checked the
// client with it), nil to ask now. A sign-in already on screen is replaced.
// Main thread.
func gdriveBeginSignIn(client gdriveClient, pre *gdriveDeviceCode) {
	if gdriveSignIn.active {
		gdriveSignInEnded(context.Canceled)
	}
	ctx, cancel := context.WithCancel(context.Background())
	gdriveSignIn.gen++
	gen := gdriveSignIn.gen
	gdriveSignIn.active, gdriveSignIn.cancel = true, cancel
	gdriveSignIn.code, gdriveSignIn.url, gdriveSignIn.qr = "", "", nil
	ssyncScreen.nav.focus = "gd-cancel"
	ssyncScreen.model.builtAt = time.Time{}
	current := func() bool { return gdriveSignIn.active && gdriveSignIn.gen == gen }
	go func() {
		defer cancel()
		var dc gdriveDeviceCode
		if pre != nil {
			dc = *pre
		} else {
			cctx, ccancel := context.WithTimeout(ctx, 30*time.Second)
			got, err := gdriveRequestDeviceCode(cctx, client)
			ccancel()
			if err != nil {
				tbxPost(func() {
					if current() {
						gdriveSignInEnded(err)
					}
				})
				return
			}
			dc = got
		}
		link := dc.url()
		qr := tbxQRModules(link)
		tbxPost(func() {
			if !current() {
				return
			}
			gdriveSignIn.code, gdriveSignIn.url, gdriveSignIn.qr = dc.UserCode, link, qr
			gdriveSignIn.expires = time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
			if len(qr) > 0 {
				ssyncScreen.nav.focus = "gd-qr"
			}
		})
		ans, err := gdrivePollDeviceToken(ctx, client, dc)
		if err != nil {
			tbxPost(func() {
				if current() {
					gdriveSignInEnded(err)
				}
			})
			return
		}
		fctx, fcancel := context.WithTimeout(ctx, 30*time.Second)
		st, err := gdriveFinishSignIn(fctx, client, ans)
		fcancel()
		tbxPost(func() {
			if !current() {
				return
			}
			gdriveSignInEnded(err)
			if err != nil {
				return
			}
			PlaySuccessSound()
			ShowToast("Signed in to Google Drive as "+st.accountLabel(), ToastKindSuccess)
			// The first comparison at once: the player sees what goes up.
			if peerCurrentScene(appConfig) == ssyncSceneName && !ssyncWorkSnapshot().busy {
				gdriveStartCompare()
			}
		})
	}()
}

// gdriveSignInEnded clears the sign-in card; err (other than a cancel) is
// shown.
func gdriveSignInEnded(err error) {
	if gdriveSignIn.cancel != nil {
		gdriveSignIn.cancel()
	}
	gdriveSignIn.active, gdriveSignIn.cancel = false, nil
	gdriveSignIn.code, gdriveSignIn.url, gdriveSignIn.qr = "", "", nil
	ssyncScreen.nav.focus = ""
	ssyncScreen.model.builtAt = time.Time{}
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	logWarn("gdrive", "sign-in failed: %s", gdriveErrorText(err))
	ShowToast("Google sign-in: "+gdriveErrorText(err), ToastKindWarning)
}

// gdriveCancelSignIn stops waiting for the phone.
func gdriveCancelSignIn() {
	if gdriveSignIn.active {
		gdriveSignInEnded(context.Canceled)
	}
}

// ── Compare and sync ────────────────────────────────────────────────────────

// gdriveStartCompare compares this device's saves with Drive in the
// background and opens the comparison (ssyncRenderCompare).
func gdriveStartCompare() {
	ctx, ok := ssyncWorkStart("Comparing saves with Google Drive…")
	if !ok {
		ShowToast("Wait for the current job to finish", ToastKindInfo)
		return
	}
	ssyncScreen.model.builtAt = time.Time{}
	go func() {
		var (
			cmp    ssyncComparison
			remote *gdriveRemote
			err    error
		)
		if gdriveLockWithin(ctx) {
			cmp, remote, err = gdriveCompare(ctx, "")
			gdriveSyncMu.Unlock()
		} else {
			err = ctx.Err()
		}
		tbxPost(func() {
			ssyncWorkEnd()
			ssyncScreen.model.builtAt = time.Time{}
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					ShowToast("Could not compare with Google Drive: "+gdriveErrorText(err), ToastKindWarning)
				}
				return
			}
			ssyncScreen.cmp = &cmp
			ssyncScreen.gdriveRemote = remote
			cloudScreen.webdav = nil
			ssyncScreen.cmpRows, ssyncScreen.cmpSummary = ssyncBuildCompareRows(&cmp)
			ssyncScreen.mode = ssyncModeCompare
			ssyncScreen.nav.focus, ssyncScreen.nav.scroll = "run", 0
			PlaySuccessSound()
		})
	}()
}

// gdriveStartSync carries out the cloud comparison on screen (ssyncStartSync
// hands over every comparison marked gdrivePeerID): Drive's here, WebDAV's in
// webdavStartSync.
func gdriveStartSync(job ssyncComparison) {
	if job.PeerName == webdavPeerName {
		webdavStartSync(job)
		return
	}
	remote := ssyncScreen.gdriveRemote
	if remote == nil {
		gdriveStartCompare()
		return
	}
	ctx, ok := ssyncWorkStart("Syncing saves with Google Drive…")
	if !ok {
		return
	}
	PlayActivateSound()
	ssyncScreen.gdriveRemote = nil
	go func() {
		var res ssyncResult
		var err error
		if gdriveLockWithin(ctx) {
			res = gdriveRun(ctx, job, remote, true, ssyncWorkProgress)
			gdriveSyncMu.Unlock()
			gdriveNoteResult(ssyncResultText(res))
		} else {
			err = ctx.Err()
		}
		tbxPost(func() { cloudSyncEnded(gdrivePeerName, res, err) })
	}()
}

// cloudSyncEnded puts the page back after a cloud sync and says how it went.
// Main thread.
func cloudSyncEnded(name string, res ssyncResult, err error) {
	ssyncWorkEnd()
	ssyncScreen.cmp = nil
	ssyncScreen.cmpRows = nil
	ssyncScreen.mode = ssyncModeHome
	ssyncScreen.nav.focus, ssyncScreen.nav.scroll = "", 0
	ssyncScreen.model.builtAt = time.Time{}
	if err != nil {
		return
	}
	ssyncScreen.resultLine = "Last sync with " + name + ": " + ssyncResultText(res)
	if res.Pulled+res.Pushed > 0 {
		trophyEvent(tkSaveBackup)
	}
	kind := ToastKindSuccess
	if res.Failed > 0 {
		kind = ToastKindWarning
	}
	msg := "Saves synced with " + name + " · " + ssyncResultText(res)
	if res.Failed > 0 && len(res.Errors) > 0 {
		msg += " — " + res.Errors[0]
	}
	ShowToast(msg, kind)
}

// webdavStartCompare compares this device's saves with the WebDAV server in
// the background and opens the comparison.
func webdavStartCompare() {
	ctx, ok := ssyncWorkStart("Comparing saves with the WebDAV server…")
	if !ok {
		ShowToast("Wait for the current job to finish", ToastKindInfo)
		return
	}
	ssyncScreen.model.builtAt = time.Time{}
	go func() {
		var (
			cmp    ssyncComparison
			remote *webdavRemote
			err    error
		)
		if gdriveLockWithin(ctx) {
			cmp, remote, err = webdavCompare(ctx, "")
			gdriveSyncMu.Unlock()
		} else {
			err = ctx.Err()
		}
		tbxPost(func() {
			ssyncWorkEnd()
			ssyncScreen.model.builtAt = time.Time{}
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					ShowToast("Could not compare with the WebDAV server: "+webdavErrorText(err), ToastKindWarning)
				}
				return
			}
			ssyncScreen.cmp = &cmp
			ssyncScreen.gdriveRemote = nil
			cloudScreen.webdav = remote
			ssyncScreen.cmpRows, ssyncScreen.cmpSummary = ssyncBuildCompareRows(&cmp)
			ssyncScreen.mode = ssyncModeCompare
			ssyncScreen.nav.focus, ssyncScreen.nav.scroll = "run", 0
			PlaySuccessSound()
		})
	}()
}

// webdavStartSync carries out the WebDAV comparison on screen.
func webdavStartSync(job ssyncComparison) {
	remote := cloudScreen.webdav
	if remote == nil {
		webdavStartCompare()
		return
	}
	ctx, ok := ssyncWorkStart("Syncing saves with the WebDAV server…")
	if !ok {
		return
	}
	PlayActivateSound()
	cloudScreen.webdav = nil
	go func() {
		var res ssyncResult
		var err error
		if gdriveLockWithin(ctx) {
			res = webdavRun(ctx, job, remote, true, ssyncWorkProgress)
			gdriveSyncMu.Unlock()
			webdavNoteResult(ssyncResultText(res))
		} else {
			err = ctx.Err()
		}
		tbxPost(func() { cloudSyncEnded(webdavPeerName, res, err) })
	}()
}
