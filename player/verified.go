package main

// verified.go — "Verified on this device": what was really seen to work on
// this model, for each game, port, emulator and app.
//
// A compatibility list (compat.go) says a game should run well; Game Ready
// (gameready.go) says nothing is missing before it starts. Neither says it
// did run, here, the whole way through. Five things make a handheld game
// dependable, and each is a check with its own result:
//
//   - launch:   it started and kept running;
//   - controls: the pad drove it;
//   - sound:    it played audio;
//   - sleep:    it came back from the power key's sleep and wake;
//   - return:   quitting it brought JukaHub back with nothing left behind.
//
// A result comes from one of three places, and says which:
//
//   - "auto": JukaHub saw it happen during a session (verified_observe*.go);
//   - "user": the player said so on the game's Device check sheet or in the
//     guided device check (devcheck*.go);
//   - "catalog": a shipped list (verified_seed.json) or a JSON file copied to
//     <data>/verified/import — results from another device of the same model.
//
// A user result is not overwritten by an automatic one of the same check
// unless JukaHub or the emulator changed since: the person saw the problem,
// and a session that merely ran is weaker evidence than that.
//
// Every result carries what it was tested with: the JukaHub release, the
// firmware, the emulator or core and its version, and an ordering number
// (Seq) that needs no clock. A TrimUI often boots before its clock is set, so
// a date is written only when the clock is plausible (vfDateNow) and nothing
// is ever ordered or expired by date.
//
// Records are per model: a card moved from a Smart Pro to a Brick starts the
// Brick with no results of its own, and the Smart Pro's are there again when
// the card goes back. The file is <data>/verified/verified.json, written
// through AtomicWrite two seconds after the last change (vfSaveSoon) and at
// once by vfFlush.
//
// Everything here is safe from any goroutine. Lookups answer from memory (the
// game page draws its chip every frame): the file is read on a goroutine the
// first time anything asks (vfEnsureLoaded), and until then lookups report
// nothing. A change loads it first, synchronously, so no result is lost to a
// change that came before the read.

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	vfSchemaVersion = 1
	vfDirName       = "verified"
	vfFileName      = "verified.json"
	vfImportDirName = "import"
	// vfMaxRecords bounds the file: the oldest records (by Seq) go first.
	vfMaxRecords = 4000
	// vfMaxFileBytes bounds every file read here, the import folder's too.
	vfMaxFileBytes = 8 << 20
	// vfMaxImports bounds how many files the import folder contributes.
	vfMaxImports = 64
	// vfIssueMaxRunes and vfNoteMaxRunes keep the texts one card line or two.
	vfIssueMaxRunes = 280
	vfNoteMaxRunes  = 140
	vfIDMaxRunes    = 200
	// vfSaveDelay gathers the results of one session into one write.
	vfSaveDelay = 2 * time.Second
)

// Kinds of things a record describes.
const (
	vfKindGame     = "game"
	vfKindPort     = "port"
	vfKindEmulator = "emulator"
	vfKindApp      = "app"
)

// The five checks.
const (
	vfCheckLaunch   = "launch"
	vfCheckControls = "controls"
	vfCheckSound    = "sound"
	vfCheckSleep    = "sleep"
	vfCheckReturn   = "return"
)

// vfChecks is the order the checks are shown in.
var vfChecks = [...]string{vfCheckLaunch, vfCheckControls, vfCheckSound, vfCheckSleep, vfCheckReturn}

// Result states. A check with no result is untested.
const (
	vfPass = "pass"
	vfFail = "fail"
)

// Where a result came from.
const (
	vfByAuto    = "auto"
	vfByUser    = "user"
	vfByCatalog = "catalog"
)

// Verdict levels, in the order they outrank each other.
const (
	vfLevelUntested = iota
	vfLevelPartial
	vfLevelVerified
	vfLevelIssue
)

// vfResult is one check's result.
type vfResult struct {
	State    string `json:"state"`
	By       string `json:"by"`
	Note     string `json:"note,omitempty"`
	Version  string `json:"version,omitempty"`
	Firmware string `json:"firmware,omitempty"`
	Engine   string `json:"engine,omitempty"`
	Date     string `json:"date,omitempty"`
	Seq      int64  `json:"seq,omitempty"`
}

