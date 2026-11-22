package main

// openwith.go — "Open with" and default apps for files.
//
// A in Files opens a file in JukaHub's own viewer for its type (files_open.go,
// features.go feEnterFocused). On a TrimUI that is now only one choice among
// several: the card's Apps folder holds programs such as KOReader that read
// formats JukaHub cannot (MOBI, DjVu, comics) or read a PDF better, and an
// app the user prefers should be able to take a type over for good. So:
//
//   - Files → Y → "Open with…" (and the text viewer's and the reader's menus)
//     opens a sheet that lists every way to open the file: JukaHub's viewers
//     that really handle the type, Open with Windows on a PC, the installed
//     TrimUI apps that open it (the App Center says which are verified,
//     appcenter_open.go), "Get <App>" for a verified app that is not on the
//     card, and "Another app…" for any other installed app.
//   - A opens the file once; X makes the row the default for the extension
//     (and opens it); Y forgets the default.
//   - With a default saved, A in Files opens the file there. Games in a
//     console folder always play: a default never takes a ROM away from the
//     library's emulator. A default app that left the card is passed over
//     (one toast per type and session) and the file opens as without one.
//   - A format JukaHub has no viewer for, with exactly one verified app on the
//     card that reads it, opens in that app on A without asking (the implicit
//     default), and a toast says how to change it. KOReader (koreader.go)
//     comes after that, and takes PDF and EPUB too under "Open books with:
//     KOReader"; for those types JukaHub's own viewer is saved by name when
//     it is chosen (owNoDefaultGoesElsewhere).
//   - Default apps… lists the saved defaults: A changes one, X resets it.
//
// App names, and what takes a type without a default, are drawn every frame
// but resolved at presses only (owPrimeNames): the rows read caches.
//
// The defaults are one bookkeeping Custom entry (owDefaultsKey), a JSON
// object of ".ext" → handler ID: "jh:<viewer>" for JukaHub's own, "win:system"
// for Windows' association and "tsp:<app folder>" for an installed app. The
// sheet itself lives in openwith_ui.go.

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// owDefaultsKey is the Custom entry that holds the defaults.
const owDefaultsKey = "openWithDefaults"

func init() {
	// The defaults name apps installed on this card and viewers of this build:
	// bookkeeping the Settings docs do not list (cfgdoc.go).
	cfgdocRuntimeKeys[owDefaultsKey] = true
}

// The handler IDs JukaHub itself answers.
const (
	owReader  = "jh:reader"
	owImages  = "jh:images"
	owMusic   = "jh:music"
	owVideo   = "jh:video"
	owText    = "jh:text"
	owHex     = "jh:hex"
	owArchive = "jh:archive"
	owGame    = "jh:game"
	owWeb     = "jh:web"
	owScript  = "jh:script"
	owJava    = "jh:java"
	owSystem  = "win:system"
	owAppPfx  = "tsp:"
)

// owBuiltinOrder is the order JukaHub's viewers are listed in after the one
// A picks today.
var owBuiltinOrder = []string{owReader, owImages, owMusic, owVideo, owGame, owArchive, owWeb, owScript, owJava, owText, owHex}

// owBuiltinNames are the viewers' names in the sheet and in toasts.
var owBuiltinNames = map[string]string{
	owReader:  "Reader",
	owImages:  "Image viewer",
	owMusic:   "Music player",
	owVideo:   "Video player",
	owText:    "Text editor",
	owHex:     "Hex editor",
	owArchive: "Archive",
	owGame:    "Play",
	owWeb:     "Canvas sandbox",
	owScript:  "Run script",
	owJava:    "Java",
	owSystem:  "Windows",
}

// owBuiltinDetails are the lines under the viewers' names.
var owBuiltinDetails = map[string]string{
	owReader:  "JukaHub · PDF and EPUB pages, bookmarks",
	owImages:  "JukaHub · zoom and slideshow",
	owMusic:   "JukaHub · plays the folder as a playlist",
	owVideo:   "JukaHub · video player",
	owText:    "JukaHub · view and edit the text",
	owHex:     "JukaHub · view and change the file's bytes",
	owArchive: "JukaHub · see what is inside, extract",
	owGame:    "JukaHub · the library's emulator",
	owWeb:     "JukaHub · runs the page offline",
	owScript:  "JukaHub · runs it as a shell script",
	owJava:    "JukaHub · runs the Java program",
	owSystem:  "The app Windows uses for this type",
}

