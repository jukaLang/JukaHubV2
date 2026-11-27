package main

// gamelab_share.go — a Game Lab setup as a file another player can use: a
// compatibility profile.
//
// "Share" in the Lab writes the chosen setup (the one the player applied, else
// the one the verdict favours), what it measured, and where it was measured —
// device model, firmware, emulator and core — with the game's identity, to
// data/game-lab/export/<system>-<title>.json. The schema is fixed and
// versioned ("schema": "jukahub.gamelab.profile", "schemaVersion": 1); no
// path of this machine goes into it, only the emulator's name, its launcher's
// folder and file, and the game's file name. The game's CRC32 is included
// when the library already knows it (the game's metadata record): a big disc
// image is never hashed for this.
//
// Files dropped into data/game-lab/import are read when a Lab opens (at most
// gameLabImportFiles of them, each at most gameLabImportMax bytes), checked
// against the schema with every number held to a sane range and every string
// cut and cleaned, and the ones for this game (same system, and the same CRC
// when both sides know it, else the same normalised title) are listed under
// "Shared setups". Choosing one loads it into setup B — only when its
// emulator is installed here, matched by core, name or launcher — and never
// starts anything: the player runs B to see it for themselves.

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/skip2/go-qrcode"
)

const (
	gameLabProfileSchema  = "jukahub.gamelab.profile"
	gameLabProfileVersion = 1
	// gameLabImportMax bounds one profile file, gameLabImportFiles how many
	// are read, gameLabImportShown how many one game lists.
	gameLabImportMax   = 64 << 10
	gameLabImportFiles = 200
	gameLabImportShown = 6
)

// gameLabProfile is the shared file.
type gameLabProfile struct {
	Schema        string                `json:"schema"`
	SchemaVersion int                   `json:"schemaVersion"`
	Device        gameLabProfileDevice  `json:"device"`
	Game          gameLabProfileGame    `json:"game"`
	Setup         gameLabProfileSetup   `json:"setup"`
	Results       gameLabProfileResults `json:"results"`
}

type gameLabProfileDevice struct {
	Model    string `json:"model"`
	Firmware string `json:"firmware,omitempty"`
	OS       string `json:"os,omitempty"`
}

type gameLabProfileGame struct {
	System string `json:"system"`
	Title  string `json:"title"`
	File   string `json:"file,omitempty"`
	Size   int64  `json:"size,omitempty"`
	CRC32  string `json:"crc32,omitempty"`
}

type gameLabProfileSetup struct {
	Emulator    string `json:"emulator"`
	Kind        string `json:"kind,omitempty"`
	Core        string `json:"core,omitempty"`
	CoreVersion string `json:"coreVersion,omitempty"`
	Launcher    string `json:"launcher,omitempty"`
	CPUProfile  string `json:"cpuProfile"`
	MaxFreqKHz  int64  `json:"maxFreqKHz,omitempty"`
}

type gameLabProfileResults struct {
	FPS            float64 `json:"fps,omitempty"`
	TargetFPS      float64 `json:"targetFps,omitempty"`
	FPSMethod      string  `json:"fpsMethod,omitempty"`
	SlowShare      float64 `json:"slowShare,omitempty"`
	AudioChecked   bool    `json:"audioChecked"`
	GlitchesPerMin float64 `json:"glitchesPerMinute,omitempty"`
	SoundLost      bool    `json:"soundLost,omitempty"`
	Watts          float64 `json:"watts,omitempty"`
	PctPerHour     float64 `json:"batteryPctPerHour,omitempty"`
	PlayedSeconds  float64 `json:"playedSeconds"`
	TooShort       bool    `json:"tooShort,omitempty"`
}

// gameLabShared is one imported profile the Lab lists.
type gameLabShared struct {
	File    string
	Profile gameLabProfile
}

// gameLabFirmware names the system JukaHub runs on, for a profile.
func gameLabFirmware() string {
	switch {
	case IsTrimuiDevice():
		return jukaosFirmwareName()
	case IsWindows():
		return "Windows"
	}
	return strings.ToUpper(runtime.GOOS[:1]) + runtime.GOOS[1:]
}

// gameLabLauncherTail is a launcher as "folder/file", never a full path.
func gameLabLauncherTail(launcher string) string {
	launcher = filepath.Clean(strings.TrimSpace(launcher))
	base := filepath.Base(launcher)
	if dir := filepath.Base(filepath.Dir(launcher)); dir != "" && dir != "." && dir != string(filepath.Separator) {
		return dir + "/" + base
	}
	return base
}

