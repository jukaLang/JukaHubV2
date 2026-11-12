package main

// apkstore_render.go — drawing the APK Store (apkstore.go holds its state and
// input). The page is the tool screens' frame: the shared header with a
// read-out, a tab strip that is the first row of the focus list, then the tab:
// glass rows with the APK's own icon, its verdict and a status pill.

import (
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	apkStoreTabH   = int32(46)
	apkStoreRowGap = int32(8)
)

// apkStoreTruncatedNote ends a list the scan's bounds cut short.
var apkStoreTruncatedNote = "The scan stopped early (it reads at most " + strconv.Itoa(apkStoreScanMax) +
	" APKs, two folders deep, for a few seconds) — keep the ones you want in /mnt/SDCARD/APKs."

// apkStoreTabIDs are the tab strip's focus ids.
var apkStoreTabIDs = [apkTabCount]string{"tab0", "tab1", "tab2"}

// apkStoreHeader is the page bar.
func apkStoreHeader() UIPageHeader {
	return tbxHeader(apkStoreEntryName, "Install native Android games — Penguin Dash played on Smart Pro",
		apkStoreGlyph, apkStoreUI.status, ColorTextTertiary())
}

// apkStoreVisible reports whether a row at screen y with height h shows.
func apkStoreVisible(y, h int32, body sdl.Rect) bool {
	return y+h >= body.Y && y <= body.Y+body.H
}

// apkStoreRender draws the screen.
func apkStoreRender(renderer *sdl.Renderer, config *Config) {
	if apkStoreUI.visit.begin() {
		apkStoreEnter()
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, apkStoreHeader())
	body := frame.bodyRect()
	nav := &apkStoreUI.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	y := apkStoreDrawTabs(renderer, config, fonts, frame, top)
	switch apkStoreUI.tab {
	case apkTabStore:
		apkStoreDrawStore(renderer, config, fonts, frame, body, top, y)
	case apkTabRuntime:
		apkStoreDrawRuntime(renderer, config, fonts, frame, body, top, y)
	default:
		apkStoreDrawMine(renderer, config, fonts, frame, body, top, y)
	}
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// apkStoreDrawTabs draws the tab strip and Rescan as row 0 and returns the
// body y below it.
func apkStoreDrawTabs(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top int32) int32 {
	nav := &apkStoreUI.nav
	x := frame.x
	for i := 0; i < apkTabCount; i++ {
		label := apkStoreUI.tabLabels[i]
		if label == "" {
			label = apkStoreTabNames[i]
		}
		w := tbxButtonWidth(fonts.body, label, 132)
		id := apkStoreTabIDs[i]
		rect := sdl.Rect{X: x, Y: 0, W: w, H: apkStoreTabH}
		screen := rect
		screen.Y += top
		tbxDrawButton(renderer, config, fonts.body, screen, label, i == apkStoreUI.tab, nav.focused(id), false)
		nav.add(id, rect, 0, false)
		x += w + 10
	}
	label := "Rescan"
	if apkStoreUI.scanning {
		label = "Reading…"
	}
	w := tbxButtonWidth(fonts.body, label, 120)
	if right := frame.x + frame.w - w; right > x {
		rect := sdl.Rect{X: right, Y: 0, W: w, H: apkStoreTabH}
		screen := rect
		screen.Y += top
		tbxDrawButton(renderer, config, fonts.body, screen, label, false, nav.focused("rescan"), apkStoreUI.scanning)
		nav.add("rescan", rect, 0, apkStoreUI.scanning)
	}
	nav.extend(apkStoreTabH)
	return apkStoreTabH + 18
}

// apkStoreDrawNote draws one quiet line at body y and returns the y below it.
func apkStoreDrawNote(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, body sdl.Rect, top, y int32, text string) int32 {
	if apkStoreVisible(top+y, fonts.smallH, body) {
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, text, frame.w), ColorTextTertiary(), frame.x, top+y)
	}
	apkStoreUI.nav.extend(y + fonts.smallH)
	return y + fonts.smallH + 12
}

// ── Your APKs ───────────────────────────────────────────────────────────────

