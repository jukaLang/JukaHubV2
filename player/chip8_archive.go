package main

// chip8_archive.go — the chip8Archive as the CHIP-8 shelf's source of games.
//
// John Earnest's chip8Archive (github.com/JohnEarnest/chip8Archive) collects
// the CHIP-8, SUPER-CHIP and XO-CHIP programs written for the yearly Octojam
// and since, all released into the public domain (CC0). Its programs.json is
// one object keyed by program: title, authors, desc, event, release,
// platform (chip8, schip or xochip) and the Octo options the game wants
// (chip8_options.go). Each game's cartridge is roms/<key>.ch8.
//
//   - The list (chip8ParsePrograms) is read defensively — it is outside data:
//     a key must be a plain file-safe name, every field may be missing or of
//     another type (authors is a list, sometimes a string), text is cleaned
//     and cut to a sane length, and an entry that does not parse is skipped.
//   - It is saved under data/chip8/programs.json. A visit shows the saved
//     copy at once and reads the network copy once per run when online (Y
//     reads it again); offline the saved copy is the list, said so.
//   - Installing (chip8Install) downloads the cartridge, puts it in the
//     CHIP-8 Roms folder under the game's title with its machine's
//     extension (.ch8, .sc8, .xo8 — so it still plays right if the index is
//     lost), and records the archive entry under the cartridge's SHA-1 in
//     data/chip8/installed.json, which the player reads for the game's
//     quirks, speed and colours. The cover is the game itself: the machine
//     runs it headless for a few seconds and its screen, in the game's
//     colours, is saved where the library looks for covers.
//
// Everything here runs on goroutines; the shelf screen (chip8_shelf_ui.go)
// posts the results back with tbxPost.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	chip8ArchiveBase = "https://raw.githubusercontent.com/JohnEarnest/chip8Archive/master/"
	chip8CatalogURL  = chip8ArchiveBase + "programs.json"
	chip8ArchiveRepo = "https://github.com/JohnEarnest/chip8Archive"
	// chip8CatalogMax bounds the list's download (it is about 100 KB).
	chip8CatalogMax = 4 << 20
	// chip8CatalogMaxEntries bounds how many programs are listed.
	chip8CatalogMaxEntries = 3000
	// chip8OptionsMax bounds one entry's options object.
	chip8OptionsMax = 16 << 10
	chip8UserAgent  = "JukaHub-CHIP8/1.0 (+https://jukalang.com)"
)

// chip8Program is one archive entry.
type chip8Program struct {
	Key      string
	Title    string
	Authors  string
	Desc     string
	Event    string
	Release  string
	Platform string // chip8, schip or xochip
	// Options is the entry's options object as it came (compact JSON), nil
	// when it had none.
	Options json.RawMessage
	// line is the shelf row's second line, made once.
	line string
}

// platform is the entry's machine.
func (p chip8Program) platform() chip8Platform {
	plat, _ := chip8PlatformFromName(p.Platform)
	return plat
}

// rowLine is the shelf row's second line: authors, machine, event.
func (p *chip8Program) rowLine() string {
	if p.line == "" {
		parts := make([]string, 0, 3)
		if p.Authors != "" {
			parts = append(parts, p.Authors)
		}
		parts = append(parts, p.platform().label())
		if p.Event != "" {
			parts = append(parts, p.Event)
		}
		p.line = strings.Join(parts, " · ")
	}
	return p.line
}

// options is what the player will run the game with.
func (p chip8Program) options() chip8Options {
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(p.Options, &raw)
	return chip8ApplyOptions(chip8OctoDefaults(p.platform()), raw)
}

