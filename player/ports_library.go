package main

// ports_library.go — LIB-9 "Ports are games": a port PortMaster installed is
// a game in the Games library (console "Ports"), in Continue and the recents,
// in playtime, search, the game assistant's "Ask about <title>" and the
// trophies, whether it was started from the library or from the Ports panel.
//
// The library reads the same launcher scripts the Ports panel lists
// (scanPortLaunchers). A port's title comes from its own port.json
// (attr.title), else its gameinfo.xml, else the script's name; its cover from
// the library's artwork folder, else the firmware's Imgs/<script>.png beside
// the script (romCoverPath), else the port's own cover/screenshot, which is
// copied there once (a JPEG becomes a PNG) so every library surface that
// reads covers by path shows it.
//
// port.json is PortMaster's manifest, which it keeps in the port's data
// folder: {"version": <format>, "name": "<zip>", "items": [...],
// "items_opt": [...], "attr": {title, desc, inst, porter, runtime, reqs,
// arch, ...}}. "version" is the manifest's format, not the port's. The md5
// of the zip PortMaster installed is read from where harbourmaster records it
// ("status": {"md5": …}, else a top-level "md5") — unverified on a device.

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// portsLibrarySystem is the console name of the row.
	portsLibrarySystem = "Ports"
	// portsLibraryLauncher stands in for an emulator path (as the Android
	// row's does): launchLibraryGame hands a port to startPortScript.
	portsLibraryLauncher = "portmaster:port-script"
	// portMetaMaxBytes bounds a port.json / gameinfo.xml read.
	portMetaMaxBytes = 256 << 10
	// portCoverMaxBytes bounds a cover picture that is copied or converted.
	portCoverMaxBytes = 6 << 20
	// portMetaCacheMax bounds the metadata cache.
	portMetaCacheMax = 512
)

// portMeta is what a port's own files say about it.
type portMeta struct {
	Dir      string // the port's data folder ("" when none was found)
	JSON     string // its port.json, "" when there is none
	Zip      string // "name": the catalogue's key, "2048.zip"
	Title    string
	Desc     string
	Inst     string
	Porters  []string
	Runtime  []string
	Items    []string
	ItemsOpt []string
	Reqs     []string
	MD5      string // the zip PortMaster installed; "" unknown
	Image    string // gameinfo.xml's picture, absolute; "" none
}

// portJSONStrings reads a list that a manifest writes as a list, a single
// string or null.
func portJSONStrings(v interface{}) []string {
	if s, ok := v.(string); ok {
		if s = strings.TrimSpace(s); s != "" {
			return []string{s}
		}
		return nil
	}
	return mcJSONStrings(v)
}

// parsePortJSON reads a port.json. ok is false for anything that is not a
// JSON object.
func parsePortJSON(raw []byte) (portMeta, bool) {
	var doc map[string]interface{}
	if err := json.Unmarshal(stripUTF8BOM(raw), &doc); err != nil || doc == nil {
		return portMeta{}, false
	}
	m := portMeta{
		Zip:      mcJSONString(doc, "name"),
		Items:    portJSONStrings(doc["items"]),
		ItemsOpt: portJSONStrings(doc["items_opt"]),
		MD5:      mcJSONString(doc, "md5"),
	}
	if status, ok := doc["status"].(map[string]interface{}); ok {
		if s := mcJSONString(status, "md5"); s != "" {
			m.MD5 = s
		}
	}
	if attr, ok := doc["attr"].(map[string]interface{}); ok {
		m.Title = mcJSONString(attr, "title")
		m.Desc = mcJSONString(attr, "desc")
		m.Inst = mcJSONString(attr, "inst")
		m.Porters = portJSONStrings(attr["porter"])
		m.Runtime = portJSONStrings(attr["runtime"])
		m.Reqs = portJSONStrings(attr["reqs"])
	}
	if m.Title == "" {
		m.Title = mcJSONString(doc, "title")
	}
	if len(m.MD5) != 32 {
		m.MD5 = ""
	}
	return m, true
}

// portGameInfo is the part of an EmulationStation gameinfo.xml a port ships.
type portGameInfo struct {
	Games []struct {
		Path      string `xml:"path"`
		Name      string `xml:"name"`
		Desc      string `xml:"desc"`
		Image     string `xml:"image"`
		Thumbnail string `xml:"thumbnail"`
	} `xml:"game"`
}

// parsePortGameInfo returns the name, description and picture (as written,
// usually "./<dir>/screenshot.png") of the entry for script, else the first
// entry.
func parsePortGameInfo(raw []byte, script string) (name, desc, picture string) {
	var doc portGameInfo
	if err := xml.Unmarshal(raw, &doc); err != nil || len(doc.Games) == 0 {
		return "", "", ""
	}
	pick := 0
	base := strings.ToLower(filepath.Base(script))
	for i, g := range doc.Games {
		if base != "" && strings.ToLower(filepath.Base(filepath.FromSlash(strings.TrimSpace(g.Path)))) == base {
			pick = i
			break
		}
	}
	g := doc.Games[pick]
	picture = strings.TrimSpace(g.Image)
	if picture == "" {
		picture = strings.TrimSpace(g.Thumbnail)
	}
	return strings.TrimSpace(g.Name), strings.TrimSpace(g.Desc), picture
}

