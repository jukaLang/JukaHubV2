package main

// koreader.go — "Open with KOReader": books, comics and game manuals in the
// KOReader installed on the card (CrossMix ships one; anyone can copy it to
// the Apps folder). JukaHub's own reader stays a choice and the default, and
// every addition here is invisible while KOReader is not installed.
//
//   - Settings → Media & playback → "Open books with": JukaHub reader or
//     KOReader. With KOReader, every PDF or EPUB that would open in the reader
//     (Files, the chat, game manuals) opens there instead: the hook is the top
//     of pdfvOpenDocument. pdfvOpenDocumentHere is the reader itself, for the
//     places the reader was picked by name (Open with… → Reader, Y on a
//     manual). When KOReader does not start, the reader opens after all.
//   - Files: a format JukaHub cannot show but KOReader reads (comics, DjVu,
//     FB2, MOBI/AZW, XPS, CHM, Word) opens in KOReader on A, after any default
//     app Open with… saved (features.go). Without KOReader such a file still
//     opens in the hex view, and a toast says once what would read it.
//   - The reader's Select menu has "Open in KOReader" (the page reached is
//     saved first) unless Open with's own row (openwith.go, from the App
//     Center) already offers it through a launch script that passes the book
//     on; a manual's list has Y for the reader "Open books with" did not pick.
//
// How the book gets there. A TrimUI app starts through its launch script,
// and a KOReader port's script may or may not hand "$1" on to reader.lua
// (KOReader's entry: reader.lua [options] path). koreaderPlanFor reads the
// script at the press: when it uses its arguments, the script runs with the
// book's path, so its environment and set-up stay exactly as the app wants.
// When it does not, KOReader's own entry runs — "luajit reader.lua <book>"
// from the folder holding reader.lua, with the app's libs folders on
// LD_LIBRARY_PATH and the script's literal exports — which is how every
// KOReader platform script starts it. Without either, KOReader starts plainly
// and a toast names the book to open from its file browser. The path is
// always one argv element, never shell text (extAppLaunchWith).
//
// Coming back: the launch is the Apps screen's hand-off (extapps.go), so
// JukaHub waits parked and resumes on the same screen, with the reader's
// document and page as they were, when KOReader quits.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// koreaderBooksKey is the Custom entry behind "Open books with".
const koreaderBooksKey = "openBooksWith"

// Its two values; anything else reads as JukaHub's reader.
const (
	koreaderBooksJukaHub  = "jukahub"
	koreaderBooksKOReader = "koreader"
)

// koreaderMenuID is the reader menu's "Open in KOReader" row (pdfv_view.go).
const koreaderMenuID = "koreader"

// koreaderOnlyTypes are the formats KOReader reads that JukaHub has no viewer
// for (PDF and EPUB are JukaHub's reader's).
var koreaderOnlyTypes = map[string]bool{
	".cbz": true, ".cbr": true, ".cb7": true, ".cbt": true,
	".djvu": true, ".djv": true, ".fb2": true, ".fbz": true,
	".mobi": true, ".prc": true, ".azw": true, ".azw3": true, ".pdb": true,
	".xps": true, ".oxps": true, ".chm": true,
	".doc": true, ".docx": true, ".odt": true,
}

// koreaderComicTypes word the install hint for a comic.
var koreaderComicTypes = map[string]bool{".cbz": true, ".cbr": true, ".cb7": true, ".cbt": true}

// koreaderApp is the installed KOReader, from memory (extAppFind never
// touches the card on this thread: rows drawn every frame ask it).
func koreaderApp() (extApp, bool) {
	if IsWindows() {
		return extApp{}, false
	}
	return extAppFind(extAppKOReader)
}

// koreaderAppNow is koreaderApp for a press: right after start-up, before
// the first scan landed, it scans instead of answering "not installed".
func koreaderAppNow() (extApp, bool) {
	if IsWindows() {
		return extApp{}, false
	}
	return extAppFindNow(extAppKOReader)
}

