package main

// appcenter_ui.go — the App Center page (Apps → App Center, Home search),
// drawn like the other tool screens (tbxui.go).
//
// Top to bottom: the catalogue apps on this card (installed by the App
// Center or by hand), the catalogue apps that are not, and every other app
// folder on the card. Each catalogue row says what it is, how it fares on
// THIS handheld's firmware ("Works here", "Needs CrossMix", "Untested
// here") and where it stands: Installed <version>, Update <version>, Get, or
// how to get it when there is no download. A opens the sheet: the
// description, the per-firmware table, the dependencies checked live, what
// it opens and how the file is passed, where JukaHub offers it, the source
// (as text and a phone QR code) and Get / Update / Open / Roll back /
// Remove. X starts an installed app straight from the list; Y (or the "Look
// for new releases" row, for a mouse) asks GitHub again, at most once a
// minute.
//
// Without a network the header says "Offline · browsing", the list still
// browses and Get says it needs one; on a PC, NextUI or spruceOS the page
// browses only, and Get and Open say where they work.

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const acSceneName = "AppCenter"

// acCtlModeScene is Controller Mode's page (ctlmode*.go, another screen's):
// named here, not imported, so the App Center does not depend on its files.
// Its button only shows when that scene exists.
const acCtlModeScene = "ControllerMode"

// acRow is one row of the page.
type acRow struct {
	id     string // "app:<id>", "other:<folder>" or acCheckRowID
	appID  string
	folder string
	spec   tbxRowSpec
	// in is the installed copy the row shows (hasIn), kept with the row so
	// the hints, drawn every frame, and X never ask the scan.
	in    acInstalled
	hasIn bool
}

// acCheckRowID is the row that looks for new releases (Y's twin for a mouse).
const acCheckRowID = "action:check"

// acCheckEvery is how soon Y may ask GitHub again: unsigned, it allows 60
// calls an hour, and each check asks once per downloadable entry.
const acCheckEvery = 60 * time.Second

type acScreenState struct {
	visit     tbxVisit
	nav       tbxNav
	builtAt   time.Time
	header    UIPageHeader
	installed []acRow
	available []acRow
	other     []acRow
	actions   []acRow
	// pending is a catalogue app whose sheet opens on the next frame
	// (acOpenCenter); fromRecordings the recorder the Recordings page's
	// button means (acRecordingsPlace), used once.
	pending        string
	fromRecordings string
	token          int
	// checkedAt is when Y (or the check row) last asked GitHub.
	checkedAt time.Time
}

var acScreen acScreenState

type acScene struct{ sceneBase }

func (acScene) render(r *sdl.Renderer, c *Config) bool {
	if acScreen.visit.begin() {
		acScreen.nav.scroll = 0
		acScreen.builtAt = time.Time{}
		if acScreen.pending == "" && acScreen.fromRecordings != "" && acCameFrom(c, "Recordings") {
			acScreen.pending = acScreen.fromRecordings
			// Once: the Recordings page sets it again when it lists its
			// folder, and a later visit from anywhere else opens no sheet.
			acScreen.fromRecordings = ""
		}
		acLookupLatest(func() { acScreen.builtAt = time.Time{} })
	}
	acRenderScreen(r, c)
	return true
}

func (acScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), acSceneAction)
}

func (acScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), acSceneAction)
}

func init() { registerScene(acScene{sceneBase{name: acSceneName}}) }

// acCameFrom reports whether the scene before this one was name.
func acCameFrom(config *Config, name string) bool {
	if config == nil || len(sceneHistory) == 0 {
		return false
	}
	idx := sceneHistory[len(sceneHistory)-1]
	return idx >= 0 && idx < len(config.Scenes) && config.Scenes[idx].Name == name
}

// acPlaceIcon is a row's glyph, from what the app is for.
func acPlaceIcon(a *acApp) string {
	switch {
	case a == nil:
		return "apps"
	case a.hasPlace("books"):
		return "files"
	case a.hasPlace("recordings"):
		return "camera"
	case a.hasPlace("device"):
		return "gamepad"
	}
	return "share"
}

