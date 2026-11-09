package main

// secaudit.go — the Audit toolkit's catalog: which security tools exist, how
// each one gets onto *this* device, and how it is started once it is there.
//
// Every entry carries a recipe per platform, and the screen shows the tools
// that apply first:
//
//   - Windows (x86-64): the vendor installers through winget, which checks the
//     installer against the SHA-256 in its signed package manifest; portable
//     zips from the project's own download server for the tools winget does not
//     carry; or, where the project ships only a format JukaHub cannot unpack, a
//     button that opens the official download page.
//   - The handheld (TrimUI, Linux arm64, no desktop): static arm64 releases
//     where the project publishes them (verified against the release's
//     checksum list), Python and Perl tools only when the firmware actually has
//     the interpreter, and an honest "Not available on this device" with the
//     reason for everything else — a Java desktop app or a GPU cracker cannot
//     run there, and a button that pretends otherwise is worse than none.
//   - Desktop Linux: the distribution's packages, with the command to copy.
//
// Nothing here is installed without a press, nothing is started with a target,
// and tools that send traffic to other hosts sit behind the same one-time
// "systems you own or may test" acknowledgement as the Toolbox scanners.
// Command-line tools open in the Terminal with their help command typed in;
// desktop tools start as their own process. Detection is forgiving: PATH first,
// then the absolute paths the vendor installers use (a fresh install's PATH
// change does not reach a running app).

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// auditRecipeKind is how a tool gets onto one platform.
type auditRecipeKind int

const (
	// auditKindNone: not available here; Reason says why.
	auditKindNone auditRecipeKind = iota
	// auditKindWinget: `winget install --id WingetID` (Windows).
	auditKindWinget
	// auditKindGitHub: the latest GitHub release asset matching Assets, checked
	// against the release's checksum list when it has one.
	auditKindGitHub
	// auditKindArchive: a portable zip from a fixed official URL, unpacked whole.
	auditKindArchive
	// auditKindScript: a Python or Perl project from its GitHub branch archive;
	// Needs names the interpreter.
	auditKindScript
	// auditKindManual: the user installs it from Page (or with Hint).
	auditKindManual
)

// auditRecipe is one platform's way in.
type auditRecipe struct {
	Kind     auditRecipeKind
	WingetID string
	Repo     string
	Assets   [][]string
	URL      string
	// Entry is the file to start, relative to the unpacked folder; for an
	// archive whose layout varies it is the file name searched for.
	Entry string
	// Needs is the interpreter a script needs: "python" or "perl".
	Needs string
	// Page is the official download page.
	Page string
	// Hint is a command to copy (a package manager line).
	Hint string
	// Reason explains why the tool cannot run here.
	Reason string
	// Note is a caveat worth reading before installing.
	Note string
}

// auditPlatform names the recipe set that applies.
type auditPlatform string

const (
	auditPlatformWindows  auditPlatform = "windows"
	auditPlatformHandheld auditPlatform = "handheld"
	auditPlatformLinux    auditPlatform = "linux"
	auditPlatformOther    auditPlatform = "other"
)

// auditPlatformOverride is a test seam.
var auditPlatformOverride auditPlatform

// auditCurrentPlatform is this device's recipe set.
func auditCurrentPlatform() auditPlatform {
	if auditPlatformOverride != "" {
		return auditPlatformOverride
	}
	switch {
	case runtime.GOOS == "windows":
		return auditPlatformWindows
	case runtime.GOOS == "linux" && P().IsHandheld():
		return auditPlatformHandheld
	case runtime.GOOS == "linux":
		return auditPlatformLinux
	}
	return auditPlatformOther
}

// auditPlatformLabel is how the screen names the platform.
func auditPlatformLabel(p auditPlatform) string {
	switch p {
	case auditPlatformWindows:
		return "Windows x64"
	case auditPlatformHandheld:
		return deviceDisplayName(DetectDevice()) + " · Linux " + runtime.GOARCH
	case auditPlatformLinux:
		return "Linux " + runtime.GOARCH
	}
	return runtime.GOOS + " " + runtime.GOARCH
}

// secAuditTool is one entry in the audit catalog.
type secAuditTool struct {
	// ID is the stable key used in rows and actions.
	ID string
	// Name is what the row shows.
	Name string
	// Category groups the list: "Web & API", "Network" or "Validation".
	Category string
	// Summary is one line about what the tool is for.
	Summary string
	// Binaries are looked up on PATH in order; the first hit wins.
	Binaries []string
	// Paths are absolute candidates for installers that do not touch PATH.
	// $VAR and ~ are expanded before the file is tested.
	Paths []string
	// Version is the argument vector for a version report. Empty means the tool
	// is a GUI application: opening it starts its window.
	Version []string
	// Start is what the Terminal is opened with after the program path (a help
	// command, so Enter shows usage instead of starting a scan).
	Start []string
	// Install is the short hint shown when the tool is not installed.
	Install string
	// SendsTraffic marks a tool whose normal use talks to other hosts.
	SendsTraffic bool
	// Recipes maps an auditPlatform to how the tool is installed there.
	Recipes map[auditPlatform]auditRecipe
}

// GUI reports whether opening the tool starts a window instead of a command.
func (t secAuditTool) GUI() bool { return len(t.Version) == 0 }

