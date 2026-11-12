package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	fdroidPageSize                    = 40
	fdroidSearchVKTarget vkTargetKind = 68
)

func apkStoreStartFDroid(force bool) {
	if apkStoreUI.fdroidLoading || !apkStoreScanEnabled {
		return
	}
	apkStoreUI.fdroidLoading = true
	apkStoreUI.fdroidError = ""
	apkStoreUI.fdroidGen++
	gen := apkStoreUI.fdroidGen
	started := runSceneTask(func(ctx context.Context) {
		if saved, err := fdroidReadCache(); err == nil {
			tbxPost(func() {
				if gen == apkStoreUI.fdroidGen {
					apkStoreUI.fdroid, apkStoreUI.fdroidReady = saved, true
					apkStoreRebuildText()
				}
			})
		}
		if !IsOnline() {
			tbxPost(func() {
				if gen == apkStoreUI.fdroidGen {
					apkStoreUI.fdroidLoading = false
					apkStoreUI.fdroidError = "Connect to Wi-Fi and press Refresh to read F-Droid."
					if apkStoreUI.fdroidReady {
						apkStoreUI.fdroidError = "Showing saved F-Droid list · connect to Wi-Fi to refresh."
					}
				}
			})
			return
		}
		list, err := fdroidRefresh(ctx, force)
		tbxPost(func() {
			if gen != apkStoreUI.fdroidGen {
				return
			}
			apkStoreUI.fdroidLoading = false
			if len(list.Apps) > 0 {
				apkStoreUI.fdroid, apkStoreUI.fdroidReady = list, true
			}
			if err != nil {
				prefix := "Refresh failed: "
				if apkStoreUI.fdroidReady {
					prefix = "Showing saved list · refresh failed: "
				}
				apkStoreUI.fdroidError = prefix + err.Error()
			} else {
				apkStoreUI.fdroidError = ""
			}
			apkStoreRebuildText()
		})
	})
	if !started {
		apkStoreUI.fdroidLoading = false
		apkStoreUI.fdroidError = "Could not start the F-Droid reader."
	}
}

func apkStoreFDroidSetQuery(query string) {
	apkStoreUI.fdroidQuery = query
	apkStoreUI.fdroidPage = 0
	apkStoreUI.nav.scroll = 0
}

func apkStoreFDroidSearchKeyboard() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = fdroidSearchVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

func apkStoreFDroidKeyboardDone() {
	if len(apkStoreFDroidMatches()) > 0 {
		apkStoreUI.nav.focus = "fd0"
	}
	apkStoreUI.nav.scroll = 0
}

func apkStoreFDroidMatches() []int {
	ui := &apkStoreUI
	if ui.fdroidMatchQuery == ui.fdroidQuery && ui.fdroidMatchSHA == ui.fdroid.IndexSHA &&
		ui.fdroidMatchGamesOnly == ui.fdroidGamesOnly && ui.fdroidMatches != nil {
		return ui.fdroidMatches
	}
	query := strings.ToLower(strings.TrimSpace(apkStoreUI.fdroidQuery))
	matches := make([]int, 0, len(apkStoreUI.fdroid.Apps))
	for i, app := range apkStoreUI.fdroid.Apps {
		if apkStoreUI.fdroidGamesOnly && !app.isGame() {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(app.Name), query) &&
			!strings.Contains(strings.ToLower(app.Package), query) &&
			!strings.Contains(strings.ToLower(app.Summary), query) {
			continue
		}
		matches = append(matches, i)
	}
	ui.fdroidMatches, ui.fdroidMatchQuery, ui.fdroidMatchSHA, ui.fdroidMatchGamesOnly =
		matches, ui.fdroidQuery, ui.fdroid.IndexSHA, ui.fdroidGamesOnly
	return matches
}

func apkStoreFDroidPage() []fdroidApp {
	matches := apkStoreFDroidMatches()
	start := apkStoreUI.fdroidPage * fdroidPageSize
	if start >= len(matches) {
		start = 0
	}
	end := start + fdroidPageSize
	if end > len(matches) {
		end = len(matches)
	}
	page := make([]fdroidApp, 0, end-start)
	for _, i := range matches[start:end] {
		page = append(page, apkStoreUI.fdroid.Apps[i])
	}
	return page
}

