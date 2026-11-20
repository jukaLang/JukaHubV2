package main

// appcenter_open.go — what the rest of JukaHub asks the App Center.
//
// Files' "Open with" (openwith*.go) lists the apps that can open a file and
// remembers a default per type; it calls the functions below and nothing
// else, so the two sides meet in one place (the contract in the night pass's
// notes). Each opener is an installed TrimUI app, named "tsp:" plus its
// folder in lower case, which is what a saved default stores.
//
// An app opens a type when the catalogue says so or when its own config.json
// declares it:
//
//	"jukahub": {"opens": [".epub", ".pdf"]}
//
// (docs/APP_CENTER.md). It is Verified only when the catalogue also says how
// the file is passed and the installed launch script passes it on. Where the
// App Center only browses (a PC, NextUI, spruceOS) every list is empty.
// Every answer comes from the cached scan of the Apps
// folder (acInstalledNow), so a call costs no disk reads once the scan is
// warm; the openwith sheet calls these when it opens, not per frame.
//
// The places: an app is offered where it is useful besides the App Center.
// "books" is Files' reader menu, through acSuggestedForFile and
// acOpenersForFile; "recordings" is a button the Recordings page gains
// (acRecordingsPlace); "device" is Controller Mode (ctlmode*.go, another
// screen's), which offers the controller apps itself — the App Center links
// to it from the sheet of such an app that is not installed instead of
// listing them a second time.

import (
	"path/filepath"
	"strings"
	"time"
)

// acFileOpener is an app that can be handed a file.
type acFileOpener struct {
	ID        string // stable, persisted as a default: "tsp:" + lower(app folder name), e.g. "tsp:koreader"
	Name      string // "KOReader"
	Icon      string // icon image path or ""
	Launch    string // absolute launch script path ("" when not installed)
	AppID     string // App Center catalog id, "" for an app the catalog does not know
	Installed bool
	Verified  bool   // the catalog says this app opens this type and how the file is passed
	Note      string // short honest line: "CrossMix app", "Not tested with files", "Needs CrossMix"
}

// acOpenerIDPrefix starts every opener ID.
const acOpenerIDPrefix = "tsp:"

// acOpenerFor describes an installed app as an opener.
func acOpenerFor(in acInstalled, ext string) acFileOpener {
	op := acFileOpener{
		ID:        acOpenerIDPrefix + strings.ToLower(in.Folder),
		Name:      in.Label,
		Icon:      in.Icon,
		Launch:    in.Launch,
		AppID:     in.AppID,
		Installed: true,
	}
	app := acCatalogueByID(in.AppID)
	switch {
	case app != nil && ext != "" && app.opensExt(ext):
		// Verified: the source says how a file is passed AND this copy's
		// launch script passes it on.
		op.Verified = app.ArgHow != "" && in.ForwardsArgs
		switch {
		case !in.ForwardsArgs && acKOReaderIs(in, false):
			op.Note = "Its launch script does not pass a file on — JukaHub tries KOReader's own reader.lua with it"
		case !in.ForwardsArgs:
			op.Note = "Its launch script does not pass a file on — it may open without it"
		case app.ArgHow == "":
			op.Note = "Its source does not say it takes a file; its launch script passes one on"
		case app.Status == acStatusChecked:
			op.Note = "Checked against its source"
		default:
			op.Note = "Opens " + strings.TrimPrefix(ext, ".") + " files (untested with JukaHub)"
		}
	case ext != "" && acContains(in.Declared, ext):
		op.Note = "Its config.json says it opens " + strings.TrimPrefix(ext, ".") + " files"
		if !in.ForwardsArgs {
			op.Note += " · its launch script does not pass a file on"
		}
	default:
		op.Note = "Not tested with files"
		if app != nil {
			if note, ok := app.noteHere(); ok && note.State == acNeeds {
				op.Note = "Needs CrossMix · not tested with files"
			}
		}
	}
	return op
}

// acContains reports whether list holds s.
func acContains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// acFileExt is a path's extension in lower case (".epub").
func acFileExt(path string) string {
	return strings.ToLower(filepath.Ext(path))
}

// acOpenersForFile lists the installed apps that open this file's type:
// catalogue-verified first, then apps whose config.json declares the
// extension. Nil where the App Center only browses (a PC, NextUI, spruceOS:
// no TrimUI Apps folder it may start apps from).
func acOpenersForFile(path string) []acFileOpener {
	ext := acFileExt(path)
	if ext == "" || !acHandheld() {
		return nil
	}
	var verified, listed, declared []acFileOpener
	for _, in := range acInstalledNow() {
		app := acCatalogueByID(in.AppID)
		switch {
		case app != nil && app.opensExt(ext):
			if op := acOpenerFor(in, ext); op.Verified {
				verified = append(verified, op)
			} else {
				listed = append(listed, op)
			}
		case acContains(in.Declared, ext):
			declared = append(declared, acOpenerFor(in, ext))
		}
	}
	return append(append(verified, listed...), declared...)
}

