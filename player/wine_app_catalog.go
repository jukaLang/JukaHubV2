package main

// wine_app_catalog.go — a short list of free, open-source programs that are
// made for Windows and are known to run under Wine. It is a reading list, not
// a store: JukaHub ships none of these programs, downloads none of them, and
// asks the user to get the installer from the project's own site.
//
// Only ordinary desktop tools are listed. Nothing here bypasses a licence,
// unlocks a console, or runs a game the user does not own. A password manager
// is included with a plain warning about keeping a master password on a
// handheld that can be lost.

import (
	"net/url"
	"strings"
	"sync"
	"time"
)

type wineAppSuggestion struct {
	Name    string
	Purpose string
	License string
	Source  string // the project's own download or release page
	Note    string
}

var wineAppSuggestions = []wineAppSuggestion{
	{
		Name:    "7-Zip",
		Purpose: "Open and make ZIP, 7z and tar archives",
		License: "LGPL-2.1-or-later",
		Source:  "https://www.7-zip.org/download.html",
		Note:    "The Windows build is the one to put in your Windows Apps folder; it runs well under Wine.",
	},
	{
		Name:    "Notepad++",
		Purpose: "Edit notes, configs and scripts with syntax colours",
		License: "GPL-3.0-or-later",
		Source:  "https://notepad-plus-plus.org/downloads/",
		Note:    "A small editor that suits a handheld's screen; pick the 32-bit build on older Wine setups.",
	},
	{
		Name:    "WinSCP",
		Purpose: "Copy files to and from your own PC over SFTP/SCP",
		License: "GPL-3.0-or-later",
		Source:  "https://winscp.net/eng/downloads.php",
		Note:    "Use it only with servers you own or are allowed to use. Store credentials in its saved sessions, not in a JukaHub config.",
	},
	{
		Name:    "WinMerge",
		Purpose: "Compare and merge text files",
		License: "GPL-2.0-or-later",
		Source:  "https://github.com/WinMerge/winmerge/releases",
		Note:    "Handy for checking a config you edited on the handheld against the one on your PC.",
	},
	{
		Name:    "Sumatra PDF",
		Purpose: "Read PDF and EPUB manuals on the card",
		License: "GPL-3.0-or-later",
		Source:  "https://www.sumatrapdfreader.org/download-free-pdf-viewer.html",
		Note:    "Light enough for a handheld; choose the portable build so it stays in its own folder.",
	},
	{
		Name:    "KeePass",
		Purpose: "Keep passwords in an encrypted database",
		License: "GPL-2.0-or-later",
		Source:  "https://keepass.info/download.html",
		Note:    "Think before putting a master password on a handheld that can be lost or stolen; the database is only as safe as the device.",
	},
	{
		Name:    "MPC-HC",
		Purpose: "Play local video and audio files",
		License: "GPL-2.0-or-later",
		Source:  "https://github.com/clsid2/mpc-hc/releases",
		Note:    "Use it for files you own; JukaHub's own video player stays the first choice on the device.",
	},
	{
		Name:    "Explorer++",
		Purpose: "A tabbed file manager for the Windows folders Wine uses",
		License: "MPL-2.0",
		Source:  "https://github.com/explorerplusplus/explorerplusplus/releases",
		Note:    "Take the portable build so it stays inside its own folder on the card.",
	},
	{
		Name:    "PuTTY",
		Purpose: "SSH and serial access to a PC you own",
		License: "MIT",
		Source:  "https://www.chiark.greenend.org.uk/~sgtatham/putty/latest.html",
		Note:    "Only connect to machines you are allowed to use; keep saved sessions out of shared configs.",
	},
}

// wineAppTitleKey folds a manifest title the way a user would read it: case,
// spaces, hyphens and dots are not differences ("7-zip 24.09" is "7zip2409").
func wineAppTitleKey(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		if r == ' ' || r == '-' || r == '_' || r == '.' || r == '+' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

var wineAppInstalledCache struct {
	mu    sync.Mutex
	at    time.Time
	names map[string]bool
}

// wineAppInstalledNames is the titles in the Windows Apps folder, rebuilt at
// most every few seconds: the card draws every frame.
func wineAppInstalledNames() map[string]bool {
	wineAppInstalledCache.mu.Lock()
	defer wineAppInstalledCache.mu.Unlock()
	if time.Since(wineAppInstalledCache.at) < 3*time.Second {
		return wineAppInstalledCache.names
	}
	names := map[string]bool{}
	for _, e := range windowsAppEntries() {
		names[wineAppTitleKey(e.Title)] = true
	}
	wineAppInstalledCache.at, wineAppInstalledCache.names = time.Now(), names
	return names
}

// wineAppCatalogLines is the list as plain text for the overlay card.
func wineAppCatalogLines(cursor int) []string {
	lines := []string{
		"Free, open-source Windows programs that run under Wine.",
		"JukaHub does not ship or download them: get each from its own site,",
		"then put the program folder in your Windows Apps folder.",
		"Use them with files and servers you are allowed to use.",
		"",
	}
	installed := wineAppInstalledNames()
	for i, app := range wineAppSuggestions {
		mark := " "
		if i == cursor {
			mark = "›"
		}
		line := mark + " " + app.Name + " · " + app.Purpose
		for title := range installed {
			if strings.Contains(title, wineAppTitleKey(app.Name)) {
				line += " · in your Windows Apps folder"
				break
			}
		}
		lines = append(lines, line)
		if i == cursor {
			lines = append(lines, "    "+app.License+" · "+app.Source, "    "+app.Note)
		}
	}
	lines = append(lines, "", "A opens the project's page on a paired PC, B goes back")
	return lines
}

// wineAppCatalogMove steps the list with wrap-around.
func wineAppCatalogMove(delta int) {
	wineAppCatalogUI.cursor = (wineAppCatalogUI.cursor + delta + len(wineAppSuggestions)) % len(wineAppSuggestions)
	PlayNavSound()
}

// wineAppCatalogOpen sends the project's own page to the paired PC or the
// device's browser. It never downloads a file itself.
func wineAppCatalogOpen() {
	if wineAppCatalogUI.cursor < 0 || wineAppCatalogUI.cursor >= len(wineAppSuggestions) {
		return
	}
	app := wineAppSuggestions[wineAppCatalogUI.cursor]
	if !IsWindows() {
		// A handheld cannot open a desktop browser here; the same QR the
		// Steam store page uses lets the phone carry the link.
		code, err := steamQRModules(app.Source)
		if err != nil {
			ShowToast("Could not draw the project page code", ToastKindError)
			return
		}
		qrZoomOpenCardDetail(code, app.Name+" · project site", "Scan with your phone to open the project's own page")
		return
	}
	if err := openExternalURL(app.Source); err != nil {
		ShowToast("Could not open the project page: "+firstLine(err.Error()), ToastKindError)
		return
	}
	ShowToast("Opening "+app.Name+"'s own site — get the installer there", ToastKindInfo)
}

var wineAppCatalogUI struct{ cursor int }

// wineAppCatalogWellFormed is a test seam for the catalogue's own data: every
// source must be a plain HTTPS page, every licence and purpose stated.
func wineAppCatalogWellFormed(list []wineAppSuggestion) bool {
	for _, app := range list {
		if app.Name == "" || app.License == "" || app.Purpose == "" || app.Source == "" {
			return false
		}
		// These are links the user opens, not URLs JukaHub fetches: the hard
		// rule is HTTPS and a host, not the download allowlist.
		u, err := url.Parse(app.Source)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return false
		}
	}
	return true
}