func apkStoreFDroidLocal(app fdroidApp) apkStoreCatalogLocalMatch {
	return apkStoreCatalogMatchOnCard(apkStoreCatalogEntry{Package: app.Package, Kind: "title"},
		apkStoreUI.apks, apkStoreUI.games)
}

func apkStoreDrawFDroid(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, body sdl.Rect, top, y int32) {
	nav := &apkStoreUI.nav
	note := "Official F-Droid apps · APKs are checked before saving · AndroidRT can add native ARM64 apps only"
	if apkStoreUI.fdroidLoading {
		note = "Reading F-Droid's signed app list… first download may take a while"
	}
	if apkStoreUI.fdroidError != "" {
		note = apkStoreUI.fdroidError
	}
	y = apkStoreDrawNote(renderer, config, fonts, frame, body, top, y, note)
	buttons := []struct {
		id, label string
		disabled  bool
	}{
		{"fd-search", "Search: " + apkStoreFDroidSearchLabel(), false},
		{"fd-filter", apkStoreFDroidFilterLabel(), false},
	}
	matches := apkStoreFDroidMatches()
	pages := (len(matches) + fdroidPageSize - 1) / fdroidPageSize
	if pages < 1 {
		pages = 1
	}
	page := apkStoreUI.fdroidPage
	if page >= pages {
		page = 0
		apkStoreUI.fdroidPage = 0
	}
	buttons = append(buttons,
		struct {
			id, label string
			disabled  bool
		}{"fd-prev", "Previous", page == 0},
		struct {
			id, label string
			disabled  bool
		}{"fd-next", "Next", page+1 >= pages})
	x := frame.x
	for _, button := range buttons {
		w := tbxButtonWidth(fonts.body, button.label, 130)
		if button.id == "fd-search" && w < 240 {
			w = 240
		}
		rect := sdl.Rect{X: x, Y: y, W: w, H: apkStoreTabH}
		screen := rect
		screen.Y += top
		if apkStoreVisible(screen.Y, rect.H, body) {
			tbxDrawButton(renderer, config, fonts.body, screen, button.label, false, nav.focused(button.id), button.disabled)
		}
		nav.add(button.id, rect, 1, button.disabled)
		x += w + 10
	}
	y += apkStoreTabH + 18
	countLine := fmt.Sprintf("%d apps · page %d of %d", len(matches), page+1, pages)
	if apkStoreUI.fdroidReady {
		countLine += " · list " + time.UnixMilli(apkStoreUI.fdroid.Timestamp).Format("Jan 2, 2006")
	}
	y = apkStoreDrawNote(renderer, config, fonts, frame, body, top, y, countLine)
	if len(matches) == 0 {
		msg := "No F-Droid apps match this search."
		if !apkStoreUI.fdroidReady {
			msg = "The F-Droid list is not ready yet."
		}
		rect := sdl.Rect{X: frame.x, Y: top + y, W: frame.w, H: 180}
		if apkStoreVisible(rect.Y, rect.H, body) {
			tbxDrawEmpty(renderer, config, rect, apkStoreGlyph, "F-Droid", msg)
		}
		nav.extend(y + 180)
		return
	}
	rowH := tbxRowHeight(fonts)
	for i, app := range apkStoreFDroidPage() {
		id := apkStoreRowID("fd", i)
		rect := sdl.Rect{X: frame.x, Y: y, W: frame.w, H: rowH}
		if apkStoreVisible(top+y, rowH, body) {
			screen := rect
			screen.Y += top
			pill, tone := "Download", tbxToneAccent
			local := apkStoreFDroidLocal(app)
			switch {
			case local.Found && local.InGames:
				pill, tone = "In Games", tbxToneGood
			case local.Found:
				pill, tone = "On card", tbxToneAccent
			case !app.hasTrimuiABI():
				pill, tone = "Wrong CPU", tbxToneBad
			case !app.canDownload():
				pill, tone = "Too large", tbxToneNeutral
			}
			if busy := apkStoreUI.busy[app.asCatalogEntry().ID]; busy != "" {
				pill, tone = busy, tbxToneAccent
			}
			tbxDrawRow(renderer, config, fonts, screen, tbxRowSpec{
				icon: apkStoreGlyph, title: app.Name, subtitle: app.Summary, pill: pill, tone: tone,
			}, nav.focused(id))
		}
		nav.add(id, rect, 2+i, false)
		y += rowH + apkStoreRowGap
	}
	nav.extend(y)
}