// acSuggestedForFile lists the catalogue apps that would open this type but
// are not installed (Installed false), for "Get <Name> (App Center)".
func acSuggestedForFile(path string) []acFileOpener {
	ext := acFileExt(path)
	if ext == "" || !acHandheld() {
		return nil
	}
	var out []acFileOpener
	for i := range acCatalogue {
		a := &acCatalogue[i]
		if !a.opensExt(ext) {
			continue
		}
		if _, ok := acInstalledFor(a.ID); ok {
			continue
		}
		folder := a.ID
		if len(a.Folders) > 0 {
			folder = a.Folders[0]
		}
		op := acFileOpener{
			ID:       acOpenerIDPrefix + strings.ToLower(folder),
			Name:     a.Name,
			AppID:    a.ID,
			Verified: a.ArgHow != "",
		}
		chip, _ := a.chipHere()
		switch {
		case !a.Source.downloadable():
			op.Note = chip + " · " + acWhereToGet(a)
		default:
			op.Note = chip + " · Get it in the App Center"
		}
		out = append(out, op)
	}
	return out
}

// acOtherApps lists every installed TrimUI app (for "Another app…");
// Verified is false unless the catalogue knows it opens files.
func acOtherApps() []acFileOpener {
	if !acHandheld() {
		return nil
	}
	list := acInstalledNow()
	out := make([]acFileOpener, 0, len(list))
	for _, in := range list {
		op := acOpenerFor(in, "")
		if app := acCatalogueByID(in.AppID); app != nil && len(app.Opens) > 0 {
			op.Verified = app.ArgHow != "" && in.ForwardsArgs
			op.Note = "Opens " + strings.Join(acExtWords(app.Opens, 4), ", ")
			if !in.ForwardsArgs {
				op.Note += " · its launch script does not pass a file on"
			}
		}
		out = append(out, op)
	}
	return out
}

// acExtWords is up to n extensions without their dots, "…" after.
func acExtWords(exts []string, n int) []string {
	out := make([]string, 0, n+1)
	for i, e := range exts {
		if i == n {
			out = append(out, "…")
			break
		}
		out = append(out, strings.TrimPrefix(e, "."))
	}
	return out
}

// acOpenerByID resolves a saved default; false when that app is no longer
// installed.
func acOpenerByID(id string) (acFileOpener, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if !strings.HasPrefix(id, acOpenerIDPrefix) || !acHandheld() {
		// Where apps cannot start (a PC, NextUI), a saved default falls back
		// to JukaHub's own choice instead of failing on every press.
		return acFileOpener{}, false
	}
	for _, in := range acInstalledNow() {
		if acOpenerIDPrefix+strings.ToLower(in.Folder) == id {
			return acOpenerFor(in, ""), true
		}
	}
	return acFileOpener{}, false
}

// acOpenFile starts the app with the file (screen hand-off + clean return);
// it toasts on failure and reports whether the app started.
//
// A KOReader whose launch script ignores its arguments would open without the
// book: that one is the KOReader koreader.go knows (same folder), and its
// koreaderOpen runs KOReader's own entry, reader.lua, with the book (or says
// to open it from KOReader's file browser when it cannot).
func acOpenFile(config *Config, op acFileOpener, file string) bool {
	for _, in := range acInstalledNow() {
		if acOpenerIDPrefix+strings.ToLower(in.Folder) == strings.ToLower(op.ID) || (op.Launch != "" && in.Launch == op.Launch) {
			logInfo("appcenter", "opening %s with %s", filepath.Ext(file), in.Label)
			if file != "" && !in.ForwardsArgs && acKOReaderIs(in, true) {
				return koreaderOpen(file)
			}
			return acLaunch(in, file)
		}
	}
	if op.AppID != "" && !op.Installed {
		acOpenCenter(config, op.AppID)
		return false
	}
	ShowToast(op.Name+" is not installed any more", ToastKindWarning)
	return false
}

// acKOReaderIs reports whether an installed app is the KOReader koreader.go
// starts (extapps.go knows it by name or by its reader.lua): the same folder.
// press reads the card once if no scan has landed yet (koreaderAppNow); a
// row's text asks memory only (koreaderApp).
func acKOReaderIs(in acInstalled, press bool) bool {
	var app extApp
	var ok bool
	if press {
		app, ok = koreaderAppNow()
	} else {
		app, ok = koreaderApp()
	}
	if !ok || app.Dir == "" || in.Dir == "" {
		return false
	}
	return filepath.Clean(app.Dir) == filepath.Clean(in.Dir) || isPathWithin(in.Launch, app.Dir)
}

// acOpenCenter opens the App Center, focused on a catalogue app (its sheet
// open) when appID is not "".
func acOpenCenter(config *Config, appID string) {
	if config == nil {
		config = appConfig
	}
	acScreen.pending = appID
	acScreen.builtAt = time.Time{}
	if !tbxOpenScene(config, acSceneName) {
		logWarn("appcenter", "the App Center scene is missing")
	}
}

