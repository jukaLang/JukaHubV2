package main

// minecraft.go — Minecraft on JukaHub (Start → Games → Minecraft).
//
// Minecraft is not one program a launcher can ship, so the screen gathers
// every honest way to play it on the machine in hand:
//
//   - Java Edition on the handheld, through PortMaster's Minecraft port
//     (Java 17 and WestonPack runtimes that PortMaster fetches). The port is
//     the engine; the game files are the player's own, from the .minecraft
//     folder of a PC where they own the game. A paired JukaHub PC hands them
//     over in one press (minecraft_peer.go).
//   - Bedrock Edition on the handheld, through AndroidRT and the player's own
//     arm64 APK (the APK Store does the triage; docs/ANDROID-MINECRAFT.md).
//   - Free Minecraft-style games that need no files: ClassiCube (a Minecraft
//     Classic client) and Luanti with Mineclonia, both PortMaster ports.
//   - On a PC: the Minecraft Launcher, Minecraft for Windows (Bedrock) and
//     Prism Launcher where installed (minecraft_windows.go, minecraft_other.go).
//
// Installing a port is PortMaster's job and stays PortMaster's: JukaHub finds
// the port in PortMaster's own catalogue (ports.json from its release), checks
// the download against the MD5 the catalogue lists, drops the zip in
// PortMaster's autoinstall folder and opens PortMaster, which installs it with
// the runtimes it declares — the same path TRIMUI_EX documents for hand-copied
// zips. When the catalogue cannot be read, PortMaster opens on its own with
// the name to search for.
//
// Nothing here reports a purchase, a licence or a sign-in that did not happen.

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	minecraftSceneName = "Minecraft"
	minecraftEntryName = "Minecraft"
	// minecraftGlyph is the grass block (icondefs.go).
	minecraftGlyph = "block"

	// mcPortsCatalogURL is PortMaster's catalogue: the list its own GUI reads,
	// rebuilt by PortMaster-New's CI on every release.
	mcPortsCatalogURL = "https://github.com/PortsMaster/PortMaster-New/releases/latest/download/ports.json"
	// mcPortDownloadBase is where a port zip lives when the catalogue entry
	// names only the zip.
	mcPortDownloadBase = "https://github.com/PortsMaster/PortMaster-New/releases/latest/download/"
	// mcCatalogMax bounds the catalogue read (it is a few MB).
	mcCatalogMax = 32 << 20
	// mcCatalogTTL is how long a read catalogue is reused.
	mcCatalogTTL = 6 * time.Hour
)

// ── The ports ───────────────────────────────────────────────────────────

// mcPortKind names one PortMaster port the screen knows.
type mcPortKind int

const (
	mcPortJava mcPortKind = iota
	mcPortClassiCube
	mcPortLuanti
	mcPortCount
)

// mcPortSpec says how to recognise a port and what it is.
type mcPortSpec struct {
	id    string // row id suffix and log name
	title string // the row's title
	// search is the name to look for in PortMaster when JukaHub cannot.
	search string
	// needsFiles: the port is an engine for the player's own game files.
	needsFiles bool
	blurb      string
}

var mcPortSpecs = [mcPortCount]mcPortSpec{
	mcPortJava: {
		id: "java", title: "Minecraft: Java Edition", search: "Minecraft", needsFiles: true,
		blurb: "The real Java Edition through PortMaster's Minecraft port: Java 17 and a Wayland/OpenGL layer that " +
			"PortMaster installs, with the game files from your own copy.",
	},
	mcPortClassiCube: {
		id: "classicube", title: "ClassiCube", search: "ClassiCube",
		blurb: "A free, open-source client compatible with Minecraft Classic: build in singleplayer or join " +
			"ClassiCube servers. No game files needed.",
	},
	mcPortLuanti: {
		id: "luanti", title: "Luanti · Mineclonia", search: "Luanti",
		blurb: "Luanti (formerly Minetest) running Mineclonia, a free game that plays like Minecraft survival: " +
			"mining, crafting, mobs and redstone. No game files needed.",
	},
}