// acStatusPill is a catalogue row's pill: what the App Center can do with it.
func acStatusPill(a *acApp, in acInstalled, installed bool) (string, tbxTone) {
	busy := acBusyID()
	if busy == a.ID || (installed && (busy == "remove:"+in.Folder || busy == "rollback:"+in.Folder)) {
		return "Working…", tbxToneAccent
	}
	latest, haveLatest := acLatestFor(a.ID)
	if installed {
		newer := in.HasMarker && haveLatest && latest.Tag != "" && latest.Tag != in.Marker.Tag
		switch {
		case newer && a.SelfUpdates:
			// It may have updated itself past the tag the App Center wrote:
			// a newer release exists, an update is not claimed.
			return "Newer release " + latest.Tag, tbxToneNeutral
		case newer:
			return "Update " + latest.Tag, tbxToneWarn
		case in.HasMarker && in.Marker.Tag != "" && !a.SelfUpdates:
			return "Installed " + in.Marker.Tag, tbxToneGood
		}
		return "Installed", tbxToneGood
	}
	switch {
	case a.Source.downloadable():
		return "Get", tbxToneAccent
	case a.Source.Bundled != "":
		return "Comes with " + acShortFirmware(a.Source.Bundled), tbxToneNeutral
	}
	return "Manual install", tbxToneNeutral
}

// acShortFirmware is "CrossMix" for "CrossMix-OS CE v1.4.0-ce.1".
func acShortFirmware(s string) string {
	if strings.Contains(strings.ToLower(s), "crossmix") {
		return "CrossMix"
	}
	return s
}

func acBuildModel(now time.Time) {
	m := &acScreen
	m.builtAt = now
	fw := acDetectFirmware()
	ink := ColorTextTertiary()
	status := acFirmwareName(fw)
	if acHandheld() {
		ink = ColorTextMuted()
	}
	online := IsOnline()
	if !online {
		// The list still browses; Get and the release check need a network.
		status = "Offline · browsing"
		if acHandheld() {
			status = acFirmwareName(fw) + " · Offline · browsing"
		}
	}
	m.header = tbxHeader("App Center", "TrimUI apps, each checked against its own source", "apps", status, ink)
	list := acInstalledNow()
	byID := map[string]acInstalled{}
	for _, in := range list {
		if in.AppID != "" {
			if _, dup := byID[in.AppID]; !dup {
				byID[in.AppID] = in
			}
		}
	}
	m.installed, m.available, m.other = m.installed[:0], m.available[:0], m.other[:0]
	for i := range acCatalogue {
		a := &acCatalogue[i]
		in, installed := byID[a.ID]
		chip, _ := a.chipHere()
		spec := tbxRowSpec{icon: acPlaceIcon(a), title: a.Name, subtitle: a.Summary, note: chip}
		spec.pill, spec.tone = acStatusPill(a, in, installed)
		row := acRow{id: "app:" + a.ID, appID: a.ID, spec: spec}
		if installed {
			row.folder = in.Folder
			row.in, row.hasIn = in, true
			if !in.HasMarker {
				row.spec.subtitle = "Installed by hand · " + a.Summary
			}
			m.installed = append(m.installed, row)
		} else {
			m.available = append(m.available, row)
		}
	}
	for _, in := range list {
		if in.AppID != "" {
			if first, ok := byID[in.AppID]; ok && first.Dir == in.Dir {
				continue
			}
		}
		sub := "Not in the App Center's list · Apps/" + in.Folder
		if len(in.Declared) > 0 {
			sub += " · opens " + strings.Join(acExtWords(in.Declared, 4), ", ")
		}
		m.other = append(m.other, acRow{id: "other:" + in.Folder, folder: in.Folder, in: in, hasIn: true,
			spec: tbxRowSpec{icon: "apps", title: in.Label, subtitle: sub, pill: "Open", tone: tbxToneNeutral}})
	}
	check := tbxRowSpec{icon: "update", title: "Look for new releases", subtitle: "Asks GitHub for each app the App Center can download (also Y)"}
	switch {
	case !online:
		check.subtitle, check.pill, check.tone = "Needs the internet", "Offline", tbxToneNeutral
	case !m.checkedAt.IsZero() && now.Sub(m.checkedAt) < acCheckEvery:
		check.pill, check.tone = "Just checked", tbxToneNeutral
	}
	m.actions = append(m.actions[:0], acRow{id: acCheckRowID, spec: check})
}