// owImplicitTypes are the formats JukaHub has no viewer for that a reading
// app on the card usually does: e-books, comics and office documents. Only
// these open in an app without being asked (owOpenImplicit); a plain .txt
// keeps opening as text even though KOReader reads it too, and so does an
// .rtf (the text page shows it).
var owImplicitTypes = map[string]bool{
	".mobi": true, ".azw": true, ".azw3": true, ".prc": true, ".pdb": true,
	".fb2": true, ".fbz": true, ".djvu": true, ".djv": true, ".chm": true,
	".cbz": true, ".cbr": true, ".cb7": true, ".cbt": true,
	".xps": true, ".oxps": true, ".doc": true, ".docx": true, ".odt": true,
}

// owNoDefaultGoesElsewhere reports whether, with no default saved, something
// other than JukaHub's choice takes A on the type: the one verified app or
// KOReader for a format JukaHub cannot show (owOpenImplicit, koreader.go's
// koreaderFilesOpen), or KOReader for PDF and EPUB under Settings → Media &
// playback → "Open books with: KOReader". JukaHub's own viewer is then saved
// by name to be the default, not as "no default".
func owNoDefaultGoesElsewhere(c *Config, ext string) bool {
	switch {
	case owImplicitTypes[ext], koreaderOnlyTypes[ext]:
		return true
	case ext == ".pdf" || ext == ".epub":
		return koreaderBooksChoice(c) == koreaderBooksKOReader
	}
	return false
}

// owKind is what a row of the sheet does.
type owKind uint8

const (
	owKindBuiltin owKind = iota // one of JukaHub's viewers
	owKindSystem                // Windows' association
	owKindApp                   // an installed app
	owKindGet                   // "Get <App>": the App Center, on that app
	owKindMore                  // "Another app…": lists every other app
)

// owHandler is one way to open a file.
type owHandler struct {
	ID     string
	Name   string
	Detail string
	Kind   owKind
	App    acFileOpener
}

// owExt is the lower-case extension defaults are kept under ("" for none).
func owExt(name string) string {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	if len(ext) < 2 || len(ext) > 16 || strings.ContainsAny(ext, " /\\") {
		return ""
	}
	return ext
}

// owValidID reports whether a saved value names a handler at all.
func owValidID(id string) bool {
	if len(id) > 200 {
		return false
	}
	if _, ok := owBuiltinNames[id]; ok {
		return true
	}
	return strings.HasPrefix(id, owAppPfx) && len(id) > len(owAppPfx)
}

// --- Defaults ---------------------------------------------------------------

// owDefaultsCache keeps the parsed defaults for the raw value they came from
// (or the object a hand edit left, held so its address stays its own): the
// Files sheet reads them every frame.
var owDefaultsCache struct {
	raw string
	obj map[string]interface{}
	m   map[string]string
}

// owDefaultsView returns the saved defaults. The map is shared: callers that
// change it copy it first (owDefaultsSet).
func owDefaultsView(config *Config) map[string]string {
	if config == nil || config.Variables.Custom == nil {
		return nil
	}
	raw := ""
	var obj map[string]interface{}
	switch v := config.Variables.Custom[owDefaultsKey].(type) {
	case string:
		raw = v
	case map[string]interface{}:
		// A hand-edited jukauser.json may hold the object itself: it is
		// marshalled once per object, not once per frame.
		if owDefaultsCache.obj != nil && owDefaultsCache.m != nil &&
			reflect.ValueOf(v).Pointer() == reflect.ValueOf(owDefaultsCache.obj).Pointer() {
			return owDefaultsCache.m
		}
		obj = v
		if b, err := json.Marshal(v); err == nil {
			raw = string(b)
		}
	}
	if obj == nil && owDefaultsCache.obj == nil && raw == owDefaultsCache.raw && owDefaultsCache.m != nil {
		return owDefaultsCache.m
	}
	parsed := map[string]string{}
	_ = json.Unmarshal([]byte(raw), &parsed)
	clean := make(map[string]string, len(parsed))
	for ext, id := range parsed {
		// "epub" and ".EPUB" are both .epub; app IDs are kept in lower case,
		// as the App Center names them ("tsp:" + the folder in lower case).
		ext = strings.TrimSpace(ext)
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		ext = owExt("x" + ext)
		id = strings.ToLower(strings.TrimSpace(id))
		if ext == "" || !owValidID(id) {
			continue
		}
		clean[ext] = id
	}
	owDefaultsCache.raw, owDefaultsCache.obj, owDefaultsCache.m = raw, obj, clean
	return clean
}