// vfIssue is a known problem in words: the player's own, or a catalog's.
type vfIssue struct {
	Text    string `json:"text"`
	By      string `json:"by"`
	Version string `json:"version,omitempty"`
	Date    string `json:"date,omitempty"`
	Seq     int64  `json:"seq,omitempty"`
}

// vfRecord is everything known about one thing on one model. Engine is the
// emulator or core (with its version) the last session used; IssueDismissed
// hides a catalog's issue the player no longer sees.
type vfRecord struct {
	Kind           string              `json:"kind"`
	ID             string              `json:"id"`
	Title          string              `json:"title,omitempty"`
	Model          string              `json:"model"`
	Engine         string              `json:"engine,omitempty"`
	Sessions       int                 `json:"sessions,omitempty"`
	Checks         map[string]vfResult `json:"checks,omitempty"`
	Issue          *vfIssue            `json:"issue,omitempty"`
	IssueDismissed bool                `json:"issueDismissed,omitempty"`
	Seq            int64               `json:"seq,omitempty"`
}

// vfFile is verified.json, and the format of the seed, an import and an
// export (their seq is ignored).
type vfFile struct {
	SchemaVersion int        `json:"schemaVersion"`
	Seq           int64      `json:"seq,omitempty"`
	Records       []vfRecord `json:"records"`
}

// vfVerdict is a record in one chip and one line.
type vfVerdict struct {
	Level  int
	Text   string
	Detail string
	Tone   int
	Passed int
	Failed int
	// Stale is set when the newest result was seen on another JukaHub
	// release than the one running: still shown, with that release named.
	Stale bool
}

// vfSeedJSON is the list shipped with JukaHub: results from the maintainers'
// own devices, by model (export-*.json from the Tested on this device page,
// reviewed and copied over this file).
//
//go:embed verified_seed.json
var vfSeedJSON []byte

var vfStore struct {
	mu      sync.Mutex
	loaded  bool
	loading bool
	dirty   bool
	file    vfFile
	index   map[string]int
	catalog map[string]vfRecord
	issues  []string
	timer   *time.Timer
	// frozen: verified.json was written by a newer JukaHub (a release the
	// self-repair rolled back from). It is left as it is: results seen now
	// are shown but not saved over it.
	frozen bool
}

// vfReadMu lets one read of the files run at a time, so the first read in
// the background and a change's vfLoadSync never both read (and never both
// move a damaged file aside). vfWriteMu does the same for vfFlush, so an
// older snapshot never lands after a newer one and the exit's flush waits
// for a write the timer started. Both are taken before vfStore.mu, never
// under it.
var vfReadMu, vfWriteMu sync.Mutex

// errVfNewerSchema is vfParse's error for a file from a newer JukaHub.
var errVfNewerSchema = errors.New("written by a newer JukaHub")

// vfGeneration changes with every change, so a screen that keeps a list
// knows when to read it again.
var vfGeneration atomic.Int64

// vfDirOverride points the store at a test folder.
var vfDirOverride string

// vfDir is <data>/verified.
func vfDir() string {
	if vfDirOverride != "" {
		return vfDirOverride
	}
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return filepath.Join("data", vfDirName)
	}
	return filepath.Join(dir, vfDirName)
}

func vfPath() string { return filepath.Join(vfDir(), vfFileName) }

// vfModel is the model results are kept under.
func vfModel() string { return string(DetectDevice()) }

// vfModelShort names a model for a chip: "Smart Pro", "Brick"; "this PC" on
// Windows and "this device" for anything unknown.
func vfModelShort(model string) string {
	d := DeviceModel(model)
	if d.IsTrimui() {
		name := strings.TrimPrefix(deviceDisplayName(d), "TrimUI ")
		if i := strings.Index(name, " ("); i > 0 {
			name = name[:i]
		}
		if name != "" && name != "Unknown" {
			return name
		}
	}
	if runtime.GOOS == "windows" {
		return "this PC"
	}
	return "this device"
}

// vfMapKey keys a record by model, kind and id.
func vfMapKey(model, kind, id string) string { return model + "\x00" + kind + "\x00" + id }

// vfNormalizeID makes an id comparable: lower case, forward slashes, single
// spaces, bounded.
func vfNormalizeID(id string) string {
	id = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(id, "\\", "/")))
	id = strings.Join(strings.Fields(id), " ")
	return vfClip(id, vfIDMaxRunes)
}

