package main

// packages_test.go — the Packages store: the catalog model (categories,
// versions, lenient parsing, shelves, filters), the files it keeps next to the
// packages, the auto-refresh and offline rules, every device's input, and the
// rendered views.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// pkgDrainEventsForTest throws away events a background task of an earlier
// test may have left behind.
func pkgDrainEventsForTest() {
	for {
		select {
		case <-pkgEvents:
		default:
			return
		}
	}
}

// pkgForceOnlineForTest pins the connectivity monitor online for one test, so
// a test never depends on whether the machine running it has a network (the
// twin of forceOfflineForTest).
func pkgForceOnlineForTest(t *testing.T) {
	t.Helper()
	netMon.mu.Lock()
	online, checked, reason := netMon.online, netMon.checked, netMon.reason
	netMon.online, netMon.checked, netMon.reason = true, true, ""
	netMon.mu.Unlock()
	t.Cleanup(func() {
		netMon.mu.Lock()
		netMon.online, netMon.checked, netMon.reason = online, checked, reason
		netMon.mu.Unlock()
	})
}

// pkgTestState isolates the store for one test: a temporary packages folder,
// a fresh state that counts as entered (so nothing starts a scan or a fetch),
// the connection pinned online, a registry fetcher that never touches the
// network, and everything restored afterwards.
func pkgTestState(t *testing.T) {
	t.Helper()
	pkgForceOnlineForTest(t)
	prevDir := pkgInstallDirOverride
	prevUI := pkgUI
	prevHook := packageInstallHook
	prevFetcher := pkgCatalogFetcher
	prevRunner := pkgInstallRunner
	prevEngine := focusEngine
	focusEngine = NewFocusEngine()
	installedPackagesMu.Lock()
	prevInstalled := installedPackages
	installedPackages = map[string]bool{}
	installedPackagesMu.Unlock()

	pkgInstallDirOverride = t.TempDir()
	pkgDrainEventsForTest()
	pkgUI = pkgState{entered: true, prefsLoaded: true, online: IsOnline(), appChecked: sdl.GetTicks64()}
	pkgEnsureMaps()
	pkgCatalogFetcher = func(context.Context) (string, error) {
		return "", errors.New("no network in tests")
	}
	t.Cleanup(func() {
		pkgDrainEventsForTest()
		pkgUI = prevUI
		pkgInstallDirOverride = prevDir
		packageInstallHook = prevHook
		pkgCatalogFetcher = prevFetcher
		pkgInstallRunner = prevRunner
		focusEngine = prevEngine
		installedPackagesMu.Lock()
		installedPackages = prevInstalled
		installedPackagesMu.Unlock()
	})
}

// pkgEntryFor builds a registry entry.
func pkgEntryFor(name, version, date, desc string) pkgEntry {
	return pkgEntry{Pkg: Package{
		Name: name, Version: version, Date: date, Description: desc,
		Author: "someone", Download: "https://github.com/org/" + strings.ReplaceAll(name, " ", "") + "/releases/download/v1/pkg.zip",
	}}
}

// pkgSeed puts a catalog and an installed set into the store and builds it.
func pkgSeed(t *testing.T, entries []pkgEntry, installed ...string) {
	t.Helper()
	pkgUI.entries = entries
	pkgUI.haveCatalog = true
	pkgUI.cacheTried = true
	for _, name := range installed {
		pkgUI.installed[packageDirName(name)] = true
	}
	pkgRebuild()
}

// pkgTestEntries is a small catalog: games, look, network and system
// packages with dates, so every shelf has something.
func pkgTestEntries() []pkgEntry {
	return []pkgEntry{
		pkgEntryFor("EmuDrop", "2.1.6", "2025-01-28", "Feature-rich ROM downloader with built-in screen scraper."),
		pkgEntryFor("BGM Swap", "2.0", "2024-05-12", "Replaces the current theme's background music."),
		pkgEntryFor("FTP", "1.0", "2024-04-04", "Starts an FTP server on the device."),
		pkgEntryFor("Reboot", "1.0", "2024-04-04", "A simple app to restart the device."),
		pkgEntryFor("Random Game", "1.0", "2024-04-04", "Picks a random emulator and a matching ROM and launches it."),
	}
}

// ── Model ────────────────────────────────────────────────────────────────

