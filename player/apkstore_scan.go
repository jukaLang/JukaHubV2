package main

// apkstore_scan.go — finding the APKs on the card and reading each one.
//
// The store looks where APKs end up: a folder for them on the card (APKs, or
// Android, or common Roms/Android and Apps/Android folders), the card's Downloads, the Phone
// Companion's drop folder (a phone sends an APK there in one tap), JukaHub's
// own downloads, and on a computer the user's Downloads. The walk is bounded
// — two levels deep, at most apkStoreScanMax files, a few seconds — because a
// card can hold anything and the scan runs every time the screen opens.
//
// Reading an APK is the expensive part (the zip directory, the manifest, the
// resource table, an icon), so each result is cached by path, size and
// modification time: opening the screen again is instant, and a replaced file
// is read again.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"image"
	_ "image/jpeg" // APK icons may be JPEG
	_ "image/png"  // and usually are PNG
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	apkStoreScanMax       = 300             // files listed at most
	apkStoreScanDepth     = 2               // folder levels below each root
	apkStoreScanBudget    = 4 * time.Second // for the directory walk
	apkStoreIconSize      = 64              // icons are kept at 64x64
	apkStoreIconMaxBytes  = 2 << 20         // an icon entry larger than this is skipped
	apkStoreIconMaxPixels = 2048 * 2048     // and so is one that decodes larger
	apkStoreIconsMax      = 120             // icons decoded per scan
	apkStoreDexMaxBytes   = 96 << 20        // DEX bytes searched per APK
	apkStoreCacheMax      = 600             // remembered analyses
	apkStoreCardRoot      = "/mnt/SDCARD"   // the handheld's card
	apkStoreDownloadDir   = "APKs"          // where the store saves what it downloads, on the card
	apkStoreDexChunk      = 256 << 10       // DEX read size
	apkStoreMaxLabelRunes = 80              // a label longer than this is cut
	apkStoreMaxABIs       = 16              // lib/<abi> folders listed at most
	apkStoreEntryReadMax  = 64 << 20        // library bytes searched per APK for an entry point
	apkStoreDynstrMax     = 16 << 20        // a dynamic string table larger than this is not read
	apkStoreELFSections   = 4096            // section headers read at most
)

// apkStoreInfo is what the store knows about one APK file.
type apkStoreInfo struct {
	Path    string
	Size    int64
	ModTime time.Time
	Label   string
	// Manifest is the decoded AndroidManifest.xml; ManifestErr says why it
	// could not be read (triage still runs without it).
	Manifest    apkManifest
	ManifestErr string
	ABIs        []string // every lib/<abi> the APK ships
	ChosenABI   string   // the ABI the game would run
	EntryLib    string   // the native entry selected: lib/<abi>/<name>.so
	MainLib     string   // the profile's mainLib: manifest or exported entry, "" for triage's pick
	LibCount    int      // native libraries in the chosen ABI
	Engine      string
	TriageErr   string
	// Entry is what the libraries said about a native entry point (looked at
	// only for native code behind a Java start).
	Entry     apkStoreEntryCheck
	Tier      apkStoreTier
	Verdict   string
	Why       string
	IconEntry string
	Icon      *image.NRGBA
	// IconTried reports that the icon was looked for; an analysis made after
	// the scan's icon budget ran out is read again for its icon next time.
	IconTried bool
}

// apkStoreCache remembers analyses by path; an entry is valid while the file's
// size and modification time are unchanged.
var apkStoreCache struct {
	mu sync.Mutex
	m  map[string]apkStoreInfo
}

func apkStoreCached(p string, size int64, mod time.Time) (apkStoreInfo, bool) {
	apkStoreCache.mu.Lock()
	defer apkStoreCache.mu.Unlock()
	info, ok := apkStoreCache.m[p]
	if !ok || info.Size != size || !info.ModTime.Equal(mod) {
		return apkStoreInfo{}, false
	}
	return info, true
}