// owDefaultFor is the handler saved for an extension, or "".
func owDefaultFor(config *Config, ext string) string {
	if ext == "" {
		return ""
	}
	return owDefaultsView(config)[ext]
}

// owDefaultsSet saves (or, with id "", forgets) the default for ext.
func owDefaultsSet(config *Config, ext, id string) {
	if config == nil || ext == "" {
		return
	}
	next := make(map[string]string, len(owDefaultsView(config))+1)
	for k, v := range owDefaultsView(config) {
		next[k] = v
	}
	if id == "" {
		delete(next, ext)
	} else {
		next[ext] = id
	}
	raw := "{}"
	if b, err := json.Marshal(next); err == nil {
		raw = string(b)
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	// Bookkeeping: written directly, not through setStoreString, so no
	// same-name mirror is re-applied (extras.go).
	config.Variables.Custom[owDefaultsKey] = raw
	saveUserConfigDebounced(userConfigFromVariables(config))
	owPrimeNames(config)
}

// owSortedDefaults lists the saved defaults by extension.
func owSortedDefaults(config *Config) []string {
	m := owDefaultsView(config)
	exts := make([]string, 0, len(m))
	for ext := range m {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	return exts
}

// --- Names ------------------------------------------------------------------

// owNames caches an app's name by handler ID for the rows drawn every frame
// (the Files sheet's detail, the Default apps list): resolving an app may
// scan the card, so it happens at presses only (owPrimeNames) and the rows
// read this.
var owNames struct {
	byID map[string]owNameEntry
}

type owNameEntry struct {
	name      string
	installed bool
}

// owNoteName remembers an app's name the moment it was resolved anyway.
func owNoteName(op acFileOpener) {
	if op.ID == "" {
		return
	}
	if owNames.byID == nil {
		owNames.byID = map[string]owNameEntry{}
	}
	owNames.byID[strings.ToLower(op.ID)] = owNameEntry{name: op.Name, installed: op.Installed && op.Launch != ""}
}

// owImplicitSeen is, per format JukaHub cannot show, the one verified app on
// the card that reads it (owImplicitOpener), as last seen at a press: what A
// does with such a file is drawn every frame (owAsBefore).
var owImplicitSeen = map[string]acFileOpener{}

// owNoteImplicit refreshes owImplicitSeen for one type from a path (a real
// file or owTypeProbe).
func owNoteImplicit(ext, path string) {
	if !owImplicitTypes[ext] {
		return
	}
	if op, ok := owImplicitOpener(path); ok {
		owImplicitSeen[ext] = op
		owNoteName(op)
		return
	}
	delete(owImplicitSeen, ext)
}

// owPrimeNames resolves every saved app default, and the implicit app of each
// format JukaHub cannot show, into the caches the rows read. Called at
// presses: the Files sheet opening (files_manager.go openFeSheet), Default
// apps, the Open with sheet, a default being saved. A card scan happens here
// if anywhere (the App Center's list is cached once it ran).
func owPrimeNames(config *Config) {
	for _, id := range owDefaultsView(config) {
		if !owIsAppID(id) {
			continue
		}
		if op, ok := acOpenerByID(id); ok && op.Installed && op.Launch != "" {
			owNoteName(op)
			continue
		}
		if owNames.byID == nil {
			owNames.byID = map[string]owNameEntry{}
		}
		e := owNames.byID[id]
		if e.name == "" {
			e.name = strings.TrimPrefix(id, owAppPfx)
		}
		e.installed = false
		owNames.byID[id] = e
	}
	for ext := range owImplicitTypes {
		owNoteImplicit(ext, owTypeProbe(ext))
	}
}

// owHandlerName is a handler ID's name: a viewer's, or the app's from the
// name cache. It reads nothing: an app no press has resolved yet is named by
// its folder and taken as installed (owPrimeNames corrects both at the next
// press).
func owHandlerName(id string) (string, bool) {
	if name, ok := owBuiltinNames[id]; ok {
		if id == owSystem {
			return name, filesSystemOpenAvailable()
		}
		return name, true
	}
	if !owIsAppID(id) {
		return "", false
	}
	if e, ok := owNames.byID[id]; ok {
		return e.name, e.installed
	}
	return strings.TrimPrefix(id, owAppPfx), true
}

// owIsAppID reports whether a handler ID names an app on the card.
func owIsAppID(id string) bool {
	return len(id) > len(owAppPfx) && strings.HasPrefix(id, owAppPfx)
}

// --- What handles a file ----------------------------------------------------

// owTypeLooksBinary guesses, without reading the disk, whether a type is
// bytes rather than text: the Default apps list has a type, not a file.
func owTypeLooksBinary(name string) bool {
	if fileEditorCanEdit(name) {
		return false
	}
	return owImplicitTypes[owExt(name)] || filesHasDedicatedViewer(name)
}

// owFileIsBinary reads the head of a real file; a file that cannot be read
// counts as binary (the hex view explains the error, the editor would not).
func owFileIsBinary(path string) bool {
	binary, err := filesLooksBinary(path)
	return err != nil || binary
}

// owJukaChoice is the viewer A picks today, in feEnterFocused's order.
func owJukaChoice(path string, binary bool) string {
	name := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(name))
	rom := filesROMSystem(path) != ""
	switch {
	case ext == ".sh":
		return owScript
	case javaIsFile(name):
		if rom {
			return owGame
		}
		return owJava
	case feArchiveKindOf(name) != feArchiveNone:
		if rom {
			return owGame
		}
		return owArchive
	case isImageExtension(ext):
		return owImages
	case ext == ".html" || ext == ".js":
		return owWeb
	case pdfIsDocumentPath(path):
		return owReader
	case isMusicPlaylistFile(name) || isAudioFile(name):
		return owMusic
	case isMediaFile(name) || hasMediaExtension(path):
		return owVideo
	case rom:
		return owGame
	case binary:
		return owHex
	}
	return owText
}

// owBuiltinHandles reports whether one of JukaHub's viewers really handles
// the file. ctx is where the sheet was opened ("files", "editor", "hex",
// "reader", "defaults"): the archive sheet belongs to the Files screen. The
// hex editor (fileeditor_hex.go) opens every file.
func owBuiltinHandles(id, path string, binary bool, ctx string) bool {
	name := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(name))
	switch id {
	case owReader:
		return pdfIsDocumentPath(path)
	case owImages:
		return isImageExtension(ext)
	case owMusic:
		return isMusicPlaylistFile(name) || isAudioFile(name)
	case owVideo:
		return (isMediaFile(name) || hasMediaExtension(path)) && !isAudioFile(name)
	case owGame:
		return filesROMSystem(path) != ""
	case owArchive:
		return feArchiveKindOf(name) != feArchiveNone && (ctx == "files" || ctx == "defaults")
	case owWeb:
		return ext == ".html" || ext == ".js"
	case owScript:
		return ext == ".sh" && !IsWindows()
	case owJava:
		return javaIsFile(name) && filesROMSystem(path) == ""
	case owText:
		return !binary
	case owHex:
		return true
	case owSystem:
		return filesSystemOpenAvailable()
	}
	return false
}

