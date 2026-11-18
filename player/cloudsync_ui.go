package main

// cloudsync_ui.go — the Google Drive card at the top of Save sync's start
// view (ssyncui.go draws the page; this file draws and runs the card).
//
//   - no OAuth client in this build: one row that explains what is missing;
//   - signed out: "Sign in to Google Drive", which shows a code and a QR code
//     of google.com/device on the page (Y/A on "Show the code full screen"
//     enlarges it) while the phone approves;
//   - signed in: the account and the last result with "Compare" (the same
//     comparison and Sync now as a paired device), the automatic-sync switch
//     and Sign out.

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

// cloudDriveModel is what the card draws, rebuilt with the page's model.
type cloudDriveModel struct {
	configured bool
	settings   gdriveSettings
}

func cloudBuildDriveModel(config *Config) cloudDriveModel {
	return cloudDriveModel{configured: gdriveClientConfigured(config), settings: gdriveSettingsGet()}
}

// cloudShortURL is the address as the player types it.
func cloudShortURL(u string) string {
	u = strings.TrimPrefix(u, "https://")
	return strings.TrimPrefix(u, "www.")
}

// cloudRenderDrive draws the card and returns the y below it and the next
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
		addRow("gd-setup", tbxRowSpec{icon: "cloud", title: "Google Drive sign-in is not set up in this build", pill: "Why", tone: tbxToneNeutral,
			subtitle: "It needs a Google OAuth client — docs/CLOUD_SYNC.md says how", dim: true})
	case !s.signedIn():
		addRow("gd-signin", tbxRowSpec{icon: "cloud", title: "Sign in to Google Drive", pill: "Sign in", tone: tbxToneAccent,
			subtitle: "Saves follow you to every device — scan a code with your phone, no typing here"})
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

// cloudHint is the A hint for the card's rows.
func cloudHint(id string) (FooterHint, bool) {
	switch id {
	case "gd-setup":
		return FooterHint{Button: "A", Label: "Why"}, true
	case "gd-signin":
		return FooterHint{Button: "A", Label: "Sign in"}, true
	case "gd-qr":
		return FooterHint{Button: "A", Label: "Enlarge"}, true
	case "gd-cancel":
		return FooterHint{Button: "A", Label: "Cancel"}, true
	case "gd-sync":
		return FooterHint{Button: "A", Label: "Compare"}, true
	case "gd-auto":
		return FooterHint{Button: "A", Label: "Toggle"}, true
	case "gd-signout":
		return FooterHint{Button: "A", Label: "Sign out"}, true
	}
	return FooterHint{}, false
}

// cloudActivate runs a card row; false when id is not the card's.
func cloudActivate(config *Config, id string) bool {
	switch id {
	case "gd-setup":
		PlayActivateSound()
		cloudOpenSetupSheet(config)
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
			tbxPost(func() {
				ssyncScreen.model.builtAt = time.Time{}
				if err != nil {
					ShowToast("Could not save the setting: "+firstLine(err.Error()), ToastKindError)
				}
			})
		}()
	case "gd-signout":
		PlayActivateSound()
		cloudConfirmSignOut(config)
	default:
		return false
	}
	return true
}

// cloudOpenSetupSheet says why this build cannot sign in.
func cloudOpenSetupSheet(config *Config) {
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Google Drive sign-in is not set up",
		subtitle: "This build has no Google OAuth client",
		icon:     "cloud",
		body: "Signing in to Google Drive needs an OAuth client that belongs to whoever builds JukaHub. This build was made without one.\n\n" +
			"## To set it up\n" +
			"• Create a \"TVs and Limited Input devices\" OAuth client in Google Cloud Console, with the Google Drive API on and the drive.file scope (docs/CLOUD_SYNC.md walks through it)\n" +
			"• Put its ID and secret in the config as google_client_id and google_client_secret, or build with JUKAHUB_GOOGLE_CLIENT_ID and JUKAHUB_GOOGLE_CLIENT_SECRET set\n\n" +
			"Sync with your paired devices and the encrypted export work without it.",
		qr:      tbxQRModules(cloudDocURL),
		qrLabel: "CLOUD_SYNC.md",
		buttons: []tbxSheetButton{{label: "Close", primary: true}},
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

// ── Sign-in ─────────────────────────────────────────────────────────────────

// gdriveStartSignIn asks Google for a code, shows it, and waits for the
// phone. Main thread.
func gdriveStartSignIn(config *Config) {
	if gdriveSignIn.active {
		return
	}
	client := gdriveClientFrom(config)
	if client.ID == "" {
		PlayActivateSound()
		cloudOpenSetupSheet(config)
		return
	}
	if !IsOnline() {
		ShowToast("Connect to Wi-Fi first — signing in needs the internet", ToastKindWarning)
		return
	}
	PlayActivateSound()
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
		cctx, ccancel := context.WithTimeout(ctx, 30*time.Second)
		dc, err := gdriveRequestDeviceCode(cctx, client)
		ccancel()
		if err != nil {
			tbxPost(func() {
				if current() {
					gdriveSignInEnded(err)
				}
			})
			return
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
			if peerCurrentScene(appConfig) == ssyncSceneName {
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
			ssyncScreen.cmpRows, ssyncScreen.cmpSummary = ssyncBuildCompareRows(&cmp)
			ssyncScreen.mode = ssyncModeCompare
			ssyncScreen.nav.focus, ssyncScreen.nav.scroll = "run", 0
			PlaySuccessSound()
		})
	}()
}

// gdriveStartSync carries out the Drive comparison on screen (ssyncStartSync
// hands it over).
func gdriveStartSync(job ssyncComparison) {
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
		tbxPost(func() {
			ssyncWorkEnd()
			ssyncScreen.cmp = nil
			ssyncScreen.cmpRows = nil
			ssyncScreen.mode = ssyncModeHome
			ssyncScreen.nav.focus, ssyncScreen.nav.scroll = "", 0
			ssyncScreen.model.builtAt = time.Time{}
			if err != nil {
				return
			}
			ssyncScreen.resultLine = "Last sync with Google Drive: " + ssyncResultText(res)
			if res.Pulled+res.Pushed > 0 {
				trophyEvent(tkSaveBackup)
			}
			kind := ToastKindSuccess
			if res.Failed > 0 {
				kind = ToastKindWarning
			}
			msg := "Saves synced with Google Drive · " + ssyncResultText(res)
			if res.Failed > 0 && len(res.Errors) > 0 {
				msg += " — " + res.Errors[0]
			}
			ShowToast(msg, kind)
		})
	}()
}