// Recipe is the tool's recipe for p; a missing entry means not available.
func (t secAuditTool) Recipe(p auditPlatform) auditRecipe {
	if r, ok := t.Recipes[p]; ok {
		return r
	}
	return auditRecipe{Kind: auditKindNone, Reason: "There is no build of " + t.Name + " for this system."}
}

// secAuditCatalog is the list, grouped the way a security review is usually
// organised.
func secAuditCatalog() []secAuditTool {
	return []secAuditTool{
		// ── Web application & API testing ──────────────────────────────────
		{
			ID:       "burp",
			Name:     "Burp Suite",
			Category: "Web & API",
			Summary:  "Intercept and replay requests against your own web app or API",
			Binaries: []string{"burpsuite", "BurpSuiteCommunity"},
			Paths: []string{
				`$LOCALAPPDATA\Programs\BurpSuiteCommunity\BurpSuiteCommunity.exe`,
				`C:\Program Files\BurpSuiteCommunity\BurpSuiteCommunity.exe`,
				`C:\Program Files\BurpSuite Professional\BurpSuite Professional.exe`,
				"/opt/BurpSuiteCommunity/BurpSuiteCommunity",
				"~/BurpSuiteCommunity/BurpSuiteCommunity",
			},
			Install:      "portswigger.net/burp/communitydownload",
			SendsTraffic: true,
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindWinget, WingetID: "PortSwigger.BurpSuite.Community",
					Page: "https://portswigger.net/burp/communitydownload"},
				auditPlatformHandheld: {Kind: auditKindNone,
					Reason: "Burp Suite is a desktop Java application. The handheld has no desktop or Java runtime to run it."},
				auditPlatformLinux: {Kind: auditKindManual, Page: "https://portswigger.net/burp/communitydownload",
					Note: "PortSwigger publishes a Linux installer (x86-64 and ARM64)."},
			},
		},
		{
			ID:       "zap",
			Name:     "OWASP ZAP",
			Category: "Web & API",
			Summary:  "Open-source web app scanner; the free path to a repeatable check",
			Binaries: []string{"zap.sh", "zaproxy", "zap"},
			Paths: []string{
				`C:\Program Files\ZAP\Zed Attack Proxy\ZAP.exe`,
				`C:\Program Files\ZAP\Zed Attack Proxy\zap.bat`,
				`C:\Program Files (x86)\ZAP\Zed Attack Proxy\zap.bat`,
				"/usr/share/zaproxy/zap.sh",
				"/opt/zaproxy/zap.sh",
			},
			Install:      "zaproxy.org/download",
			SendsTraffic: true,
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindWinget, WingetID: "ZAP.ZAP", Page: "https://www.zaproxy.org/download/",
					Note: "ZAP runs on Java 17 or newer. If it does not open after installing, install a Java runtime such as Eclipse Temurin."},
				auditPlatformHandheld: {Kind: auditKindNone,
					Reason: "ZAP needs Java and a desktop session; the handheld's firmware has neither."},
				auditPlatformLinux: {Kind: auditKindManual, Page: "https://www.zaproxy.org/download/", Hint: "sudo snap install zaproxy --classic"},
			},
		},
		{
			ID:           "sqlmap",
			Name:         "sqlmap",
			Category:     "Web & API",
			Summary:      "Confirms an SQL injection on an endpoint you are allowed to test",
			Binaries:     []string{"sqlmap", "sqlmap.py"},
			Version:      []string{"--version"},
			Start:        []string{"-h"},
			Install:      "pip install sqlmap",
			SendsTraffic: true,
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindScript, Needs: "python", Entry: "sqlmap.py",
					URL: "https://github.com/sqlmapproject/sqlmap/archive/refs/heads/master.zip"},
				auditPlatformHandheld: {Kind: auditKindScript, Needs: "python", Entry: "sqlmap.py",
					URL: "https://github.com/sqlmapproject/sqlmap/archive/refs/heads/master.zip"},
				auditPlatformLinux: {Kind: auditKindScript, Needs: "python", Entry: "sqlmap.py",
					URL: "https://github.com/sqlmapproject/sqlmap/archive/refs/heads/master.zip"},
			},
		},
		{
			ID:           "nikto",
			Name:         "Nikto",
			Category:     "Web & API",
			Summary:      "Fast server scan for risky files and known misconfigurations",
			Binaries:     []string{"nikto", "nikto.pl"},
			Version:      []string{"-Version"},
			Start:        []string{"-Help"},
			Install:      "apt install nikto",
			SendsTraffic: true,
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindScript, Needs: "perl", Entry: "program/nikto.pl",
					URL: "https://github.com/sullo/nikto/archive/refs/heads/master.zip"},
				auditPlatformHandheld: {Kind: auditKindScript, Needs: "perl", Entry: "program/nikto.pl",
					URL: "https://github.com/sullo/nikto/archive/refs/heads/master.zip"},
				auditPlatformLinux: {Kind: auditKindScript, Needs: "perl", Entry: "program/nikto.pl",
					URL: "https://github.com/sullo/nikto/archive/refs/heads/master.zip"},
			},
		},
		{
			ID:           "nuclei",
			Name:         "Nuclei",
			Category:     "Web & API",
			Summary:      "Template-driven checks for known vulnerabilities and misconfigurations",
			Binaries:     []string{"nuclei"},
			Version:      []string{"-version"},
			Start:        []string{"-h"},
			Install:      "github.com/projectdiscovery/nuclei/releases",
			SendsTraffic: true,
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindGitHub, Repo: "projectdiscovery/nuclei", Entry: "nuclei.exe",
					Assets: [][]string{{"windows", "amd64", ".zip"}},
					Note:   "The first run downloads the community scan templates."},
				auditPlatformHandheld: {Kind: auditKindGitHub, Repo: "projectdiscovery/nuclei", Entry: "nuclei",
					Assets: [][]string{{"linux", "arm64", ".zip"}},
					Note:   "The first run downloads the community scan templates to the card."},
				auditPlatformLinux: {Kind: auditKindGitHub, Repo: "projectdiscovery/nuclei", Entry: "nuclei",
					Assets: [][]string{{"linux", runtime.GOARCH, ".zip"}}},
			},
		},

		// ── Network & traffic analysis ─────────────────────────────────────
		{
			ID:       "nmap",
			Name:     "Nmap",
			Category: "Network",
			Summary:  "The standard network mapper: hosts, ports and services",
			Binaries: []string{"nmap"},
			Paths: []string{
				`C:\Program Files (x86)\Nmap\nmap.exe`,
				`C:\Program Files\Nmap\nmap.exe`,
			},
			Version:      []string{"--version"},
			Start:        []string{"-h"},
			Install:      "apt install nmap",
			SendsTraffic: true,
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindWinget, WingetID: "Insecure.Nmap", Page: "https://nmap.org/download.html",
					Note: "Raw scans use Npcap, which the Nmap installer offers."},
				auditPlatformHandheld: {Kind: auditKindNone,
					Reason: "Nmap publishes no ARM64 Linux build. For port scans on the handheld, install naabu from the Toolbox."},
				auditPlatformLinux: {Kind: auditKindManual, Page: "https://nmap.org/download.html", Hint: "sudo apt install nmap"},
			},
		},
		{
			ID:       "wireshark",
			Name:     "Wireshark",
			Category: "Network",
			Summary:  "Capture and read packets on this device's own interfaces",
			Binaries: []string{"wireshark", "wireshark-gtk"},
			Paths: []string{
				`C:\Program Files\Wireshark\Wireshark.exe`,
				`C:\Program Files (x86)\Wireshark\Wireshark.exe`,
			},
			Install:      "wireshark.org/download.html",
			SendsTraffic: true,
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindWinget, WingetID: "WiresharkFoundation.Wireshark",
					Page: "https://www.wireshark.org/download.html",
					Note: "Capturing needs Npcap, which the Wireshark installer offers to install."},
				auditPlatformHandheld: {Kind: auditKindNone,
					Reason: "Wireshark is a desktop application, and the handheld has no desktop to show it."},
				auditPlatformLinux: {Kind: auditKindManual, Page: "https://www.wireshark.org/download.html", Hint: "sudo apt install wireshark"},
			},
		},
		{
			ID:           "aircrack",
			Name:         "Aircrack-ng",
			Category:     "Network",
			Summary:      "Audit the encryption strength of a Wi-Fi network you operate",
			Binaries:     []string{"aircrack-ng"},
			Version:      []string{"--help"},
			Start:        []string{"--help"},
			Install:      "apt install aircrack-ng",
			SendsTraffic: true,
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindArchive, Entry: "aircrack-ng.exe",
					URL:  "https://download.aircrack-ng.org/aircrack-ng-1.7-win.zip",
					Page: "https://www.aircrack-ng.org/downloads.html",
					Note: "On Windows it analyses captures you already have; capturing needs Linux and a monitor-mode Wi-Fi adapter."},
				auditPlatformHandheld: {Kind: auditKindNone,
					Reason: "No ARM64 build is published, and the handheld's Wi-Fi chip cannot capture in monitor mode."},
				auditPlatformLinux: {Kind: auditKindManual, Page: "https://www.aircrack-ng.org/downloads.html", Hint: "sudo apt install aircrack-ng"},
			},
		},

		// ── Validation and password auditing ──────────────────────────────
		{
			ID:       "metasploit",
			Name:     "Metasploit Framework",
			Category: "Validation",
			Summary:  "Turns a suspected weakness into confirmed evidence, on your own host",
			Binaries: []string{"msfconsole", "msfvenom"},
			Paths: []string{
				`C:\metasploit-framework\bin\msfconsole.bat`,
				"/opt/metasploit-framework/bin/msfconsole",
			},
			Version:      []string{"--version"},
			Start:        []string{"-q"},
			Install:      "docs.metasploit.com — nightly installers",
			SendsTraffic: true,
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindManual,
					Page: "https://docs.metasploit.com/docs/using-metasploit/getting-started/nightly-installers.html",
					Note: "Antivirus software flags parts of Metasploit by design. Follow Rapid7's install guide for the exclusions it needs."},
				auditPlatformHandheld: {Kind: auditKindNone,
					Reason: "Metasploit needs Ruby and a full Linux system with a package manager; the handheld's firmware has neither."},
				auditPlatformLinux: {Kind: auditKindManual,
					Page: "https://docs.metasploit.com/docs/using-metasploit/getting-started/nightly-installers.html"},
			},
		},
		{
			ID:       "hashcat",
			Name:     "Hashcat",
			Category: "Validation",
			Summary:  "Recovers passwords from hashes you are authorized to audit",
			Binaries: []string{"hashcat"},
			Version:  []string{"--version"},
			Start:    []string{"--help"},
			Install:  "apt install hashcat",
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindManual, Page: "https://hashcat.net/hashcat/",
					Note: "Hashcat ships as a 7-Zip archive and needs a GPU driver with OpenCL, CUDA or HIP."},
				auditPlatformHandheld: {Kind: auditKindNone,
					Reason: "Hashcat needs a GPU driver with OpenCL; the handheld's graphics chip has none."},
				auditPlatformLinux: {Kind: auditKindManual, Page: "https://hashcat.net/hashcat/", Hint: "sudo apt install hashcat"},
			},
		},
		{
			ID:       "john",
			Name:     "John the Ripper",
			Category: "Validation",
			Summary:  "Slower than Hashcat, better at odd hash formats and CPU-only hosts",
			Binaries: []string{"john", "john-the-ripper"},
			Version:  []string{"--list=build-info"},
			Start:    []string{},
			Install:  "apt install john",
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindArchive, Entry: "john.exe",
					URL:  "https://www.openwall.com/john/k/john-1.9.0-jumbo-1-win64.zip",
					Page: "https://www.openwall.com/john/"},
				auditPlatformHandheld: {Kind: auditKindNone,
					Reason: "Openwall publishes no ARM64 Linux build; it has to be compiled from source on a PC."},
				auditPlatformLinux: {Kind: auditKindManual, Page: "https://www.openwall.com/john/", Hint: "sudo apt install john"},
			},
		},
	}
}

