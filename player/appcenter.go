package main

// appcenter.go — the App Center: a short, honest catalogue of TrimUI apps.
//
// The handheld's firmwares run apps from the card's Apps folder: one folder
// per app with a config.json (label, launch script, icon) and a launch.sh
// (apps.go scanAppSubdir). People find them on GitHub, in CrossMix and in
// forum threads, and nothing on the device says which work on the stock
// firmware, which need CrossMix, what they depend on or whether they can be
// handed a file. The App Center is that list, embedded in the program (no
// remote catalogue that could change under a user), with rules about what it
// may claim:
//
//   - every entry names where it comes from: a GitHub repository and the
//     release asset the installer picks, or "comes with <firmware>" when there
//     is no download of its own. An entry whose download could not be traced
//     to a published release has none: the page says where to get it and
//     never guesses a file name;
//   - Status is "Checked" only when the app's own source documents what the
//     entry says it does (how it starts, what it opens, where it writes);
//     everything else is "Untested with JukaHub", and the per-firmware lines
//     say "Untested" rather than "Works" when nobody has reported either way;
//   - dependencies are checked live where a check is possible (a file on the
//     card, Bluetooth, Wi-Fi); one that lives elsewhere (a receiver program on
//     a PC) says that it cannot be checked;
//   - what an app opens is only listed when the source says it opens files,
//     and it is "verified" for Open with only when the source also says how
//     the file is passed AND the installed launch script passes it on (read
//     from the script itself, acScriptForwardsArgs), never assumed.
//
// The files: this one holds the table, the firmware detection, the live
// dependency checks and the cached scan of the Apps folder.
// appcenter_install.go installs, updates, rolls back and removes (the
// Packages store's staging and swap, pkgstage.go); appcenter_launch.go starts
// an app and puts back what it changed; appcenter_ui.go is the page;
// appcenter_open.go is what Files' "Open with" (openwith*.go) calls, and the
// places (Recordings) that offer an app where it is useful.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The honest statuses an entry may carry (see the file comment).
const (
	acStatusChecked  = "Checked"
	acStatusUntested = "Untested with JukaHub"
)

// acSupport is what one firmware does with an app.
type acSupport int

const (
	acUntested acSupport = iota
	acWorks
	acNeeds
	acNotSupported
)

// acSupportLabel is the word the detail sheet shows for a support state.
func acSupportLabel(s acSupport) string {
	switch s {
	case acWorks:
		return "Works"
	case acNeeds:
		return "Needs more"
	case acNotSupported:
		return "Not supported"
	}
	return "Untested"
}

// acFirmwareNote is one firmware's line of an entry: the state and why.
type acFirmwareNote struct {
	State acSupport
	Why   string
}

// The kinds of dependency an entry can name. Each has a live check except
// acDepRemote, which lives on another machine.
const (
	acDepFile      = "file"      // a file or folder on the device (Paths, any of them)
	acDepBluetooth = "bluetooth" // a Bluetooth controller the system sees
	acDepWiFi      = "wifi"      // a network connection
	acDepRemote    = "remote"    // something on a PC or a phone: cannot be checked
)

// acDep is one thing an app needs.
type acDep struct {
	Name  string
	Kind  string
	Paths []string
	Why   string
}

// acSource is where an app comes from. Owner/Repo with an Asset pattern is a
// download the installer can fetch; Bundled names the firmware that ships the
// app when it has no download of its own.
type acSource struct {
	Owner string
	Repo  string
	// TagPrefix limits the releases to those whose tag starts with it, for a
	// repository that publishes several apps as separate releases.
	TagPrefix string
	// Asset is a path.Match pattern for the release asset, compared in lower
	// case. "*.zip" takes the release's one zip; the archive must still hold an
	// app folder (acFindAppRoot) or nothing is installed.
	Asset   string
	Bundled string
	// URL is the page shown to the user (and as a QR code).
	URL string
}

// downloadable reports whether the installer can fetch the app.
func (s acSource) downloadable() bool {
	return s.Owner != "" && s.Repo != "" && s.Asset != ""
}