// mcNorm folds a name for matching: lower case, letters and digits only.
func mcNorm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// mcJavaOtherWords are Minecraft-named things that are not the Java port.
var mcJavaOtherWords = []string{"classicube", "classic", "luanti", "minetest", "mineclon", "dungeons", "legends",
	"storymode", "story", "pied", "piedition", "mcpi", "earth", "education", "server"}

// mcPortMatches reports whether a launcher, folder, zip or catalogue title
// belongs to kind.
func mcPortMatches(kind mcPortKind, name string) bool {
	n := mcNorm(name)
	if n == "" {
		return false
	}
	switch kind {
	case mcPortJava:
		if !strings.Contains(n, "minecraft") {
			return false
		}
		for _, w := range mcJavaOtherWords {
			if strings.Contains(n, w) {
				return false
			}
		}
		return true
	case mcPortClassiCube:
		return strings.Contains(n, "classicube")
	case mcPortLuanti:
		for _, w := range []string{"luanti", "minetest", "mineclonia", "mineclone", "voxelibre"} {
			if strings.Contains(n, w) {
				return true
			}
		}
	}
	return false
}

// mcPortState is one port on this card.
type mcPortState struct {
	Installed bool
	Name      string     // the launcher's name ("Minecraft")
	Launch    string     // the launcher script
	DataDir   string     // the port's folder, "" when not found
	Compat    portCompat // the manifest's own word on this chip
	// Versions are the Java versions whose files are in the port's folder.
	Versions []string
	// Starts are the port's own start files ("1.12.2", "1.7.10-Forge"): the
	// versions it says it can launch.
	Starts []string
	// GameDir is where Java game files go (the port's .minecraft).
	GameDir string
	// Queued is a downloaded zip still waiting in PortMaster's autoinstall
	// folder (not installed yet).
	Queued string
}

// mcGameDirRe finds the port folder a PortMaster script works in:
// GAMEDIR="/$directory/ports/minecraft" and the variations seen in ports.
var mcGameDirRe = regexp.MustCompile(`(?m)^\s*(?:export\s+)?GAMEDIR=["']?[^"'\n]*?/ports/([A-Za-z0-9._ -]+?)/?["']?\s*$`)

// mcScriptGameDir reads a launcher's GAMEDIR folder name, "" when it has none.
func mcScriptGameDir(script string) string {
	f, err := os.Open(script)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, 64<<10))
	m := mcGameDirRe.FindSubmatch(data)
	if m == nil {
		return ""
	}
	name := strings.TrimSpace(string(m[1]))
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return ""
	}
	return name
}

// mcFindPortDir finds a port's folder under the PortMaster roots: the name its
// script works in first, then a folder that matches the kind.
func mcFindPortDir(roots []string, kind mcPortKind, scriptDir string) string {
	for _, root := range roots {
		if scriptDir != "" {
			if items, err := os.ReadDir(root); err == nil {
				for _, it := range items {
					if it.IsDir() && strings.EqualFold(it.Name(), scriptDir) {
						return filepath.Join(root, it.Name())
					}
				}
			}
		}
	}
	for _, root := range roots {
		items, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, it := range items {
			if it.IsDir() && !strings.HasPrefix(it.Name(), ".") && mcPortMatches(kind, it.Name()) {
				return filepath.Join(root, it.Name())
			}
		}
	}
	return ""
}

// mcJavaGameDir is where the Java port keeps game files: its .minecraft, or
// the port folder itself when it already holds versions/ there.
func mcJavaGameDir(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	dot := filepath.Join(dataDir, ".minecraft")
	if dirExists(dot) {
		return dot
	}
	if dirExists(filepath.Join(dataDir, "versions")) {
		return dataDir
	}
	return dot
}

// mcLocalVersions lists the versions in a .minecraft: a versions/<id> folder
// with its <id>.json.
func mcLocalVersions(gameDir string) []string {
	if gameDir == "" {
		return nil
	}
	items, err := os.ReadDir(filepath.Join(gameDir, "versions"))
	if err != nil {
		return nil
	}
	var out []string
	for _, it := range items {
		if !it.IsDir() || strings.HasPrefix(it.Name(), ".") {
			continue
		}
		if regularFile(filepath.Join(gameDir, "versions", it.Name(), it.Name()+".json")) {
			out = append(out, it.Name())
		}
	}
	sort.Strings(out)
	return out
}