// portReadSmall reads a file of at most portMetaMaxBytes.
func portReadSmall(path string) ([]byte, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > portMetaMaxBytes {
		return nil, false
	}
	raw, err := os.ReadFile(path)
	return raw, err == nil
}

// portDataDirCandidates are the folders a port's files may be in: the one
// its script's GAMEDIR names (gameDiagPortDir), then <name>/ beside the
// script and in every ports folder.
func portDataDirCandidates(script string) []string {
	var out []string
	if dir, _ := gameDiagPortDir(script); dir != "" {
		out = append(out, dir)
	}
	stem := strings.TrimSuffix(filepath.Base(script), filepath.Ext(script))
	if stem != "" {
		out = append(out, filepath.Join(filepath.Dir(script), stem))
		for _, root := range portRoots(portMasterSDRoot) {
			out = append(out, filepath.Join(root, stem))
		}
	}
	return dedupeCleanPaths(out)
}

var portMetaCache struct {
	mu sync.Mutex
	m  map[string]portMetaCached
}

type portMetaCached struct {
	mod  time.Time
	meta portMeta
}

// portMetaFor reads a port's manifest and gameinfo.xml, cached by the
// script's modification time. Any goroutine.
func portMetaFor(script string) portMeta {
	info, err := os.Stat(script)
	if err != nil {
		return portMeta{}
	}
	portMetaCache.mu.Lock()
	if c, ok := portMetaCache.m[script]; ok && c.mod.Equal(info.ModTime()) {
		portMetaCache.mu.Unlock()
		return c.meta
	}
	portMetaCache.mu.Unlock()
	meta := portReadMeta(script)
	portMetaCache.mu.Lock()
	if portMetaCache.m == nil || len(portMetaCache.m) >= portMetaCacheMax {
		portMetaCache.m = make(map[string]portMetaCached, 32)
	}
	portMetaCache.m[script] = portMetaCached{mod: info.ModTime(), meta: meta}
	portMetaCache.mu.Unlock()
	return meta
}

// portMetaForget drops the cache (after PortMaster ran, an update or an
// uninstall).
func portMetaForget() {
	portMetaCache.mu.Lock()
	portMetaCache.m = nil
	portMetaCache.mu.Unlock()
}

// portReadMeta is portMetaFor without the cache.
func portReadMeta(script string) portMeta {
	var meta portMeta
	for _, dir := range portDataDirCandidates(script) {
		if !dirExists(dir) {
			continue
		}
		if meta.Dir == "" {
			meta.Dir = dir
		}
		if raw, ok := portReadSmall(filepath.Join(dir, "port.json")); ok {
			if m, ok := parsePortJSON(raw); ok {
				m.Dir, m.JSON = dir, filepath.Join(dir, "port.json")
				meta = m
				break
			}
		}
	}
	if meta.Dir == "" {
		return meta
	}
	if raw, ok := portReadSmall(filepath.Join(meta.Dir, "gameinfo.xml")); ok {
		name, desc, picture := parsePortGameInfo(raw, script)
		if meta.Title == "" {
			meta.Title = name
		}
		if meta.Desc == "" {
			meta.Desc = desc
		}
		if picture != "" {
			// "./<dir>/screenshot.png" is relative to the ports folder the
			// gameinfo.xml was written for; try the port's folder too.
			rel := filepath.FromSlash(strings.TrimPrefix(picture, "./"))
			for _, base := range []string{filepath.Dir(meta.Dir), meta.Dir} {
				if p := filepath.Join(base, rel); regularFile(p) {
					meta.Image = p
					break
				}
				if p := filepath.Join(base, filepath.Base(rel)); regularFile(p) {
					meta.Image = p
					break
				}
			}
		}
	}
	return meta
}

// portLibraryTitle is the name a port goes by in the library, the recents
// and playtime.
func portLibraryTitle(script string) string {
	// One line: a manifest title with a newline or tab would break the row,
	// the recents and the playtime key.
	if t := strings.Join(strings.Fields(portMetaFor(script).Title), " "); t != "" {
		return t
	}
	return gameDisplayTitle(script)
}

// portLibraryGame is the library entry for a port's script.
func portLibraryGame(script string) libraryGame {
	art := romCoverPath(scannedROM{Path: script, System: portsLibrarySystem}, libraryArtworkRoot())
	return libraryGame{
		Path:   script,
		System: portsLibrarySystem,
		Title:  portLibraryTitle(script),
		Art:    art,
		HasArt: artworkCoverPresent(art),
	}
}