// owBuiltinIDs lists the viewers that handle the file, JukaHub's choice
// first. The viewer the sheet was opened from is left out: the text page's
// "Open with…" offers the hex editor and not itself, the hex editor's the
// text editor (for a text file) and not itself.
func owBuiltinIDs(path string, binary bool, ctx string) []string {
	choice := owJukaChoice(path, binary)
	ids := make([]string, 0, 6)
	seen := map[string]bool{}
	add := func(id string) {
		if seen[id] || !owBuiltinHandles(id, path, binary, ctx) {
			return
		}
		switch {
		case ctx == "editor" && id == owText:
			return
		case ctx == "hex" && id == owHex:
			return
		case ctx == "reader" && id == owReader:
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	add(choice)
	for _, id := range owBuiltinOrder {
		add(id)
	}
	return ids
}

// owResolve turns a saved ID into a handler for this file. False when the
// viewer no longer handles the type or the app is not on the card.
func owResolve(id, path string, ctx string) (owHandler, bool) {
	if strings.HasPrefix(id, owAppPfx) {
		op, ok := acOpenerByID(id)
		if !ok || !op.Installed || op.Launch == "" {
			return owHandler{}, false
		}
		owNoteName(op)
		return owHandler{ID: op.ID, Name: op.Name, Kind: owKindApp, App: op}, true
	}
	if _, ok := owBuiltinNames[id]; !ok {
		return owHandler{}, false
	}
	binary := false
	if id == owText {
		binary = owFileIsBinary(path)
	} else if id == owHex {
		binary = true // the hex view shows any file
	}
	if !owBuiltinHandles(id, path, binary, ctx) {
		return owHandler{}, false
	}
	kind := owKindBuiltin
	if id == owSystem {
		kind = owKindSystem
	}
	return owHandler{ID: id, Name: owBuiltinNames[id], Detail: owBuiltinDetails[id], Kind: kind}, true
}

// owAppDetail is the line under an app's name. ext is the type the row is
// for, "" for an app listed under "Another app…" (not known for the type).
// Verified means the App Center's catalogue documents the type and how the
// file is passed, not that JukaHub tried it, so it says "catalogue".
func owAppDetail(op acFileOpener, ext string) string {
	parts := []string{"App"}
	note := strings.TrimSpace(op.Note)
	switch {
	case op.Verified && ext != "":
		parts = append(parts, "catalogue: opens "+ext)
		// The App Center's "Opens epub files (untested with JukaHub)" would
		// say the type twice; its aside stays.
		if strings.HasPrefix(note, "Opens ") {
			note = ""
			if i := strings.IndexByte(op.Note, '('); i >= 0 {
				note = strings.Trim(op.Note[i:], "() ")
			}
		}
	case op.Verified && note == "":
		note = "catalogue: opens files"
	case note == "":
		note = "Not tested with files"
	}
	if note != "" {
		parts = append(parts, note)
	}
	return strings.Join(parts, " · ")
}

// owAppRow is an installed app as a row.
func owAppRow(op acFileOpener, ext string) owHandler {
	owNoteName(op)
	return owHandler{ID: op.ID, Name: op.Name, Detail: owAppDetail(op, ext), Kind: owKindApp, App: op}
}

// owTypeProbe is a stand-in file name for a type ("Default apps" changes a
// type, not a file): the App Center matches by extension.
func owTypeProbe(ext string) string {
	return "file" + ext
}

// owBuildRows lists every way to open path (a real file, or owTypeProbe in
// the Default apps list). saved is the type's default ("" for JukaHub's
// choice); it is moved to the top. more is what "Another app…" expands to.
func owBuildRows(path string, binary bool, ctx, saved string) (rows, more []owHandler) {
	ext := owExt(path)
	for _, id := range owBuiltinIDs(path, binary, ctx) {
		rows = append(rows, owHandler{ID: id, Name: owBuiltinNames[id], Detail: owBuiltinDetails[id], Kind: owKindBuiltin})
	}
	if owBuiltinHandles(owSystem, path, binary, ctx) {
		rows = append(rows, owHandler{ID: owSystem, Name: "Open with Windows", Detail: owBuiltinDetails[owSystem], Kind: owKindSystem})
	}
	listed := map[string]bool{}
	for _, op := range acOpenersForFile(path) {
		if op.ID == "" || listed[op.ID] || !op.Installed || op.Launch == "" {
			continue
		}
		listed[op.ID] = true
		rows = append(rows, owAppRow(op, ext))
	}
	// "Get <App>" only for an app the catalogue says opens the type and
	// takes the file (none off the handheld: the App Center answers nil).
	for _, op := range acSuggestedForFile(path) {
		if op.AppID == "" || !op.Verified || listed[op.ID] {
			continue
		}
		listed[op.ID] = true
		detail := "App Center"
		if ext != "" {
			detail += " · opens " + ext + " files"
		}
		if note := strings.TrimSpace(op.Note); note != "" {
			detail += " · " + note
		}
		rows = append(rows, owHandler{ID: op.ID, Name: "Get " + op.Name, Detail: detail, Kind: owKindGet, App: op})
	}
	for _, op := range acOtherApps() {
		if op.ID == "" || listed[op.ID] || !op.Installed || op.Launch == "" {
			continue
		}
		listed[op.ID] = true
		// Not known to open this type: its line says what it does open.
		more = append(more, owAppRow(op, ""))
	}
	// A saved default listed only under "Another app…" comes up front.
	if saved != "" {
		for i, h := range more {
			if h.ID == saved {
				rows = append(rows, h)
				more = append(more[:i:i], more[i+1:]...)
				break
			}
		}
	}
	if len(more) > 0 {
		rows = append(rows, owHandler{ID: "more", Name: "Another app…",
			Detail: pluralizeCount(len(more), "installed app", "installed apps") + " · not tested with files", Kind: owKindMore})
	}
	if saved != "" {
		for i, h := range rows {
			if h.ID == saved && i > 0 {
				moved := h
				copy(rows[1:i+1], rows[:i])
				rows[0] = moved
				break
			}
		}
	}
	return rows, more
}

// --- Opening ----------------------------------------------------------------

// owRun opens path with a handler. entry is the Files row when there is one
// (the game and Java launchers check it against the Files root themselves).
// confirmed: the sheet already took the second press a script from Downloads
// needs (chatConfirmRun), so it is not asked for again.
func owRun(config *Config, h owHandler, path string, entry FileEntry, confirmed bool) bool {
	if entry.Path == "" {
		entry = FileEntry{Name: filepath.Base(path), Path: path}
	}
	switch h.Kind {
	case owKindApp:
		return acOpenFile(config, h.App, path)
	case owKindSystem:
		return filesOpenWithSystem(path)
	case owKindGet:
		acOpenCenter(config, h.App.AppID)
		return true
	}
	switch h.ID {
	case owReader:
		// Picked by name: JukaHub's reader even with "Open books with:
		// KOReader" (koreader.go).
		return pdfvOpenDocumentHere(config, path)
	case owImages:
		openImageViewer(config, path)
	case owMusic:
		if isMusicPlaylistFile(path) {
			openMusicPlaylistFile(config, path)
			return true
		}
		recordPlayed(config, path)
		openAudioPlayer(config, path)
	case owVideo:
		recordPlayed(config, path)
		playSmartURL(config, path)
	case owText:
		return fileEditorOpen(path)
	case owHex:
		return fileEditorOpenHex(path)
	case owArchive:
		return openFeArchiveSheet(config, path, false)
	case owGame:
		return filesOpenROM(config, entry)
	case owWeb:
		openFileInCanvasSandbox(path)
	case owScript:
		// A script from the chat still runs on a second press only.
		if !confirmed && !chatConfirmRun(path) {
			return false
		}
		if err := feLaunchScript(config, path); err != nil {
			showToast("Cannot run script: "+err.Error(), ToastError())
			return false
		}
		showToast("Started "+filepath.Base(path), ToastSuccess())
	case owJava:
		return javaFilesOpen(config, entry)
	default:
		return false
	}
	return true
}

// owMissingWarned remembers, per extension, that this session already said
// the default app is gone: the toast comes once, the default stays saved (the
// app may come back with the card or a reinstall).
var owMissingWarned = map[string]bool{}

// owOpenWithDefault is A on a file in Files (feEnterFocused): it opens the
// file in the type's saved default, or in the one verified app that reads a
// format JukaHub cannot (owOpenImplicit). It reports whether it handled the
// press; false leaves A to JukaHub's own choice.
func owOpenWithDefault(config *Config, entry FileEntry) bool {
	if config == nil || entry.IsDir || entry.Name == ".." || !feSceneActive(config) {
		return false
	}
	// A game in a console folder always plays in the library's emulator.
	if filesROMSystem(entry.Path) != "" {
		return false
	}
	ext := owExt(entry.Name)
	if ext == "" {
		return false
	}
	id := owDefaultFor(config, ext)
	if id == "" {
		return owOpenImplicit(config, entry, ext)
	}
	path, err := resolveExplorerEntry(config, entry.Path)
	if err != nil {
		return false // A's own path explains why the file cannot be opened
	}
	h, ok := owResolve(id, path, "files")
	if !ok {
		if !owMissingWarned[ext] {
			owMissingWarned[ext] = true
			// The name the last press saw (owPrimeNames), not a fresh scan.
			name, _ := owHandlerName(id)
			msg := name + " is not available for " + ext + " files any more"
			if !owIsAppID(id) {
				msg = name + " cannot open " + ext + " files here"
				if id == owSystem {
					msg = "Open with Windows works on a PC only"
				}
			}
			showToast(msg, ToastWarn())
			// koreader.go's "Install KOReader…" would be a second toast for
			// the same press.
			koreaderHinted = true
		}
		// The file opens as if no default were saved: the one verified app
		// for a format JukaHub cannot show, else (false) KOReader or
		// JukaHub's own viewer in feEnterFocused.
		return owOpenImplicit(config, entry, ext)
	}
	owRun(config, h, path, entry, false)
	return true
}

// owImplicitOpener is the one verified, installed app that reads path's type
// (a real file or owTypeProbe); false with none or with two.
func owImplicitOpener(path string) (acFileOpener, bool) {
	var only acFileOpener
	n := 0
	for _, op := range acOpenersForFile(path) {
		if op.Verified && op.Installed && op.Launch != "" {
			only = op
			n++
		}
	}
	return only, n == 1
}

// owOpenImplicit opens a format JukaHub has no viewer for in the one
// verified app on the card that reads it.
func owOpenImplicit(config *Config, entry FileEntry, ext string) bool {
	if !owImplicitTypes[ext] {
		return false
	}
	path, err := resolveExplorerEntry(config, entry.Path)
	if err != nil {
		return false
	}
	owNoteImplicit(ext, path)
	only, ok := owImplicitSeen[ext]
	if !ok {
		return false
	}
	if acOpenFile(config, only, path) {
		showToast("Opening in "+only.Name+" · "+hintSentence("%s → Open with… to change", "Y", "Actions"), ToastInfo())
	}
	// A start that failed has said so; the hex view behind it would only add
	// a second answer to one press.
	return true
}

// owAsBefore names what takes A on a type with no default saved when that is
// not JukaHub's own viewer (owNoDefaultGoesElsewhere): the one verified app
// for a format JukaHub cannot show, else KOReader (koreader.go). False when
// JukaHub's choice opens it. From memory only: rows drawn every frame ask.
func owAsBefore(config *Config, ext string) (string, bool) {
	if !owNoDefaultGoesElsewhere(config, ext) {
		return "", false
	}
	if op, ok := owImplicitSeen[ext]; ok && owImplicitTypes[ext] {
		return op.Name, true
	}
	if _, ok := koreaderApp(); ok {
		return "KOReader", true
	}
	return "", false
}

// owCurrentLabel is what A does with a file, for a row's detail: the saved
// default's name, else what takes the type without one (owAsBefore), else
// JukaHub's choice. It reads no disk: the caches are filled at presses.
func owCurrentLabel(config *Config, name, path string) string {
	ext := owExt(name)
	if filesROMSystem(path) == "" {
		if id := owDefaultFor(config, ext); id != "" {
			if label, installed := owHandlerName(id); installed {
				return label + " · default"
			}
		}
		if label, ok := owAsBefore(config, ext); ok {
			return label
		}
	}
	choice := owJukaChoice(path, owTypeLooksBinary(name))
	if choice == owText && !fileEditorCanEdit(name) && !filesHasDedicatedViewer(name) {
		// A type the editor does not know: A reads the file and shows text or
		// its bytes, whichever it turns out to be.
		return "Text or hex editor"
	}
	return owBuiltinNames[choice]
}

// --- Files sheet rows (files_manager.go) -------------------------------------

// The Files sheet's two rows, numbered well clear of files_manager.go's own
// kinds (shelf_dos_import.go uses 1000).
const (
	feActOpenWith    feActionKind = 1100
	feActDefaultApps feActionKind = 1101
)

// owFilesAction is the Files sheet's "Open with…" row: one file at a time.
func owFilesAction(config *Config, targets []FileEntry) feAction {
	a := feAction{Kind: feActOpenWith, Label: "Open with…", Detail: "pick one file"}
	switch {
	case len(targets) == 1 && targets[0].IsDir:
		a.Detail = "folders open in Files"
	case len(targets) == 1 && targets[0].Name != "..":
		a.Detail = owCurrentLabel(config, targets[0].Name, targets[0].Path)
		a.Enabled = true
	case len(targets) > 1:
		a.Detail = "one at a time"
	}
	return a
}

// owDefaultAppsAction is the "Default apps…" row, shown once a default is
// saved (before that there is nothing to review).
func owDefaultAppsAction(config *Config) (feAction, bool) {
	n := len(owDefaultsView(config))
	if n == 0 {
		return feAction{}, false
	}
	return feAction{Kind: feActDefaultApps, Label: "Default apps…",
		Detail: pluralizeCount(n, "file type", "file types"), Enabled: true}, true
}

// owFilesActivate runs one of the two rows (feSheetActivate).
func owFilesActivate(config *Config, kind feActionKind, targets []FileEntry) {
	switch kind {
	case feActOpenWith:
		if len(targets) != 1 || targets[0].IsDir || targets[0].Name == ".." {
			showToast("Open one file at a time", ToastInfo())
			return
		}
		path, err := resolveExplorerEntry(config, targets[0].Path)
		if err != nil {
			showToast("Cannot open "+targets[0].Name+": "+filesFriendlyError(err), ToastError())
			return
		}
		feSheetForget()
		owOpenPicker(config, path, targets[0], "files")
	case feActDefaultApps:
		feSheetForget()
		owShowDefaultApps(config)
	}
}

// --- The reader's menu (pdfv_view.go) ----------------------------------------

// owReaderBeside is the app the reader offers beside its own pages: the
// verified app on the card that opens the document ("Open in <App>"), else a
// verified app the App Center can fetch ("Get <App>"). Nothing when that app
// is the KOReader koreader.go finds, or KOReader is on the card at all:
// koreader.go's own "Open in KOReader" row opens it whatever its launch
// script does with "$1", and one reading app beside the reader is enough.
func owReaderBeside(path string) (owHandler, bool) {
	if path == "" {
		return owHandler{}, false
	}
	ko, haveKO := koreaderApp()
	for _, op := range acOpenersForFile(path) {
		if !op.Verified || !op.Installed || op.Launch == "" {
			continue
		}
		if haveKO && isPathWithin(op.Launch, ko.Dir) {
			return owHandler{}, false
		}
		owNoteName(op)
		return owHandler{ID: op.ID, Name: op.Name, Kind: owKindApp, App: op}, true
	}
	if haveKO {
		return owHandler{}, false
	}
	for _, op := range acSuggestedForFile(path) {
		if op.AppID != "" && op.Verified {
			return owHandler{ID: op.ID, Name: op.Name, Kind: owKindGet, App: op}, true
		}
	}
	return owHandler{}, false
}

// owReaderRows keeps the reader menu's rows for the document they were built
// for: the menu rebuilds its rows after every press (pdfvMenuRows), and the
// app beside the reader is asked of the App Center once per menu opening.
var owReaderRows struct {
	path string
	rows []filesMenuRow
}

// owReaderMenuRows are the reader menu's rows: the app beside the reader,
// then "Open with…". fresh is true when the menu has just opened (its rows
// are rebuilt from the App Center then, and reused until the next opening).
func owReaderMenuRows(path string, fresh bool) []filesMenuRow {
	if path == "" {
		return nil
	}
	if fresh || owReaderRows.path != path || owReaderRows.rows == nil {
		var rows []filesMenuRow
		if h, ok := owReaderBeside(path); ok {
			if h.Kind == owKindGet {
				rows = append(rows, filesMenuRow{ID: "owapp", Label: "Get " + h.Name, Value: "App Center", Enabled: true})
			} else {
				rows = append(rows, filesMenuRow{ID: "owapp", Label: "Open in " + h.Name, Enabled: true})
			}
		}
		owReaderRows.path = path
		owReaderRows.rows = append(rows, filesMenuRow{ID: "openwith", Label: "Open with…", Enabled: true})
	}
	// A copy: the caller's slice is edited in place (koreaderReaderMenuRows).
	return append([]filesMenuRow(nil), owReaderRows.rows...)
}

// owReaderMenuApply runs one of those rows; false for any other row.
func owReaderMenuApply(config *Config, id, path string) bool {
	switch id {
	case "owapp":
		h, ok := owReaderBeside(path)
		if !ok {
			showToast("That app is not available any more", ToastInfo())
			return true
		}
		owRun(config, h, path, FileEntry{}, false)
		return true
	case "openwith":
		owOpenPicker(config, path, FileEntry{}, "reader")
		return true
	}
	return false
}