// koreaderAvailable reports whether KOReader is on the card.
func koreaderAvailable() bool {
	_, ok := koreaderApp()
	return ok
}

// koreaderBooksChoice is "Open books with" as stored.
func koreaderBooksChoice(c *Config) string {
	if c == nil || c.Variables.Custom == nil {
		return koreaderBooksJukaHub
	}
	if v, ok := customString(c.Variables.Custom, koreaderBooksKey); ok && strings.EqualFold(strings.TrimSpace(v), koreaderBooksKOReader) {
		return koreaderBooksKOReader
	}
	return koreaderBooksJukaHub
}

// koreaderTakesBooks reports whether PDF and EPUB go to KOReader now: the
// setting says so and KOReader is still on the card. For a press (it may scan
// once, right after start-up; the setting is read first).
func koreaderTakesBooks(c *Config) bool {
	if koreaderBooksChoice(c) != koreaderBooksKOReader {
		return false
	}
	_, ok := koreaderAppNow()
	return ok
}

// koreaderOpenBook is pdfvOpenDocument's hook: a PDF or EPUB opens in
// KOReader when "Open books with" says so. False leaves it to the reader (the
// setting is JukaHub's reader, the file cannot be resolved, KOReader did not
// start).
func koreaderOpenBook(config *Config, path string) bool {
	if config == nil || !pdfIsDocumentPath(path) || !koreaderTakesBooks(config) {
		return false
	}
	resolved, err := resolveExplorerEntry(config, path)
	if err != nil || !regularFile(resolved) {
		return false // the reader explains why it cannot open it
	}
	return koreaderOpen(resolved)
}

// koreaderFilesOpen is A in Files on a format only KOReader reads: it opens
// there. False leaves the file to JukaHub (the hex view); without KOReader a
// toast says once per session what would read it.
func koreaderFilesOpen(config *Config, entry FileEntry) bool {
	if config == nil || entry.IsDir || entry.Name == ".." {
		return false
	}
	ext := strings.ToLower(filepath.Ext(entry.Name))
	if !koreaderOnlyTypes[ext] || filesROMSystem(entry.Path) != "" {
		return false
	}
	if _, ok := koreaderAppNow(); !ok {
		if !IsWindows() && !koreaderHinted {
			koreaderHinted = true
			msg := "Install KOReader to read e-books like this one"
			if koreaderComicTypes[ext] {
				msg = "Install KOReader to read comics"
			}
			ShowToast(msg, ToastKindInfo)
		}
		return false
	}
	path, err := resolveExplorerEntry(config, entry.Path)
	if err != nil {
		return false // A's own path explains why the file cannot be opened
	}
	koreaderOpen(path)
	// A start that failed has said so; the hex view would be a second answer.
	return true
}

// koreaderHinted remembers that this session said what reads comics.
var koreaderHinted bool