func apkStoreRemember(info apkStoreInfo) {
	apkStoreCache.mu.Lock()
	defer apkStoreCache.mu.Unlock()
	if apkStoreCache.m == nil {
		apkStoreCache.m = map[string]apkStoreInfo{}
	}
	if len(apkStoreCache.m) >= apkStoreCacheMax {
		// Old entries are cheap to read again; dropping them all keeps the
		// cache bounded without bookkeeping.
		apkStoreCache.m = map[string]apkStoreInfo{}
	}
	apkStoreCache.m[info.Path] = info
}

// ── Where to look ───────────────────────────────────────────────────────────

// apkStorePhoneDropDir is the Phone Companion's drop folder without creating
// anything: ftpRootDir makes the desktop "shared" folder, and a scan must not.
func apkStorePhoneDropDir() string {
	if custom := strings.TrimSpace(os.Getenv("JUKAHUB_FTP_ROOT")); custom != "" {
		return filepath.Join(custom, companionDropDir)
	}
	if dirExists(apkStoreCardRoot) {
		return filepath.Join(apkStoreCardRoot, companionDropDir)
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "shared", companionDropDir)
	}
	return ""
}

// apkStoreSaveDir is where a downloaded APK is put: the card's APKs folder on
// the handheld, the data folder elsewhere.
func apkStoreSaveDir() string {
	if dirExists(apkStoreCardRoot) {
		return filepath.Join(apkStoreCardRoot, apkStoreDownloadDir)
	}
	if dir, err := P().DataDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "apkstore", "apks")
	}
	return filepath.Join("data", "apkstore", "apks")
}

// apkStoreScanRoots lists the folders the store searches, existing or not
// (the empty state names them). JUKAHUB_APK_DIRS adds more, separated like
// PATH.
func apkStoreScanRoots() []string {
	var roots []string
	add := func(p string) {
		if p = strings.TrimSpace(p); p != "" {
			roots = append(roots, filepath.Clean(p))
		}
	}
	for _, p := range filepath.SplitList(os.Getenv("JUKAHUB_APK_DIRS")) {
		add(p)
	}
	if IsWindows() {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			add(filepath.Join(home, "Downloads"))
		}
	} else {
		for _, sub := range []string{
			"APKs", "Android", "Roms/ANDROID", "Roms/Android", "Roms/APK", "Roms/APKs", "Roms/Apks",
			"Apps/Android", "Apps/APK", "Data/APKs", "Downloads", companionDropDir,
		} {
			add(filepath.Join(apkStoreCardRoot, filepath.FromSlash(sub)))
		}
		if !IsTrimuiDevice() {
			if home, err := os.UserHomeDir(); err == nil && home != "" {
				add(filepath.Join(home, "Downloads"))
			}
		}
	}
	add(apkStorePhoneDropDir())
	add(apkStoreSaveDir())
	add(downloadsRoot())
	return apkStoreDedupePaths(roots)
}