// vfClip cuts s to max runes.
func vfClip(s string, max int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:max-1])) + "…"
}

// vfGameRef names a library game: a port by its script's name, any other
// game by its console and file name — not the folder, so a card moved to
// another path or another profile keeps its results.
func vfGameRef(game libraryGame) (kind, id, title string) {
	base := filepath.Base(strings.ReplaceAll(game.Path, "\\", "/"))
	title = game.Label()
	if strings.EqualFold(strings.TrimSpace(game.System), portsLibrarySystem) {
		return vfKindPort, vfNormalizeID(strings.TrimSuffix(base, filepath.Ext(base))), title
	}
	system := canonicalCompatSystem(game.System)
	if system == "" {
		system = "unknown"
	}
	return vfKindGame, vfNormalizeID(system + "/" + base), title
}

// vfAppRef names an app by the program or script that starts it: a generic
// launcher name (launch.sh, start.sh, run.sh…) is named after its folder.
func vfAppRef(path string) (kind, id, title string) {
	path = strings.ReplaceAll(strings.TrimSpace(path), "\\", "/")
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	switch strings.ToLower(stem) {
	case "launch", "start", "run", "main", "app", "go", "default":
		if dir := filepath.Base(filepath.Dir(path)); dir != "" && dir != "." && dir != "/" {
			stem = strings.TrimSuffix(dir, ".pak")
		}
	}
	return vfKindApp, vfNormalizeID(stem), stem
}

// vfEmulatorRef names an emulator or core ("mGBA", "PPSSPP") as its record.
func vfEmulatorRef(name string) (kind, id, title string) {
	name = strings.TrimSpace(name)
	return vfKindEmulator, vfNormalizeID(name), name
}

// vfVersionNow is the running JukaHub release.
func vfVersionNow() string {
	if tag := strings.TrimSpace(readInstalledReleaseInfo().Tag); tag != "" {
		return tag
	}
	if d := strings.TrimSpace(buildDate); d != "" {
		return "build " + d
	}
	return "development build"
}

// vfVersionLabel shows a release: "JukaHub-20261119041211" → "2026-11-19
// build"; anything else as it is.
func vfVersionLabel(v string) string {
	v = strings.TrimSpace(v)
	digits := strings.TrimPrefix(v, "JukaHub-")
	if digits != v && len(digits) >= 8 {
		if _, err := strconv.Atoi(digits[:8]); err == nil {
			return digits[:4] + "-" + digits[4:6] + "-" + digits[6:8] + " build"
		}
	}
	return v
}

// vfDateNow is today's date when the clock is plausible: not before this
// build was made (less two days for time zones and build machines). A
// factory clock after a cold boot is years earlier and gives "".
func vfDateNow() string {
	now := time.Now()
	if now.Year() < 2025 {
		return ""
	}
	if built, ok := parseReleaseTimestamp(buildTime); ok && now.Before(built.Add(-48*time.Hour)) {
		return ""
	}
	return now.Format("2006-01-02")
}

// vfCheckLabel is a check's name on screen.
func vfCheckLabel(check string) string {
	switch check {
	case vfCheckLaunch:
		return "Starts"
	case vfCheckControls:
		return "Controls"
	case vfCheckSound:
		return "Sound"
	case vfCheckSleep:
		return "Sleep and wake"
	case vfCheckReturn:
		return "Quits back"
	}
	return check
}

func vfValidCheck(check string) bool {
	for _, c := range vfChecks {
		if c == check {
			return true
		}
	}
	return false
}

func vfValidKind(kind string) bool {
	switch kind {
	case vfKindGame, vfKindPort, vfKindEmulator, vfKindApp:
		return true
	}
	return false
}

// ── loading ───────────────────────────────────────────────────────────────

// vfEnsureLoaded starts the first read; it never waits.
func vfEnsureLoaded() {
	vfStore.mu.Lock()
	if vfStore.loaded || vfStore.loading {
		vfStore.mu.Unlock()
		return
	}
	vfStore.loading = true
	vfStore.mu.Unlock()
	go vfReadInstall()
}