// portCoverSource is the picture to use as a port's cover: its own cover,
// gameinfo.xml's picture, then its screenshot.
func portCoverSource(meta portMeta) string {
	if meta.Dir == "" {
		return ""
	}
	var candidates []string
	for _, name := range []string{"cover.png", "cover.jpg", "cover.jpeg"} {
		candidates = append(candidates, filepath.Join(meta.Dir, name))
	}
	if meta.Image != "" {
		candidates = append(candidates, meta.Image)
	}
	for _, name := range []string{"screenshot.png", "screenshot.jpg", "screenshot.jpeg"} {
		candidates = append(candidates, filepath.Join(meta.Dir, name))
	}
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Size() > 8 && info.Size() <= portCoverMaxBytes {
			return p
		}
	}
	return ""
}

// portImportCover writes src (PNG or JPEG) to dst as a PNG. Off the render
// thread: a JPEG is decoded.
func portImportCover(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if len(raw) > portCoverMaxBytes {
		return fmt.Errorf("%s is too large", filepath.Base(src))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if artworkIsPNG(raw) {
		return AtomicWrite(dst, raw, 0o644)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%s is neither PNG nor JPEG", filepath.Base(src))
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 4096 || cfg.Height > 4096 {
		return fmt.Errorf("%s is %dx%d", filepath.Base(src), cfg.Width, cfg.Height)
	}
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return portWritePNG(dst, img)
}

func portWritePNG(dst string, img image.Image) error {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(io.Writer(&buf), img); err != nil {
		return err
	}
	return AtomicWrite(dst, buf.Bytes(), 0o644)
}

// portEnsureCover copies a port's own picture to the library's artwork path
// when no cover exists for it yet. Off the render thread.
func portEnsureCover(script string) {
	root := libraryArtworkRoot()
	if root == "" {
		return
	}
	rom := scannedROM{Path: script, System: portsLibrarySystem}
	// A picture taken while the port played (playshot_cover.go) is not a
	// cover: the port's own picture still goes in ahead of it.
	// A port whose cover the user removed gets none (artwork_nocover.go).
	if artworkCoverPresent(romArtworkPath(rom, root)) || romLocalCoverPath(rom) != "" || artworkNoCover(rom, root) {
		return
	}
	src := portCoverSource(portMetaFor(script))
	if src == "" {
		return
	}
	dst := romArtworkPath(rom, root)
	if dst == "" {
		return
	}
	if err := portImportCover(src, dst); err != nil {
		logDebug("portmaster", "cover for %s: %v", filepath.Base(script), err)
	}
}

// portsLibraryEnabled: ports run on the Linux handheld only.
func portsLibraryEnabled() bool {
	return runtime.GOOS == "linux"
}

// libraryAddPortGames adds the Ports row to a built library (the scan
// goroutine, through buildGameLibraryWith; seconds is the playtime snapshot).
func libraryAddPortGames(lib *gameLibraryReport, seconds map[string]int64) {
	if lib == nil || !portsLibraryEnabled() {
		return
	}
	for i := range lib.Systems {
		if lib.Systems[i].Name == portsLibrarySystem {
			return // already added by this build
		}
	}
	found := scanPortLaunchers(portRoots(portMasterSDRoot))
	if len(found) == 0 {
		return
	}
	games := make([]libraryGame, 0, len(found))
	for _, p := range found {
		portEnsureCover(p.Launch)
		g := portLibraryGame(p.Launch)
		if info, err := os.Stat(p.Launch); err == nil {
			g.AddedAt = info.ModTime()
		}
		games = append(games, g)
	}
	libraryDecorateGames(games, seconds)
	withArt := countCoveredGames(games)
	lib.Systems = append(lib.Systems, librarySystem{Name: portsLibrarySystem, Games: games, WithArt: withArt})
	lib.Total += len(games)
	lib.WithArt += withArt
}

// launchPortLibraryGame starts a port from the library, Continue or search
// (launchLibraryGame). Main thread.
func launchPortLibraryGame(game libraryGame) error {
	if !portsLibraryEnabled() {
		return fmt.Errorf("PortMaster ports run on the handheld")
	}
	if !regularFile(game.Path) {
		invalidatePortsCache()
		return fmt.Errorf("the port is no longer installed: %s", filepath.Base(game.Path))
	}
	if c := portCompatForLauncher(game.Path); portCompatBlocked(c.State) && !portCompatIgnored(appConfig) {
		return fmt.Errorf("it cannot run here: %s", setOr(c.Note, portCompatBadge(c.State)))
	}
	name := strings.TrimSuffix(filepath.Base(game.Path), filepath.Ext(game.Path))
	if privateGlibcPortNeedsRuntime(game.Path) && !privateGlibcReady() {
		ShowToast("Downloading GreenOvercast's compatibility runtime…", ToastKindInfo)
		privateGlibcEnsureAsync(func(err error) {
			if err != nil {
				ShowToast("Could not prepare GreenOvercast — "+firstLine(err.Error()), ToastKindError)
				return
			}
			launchPortScript(name, game.Path)
		})
		return nil
	}
	if err := startPortScript(name, game.Path); !errors.Is(err, errPortPreparing) {
		return err // nil when it started
	}
	return nil // it starts by itself once PortMaster's runtime is ready
}