// secAuditFind returns the catalog entry with the given ID.
func secAuditFind(id string) (secAuditTool, bool) {
	for _, t := range secAuditCatalog() {
		if t.ID == id {
			return t, true
		}
	}
	return secAuditTool{}, false
}

// secAuditExpandPath expands $VARS and a leading ~ in a catalog path.
func secAuditExpandPath(p string) string {
	p = os.ExpandEnv(strings.TrimSpace(p))
	if strings.HasPrefix(p, "~"+string(os.PathSeparator)) || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			p = filepath.Join(home, p[2:])
		}
	}
	return p
}

// secAuditResolve finds the installed executable for a tool: what JukaHub
// installed first, then PATH, then the absolute installer paths. It returns ""
// when nothing is installed.
func secAuditResolve(t secAuditTool) (string, bool) {
	if rec, ok := auditRecordFor(t.ID); ok && rec.Path != "" {
		if info, err := os.Stat(rec.Path); err == nil && info.Mode().IsRegular() {
			return rec.Path, true
		}
	}
	for _, name := range t.Binaries {
		if path, err := exec.LookPath(name); err == nil {
			if abs, absErr := filepath.Abs(path); absErr == nil {
				return abs, true
			}
			return path, true
		}
	}
	for _, candidate := range t.Paths {
		path := secAuditExpandPath(candidate)
		if path == "" || strings.Contains(path, "$") {
			continue
		}
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, true
		}
	}
	return "", false
}