// gameLabProfileFor builds the profile of a setup and its result. Off the
// main thread: it reads the game file's size and its metadata record.
func gameLabProfileFor(game libraryGame, s gameLabSetup, r *gameLabResult) gameLabProfile {
	p := gameLabProfile{
		Schema:        gameLabProfileSchema,
		SchemaVersion: gameLabProfileVersion,
		Device: gameLabProfileDevice{
			Model:    string(DetectDevice()),
			Firmware: gameLabFirmware(),
			OS:       runtime.GOOS + "/" + runtime.GOARCH,
		},
		Game: gameLabProfileGame{System: game.System, Title: game.Title, File: filepath.Base(game.Path)},
		Setup: gameLabProfileSetup{
			Emulator:   s.Name,
			Kind:       s.Kind,
			Core:       s.Core,
			Launcher:   gameLabLauncherTail(s.Launcher),
			CPUProfile: normaliseGameCPUProfile(s.Profile),
			MaxFreqKHz: s.MaxFreqKHz,
		},
	}
	if p.Setup.CPUProfile == "" {
		p.Setup.CPUProfile = "auto"
	}
	if DetectDevice() == DeviceDevBuild {
		p.Device.Model = "pc" // a desktop build names no model
	}
	if info, err := os.Stat(game.Path); err == nil && info.Mode().IsRegular() {
		p.Game.Size = info.Size()
	}
	if rec, ok := gmetaGet(game.System, game.Title); ok {
		p.Game.CRC32 = strings.ToLower(strings.TrimSpace(rec.CRC))
	}
	if r != nil {
		if r.Core != "" {
			p.Setup.Core, p.Setup.CoreVersion = r.Core, r.CoreVersion
		}
		p.Results = gameLabProfileResults{
			FPS:           r.FPS,
			TargetFPS:     r.Target,
			FPSMethod:     r.FPSMethod,
			SlowShare:     r.SlowShare,
			AudioChecked:  r.Audio,
			SoundLost:     r.SoundLost,
			Watts:         r.Watts,
			PctPerHour:    r.PctPerHour,
			PlayedSeconds: r.Played,
			TooShort:      r.Short,
		}
		if rate, ok := gameLabGlitchRate(r); ok && !r.SoundLost {
			p.Results.GlitchesPerMin = round1(rate)
		}
	}
	return p
}

// gameLabExportName is the export's file name: "<system>-<title>.json", with
// anything a card's file system or a chat app would trip on replaced.
func gameLabExportName(system, title string) string {
	clean := func(s string, limit int) string {
		var b strings.Builder
		for _, r := range strings.TrimSpace(s) {
			switch {
			case unicode.IsLetter(r), unicode.IsDigit(r), r == ' ', r == '-', r == '_', r == '(', r == ')', r == '.':
				b.WriteRune(r)
			default:
				b.WriteRune('_')
			}
		}
		out := strings.Trim(b.String(), " ._")
		if r := []rune(out); len(r) > limit {
			out = strings.TrimSpace(string(r[:limit]))
		}
		return out
	}
	name := clean(system, 32) + "-" + clean(title, 80)
	if strings.Trim(name, "-") == "" {
		name = "game"
	}
	return name + ".json"
}

// gameLabExport writes side's setup as a profile and says where. Its own
// goroutine.
func gameLabExport(game libraryGame, e gameLabEntry, side string) {
	p := gameLabProfileFor(game, e.setup(side), e.result(side))
	dir := gameLabDir()
	if dir == "" {
		ShowToast("No data folder to save the setup in", ToastKindWarning)
		return
	}
	dir = filepath.Join(dir, "export")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		ShowToast("Cannot save the setup: "+err.Error(), ToastKindWarning)
		return
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return
	}
	name := gameLabExportName(game.System, game.Title)
	if err := AtomicWrite(filepath.Join(dir, name), append(data, '\n'), 0o644); err != nil {
		ShowToast("Cannot save the setup: "+err.Error(), ToastKindWarning)
		return
	}
	logInfo("gamelab", "shared setup %s of %s as %s", strings.ToUpper(side), filepath.Base(game.Path), name)
	ShowToast("Saved to game-lab/export/"+name+" — copy it to a friend's game-lab/import folder", ToastKindSuccess)
	tbxPost(func() { gameLabShareQR(name) })
}