// acApp is one catalogue entry.
type acApp struct {
	ID   string
	Name string
	// Folders are the app's folder names under Apps (compared without case);
	// Labels its config.json labels (compared folded). Either identifies an
	// installed copy, whoever installed it.
	Folders []string
	Labels  []string
	Summary string
	About   string
	Source  acSource
	Licence string
	Stock   acFirmwareNote
	Cross   acFirmwareNote
	Deps    []acDep
	// Opens lists the lower-case extensions the source says the app opens,
	// and ArgHow how the source says a file reaches it ("$1": the first
	// argument of its launch script); "" when no source documents that, and
	// then Open with never calls it verified.
	Opens  []string
	ArgHow string
	// Places are where JukaHub offers the app besides this page: "books",
	// "recordings", "device".
	Places []string
	Status string
	// Checked is what the source documents, one line, for the sheet.
	Checked string
	// Background apps start a helper and return at once (Screen Recorder):
	// JukaHub keeps the screen, so the helper records JukaHub, and is not
	// parked behind them.
	Background bool
	// SelfUpdates apps can replace themselves from their own menu, so the
	// release tag the App Center wrote may be older than what runs: the page
	// says "Newer release <tag>", never "Update", and does not name a version.
	SelfUpdates bool
}

// acCatalogue is the table. Sources: docs/APP_CENTER.md lists every URL the
// entries were checked against.
var acCatalogue = []acApp{
	{
		ID:      "koreader",
		Name:    "KOReader",
		Folders: []string{"KOReader", "koreader"},
		Labels:  []string{"KOReader"},
		Summary: "E-book and document reader: EPUB, PDF, DjVu, CBZ, FB2, MOBI",
		About: "KOReader is the open-source document reader of Kindle and Kobo e-readers. CrossMix-OS CE (the community edition of CrossMix, " +
			"v1.4.0-ce.1) ships it as an app; it has no TrimUI download of its own, so the App Center does not install it.",
		Source: acSource{
			Bundled: "CrossMix-OS CE v1.4.0-ce.1",
			URL:     "https://github.com/SrScorpio/CrossMix-OS-SE/releases/tag/v1.4.0-ce.1",
		},
		Licence: "AGPL-3.0",
		Stock:   acFirmwareNote{acNeeds, "Not offered for the stock firmware: it comes inside CrossMix-OS CE, whose libraries it may use"},
		Cross:   acFirmwareNote{acNeeds, "CE edition only: CrossMix-OS CE v1.4.0-ce.1 ships it, the original CrossMix 1.3.0 does not"},
		Opens:   []string{".epub", ".pdf", ".djvu", ".cbz", ".cbt", ".fb2", ".mobi", ".txt", ".html", ".rtf", ".chm", ".doc"},
		ArgHow:  "$1",
		Places:  []string{"books"},
		Status:  acStatusUntested,
		Checked: "KOReader's reader.lua opens a document given as its argument; whether CrossMix CE's launch.sh passes one on is read from the installed script.",
	},
	{
		ID:      "ritder",
		Name:    "Ritder",
		Folders: []string{"Ritder"},
		Labels:  []string{"Ritder"},
		Summary: "KOReader-based reader for TrimUI: PDF, CBZ, CBR, EPUB, DjVu, FB2, MOBI",
		About: "Ritder packages KOReader's official arm64 build with its own C library and a TrimUI screen and button layer. It was made for the " +
			"TrimUI Brick Pro on the stock firmware; its README says the screen and button code follows the TG5040 firmware the Smart Pro shares, " +
			"so on a Smart Pro it is untested. Books go in the card's Books folder. It can update itself (MENU → Check for updates).",
		Source: acSource{
			Owner: "nomiz1734", Repo: "Ritder", Asset: "ritder-stock.zip",
			URL: "https://github.com/nomiz1734/Ritder",
		},
		Licence: "AGPL-3.0",
		Stock:   acFirmwareNote{acUntested, "Made for the Brick Pro's stock firmware; the Smart Pro shares its TG5040 base but nobody has reported it"},
		Cross:   acFirmwareNote{acUntested, "Not reported on CrossMix"},
		Deps: []acDep{
			{Name: "Books folder", Kind: acDepFile, Paths: []string{"/mnt/SDCARD/Books"}, Why: "Ritder lists the card's Books folder (it creates it on first run)"},
		},
		// Its sources do not say that launch.sh takes a file: no ArgHow, so
		// Open with never marks it verified.
		Opens:       []string{".pdf", ".cbz", ".cbr", ".epub", ".djvu", ".fb2", ".mobi"},
		Places:      []string{"books"},
		Status:      acStatusUntested,
		Checked:     "Its release notes document the formats, the Books folder and the Apps/Ritder install; passing a file to launch.sh is not documented.",
		SelfUpdates: true,
	},
	{
		ID:   "screenrecorder",
		Name: "Screen Recorder",
		// ScreencapTK is the folder CrossMix keeps the recorder's ffmpeg in
		// (screenrec.go looks there too).
		Folders: []string{"ScreenRecorder", "ScreencapTK"},
		Labels:  []string{"Screen Recorder", "ScreenRecorder"},
		Summary: "Records the screen to a video; launch again or L1 + R1 to stop",
		About: "CrossMix's Screen Recorder: starting the app starts a recording, and L1 + R1 (or starting the app again) stops it. A CrossMix " +
			"install guide (not the app's own source) says the videos go to Apps/ScreenRecorder/output. JukaHub starts it without handing the " +
			"screen over, so what it records is JukaHub; on the Smart Pro S, " +
			"whose display is not a plain framebuffer, nobody has checked what it captures. JukaHub's own Record screen (Quick Settings, the " +
			"in-game menu) does the same without the app, and the Recordings page lists both recorders' clips.",
		Source: acSource{
			Bundled: "CrossMix-OS",
			URL:     "https://github.com/cizia64/CrossMix-OS/wiki/apps",
		},
		Licence: "GPL-3.0 (CrossMix-OS)",
		Stock:   acFirmwareNote{acNeeds, "Part of CrossMix: its folder alone may lean on CrossMix's tools"},
		Cross:   acFirmwareNote{acWorks, "Ships with CrossMix-OS (Apps wiki: Screen Recorder)"},
		Deps: []acDep{
			{Name: "Output folder", Kind: acDepFile, Paths: []string{"/mnt/SDCARD/Apps/ScreenRecorder/output", "/mnt/SDCARD/Apps/ScreencapTK/output"},
				Why: "Where the recordings land, per a third-party guide (the app makes it)"},
		},
		Places: []string{"recordings"},
		// The wiki is CrossMix's own; the output folder is not from it, so the
		// entry as a whole is not "Checked".
		Status:     acStatusUntested,
		Checked:    "CrossMix's Apps wiki documents start-on-launch and the L1 + R1 stop; the output folder comes from a third-party CrossMix guide, not from the app's source.",
		Background: true,
	},
	{
		ID:      "xboxpadbt",
		Name:    "Xbox Pad BT",
		Folders: []string{"TrimuiXboxPad"},
		Labels:  []string{"Xbox Pad BT"},
		Summary: "Use the handheld as a Bluetooth gamepad for a phone or a PC",
		About: "AleWiccan's Bluetooth gamepad app (release GamepadBT1.4): turn Bluetooth on, start \"Xbox Pad BT\" and wait, then pair " +
			"\"Trimui Gamepad\" from the phone or PC. It connects as an XInput pad; the author notes some PC games need the X360 Emulator program.",
		Source: acSource{
			Owner: "AleWiccan", Repo: "TSP-Apps", TagPrefix: "GamepadBT", Asset: "*.zip",
			URL: "https://github.com/AleWiccan/TSP-Apps/releases/tag/GamepadBT1.4",
		},
		Stock: acFirmwareNote{acUntested, "Made for the Smart Pro; the release was tested with an Android phone"},
		Cross: acFirmwareNote{acUntested, "Not reported on CrossMix"},
		Deps: []acDep{
			{Name: "Bluetooth", Kind: acDepBluetooth, Why: "The handheld pairs as \"Trimui Gamepad\""},
			{Name: "A phone or PC to pair with", Kind: acDepRemote, Why: "Paired from the other device's Bluetooth settings"},
		},
		Places:  []string{"device"},
		Status:  acStatusUntested,
		Checked: "The GamepadBT1.4 release notes document the steps and the XInput caveat; the release's asset name was not checked, so the installer takes its one zip and checks that it holds an app.",
	},
	{
		ID:   "gamepadpc",
		Name: "Gamepad a PC",
		// Its folder name is not documented: an installed copy is found by
		// its label only.
		Labels:  []string{"Gamepad a PC", "Gamepad to PC"},
		Summary: "Use the handheld as a controller for a PC (AleWiccan's TSP-Apps)",
		About: "Listed in the README of AleWiccan's TSP-Apps among the ready apps. The README does not say how it connects or what the PC " +
			"needs, and no release of it could be traced, so the App Center does not download it: install it from the repository's Releases " +
			"page by hand. JukaHub's own Controller Mode does the same job with documented receivers.",
		Source: acSource{
			URL: "https://github.com/AleWiccan/TSP-Apps",
		},
		Stock: acFirmwareNote{acUntested, "Made for the Smart Pro; how it connects is not documented"},
		Cross: acFirmwareNote{acUntested, "Not reported on CrossMix"},
		Deps: []acDep{
			{Name: "Network", Kind: acDepWiFi, Why: "A controller for a PC that is not plugged in needs a link to it"},
			{Name: "A program on the PC", Kind: acDepRemote, Why: "Whatever the PC side needs is not documented; JukaHub cannot check it"},
		},
		Places:  []string{"device"},
		Status:  acStatusUntested,
		Checked: "Only its name in the TSP-Apps README.",
	},
	{
		ID:      "trimuidrop",
		Name:    "TrimUI Drop",
		Folders: []string{"SendFile"},
		Labels:  []string{"TrimUI Drop"},
		Summary: "Send files from a phone or PC over Wi-Fi by scanning a QR code",
		About: "An AirDrop-style transfer app for the Smart Pro and the Brick: the handheld shows a QR code, the phone opens an upload page, " +
			"and files are sorted into Roms, Music, Videos, Pictures, Books and Apps. It works on the home Wi-Fi or on a hotspot of its own, and " +
			"carries its own arm64 Python. JukaHub's phone companion and USB transfer do this too.",
		Source: acSource{
			Owner: "nhhuan90-ux", Repo: "trimui-drop", Asset: "trimui-drop-*.zip",
			URL: "https://github.com/nhhuan90-ux/trimui-drop/releases",
		},
		Stock: acFirmwareNote{acWorks, "Its release notes name the Smart Pro and the Brick and install it as Apps/SendFile"},
		Cross: acFirmwareNote{acUntested, "Not reported on CrossMix"},
		Deps: []acDep{
			{Name: "Wi-Fi (or its own hotspot)", Kind: acDepWiFi, Why: "The phone reaches the upload page over the network"},
			{Name: "A phone or PC with a browser", Kind: acDepRemote, Why: "Scans the QR code and uploads"},
		},
		Status:      acStatusChecked,
		Checked:     "Release notes v1.0.0 and v1.1.0 document the QR flow, the folders, TrimUI-Drop-v<version>.zip and Apps/SendFile.",
		SelfUpdates: true,
	},
}