// vfReadInstall reads the files and installs them, unless a read that ran
// first already did.
func vfReadInstall() {
	vfReadMu.Lock()
	defer vfReadMu.Unlock()
	if vfLoaded() {
		return
	}
	file, catalog, issues := vfReadAll()
	vfStore.mu.Lock()
	vfInstallLocked(file, catalog, issues)
	vfStore.mu.Unlock()
	vfGeneration.Add(1)
}

// vfLoaded reports whether lookups can answer yet.
func vfLoaded() bool {
	vfStore.mu.Lock()
	defer vfStore.mu.Unlock()
	return vfStore.loaded
}

// vfLoadSync reads the files now, for a change that cannot wait, or waits
// for the read already under way. The read happens outside vfStore.mu, so
// the game page's per-frame lookup never waits for the card.
func vfLoadSync() {
	if vfLoaded() {
		return
	}
	vfReadInstall()
}

// vfInstallLocked takes what vfReadAll read, unless a change already did.
func vfInstallLocked(file vfFile, catalog map[string]vfRecord, issues []string) {
	if vfStore.loaded {
		return
	}
	vfStore.file = file
	vfStore.catalog = catalog
	vfStore.issues = issues
	vfStore.frozen = file.SchemaVersion > vfSchemaVersion
	vfReindexLocked()
	vfStore.loaded = true
	vfStore.loading = false
}

func vfReindexLocked() {
	vfStore.index = make(map[string]int, len(vfStore.file.Records))
	for i, r := range vfStore.file.Records {
		vfStore.index[vfMapKey(r.Model, r.Kind, r.ID)] = i
	}
}

// vfReadAll reads verified.json, the shipped seed and the import folder.
// Problems are collected as one-line issues for the page; a damaged file
// is kept aside as verified.json.damaged and the store starts empty.
func vfReadAll() (vfFile, map[string]vfRecord, []string) {
	var issues []string
	file := vfFile{SchemaVersion: vfSchemaVersion}
	if data, err := vfReadBounded(vfPath()); err == nil {
		parsed, perr := vfParse(data)
		switch {
		case errors.Is(perr, errVfNewerSchema):
			// Kept as it is for the newer release; nothing is saved over it.
			issues = append(issues, "verified.json was written by a newer JukaHub; it is kept as it is, and results seen now are not saved")
			file.SchemaVersion = parsed.SchemaVersion
		case perr != nil:
			issues = append(issues, "verified.json could not be read ("+perr.Error()+"); it was kept as verified.json.damaged")
			_ = os.Rename(vfPath(), vfPath()+".damaged")
		default:
			file.Seq = parsed.Seq
			file.Records = parsed.Records
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		// Moved aside too, so the next save does not write over it.
		issues = append(issues, "verified.json: "+err.Error()+"; it was kept as verified.json.damaged")
		_ = os.Rename(vfPath(), vfPath()+".damaged")
	}
	for _, r := range file.Records {
		if r.Seq > file.Seq {
			file.Seq = r.Seq
		}
	}
	catalog := make(map[string]vfRecord)
	addCatalog := func(name string, data []byte) {
		parsed, err := vfParse(data)
		if err != nil {
			issues = append(issues, name+": "+err.Error())
			return
		}
		for _, r := range parsed.Records {
			// Another device's ordering numbers mean nothing here: a
			// catalog result never counts as newer than this device's own
			// (vfVerdictFor's newest, the device check's baseline).
			r.Seq = 0
			for check, res := range r.Checks {
				res.By, res.Seq = vfByCatalog, 0
				r.Checks[check] = res
			}
			if r.Issue != nil {
				r.Issue.By, r.Issue.Seq = vfByCatalog, 0
			}
			key := vfMapKey(r.Model, r.Kind, r.ID)
			catalog[key] = vfMerge(catalog[key], r)
		}
	}
	if len(vfSeedJSON) > 0 {
		addCatalog("verified_seed.json", vfSeedJSON)
	}
	importDir := filepath.Join(vfDir(), vfImportDirName)
	if entries, err := os.ReadDir(importDir); err == nil {
		n := 0
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
				continue
			}
			if n++; n > vfMaxImports {
				issues = append(issues, "import: only the first "+strconv.Itoa(vfMaxImports)+" files are read")
				break
			}
			data, err := vfReadBounded(filepath.Join(importDir, e.Name()))
			if err != nil {
				issues = append(issues, e.Name()+": "+err.Error())
				continue
			}
			addCatalog(e.Name(), data)
		}
	}
	return file, catalog, issues
}