// gameLabShareQR offers the setup just written on the phone: the Game Lab
// page lists it and downloads it as the file JukaHub reads. It runs on the
// main thread, where the companion's address and the QR overlay live.
func gameLabShareQR(name string) {
	companionStart()
	if companion.url == "" {
		msg := companion.err
		if msg == "" {
			msg = "Connect this device to Wi-Fi to send the setup to a phone"
		}
		ShowToast(msg, ToastKindWarning)
		return
	}
	code, err := qrcode.New(companionGameLabURL(companion.url), qrcode.Low)
	if err != nil {
		ShowToast("Cannot draw the QR code: "+err.Error(), ToastKindWarning)
		return
	}
	detail := "Open it and download " + name
	if caption := companionPairingCaption(companion.url); caption != "" {
		detail += " · " + caption
	}
	qrZoomOpenCardDetail(code.Bitmap(), "Game Lab on your phone", detail)
}

// ── Import ───────────────────────────────────────────────────────────────

// gameLabCleanText cuts a string to limit runes and drops control and format
// characters (a right-to-left override would make a shared name read as
// something else).
func gameLabCleanText(s string, limit int) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if n >= limit {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// gameLabSane reports a finite number within [lo, hi].
func gameLabSane(v, lo, hi float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= lo && v <= hi
}

// gameLabParseProfile reads and checks one shared file. Pure.
func gameLabParseProfile(raw []byte) (gameLabProfile, error) {
	var p gameLabProfile
	if len(raw) > gameLabImportMax {
		return p, errors.New("too large")
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	if p.Schema != gameLabProfileSchema {
		return p, errors.New("not a Game Lab profile")
	}
	if p.SchemaVersion < 1 || p.SchemaVersion > gameLabProfileVersion {
		return p, errors.New("a newer profile version")
	}
	p.Device.Model = gameLabCleanText(p.Device.Model, 48)
	p.Device.Firmware = gameLabCleanText(p.Device.Firmware, 48)
	p.Device.OS = gameLabCleanText(p.Device.OS, 32)
	p.Game.System = gameLabCleanText(p.Game.System, 64)
	p.Game.Title = gameLabCleanText(p.Game.Title, 200)
	p.Game.File = gameLabCleanText(p.Game.File, 200)
	p.Game.CRC32 = strings.ToLower(gameLabCleanText(p.Game.CRC32, 8))
	p.Setup.Emulator = gameLabCleanText(p.Setup.Emulator, 80)
	p.Setup.Core = strings.ToLower(gameLabCleanText(p.Setup.Core, 64))
	p.Setup.CoreVersion = gameLabCleanText(p.Setup.CoreVersion, 32)
	p.Setup.Launcher = gameLabCleanText(p.Setup.Launcher, 160)
	p.Results.FPSMethod = gameLabCleanText(p.Results.FPSMethod, 8)
	if p.Game.System == "" || p.Game.Title == "" || p.Setup.Emulator == "" {
		return p, errors.New("no game or emulator named")
	}
	for _, c := range p.Game.CRC32 {
		if !strings.ContainsRune("0123456789abcdef", c) {
			p.Game.CRC32 = ""
			break
		}
	}
	switch p.Setup.Kind {
	case "core", "retroarch", "standalone":
	default:
		p.Setup.Kind = ""
	}
	cpu := strings.ToLower(strings.TrimSpace(p.Setup.CPUProfile))
	if cpu == "auto" {
		cpu = ""
	}
	if normaliseGameCPUProfile(cpu) != cpu {
		return p, errors.New("unknown CPU profile")
	}
	p.Setup.CPUProfile = cpu
	if k := p.Setup.MaxFreqKHz; k != 0 && (k < 200_000 || k > 5_000_000) {
		return p, errors.New("clock limit out of range")
	}
	r := p.Results
	if !gameLabSane(r.FPS, 0, 240) || !gameLabSane(r.TargetFPS, 0, 240) || !gameLabSane(r.SlowShare, 0, 1) ||
		!gameLabSane(r.GlitchesPerMin, 0, 10000) || !gameLabSane(r.Watts, 0, 60) || !gameLabSane(r.PctPerHour, 0, 400) ||
		!gameLabSane(r.PlayedSeconds, 0, 86400) || (r.FPS > 0 && r.TargetFPS <= 0) {
		return p, errors.New("a measurement out of range")
	}
	if p.Results.FPSMethod != "frames" && p.Results.FPSMethod != "log" {
		p.Results.FPSMethod = ""
	}
	return p, nil
}

// gameLabProfileMatches reports whether a profile is for this game: the same
// system, and the same CRC when both know it, else the same title.
func gameLabProfileMatches(p gameLabProfile, system, title, crc string) bool {
	// The console as compat.go names it, so "GBA" on one device and "Game Boy
	// Advance" on another are one system.
	if !strings.EqualFold(canonicalCompatSystem(p.Game.System), canonicalCompatSystem(system)) {
		return false
	}
	if p.Game.CRC32 != "" && crc != "" {
		return strings.EqualFold(p.Game.CRC32, crc)
	}
	return artworkNormalizeTitle(p.Game.Title) == artworkNormalizeTitle(title)
}

// gameLabScanImports lists the shared profiles for game. Off the main thread.
func gameLabScanImports(game libraryGame) []gameLabShared {
	dir := gameLabDir()
	if dir == "" {
		return nil
	}
	dir = filepath.Join(dir, "import")
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name() < items[j].Name() })
	crc := ""
	if rec, ok := gmetaGet(game.System, game.Title); ok {
		crc = strings.ToLower(strings.TrimSpace(rec.CRC))
	}
	var out []gameLabShared
	read := 0
	for _, it := range items {
		if it.IsDir() || !strings.EqualFold(filepath.Ext(it.Name()), ".json") {
			continue
		}
		if read >= gameLabImportFiles || len(out) >= gameLabImportShown {
			break
		}
		read++
		path := filepath.Join(dir, it.Name())
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > gameLabImportMax {
			continue
		}
		raw, err := gameLabReadCapped(path)
		if err != nil {
			continue
		}
		p, err := gameLabParseProfile(raw)
		if err != nil {
			logInfo("gamelab", "skipping shared setup %s: %v", it.Name(), err)
			continue
		}
		if gameLabProfileMatches(p, game.System, game.Title, crc) {
			out = append(out, gameLabShared{File: it.Name(), Profile: p})
		}
	}
	return out
}