// secAuditCache holds one detection pass for a couple of seconds. LookPath
// walks every directory on PATH, so resolving every tool per frame would stat
// the card hundreds of times a second.
var secAuditCache struct {
	mu      sync.Mutex
	at      time.Time
	results map[string]string
}

// secAuditDetected returns tool ID → installed path ("" when missing).
func secAuditDetected() map[string]string {
	secAuditCache.mu.Lock()
	defer secAuditCache.mu.Unlock()
	if secAuditCache.results != nil && time.Since(secAuditCache.at) < 2*time.Second {
		return secAuditCache.results
	}
	results := make(map[string]string, len(secAuditCatalog()))
	for _, t := range secAuditCatalog() {
		path, _ := secAuditResolve(t)
		results[t.ID] = path
	}
	secAuditCache.results = results
	secAuditCache.at = time.Now()
	return results
}

// secAuditForget drops the detection cache, so a finished install shows at
// once.
func secAuditForget() {
	secAuditCache.mu.Lock()
	secAuditCache.results = nil
	secAuditCache.mu.Unlock()
}

// secAuditInstalledCount is the number the Tools hub shows.
func secAuditInstalledCount() int {
	n := 0
	for _, path := range secAuditDetected() {
		if path != "" {
			n++
		}
	}
	return n
}

// ── Interpreters ─────────────────────────────────────────────────────────