func apkStoreFDroidSearchLabel() string {
	if s := strings.TrimSpace(apkStoreUI.fdroidQuery); s != "" {
		if len([]rune(s)) > 20 {
			s = string([]rune(s)[:19]) + "…"
		}
		return s
	}
	return "All apps"
}

func apkStoreFDroidFilterLabel() string {
	if apkStoreUI.fdroidGamesOnly {
		return "Games only"
	}
	return "All categories"
}

func apkStoreOpenFDroidEntry(config *Config, app fdroidApp) {
	local := apkStoreFDroidLocal(app)
	var b strings.Builder
	b.WriteString(app.Summary)
	b.WriteString("\n\nF-Droid · " + app.Package + " · version " + app.Version +
		" (" + strconv.FormatInt(app.VersionCode, 10) + ") · " + humanBytes(app.Size))
	if app.Channel != "" {
		b.WriteString(" · " + app.Channel)
	}
	if len(app.Categories) > 0 {
		b.WriteString("\nCategories: " + strings.Join(app.Categories, ", "))
	}
	b.WriteString("\n\nJukaHub checks the downloaded APK against F-Droid's signed list, then inspects its actual launcher. An eligible native ARM64 app is added to Games automatically. Java apps stay in Your APKs, but need Android to run.")
	if app.Package == "org.fdroid.fdroid" {
		b.WriteString("\n\nThis is F-Droid's Android client APK. It needs Android's Java runtime and package manager; this JukaHub tab is the F-Droid browser that works on stock TrimUI.")
	}
	buttons := []tbxSheetButton{}
	if local.Found {
		b.WriteString("\n\nOn your card: " + filepath.Base(local.Info.Path) + " — " + local.Info.Verdict)
		buttons = append(buttons, tbxSheetButton{label: "Open local APK", run: func() { apkStoreOpenAPK(config, local.Info) }})
		if local.Info.Tier.canAdd() && !local.InGames {
			buttons = append(buttons, tbxSheetButton{label: "Add local APK", primary: true,
				run: func() { apkStoreAddOrOffer(config, local.Info) }})
		}
	}
	switch {
	case !app.hasTrimuiABI():
		b.WriteString("\n\nThis build has no ARM64 code for the TrimUI.")
	case !app.canDownload():
		b.WriteString("\n\nThis APK is larger than JukaHub's download limit.")
	default:
		buttons = append(buttons, tbxSheetButton{label: "Download & check", primary: !local.Found,
			run: func() { apkStoreDownloadFDroid(app) }})
	}
	focus := 0
	for i, button := range buttons {
		if button.primary {
			focus = i
		}
	}
	tbxOpenSheet(config, tbxSheetSpec{title: app.Name, subtitle: "F-Droid app", icon: apkStoreGlyph,
		body: b.String(), buttons: buttons, focus: focus})
	PlayActivateSound()
}

func apkStoreDownloadFDroid(app fdroidApp) {
	if !app.canDownload() || !app.hasTrimuiABI() {
		return
	}
	e := app.asCatalogEntry()
	if apkStoreUI.busy[e.ID] != "" {
		return
	}
	if !IsOnline() {
		ShowToast("Downloading needs internet", ToastKindWarning)
		return
	}
	apkStoreSetBusy(e.ID, "Downloading…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), patchDownloadTimeout)
		defer cancel()
		info, profile, added, err := fdroidDownloadAndCheck(ctx, app)
		tbxPost(func() {
			apkStoreSetBusy(e.ID, "")
			if info.Path != "" && apkStoreOnScreen() {
				apkStoreStartScan()
			}
			if err != nil {
				PlayErrorSound()
				ShowToast("F-Droid download failed: "+err.Error(), ToastKindError)
				return
			}
			PlaySuccessSound()
			if added {
				ShowToast("Added "+profile.Title+" to Games — launch is experimental", ToastKindSuccess)
				if apkStoreRuntimeMissing() {
					androidRTStartInstall(nil)
				}
			} else if info.Path != "" {
				ShowToast("Saved "+app.Name+" in Your APKs — "+info.Verdict, ToastKindInfo)
			} else {
				ShowToast("Saved "+app.Name+" in Your APKs", ToastKindSuccess)
			}
		})
	}()
}