// acCatalogueByID finds an entry.
func acCatalogueByID(id string) *acApp {
	for i := range acCatalogue {
		if acCatalogue[i].ID == id {
			return &acCatalogue[i]
		}
	}
	return nil
}

// acHasPlace reports whether app is offered at place.
func (a *acApp) hasPlace(place string) bool {
	for _, p := range a.Places {
		if p == place {
			return true
		}
	}
	return false
}

// acOpensExt reports whether the source says app opens ext (".epub").
func (a *acApp) opensExt(ext string) bool {
	for _, e := range a.Opens {
		if e == ext {
			return true
		}
	}
	return false
}

// ── Firmware ─────────────────────────────────────────────────────────────

// acFirmware is what runs the handheld, as far as the App Center cares.
type acFirmware int

const (
	acFwDesktop acFirmware = iota
	acFwStock
	acFwCrossMix
	acFwNextUI
	acFwSpruce
)

var acFirmwareCache struct {
	once sync.Once
	fw   acFirmware
}

// acDetectFirmware decides once per run, with JukaHub OS's own reading of the
// card (jukaosFirmwareName: NextUI, CrossMix, spruceOS or TrimUI's own). It
// only stats a few paths, and never runs inside DetectDevice's own once (an
// init deadlock once came from that).
func acDetectFirmware() acFirmware {
	acFirmwareCache.once.Do(func() {
		switch {
		case runtime.GOOS == "windows" || !IsTrimuiDevice():
			acFirmwareCache.fw = acFwDesktop
		case nextuiActive():
			acFirmwareCache.fw = acFwNextUI
		default:
			switch jukaosFirmwareName() {
			case "NextUI":
				acFirmwareCache.fw = acFwNextUI
			case "CrossMix":
				acFirmwareCache.fw = acFwCrossMix
			case "spruceOS":
				acFirmwareCache.fw = acFwSpruce
			default:
				acFirmwareCache.fw = acFwStock
			}
		}
	})
	return acFirmwareCache.fw
}