// auditInterpreter is the command that runs a script tool: the interpreter's
// path plus any fixed arguments ("py -3"). ok is false when it is missing.
func auditInterpreter(needs string) ([]string, bool) {
	switch needs {
	case "python":
		if runtime.GOOS == "windows" {
			// The Python launcher first; then python.exe. A python.exe in
			// WindowsApps is either a Microsoft Store install or the alias
			// that only opens the Store, so it counts only when it answers.
			if p, err := exec.LookPath("py"); err == nil && !auditIsStoreStub(p) {
				return []string{p, "-3"}, true
			}
			if p, err := exec.LookPath("python"); err == nil && (!auditIsStoreStub(p) || auditStorePythonWorks(p)) {
				return []string{p}, true
			}
			matches, _ := filepath.Glob(filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Python", "Python3*", "python.exe"))
			sort.Strings(matches)
			if len(matches) > 0 {
				return []string{matches[len(matches)-1]}, true
			}
			return nil, false
		}
		for _, name := range []string{"python3", "python"} {
			if p, err := exec.LookPath(name); err == nil {
				return []string{p}, true
			}
		}
	case "perl":
		if p, err := exec.LookPath("perl"); err == nil && !auditIsStoreStub(p) {
			return []string{p}, true
		}
		if runtime.GOOS == "windows" {
			if p := `C:\Strawberry\perl\bin\perl.exe`; fileExists(p) {
				return []string{p}, true
			}
		}
	}
	return nil, false
}

// auditIsStoreStub reports the Windows "app execution alias" placeholders.
func auditIsStoreStub(path string) bool {
	return strings.Contains(strings.ToLower(path), `\windowsapps\`)
}

// auditStorePython remembers which WindowsApps python.exe answered, so the
// probe runs once per path (detection runs it off the render thread first).
var auditStorePython struct {
	mu   sync.Mutex
	seen map[string]bool
}

// auditStorePythonWorks reports whether a python.exe in WindowsApps is a real
// Microsoft Store install: that one answers --version with "Python 3.x",
// while the placeholder alias prints a hint and exits with an error (it only
// opens the Store when started without arguments).
func auditStorePythonWorks(path string) bool {
	auditStorePython.mu.Lock()
	works, done := auditStorePython.seen[path]
	auditStorePython.mu.Unlock()
	if done {
		return works
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	auditConfigureHidden(cmd)
	cmd.WaitDelay = powerScriptWaitDelay
	out, err := cmd.CombinedOutput()
	works = err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Python 3")
	auditStorePython.mu.Lock()
	if auditStorePython.seen == nil {
		auditStorePython.seen = map[string]bool{}
	}
	auditStorePython.seen[path] = works
	auditStorePython.mu.Unlock()
	return works
}

// auditInterpreterLabel is how a missing interpreter is named.
func auditInterpreterLabel(needs string) string {
	if needs == "perl" {
		return "Perl"
	}
	return "Python 3"
}

// auditInterpreterWinget is the winget package that provides an interpreter.
func auditInterpreterWinget(needs string) string {
	if needs == "perl" {
		return "StrawberryPerl.StrawberryPerl"
	}
	return "Python.Python.3.12"
}

// ── Install records ──────────────────────────────────────────────────────

// auditRecord is what JukaHub installed for a tool: the program to start, the
// version it came from and whether the download matched a published checksum.
type auditRecord struct {
	Kind     string `json:"kind"`
	Path     string `json:"path,omitempty"`
	Version  string `json:"version,omitempty"`
	Verified bool   `json:"verified"`
	At       string `json:"installed_at,omitempty"`
}

// auditInstallRootOverride is a test seam for the install folder.
var auditInstallRootOverride string

// auditInstallRoot is where portable audit tools are unpacked: one folder per
// tool under the tools folder.
func auditInstallRoot() string {
	if dir := strings.TrimSpace(auditInstallRootOverride); dir != "" {
		return dir
	}
	dir := toolboxToolsDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "audit")
}

var auditRecords struct {
	mu     sync.Mutex
	root   string
	loaded bool
	recs   map[string]auditRecord
}

func auditRecordsLoadLocked() {
	root := auditInstallRoot()
	if auditRecords.loaded && auditRecords.root == root {
		return
	}
	auditRecords.root = root
	auditRecords.loaded = true
	auditRecords.recs = map[string]auditRecord{}
	if root == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(root, "audit-installed.json"))
	if err != nil {
		return
	}
	var recs map[string]auditRecord
	if json.Unmarshal(data, &recs) == nil && recs != nil {
		auditRecords.recs = recs
	}
}

func auditRecordsSaveLocked() error {
	if auditRecords.root == "" {
		return fmt.Errorf("tools folder unavailable")
	}
	if err := os.MkdirAll(auditRecords.root, PermDir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(auditRecords.recs, "", "  ")
	if err != nil {
		return err
	}
	target := filepath.Join(auditRecords.root, "audit-installed.json")
	if err := os.WriteFile(target+".tmp", data, 0o644); err != nil {
		return err
	}
	return os.Rename(target+".tmp", target)
}

// auditRecordFor returns what JukaHub installed for id.
func auditRecordFor(id string) (auditRecord, bool) {
	auditRecords.mu.Lock()
	defer auditRecords.mu.Unlock()
	auditRecordsLoadLocked()
	rec, ok := auditRecords.recs[id]
	return rec, ok
}

func auditSaveRecord(id string, rec auditRecord) {
	auditRecords.mu.Lock()
	defer auditRecords.mu.Unlock()
	auditRecordsLoadLocked()
	auditRecords.recs[id] = rec
	if err := auditRecordsSaveLocked(); err != nil {
		logPatch("[audit] cannot save the install record: %v", err)
	}
}

func auditForgetRecord(id string) {
	auditRecords.mu.Lock()
	defer auditRecords.mu.Unlock()
	auditRecordsLoadLocked()
	if _, ok := auditRecords.recs[id]; !ok {
		return
	}
	delete(auditRecords.recs, id)
	if err := auditRecordsSaveLocked(); err != nil {
		logPatch("[audit] cannot save the install record: %v", err)
	}
}

// ── Install engine ───────────────────────────────────────────────────────

var errAuditNoWinget = errors.New("winget is not available — install \"App Installer\" from the Microsoft Store, or use the download page")

// auditWingetRun runs winget with args and returns its cleaned-up output.
func auditWingetRun(ctx context.Context, args ...string) (string, error) {
	path, err := exec.LookPath("winget")
	if err != nil {
		return "", errAuditNoWinget
	}
	cmd := exec.CommandContext(ctx, path, args...)
	auditConfigureHidden(cmd)
	// An installer that leaves a helper holding winget's output must not keep
	// the sheet waiting after winget itself has finished.
	cmd.WaitDelay = powerScriptWaitDelay
	out, err := cmd.CombinedOutput()
	text := auditCleanConsole(string(out))
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		err = nil
	}
	if err != nil && strings.Contains(strings.ToLower(text), "no package found") {
		return text, fmt.Errorf("winget has no package with this name — use the download page instead")
	}
	return text, err
}

// auditCleanConsole turns a console program's output into lines for a sheet:
// progress bars redraw a line with carriage returns, so only the last state of
// each line is kept, and lines that are only bar and spinner glyphs go.
func auditCleanConsole(text string) string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if i := strings.LastIndex(line, "\r"); i >= 0 {
			line = line[i+1:]
		}
		line = strings.TrimRight(line, " \t")
		trimmed := strings.Trim(line, " -\\|/█▒░▓")
		if trimmed == "" {
			continue
		}
		out = append(out, line)
	}
	if len(out) > 80 {
		out = out[len(out)-80:]
	}
	return strings.Join(out, "\n")
}

// auditInstall installs t with recipe r and returns a short report. It blocks;
// callers run it on a goroutine.
func auditInstall(t secAuditTool, r auditRecipe) (string, error) {
	switch r.Kind {
	case auditKindWinget:
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		out, err := auditWingetRun(ctx, "install", "--id", r.WingetID, "--exact",
			"--accept-source-agreements", "--accept-package-agreements")
		secAuditForget()
		return out, err
	case auditKindGitHub:
		return auditInstallGitHub(t, r)
	case auditKindArchive, auditKindScript:
		return auditInstallArchive(t, r)
	}
	return "", fmt.Errorf("%s cannot be installed from here", t.Name)
}

// auditUpdate brings an installed tool to the latest version.
func auditUpdate(t secAuditTool, r auditRecipe) (string, error) {
	if r.Kind == auditKindWinget {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		out, err := auditWingetRun(ctx, "upgrade", "--id", r.WingetID, "--exact",
			"--accept-source-agreements", "--accept-package-agreements")
		secAuditForget()
		if err != nil && strings.Contains(strings.ToLower(out), "no available upgrade") {
			return out, nil
		}
		return out, err
	}
	return auditInstall(t, r)
}

// auditRemove uninstalls what JukaHub installed.
func auditRemove(t secAuditTool, r auditRecipe) (string, error) {
	defer secAuditForget()
	if r.Kind == auditKindWinget {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		return auditWingetRun(ctx, "uninstall", "--id", r.WingetID, "--exact")
	}
	root := auditInstallRoot()
	if root == "" {
		return "", fmt.Errorf("tools folder unavailable")
	}
	dir := filepath.Join(root, t.ID)
	if !isPathWithin(dir, root) {
		return "", fmt.Errorf("refusing to remove %s", dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("remove %s: %w", t.Name, err)
	}
	auditForgetRecord(t.ID)
	return t.Name + " removed.", nil
}

// auditInstallGitHub installs a single-binary release into its own folder,
// checked against the release's checksum list when it publishes one.
func auditInstallGitHub(t secAuditTool, r auditRecipe) (report string, err error) {
	root := auditInstallRoot()
	if root == "" {
		return "", fmt.Errorf("tools folder unavailable")
	}
	if !IsOnline() {
		return "", errToolsOffline
	}
	dir := filepath.Join(root, t.ID)
	if err := os.MkdirAll(dir, PermDir); err != nil {
		return "", err
	}
	run, ctx := installBarBeginRun(t.Name)
	defer func() {
		if err != nil {
			installBarFailRun(run, installCardDetail(err.Error()))
		}
	}()
	installBarPhase(installPhaseResolving)
	rel, err := toolboxReleaseFetch(ctx, r.Repo)
	if err != nil {
		return "", fmt.Errorf("find %s release: %w", t.Name, err)
	}
	asset, ok := pickToolAsset(rel.Assets, r.Assets)
	if !ok {
		return "", fmt.Errorf("%s %s has no build for this device", t.Name, rel.Tag)
	}
	tmp, err := os.CreateTemp(dir, ".download-*"+toolboxArchiveExt(asset.Name))
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)
	installBarPhase(installPhaseDownload)
	if err := downloadFileWithProgressCtx(ctx, asset.URL, tmpName, installBarProgress); err != nil {
		return "", fmt.Errorf("download %s: %w", t.Name, err)
	}
	_, verified, err := toolboxVerifyDownload(ctx, rel.Assets, asset.Name, tmpName)
	if err != nil {
		return "", fmt.Errorf("%s: %w", t.Name, err)
	}
	installBarPhase(installPhaseExtract)
	stage, err := os.MkdirTemp(dir, ".stage-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	staged := tmpName
	if toolboxArchived(asset.Name) {
		found, err := extractToolFiles(tmpName, stage, []string{r.Entry})
		if err != nil {
			return "", fmt.Errorf("unpack %s: %w", t.Name, err)
		}
		if len(found) == 0 {
			return "", fmt.Errorf("the %s download did not contain %s", t.Name, r.Entry)
		}
		staged = filepath.Join(stage, strings.ToLower(r.Entry))
	}
	target := filepath.Join(dir, r.Entry)
	if runtime.GOOS != "windows" {
		_ = os.Chmod(staged, PermExec)
	}
	if err := toolboxPlace(staged, target, fileExists(target)); err != nil {
		return "", fmt.Errorf("install %s: %w", t.Name, err)
	}
	auditSaveRecord(t.ID, auditRecord{Kind: "github", Path: target, Version: rel.Tag, Verified: verified,
		At: time.Now().UTC().Format(time.RFC3339)})
	secAuditForget()
	installBarDoneRun(run)
	check := "The release publishes no checksum for this file."
	if verified {
		check = "Matched the SHA-256 the project publishes for this release."
	}
	return t.Name + " " + rel.Tag + " installed to " + target + "\n\n" + check, nil
}

// auditInstallArchive unpacks a portable zip (or a script project) into the
// tool's own folder, replacing an older copy only after the new one unpacked.
func auditInstallArchive(t secAuditTool, r auditRecipe) (report string, err error) {
	root := auditInstallRoot()
	if root == "" {
		return "", fmt.Errorf("tools folder unavailable")
	}
	if r.Kind == auditKindScript {
		if _, ok := auditInterpreter(r.Needs); !ok {
			return "", fmt.Errorf("%s needs %s, which is not installed", t.Name, auditInterpreterLabel(r.Needs))
		}
	}
	if !IsOnline() {
		return "", errToolsOffline
	}
	if err := os.MkdirAll(root, PermDir); err != nil {
		return "", err
	}
	run, ctx := installBarBeginRun(t.Name)
	defer func() {
		if err != nil {
			installBarFailRun(run, installCardDetail(err.Error()))
		}
	}()
	tmp, err := os.CreateTemp(root, ".download-*.zip")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)
	installBarPhase(installPhaseDownload)
	if err := downloadFileWithProgressCtx(ctx, r.URL, tmpName, installBarProgress); err != nil {
		return "", fmt.Errorf("download %s: %w", t.Name, err)
	}
	sum, err := hashFile(tmpName)
	if err != nil {
		return "", err
	}
	installBarPhase(installPhaseExtract)
	stage, err := os.MkdirTemp(root, ".stage-"+t.ID+"-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := auditExtractTree(ctx, tmpName, stage); err != nil {
		return "", fmt.Errorf("unpack %s: %w", t.Name, err)
	}
	entry := ""
	if r.Kind == auditKindScript {
		if p := filepath.Join(stage, filepath.FromSlash(r.Entry)); fileExists(p) {
			entry = r.Entry
		}
	} else {
		entry = auditFindEntry(stage, r.Entry)
	}
	if entry == "" {
		return "", fmt.Errorf("the %s download did not contain %s", t.Name, r.Entry)
	}
	dir := filepath.Join(root, t.ID)
	old := dir + ".old"
	_ = os.RemoveAll(old)
	if fileExists(dir) || dirExists(dir) {
		if err := os.Rename(dir, old); err != nil {
			return "", fmt.Errorf("move the old copy aside: %w", err)
		}
	}
	if err := os.Rename(stage, dir); err != nil {
		_ = os.Rename(old, dir)
		return "", fmt.Errorf("install %s: %w", t.Name, err)
	}
	_ = os.RemoveAll(old)
	target := filepath.Join(dir, filepath.FromSlash(entry))
	if runtime.GOOS != "windows" && r.Kind != auditKindScript {
		_ = os.Chmod(target, PermExec)
	}
	auditSaveRecord(t.ID, auditRecord{Kind: "archive", Path: target, Verified: false,
		Version: "sha256 " + sum[:12], At: time.Now().UTC().Format(time.RFC3339)})
	secAuditForget()
	installBarDoneRun(run)
	return t.Name + " installed to " + dir + "\n\nThe project publishes no checksum for this download; it came over HTTPS from the official server.", nil
}

// auditExtractTree unpacks a whole zip into dest. A single top folder (GitHub's
// "<repo>-<branch>/") is dropped so paths start at the project root. Only
// regular files and folders are written, every path is checked to stay inside
// dest, and the total size is capped.
func auditExtractTree(ctx context.Context, archive, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		// An archive with unsafe member names opens with zip.ErrInsecurePath
		// and a usable reader; it is refused all the same.
		if zr != nil {
			zr.Close()
		}
		return fmt.Errorf("%w (%v)", errBadToolArchive, err)
	}
	defer zr.Close()
	prefix := auditCommonTopDir(zr.File)
	const maxTotal = int64(2 << 30)
	var total int64
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := filepath.ToSlash(f.Name)
		if unsafeArchiveName(name) {
			return fmt.Errorf("%w (unsafe archive path %q)", errBadToolArchive, f.Name)
		}
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if !isPathWithin(target, dest) {
			return fmt.Errorf("%w (%s escapes the install folder)", errBadToolArchive, f.Name)
		}
		if f.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(target, PermDir); err != nil {
				return err
			}
			continue
		}
		if !f.Mode().IsRegular() {
			continue
		}
		size := int64(f.UncompressedSize64)
		total += size
		if size < 0 || total > maxTotal {
			return fmt.Errorf("%w (the archive unpacks to more than %d MB)", errBadToolArchive, maxTotal>>20)
		}
		if err := os.MkdirAll(filepath.Dir(target), PermDir); err != nil {
			return err
		}
		if err := auditWriteZipEntry(ctx, f, target, size); err != nil {
			return err
		}
	}
	return nil
}

// auditWriteZipEntry writes one member; empty files are fine here (a Python
// package's __init__.py usually is).
func auditWriteZipEntry(ctx context.Context, f *zip.File, target string, size int64) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("%w (%s: %v)", errBadToolArchive, f.Name, err)
	}
	defer rc.Close()
	perm := os.FileMode(0o644)
	if f.Mode()&0o111 != 0 {
		perm = PermExec
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, &archiveReader{ctx: ctx, r: io.LimitReader(rc, size+1)})
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil && n != size {
		err = fmt.Errorf("%w (%s ended after %d of %d bytes)", errBadToolArchive, f.Name, n, size)
	}
	if err != nil {
		_ = os.Remove(target)
	}
	return err
}

// auditCommonTopDir is the single top-level folder every member sits in
// ("sqlmap-master/"), or "" when the archive has files at its root.
func auditCommonTopDir(files []*zip.File) string {
	prefix := ""
	for _, f := range files {
		name := filepath.ToSlash(f.Name)
		i := strings.Index(name, "/")
		if i <= 0 {
			return ""
		}
		top := name[:i+1]
		if prefix == "" {
			prefix = top
		} else if top != prefix {
			return ""
		}
	}
	return prefix
}

// auditFindEntry finds the program named name under root and returns its path
// relative to root ("" when it is missing). A 64-bit build is preferred when
// the archive carries both.
func auditFindEntry(root, name string) string {
	var found []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if strings.EqualFold(info.Name(), name) {
			if rel, err := filepath.Rel(root, p); err == nil {
				found = append(found, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	if len(found) == 0 {
		return ""
	}
	sort.Slice(found, func(i, j int) bool {
		a, b := strings.Contains(found[i], "64"), strings.Contains(found[j], "64")
		if a != b {
			return a
		}
		return len(found[i]) < len(found[j])
	})
	return found[0]
}

// ── Launch ───────────────────────────────────────────────────────────────

// auditTerminalCommand is the line the Terminal opens with for a command-line
// tool: interpreter (for scripts), program and its help arguments.
func auditTerminalCommand(t secAuditTool, path string) string {
	parts := []string{}
	if rec, ok := auditRecordFor(t.ID); ok && rec.Path == path {
		if r := t.Recipe(auditCurrentPlatform()); r.Kind == auditKindScript {
			if interp, ok := auditInterpreter(r.Needs); ok {
				parts = append(parts, interp...)
			}
		}
	}
	parts = append(parts, path)
	parts = append(parts, t.Start...)
	for i, p := range parts {
		if strings.ContainsAny(p, " \t") {
			parts[i] = `"` + p + `"`
		}
	}
	return strings.Join(parts, " ")
}

// secAuditOpen starts a GUI tool and lets it run on its own.
func secAuditOpen(path string) (string, error) {
	cmd := exec.Command(path)
	cmd.Dir = filepath.Dir(path)
	cmd.Env = helperToolEnv(path)
	auditConfigureDetached(cmd)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go func() { _ = cmd.Wait() }()
	return "Opened " + filepath.Base(path), nil
}

// secAuditProbe runs a command-line tool's own version report (or opens a GUI
// tool). It never passes a target, so no traffic leaves the device.
func secAuditProbe(id string, timeout time.Duration) (string, error) {
	t, ok := secAuditFind(id)
	if !ok {
		return "", fmt.Errorf("unknown tool %q", id)
	}
	path, found := secAuditResolve(t)
	if !found {
		return "", fmt.Errorf("%s is not installed", t.Name)
	}
	if t.GUI() {
		return secAuditOpen(path)
	}
	if timeout <= 0 {
		timeout = powerTimeoutScript
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	argv := append([]string{path}, t.Version...)
	if r := t.Recipe(auditCurrentPlatform()); r.Kind == auditKindScript {
		if rec, ok := auditRecordFor(t.ID); ok && rec.Path == path {
			if interp, ok := auditInterpreter(r.Needs); ok {
				argv = append(append([]string{}, interp...), argv...)
			}
		}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = filepath.Dir(path)
	cmd.Env = helperToolEnv(path)
	auditConfigureHidden(cmd)
	cmd.WaitDelay = powerScriptWaitDelay
	out, err := cmd.CombinedOutput()
	text := truncatePowerOutput(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return text + fmt.Sprintf("\n(stopped after %s)", timeout), ctx.Err()
	}
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		return text, nil
	}
	if err != nil && strings.TrimSpace(text) == "" {
		return "The tool exited with: " + err.Error(), err
	}
	return text, err
}