func acRenderScreen(renderer *sdl.Renderer, config *Config) {
	now := time.Now()
	if now.Sub(acScreen.builtAt) > time.Second {
		acBuildModel(now)
	}
	m := &acScreen
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, m.header)
	body := frame.bodyRect()
	nav := &m.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0
	rowH := tbxRowHeight(fonts)
	group := func(title, icon string, rows []acRow) {
		if len(rows) == 0 {
			return
		}
		if y > 0 {
			y += 14
		}
		tbxDrawSection(renderer, config, title, icon, x, top+y, w)
		y += tbxSectionHeight
		for _, r := range rows {
			rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
			screen := rect
			screen.Y += top
			tbxDrawRow(renderer, config, fonts, screen, r.spec, nav.focused(r.id))
			nav.add(r.id, rect, row, false)
			row++
			y += rowH + 8
		}
	}
	group("On this handheld", "check", m.installed)
	group("Available", "download", m.available)
	group("Other apps on the card", "apps", m.other)
	group("Releases", "update", m.actions)
	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
	if id := m.pending; id != "" {
		m.pending = ""
		if a := acCatalogueByID(id); a != nil {
			nav.setFocus("app:" + id)
			acOpenAppSheet(config, a)
		}
	}
}

// ── Input ────────────────────────────────────────────────────────────────