func apkStoreDrawMine(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, body sdl.Rect, top, y int32) {
	nav := &apkStoreUI.nav
	if apkStoreUI.scanning {
		done, total := apkStoreLiveGet()
		text := "Reading the card…"
		if total > 0 {
			text = "Reading APK " + strconv.Itoa(done) + " of " + strconv.Itoa(total) + "…"
		}
		if !apkStoreUI.scanned {
			rect := sdl.Rect{X: frame.x, Y: top + y, W: frame.w, H: 200}
			tbxDrawEmpty(renderer, config, rect, "scan", text, "Looking in "+apkStoreUI.rootsShort+".")
			nav.extend(y + 200)
			return
		}
		y = apkStoreDrawNote(renderer, config, fonts, frame, body, top, y, text)
	}
	if len(apkStoreUI.apks) == 0 {
		rect := sdl.Rect{X: frame.x, Y: top + y, W: frame.w, H: 220}
		tbxDrawEmpty(renderer, config, rect, apkStoreGlyph, "No APKs found",
			"Copy APKs you own into /mnt/SDCARD/APKs, or send one from your phone with the Phone Companion (it lands in "+
				companionDropDir+"). Searched: "+apkStoreUI.rootsLong+".")
		nav.extend(y + 220)
		return
	}
	y = apkStoreDrawNote(renderer, config, fonts, frame, body, top, y,
		"Experimental: AndroidRT runs native (NDK) games only. Penguin Dash has raced on a Smart Pro.")
	rowH := tbxRowHeight(fonts)
	for i, info := range apkStoreUI.apks {
		id := apkStoreRowID("apk", i)
		rect := sdl.Rect{X: frame.x, Y: y, W: frame.w, H: rowH}
		if apkStoreVisible(top+y, rowH, body) {
			screen := rect
			screen.Y += top
			sub := info.Verdict
			if i < len(apkStoreUI.subs) {
				sub = apkStoreUI.subs[i]
			}
			apkStoreDrawAPKRow(renderer, config, fonts, screen, info, sub, nav.focused(id))
		}
		nav.add(id, rect, 1+i, false)
		y += rowH + apkStoreRowGap
	}
	if apkStoreUI.truncated {
		y += 4
		y = apkStoreDrawNote(renderer, config, fonts, frame, body, top, y, apkStoreTruncatedNote)
	}
	nav.extend(y)
}

// apkStoreRootsLine names the first n searched folders (or the planned ones
// before the first scan), for the empty and working states.
func apkStoreRootsLine(n int) string {
	roots := apkStoreUI.searched
	if len(roots) == 0 {
		roots = apkStoreUI.roots
	}
	if len(roots) == 0 {
		return "the card"
	}
	more := ""
	if len(roots) > n {
		more = " and " + strconv.Itoa(len(roots)-n) + " more"
		roots = roots[:n]
	}
	return strings.Join(roots, ", ") + more
}

// apkStoreRowPill is the row's status: work in flight, the profile, or the
// verdict.
func apkStoreRowPill(info apkStoreInfo) (string, tbxTone) {
	if busy := apkStoreUI.busy[info.Path]; busy != "" {
		return busy, tbxToneAccent
	}
	if st, ok := apkStoreUI.games[info.Path]; ok {
		switch {
		case !info.Tier.canAdd():
			// A hand-written profile for an APK the verdict says cannot run:
			// being in Games does not make it green.
			return "In Games · won't run", tbxToneBad
		case st.Issue != "":
			return "In Games · can't start", tbxToneWarn
		}
		return "In Games", tbxToneGood
	}
	return info.Tier.pill(), info.Tier.tone()
}

// apkStoreRowSubtitle is the line under the name: the verdict, then the
// version.
func apkStoreRowSubtitle(info apkStoreInfo) string {
	parts := []string{info.Verdict}
	if v := strings.TrimSpace(info.Manifest.VersionName); v != "" {
		parts = append(parts, v)
	}
	if info.Engine != "" && info.Engine != apkEngineNativeActivity && info.Engine != apkEngineGameActivity &&
		!strings.Contains(info.Why, info.Engine) {
		parts = append(parts, info.Engine)
	}
	return strings.Join(parts, " · ")
}