// koreaderOpen hands a file to KOReader. It reports whether KOReader started;
// a failure has been shown.
func koreaderOpen(path string) bool {
	app, ok := koreaderAppNow()
	if !ok {
		ShowToast("KOReader is not on the card", ToastKindInfo)
		return false
	}
	if runningGameChildren.Load() > 0 {
		// A game (or another app) holds the screen.
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil || !regularFile(abs) {
		ShowToast("Cannot open "+filepath.Base(path), ToastKindError)
		return false
	}
	plan := koreaderPlanFor(app)
	switch plan.how {
	case koreaderViaScript:
		logInfo("apps", "KOReader: opening a %s through its launch script", filepath.Ext(abs))
		return extAppLaunch(app, abs)
	case koreaderViaEntry:
		logInfo("apps", "KOReader: its launch script ignores arguments; running reader.lua with a %s", filepath.Ext(abs))
		return extAppLaunchWith(app, plan.exe, plan.env, "reader.lua", abs)
	}
	logInfo("apps", "KOReader: no way to pass a file; starting it plainly")
	if !extAppLaunch(app) {
		return false
	}
	// A warning stays in the bell (notifRecordToast) for when KOReader is
	// closed again; the toast itself goes under KOReader's screen.
	ShowToast("KOReader's launcher does not take a file: open "+filepath.Base(abs)+" from its file browser", ToastKindWarning)
	return true
}

// How a book reaches KOReader (koreaderPlanFor).
const (
	koreaderViaNone   = ""
	koreaderViaScript = "script" // the launch script, with the path as $1
	koreaderViaEntry  = "entry"  // luajit reader.lua <path>
)

// koreaderPlan is how to start KOReader with a file.
type koreaderPlan struct {
	how string
	exe string   // the luajit binary (koreaderViaEntry)
	env []string // "NAME=value" for it
}

// koreaderScriptMax bounds the launch script read (a press reads it).
const koreaderScriptMax = 64 << 10

// koreaderPlanFor reads KOReader's launch script and folder.
func koreaderPlanFor(app extApp) koreaderPlan {
	script := ""
	if f, err := os.Open(app.Launch); err == nil {
		data, _ := io.ReadAll(io.LimitReader(f, koreaderScriptMax))
		_ = f.Close()
		script = string(data)
	}
	if koreaderScriptForwards(script) {
		return koreaderPlan{how: koreaderViaScript}
	}
	tree := extAppKOReaderTree(app.Dir)
	if tree == "" {
		return koreaderPlan{how: koreaderViaNone}
	}
	luajit := filepath.Join(tree, "luajit")
	if !regularFile(luajit) {
		return koreaderPlan{how: koreaderViaNone}
	}
	return koreaderPlan{how: koreaderViaEntry, exe: luajit, env: koreaderEntryEnv(app.Dir, tree, script)}
}

// koreaderScriptForwards reports whether a launch script hands its own
// arguments on: "$1", "$@" or "$*" (or their ${…} forms) outside single
// quotes, on a line that is not a comment or an echo. Then the book's path
// given to the script reaches reader.lua.
func koreaderScriptForwards(script string) bool {
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(koreaderDropSingleQuoted(strings.TrimSpace(line)))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		switch strings.Fields(line + " ")[0] {
		case "echo", "printf", "logger", ":":
			continue
		}
		for _, ref := range []string{"$1", "${1", "$@", "${@", "$*", "${*"} {
			if strings.Contains(line, ref) {
				return true
			}
		}
	}
	return false
}

// koreaderDropSingleQuoted removes the single-quoted parts of a shell line:
// nothing is expanded there (awk '{print $1}' is not an argument).
func koreaderDropSingleQuoted(line string) string {
	if !strings.Contains(line, "'") {
		return line
	}
	var b strings.Builder
	inSingle, inDouble := false, false
	for _, r := range line {
		switch {
		case r == '\'' && !inDouble:
			inSingle = !inSingle
			continue
		case r == '"' && !inSingle:
			inDouble = !inDouble
		}
		if !inSingle {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// koreaderEntryEnv is the environment KOReader's entry runs with: the
// script's exports whose values are plain text, and LD_LIBRARY_PATH led by
// the app's libs folders.
func koreaderEntryEnv(appDir, tree, script string) []string {
	var env []string
	scriptLD := ""
	for _, kv := range koreaderScriptExports(script) {
		if strings.HasPrefix(kv, "LD_LIBRARY_PATH=") {
			scriptLD = strings.TrimPrefix(kv, "LD_LIBRARY_PATH=")
			continue
		}
		env = append(env, kv)
	}
	var parts []string
	for _, d := range []string{filepath.Join(tree, "libs"), filepath.Join(appDir, "libs"), filepath.Join(appDir, "lib")} {
		dup := false
		for _, p := range parts {
			dup = dup || p == d
		}
		if !dup && dirExists(d) {
			parts = append(parts, d)
		}
	}
	if scriptLD != "" {
		parts = append(parts, scriptLD)
	}
	if len(parts) == 0 {
		return env
	}
	// Then the path any app started from JukaHub gets: the firmware's, not
	// JukaHub's own (childenv.go).
	for _, kv := range screenChildEnv(os.Environ()) {
		if v, ok := strings.CutPrefix(kv, "LD_LIBRARY_PATH="); ok && v != "" {
			parts = append(parts, v)
		}
	}
	return append(env, "LD_LIBRARY_PATH="+strings.Join(parts, ":"))
}

// koreaderScriptExports lists a script's "export NAME=value" lines whose value
// is plain text: no variable, command, quote inside or operator, so taking it
// needs no shell.
func koreaderScriptExports(script string) []string {
	var out []string
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "export ") {
			continue
		}
		name, value, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(line, "export ")), "=")
		if !ok || !koreaderEnvName(name) {
			continue
		}
		quoted := false
		if n := len(value); n >= 2 && (value[0] == '"' || value[0] == '\'') && value[n-1] == value[0] {
			value, quoted = value[1:n-1], true
		}
		if value == "" || strings.ContainsAny(value, "$`\\\"';&|<>(){}#\x00") || (!quoted && strings.ContainsAny(value, " \t")) {
			continue
		}
		out = append(out, name+"="+value)
	}
	return out
}