// acFirmwareName is the page header's read-out.
func acFirmwareName(fw acFirmware) string {
	switch fw {
	case acFwStock:
		return "Stock firmware"
	case acFwCrossMix:
		return "CrossMix"
	case acFwNextUI:
		return "NextUI"
	case acFwSpruce:
		return "spruceOS"
	}
	return "Browse only on this PC"
}

// acBrowseOnlyText is the toast for Get, Update or Open where the App Center
// only browses: it names the two firmwares it works on.
func acBrowseOnlyText() string {
	switch fw := acDetectFirmware(); fw {
	case acFwNextUI, acFwSpruce:
		return "The App Center installs and starts apps on the TrimUI stock firmware and CrossMix; on " + acFirmwareName(fw) + " it only browses"
	case acFwStock, acFwCrossMix:
		return "This card has no Apps folder, so the App Center only browses"
	}
	return "Installing and starting apps works on a TrimUI with the stock firmware or CrossMix; on this PC the App Center only browses"
}

// acHandheld reports whether apps can be installed and started here: a
// TrimUI with an Apps folder (stock or CrossMix). NextUI keeps its tools as
// .pak folders, and spruceOS lays out its apps its own way; the App Center
// manages neither.
func acHandheld() bool {
	fw := acDetectFirmware()
	return (fw == acFwStock || fw == acFwCrossMix) && acAppsRoot() != ""
}