// chip8ValidKey reports a program key that is safe in a URL and a file name.
func chip8ValidKey(key string) bool {
	if key == "" || len(key) > 64 {
		return false
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// chip8CleanText drops control characters (newlines kept when multiline),
// trims and cuts s to limit runes.
func chip8CleanText(s string, limit int, multiline bool) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if r == '\r' {
			continue
		}
		if r == '\n' && multiline {
			b.WriteRune(r)
			n++
		} else if unicode.IsControl(r) {
			b.WriteByte(' ')
			n++
		} else if r != utf8.RuneError {
			b.WriteRune(r)
			n++
		}
		if n >= limit {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// chip8JSONText reads a string field, or a list of strings joined with
// commas (authors).
func chip8JSONText(raw json.RawMessage) string {
	if s, ok := chip8JSONString(raw); ok {
		return s
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return ""
	}
	parts := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := chip8JSONString(item); ok && s != "" {
			parts = append(parts, s)
		}
		if len(parts) >= 12 {
			break
		}
	}
	return strings.Join(parts, ", ")
}

// chip8ParsePrograms reads programs.json, sorted by title.
func chip8ParsePrograms(raw []byte) ([]chip8Program, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("the game list is damaged: %w", err)
	}
	out := make([]chip8Program, 0, min(len(top), chip8CatalogMaxEntries))
	for key, entry := range top {
		if !chip8ValidKey(key) {
			continue
		}
		var e map[string]json.RawMessage
		if json.Unmarshal(entry, &e) != nil {
			continue
		}
		p := chip8Program{
			Key:     key,
			Title:   chip8CleanText(chip8JSONText(e["title"]), 80, false),
			Authors: chip8CleanText(chip8JSONText(e["authors"]), 120, false),
			Desc:    chip8CleanText(chip8JSONText(e["desc"]), 1500, true),
			Event:   chip8CleanText(chip8JSONText(e["event"]), 60, false),
			Release: chip8CleanText(chip8JSONText(e["release"]), 20, false),
		}
		if p.Title == "" {
			p.Title = key
		}
		plat, ok := chip8PlatformFromName(chip8JSONText(e["platform"]))
		if !ok {
			plat = chip8PlatChip8
		}
		p.Platform = [...]string{"chip8", "schip", "xochip"}[plat]
		// Octo's options are a dozen short keys; anything far larger is not
		// one, and would be copied into installed.json as it came.
		if opts := bytes.TrimSpace(e["options"]); len(opts) > 0 && len(opts) <= chip8OptionsMax && opts[0] == '{' {
			var obj map[string]json.RawMessage
			if json.Unmarshal(opts, &obj) == nil {
				if compact, err := json.Marshal(obj); err == nil {
					p.Options = compact
				}
			}
		}
		out = append(out, p)
		if len(out) >= chip8CatalogMaxEntries {
			break
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the game list has no games")
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Title), strings.ToLower(out[j].Title)
		if a != b {
			return a < b
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// chip8Get reads url with a size bound.
func chip8Get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", chip8UserAgent)
	resp, err := (&http.Client{Timeout: 45 * time.Second, Transport: ytTransport()}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("GitHub sent more than expected")
	}
	return data, nil
}

// chip8WriteFile writes data through a temporary file and a rename, so a
// power cut leaves the old file or none, never half of one.
func chip8WriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func chip8CatalogPath() string { return filepath.Join(chip8DataDir(), "programs.json") }

// chip8LoadSavedCatalog is the list saved by the last fetch.
func chip8LoadSavedCatalog() ([]chip8Program, error) {
	raw, err := os.ReadFile(chip8CatalogPath())
	if err != nil {
		return nil, err
	}
	return chip8ParsePrograms(raw)
}

// chip8FetchCatalog reads the list from GitHub and saves it.
func chip8FetchCatalog(ctx context.Context) ([]chip8Program, error) {
	raw, err := chip8Get(ctx, chip8CatalogURL, chip8CatalogMax)
	if err != nil {
		return nil, err
	}
	progs, err := chip8ParsePrograms(raw)
	if err != nil {
		return nil, err
	}
	if werr := chip8WriteFile(chip8CatalogPath(), raw); werr != nil {
		logWarn("chip8", "saving the game list: %v", werr)
	}
	return progs, nil
}

// chip8RomsDir is the CHIP-8 shelf's folder: the one the card already has
// (any of the console's folder names, or a NextUI "(CHIP8)" folder), else a
// new Roms/CHIP8 in the library's first Roms folder.
func chip8RomsDir() (string, error) {
	roots := romLibraryRoots()
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if s, ok := romSystemAliases[strings.ToLower(name)]; ok && s.Name == chip8System {
				return filepath.Join(root, name), nil
			}
			if _, tag, tagged := nextuiFolderTag(name); tagged {
				if s, ok := nextuiTagSystem(tag); ok && s.Name == chip8System {
					return filepath.Join(root, name), nil
				}
			}
		}
	}
	if len(roots) == 0 {
		return "", errors.New("there is no Roms folder")
	}
	dir := filepath.Join(roots[0], "CHIP8")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cannot make %s: %w", dir, err)
	}
	return dir, nil
}