// Every package in today's registry lands on the shelf a person would put it
// on: the classifier weighs the name over the description, so a backup tool
// that names six emulators is still a backup tool.
func TestPkgClassifyRegistry(t *testing.T) {
	cases := []struct{ name, desc, want string }{
		{"Backup Config Files", "Backs up emulator configuration files for PPSSPP, NDS (DraStic), and RetroArch — including button mappings, core configs, and shader settings — to a timestamped archive on the SDCARD. Run before any major system change to protect your settings.", "system"},
		{"BGM Start/Stop", "Toggles background music on or off for the currently active theme. If the theme folder has a bgm.mp3 in its sound directory, this app will start or stop it. Per-theme state is remembered between runs.", "look"},
		{"BGM Swap", "Replaces the current theme's background music with a custom bgm.mp3 placed in the root of the SDCARD. Updates the BGM for whichever theme is currently active. The original BGM is backed up on first run as bgm-bak.mp3.", "look"},
		{"BootLogo", "A user-friendly app for flashing a custom boot logo on the TrimUI Smart Pro. Place your image on the SD card and run the app to apply it at startup.", "look"},
		{"Bootlogo Updater", "Replaces the startup boot image with a custom BMP file. Place a logo.bmp (max 1280x720, under 6 MB) on the root of SDCARD or UDISK and run this app to apply it.", "look"},
		{"CollaGen", "CLI-based game collection generator for the TrimUI Smart Pro. Dynamically creates and refreshes ROM collections using keyword include/exclude filters. Compatible with CrossMix-OS and, with minor edits, Stock OS. Copy the CollaGen folder to /Apps, launch it, and use the on-screen terminal to build named collections.", "games"},
		{"DNS Changer", "Changes the DNS server settings on the TrimUI Smart Pro and TrimUI Brick. Useful for improving connectivity, bypassing region restrictions, or using custom DNS resolvers. Built with Python and SDL2 for a controller-friendly on-device UI.", "network"},
		{"EmuCleaner", "Automatically hides emulators that have no ROMs installed and re-enables those that do. Prevents a cluttered console list and is also useful after adding new ROMs to refresh which emulators are visible. Edits Emus/show.json using the same mechanism as the stock MainUI, so manual show/hide overrides still work.", "emulators"},
		{"EmuDrop", "Feature-rich ROM downloader with built-in screen scraper for TrimUI Smart Pro and TrimUI Brick. Provides a sleek SDL2 controller-friendly interface to browse platforms and game libraries, search, download ROMs, and pull cover art from multiple sources. Compatible with Stock OS, CrossMix-OS, and Knulli.", "games"},
		{"FTP", "Starts an FTP server on the device. Run again to stop it. Automatically times out after 5 minutes of inactivity. Optionally modify the script to set a root password.", "network"},
		{"FTP Enable", "Enables FTP at every boot by mounting /mnt/SDCARD. Run once to enable persistent FTP access (user: root, pass: root). Run again to disable the boot entry.", "network"},
		{"LED Batt", "Colors the LEDs to indicate remaining battery percentage in real time: green (100-90%) through red (<20%). Run the app again to stop the effect.", "look"},
		{"LED Temp", "Colors the LEDs to indicate CPU temperature in real time: green (<40C) through red (>70C). Run the app again to stop the effect.", "look"},
		{"Mac Update", "Sets the device Wi-Fi MAC address to a static value to prevent connectivity issues across reboots. Not required on firmware 1.0.4 hotfix 20240414 or later.", "network"},
		{"Max CPU", "Forces the device into Performance CPU governor mode and keeps it there even after resets or FN button presses. LEDs turn green on success.", "system"},
		{"OctoLair", "Downloads ROMs directly onto the TrimUI Smart Pro from within the device, with a controller-friendly SDL2 UI. Browse systems and game listings, select titles, and download them straight to the appropriate ROM folder without touching a PC.", "games"},
		{"RainbowLEDs", "Cycles the joystick and middle LEDs through a full rainbow of colors in a continuous loop. Run the app again to stop the effect.", "look"},
		{"Random Game", "Picks a random emulator and a matching ROM from your SDCARD and launches it. Configurable base directories for Roms and Emus via script edit.", "games"},
		{"Reboot", "A simple app to restart the TrimUI Smart Pro quickly and cleanly from within the menu.", "system"},
		{"Restore Config Files", "Restores a previously created configuration backup for PPSSPP, NDS (DraStic), and RetroArch. Reads the latest backup version from ForRestoring.txt in the backup folder, or restore a specific version by editing that file. Requires a prior backup to exist.", "system"},
		{"Resume at Boot", "A set of scripts that adds a resume-last-game feature on startup, automatically relaunching the most recently played game when the device powers on.", "games"},
		{"Scraper", "A command-line ScreenScraper implementation that automatically downloads box art and media for your ROM library directly on the device.", "games"},
		{"Syncthing", "Installs and runs Syncthing on the TrimUI Smart Pro for wireless file synchronization between the device and a PC or phone. Optionally enables Syncthing on boot. Default credentials: user trimui / pass trimuisync. Requires TRIMUI_EX or TomatoOS.", "network"},
		{"System Tools", "A hub app that centralizes multiple utility scripts and tools in one place, including a PSX Analog Detector that auto-configures controller settings for PlayStation games compatible with analog sticks.", "system"},
		{"Telnetd", "Installs a static telnetd binary and adds it to rc.local so the device is accessible via Telnet on port 23 at every boot. Sets root credentials to user: root, pass: root. Only needs to be run once.", "network"},
		{"ThemixPro", "Automatically updates system icons, selected-state icons, and background images whenever a theme is changed. Themes must include icon, iconsel, and background subfolders with files named after each emulator system (e.g. SFC.png). Ships with the GREYS-DARK theme by tenlevels. Requires TRIMUI_EX or TomatoOS.", "look"},
		{"UDISK", "Formats and unlocks the internal UDISK partition as FAT32, exposing ~6 GB of on-device storage for apps, emulators, and ROMs. Also replaces the stock usb_storage app to allow simultaneous SDCARD and UDISK USB mounting. USE AT YOUR OWN RISK — formats UDISK partition. Only needs to be run once.", "system"},
		{"Thing", "Does something.", "other"},
	}
	for _, c := range cases {
		if got := pkgClassify(c.name, c.desc); got != c.want {
			t.Errorf("pkgClassify(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// A registry that names its own category wins over the classifier; a name the
// store does not know becomes a shelf of its own.
func TestPkgCategoryFromTheRegistry(t *testing.T) {
	cases := []struct {
		category, wantID, wantTitle string
	}{
		{"Themes", "look", "Look & feel"},
		{"fonts", "assets", "Fonts & assets"},
		{"Emulators", "emulators", "Emulators & cores"},
		{"Homebrew", "x:homebrew", "Homebrew"},
		{"", "network", "Network & sharing"},
	}
	for _, c := range cases {
		e := pkgEntry{Pkg: Package{Name: "FTP", Description: "Starts an FTP server."}, Extra: pkgExtra{Category: c.category}}
		id, title, _ := pkgCategoryFor(e)
		if id != c.wantID || title != c.wantTitle {
			t.Errorf("category %q → %q/%q, want %q/%q", c.category, id, title, c.wantID, c.wantTitle)
		}
	}
}

func TestPkgCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2.0", "1.0", 1},
		{"1.10", "1.9", 1},
		{"v2.0", "2.0.0", 0},
		{"2024.9.13.1", "2024.9.13", 1},
		{"1.0", "1.0-beta", 1},
		{"1.0-beta", "1.0-alpha", 1},
		{"", "", 0},
		{"1.2.3", "1.2.4", -1},
	}
	for _, c := range cases {
		if got := pkgCompareVersions(c.a, c.b); got != c.want {
			t.Errorf("pkgCompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// One malformed entry no longer empties the store: a field of the wrong type
// is zeroed, an entry with no name or a duplicate is dropped, and the optional
// store metadata is read whatever shape it comes in.
func TestPkgParseCatalogIsLenient(t *testing.T) {
	raw := `[
		{"name": "Beta", "version": "1.0", "device": "trimui_smart_pro", "category": ["Themes"], "size": "1.5 MB", "changelog": ["Faster", "Smaller"], "featured": "yes"},
		{"name": "alpha", "version": 2, "size": 2048},
		{"name": ""},
		"not an object",
		{"name": "Beta", "version": "9.9"},
		{"name": "Gamma", "homepage": "https://example.com/gamma"}
	]`
	entries, err := pkgParseCatalog([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("%d entries, want alpha, Beta and Gamma", len(entries))
	}
	if entries[0].Pkg.Name != "alpha" || entries[1].Pkg.Name != "Beta" || entries[2].Pkg.Name != "Gamma" {
		t.Fatalf("entries not sorted by name: %q %q %q", entries[0].Pkg.Name, entries[1].Pkg.Name, entries[2].Pkg.Name)
	}
	beta := entries[1]
	if beta.Pkg.Version != "1.0" {
		t.Errorf("the first Beta should win over the duplicate, got version %q", beta.Pkg.Version)
	}
	if beta.Pkg.Device != nil {
		t.Errorf("a string where an array belongs should be zeroed, got %v", beta.Pkg.Device)
	}
	if beta.Extra.Category != "Themes" || beta.Extra.Size != 1572864 || !beta.Extra.Featured {
		t.Errorf("extras not read: %+v", beta.Extra)
	}
	if beta.Extra.Changelog != "• Faster\n• Smaller" {
		t.Errorf("changelog = %q", beta.Extra.Changelog)
	}
	if entries[0].Extra.Size != 2048 || entries[0].Pkg.Version != "" {
		t.Errorf("alpha: size %d version %q", entries[0].Extra.Size, entries[0].Pkg.Version)
	}
	if entries[2].Extra.Homepage != "https://example.com/gamma" {
		t.Errorf("homepage = %q", entries[2].Extra.Homepage)
	}
	if _, err := pkgParseCatalog([]byte(`{"oops": true}`)); err == nil {
		t.Error("a registry that is not a list must be an error")
	}
}

func TestPkgParseSize(t *testing.T) {
	cases := map[string]int64{
		"1234": 1234, "512 KB": 512 << 10, "1.5MB": 1572864, "2 GiB": 2 << 30,
		"": 0, "big": 0, "3 parsecs": 0, "-1": 0,
	}
	for in, want := range cases {
		if got := pkgParseSize(in); got != want {
			t.Errorf("pkgParseSize(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestPkgInitials(t *testing.T) {
	cases := map[string]string{
		"Backup Config Files": "BC", "EmuDrop": "ED", "FTP": "FTP", "UDISK": "UD",
		"Resume at Boot": "RB", "Reboot": "R", "RainbowLEDs": "RL", "BGM Start/Stop": "BS",
		"": "?", "9 Lives": "9L",
	}
	for in, want := range cases {
		if got := pkgInitials(in); got != want {
			t.Errorf("pkgInitials(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPkgDeviceFit(t *testing.T) {
	cases := []struct {
		devs []string
		dev  DeviceModel
		want int
	}{
		{[]string{"trimui_smart_pro"}, DeviceTrimuiSmartPro, 1},
		{[]string{"TrimUI Brick"}, DeviceTrimuiBrick, 1},
		{[]string{"tg5040"}, DeviceTrimuiSmartPro, 1},
		{[]string{"all"}, DeviceDevBuild, 1},
		{[]string{"trimui_smart_pro"}, DeviceTrimuiBrick, -1},
		{nil, DeviceTrimuiSmartPro, 0},
	}
	for _, c := range cases {
		if got := pkgDeviceFit(c.devs, c.dev); got != c.want {
			t.Errorf("pkgDeviceFit(%v, %s) = %d, want %d", c.devs, c.dev, got, c.want)
		}
	}
	if got := pkgDeviceLabel("trimui_smart_pro"); got != "TrimUI Smart Pro" {
		t.Errorf("device label = %q", got)
	}
}

// A registry entry can never name a folder outside the packages folder.
func TestPackageDirNameCannotLeaveTheFolder(t *testing.T) {
	for _, name := range []string{"..", ".", "../etc", `a\b`, "a/b", ".hidden", "", "x:y*z"} {
		got := packageDirName(name)
		if got == "" || got == "." || got == ".." || strings.HasPrefix(got, ".") || strings.ContainsAny(got, `/\:*`) {
			t.Errorf("packageDirName(%q) = %q", name, got)
		}
		if filepath.Base(got) != got {
			t.Errorf("packageDirName(%q) = %q is not a single folder name", name, got)
		}
	}
	if got := packageDirName("Backup Config Files"); got != "Backup_Config_Files" {
		t.Errorf("an ordinary name changed folder: %q", got)
	}
	if err := pkgRemoveDir("../x"); err == nil {
		t.Error("pkgRemoveDir accepted a path")
	}
}

// The rows view's shelves: Updates first, then what is on the card, then the
// newest, then a shelf per category; the chips count the same packages.
func TestPkgBuildModelShelvesAndChips(t *testing.T) {
	entries := pkgTestEntries()
	m := pkgBuildModel(pkgModelInput{
		Entries:   entries,
		Installed: map[string]bool{"FTP": true, "Reboot": true, "Old_Tool": true},
		Meta: map[string]pkgInstalledMeta{
			"FTP":      {Name: "FTP", Version: "0.9"},
			"Reboot":   {Name: "Reboot", Version: "1.0"},
			"Old_Tool": {Name: "Old Tool", Version: "0.1"},
		},
		Device:    DeviceTrimuiSmartPro,
		AppUpdate: "2026-11-07",
	})
	ids := make([]string, 0, len(m.Shelves))
	for _, s := range m.Shelves {
		ids = append(ids, s.ID)
	}
	want := []string{"updates", "installed", "new", "cat:games", "cat:look", "cat:network", "cat:system"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("shelves = %v, want %v", ids, want)
	}
	updates := m.Shelves[0].Items
	if len(updates) != 2 || updates[0].Kind != pkgKindApp || updates[1].Pkg.Name != "FTP" {
		t.Fatalf("updates shelf = %d items (first kind %d): want the JukaHub release, then FTP", len(updates), updates[0].Kind)
	}
	if m.ByKey["Reboot"].HasUpdate {
		t.Error("a package at the registry's version claims an update")
	}
	local := m.ByKey["Old_Tool"]
	if local == nil || local.Kind != pkgKindLocal || local.Pkg.Name != "Old Tool" {
		t.Fatalf("an installed folder the registry does not list should stay as a local item: %+v", local)
	}
	newest := m.Shelves[2].Items
	if newest[0].Pkg.Name != "EmuDrop" {
		t.Errorf("New & updated starts with %q, want the newest", newest[0].Pkg.Name)
	}
	if m.Updates != 2 || m.Installed != 3 || m.Available != 5 {
		t.Errorf("counts: updates %d installed %d available %d", m.Updates, m.Installed, m.Available)
	}
	chips := map[string]int{}
	for _, c := range m.Chips {
		chips[c.ID] = c.Count
	}
	// All: five registry packages, the local folder and the JukaHub release.
	if chips["all"] != 7 || chips["installed"] != 3 || chips["updates"] != 2 || chips["cat:games"] != 2 {
		t.Errorf("chips = %v", chips)
	}
	if got := len(pkgFilterSort(m, "all", pkgSortName)); got != chips["all"] {
		t.Errorf("the All chip counts %d but lists %d", chips["all"], got)
	}
	if len(m.Featured) == 0 || m.Featured[0].Pkg.Name != "EmuDrop" {
		t.Errorf("the hero should feature the newest package")
	}
}

func TestPkgFilterSortModes(t *testing.T) {
	entries := pkgTestEntries()
	entries[0].Extra.Size = 5 << 20
	entries[2].Extra.Size = 1 << 20
	m := pkgBuildModel(pkgModelInput{Entries: entries, Installed: map[string]bool{"Reboot": true}})
	names := func(items []*pkgItem) string {
		out := make([]string, 0, len(items))
		for _, it := range items {
			out = append(out, it.Pkg.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names(pkgFilterSort(m, "all", pkgSortName)); got != "BGM Swap,EmuDrop,FTP,Random Game,Reboot" {
		t.Errorf("by name: %s", got)
	}
	if got := names(pkgFilterSort(m, "all", pkgSortUpdated)); !strings.HasPrefix(got, "EmuDrop,BGM Swap") {
		t.Errorf("by date: %s", got)
	}
	if got := names(pkgFilterSort(m, "all", pkgSortSize)); !strings.HasPrefix(got, "EmuDrop,FTP") {
		t.Errorf("by size: %s", got)
	}
	if got := names(pkgFilterSort(m, "all", pkgSortInstalled)); !strings.HasPrefix(got, "Reboot,") {
		t.Errorf("installed first: %s", got)
	}
	if got := names(pkgFilterSort(m, "cat:games", pkgSortName)); got != "EmuDrop,Random Game" {
		t.Errorf("games chip: %s", got)
	}
	if got := names(pkgFilterSort(m, "installed", pkgSortName)); got != "Reboot" {
		t.Errorf("installed chip: %s", got)
	}
	for _, mode := range []pkgSortMode{pkgSortName, pkgSortUpdated, pkgSortSize, pkgSortInstalled} {
		if pkgSortFromKey(pkgSortKey(mode)) != mode {
			t.Errorf("sort %d does not round-trip through the config", mode)
		}
	}
	for v := pkgViewRows; v < pkgViewModes; v++ {
		if pkgViewFromKey(pkgViewKey(v)) != v {
			t.Errorf("view %d does not round-trip through the config", v)
		}
	}
}

// Registry artwork: only HTTPS pictures are fetched, the cache name keeps an
// image extension, and a card shows a downloaded picture only until the
// package's own folder icon is there to replace it.
func TestPkgArtwork(t *testing.T) {
	pkgTestState(t)
	for in, want := range map[string]string{
		"https://example.com/a.png": "https://example.com/a.png",
		"http://example.com/a.png":  "",
		"file:///etc/passwd":        "",
		"":                          "",
	} {
		if got := pkgArtURL(pkgEntry{Extra: pkgExtra{Icon: in}}); got != want {
			t.Errorf("pkgArtURL(%q) = %q, want %q", in, got, want)
		}
	}
	png := pkgArtPath("https://example.com/icons/a.PNG?size=2")
	if filepath.Ext(png) != ".png" || filepath.Dir(png) != filepath.Join(packageInstallDir(), pkgArtDirName) {
		t.Errorf("artwork cache path = %q", png)
	}
	if ext := filepath.Ext(pkgArtPath("https://example.com/icon")); ext != ".png" {
		t.Errorf("a URL with no image extension cached as %q", ext)
	}
	if pkgArtPath("https://example.com/a.png") == pkgArtPath("https://example.com/b.png") {
		t.Error("two pictures share a cache file")
	}
	wanted := pkgArtWanted([]pkgEntry{
		{Pkg: Package{Name: "Has Art"}, Extra: pkgExtra{Icon: "https://example.com/a.png"}},
		{Pkg: Package{Name: "No Art"}},
	})
	if len(wanted) != 1 || wanted["Has_Art"] == "" {
		t.Fatalf("wanted = %v", wanted)
	}
	m := pkgBuildModel(pkgModelInput{
		Entries:   []pkgEntry{{Pkg: Package{Name: "Has Art"}}, {Pkg: Package{Name: "Mine"}}},
		Installed: map[string]bool{"Mine": true},
		Icons:     map[string]string{"Mine": "/card/Mine/icon.png"},
		Art:       map[string]string{"Has_Art": "/cache/a.png", "Mine": "/cache/m.png"},
	})
	if m.ByKey["Has_Art"].Icon != "/cache/a.png" {
		t.Errorf("a registry picture is not used: %q", m.ByKey["Has_Art"].Icon)
	}
	if m.ByKey["Mine"].Icon != "/card/Mine/icon.png" {
		t.Errorf("the folder's own icon should win: %q", m.ByKey["Mine"].Icon)
	}
}

// ── Files next to the packages ───────────────────────────────────────────

func TestPkgCatalogCacheRoundTrip(t *testing.T) {
	pkgTestState(t)
	raw := []byte(`[{"name":"FTP","version":"1.0"},{"name":"Reboot","version":"2.0"}]`)
	at := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	if err := pkgWriteCatalogCache(raw, at); err != nil {
		t.Fatalf("write: %v", err)
	}
	entries, got, err := pkgReadCatalogCache()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != 2 || !got.Equal(at) {
		t.Fatalf("cache gave %d entries fetched at %v", len(entries), got)
	}
	if err := pkgWriteCatalogCache([]byte("not json"), at); err == nil {
		t.Error("a response that is not JSON was cached")
	}
	// The cache is a file, never a folder: the installed scan must not count it.
	if set := pkgScanInstalled(); len(set) != 0 {
		t.Errorf("the scan counted the store's own files as packages: %v", set)
	}
}

// Recording the version a package was installed at is what makes "Update"
// possible when the registry moves on.
func TestPkgInstalledVersionMakesAnUpdate(t *testing.T) {
	pkgTestState(t)
	pkgRecordInstalled(Package{Name: "FTP", Version: "1.0", Date: "2024-04-04"})
	meta := pkgLoadInstalledMeta()
	if meta["FTP"].Version != "1.0" || meta["FTP"].InstalledAt == "" {
		t.Fatalf("record = %+v", meta["FTP"])
	}
	m := pkgBuildModel(pkgModelInput{
		Entries:   []pkgEntry{pkgEntryFor("FTP", "1.1", "2024-05-01", "Starts an FTP server.")},
		Installed: map[string]bool{"FTP": true},
		Meta:      meta,
	})
	if !m.ByKey["FTP"].HasUpdate {
		t.Fatal("a newer registry version is not offered as an update")
	}
	pkgForgetInstalled("FTP")
	if _, ok := pkgLoadInstalledMeta()["FTP"]; ok {
		t.Fatal("a removed package stayed in the record")
	}
}

// The list older builds kept inside jukauser.json is shown at once and moved
// out of the user config.
func TestPkgLegacyListIsCarriedOver(t *testing.T) {
	pkgTestState(t)
	var legacy interface{}
	if err := json.Unmarshal([]byte(`[{"name":"FTP","version":"1.0"},{"name":"Reboot","version":"1.0"}]`), &legacy); err != nil {
		t.Fatal(err)
	}
	config := &Config{Variables: Variables{Custom: map[string]interface{}{
		"packages_list":  legacy,
		"packages_error": nil,
	}}}
	pkgMigrateLegacy(config)
	if _, ok := config.Variables.Custom["packages_list"]; ok {
		t.Error("the old list stayed in the user config")
	}
	if !pkgUI.haveCatalog || len(pkgUI.entries) != 2 {
		t.Fatalf("the carried-over list is not on screen: %d entries", len(pkgUI.entries))
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(pkgCatalogCachePath()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the carried-over list was not cached for offline visits")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Let the background write finish its directory sync before the temporary
	// folder is removed.
	time.Sleep(100 * time.Millisecond)
	if entries, _, err := pkgReadCatalogCache(); err != nil || len(entries) != 2 {
		t.Fatalf("cached list: %d entries, %v", len(entries), err)
	}
}

// ── Refresh and offline ──────────────────────────────────────────────────

func TestPkgFetchCachesAndUpdatesInPlace(t *testing.T) {
	pkgTestState(t)
	pkgSeed(t, []pkgEntry{pkgEntryFor("FTP", "1.0", "2024-04-04", "Starts an FTP server.")})
	pkgCatalogFetcher = func(context.Context) (string, error) {
		return `[{"name":"FTP","version":"1.0"},{"name":"Telnetd","version":"1.0"}]`, nil
	}
	pkgUI.loading = true
	gen := pkgUI.fetchGen
	pkgFetchTask(context.Background(), gen)
	pkgDrainEvents()
	if pkgUI.loading {
		t.Error("the fetch finished but the screen still says it is loading")
	}
	if pkgUI.dirty {
		pkgRebuild()
	}
	if pkgUI.model.ByKey["Telnetd"] == nil {
		t.Fatal("the fetched list did not replace the one on screen")
	}
	if !pkgUI.fromNetwork || pkgUI.lastOK.IsZero() {
		t.Error("a fetched list is not marked as fresh")
	}
	if _, err := os.Stat(pkgCatalogCachePath()); err != nil {
		t.Errorf("the fetched list was not cached: %v", err)
	}
}

// A failed fetch says "connect Wi-Fi", never a resolver error.
func TestPkgFetchFailureIsPlain(t *testing.T) {
	pkgTestState(t)
	pkgCatalogFetcher = func(context.Context) (string, error) {
		return "", errors.New("Get \"https://raw.githubusercontent.com\": dial tcp: lookup raw.githubusercontent.com: no such host")
	}
	pkgUI.loading = true
	pkgFetchTask(context.Background(), pkgUI.fetchGen)
	pkgDrainEvents()
	if pkgUI.fetchErr != offlinePlainNotice {
		t.Fatalf("fetch error shown as %q", pkgUI.fetchErr)
	}
}

// Offline the registry is not asked at all, so nothing waits for a timeout.
func TestPkgOfflineSkipsTheFetch(t *testing.T) {
	pkgTestState(t)
	forceOfflineForTest(t)
	called := false
	pkgCatalogFetcher = func(context.Context) (string, error) {
		called = true
		return "", nil
	}
	pkgMaybeRefresh(&Config{}, false)
	pkgMaybeRefresh(&Config{}, true)
	if pkgUI.loading || called {
		t.Fatal("an offline store started a registry fetch")
	}
}

// Offline, installed packages stay manageable; getting a new one is dimmed.
func TestPkgSheetButtonsOffline(t *testing.T) {
	pkgTestState(t)
	forceOfflineForTest(t)
	pkgUI.online = false
	pkgSeed(t, pkgTestEntries(), "FTP")
	byID := func(it *pkgItem) map[string]pkgSheetBtn {
		out := map[string]pkgSheetBtn{}
		for _, b := range pkgSheetButtons(it, pkgLive{}) {
			out[b.ID] = b
		}
		return out
	}
	get := byID(pkgUI.model.ByKey["Reboot"])
	if !get["get"].Disabled {
		t.Error("Get is not dimmed offline")
	}
	installed := byID(pkgUI.model.ByKey["FTP"])
	if installed["open"].Disabled || installed["remove"].Disabled {
		t.Error("an installed package cannot be opened or removed offline")
	}
	if !installed["reinstall"].Disabled {
		t.Error("Reinstall needs the internet but is not dimmed")
	}
	if !pkgSheetNeedsNet(pkgSheetButtons(pkgUI.model.ByKey["Reboot"], pkgLive{})) {
		t.Error("the sheet does not say why Get is dimmed")
	}
}

// ── Input ────────────────────────────────────────────────────────────────

func pkgTestKey(config *Config, sym sdl.Keycode) {
	handlePackageInput(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym}}, config)
}

func pkgTestPad(config *Config, button uint8) {
	packageListController(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: button}, config)
}

// The rows view walks the hero and the shelves; A opens the details sheet on
// the focused card and B closes the sheet before anything else.
func TestPkgRowsNavigationOpensTheSheet(t *testing.T) {
	pkgTestState(t)
	pkgSeed(t, pkgTestEntries(), "FTP")
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	if !pkgHasHero(pkgUI.model) || pkgUI.rowFocus != 0 {
		t.Fatalf("the rows view should open on the hero (focus %d)", pkgUI.rowFocus)
	}
	pkgTestKey(config, sdl.K_DOWN)
	shelf, _ := pkgShelfAtRow(pkgUI.model, pkgUI.rowFocus)
	if shelf == nil || shelf.ID != "installed" {
		t.Fatalf("Down from the hero landed on %+v, want the On this device shelf", shelf)
	}
	pkgTestKey(config, sdl.K_DOWN)
	shelf, _ = pkgShelfAtRow(pkgUI.model, pkgUI.rowFocus)
	if shelf == nil || shelf.ID != "new" {
		t.Fatalf("second Down landed on %+v", shelf)
	}
	pkgTestKey(config, sdl.K_RIGHT)
	if pkgUI.rowCols["new"] != 1 {
		t.Fatalf("Right did not move along the shelf: col %d", pkgUI.rowCols["new"])
	}
	want := shelf.Items[1].Key
	pkgTestKey(config, sdl.K_RETURN)
	if !pkgUI.sheet.open || pkgUI.sheet.key != want {
		t.Fatalf("Enter opened %q (open %v), want %q", pkgUI.sheet.key, pkgUI.sheet.open, want)
	}
	// L1/R1 step through the shelf without closing the sheet.
	pkgTestPad(config, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER)
	if !pkgUI.sheet.open || pkgUI.sheet.key != shelf.Items[2].Key || pkgUI.rowCols["new"] != 2 {
		t.Fatalf("R1 in the sheet: key %q col %d", pkgUI.sheet.key, pkgUI.rowCols["new"])
	}
	pkgTestKey(config, sdl.K_ESCAPE)
	if pkgUI.sheet.open {
		t.Fatal("Escape did not close the sheet first")
	}
	pkgTestPad(config, sdl.CONTROLLER_BUTTON_A)
	if !pkgUI.sheet.open {
		t.Fatal("pad A did not open the sheet")
	}
	pkgTestPad(config, sdl.CONTROLLER_BUTTON_B)
	if pkgUI.sheet.open {
		t.Fatal("pad B did not close the sheet")
	}
}

// Y on the pad, V on a keyboard and the number keys switch the view, and the
// choice is remembered in the user config.
func TestPkgViewSwitchIsRemembered(t *testing.T) {
	pkgTestState(t)
	pkgSeed(t, pkgTestEntries())
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	pkgTestPad(config, sdl.CONTROLLER_BUTTON_Y)
	if pkgUI.view != pkgViewGrid || config.Variables.Custom["pkg_view"] != "grid" {
		t.Fatalf("Y: view %d, remembered %v", pkgUI.view, config.Variables.Custom["pkg_view"])
	}
	pkgTestKey(config, sdl.K_v)
	if pkgUI.view != pkgViewList || config.Variables.Custom["pkg_view"] != "list" {
		t.Fatalf("V: view %d, remembered %v", pkgUI.view, config.Variables.Custom["pkg_view"])
	}
	pkgTestKey(config, sdl.K_1)
	if pkgUI.view != pkgViewRows {
		t.Fatalf("1 did not pick the rows view")
	}
	// A fresh visit reads it back.
	pkgUI.prefsLoaded = false
	config.Variables.Custom["pkg_view"] = "grid"
	config.Variables.Custom["pkg_sort"] = "size"
	pkgLoadPrefs(config)
	if pkgUI.view != pkgViewGrid || pkgUI.sortMode != pkgSortSize {
		t.Fatalf("prefs not restored: view %d sort %d", pkgUI.view, pkgUI.sortMode)
	}
}

// In the grid, Up from the first row reaches the filter chips; A on a chip
// filters, and the sort chip cycles the order.
func TestPkgGridChipsFilterAndSort(t *testing.T) {
	pkgTestState(t)
	pkgSeed(t, pkgTestEntries())
	pkgUI.view = pkgViewGrid
	pkgUI.gridCols = 3
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	pkgTestKey(config, sdl.K_RIGHT)
	if pkgUI.gridIndex != 1 {
		t.Fatalf("Right in the grid: index %d", pkgUI.gridIndex)
	}
	pkgTestKey(config, sdl.K_DOWN)
	if pkgUI.gridIndex != 4 {
		t.Fatalf("Down in a 3-column grid: index %d, want 4", pkgUI.gridIndex)
	}
	pkgTestKey(config, sdl.K_UP)
	pkgTestKey(config, sdl.K_UP)
	if !pkgUI.chipFocus {
		t.Fatal("Up from the first row did not reach the chips")
	}
	gamesChip := -1
	for i, c := range pkgUI.model.Chips {
		if c.ID == "cat:games" {
			gamesChip = i
		}
	}
	for pkgUI.chipCursor < gamesChip {
		pkgTestKey(config, sdl.K_RIGHT)
	}
	pkgTestKey(config, sdl.K_RETURN)
	pkgEnsureFiltered()
	if pkgUI.chip != "cat:games" || len(pkgUI.filtered) != 2 {
		t.Fatalf("the games chip filtered to %d packages (chip %q)", len(pkgUI.filtered), pkgUI.chip)
	}
	pkgTestKey(config, sdl.K_s)
	if pkgUI.sortMode != pkgSortUpdated || config.Variables.Custom["pkg_sort"] != "updated" {
		t.Fatalf("S did not step the sort: %d", pkgUI.sortMode)
	}
	pkgTestKey(config, sdl.K_DOWN)
	if pkgUI.chipFocus {
		t.Fatal("Down from the chips did not return to the grid")
	}
}

// Remove asks twice: the first press arms it, the second removes the folder.
func TestPkgRemoveAsksTwice(t *testing.T) {
	pkgTestState(t)
	dir := filepath.Join(packageInstallDir(), "FTP")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	pkgSeed(t, pkgTestEntries(), "FTP")
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	pkgOpenSheet(pkgUI.model.ByKey["FTP"])
	btns := pkgSheetButtons(pkgUI.model.ByKey["FTP"], pkgLive{})
	removeAt := -1
	for i, b := range btns {
		if b.ID == "remove" {
			removeAt = i
		}
	}
	if removeAt < 0 {
		t.Fatal("an installed package has no Remove")
	}
	pkgUI.sheet.btn = removeAt
	pkgTestKey(config, sdl.K_RETURN)
	if _, err := os.Stat(dir); err != nil || pkgUI.sheet.armed != "remove" {
		t.Fatalf("the first press should only arm Remove (armed %q, stat %v)", pkgUI.sheet.armed, err)
	}
	pkgTestKey(config, sdl.K_RETURN)
	deadline := time.Now().Add(3 * time.Second)
	for pkgUI.removing["FTP"] {
		pkgDrainEvents()
		if time.Now().After(deadline) {
			t.Fatal("the removal never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the folder is still there: %v", err)
	}
	if pkgUI.installed["FTP"] {
		t.Fatal("the store still lists the package as installed")
	}
}

// Installs run one at a time: the install card shows one run, and a second
// run used to cancel the first one's download.
func TestPkgInstallQueueRunsOneAtATime(t *testing.T) {
	pkgTestState(t)
	packageInstallHook = nil
	var mu sync.Mutex
	order := []string{}
	release := make(chan struct{})
	started := make(chan string, 4)
	pkgInstallRunner = func(_ *Config, p Package) bool {
		started <- p.Name
		<-release
		mu.Lock()
		order = append(order, p.Name)
		mu.Unlock()
		return true
	}
	pkgEnqueueInstall(nil, Package{Name: "A"})
	pkgEnqueueInstall(nil, Package{Name: "B"})
	pkgEnqueueInstall(nil, Package{Name: "A"})
	if got := <-started; got != "A" {
		t.Fatalf("first install = %q", got)
	}
	if queued, _ := pkgQueueState("B"); !queued {
		t.Fatal("B is not waiting while A installs")
	}
	release <- struct{}{}
	if got := <-started; got != "B" {
		t.Fatalf("second install = %q", got)
	}
	release <- struct{}{}
	deadline := time.Now().Add(3 * time.Second)
	for {
		pkgQueue.mu.Lock()
		running := pkgQueue.running
		pkgQueue.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the queue never drained")
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(order, ",") != "A,B" {
		t.Fatalf("installs ran as %v, want A then B once each", order)
	}
}

// The footer names the keys of whatever holds the pad.
func TestPkgFooterHints(t *testing.T) {
	pkgTestState(t)
	pkgSeed(t, pkgTestEntries())
	labels := func() string {
		parts := []string{}
		for _, h := range pkgFooterHints() {
			parts = append(parts, h.Button+" "+h.Label)
		}
		return strings.Join(parts, " | ")
	}
	browse := labels()
	for _, want := range []string{"A Details", "B Back", "X Refresh", "Y View"} {
		if !strings.Contains(browse, want) {
			t.Errorf("browsing footer %q lacks %q", browse, want)
		}
	}
	pkgOpenSheet(pkgUI.model.ByKey["FTP"])
	if sheet := labels(); !strings.Contains(sheet, "B Close") || !strings.Contains(sheet, "A Get") {
		t.Errorf("sheet footer = %q", sheet)
	}
}

func TestPkgWheelScrollsUnderThePointer(t *testing.T) {
	pkgTestState(t)
	pkgSeed(t, pkgTestEntries())
	elem := Element{Type: "packagelist", X: 24, Y: 72, Width: "1232", Height: "576"}
	if pkgHandleWheel(nil, elem, 5, 5, 0, -1) {
		t.Fatal("a wheel outside the store was taken")
	}
	if !pkgHandleWheel(nil, elem, 400, 400, 0, -1) || pkgUI.pageScroll.target <= 0 {
		t.Fatalf("the wheel did not scroll the page (target %.0f)", pkgUI.pageScroll.target)
	}
	pkgUI.view = pkgViewList
	pkgHandleWheel(nil, elem, 400, 400, 0, -2)
	if pkgUI.listScroll.target <= 0 {
		t.Fatal("the wheel did not scroll the list")
	}
}

// pkgDownload refuses an error page instead of saving it as the package, and
// reports byte progress for a real one.
func TestPkgDownloadRefusesErrorPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing.zip" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("PK\x03\x04payload"))
	}))
	defer srv.Close()
	if _, err := pkgDownload(context.Background(), srv.URL+"/missing.zip"); err == nil {
		t.Fatal("a 404 page was accepted as the package")
	}
	data, err := pkgDownload(context.Background(), srv.URL+"/ok.zip")
	if err != nil || string(data) != "PK\x03\x04payload" {
		t.Fatalf("download = %q, %v", data, err)
	}
}

func TestPackageRepositoryLabels(t *testing.T) {
	for _, tc := range []struct {
		raw, label, target string
		ok                 bool
	}{
		{"https://github.com/org/repo/releases/download/v1/pkg.zip", "org/repo", "https://github.com/org/repo", true},
		{"https://raw.githubusercontent.com/org/repo/main/pkg.zip", "org/repo", "https://github.com/org/repo", true},
		{"https://api.github.com/repos/org/repo/releases/latest", "org/repo", "https://github.com/org/repo", true},
		{"https://example.com/files/pkg.zip", "example.com/files/pkg.zip", "https://example.com/files/pkg.zip", true},
		{"https://github.com@evil.example/x", "", "", false},
		{"javascript:alert(1)", "", "", false},
	} {
		label, target, ok := packageRepository(tc.raw)
		if label != tc.label || target != tc.target || ok != tc.ok {
			t.Errorf("%q = %q, %q, %v", tc.raw, label, target, ok)
		}
	}
}

// ── Rendering ────────────────────────────────────────────────────────────

// pkgWithScreen sets the logical canvas for one test.
func pkgWithScreen(t *testing.T, w, h int32) {
	t.Helper()
	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = w, h
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })
}

// pkgRenderProbe draws the store into an offscreen frame.
func pkgRenderProbe(t *testing.T, config *Config) (*probeFrame, Element) {
	t.Helper()
	pkgWithScreen(t, probeW, probeH)
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	// Destroyed as soon as the pixels are read: the text cache is shared, so a
	// second probe drawing while this renderer lived would be handed its
	// textures.
	defer destroyTestRenderer(renderer)
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	elem := Element{Type: "packagelist", X: 24, Y: 72, Width: "1232", Height: "576"}
	renderPackageList(renderer, config, elem)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels: %v", err)
	}
	return f, elem
}

// pkgRingPixels counts pixels in the focus colour inside rect.
func pkgRingPixels(f *probeFrame, rect sdl.Rect) int {
	ring := glassAccent()
	return countPixelsMatching(f, rect, func(c sdl.Color) bool {
		return c.R == ring.R && c.G == ring.G && c.B == ring.B
	})
}

// Every view draws, the focused card wears the accent ring, and the cards
// leave click targets behind for the mouse.
func TestPkgRendersEveryView(t *testing.T) {
	pkgTestState(t)
	pkgSeed(t, pkgTestEntries(), "FTP")
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	for _, view := range []pkgViewMode{pkgViewRows, pkgViewGrid, pkgViewList} {
		pkgUI.view = view
		pkgUI.rowFocus = 1
		pkgUI.gridIndex, pkgUI.listIndex = 0, 0
		pkgUI.reveal = true
		f, _ := pkgRenderProbe(t, config)
		var focused, other pkgHit
		cards := 0
		for _, h := range pkgUI.hits {
			if h.Kind != pkgHitCard {
				continue
			}
			cards++
			isFocused := h.Index == 0 && (view != pkgViewRows || h.Row == 1)
			if isFocused {
				focused = h
			} else if other.Kind != pkgHitCard && h.Rect.W > 100 && h.Rect.H > 40 {
				other = h
			}
		}
		if cards == 0 || focused.Kind != pkgHitCard || other.Kind != pkgHitCard {
			t.Fatalf("%s view recorded %d card targets (focused found %v)", pkgViewKey(view), cards, focused.Kind == pkgHitCard)
		}
		// The ring sits on the card's own edge (a lifted card grows a few
		// pixels past it), so the count looks just around the card.
		around := func(r sdl.Rect) sdl.Rect { return sdl.Rect{X: r.X - 8, Y: r.Y - 8, W: r.W + 16, H: r.H + 16} }
		if n := pkgRingPixels(f, around(focused.Rect)); n < 200 {
			t.Errorf("%s view: %d accent pixels around the focused card, want its ring", pkgViewKey(view), n)
		}
		if n := pkgRingPixels(f, sdl.Rect{X: other.Rect.X + 6, Y: other.Rect.Y + 6, W: other.Rect.W - 12, H: other.Rect.H - 12}); n > 40 {
			t.Errorf("%s view: an unfocused card carries %d accent pixels", pkgViewKey(view), n)
		}
	}
}

// Offline the banner is drawn and says why new packages cannot be fetched.
func TestPkgOfflineBannerRenders(t *testing.T) {
	pkgTestState(t)
	forceOfflineForTest(t)
	pkgUI.online = false
	pkgSeed(t, pkgTestEntries(), "FTP")
	f, elem := pkgRenderProbe(t, &Config{})
	banner := sdl.Rect{X: elem.X + pkgPad, Y: elem.Y + pkgToolbarH + 8, W: 1232 - 2*pkgPad, H: pkgBannerH}
	warn := ColorWarning
	tinted := countPixelsMatching(f, banner, func(c sdl.Color) bool {
		return c.R > c.B+20 && c.G > c.B
	})
	if tinted < 200 {
		t.Fatalf("no amber offline banner (%d tinted pixels, warning colour %v)", tinted, warn)
	}
}

// A click on a card opens it on release; a press that turns into a drag
// scrolls the shelf instead and opens nothing.
func TestPkgPointerClickAndDrag(t *testing.T) {
	pkgTestState(t)
	entries := pkgTestEntries()
	for i := 0; i < 8; i++ {
		entries = append(entries, pkgEntryFor("Extra "+string(rune('A'+i)), "1.0", "2024-0"+string(rune('1'+i%9))+"-01", "Starts an FTP server."))
	}
	pkgSeed(t, entries)
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	pkgRenderProbe(t, config)
	var card pkgHit
	for _, h := range pkgUI.hits {
		if h.Kind == pkgHitCard && h.Row > 0 && h.Rect.W > 100 {
			card = h
			break
		}
	}
	if card.Kind != pkgHitCard {
		t.Fatal("no card target recorded")
	}
	cx, cy := card.Rect.X+card.Rect.W/2, card.Rect.Y+card.Rect.H/2
	handlePackageMouseClick(cx, cy, config)
	if pkgUI.sheet.open {
		t.Fatal("the card opened on the press, before the release")
	}
	pkgPointerUp(config, cx, cy)
	if !pkgUI.sheet.open || pkgUI.sheet.key != card.Key {
		t.Fatalf("release on the card opened %q (open %v), want %q", pkgUI.sheet.key, pkgUI.sheet.open, card.Key)
	}
	pkgCloseSheet(false)

	shelf, _ := pkgShelfAtRow(pkgUI.model, card.Row)
	before := pkgShelfScroll(shelf.ID).cur
	handlePackageMouseClick(cx, cy, config)
	pkgPointerMotion(config, cx-30, cy, true)
	pkgPointerMotion(config, cx-90, cy, true)
	pkgPointerUp(config, cx-90, cy)
	if pkgUI.sheet.open {
		t.Fatal("a swipe opened the card it started on")
	}
	if after := pkgShelfScroll(shelf.ID).target; after <= before {
		t.Fatalf("the swipe did not scroll the shelf (%.0f → %.0f)", before, after)
	}
}

// The details sheet renders with its buttons, and a click on a button runs it.
func TestPkgSheetRendersAndClicks(t *testing.T) {
	pkgTestState(t)
	pkgSeed(t, pkgTestEntries())
	var installs []string
	packageInstallHook = func(_ *Config, p Package) { installs = append(installs, p.Name) }
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	pkgOpenSheet(pkgUI.model.ByKey["FTP"])
	pkgRenderProbe(t, config)
	var get pkgHit
	for _, h := range pkgUI.hits {
		if h.Kind == pkgHitSheetButton && h.Index == 0 {
			get = h
		}
	}
	if get.Kind != pkgHitSheetButton {
		t.Fatal("the sheet drew no buttons")
	}
	handlePackageMouseClick(get.Rect.X+get.Rect.W/2, get.Rect.Y+get.Rect.H/2, config)
	if len(installs) != 1 || installs[0] != "FTP" {
		t.Fatalf("clicking Get started %v, want FTP", installs)
	}
	if len(pkgUI.sheet.lines) == 0 || pkgUI.sheet.lines[0].Text != "ABOUT" {
		t.Fatalf("the sheet text was not laid out: %+v", pkgUI.sheet.lines)
	}
}

// The scene change takes the sheet down before the fade (core.go calls the
// departure on every switch).
func TestPkgDepartureForgetsTheSheet(t *testing.T) {
	pkgTestState(t)
	pkgSeed(t, pkgTestEntries())
	pkgOpenSheet(pkgUI.model.ByKey["FTP"])
	packageActionsForget()
	if pkgUI.sheet.open || pkgUI.entered {
		t.Fatal("the departure left the sheet up or the visit open")
	}
}