// apkStoreDrawAPKRow draws one APK: its icon (or the package glyph), the name
// over the verdict, the size and the pill.
func apkStoreDrawAPKRow(renderer *sdl.Renderer, config *Config, fonts tbxFonts, rect sdl.Rect, info apkStoreInfo, sub string, focused bool) {
	UIListRow(renderer, rect, glassRowRadius, focused, false)
	x := rect.X + 14
	badge := rect.H - 24
	if badge > 44 {
		badge = 44
	}
	if badge < 28 {
		badge = 28
	}
	box := sdl.Rect{X: x, Y: rect.Y + (rect.H-badge)/2, W: badge, H: badge}
	if tex := apkStoreIconTexture(renderer, info); tex != nil {
		_ = renderer.Copy(tex, nil, &box)
	} else {
		inner := drawGlassBadge(renderer, box)
		DrawHomeIcon(renderer, "package", inner, focused)
	}
	x += badge + 14
	right := rect.X + rect.W - 16
	cy := rect.Y + rect.H/2
	pill, tone := apkStoreRowPill(info)
	p := tbxDrawPill(renderer, config, fonts.tiny, right, cy, pill, tone)
	right = p.X - 12
	if info.Size > 0 {
		size := humanBytes(info.Size)
		if sw, sh := measureText(fonts.small, size); sw < right-x-160 {
			renderText(renderer, config, fonts.small, size, ColorTextTertiary(), right-sw, cy-sh/2)
			right -= sw + 14
		}
	}
	textW := right - x
	if textW < 40 {
		textW = 40
	}
	block := fonts.titleH + 4 + fonts.smallH
	ty := rect.Y + (rect.H-block)/2
	titleInk, subInk := ColorTextPrimary(), ColorTextMuted()
	if info.Tier == apkTierBroken {
		titleInk, subInk = ColorTextTertiary(), ColorTextTertiary()
	}
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, info.Label, textW), titleInk, x, ty)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, sub, textW), subInk, x, ty+fonts.titleH+4)
}

// ── Store ───────────────────────────────────────────────────────────────────

func apkStoreDrawStore(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, body sdl.Rect, top, y int32) {
	nav := &apkStoreUI.nav
	entries := apkStoreUI.catalog.Entries
	y = apkStoreDrawNote(renderer, config, fonts, frame, body, top, y, apkStoreUI.catalogNote)
	if len(entries) == 0 {
		rect := sdl.Rect{X: frame.x, Y: top + y, W: frame.w, H: 200}
		tbxDrawEmpty(renderer, config, rect, apkStoreGlyph, "The catalogue is empty", "It could not be read; Your APKs still works.")
		nav.extend(y + 200)
		return
	}
	rowH := tbxRowHeight(fonts)
	for i, e := range entries {
		id := apkStoreRowID("cat", i)
		rect := sdl.Rect{X: frame.x, Y: y, W: frame.w, H: rowH}
		if apkStoreVisible(top+y, rowH, body) {
			screen := rect
			screen.Y += top
			pill, tone := apkStoreCatalogStatusLabel(e.Status)
			if _, installed := apkStoreCatalogInstalled(e); installed {
				pill, tone = "In Games", tbxToneGood
			} else if e.installable() && androidRTSupported() {
				pill, tone = "Install", tbxToneAccent
			} else if e.downloadable() {
				pill, tone = "Download", tbxToneAccent
			}
			if busy := apkStoreUI.busy[e.ID]; busy != "" {
				pill, tone = busy, tbxToneAccent
			}
			icon := "close"
			switch e.Status {
			case apkStatusWorks:
				icon = "check"
			case apkStatusExperimental:
				icon = "warning"
			}
			spec := tbxRowSpec{icon: icon, title: e.Title, subtitle: e.Summary, pill: pill, tone: tone}
			tbxDrawRow(renderer, config, fonts, screen, spec, nav.focused(id))
		}
		nav.add(id, rect, 1+i, false)
		y += rowH + apkStoreRowGap
	}
	nav.extend(y)
}

// ── Runtime ─────────────────────────────────────────────────────────────────