// chip8CleanFileName turns a title into a file name every card takes.
func chip8CleanFileName(title string) string {
	var b strings.Builder
	space := false
	for _, r := range title {
		switch {
		case strings.ContainsRune(`<>:"/\|?*`, r) || unicode.IsControl(r):
			r = ' '
		}
		if r == ' ' {
			if space {
				continue
			}
			space = true
		} else {
			space = false
		}
		b.WriteRune(r)
	}
	name := strings.Trim(b.String(), " .")
	if utf8.RuneCountInString(name) > 60 {
		name = strings.TrimSpace(string([]rune(name)[:60]))
	}
	// Windows' device names, which no file may have whatever its extension.
	switch up := strings.ToUpper(name); {
	case up == "CON", up == "PRN", up == "AUX", up == "NUL":
		name += " game"
	case len(up) == 4 && (strings.HasPrefix(up, "COM") || strings.HasPrefix(up, "LPT")) && up[3] >= '0' && up[3] <= '9':
		name += " game"
	}
	return name
}

// chip8Install puts an archive game on the CHIP-8 shelf and returns its path.
func chip8Install(ctx context.Context, p chip8Program) (string, error) {
	if !chip8ValidKey(p.Key) {
		return "", errors.New("this entry has no usable name")
	}
	data, err := chip8Get(ctx, chip8ArchiveBase+"roms/"+p.Key+".ch8", chip8MaxROM)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", errors.New("the archive sent an empty file")
	}
	dir, err := chip8RomsDir()
	if err != nil {
		return "", err
	}
	name := chip8CleanFileName(p.Title)
	if name == "" {
		name = p.Key
	}
	ext := p.platform().ext()
	path := ""
	for _, candidate := range []string{name, name + " (" + p.Key + ")"} {
		try := filepath.Join(dir, candidate+ext)
		// Free, or this very game already: anything else there (another
		// file, a folder, one that cannot be read) is left alone.
		if _, serr := os.Lstat(try); errors.Is(serr, os.ErrNotExist) {
			path = try
			break
		}
		if old, rerr := chip8ReadROM(try); rerr == nil && bytes.Equal(old, data) {
			path = try
			break
		}
	}
	if path == "" {
		return "", errors.New("a different file already has this game's name")
	}
	if err := chip8WriteFile(path, data); err != nil {
		return "", err
	}
	hash := chip8HashROM(data)
	rec := chip8InstallRec{Key: p.Key, Title: p.Title, Authors: p.Authors, Platform: p.Platform, Options: p.Options, Path: path}
	if err := chip8InstalledPut(hash, rec); err != nil {
		logWarn("chip8", "recording %s: %v", p.Key, err)
	}
	chip8WriteCover(path, data, p.options())
	logInfo("chip8", "installed %s (%s) as %s", p.Key, p.Platform, path)
	return path, nil
}

// chip8WriteCover runs a cartridge headless and saves its screen as the
// game's cover, unless the game has one.
func chip8WriteCover(romPath string, rom []byte, o chip8Options) {
	root := libraryArtworkRoot()
	if root == "" {
		return
	}
	dest := romArtworkPath(scannedROM{Path: romPath, System: chip8System}, root)
	if dest == "" || artworkCoverPresent(dest) {
		return
	}
	data := chip8HeadlessFrame(rom, o)
	if len(data) == 0 {
		return
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return
	}
	if err := chip8WriteFile(dest, data); err != nil {
		logWarn("chip8", "saving a cover: %v", err)
	}
}

// chip8HeadlessFrame runs a game for up to six seconds with no keys pressed
// and returns its first lit screen (sampled each second from the second on)
// as a PNG, nil when it never shows anything. A stuck program is bounded:
// at most 360 frames of at most 1000 instructions.
func chip8HeadlessFrame(rom []byte, o chip8Options) (out []byte) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("chip8", "cover run failed: %v", r)
			out = nil
		}
	}()
	vm := newChip8VM(rom, o.Quirks, 1)
	vm.xo = o.Platform == chip8PlatXO
	tick := max(1, min(o.Tickrate, 1000))
	for f := 1; f <= 360; f++ {
		vm.runFrame(tick)
		if vm.halted {
			break
		}
		if f >= 120 && f%60 == 0 && !vm.blank() {
			break
		}
	}
	if vm.blank() {
		return nil
	}
	return chip8FramePNG(vm, o.Colors)
}

// chip8FramePNG is the machine's screen as a 256×128 PNG in colors.
func chip8FramePNG(vm *chip8VM, colors [chip8ColCount]uint32) []byte {
	w, h := vm.size()
	scale := 256 / w
	img := image.NewRGBA(image.Rect(0, 0, w*scale, h*scale))
	var pal [4]color.RGBA
	for i, ci := range [4]int{chip8ColBG, chip8ColFill, chip8ColFill2, chip8ColBlend} {
		c := colors[ci]
		pal[i] = color.RGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 255}
	}
	for y := range h * scale {
		for x := range w * scale {
			img.SetRGBA(x, y, pal[vm.pixel(x/scale, y/scale)])
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}