// gameLabReadCapped reads at most one byte past gameLabImportMax of a shared
// file, so one that grew after its size was checked is still refused (by
// gameLabParseProfile) without being read whole.
func gameLabReadCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, gameLabImportMax+1))
}

// gameLabNum is a figure with one decimal at most: "59.9", "60".
func gameLabNum(v float64) string { return strconv.FormatFloat(round1(v), 'f', -1, 64) }

// gameLabSharedLabel is a shared setup's two lines in the Lab.
func gameLabSharedLabel(s gameLabShared) (string, string) {
	p := s.Profile
	name := p.Setup.Emulator + " · " + gameLabProfileWord(p.Setup.CPUProfile)
	if p.Setup.MaxFreqKHz > 0 {
		name += " · " + formatGameCeiling(p.Setup.MaxFreqKHz)
	}
	var facts []string
	if p.Results.FPS > 0 {
		facts = append(facts, gameLabNum(p.Results.FPS)+" fps")
	}
	if p.Results.Watts > 0 {
		facts = append(facts, gameLabNum(p.Results.Watts)+" W")
	}
	where := p.Device.Model
	if p.Device.Firmware != "" {
		where += " · " + p.Device.Firmware
	}
	if where != "" {
		facts = append(facts, "on "+where)
	}
	if p.Results.TooShort {
		facts = append(facts, "short run")
	}
	return name, strings.Join(facts, " · ")
}

// gameLabMatchChoice finds the emulator a shared setup names among the game's
// choices here: by core for a RetroArch setup, else by name, else by its
// launcher's folder and file.
func gameLabMatchChoice(p gameLabProfile, choices []gameLabChoice) (gameLabChoice, bool) {
	core := normalizeCoreName(p.Setup.Core)
	if core != "" {
		for _, c := range choices {
			if c.Core != "" && c.Kind != "standalone" && normalizeCoreName(c.Core) == core {
				return c, true
			}
		}
	}
	for _, c := range choices {
		if strings.EqualFold(strings.TrimSpace(c.Name), p.Setup.Emulator) {
			return c, true
		}
	}
	if p.Setup.Launcher != "" {
		for _, c := range choices {
			if strings.EqualFold(gameLabLauncherTail(c.Launcher), p.Setup.Launcher) {
				return c, true
			}
		}
	}
	return gameLabChoice{}, false
}