// acAppsRoot is the card's Apps folder ("" when there is none).
func acAppsRoot() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	if dirs := systemAppDirs(); len(dirs) > 0 {
		return dirs[0]
	}
	return ""
}

// acNoteHere is the firmware line that applies to this handheld.
func (a *acApp) noteHere() (acFirmwareNote, bool) {
	switch acDetectFirmware() {
	case acFwStock:
		return a.Stock, true
	case acFwCrossMix:
		return a.Cross, true
	}
	return acFirmwareNote{}, false
}

// acChipHere is the short word a row shows for this handheld: "Works here",
// "Needs CrossMix", "CrossMix CE only", "Untested here", "Not for this
// firmware", "Not for NextUI" or, on a PC, "TrimUI app".
func (a *acApp) chipHere() (string, tbxTone) {
	note, ok := a.noteHere()
	if !ok {
		if fw := acDetectFirmware(); fw == acFwNextUI || fw == acFwSpruce {
			return "Not for " + acFirmwareName(fw), tbxToneNeutral
		}
		return "TrimUI app", tbxToneNeutral
	}
	switch note.State {
	case acWorks:
		return "Works here", tbxToneGood
	case acNeeds:
		fw := acDetectFirmware()
		bundled := strings.ToLower(a.Source.Bundled)
		ce := strings.Contains(bundled, "crossmix-os ce")
		switch {
		case fw == acFwStock && ce:
			return "Needs CrossMix CE", tbxToneWarn
		case fw == acFwStock && (a.Cross.State == acWorks || strings.Contains(bundled, "crossmix")):
			return "Needs CrossMix", tbxToneWarn
		case fw == acFwCrossMix && ce:
			// KOReader: the community edition ships it, CrossMix 1.3.0 does not.
			return "CrossMix CE only", tbxToneWarn
		}
		return "Needs more", tbxToneWarn
	case acNotSupported:
		return "Not for this firmware", tbxToneBad
	}
	return "Untested here", tbxToneNeutral
}