func acSceneAction(config *Config, act tbxAction) bool {
	nav := &acScreen.nav
	switch act {
	case tbxActConfirm:
		if nav.focus != "" {
			PlayActivateSound()
			acActivate(config, nav.focus)
		}
		return true
	case tbxActX:
		if in, ok := acFocusedInstalled(); ok {
			PlayActivateSound()
			acLaunch(in, "")
			return true
		}
		PlayErrorSound()
		return true
	case tbxActY:
		acCheckReleases(true)
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func acSceneClick(mx, my int32, config *Config) bool {
	it, ok := acScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	acScreen.nav.focus = it.id
	PlayActivateSound()
	acActivate(config, it.id)
	return true
}

func acSceneHints() []FooterHint {
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Details"}}
	if _, ok := acFocusedInstalled(); ok {
		hints = append(hints, FooterHint{Button: "X", Label: "Open"})
	}
	return append(hints, FooterHint{Button: "Y", Label: "Check for updates"}, FooterHint{Button: "B", Label: "Back"})
}

// acCheckReleases is Y and the "Look for new releases" row: GitHub is asked
// again at most once a minute (acCheckEvery); its answers, errors too, are
// otherwise kept for acLatestAge. sound is false where the press has played
// its own (a row's A or click).
func acCheckReleases(sound bool) {
	since := time.Since(acScreen.checkedAt)
	recent := !acScreen.checkedAt.IsZero() && since < acCheckEvery
	if sound && (recent || !IsOnline()) {
		PlayErrorSound()
	}
	if !IsOnline() {
		ShowToast(offlinePlainNotice, ToastKindWarning)
		return
	}
	if recent {
		wait := int((acCheckEvery - since + time.Second - 1) / time.Second)
		ShowToast("Checked a moment ago — GitHub allows only a few lookups an hour; again in "+strconv.Itoa(wait)+" s", ToastKindInfo)
		return
	}
	if sound {
		PlayActivateSound()
	}
	acScreen.checkedAt = time.Now()
	acScreen.builtAt = time.Time{}
	acLatestForget()
	acLookupLatest(func() { acScreen.builtAt = time.Time{} })
	ShowToast("Looking for new releases…", ToastKindInfo)
}

// acRowInstalled is the installed copy a row shows, from the model (no scan).
func acRowInstalled(id string) (acInstalled, bool) {
	for _, rows := range [][]acRow{acScreen.installed, acScreen.other} {
		for _, r := range rows {
			if r.id == id && r.hasIn {
				return r.in, true
			}
		}
	}
	return acInstalled{}, false
}

// acFocusedInstalled is the installed app under the focus. The hints ask it
// every frame: it reads the model built once a second, never the card.
func acFocusedInstalled() (acInstalled, bool) {
	return acRowInstalled(acScreen.nav.focus)
}

// acLatestForget makes the next lookup ask GitHub again.
func acLatestForget() {
	acLatestCache.mu.Lock()
	for id, l := range acLatestCache.byID {
		l.At = time.Time{}
		acLatestCache.byID[id] = l
	}
	acLatestCache.mu.Unlock()
}

func acActivate(config *Config, id string) {
	if id == acCheckRowID {
		acCheckReleases(false)
		return
	}
	kind, rest, ok := strings.Cut(id, ":")
	if !ok {
		return
	}
	switch kind {
	case "app":
		if a := acCatalogueByID(rest); a != nil {
			acOpenAppSheet(config, a)
		}
	case "other":
		if in, ok := acRowInstalled(id); ok {
			acOpenOtherSheet(config, in)
		}
	}
}

// ── Sheets ───────────────────────────────────────────────────────────────

// acPlaceWords says where JukaHub offers an app.
func acPlaceWords(a *acApp) []string {
	var out []string
	for _, p := range a.Places {
		switch p {
		case "books":
			out = append(out, "Files → Open with, for books, comics and documents")
		case "recordings":
			out = append(out, "Recordings → Record the screen")
		case "device":
			out = append(out, "Controller Mode lists it beside its own links")
		}
	}
	return out
}

// acSheetBody is a catalogue app's sheet; deps is the dependency section
// ("" while it is being checked).
func acSheetBody(a *acApp, in acInstalled, installed bool, deps string) string {
	var b strings.Builder
	b.WriteString(a.About + "\n\n")
	b.WriteString("## On this handheld\n")
	if note, ok := a.noteHere(); ok {
		chip, _ := a.chipHere()
		b.WriteString(chip + " (" + acFirmwareName(acDetectFirmware()) + "): " + note.Why + ".\n")
	} else {
		b.WriteString(acFirmwareName(acDetectFirmware()) + ": the App Center installs and starts apps on the TrimUI's stock firmware and CrossMix.\n")
	}
	switch {
	case installed && in.HasMarker:
		b.WriteString("Installed by the App Center: " + strings.TrimSpace(in.Marker.Tag+" from "+in.Marker.Source))
		if in.Marker.Verified {
			b.WriteString(", download checked against GitHub's SHA-256")
		}
		b.WriteString(" · Apps/" + in.Folder + "\n")
		if a.SelfUpdates {
			b.WriteString("It can update itself, so what runs now may be newer than that release; the App Center cannot read its version.\n")
		}
	case installed:
		b.WriteString("Installed by hand in Apps/" + in.Folder + ". The App Center has not changed it and only will if you press Update or Remove.\n")
	default:
		b.WriteString("Not installed.\n")
	}
	if installed && in.HasRollback {
		v := in.RollbackTag
		if v == "" {
			v = "the previous version"
		}
		b.WriteString("Kept for Roll back: " + v + ".\n")
	}
	if l, ok := acLatestFor(a.ID); ok && l.Tag != "" {
		b.WriteString("Newest release: " + l.Tag + " (" + l.Asset.Name + ").\n")
	}
	b.WriteString("\n## Firmware\n")
	b.WriteString("• Stock firmware: " + acSupportLabel(a.Stock.State) + " — " + a.Stock.Why + "\n")
	b.WriteString("• CrossMix: " + acSupportLabel(a.Cross.State) + " — " + a.Cross.Why + "\n")
	b.WriteString("\n## Needs\n")
	switch {
	case len(a.Deps) == 0:
		b.WriteString("Nothing beyond the firmware.\n")
	case deps == "":
		b.WriteString("Checking…\n")
	default:
		b.WriteString(deps)
	}
	if len(a.Opens) > 0 {
		b.WriteString("\n## Opens\n")
		b.WriteString(strings.Join(acExtWords(a.Opens, 20), ", ") + ". Files' Open with hands the file to its launch script as its first argument ($1).\n")
		if a.ArgHow == "" {
			b.WriteString("Its source does not say that it takes a file that way, so Open with does not call it verified.\n")
		}
		if installed {
			switch {
			case in.ForwardsArgs:
				b.WriteString("Its launch script refers to its arguments, so the file can reach the app.\n")
			case acKOReaderIs(in, false):
				b.WriteString("Its launch script does not refer to its arguments, so JukaHub starts KOReader's own reader.lua with the file instead (or, without one, says to open it from KOReader's file browser).\n")
			default:
				b.WriteString("Its launch script does not refer to its arguments: the app may open without the file.\n")
			}
		}
	}
	if places := acPlaceWords(a); len(places) > 0 {
		b.WriteString("\n## Where JukaHub offers it\n")
		for _, p := range places {
			b.WriteString("• " + p + "\n")
		}
	}
	b.WriteString("\n## Source\n")
	b.WriteString(a.Source.URL + "\n")
	switch {
	case a.Source.downloadable():
		what := "the newest release's " + a.Source.Asset
		if a.Source.TagPrefix != "" {
			what = "the newest " + a.Source.TagPrefix + "… release's " + a.Source.Asset
		}
		b.WriteString("Get downloads " + what + " from GitHub.\n")
	default:
		b.WriteString("No download here: " + acWhereToGet(a) + ".\n")
	}
	if a.Licence != "" {
		b.WriteString("Licence: " + a.Licence + ".\n")
	}
	b.WriteString("\n## How sure this is\n")
	b.WriteString(a.Status + ". " + a.Checked + "\n")
	return b.String()
}

// acDepsText checks the dependencies (off the main thread).
func acDepsText(a *acApp) string {
	var b strings.Builder
	for _, d := range a.Deps {
		state, detail := acDepCheck(d)
		line := "• " + d.Name + ": " + acDepWord(state)
		if detail != "" {
			line += " — " + detail
		}
		b.WriteString(line + "\n")
		if d.Why != "" {
			b.WriteString("  " + d.Why + "\n")
		}
	}
	return b.String()
}

// acOpenAppSheet shows a catalogue app.
func acOpenAppSheet(config *Config, a *acApp) {
	in, installed := acInstalledFor(a.ID)
	acScreen.token++
	token := "appcenter-" + strconv.Itoa(acScreen.token)
	// The buttons read like a dialog: the primary one last, and never more
	// than four (the card is 860 px wide). The version an update brings is in
	// the body, not on the button.
	var buttons []tbxSheetButton
	if installed {
		// Four buttons at most: Controller Mode is offered on the sheet of a
		// controller app that is not installed (docs/APP_CENTER.md, Places).
		buttons = append(buttons, tbxSheetButton{label: "Remove", run: func() { acConfirmRemove(config, in.Label, in) }})
		if in.HasRollback {
			buttons = append(buttons, tbxSheetButton{label: "Roll back", run: func() { acStartRollBack(a.Name, in.Folder) }})
		}
		if a.Source.downloadable() {
			buttons = append(buttons, tbxSheetButton{label: "Update", run: func() { acConfirmUpdate(config, a, in) }})
		}
		buttons = append(buttons, tbxSheetButton{label: "Open", primary: true, run: func() { acLaunch(in, "") }})
	} else {
		buttons = append(buttons, tbxSheetButton{label: "Close"})
		if a.hasPlace("device") && sceneIndexExact(config, acCtlModeScene) >= 0 {
			buttons = append(buttons, tbxSheetButton{label: "Controller Mode", run: func() { tbxOpenScene(config, acCtlModeScene) }})
		}
		if a.Source.downloadable() {
			buttons = append(buttons, tbxSheetButton{label: "Get", primary: true, run: func() { acStartInstall(a) }})
		}
	}
	chip, _ := a.chipHere()
	spec := tbxSheetSpec{
		token:    token,
		title:    a.Name,
		subtitle: chip + " · " + a.Status,
		icon:     acPlaceIcon(a),
		body:     acSheetBody(a, in, installed, ""),
		buttons:  buttons,
		tall:     true,
		focus:    len(buttons) - 1,
	}
	if a.Source.URL != "" {
		spec.qr = tbxQRModules(a.Source.URL)
		spec.qrLabel = "Source on your phone"
	}
	tbxOpenSheet(config, spec)
	if len(a.Deps) > 0 {
		go func() {
			deps := acDepsText(a)
			tbxPost(func() { tbxUpdateSheet(token, acSheetBody(a, in, installed, deps), nil) })
		}()
	}
}

// acOpenOtherSheet shows an app the catalogue does not know.
func acOpenOtherSheet(config *Config, in acInstalled) {
	var b strings.Builder
	b.WriteString("This app is not in the App Center's list, so nothing here says how it fares on this firmware or what it needs. JukaHub starts it the way the Apps screen does and comes back when it exits.\n\n")
	b.WriteString("## On the card\nApps/" + in.Folder + "\n")
	if len(in.Declared) > 0 {
		b.WriteString("\n## Opens\nIts config.json declares " + strings.Join(acExtWords(in.Declared, 20), ", ") + ", so Files offers it under Open with.\n")
		if !in.ForwardsArgs {
			b.WriteString("Its launch script does not refer to its arguments: it may open without the file.\n")
		}
	}
	b.WriteString("\n## Adding it to the list\ndocs/APP_CENTER.md says what an entry needs: its source, what it was checked against and how it starts.\n")
	tbxOpenSheet(config, tbxSheetSpec{
		title:    in.Label,
		subtitle: "Not in the App Center's list",
		icon:     "apps",
		body:     b.String(),
		buttons: []tbxSheetButton{
			{label: "Remove", run: func() { acConfirmRemove(config, in.Label, in) }},
			{label: "Close"},
			{label: "Open", primary: true, run: func() { acLaunch(in, "") }},
		},
		focus: 2,
	})
}

// acConfirmUpdate asks before an update replaces an app's files; an app
// installed by hand says so.
func acConfirmUpdate(config *Config, a *acApp, in acInstalled) {
	body := "The newest release is downloaded and checked, then swapped in for Apps/" + in.Folder + ". The files you or the app wrote there (settings, saves) move into the new version; files the release ships replace theirs, and the old version is kept for Roll back."
	switch {
	case !in.HasMarker:
		body = "This copy was installed by hand, so the App Center cannot tell its version. " + body
	case a.SelfUpdates:
		body = a.Name + " can update itself, so it may already be on the newest release; this installs that release again. " + body
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Update " + a.Name + "?",
		subtitle: a.Source.Owner + "/" + a.Source.Repo,
		icon:     "update",
		body:     body,
		buttons:  []tbxSheetButton{{label: "Not now"}, {label: "Update", primary: true, run: func() { acStartInstall(a) }}},
	})
}

// acConfirmRemove asks before an app folder goes.
func acConfirmRemove(config *Config, name string, in acInstalled) {
	body := "Apps/" + in.Folder + " goes to the trash and is deleted, with the settings and saves inside it"
	switch {
	case in.HasRollback && in.HasKept:
		body += ", the version kept for Roll back and the older copies of its files kept aside in Apps/" + pkgRollbackDir
	case in.HasRollback:
		body += " and the version kept for Roll back"
	case in.HasKept:
		body += " and the older copies of its files kept aside in Apps/" + pkgRollbackDir
	}
	body += "."
	if !in.HasMarker {
		body += " It was installed by hand; the App Center has not changed it before."
	}
	if a := acCatalogueByID(in.AppID); a != nil && !a.Source.downloadable() {
		// KOReader, the Screen Recorder: nothing here can bring them back.
		body += " The App Center cannot install it again: " + acWhereToGet(a) + "."
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Remove " + name + "?",
		subtitle: "Apps/" + in.Folder,
		icon:     "trash",
		body:     body,
		buttons: []tbxSheetButton{{label: "Keep"}, {label: "Remove", primary: true, run: func() {
			acRunChange("remove:"+in.Folder, name, "Removing "+name, false, func(_ context.Context, _ func(string), _ func(int64, int64)) (string, error) {
				if err := acRemove(in.Folder); err != nil {
					return "", err
				}
				logInfo("appcenter", "removed Apps/%s", in.Folder)
				return name + " removed", nil
			})
		}}},
	})
}

// acStartRollBack swaps the kept version back in.
func acStartRollBack(name, folder string) {
	acRunChange("rollback:"+folder, name, "Rolling back "+name, false, func(_ context.Context, _ func(string), _ func(int64, int64)) (string, error) {
		if err := acRollBack(folder); err != nil {
			return "", err
		}
		logInfo("appcenter", "rolled Apps/%s back", folder)
		return name + " is back to the previous version · Roll back again to undo", nil
	})
}