// apkStoreDedupePaths drops repeats (case-insensitively on Windows), keeping
// the first.
func apkStoreDedupePaths(paths []string) []string {
	seen := map[string]bool{}
	out := paths[:0]
	for _, p := range paths {
		key := p
		if IsWindows() {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out
}

// ── The walk ────────────────────────────────────────────────────────────────

// apkStoreFind walks roots for .apk files, depth-bounded, count-bounded and
// time-bounded. truncated reports that a bound cut the walk short. Hidden
// folders and symlinked folders are not entered (a link can loop).
func apkStoreFind(ctx context.Context, roots []string, maxFiles, maxDepth int, budget time.Duration) (files []string, searched []string, truncated bool) {
	deadline := time.Now().Add(budget)
	seen := map[string]bool{}
	var walk func(dir string, depth int) bool
	walk = func(dir string, depth int) bool {
		if ctx.Err() != nil || time.Now().After(deadline) {
			truncated = true
			return false
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return true
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			full := filepath.Join(dir, name)
			typ := e.Type()
			switch {
			case typ.IsDir():
				if depth < maxDepth && !walk(full, depth+1) {
					return false
				}
			case typ.IsRegular() || typ&os.ModeSymlink != 0:
				if !strings.EqualFold(filepath.Ext(name), ".apk") {
					continue
				}
				if typ&os.ModeSymlink != 0 {
					// A link to an APK is fine; a link to anything else is not
					// followed.
					if st, err := os.Stat(full); err != nil || !st.Mode().IsRegular() {
						continue
					}
				}
				key := full
				if IsWindows() {
					key = strings.ToLower(key)
				}
				if seen[key] {
					continue
				}
				seen[key] = true
				files = append(files, full)
				if len(files) >= maxFiles {
					truncated = true
					return false
				}
			}
		}
		return true
	}
	for _, root := range roots {
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			continue
		}
		searched = append(searched, root)
		if !walk(root, 0) {
			break
		}
	}
	return files, searched, truncated
}

// apkStoreScanResult is one finished scan.
type apkStoreScanResult struct {
	apks      []apkStoreInfo
	searched  []string
	truncated bool
	took      time.Duration
}

// apkStoreScan finds and reads every APK. progress (may be nil) is called with
// how many have been read so far.
func apkStoreScan(ctx context.Context, roots []string, progress func(done, total int)) apkStoreScanResult {
	start := time.Now()
	files, searched, truncated := apkStoreFind(ctx, roots, apkStoreScanMax, apkStoreScanDepth, apkStoreScanBudget)
	res := apkStoreScanResult{searched: searched, truncated: truncated}
	icons := 0
	for i, f := range files {
		if ctx.Err() != nil {
			res.truncated = true
			break
		}
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		info, ok := apkStoreCached(f, st.Size(), st.ModTime())
		if !ok || !info.IconTried && icons < apkStoreIconsMax {
			info = apkStoreAnalyze(f, icons < apkStoreIconsMax)
			apkStoreRemember(info)
		}
		if info.Icon != nil {
			icons++
		}
		res.apks = append(res.apks, info)
		if progress != nil {
			progress(i+1, len(files))
		}
	}
	apkStoreSort(res.apks)
	res.took = time.Since(start)
	return res
}

// apkStoreSort orders the list: what can be tried first, then by name.
func apkStoreSort(apks []apkStoreInfo) {
	sort.SliceStable(apks, func(i, j int) bool {
		if ri, rj := apks[i].Tier.rank(), apks[j].Tier.rank(); ri != rj {
			return ri < rj
		}
		li, lj := strings.ToLower(apks[i].Label), strings.ToLower(apks[j].Label)
		if li != lj {
			return li < lj
		}
		return apks[i].Path < apks[j].Path
	})
}

// ── Reading one APK ─────────────────────────────────────────────────────────

// apkStoreFileLabel is the name to show when the manifest has none: the file
// name without its extension, version suffixes and separators tidied.
func apkStoreFileLabel(p string) string {
	base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	base = strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(base)
	base = strings.Join(strings.Fields(base), " ")
	if base == "" {
		return "APK"
	}
	return apkStoreClipLabel(base)
}

// apkStoreClipLabel bounds a label from the APK (it is drawn and stored).
func apkStoreClipLabel(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > apkStoreMaxLabelRunes {
		s = string(r[:apkStoreMaxLabelRunes-1]) + "…"
	}
	return s
}

// apkStoreIconRank scores a res/ bitmap as a launcher-icon candidate by its
// base name; 0 means it is not one.
func apkStoreIconRank(entry string) int {
	base := strings.ToLower(strings.TrimSuffix(path.Base(entry), path.Ext(entry)))
	switch {
	case base == "ic_launcher":
		return 5
	case base == "app_icon":
		return 4
	case base == "icon":
		return 3
	case base == "ic_launcher_round":
		return 2
	case strings.Contains(base, "launcher") && !strings.Contains(base, "background"):
		return 1
	}
	return 0
}

// apkStoreAnalyze reads one APK: the zip directory, the manifest, the resource
// table when the label or icon needs it, AndroidPak's triage, the engine and
// the verdict. withIcon decodes the launcher icon too. It never panics: a file
// that breaks the reader is a broken APK.
func apkStoreAnalyze(p string, withIcon bool) (info apkStoreInfo) {
	info = apkStoreInfo{Path: p, Label: apkStoreFileLabel(p)}
	defer func() {
		if r := recover(); r != nil {
			info.Tier, info.Verdict = apkTierBroken, "Not a readable APK"
			info.Why = "Reading it failed unexpectedly; the file may be damaged."
			info.Icon = nil
		}
	}()
	st, err := os.Stat(p)
	if err != nil {
		info.Tier, info.Verdict, info.Why = apkTierBroken, "Not a readable APK", err.Error()
		return info
	}
	info.Size, info.ModTime = st.Size(), st.ModTime()
	zr, err := zip.OpenReader(p)
	if err != nil {
		info.Tier, info.Verdict = apkTierBroken, "Not a readable APK"
		info.Why = "It is not a zip archive, so it is not an Android package (" + err.Error() + ")."
		return info
	}
	defer zr.Close()

	var manifestF, arscF *zip.File
	var dex []*zip.File
	abiSeen := map[string]bool{}
	libSeen := map[string]bool{}
	var libNames []string
	byName := map[string]*zip.File{}
	iconGuess, iconGuessRank, iconGuessSize := "", 0, uint64(0)
	for _, f := range zr.File {
		name := strings.TrimPrefix(filepath.ToSlash(f.Name), "/")
		if len(byName) < 65536 {
			byName[name] = f
		}
		switch {
		case name == "AndroidManifest.xml":
			manifestF = f
		case name == "resources.arsc":
			arscF = f
		case !strings.Contains(name, "/") && strings.HasPrefix(name, "classes") && strings.HasSuffix(name, ".dex"):
			if len(dex) < 8 {
				dex = append(dex, f)
			}
		case strings.HasPrefix(name, "lib/") && strings.HasSuffix(name, ".so"):
			parts := strings.SplitN(strings.TrimPrefix(name, "lib/"), "/", 2)
			if len(parts) == 2 && parts[1] != "" && !strings.Contains(parts[1], "/") {
				if !abiSeen[parts[0]] && len(info.ABIs) < apkStoreMaxABIs {
					abiSeen[parts[0]] = true
					info.ABIs = append(info.ABIs, parts[0])
				}
				if !libSeen[parts[1]] && len(libNames) < 512 {
					libSeen[parts[1]] = true
					libNames = append(libNames, parts[1])
				}
			}
		case strings.HasPrefix(name, "res/") && apkStoreDecodableImage(name):
			if r := apkStoreIconRank(name); r > 0 && f.UncompressedSize64 <= apkStoreIconMaxBytes {
				if r > iconGuessRank || r == iconGuessRank && f.UncompressedSize64 > iconGuessSize {
					iconGuess, iconGuessRank, iconGuessSize = name, r, f.UncompressedSize64
				}
			}
		}
	}
	sort.Strings(info.ABIs)

	// The manifest, then the resource table only when a reference needs it.
	if manifestF != nil {
		if raw, err := apkxReadEntry(manifestF, apkxMaxManifestBytes); err != nil {
			info.ManifestErr = err.Error()
		} else if m, err := parseApkManifest(raw); err != nil {
			info.ManifestErr = err.Error()
		} else {
			info.Manifest = m
		}
	} else {
		info.ManifestErr = "no AndroidManifest.xml"
	}
	m := info.Manifest
	if m.Label != "" {
		info.Label = apkStoreClipLabel(m.Label)
	}
	// One table for the label and the icon: they share its decoding budget.
	var table *apkxTable
	if arscF != nil && (m.Label == "" && m.LabelRef != 0 || withIcon && m.IconRef != 0) {
		if arsc, err := apkxReadEntry(arscF, apkxMaxArscBytes); err == nil {
			table, _ = apkxOpenTable(arsc)
		}
	}
	if m.Label == "" && m.LabelRef != 0 && table != nil {
		if label := table.label(m.LabelRef); label != "" {
			info.Label = apkStoreClipLabel(label)
		}
	}
	// The base piece of a split app does not always say so in its manifest;
	// its pieces lying beside it (split_config.*.apk) do.
	splitsRequired := m.SplitsRequired || apkStoreHasSplitSiblings(p)

	// AndroidPak's triage is the authority on what can be staged.
	libs, terr := inspectAndroidPak(p, "")
	var chosenLibs []*zip.File
	if terr != nil {
		info.TriageErr = terr.Error()
	} else {
		info.ChosenABI, info.EntryLib = libs.Chosen, libs.MainLib
		prefix := "lib/" + libs.Chosen + "/"
		for name, f := range byName {
			if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".so") && !strings.Contains(name[len(prefix):], "/") {
				info.LibCount++
				chosenLibs = append(chosenLibs, f)
			}
		}
		// NativeActivity loads exactly the library its meta-data names —
		// android.app.lib_name "X" is libX.so, as written — and the triage
		// default (the largest .so) can be a helper library instead. A value
		// that already carries "lib" or ".so" is tried without them after.
		if lib := strings.TrimSpace(m.NativeLib); lib != "" && !strings.ContainsAny(lib, `/\`) {
			for _, want := range []string{"lib" + lib + ".so", "lib" + strings.TrimPrefix(strings.TrimSuffix(lib, ".so"), "lib") + ".so"} {
				if _, ok := byName[prefix+want]; ok {
					info.MainLib = want
					info.EntryLib = prefix + want
					break
				}
			}
		}
		// Without android.app.lib_name, the largest .so is only a guess. In
		// multi-library APKs it may be a helper that fails to load before the
		// actual NativeActivity or GameActivity entry is reached. Select the
		// library exporting the entry when the bounded scan can find one.
		if info.MainLib == "" && libs.Chosen == "arm64-v8a" && m.Split == "" && !splitsRequired {
			var needles [][]byte
			switch m.Launcher {
			case "android.app.NativeActivity":
				needles = apkStoreNativeActivityEntryNeedles
			case "com.google.androidgamesdk.GameActivity":
				needles = apkStoreGameActivityLaunchNeedles
			}
			if len(needles) > 0 {
				if entry, check := apkStoreFindEntryLibrary(p, chosenLibs, needles); check == apkEntryFound {
					info.EntryLib = entry
					info.MainLib = filepath.Base(entry)
				}
			}
		}
	}

	dexNative, dexGameActivity := false, false
	engine := apkStoreDetectEngine(libNames, m.Activities, m.Launcher, false, false)
	if terr == nil && engine == "" && m.HasCode && libs.Chosen == "arm64-v8a" && m.Split == "" && !splitsRequired {
		// Native code behind a Java start. AndroidRT never runs the Java
		// side: it starts the game from a native entry point, so first ask
		// the libraries for one. A Java app with helper libraries has none,
		// and that answer needs no DEX search.
		var entry string
		entry, info.Entry = apkStoreFindEntryLibrary(p, chosenLibs, apkStoreEntryNeedles)
		if info.Entry == apkEntryFound {
			info.EntryLib = entry
			info.MainLib = filepath.Base(entry)
		}
		if info.Entry != apkEntryNone && len(dex) > 0 {
			dexNative, dexGameActivity = apkStoreScanDex(dex)
			engine = apkStoreDetectEngine(libNames, m.Activities, m.Launcher, dexNative, dexGameActivity)
		}
	}
	info.Engine = engine
	info.Tier, info.Verdict, info.Why = apkStoreVerdict(apkStoreVerdictInput{
		TriageErr:      info.TriageErr,
		ManifestErr:    info.ManifestErr,
		ABIs:           info.ABIs,
		ChosenABI:      info.ChosenABI,
		Minecraft:      terr == nil && libs.Title.Name == "Minecraft",
		Engine:         engine,
		HasCode:        m.HasCode || info.ManifestErr != "",
		Launcher:       m.Launcher,
		MinSDK:         m.MinSDK,
		Split:          m.Split,
		SplitsRequired: splitsRequired,
		Entry:          info.Entry,
	})

	if withIcon {
		info.IconTried = true
		entry := ""
		if m.IconRef != 0 && table != nil {
			entry = table.iconPath(m.IconRef)
		}
		if _, ok := byName[entry]; !ok {
			entry = iconGuess
		}
		if f := byName[entry]; f != nil {
			info.IconEntry = entry
			info.Icon = apkStoreDecodeIcon(f)
		}
	}
	return info
}

// apkStoreDecodeIcon decodes a bitmap entry into a small NRGBA, refusing
// oversized files and images.
func apkStoreDecodeIcon(f *zip.File) *image.NRGBA {
	raw, err := apkxReadEntry(f, apkStoreIconMaxBytes)
	if err != nil {
		return nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > apkStoreIconMaxPixels {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	return imgvScale(img, apkStoreIconSize, apkStoreIconSize)
}

// apkStoreDexNeedles are the class descriptors that mark a thin Java front for
// a native game.
var (
	apkStoreDexNativeActivity = []byte("Landroid/app/NativeActivity;")
	apkStoreDexGameActivity   = []byte("Lcom/google/androidgamesdk/GameActivity;")
)

// apkStoreScanDex streams the DEX files (bounded) for the two descriptors. The
// string section sits near the front of a DEX, so most finds are quick.
func apkStoreScanDex(files []*zip.File) (native, gameActivity bool) {
	budget := int64(apkStoreDexMaxBytes)
	overlap := len(apkStoreDexGameActivity)
	buf := make([]byte, apkStoreDexChunk+overlap)
	for _, f := range files {
		if budget <= 0 || native && gameActivity {
			break
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		keep := 0
		for budget > 0 {
			n, err := io.ReadFull(rc, buf[keep:keep+apkStoreDexChunk])
			if n > 0 {
				budget -= int64(n)
				window := buf[:keep+n]
				native = native || bytes.Contains(window, apkStoreDexNativeActivity)
				gameActivity = gameActivity || bytes.Contains(window, apkStoreDexGameActivity)
				if native && gameActivity {
					break
				}
				// Keep the tail so a descriptor split across reads is found.
				keep = overlap
				if keep > len(window) {
					keep = len(window)
				}
				copy(buf, window[len(window)-keep:])
			}
			if err != nil {
				break
			}
		}
		rc.Close()
	}
	return native, gameActivity
}

// ── Native entry points ─────────────────────────────────────────────────────

// apkStoreEntryNeedles are the exported names AndroidRT starts a game from
// (player/androidrt/src/runner.c): NativeActivity's onCreate, GameActivity's
// native glue, and the JNI name of GameActivity's initializeNativeCode. Each
// ends in the NUL that ends it in the dynamic string table.
var apkStoreNativeActivityEntryNeedles = [][]byte{
	[]byte("ANativeActivity_onCreate\x00"),
}

var apkStoreGameActivityLaunchNeedles = [][]byte{
	[]byte("Java_com_google_androidgamesdk_GameActivity_initializeNativeCode\x00"),
}

var apkStoreEntryNeedles = [][]byte{
	[]byte("ANativeActivity_onCreate\x00"),
	[]byte("GameActivity_onCreate\x00"),
	[]byte("Java_com_google_androidgamesdk_GameActivity_initializeNativeCode\x00"),
}

// apkStoreHasEntryName reports whether b holds one of the entry names.
func apkStoreHasEntryName(b []byte, needles [][]byte) bool {
	for _, n := range needles {
		if bytes.Contains(b, n) {
			return true
		}
	}
	return false
}

// apkStoreFindEntryLibrary returns the chosen ABI's library that exports one
// of needles. A stored library is read through its ELF dynamic string table;
// a compressed one is searched as a stream. Reads are bounded per APK.
func apkStoreFindEntryLibrary(apk string, libs []*zip.File, needles [][]byte) (string, apkStoreEntryCheck) {
	if len(libs) == 0 {
		return "", apkEntryUnchecked
	}
	// Ask smaller libraries first. A large engine/helper could exhaust the
	// bounded read before a small activity glue library is inspected.
	ordered := append([]*zip.File(nil), libs...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].UncompressedSize64 != ordered[j].UncompressedSize64 {
			return ordered[i].UncompressedSize64 < ordered[j].UncompressedSize64
		}
		return ordered[i].Name < ordered[j].Name
	})
	var file *os.File
	defer func() {
		if file != nil {
			file.Close()
		}
	}()
	budget := int64(apkStoreEntryReadMax)
	complete := true
	for _, f := range ordered {
		if f.Method == zip.Store {
			if file == nil {
				file, _ = os.Open(apk)
			}
			if file != nil {
				if off, err := f.DataOffset(); err == nil {
					found, ok := apkStoreELFExportsEntry(io.NewSectionReader(file, off, int64(f.CompressedSize64)), &budget, needles)
					if found {
						return strings.TrimPrefix(filepath.ToSlash(f.Name), "/"), apkEntryFound
					}
					if ok {
						continue
					}
				}
			}
		}
		if budget <= 0 {
			complete = false
			break
		}
		found, whole := apkStoreStreamHasEntry(f, &budget, needles)
		if found {
			return strings.TrimPrefix(filepath.ToSlash(f.Name), "/"), apkEntryFound
		}
		if !whole {
			complete = false
		}
	}
	if complete {
		return "", apkEntryNone
	}
	return "", apkEntryUnknown
}

// apkStoreELFExportsEntry reads a 64-bit little-endian ELF library's dynamic
// string table (the string table its .dynsym links to) and searches it.
// ok is false when the file is not such an ELF or its tables do not add up;
// the caller then searches the bytes as a stream instead.
func apkStoreELFExportsEntry(r *io.SectionReader, budget *int64, needles [][]byte) (found, ok bool) {
	size := r.Size()
	var hdr [64]byte
	if size < int64(len(hdr)) {
		return false, false
	}
	if _, err := r.ReadAt(hdr[:], 0); err != nil {
		return false, false
	}
	if !bytes.Equal(hdr[:4], []byte("\x7fELF")) || hdr[4] != 2 || hdr[5] != 1 {
		return false, false
	}
	le := binary.LittleEndian
	shoff := le.Uint64(hdr[0x28:])
	shentsize := int64(le.Uint16(hdr[0x3A:]))
	shnum := int64(le.Uint16(hdr[0x3C:]))
	// Elf64_Shdr is 64 bytes; anything else is not a file to trust.
	if shentsize != 64 || shnum < 1 || shnum > apkStoreELFSections || shoff > uint64(size) ||
		int64(shoff)+shnum*shentsize > size {
		return false, false
	}
	table := make([]byte, shnum*shentsize)
	if _, err := r.ReadAt(table, int64(shoff)); err != nil {
		return false, false
	}
	section := func(i int64) []byte { return table[i*shentsize : i*shentsize+64] }
	for i := int64(0); i < shnum; i++ {
		sh := section(i)
		if le.Uint32(sh[4:]) != 11 { // SHT_DYNSYM
			continue
		}
		link := int64(le.Uint32(sh[40:]))
		if link <= 0 || link >= shnum {
			return false, false
		}
		str := section(link)
		off, n := le.Uint64(str[24:]), le.Uint64(str[32:])
		if le.Uint32(str[4:]) != 3 || n > apkStoreDynstrMax || off > uint64(size) || n > uint64(size)-off { // SHT_STRTAB
			return false, false
		}
		if int64(n) > *budget {
			return false, false
		}
		*budget -= int64(n)
		dynstr := make([]byte, n)
		if _, err := r.ReadAt(dynstr, int64(off)); err != nil {
			return false, false
		}
		return apkStoreHasEntryName(dynstr, needles), true
	}
	// No dynamic symbols at all: nothing is exported, so there is no entry.
	return false, true
}

// apkStoreStreamHasEntry searches a library's bytes for the entry names,
// charging what it reads to budget. whole reports that the library was read to
// its end.
func apkStoreStreamHasEntry(f *zip.File, budget *int64, needles [][]byte) (found, whole bool) {
	rc, err := f.Open()
	if err != nil {
		return false, false
	}
	defer rc.Close()
	overlap := 0
	for _, n := range needles {
		if len(n) > overlap {
			overlap = len(n)
		}
	}
	buf := make([]byte, apkStoreDexChunk+overlap)
	keep := 0
	for *budget > 0 {
		want := apkStoreDexChunk
		if int64(want) > *budget {
			want = int(*budget)
		}
		n, err := io.ReadFull(rc, buf[keep:keep+want])
		if n > 0 {
			*budget -= int64(n)
			window := buf[:keep+n]
			if apkStoreHasEntryName(window, needles) {
				return true, false
			}
			keep = overlap
			if keep > len(window) {
				keep = len(window)
			}
			copy(buf, window[len(window)-keep:])
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return false, true
		}
		if err != nil {
			return false, false
		}
	}
	return false, false
}

// apkStoreHasSplitSiblings reports whether p is the base.apk of a split app
// whose pieces lie beside it (split_config.arm64_v8a.apk and friends), the
// way APK extractors save an App Bundle install.
func apkStoreHasSplitSiblings(p string) bool {
	if !strings.EqualFold(filepath.Base(p), "base.apk") {
		return false
	}
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		return false
	}
	for i, e := range entries {
		if i >= 512 {
			break
		}
		name := strings.ToLower(e.Name())
		if strings.HasPrefix(name, "split_") && strings.HasSuffix(name, ".apk") {
			return true
		}
	}
	return false
}

// ── The runtime ─────────────────────────────────────────────────────────────

// apkStoreRuntime is what the Runtime tab reports.
type apkStoreRuntime struct {
	dir     string               // the AndroidRT folder, "" when none was found
	err     string               // why not
	form    string               // "loader" or "launch.sh"
	source  androidRuntimeSource // which copy: bundled, downloaded, the user's own
	tag     string               // the downloaded copy's release
	skipped []string             // folders passed over, and why
	host    string               // GOOS/GOARCH
	canRun  bool                 // AndroidRT runs arm64 Android code on aarch64 Linux only
	memMB   int64                // total RAM, 0 when unknown
	handset bool                 // a TrimUI handheld
}

// apkStoreProbeRuntime looks for AndroidRT the way a launch does.
func apkStoreProbeRuntime() apkStoreRuntime {
	rt := apkStoreRuntime{
		host:    runtime.GOOS + "/" + runtime.GOARCH,
		canRun:  androidRTSupported(),
		handset: IsTrimuiDevice(),
		memMB:   apkStoreMemTotalMB("/proc/meminfo"),
	}
	found, err := findAndroidRuntime(exec.LookPath)
	rt.skipped = found.Skipped
	if err != nil {
		rt.err = err.Error()
		return rt
	}
	rt.dir, rt.source = found.Dir, found.Source
	if found.Source == androidRTSourceDownloaded {
		if info, ok := relPackRead(found.Dir); ok {
			rt.tag = info.Tag
		}
	}
	switch {
	case regularFile(filepath.Join(found.Dir, "loader")):
		rt.form = "loader"
	case regularFile(filepath.Join(found.Dir, "launch.sh")):
		rt.form = "launch.sh"
	}
	// The launch's own check: a JUKAHUB_ANDROIDRT folder is taken as given, so
	// what is wrong with it shows here.
	if _, _, cmdErr := androidRuntimeCommand(found.Dir, exec.LookPath); cmdErr != nil {
		rt.err = cmdErr.Error()
	}
	return rt
}

// apkStoreMemTotalMB reads MemTotal (MiB) from a meminfo file; 0 when absent.
func apkStoreMemTotalMB(file string) int64 {
	raw, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		rest, ok := strings.CutPrefix(line, "MemTotal:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || kb <= 0 {
			return 0
		}
		return kb / 1024
	}
	return 0
}