// ── Dependencies ─────────────────────────────────────────────────────────

// acDepState is a dependency's live answer.
type acDepState int

const (
	acDepUnknown acDepState = iota
	acDepOK
	acDepMissing
)

// acDepCheck answers one dependency. It reads the card and sysfs: call it
// off the render thread (the sheet fills in through tbxPost).
func acDepCheck(d acDep) (acDepState, string) {
	switch d.Kind {
	case acDepFile:
		for _, p := range d.Paths {
			if pkgExists(p) {
				return acDepOK, p
			}
		}
		if len(d.Paths) > 0 {
			return acDepMissing, d.Paths[0] + " is not there yet"
		}
	case acDepBluetooth:
		if runtime.GOOS == "windows" {
			return acDepUnknown, "Checked on the handheld"
		}
		// A controller the kernel lists can still be switched off (rfkill):
		// then the radio is off whatever hci0 says (rfkill_sysfs.go).
		if blocked, ok := rfkillSysfsBlocked(btRfkillRoot, rfkillBluetooth); ok && blocked {
			return acDepMissing, "Bluetooth is switched off (turn it on in Quick Settings)"
		}
		if entries, err := os.ReadDir("/sys/class/bluetooth"); err == nil && len(entries) > 0 {
			return acDepOK, "Bluetooth controller " + entries[0].Name()
		}
		return acDepMissing, "No Bluetooth controller is up (turn Bluetooth on in Quick Settings)"
	case acDepWiFi:
		// The local network is what these apps need, not the internet: an
		// address on Wi-Fi (or USB networking) is enough.
		if toolsIPAddress() != "" {
			if IsOnline() {
				return acDepOK, "On a network"
			}
			return acDepOK, "On a network (no internet, which this does not need)"
		}
		return acDepMissing, "Not on a network"
	case acDepRemote:
		return acDepUnknown, "On the other device: JukaHub cannot check it"
	}
	return acDepUnknown, ""
}

// acDepWord is the sheet's word for a state.
func acDepWord(s acDepState) string {
	switch s {
	case acDepOK:
		return "OK"
	case acDepMissing:
		return "Missing"
	}
	return "Can't check"
}

// ── Installed apps ───────────────────────────────────────────────────────

// acInstalled is one app folder on the card.
type acInstalled struct {
	Dir    string
	Folder string
	Label  string
	Launch string
	Icon   string
	// AppID is the catalogue entry it matched ("" for any other app).
	AppID string
	// Marker is the App Center's record in the folder; HasMarker false means
	// it was installed by hand (or by an older JukaHub).
	Marker    acMarker
	HasMarker bool
	// Declared are the extensions its config.json declares under
	// "jukahub": {"opens": [...]} (docs/APP_CENTER.md).
	Declared []string
	// ForwardsArgs reports that its launch script refers to its arguments
	// ($1, $@, $*), so a file handed to it can reach the app.
	ForwardsArgs bool
	// RollbackTag is the version kept for Roll back ("" with HasRollback when
	// it does not say).
	HasRollback bool
	RollbackTag string
	// HasKept reports older copies of its files kept aside in the rollback
	// folder (pkgKeepAside); Remove deletes them too.
	HasKept bool
}

// acMarkerFile is the App Center's record inside an app folder it installed.
// It starts with a dot: MainUI and JukaHub's scans ignore it.
const acMarkerFile = ".jukahub-appcenter.json"