// vfReadBounded reads a file no larger than vfMaxFileBytes.
func vfReadBounded(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("is a folder")
	}
	if info.Size() > vfMaxFileBytes {
		return nil, fmt.Errorf("larger than %d MB", vfMaxFileBytes>>20)
	}
	return os.ReadFile(path)
}

// vfParse reads one file and keeps only well-formed records: a known kind,
// an id, a model, known checks with a pass or fail, texts within bounds.
func vfParse(data []byte) (vfFile, error) {
	var f vfFile
	if err := json.Unmarshal(data, &f); err != nil {
		return vfFile{}, fmt.Errorf("not valid JSON")
	}
	if f.SchemaVersion > vfSchemaVersion {
		return vfFile{SchemaVersion: f.SchemaVersion}, fmt.Errorf("%w (schema %d)", errVfNewerSchema, f.SchemaVersion)
	}
	kept := f.Records[:0]
	for _, r := range f.Records {
		r.Kind = strings.ToLower(strings.TrimSpace(r.Kind))
		r.ID = vfNormalizeID(r.ID)
		r.Model = strings.TrimSpace(r.Model)
		if !vfValidKind(r.Kind) || r.ID == "" || r.Model == "" {
			continue
		}
		r.Title = vfClip(r.Title, vfIDMaxRunes)
		r.Engine = vfClip(r.Engine, vfNoteMaxRunes)
		checks := make(map[string]vfResult, len(r.Checks))
		for check, res := range r.Checks {
			if !vfValidCheck(check) || (res.State != vfPass && res.State != vfFail) {
				continue
			}
			if res.By != vfByAuto && res.By != vfByUser && res.By != vfByCatalog {
				res.By = vfByCatalog
			}
			res.Note = vfClip(res.Note, vfNoteMaxRunes)
			res.Engine = vfClip(res.Engine, vfNoteMaxRunes)
			res.Version = vfClip(res.Version, 64)
			res.Firmware = vfClip(res.Firmware, 64)
			res.Date = vfClip(res.Date, 10)
			checks[check] = res
		}
		r.Checks = checks
		if r.Issue != nil {
			r.Issue.Text = vfClip(r.Issue.Text, vfIssueMaxRunes)
			if r.Issue.Text == "" {
				r.Issue = nil
			}
		}
		if r.Sessions < 0 {
			r.Sessions = 0
		}
		kept = append(kept, r)
	}
	f.Records = kept
	return f, nil
}

// vfMerge lays over on top of base: each of over's checks replaces base's,
// over's issue replaces base's, and over's dismissal hides base's issue.
func vfMerge(base, over vfRecord) vfRecord {
	if base.ID == "" {
		return vfCopy(over)
	}
	out := vfCopy(base)
	if over.Title != "" {
		out.Title = over.Title
	}
	if over.Engine != "" {
		out.Engine = over.Engine
	}
	out.Sessions += over.Sessions
	if over.Seq > out.Seq {
		out.Seq = over.Seq
	}
	if out.Checks == nil {
		out.Checks = make(map[string]vfResult, len(over.Checks))
	}
	for check, res := range over.Checks {
		out.Checks[check] = res
	}
	if over.Issue != nil {
		issue := *over.Issue
		out.Issue = &issue
	} else if over.IssueDismissed {
		out.Issue = nil
	}
	out.IssueDismissed = out.IssueDismissed || over.IssueDismissed
	return out
}

// vfCopy is a deep copy, so a caller never shares the store's maps.
func vfCopy(r vfRecord) vfRecord {
	out := r
	if r.Checks != nil {
		out.Checks = make(map[string]vfResult, len(r.Checks))
		for k, v := range r.Checks {
			out.Checks[k] = v
		}
	}
	if r.Issue != nil {
		issue := *r.Issue
		out.Issue = &issue
	}
	return out
}

// ── reading ───────────────────────────────────────────────────────────────