// mcStartFiles lists the port's start files (<name>.start), which name the
// versions its launcher knows how to start.
func mcStartFiles(dataDir string) []string {
	if dataDir == "" {
		return nil
	}
	items, err := os.ReadDir(dataDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, it := range items {
		if it.IsDir() || !strings.EqualFold(filepath.Ext(it.Name()), ".start") {
			continue
		}
		out = append(out, strings.TrimSuffix(it.Name(), filepath.Ext(it.Name())))
	}
	sort.Strings(out)
	return out
}

// mcStartFor returns the start file that fits a version id ("1.7.10-Forge…"
// fits "1.7.10-Forge"), "" when none does. A start's game version must be the
// whole version or be followed by a separator ("1.12" does not fit "1.12.2"),
// and a loader in its name must be in the version's too.
func mcStartFor(starts []string, version string) string {
	v := strings.ToLower(version)
	best := ""
	for _, s := range starts {
		ls := strings.ToLower(s)
		base, loader := ls, ""
		if i := strings.IndexByte(ls, '-'); i > 0 {
			base, loader = ls[:i], ls[i+1:]
			if j := strings.IndexByte(loader, '-'); j > 0 {
				loader = loader[:j]
			}
		}
		if !strings.HasPrefix(v, base) || len(v) > len(base) && !strings.ContainsRune("-_ ", rune(v[len(base)])) {
			continue
		}
		switch {
		case loader != "":
			if !strings.Contains(v, loader) {
				continue
			}
		case v != ls && (strings.Contains(v, "forge") || strings.Contains(v, "fabric") || strings.Contains(v, "quilt")):
			continue
		}
		if len(s) > len(best) {
			best = s
		}
	}
	return best
}

// mcStartForVersion is mcStartFor for a launcher version: a Fabric or Quilt
// profile ("fabric-loader-0.14-1.16.5") names its game version only through
// inheritsFrom.
func mcStartForVersion(starts []string, v mcPeerVersion) string {
	if s := mcStartFor(starts, v.ID); s != "" {
		return s
	}
	if v.Inherits != "" {
		return mcStartFor(starts, v.Inherits+"-"+v.ID)
	}
	return ""
}

// mcLaunchers lists the PortMaster launcher scripts that belong to one of the
// three ports, by kind — only those get their manifest read.
func mcLaunchers(roots []string) [mcPortCount]portEntry {
	var out [mcPortCount]portEntry
	for _, root := range roots {
		items, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, it := range items {
			name := it.Name()
			if it.IsDir() || strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".sh") ||
				strings.EqualFold(name, "PortMaster.sh") {
				continue
			}
			label := strings.TrimSuffix(name, filepath.Ext(name))
			for k := mcPortKind(0); k < mcPortCount; k++ {
				if out[k].Launch != "" || !mcPortMatches(k, label) {
					continue
				}
				if p := filepath.Join(root, name); regularFile(p) {
					out[k] = portEntry{Name: label, Launch: p}
				}
				break
			}
		}
	}
	return out
}

// mcScanPorts reads the card for the three ports. launcher is PortMaster's
// own launcher ("" when it is not installed).
func mcScanPorts(sdRoot, launcher string) [mcPortCount]mcPortState {
	var out [mcPortCount]mcPortState
	roots := portRoots(sdRoot)
	found := mcLaunchers(roots)
	inbox := mcAutoinstallDir(launcher)
	for k := mcPortKind(0); k < mcPortCount; k++ {
		st := &out[k]
		e := found[k]
		if e.Launch == "" {
			st.Queued = mcQueuedZip(inbox, k)
			continue
		}
		st.Installed, st.Name, st.Launch = true, e.Name, e.Launch
		st.DataDir = mcFindPortDir(roots, k, mcScriptGameDir(st.Launch))
		st.Compat = portCompatForLauncher(st.Launch)
		if st.Compat.State == portCompatUnknown && st.DataDir != "" {
			// Stock TrimUI keeps the script in Roms/PORTS and the data with
			// its manifest in Data/ports.
			st.Compat = portCompatibility(st.DataDir)
		}
		if k == mcPortJava && st.DataDir != "" {
			st.GameDir = mcJavaGameDir(st.DataDir)
			st.Versions = mcLocalVersions(st.GameDir)
			st.Starts = mcStartFiles(st.DataDir)
		}
	}
	return out
}