// apkStoreRuntimeRows are the Runtime tab's rows for the current probe.
func apkStoreRuntimeRows() []apkStoreRow {
	rt, wd := apkStoreUI.runtime, apkStoreUI.waydroid
	checking := !apkStoreUI.probed
	rtSpec := tbxRowSpec{icon: "chip", title: "AndroidRT"}
	installLine, installing := androidRTInstallLine()
	newer, newerTag := androidRTUpdateKnown()
	switch {
	case installing:
		rtSpec.subtitle, rtSpec.pill, rtSpec.tone = installLine, "Installing…", tbxToneAccent
	case checking:
		rtSpec.subtitle, rtSpec.pill, rtSpec.tone = "Checking…", "…", tbxToneNeutral
	case rt.dir != "" && rt.err == "" && rt.source == androidRTSourceDownloaded && newer:
		rtSpec.subtitle, rtSpec.pill, rtSpec.tone = apkStoreRuntimeWhere(rt)+" · "+newerTag+" is out — A updates it",
			"Update", tbxToneAccent
	case rt.dir != "" && rt.err == "":
		rtSpec.subtitle, rtSpec.pill, rtSpec.tone = apkStoreRuntimeWhere(rt), "Ready", tbxToneGood
	case rt.dir != "":
		rtSpec.subtitle, rtSpec.pill, rtSpec.tone = "Found at "+rt.dir+", but "+rt.err, "Incomplete", tbxToneWarn
	case rt.canRun:
		rtSpec.subtitle, rtSpec.pill, rtSpec.tone = "Not installed — A installs it from JukaHub's own releases (a few MB)",
			"Missing", tbxToneBad
	default:
		rtSpec.subtitle, rtSpec.pill, rtSpec.tone = "Runs on the handheld (arm64 Linux), not on this machine",
			"Not here", tbxToneNeutral
	}
	devSpec := tbxRowSpec{icon: "hardware", title: "This device"}
	devSub := rt.host
	if rt.memMB > 0 {
		devSub += " · " + strconv.FormatInt(rt.memMB, 10) + " MB of RAM"
	}
	if rt.canRun {
		devSpec.pill, devSpec.tone = "Can run", tbxToneGood
	} else {
		devSub += " — Android games run on the handheld"
		devSpec.pill, devSpec.tone = "Checks only", tbxToneNeutral
	}
	devSpec.subtitle = devSub
	wdSpec := tbxRowSpec{icon: "devices", title: "Android container (Waydroid)"}
	switch {
	case checking:
		wdSpec.subtitle, wdSpec.pill = "Checking…", "…"
	case wd.usable():
		state := "session stopped"
		if wd.running {
			state = "session running"
		}
		wdSpec.subtitle, wdSpec.pill, wdSpec.tone = "Installed · "+state+" · can install any APK", "Available", tbxToneGood
	default:
		wdSpec.subtitle, wdSpec.pill, wdSpec.tone = wd.reason, "Not here", tbxToneNeutral
	}
	return []apkStoreRow{
		{"rt-androidrt", rtSpec},
		{"rt-device", devSpec},
		{"rt-waydroid", wdSpec},
		{"rt-profiles", tbxRowSpec{icon: "controller", title: "Game Profiles",
			subtitle: "Added games are here and on the Games Library's Android row"}},
		{"rt-about", tbxRowSpec{icon: "info", title: "What AndroidRT can run",
			subtitle: "Native games, experimentally; Java apps and most engines' games, no — and why"}},
	}
}

// apkStoreRuntimeWhere says which copy of AndroidRT is in use, in a few words.
func apkStoreRuntimeWhere(rt apkStoreRuntime) string {
	switch rt.source {
	case androidRTSourceBundled:
		return "Bundled with JukaHub · updates with JukaHub"
	case androidRTSourceDownloaded:
		if rt.tag != "" {
			return "Installed from JukaHub's releases (" + rt.tag + ")"
		}
		return "Installed from JukaHub's releases"
	case androidRTSourceUser:
		return "Your own copy at " + rt.dir
	case androidRTSourceOverride:
		return "JUKAHUB_ANDROIDRT: " + rt.dir
	}
	return "Installed at " + rt.dir
}

func apkStoreDrawRuntime(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, body sdl.Rect, top, y int32) {
	nav := &apkStoreUI.nav
	rowH := tbxRowHeight(fonts)
	for i, row := range apkStoreUI.rtRows {
		rect := sdl.Rect{X: frame.x, Y: y, W: frame.w, H: rowH}
		if apkStoreVisible(top+y, rowH, body) {
			screen := rect
			screen.Y += top
			tbxDrawRow(renderer, config, fonts, screen, row.spec, nav.focused(row.id))
		}
		nav.add(row.id, rect, 1+i, false)
		y += rowH + apkStoreRowGap
	}
	nav.extend(y)
}

// apkStoreRowIDs caches row ids ("apk0", "cat3"), so a frame does not format
// one per row.
var apkStoreRowIDs = map[string][]string{}

// apkStoreRowID returns the focus id of row i in the list named prefix.
func apkStoreRowID(prefix string, i int) string {
	ids := apkStoreRowIDs[prefix]
	for len(ids) <= i {
		ids = append(ids, prefix+strconv.Itoa(len(ids)))
	}
	apkStoreRowIDs[prefix] = ids
	return ids[i]
}