// acMarker is that record.
type acMarker struct {
	Schema      int    `json:"schema"`
	AppID       string `json:"app_id"`
	Tag         string `json:"tag"`
	Asset       string `json:"asset,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Verified    bool   `json:"verified,omitempty"`
	Source      string `json:"source,omitempty"`
	InstalledAt string `json:"installed_at,omitempty"`
	InstalledBy string `json:"installed_by"`
}

// acReadSmall reads a file of at most limit bytes; a larger one is refused
// without being read whole.
func acReadSmall(path string, limit int64) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, false
	}
	return data, true
}

// acReadMarker reads an app folder's record.
func acReadMarker(dir string) (acMarker, bool) {
	var m acMarker
	data, ok := acReadSmall(filepath.Join(dir, acMarkerFile), 64<<10)
	if !ok || json.Unmarshal(data, &m) != nil || m.Schema < 1 {
		return acMarker{}, false
	}
	return m, true
}

// acScanAge is how long a scan of the Apps folder is reused. The page reads
// it once a second and "Open with" on every open; a change the App Center
// makes rescans at once, and the Apps screen's own scan starts one
// (acWarmUp), so an app copied onto the card shows up within it.
const acScanAge = 30 * time.Second

var acScan struct {
	mu       sync.Mutex
	apps     []acInstalled
	at       time.Time
	done     bool
	scanning atomic.Bool
	// gen counts scans asked for by changes (an install, a removal), so a
	// scan that started before one cannot overwrite the newer answer.
	gen atomic.Uint64
}

// acInstalledNow is the cached list of the card's apps. The Apps screen's
// scan warms it on a goroutine (acWarmUp); a call that comes before any scan
// has landed (the App Center or Open with reached first) scans at once, a few
// dozen small files. Later calls answer from memory and start a background
// rescan when the answer is older than acScanAge.
func acInstalledNow() []acInstalled {
	root := acAppsRoot()
	if root == "" {
		return nil
	}
	acScan.mu.Lock()
	done, at := acScan.done, acScan.at
	apps := acScan.apps
	acScan.mu.Unlock()
	if !done {
		acRecoverOnce(root)
		return acScanStore(root)
	}
	if time.Since(at) > acScanAge {
		acScanStart(root)
	}
	return apps
}

// acScanStore scans root and keeps the answer unless a change's scan
// (acRescanNow) landed while it ran; it returns the list that is kept.
func acScanStore(root string) []acInstalled {
	gen := acScan.gen.Load()
	list := acScanApps(root)
	acScan.mu.Lock()
	defer acScan.mu.Unlock()
	if acScan.done && acScan.gen.Load() != gen {
		return acScan.apps
	}
	acScan.apps, acScan.at, acScan.done = list, time.Now(), true
	return list
}

// acScanStart rescans on a goroutine unless one is running.
func acScanStart(root string) {
	if !acScan.scanning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer acScan.scanning.Store(false)
		acScanStore(root)
	}()
}

// acWarmUp is the Apps screen's scan hook (apps.go scanInstalledApps, main
// thread): the recovery pass for an install a power cut stopped runs now, not
// on the first visit to the App Center, and the scan of the Apps folder lands
// off the render thread, so neither the page nor Open with pays for it.
func acWarmUp() {
	if !acHandheld() {
		return
	}
	root := acAppsRoot()
	acRecoverOnce(root)
	acScan.mu.Lock()
	fresh := acScan.done && time.Since(acScan.at) < 5*time.Second
	acScan.mu.Unlock()
	if fresh || !acScan.scanning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer acScan.scanning.Store(false)
		// The recovery pass may still be renaming folders: the scan reads the
		// card after it.
		<-acRecover.done
		acScanStore(root)
	}()
}

// acRescanNow scans again at once (after an install, a rollback or a
// removal, on a goroutine).
func acRescanNow() {
	root := acAppsRoot()
	if root == "" {
		return
	}
	gen := acScan.gen.Add(1)
	list := acScanApps(root)
	acScan.mu.Lock()
	// A later change's scan may have landed first; the newer answer stays.
	if acScan.gen.Load() == gen {
		acScan.apps, acScan.at, acScan.done = list, time.Now(), true
	}
	acScan.mu.Unlock()
}

// acScanApps reads every app folder directly under root (MainUI reads only
// that level). Dot-folders are the installer's own (staging, rollback,
// trash) and JukaHub's own folder is not an app to manage.
func acScanApps(root string) []acInstalled {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	// The rollback slots, read without pkgOpMu: this runs on the main thread
	// the first time, and only Windows refuses a rename while a marker is
	// open (the Apps folder is a handheld's).
	backs, kept := map[string]bool{}, map[string]bool{}
	if slots, err := os.ReadDir(filepath.Join(root, pkgRollbackDir)); err == nil {
		for _, s := range slots {
			if !s.IsDir() {
				continue
			}
			// A copy kept aside is ".kept-<Folder>-<stamp>"; the stamp holds
			// no "-" (pkgKeptCopies).
			if rest, ok := strings.CutPrefix(s.Name(), pkgKeptPrefix); ok {
				if i := strings.LastIndexByte(rest, '-'); i > 0 {
					kept[rest[:i]] = true
				}
				continue
			}
			if !skipScanEntryName(s.Name()) {
				backs[s.Name()] = true
			}
		}
	}
	var out []acInstalled
	for _, e := range entries {
		if !e.IsDir() || skipScanEntryName(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if isSelfAppFolder(dir) {
			continue
		}
		entry, ok := scanAppSubdir(dir)
		if !ok {
			continue
		}
		in := acInstalled{Dir: dir, Folder: e.Name(), Label: entry.Name, Launch: entry.Path, Icon: entry.Icon}
		in.Marker, in.HasMarker = acReadMarker(dir)
		in.AppID = acMatchApp(e.Name(), entry.Name)
		if in.HasMarker && acCatalogueByID(in.Marker.AppID) != nil {
			in.AppID = in.Marker.AppID
		}
		in.Declared = acDeclaredOpens(dir)
		in.ForwardsArgs = acScriptForwardsArgs(entry.Path)
		in.HasKept = kept[e.Name()]
		if backs[e.Name()] {
			in.HasRollback = true
			back := filepath.Join(root, pkgRollbackDir, e.Name())
			if m, ok := acReadMarker(back); ok && m.Tag != "" {
				in.RollbackTag = m.Tag
			} else if pm, ok := pkgReadMarker(back); ok {
				in.RollbackTag = strings.TrimSpace(pm.Version)
			}
		}
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	return out
}

// acMatchApp is the catalogue entry an app folder is, by folder name or
// label ("" when none).
func acMatchApp(folder, label string) string {
	fl := pkgFoldName(label)
	for i := range acCatalogue {
		a := &acCatalogue[i]
		for _, f := range a.Folders {
			if strings.EqualFold(f, folder) {
				return a.ID
			}
		}
		for _, l := range a.Labels {
			if fl != "" && pkgFoldName(l) == fl {
				return a.ID
			}
		}
	}
	return ""
}

// acInstalledFor is the installed copy of a catalogue entry.
func acInstalledFor(id string) (acInstalled, bool) {
	for _, in := range acInstalledNow() {
		if in.AppID == id {
			return in, true
		}
	}
	return acInstalled{}, false
}

// acExtRE is what a declared extension may look like: a dot and up to ten
// letters or digits.
var acExtRE = regexp.MustCompile(`^\.[a-z0-9]{1,10}$`)

// acDeclaredOpens reads "jukahub": {"opens": [".epub", ...]} from an app's
// config.json. Anything that is not a plain extension is dropped.
func acDeclaredOpens(dir string) []string {
	data, ok := acReadSmall(filepath.Join(dir, "config.json"), 256<<10)
	if !ok {
		return nil
	}
	var cfg struct {
		JukaHub struct {
			Opens []string `json:"opens"`
		} `json:"jukahub"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return nil
	}
	var out []string
	for _, e := range cfg.JukaHub.Opens {
		e = strings.ToLower(strings.TrimSpace(e))
		if e != "" && !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		if acExtRE.MatchString(e) && len(out) < 64 {
			out = append(out, e)
		}
	}
	return out
}

// acScriptForwardsArgs reports whether a launch script refers to its
// arguments, by KOReader's reading of a script (koreader.go
// koreaderScriptForwards: "$1", "$@" or "$*" outside single quotes, on a line
// that is not a comment or an echo). It is a reading of the text, said as
// such on the sheet: a script that names "$1" may still use it for something
// else.
func acScriptForwardsArgs(launch string) bool {
	f, err := os.Open(launch)
	if err != nil {
		return false
	}
	defer f.Close()
	head, _ := io.ReadAll(io.LimitReader(f, koreaderScriptMax))
	return koreaderScriptForwards(string(head))
}