// koreaderEnvName reports whether s is a shell variable name.
func koreaderEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// --- The reader's menu (pdfv_view.go) ----------------------------------------

// koreaderReaderMenuRows adds "Open in KOReader" to the reader's Select menu
// when KOReader is on the card, before "Open with…". Open with's own row for
// it (owReaderMenuRows: the App Center's launch, the book as "$1") is kept
// while KOReader's launch script passes "$1" on; when it does not, or the row
// is "Get KOReader" for an app the App Center does not see installed, this
// row replaces it. The menu is built on a press, so the script may be read.
func koreaderReaderMenuRows(rows []filesMenuRow, path string) []filesMenuRow {
	app, ok := koreaderApp()
	if path == "" || !ok {
		return rows
	}
	row := filesMenuRow{ID: koreaderMenuID, Label: "Open in KOReader", Enabled: true}
	for i, r := range rows {
		if !strings.Contains(extAppFolded(r.Label), "koreader") {
			continue
		}
		if strings.HasPrefix(r.Label, "Get ") || koreaderPlanFor(app).how != koreaderViaScript {
			rows[i] = row
		}
		return rows
	}
	for i, r := range rows {
		if r.ID == "openwith" {
			return append(rows[:i:i], append([]filesMenuRow{row}, rows[i:]...)...)
		}
	}
	return append(rows, row)
}

// --- Settings → Media & playback ---------------------------------------------

// koreaderSettingsRow is "Open books with", shown while KOReader is on the
// card.
func koreaderSettingsRow() setRow {
	return setRow{
		ID: koreaderBooksKey, Label: "Open books with", Kind: setKindChoice,
		Detail: func(c *Config) string {
			if koreaderBooksChoice(c) == koreaderBooksKOReader {
				return "PDF and EPUB open in KOReader, like comics and other e-books"
			}
			return "PDF and EPUB open in JukaHub's reader; comics and other e-books in KOReader"
		},
		Visible: func(*Config) bool { return koreaderAvailable() },
		Options: func(*Config) []setOption {
			return []setOption{
				{Value: koreaderBooksJukaHub, Label: "JukaHub reader"},
				{Value: koreaderBooksKOReader, Label: "KOReader"},
			}
		},
		Current: func(c *Config) int {
			if koreaderBooksChoice(c) == koreaderBooksKOReader {
				return 1
			}
			return 0
		},
		Choose: func(c *Config, value string) {
			if c == nil {
				return
			}
			if c.Variables.Custom == nil {
				c.Variables.Custom = map[string]interface{}{}
			}
			label := "JukaHub reader"
			if value == koreaderBooksKOReader {
				label = "KOReader"
			} else {
				value = koreaderBooksJukaHub
			}
			c.Variables.Custom[koreaderBooksKey] = value
			saveUserConfigDebounced(userConfigFromVariables(c))
			setToast("Open books with: " + label)
		},
	}
}