// mcReadme returns the start of a port's README (its own instructions), "" if
// it has none.
func mcReadme(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	for _, name := range []string{"README.md", "readme.md", "README.txt", "readme.txt", "README"} {
		f, err := os.Open(filepath.Join(dataDir, name))
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(f, 6<<10))
		f.Close()
		text := strings.ReplaceAll(string(data), "\r\n", "\n")
		var lines []string
		for _, l := range strings.Split(text, "\n") {
			l = strings.TrimRight(l, " \t")
			switch {
			case strings.HasPrefix(l, "# "), strings.HasPrefix(l, "### "):
				l = "## " + strings.TrimLeft(l, "# ")
			case strings.HasPrefix(l, "- "), strings.HasPrefix(l, "* "):
				l = "• " + l[2:]
			}
			lines = append(lines, l)
			if len(lines) >= 60 {
				lines = append(lines, "…")
				break
			}
		}
		return strings.TrimSpace(strings.Join(lines, "\n"))
	}
	return ""
}

// ── PortMaster's catalogue ──────────────────────────────────────────────

// mcCatalogEntry is one port in PortMaster's catalogue.
type mcCatalogEntry struct {
	Zip          string // "classicube.zip"
	Title        string
	URL          string
	MD5          string
	Size         int64
	Arch         []string // attr.arch; empty for runtime ports (Java, Godot…)
	Description  string
	Instructions string
	Runtime      []string
	Requirements []string
	MinGlibc     string
	Items        []string // launcher scripts installed by the port
}

// mcParsePortsCatalog reads PortMaster's ports.json. Its layout has changed
// over the years (a dict keyed by zip name, a list, "url" or "download_url",
// a nested "source"), so this walks the document for objects that name a port
// zip instead of trusting one schema.
func mcParsePortsCatalog(raw []byte) []mcCatalogEntry {
	var doc interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []mcCatalogEntry
	var walk func(v interface{}, key string, depth int)
	walk = func(v interface{}, key string, depth int) {
		if depth > 6 {
			return
		}
		switch t := v.(type) {
		case map[string]interface{}:
			if e, ok := mcCatalogFromObject(t, key); ok {
				if !seen[strings.ToLower(e.Zip)] {
					seen[strings.ToLower(e.Zip)] = true
					out = append(out, e)
				}
				return
			}
			for k, child := range t {
				// Utilities and runtimes are not ports.
				if depth == 0 && (k == "utils" || k == "runtimes") {
					continue
				}
				walk(child, k, depth+1)
			}
		case []interface{}:
			for _, child := range t {
				walk(child, "", depth+1)
			}
		}
	}
	walk(doc, "", 0)
	sort.Slice(out, func(i, j int) bool { return out[i].Zip < out[j].Zip })
	return out
}

func mcJSONString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func mcJSONInt(m map[string]interface{}, keys ...string) int64 {
	for _, k := range keys {
		if f, ok := m[k].(float64); ok && f > 0 {
			return int64(f)
		}
	}
	return 0
}