// vfLookup is what is known about kind/id on this model: the catalog's
// results with this device's own on top. ok is false when nothing is known
// (or the file is still being read).
func vfLookup(kind, id string) (vfRecord, bool) {
	vfEnsureLoaded()
	model := vfModel()
	key := vfMapKey(model, kind, vfNormalizeID(id))
	vfStore.mu.Lock()
	defer vfStore.mu.Unlock()
	if !vfStore.loaded {
		return vfRecord{}, false
	}
	cat, haveCat := vfStore.catalog[key]
	var local vfRecord
	i, haveLocal := vfStore.index[key]
	if haveLocal {
		local = vfStore.file.Records[i]
	}
	switch {
	case haveCat && haveLocal:
		return vfMerge(cat, local), true
	case haveLocal:
		return vfCopy(local), true
	case haveCat:
		return vfCopy(cat), true
	}
	return vfRecord{}, false
}

// vfList is every record of kind ("" for all) known on this model, sorted
// by kind and title.
func vfList(kind string) []vfRecord {
	vfEnsureLoaded()
	model := vfModel()
	vfStore.mu.Lock()
	byKey := make(map[string]vfRecord)
	for key, r := range vfStore.catalog {
		if r.Model == model && (kind == "" || r.Kind == kind) {
			byKey[key] = vfCopy(r)
		}
	}
	for _, r := range vfStore.file.Records {
		if r.Model != model || (kind != "" && r.Kind != kind) {
			continue
		}
		key := vfMapKey(r.Model, r.Kind, r.ID)
		byKey[key] = vfMerge(byKey[key], r)
	}
	vfStore.mu.Unlock()
	out := make([]vfRecord, 0, len(byKey))
	for _, r := range byKey {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		a, b := strings.ToLower(setOr(out[i].Title, out[i].ID)), strings.ToLower(setOr(out[j].Title, out[j].ID))
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// vfIssues are the problems the last read found, for the page.
func vfIssues() []string {
	vfStore.mu.Lock()
	defer vfStore.mu.Unlock()
	return append([]string(nil), vfStore.issues...)
}

// vfVerdictFor sums a record up. An issue or a failed check outranks
// everything; all five passed is Verified; some is Partly.
func vfVerdictFor(r vfRecord) vfVerdict {
	model := vfModelShort(setOr(r.Model, vfModel()))
	v := vfVerdict{Tone: gamePageToneNeutral}
	var newest vfResult
	var failed []string
	for _, check := range vfChecks {
		res, ok := r.Checks[check]
		if !ok {
			continue
		}
		if res.Seq >= newest.Seq {
			newest = res
		}
		switch res.State {
		case vfPass:
			v.Passed++
		case vfFail:
			v.Failed++
			failed = append(failed, strings.ToLower(vfCheckLabel(check)))
		}
	}
	if newest.Version != "" && newest.Version != vfVersionNow() {
		v.Stale = true
	}
	tested := ""
	if newest.Version != "" {
		tested = "tested on the " + vfVersionLabel(newest.Version)
	}
	switch {
	case r.Issue != nil || v.Failed > 0:
		v.Level = vfLevelIssue
		v.Text = "Known issue"
		v.Tone = gamePageToneWarn
		if r.Issue != nil {
			v.Detail = r.Issue.Text
		} else {
			v.Detail = "Problem with " + strings.Join(failed, ", ") + " on " + model
		}
	case v.Passed == len(vfChecks):
		v.Level = vfLevelVerified
		v.Text = "Verified on " + model
		v.Tone = gamePageToneGood
		v.Detail = "All five checks passed"
	case v.Passed > 0:
		v.Level = vfLevelPartial
		v.Text = strconv.Itoa(v.Passed) + " of 5 checks on " + model
		v.Detail = "Not every check has been seen yet"
	default:
		v.Level = vfLevelUntested
		v.Text = "Not tested on " + model
		v.Detail = "Play it once and JukaHub notes what works"
	}
	if tested != "" {
		v.Detail += " · " + tested
	}
	if engine := setOr(newest.Engine, r.Engine); engine != "" {
		v.Detail += " · " + engine
	}
	return v
}

// ── changing ──────────────────────────────────────────────────────────────

// vfStampResult fills in what a result was tested with.
func vfStampResult(res *vfResult) {
	res.Version = vfVersionNow()
	res.Firmware = vfClip(gameLabFirmware(), 64)
	res.Date = vfDateNow()
}

// vfRecordLocked finds or makes this model's own record for kind/id.
func vfRecordLocked(kind, id, title string) *vfRecord {
	model := vfModel()
	key := vfMapKey(model, kind, id)
	if i, ok := vfStore.index[key]; ok {
		r := &vfStore.file.Records[i]
		if title = vfClip(title, vfIDMaxRunes); title != "" {
			r.Title = title
		}
		return r
	}
	vfStore.file.Records = append(vfStore.file.Records, vfRecord{
		Kind: kind, ID: id, Title: vfClip(title, vfIDMaxRunes), Model: model, Checks: map[string]vfResult{},
	})
	vfStore.index[key] = len(vfStore.file.Records) - 1
	return &vfStore.file.Records[len(vfStore.file.Records)-1]
}

// vfChange runs fn on this model's record for kind/id with the store loaded
// and locked, then saves soon. fn returns false when it changed nothing.
func vfChange(kind, id, title string, fn func(r *vfRecord, seq int64) bool) bool {
	id = vfNormalizeID(id)
	if !vfValidKind(kind) || id == "" {
		return false
	}
	vfLoadSync()
	vfStore.mu.Lock()
	_, existed := vfStore.index[vfMapKey(vfModel(), kind, id)]
	r := vfRecordLocked(kind, id, title)
	if r.Checks == nil {
		r.Checks = map[string]vfResult{}
	}
	seq := vfStore.file.Seq + 1
	changed := fn(r, seq)
	switch {
	case changed:
		vfStore.file.Seq = seq
		r.Seq = seq
		vfStore.dirty = true
		vfTrimLocked()
	case !existed:
		// Nothing to keep: the record made for this call goes again.
		vfStore.file.Records = vfStore.file.Records[:len(vfStore.file.Records)-1]
		vfReindexLocked()
	}
	vfStore.mu.Unlock()
	if changed {
		vfGeneration.Add(1)
		vfSaveSoon()
	}
	return changed
}

// vfBeginSession counts a session of kind/id and notes its engine (the
// emulator or core and its version; "" when not known). It returns the
// session's ordering number.
func vfBeginSession(kind, id, title, engine string) int64 {
	var got int64
	vfChange(kind, id, title, func(r *vfRecord, seq int64) bool {
		r.Sessions++
		if engine = vfClip(engine, vfNoteMaxRunes); engine != "" {
			r.Engine = engine
		}
		got = seq
		return true
	})
	return got
}

// vfNote records one check's result. by is vfByAuto or vfByUser; note says
// what was seen ("ran 12 minutes", "stopped 3 s after waking"); engine is the
// emulator or core and version ("" keeps the record's). An automatic result
// does not replace the player's own for the same check unless the JukaHub
// release or the engine changed since. It reports whether anything changed.
func vfNote(kind, id, title, check, state, by, note, engine string) bool {
	if !vfValidCheck(check) || (state != vfPass && state != vfFail) || (by != vfByAuto && by != vfByUser) {
		return false
	}
	// Stamped before the store's lock: the release stamp may be read from
	// the card.
	stamp := vfResult{State: state, By: by, Note: vfClip(note, vfNoteMaxRunes)}
	vfStampResult(&stamp)
	return vfChange(kind, id, title, func(r *vfRecord, seq int64) bool {
		if engine = vfClip(engine, vfNoteMaxRunes); engine == "" {
			engine = r.Engine
		}
		res := stamp
		res.Engine, res.Seq = engine, seq
		if old, ok := r.Checks[check]; ok {
			if by == vfByAuto && old.By == vfByUser && old.Version == res.Version && old.Engine == res.Engine {
				return false
			}
			if old.State == res.State && old.By == res.By && old.Version == res.Version &&
				old.Engine == res.Engine && old.Note == res.Note {
				return false
			}
		}
		r.Checks[check] = res
		return true
	})
}

// vfClearCheck forgets one check's result on this device.
func vfClearCheck(kind, id, check string) bool {
	return vfChange(kind, id, "", func(r *vfRecord, _ int64) bool {
		if _, ok := r.Checks[check]; !ok {
			return false
		}
		delete(r.Checks, check)
		return true
	})
}

// vfSetIssue writes the known issue for kind/id in the player's (or a
// check's) words; an empty text clears it and also hides a catalog's issue
// on this device.
func vfSetIssue(kind, id, title, text, by string) bool {
	if by != vfByAuto && by != vfByUser {
		by = vfByUser
	}
	text = vfClip(text, vfIssueMaxRunes)
	version, date := vfVersionNow(), vfDateNow()
	return vfChange(kind, id, title, func(r *vfRecord, seq int64) bool {
		if text == "" {
			if r.Issue == nil && r.IssueDismissed {
				return false
			}
			r.Issue = nil
			r.IssueDismissed = true
			return true
		}
		r.Issue = &vfIssue{Text: text, By: by, Version: version, Date: date, Seq: seq}
		r.IssueDismissed = false
		return true
	})
}

// vfForget deletes this device's own record for kind/id; the catalog's
// results, if any, show again.
func vfForget(kind, id string) bool {
	id = vfNormalizeID(id)
	key := vfMapKey(vfModel(), kind, id)
	vfLoadSync()
	vfStore.mu.Lock()
	i, ok := vfStore.index[key]
	if ok {
		vfStore.file.Records = append(vfStore.file.Records[:i], vfStore.file.Records[i+1:]...)
		vfReindexLocked()
		vfStore.dirty = true
	}
	vfStore.mu.Unlock()
	if ok {
		vfGeneration.Add(1)
		vfSaveSoon()
	}
	return ok
}

// vfTrimLocked drops the oldest records past vfMaxRecords.
func vfTrimLocked() {
	if len(vfStore.file.Records) <= vfMaxRecords {
		return
	}
	sort.SliceStable(vfStore.file.Records, func(i, j int) bool {
		return vfStore.file.Records[i].Seq > vfStore.file.Records[j].Seq
	})
	vfStore.file.Records = vfStore.file.Records[:vfMaxRecords]
	vfReindexLocked()
}

// ── writing ───────────────────────────────────────────────────────────────

// vfSaveSoon writes the file vfSaveDelay after the last change.
func vfSaveSoon() {
	vfStore.mu.Lock()
	defer vfStore.mu.Unlock()
	if vfStore.timer != nil {
		vfStore.timer.Stop()
	}
	vfStore.timer = time.AfterFunc(vfSaveDelay, func() { _ = vfFlush() })
}

// vfFlush writes the file now if anything changed. Any goroutine; it waits
// for the card.
func vfFlush() error {
	vfWriteMu.Lock()
	defer vfWriteMu.Unlock()
	vfStore.mu.Lock()
	if !vfStore.loaded || !vfStore.dirty || vfStore.frozen {
		vfStore.mu.Unlock()
		return nil
	}
	f := vfFile{SchemaVersion: vfSchemaVersion, Seq: vfStore.file.Seq}
	f.Records = make([]vfRecord, len(vfStore.file.Records))
	for i, r := range vfStore.file.Records {
		f.Records[i] = vfCopy(r)
	}
	vfStore.dirty = false
	vfStore.mu.Unlock()
	data, err := json.MarshalIndent(f, "", " ")
	if err == nil {
		err = AtomicWrite(vfPath(), data, 0o644)
	}
	if err != nil {
		vfStore.mu.Lock()
		vfStore.dirty = true
		vfStore.mu.Unlock()
		logWarn("verified", "could not save the device results: %v", err)
	}
	return err
}

// vfExport writes this model's own results as a file another device, or
// the next JukaHub release (verified_seed.json), can import, and returns its
// path. Off the main thread: it writes to the card.
func vfExport() (string, int, error) {
	model := vfModel()
	vfLoadSync()
	vfStore.mu.Lock()
	f := vfFile{SchemaVersion: vfSchemaVersion}
	for _, r := range vfStore.file.Records {
		if r.Model == model && (len(r.Checks) > 0 || r.Issue != nil) {
			c := vfCopy(r)
			c.IssueDismissed = false
			f.Records = append(f.Records, c)
		}
	}
	vfStore.mu.Unlock()
	if len(f.Records) == 0 {
		return "", 0, fmt.Errorf("nothing has been tested on this device yet")
	}
	data, err := json.MarshalIndent(f, "", " ")
	if err != nil {
		return "", 0, err
	}
	name := "export-" + strings.ToLower(strings.ReplaceAll(vfModelShort(model), " ", "-"))
	if d := vfDateNow(); d != "" {
		name += "-" + d
	}
	path := filepath.Join(vfDir(), name+".json")
	if err := AtomicWrite(path, data, 0o644); err != nil {
		return "", 0, err
	}
	return path, len(f.Records), nil
}