// acPlaceApps are the catalogue apps offered at place, installed first.
func acPlaceApps(place string) []*acApp {
	var inst, other []*acApp
	for i := range acCatalogue {
		a := &acCatalogue[i]
		if !a.hasPlace(place) {
			continue
		}
		if _, ok := acInstalledFor(a.ID); ok {
			inst = append(inst, a)
		} else {
			other = append(other, a)
		}
	}
	return append(inst, other...)
}

// acPlaceRun is a place's press: an installed app starts, one that is not
// opens the App Center on it.
func acPlaceRun(config *Config, a *acApp) {
	if a == nil {
		acOpenCenter(config, "")
		return
	}
	if in, ok := acInstalledFor(a.ID); ok && acHandheld() {
		acLaunch(in, "")
		return
	}
	acOpenCenter(config, a.ID)
}

// ── Recordings ───────────────────────────────────────────────────────────

// acRecordingsButtonText starts the button the Recordings page gains.
const acRecordingsButtonText = "Record the screen"

// acRecordingsTag marks that button (its TriggerValue, which a change_scene
// trigger does not read), so a second call and the save find it and never a
// button the user designed that also opens the App Center.
const acRecordingsTag = "jukahub:appcenter-recordings"

// acRecordingsCaptionStart begins the stock Recordings caption: only that
// caption is shortened beside the button, never a label the user wrote.
const acRecordingsCaptionStart = "RetroArch writes captures"

// acRecordingsPlace gives the Recordings page (a config scene: Rescan, Media,
// a caption and the list) a "Record the screen — <recorder>" button beside
// its own, in memory. It opens the App Center on the recorder, with its sheet
// up: Open starts it when it is installed, Get or "where to get it" when it
// is not. recordings.go calls it whenever the page lists its folder.
func acRecordingsPlace(config *Config) {
	// Only where an app can be installed and started: on a PC or a NextUI
	// card the button would lead to a recorder nobody can get. And only where
	// JukaHub's own Record screen (screenrec.go: Quick Settings, the in-game
	// menu) cannot record the panel: where it can, it is the recorder, and
	// the Recordings page already lists the app's clips beside its own.
	if config == nil || !acHandheld() || screenRecSupported() {
		return
	}
	apps := acPlaceApps("recordings")
	if len(apps) == 0 {
		return
	}
	scene := sceneByName(config, "Recordings")
	if scene == nil {
		return
	}
	acScreen.fromRecordings = apps[0].ID
	text := acRecordingsButtonText + " — " + apps[0].Name
	if _, ok := acInstalledFor(apps[0].ID); !ok {
		text = "Get a screen recorder"
	}
	caption := -1
	mediaRight := int32(0)
	for i := range scene.Elements {
		el := &scene.Elements[i]
		if el.Type == "button" && el.TriggerValue == acRecordingsTag {
			el.Text = text
			return
		}
		if el.Type == "button" && el.Y == 72 && el.X+getElementWidth(*el, 0) > mediaRight {
			mediaRight = el.X + getElementWidth(*el, 0)
		}
		if el.Type == "label" && caption < 0 && strings.HasPrefix(el.Text, acRecordingsCaptionStart) {
			caption = i
		}
	}
	if mediaRight == 0 {
		return
	}
	btn := Element{
		Type:         "button",
		Text:         text,
		Trigger:      "change_scene:" + acSceneName,
		TriggerValue: acRecordingsTag,
		X:            mediaRight + 16,
		Y:            72,
		Width:        "300",
		Height:       "48",
	}
	if caption >= 0 {
		// The caption moves right of the new button and says the short
		// version of itself.
		line := &scene.Elements[caption]
		acRecordingsCaption.saved, acRecordingsCaption.x, acRecordingsCaption.text = true, line.X, line.Text
		line.X = btn.X + 300 + 20
		line.Text = acRecordingsShortCaption
	}
	scene.Elements = append(scene.Elements, btn)
}

// acRecordingsShortCaption is the Recordings caption beside the button.
const acRecordingsShortCaption = "A plays a capture · RetroArch records from its Quick Menu"

// acRecordingsCaption is the caption as the config wrote it, for the save.
var acRecordingsCaption struct {
	saved bool
	x     int32
	text  string
}

// acRecordingsDetachForSave takes the in-memory button out of the Recordings
// scene and puts the caption back before the design config is written, and
// returns the function that adds them again (the saveConfig pattern of
// menuTilesDetachForSave): generated layout never reaches jukaconfig.json.
func acRecordingsDetachForSave(config *Config) func() {
	if config == nil {
		return func() {}
	}
	scene := sceneByName(config, "Recordings")
	if scene == nil {
		return func() {}
	}
	kept := scene.Elements[:0:0]
	removed := false
	for _, el := range scene.Elements {
		if el.Type == "button" && el.TriggerValue == acRecordingsTag {
			removed = true
			continue
		}
		if acRecordingsCaption.saved && el.Type == "label" && el.Text == acRecordingsShortCaption {
			el.X, el.Text = acRecordingsCaption.x, acRecordingsCaption.text
		}
		kept = append(kept, el)
	}
	if !removed {
		return func() {}
	}
	scene.Elements = kept
	return func() { acRecordingsPlace(config) }
}