func mcJSONStrings(v interface{}) []string {
	items, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

// mcCatalogFromObject reads one catalogue object (key is its map key, which
// the dict layout uses for the zip name).
func mcCatalogFromObject(m map[string]interface{}, key string) (mcCatalogEntry, bool) {
	e := mcCatalogEntry{Zip: mcJSONString(m, "name")}
	if !strings.HasSuffix(strings.ToLower(e.Zip), ".zip") {
		e.Zip = ""
		if strings.HasSuffix(strings.ToLower(key), ".zip") {
			e.Zip = key
		}
	}
	if e.Zip == "" || strings.ContainsAny(e.Zip, `/\`) || strings.HasPrefix(e.Zip, ".") {
		return mcCatalogEntry{}, false
	}
	e.Items = mcJSONStrings(m["items"])
	attr, _ := m["attr"].(map[string]interface{})
	if attr != nil {
		e.Title = mcJSONString(attr, "title")
		e.Description = mcJSONString(attr, "desc")
		e.Instructions = mcJSONString(attr, "inst")
		e.Runtime = mcJSONStrings(attr["runtime"])
		e.Requirements = mcJSONStrings(attr["reqs"])
		e.MinGlibc = mcJSONString(attr, "min_glibc")
		if list, ok := attr["arch"].([]interface{}); ok {
			for _, a := range list {
				if s, ok := a.(string); ok && strings.TrimSpace(s) != "" {
					e.Arch = append(e.Arch, strings.ToLower(strings.TrimSpace(s)))
				}
			}
		}
	}
	if e.Title == "" {
		e.Title = mcJSONString(m, "title")
	}
	e.URL = mcJSONString(m, "download_url", "url")
	e.MD5 = mcJSONString(m, "md5")
	e.Size = mcJSONInt(m, "download_size", "size")
	if src, ok := m["source"].(map[string]interface{}); ok {
		if e.URL == "" {
			e.URL = mcJSONString(src, "download_url", "url")
		}
		if e.MD5 == "" {
			e.MD5 = mcJSONString(src, "md5")
		}
		if e.Size == 0 {
			e.Size = mcJSONInt(src, "download_size", "size")
		}
	}
	if e.URL == "" {
		e.URL = mcPortDownloadBase + e.Zip
	}
	if !strings.HasPrefix(e.URL, "https://") {
		return mcCatalogEntry{}, false
	}
	if len(e.MD5) != 32 {
		e.MD5 = ""
	}
	return e, true
}

// mcJavaPortNames are the names PortMaster's Java Edition port may go by.
// Anything else Minecraft-named in the catalogue is left to the player to
// pick in PortMaster rather than installed on a guess.
var mcJavaPortNames = map[string]bool{"minecraft": true, "minecraftjava": true, "minecraftjavaedition": true}

// mcCatalogArchOK reports whether a catalogue entry can run on aarch64: no
// arch list (a runtime port) or one that names it.
func mcCatalogArchOK(arch []string) bool {
	if len(arch) == 0 {
		return true
	}
	for _, a := range arch {
		if a == "aarch64" || a == "arm64" {
			return true
		}
	}
	return false
}

// mcCatalogFind picks kind's port: an exact name wins, then the closest title
// ("ClassiCube" over "ClassiCube Extras"). The Java port is only ever taken by
// one of its own names.
func mcCatalogFind(entries []mcCatalogEntry, kind mcPortKind) (mcCatalogEntry, bool) {
	best, bestScore := mcCatalogEntry{}, -1
	want := mcNorm(mcPortSpecs[kind].search)
	for _, e := range entries {
		zipBase := strings.TrimSuffix(e.Zip, filepath.Ext(e.Zip))
		title, zipN := mcNorm(e.Title), mcNorm(zipBase)
		if !mcCatalogArchOK(e.Arch) {
			continue
		}
		if kind == mcPortJava {
			if !mcJavaPortNames[title] && !(title == "" && mcJavaPortNames[zipN]) {
				continue
			}
		} else if !mcPortMatches(kind, e.Title) && !mcPortMatches(kind, zipBase) {
			continue
		}
		name := title
		if name == "" {
			name = zipN
		}
		score := 100 - len(name)
		if title == want || zipN == want {
			score += 1000
		}
		if score > bestScore {
			best, bestScore = e, score
		}
	}
	return best, bestScore >= 0
}

var mcCatalogCache struct {
	mu      sync.Mutex
	at      time.Time
	entries []mcCatalogEntry
}

// mcPortsCatalog returns PortMaster's catalogue, read at most every few hours.
func mcPortsCatalog(ctx context.Context) ([]mcCatalogEntry, error) {
	mcCatalogCache.mu.Lock()
	if len(mcCatalogCache.entries) > 0 && time.Since(mcCatalogCache.at) < mcCatalogTTL {
		out := mcCatalogCache.entries
		mcCatalogCache.mu.Unlock()
		return out, nil
	}
	mcCatalogCache.mu.Unlock()
	rctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, mcPortsCatalogURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Transport: ytTransport()}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("PortMaster's catalogue answered %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, mcCatalogMax))
	if err != nil {
		return nil, err
	}
	entries := mcParsePortsCatalog(raw)
	if len(entries) == 0 {
		return nil, errors.New("PortMaster's catalogue lists no ports this version can read")
	}
	mcCatalogCache.mu.Lock()
	mcCatalogCache.entries, mcCatalogCache.at = entries, time.Now()
	mcCatalogCache.mu.Unlock()
	return entries, nil
}

// mcAutoinstallDir is PortMaster's autoinstall folder for a launcher: zips
// put there are installed the next time PortMaster starts.
func mcAutoinstallDir(launcher string) string {
	if launcher == "" {
		return ""
	}
	dir := filepath.Dir(launcher)
	var pm string
	switch {
	case strings.EqualFold(filepath.Base(launcher), "launch.sh"):
		pm = filepath.Join(dir, "PortMaster") // Apps/PortMaster/launch.sh
	case strings.EqualFold(filepath.Base(dir), "PortMaster"):
		pm = dir // …/PortMaster/PortMaster.sh
	default:
		pm = filepath.Join(dir, "PortMaster") // Ports/PortMaster.sh
	}
	if !dirExists(pm) {
		return ""
	}
	return filepath.Join(pm, "autoinstall")
}

// mcTransfer is the one Minecraft transfer that may run: a port download or a
// copy from a PC. Both drive the shared install card, and a second begin on
// that card would cancel the first.
var mcTransfer struct {
	mu      sync.Mutex
	busy    bool
	copying bool       // a copy from a PC (else a port download)
	port    mcPortKind // the port downloading
	version string     // the version copying
}

// mcTransferClaim takes the transfer slot; false (with the reason) when a
// Minecraft transfer or another download already holds the card.
func mcTransferClaim(copying bool, port mcPortKind, version string) error {
	mcTransfer.mu.Lock()
	defer mcTransfer.mu.Unlock()
	if mcTransfer.busy {
		if mcTransfer.copying {
			return errors.New("already copying " + mcTransfer.version + " — one transfer at a time")
		}
		return errors.New(mcPortSpecs[mcTransfer.port].title + " is downloading — one transfer at a time")
	}
	if installBarActive() {
		return errors.New("another download is running — try again when it finishes")
	}
	mcTransfer.busy, mcTransfer.copying, mcTransfer.port, mcTransfer.version = true, copying, port, version
	return nil
}

func mcTransferRelease() {
	mcTransfer.mu.Lock()
	mcTransfer.busy = false
	mcTransfer.mu.Unlock()
}

func mcPortInstallBusy() (mcPortKind, bool) {
	mcTransfer.mu.Lock()
	defer mcTransfer.mu.Unlock()
	return mcTransfer.port, mcTransfer.busy && !mcTransfer.copying
}

// mcRunContext is the context a transfer runs on. The card's own token stops
// it only when the player cancels this run (the card still carries its
// number); a later begin by another tool retires the card's token too, and
// the transfer then carries on without the card instead of dying halfway.
func mcRunContext(gen uint64, card context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-ctx.Done():
		case <-card.Done():
			if installBarGeneration() == gen {
				cancel()
			}
		}
	}()
	return ctx, cancel
}

// mcCardUpdate reports a phase and progress on the card while it is this
// run's.
func mcCardUpdate(gen uint64, phase string, done, total int64) {
	installBarUpdateRun(gen, phase, done, total)
}

// mcSyncFile flushes a finished file before it takes its real name, so a
// power cut on the card cannot leave a full-size file of garbage behind.
func mcSyncFile(p string) error {
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	serr := f.Sync()
	if cerr := f.Close(); serr == nil {
		serr = cerr
	}
	return serr
}

// mcStartPortInstall fetches kind's port into PortMaster's autoinstall folder
// and, when the player is still on the Minecraft screen, opens PortMaster to
// install it. done runs on the UI thread once the transfer settled.
func mcStartPortInstall(kind mcPortKind, done func()) error {
	if kind < 0 || kind >= mcPortCount {
		return errors.New("unknown port")
	}
	spec := mcPortSpecs[kind]
	launcher := portMasterLauncher(portMasterSDRoot)
	if launcher == "" {
		return errors.New("install PortMaster first")
	}
	inbox := mcAutoinstallDir(launcher)
	if inbox == "" {
		// No autoinstall folder: PortMaster itself is the way, with the name
		// to look for.
		mcOpenPortMaster(launcher, "Search for "+spec.search+" in PortMaster and install it")
		return nil
	}
	if !IsOnline() {
		return errors.New("no internet — connect Wi-Fi, then try again")
	}
	if err := mcTransferClaim(false, kind, ""); err != nil {
		return err
	}

	gen, card := installBarBeginRun(spec.title)
	ctx, cancel := mcRunContext(gen, card)
	go func() {
		defer cancel()
		zipPath, err := mcDownloadPort(ctx, gen, kind, inbox)
		switch {
		case err == nil:
			installBarDoneRun(gen)
			logDebug("minecraft", "port %s queued for PortMaster: %s", spec.id, zipPath)
			tbxPost(func() {
				if mcOnScreen(appConfig) && !gameScreenSuspended() {
					mcOpenPortMaster(launcher, "PortMaster is installing "+spec.title+" — close it when it is done to come back")
				} else {
					ShowToast(spec.title+" is downloaded — open Minecraft and press A to let PortMaster install it", ToastKindSuccess)
				}
			})
		case ctx.Err() != nil:
			// The player cancelled it from the card: nothing to report.
			logDebug("minecraft", "port %s: cancelled", spec.id)
			installBarAbortRun(gen)
		case errors.Is(err, errMcNotInCatalog):
			logWarn("minecraft", "port %s: %v", spec.id, err)
			installBarAbortRun(gen)
			tbxPost(func() {
				if mcOnScreen(appConfig) && !gameScreenSuspended() {
					mcOpenPortMaster(launcher, "Search for "+spec.search+" in PortMaster — JukaHub could not pick it from the catalogue")
				} else {
					ShowToast("Search for "+spec.search+" in PortMaster to install it", ToastKindInfo)
				}
			})
		default:
			logWarn("minecraft", "port %s: %v", spec.id, err)
			installBarFailRun(gen, firstLine(err.Error()))
			tbxPost(func() {
				ShowToast("Could not download "+spec.title+" — "+firstLine(err.Error()), ToastKindError)
			})
		}
		// Released once the card is settled, so the next claim does not see
		// this run still working.
		mcTransferRelease()
		if done != nil {
			tbxPost(done)
		}
	}()
	return nil
}

var errMcNotInCatalog = errors.New("not in PortMaster's catalogue")

// mcDownloadPort downloads kind's zip into inbox and returns its path. The
// zip is written under a hidden name and renamed only once complete, checked
// and flushed, so PortMaster never sees half a file.
func mcDownloadPort(ctx context.Context, gen uint64, kind mcPortKind, inbox string) (string, error) {
	mcCardUpdate(gen, installPhaseResolving, 0, 0)
	entries, err := mcPortsCatalog(ctx)
	if err != nil {
		return "", err
	}
	entry, ok := mcCatalogFind(entries, kind)
	if !ok {
		return "", errMcNotInCatalog
	}
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		return "", fmt.Errorf("PortMaster's autoinstall folder: %w", err)
	}
	final := filepath.Join(inbox, entry.Zip)
	part := filepath.Join(inbox, "."+entry.Zip+".part")
	mcCardUpdate(gen, installPhaseDownload, 0, entry.Size)
	progress := func(done, total int64) { mcCardUpdate(gen, installPhaseDownload, done, total) }
	if err := downloadFileWithProgressCtx(ctx, entry.URL, part, progress); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	if entry.MD5 != "" {
		mcCardUpdate(gen, installPhaseVerify, 0, 0)
		sum, err := mcFileMD5(part)
		if err != nil || !strings.EqualFold(sum, entry.MD5) {
			_ = os.Remove(part)
			return "", fmt.Errorf("the download of %s does not match PortMaster's checksum", entry.Zip)
		}
	}
	if err := mcSyncFile(part); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	if err := os.Rename(part, final); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	return final, nil
}

func mcFileMD5(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// mcQueuedZip finds a downloaded zip of kind's port still waiting in
// PortMaster's autoinstall folder, "" when there is none.
func mcQueuedZip(inbox string, kind mcPortKind) string {
	if inbox == "" {
		return ""
	}
	items, err := os.ReadDir(inbox)
	if err != nil {
		return ""
	}
	for _, it := range items {
		name := it.Name()
		if it.IsDir() || strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".zip") {
			continue
		}
		if mcPortMatches(kind, strings.TrimSuffix(name, filepath.Ext(name))) {
			return filepath.Join(inbox, name)
		}
	}
	return ""
}

// mcOpenPortMaster opens PortMaster with a toast saying why. UI thread.
func mcOpenPortMaster(launcher, why string) {
	if launcher == "" {
		launcher = portMasterLauncher(portMasterSDRoot)
	}
	if launcher == "" {
		ShowToast("PortMaster is not installed", ToastKindWarning)
		return
	}
	if gameScreenSuspended() {
		return
	}
	launchPortScript(portMasterAppName, launcher)
	if why != "" {
		ShowToast(why, ToastKindInfo)
	}
	invalidatePortsCache()
}

// mcPlayPort starts an installed port. UI thread.
func mcPlayPort(kind mcPortKind, st mcPortState) {
	if !st.Installed || st.Launch == "" {
		return
	}
	if st.Compat.State == portCompat32BitOnly && !portCompatIgnored(appConfig) {
		note := st.Compat.Note
		if note == "" {
			note = "32-bit build only"
		}
		ShowToast(st.Name+" cannot run here: "+note, ToastKindWarning)
		return
	}
	logDebug("minecraft", "play %s (%s)", mcPortSpecs[kind].id, st.Launch)
	launchPortScript(setOr(st.Name, mcPortSpecs[kind].title), st.Launch)
}

// ── Bedrock on the handheld ─────────────────────────────────────────────

// mcBedrockState is what the card holds for Bedrock Edition.
type mcBedrockState struct {
	Scanned  bool
	APKs     []apkStoreInfo // Minecraft APKs found (any ABI)
	Runtime  apkStoreRuntime
	Playable int // arm64 ones with the class-pack verdict
}

// mcScanBedrock finds Minecraft APKs the way the APK Store does (same cache,
// so a later visit to the store is quick).
func mcScanBedrock(ctx context.Context) mcBedrockState {
	st := mcBedrockState{Runtime: apkStoreProbeRuntime()}
	res := apkStoreScan(ctx, apkStoreScanRoots(), nil)
	if ctx.Err() != nil {
		return st
	}
	st.Scanned = true
	for _, a := range res.apks {
		isMC := a.Tier == apkTierMinecraft || a.Engine == apkEngineMinecraft ||
			strings.HasPrefix(a.Manifest.Package, "com.mojang.minecraft")
		if !isMC {
			continue
		}
		st.APKs = append(st.APKs, a)
		if a.Tier == apkTierMinecraft {
			st.Playable++
		}
	}
	return st
}

// ── PCs and desktops ────────────────────────────────────────────────────

// mcDesktopApp is a Minecraft launcher installed on this computer.
type mcDesktopApp struct {
	id       string
	title    string
	subtitle string
	launch   func() error
}

// mcJavaHome is this computer's .minecraft folder, "" when there is none.
func mcJavaHome() string {
	for _, p := range mcJavaHomeCandidates() {
		if dirExists(filepath.Join(p, "versions")) {
			return p
		}
	}
	return ""
}
