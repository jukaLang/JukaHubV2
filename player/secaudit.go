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
//     where the project publishes them (Nuclei, OSV-Scanner, Gitleaks;
//     verified against the release's checksum list); Ubuntu's arm64 packages
//     through Linux packages (linuxpkg_*.go: Ubuntu's signed index, every
//     download checked against it) for Nmap, John the Ripper, Aircrack-ng
//     (without the helper programs it brings for uses the handheld does not
//     have, SkipDeps), testssl.sh, sslscan, ssh-audit, tcpdump, YARA and
//     Binwalk, and for the Python and Perl a script tool needs with its
//     modules (Modules),
//     installed as part of its install press, before it opens when one is
//     missing, and at a quiet moment by the daily pass (audit_interp_auto.go);
//     and an honest "Not available on this device" with the reason for
//     everything else — a Java desktop app or a GPU cracker cannot run there,
//     and a button that pretends otherwise is worse than none.
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
	"slices"
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
	// auditKindLinuxPkg: Ubuntu's arm64 Packages through Linux packages on
	// the handheld; Entry is the program's name in <data>/linux/bin.
	auditKindLinuxPkg
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
	// Packages are the Ubuntu package names an auditKindLinuxPkg installs.
	Packages []string
	// SkipDeps are dependencies of Packages left out on this platform: the
	// helper programs a package brings for uses the device does not have
	// (linuxPkgEnsureSkipping).
	SkipDeps []string
	// Modules are the Ubuntu packages a script tool needs beside its
	// interpreter (Perl or Python modules); on the handheld they come with the
	// interpreter on every Install and Update, and when the tool is opened
	// with one missing.
	Modules []string
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

// auditArchX64 is this machine's architecture as releases that call x86-64
// "x64" spell it (Gitleaks); other architectures keep Go's name.
func auditArchX64() string {
	if runtime.GOARCH == "amd64" {
		return "x64"
	}
	return runtime.GOARCH
}

// secAuditTool is one entry in the audit catalog.
type secAuditTool struct {
	// ID is the stable key used in rows and actions.
	ID string
	// Name is what the row shows.
	Name string
	// Category groups the list: "Web & API", "TLS & SSH", "Network", "Code &
	// dependencies", "Files & firmware" or "Validation".
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
		if r.Kind == auditKindLinuxPkg && !linuxPkgSupported() {
			// "Handheld" is every Linux to the platform layer; Ubuntu's
			// arm64 packages are for the TrimUI only.
			return auditRecipe{Kind: auditKindNone, Reason: t.Name + " installs here through Linux packages, which run on an ARM64 TrimUI handheld only. Use this system's own package manager."}
		}
		return r
	}
	return auditRecipe{Kind: auditKindNone, Reason: "There is no build of " + t.Name + " for this system."}
}

// secAuditCatalog is the list, grouped the way a security review is usually
// organised.
func secAuditCatalog() []secAuditTool {
	// What OSV-Scanner sends, and where; the same on every platform.
	const osvNote = "It looks up the packages it finds in the OSV.dev database: their names and versions leave the device, your code does not. Try osv-scanner scan source -r <folder>."
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
				// Nikto stops at start ("Required module not found") without
				// XML::Writer and JSON. Strawberry Perl ships JSON; cpanm, which
				// it also ships, adds XML::Writer.
				auditPlatformWindows: {Kind: auditKindScript, Needs: "perl", Entry: "program/nikto.pl",
					URL:  "https://github.com/sullo/nikto/archive/refs/heads/master.zip",
					Note: "Nikto needs the Perl modules XML::Writer and JSON. If it stops with \"Required module not found\", run cpanm XML::Writer JSON in a command prompt (Strawberry Perl includes cpanm)."},
				auditPlatformHandheld: {Kind: auditKindScript, Needs: "perl", Entry: "program/nikto.pl",
					URL:     "https://github.com/sullo/nikto/archive/refs/heads/master.zip",
					Modules: []string{"libxml-writer-perl", "libjson-perl"}},
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

		// ── TLS and SSH configuration ──────────────────────────────────────
		{
			ID:           "testssl",
			Name:         "testssl.sh",
			Category:     "TLS & SSH",
			Summary:      "Grades a server's TLS: protocols, ciphers, certificate, known flaws",
			Binaries:     []string{"testssl", "testssl.sh"},
			Version:      []string{"--version"},
			Start:        []string{"--help"},
			Install:      "apt install testssl.sh",
			SendsTraffic: true,
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindManual, Page: "https://testssl.sh/",
					Note: "testssl.sh is a Bash script: on Windows it runs inside WSL, MSYS2 or Cygwin."},
				// Ubuntu installs the script as testssl, with its data (cipher
				// mapping, CA stores) under /usr/share — on the card here, so
				// the Linux packages environment points TESTSSL_INSTALL_DIR at
				// it (linuxPkgEnvScript). Bash is asked for by name: it is
				// Essential, so no package depends on it, and the firmware's
				// shell cannot run the script.
				auditPlatformHandheld: {Kind: auditKindLinuxPkg, Packages: []string{"testssl.sh", "bash"}, Entry: "testssl",
					Note: "A full check takes a few minutes on the handheld. Give the port when it is not 443, as in example.org:8443."},
				auditPlatformLinux: {Kind: auditKindManual, Page: "https://testssl.sh/", Hint: "sudo apt install testssl.sh"},
			},
		},
		{
			ID:           "sslscan",
			Name:         "sslscan",
			Category:     "TLS & SSH",
			Summary:      "Quick list of the TLS protocols and ciphers a server accepts",
			Binaries:     []string{"sslscan"},
			Version:      []string{"--version"},
			Start:        []string{"--help"},
			Install:      "apt install sslscan",
			SendsTraffic: true,
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindManual, Page: "https://github.com/rbsec/sslscan/releases",
					Note: "The project's releases page carries a Windows build, which the project calls experimental."},
				auditPlatformHandheld: {Kind: auditKindLinuxPkg, Packages: []string{"sslscan"}, Entry: "sslscan",
					Note: "For the fuller report (certificate chain, known TLS flaws, client simulations), use testssl.sh."},
				auditPlatformLinux: {Kind: auditKindManual, Page: "https://github.com/rbsec/sslscan", Hint: "sudo apt install sslscan"},
			},
		},
		{
			ID:           "sshaudit",
			Name:         "ssh-audit",
			Category:     "TLS & SSH",
			Summary:      "Checks an SSH server's algorithms and keys against hardening advice",
			Binaries:     []string{"ssh-audit", "ssh-audit.py"},
			Version:      []string{"-h"},
			Start:        []string{"-h"},
			Install:      "pip install ssh-audit",
			SendsTraffic: true,
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindScript, Needs: "python", Entry: "ssh-audit.py",
					URL: "https://github.com/jtesta/ssh-audit/archive/refs/heads/master.zip"},
				auditPlatformHandheld: {Kind: auditKindLinuxPkg, Packages: []string{"ssh-audit"}, Entry: "ssh-audit",
					Note: "Its help screen starts with the version. ssh-audit -c audits an SSH client instead: it listens on port 2222 for the client to connect."},
				auditPlatformLinux: {Kind: auditKindScript, Needs: "python", Entry: "ssh-audit.py",
					URL: "https://github.com/jtesta/ssh-audit/archive/refs/heads/master.zip"},
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
				auditPlatformHandheld: {Kind: auditKindLinuxPkg, Packages: []string{"nmap"}, Entry: "nmap",
					Note: "Ubuntu's Nmap runs as root on the handheld, so SYN scans and OS detection are available. Its scripts and data come with it."},
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
					Reason: "Wireshark is a desktop application, and the handheld has no desktop to show it. tcpdump, in this list, captures on the handheld to a file Wireshark opens on a computer."},
				auditPlatformLinux: {Kind: auditKindManual, Page: "https://www.wireshark.org/download.html", Hint: "sudo apt install wireshark"},
			},
		},
		{
			ID:           "tcpdump",
			Name:         "tcpdump",
			Category:     "Network",
			Summary:      "Records packets on this device's own interfaces to a capture file",
			Binaries:     []string{"tcpdump"},
			Version:      []string{"--version"},
			Start:        []string{"--help"},
			Install:      "apt install tcpdump",
			SendsTraffic: true,
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindNone,
					Reason: "tcpdump is a Unix tool. On Windows, Wireshark (in this list) captures, and its installer brings the dumpcap command too."},
				// Ubuntu builds tcpdump to give up root for a "tcpdump" user,
				// which only its maintainer script would add; -Z root keeps
				// root.
				auditPlatformHandheld: {Kind: auditKindLinuxPkg, Packages: []string{"tcpdump"}, Entry: "tcpdump",
					Note: "Ubuntu's tcpdump switches to a \"tcpdump\" user the handheld does not have, so start captures with -Z root, as in tcpdump -Z root -i wlan0 -w /mnt/SDCARD/capture.pcap. Copy the file to a computer to read it in Wireshark."},
				auditPlatformLinux: {Kind: auditKindManual, Page: "https://www.tcpdump.org/", Hint: "sudo apt install tcpdump"},
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
				// The analysis programs link libhwloc, libnl, libpcap and the
				// crypto libraries, which come along; the package's helper
				// programs for adapter set-up (airmon-ng's ethtool, iw, rfkill,
				// usbutils, wireless-tools), hwloc's lstopo (with cairo, X11
				// and fonts behind it) and Python for its scripts serve nothing
				// on a chip that cannot capture, and are left out.
				auditPlatformHandheld: {Kind: auditKindLinuxPkg, Packages: []string{"aircrack-ng"}, Entry: "aircrack-ng",
					SkipDeps: []string{"ethtool", "hwloc", "iw", "python3", "rfkill", "usbutils", "wireless-tools"},
					Note:     "Analysis only: the handheld's Wi-Fi chip cannot capture in monitor mode, so aircrack-ng works on captures you copy to the card from another device. Its adapter set-up helpers are not installed."},
				auditPlatformLinux: {Kind: auditKindManual, Page: "https://www.aircrack-ng.org/downloads.html", Hint: "sudo apt install aircrack-ng"},
			},
		},

		// ── Code and dependencies ──────────────────────────────────────────
		{
			ID:       "osvscanner",
			Name:     "OSV-Scanner",
			Category: "Code & dependencies",
			Summary:  "Finds known vulnerabilities in a project's dependencies and lockfiles",
			Binaries: []string{"osv-scanner"},
			Version:  []string{"--version"},
			Start:    []string{"--help"},
			Install:  "github.com/google/osv-scanner/releases",
			Recipes: map[auditPlatform]auditRecipe{
				// One static program per platform, listed in the release's
				// osv-scanner_SHA256SUMS (toolboxChecksumAsset).
				auditPlatformWindows: {Kind: auditKindGitHub, Repo: "google/osv-scanner", Entry: "osv-scanner.exe",
					Assets: [][]string{{"osv-scanner", "windows", "amd64", ".exe"}},
					Note:   osvNote},
				auditPlatformHandheld: {Kind: auditKindGitHub, Repo: "google/osv-scanner", Entry: "osv-scanner",
					Assets: [][]string{{"osv-scanner", "linux", "arm64"}},
					Note:   osvNote},
				auditPlatformLinux: {Kind: auditKindGitHub, Repo: "google/osv-scanner", Entry: "osv-scanner",
					Assets: [][]string{{"osv-scanner", "linux", runtime.GOARCH}},
					Note:   osvNote},
			},
		},
		{
			ID:       "gitleaks",
			Name:     "Gitleaks",
			Category: "Code & dependencies",
			Summary:  "Finds passwords, API keys and tokens committed to code or folders",
			Binaries: []string{"gitleaks"},
			Version:  []string{"version"},
			Start:    []string{"--help"},
			Install:  "github.com/gitleaks/gitleaks/releases",
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindGitHub, Repo: "gitleaks/gitleaks", Entry: "gitleaks.exe",
					Assets: [][]string{{"windows", "x64", ".zip"}},
					Note:   "gitleaks dir <folder> scans files; gitleaks git <folder> reads a repository's history and needs Git for Windows."},
				auditPlatformHandheld: {Kind: auditKindGitHub, Repo: "gitleaks/gitleaks", Entry: "gitleaks",
					Assets: [][]string{{"linux", "arm64", ".tar.gz"}},
					Note:   "gitleaks dir <folder> scans files; gitleaks git <folder> reads a repository's history and needs git, from Linux packages."},
				auditPlatformLinux: {Kind: auditKindGitHub, Repo: "gitleaks/gitleaks", Entry: "gitleaks",
					Assets: [][]string{{"linux", auditArchX64(), ".tar.gz"}}},
			},
		},

		// ── Files and firmware ─────────────────────────────────────────────
		{
			ID:       "yara",
			Name:     "YARA",
			Category: "Files & firmware",
			Summary:  "Scans files against malware-detection rules you provide",
			Binaries: []string{"yara", "yara64"},
			Paths: []string{
				`$LOCALAPPDATA\Microsoft\WinGet\Links\yara.exe`,
				`$LOCALAPPDATA\Microsoft\WinGet\Links\yara64.exe`,
			},
			Version: []string{"--version"},
			Start:   []string{"--help"},
			Install: "apt install yara",
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindWinget, WingetID: "VirusTotal.YARA", Page: "https://github.com/VirusTotal/yara/releases",
					Note: "YARA brings no rules: save .yar rule files you trust, then run yara -r rules.yar <folder>. If it does not start, install the Microsoft Visual C++ Redistributable."},
				auditPlatformHandheld: {Kind: auditKindLinuxPkg, Packages: []string{"yara"}, Entry: "yara",
					Note: "YARA brings no rules: copy .yar rule files you trust to the card, then run yara -r rules.yar <folder>."},
				auditPlatformLinux: {Kind: auditKindManual, Page: "https://virustotal.github.io/yara/", Hint: "sudo apt install yara"},
			},
		},
		{
			ID:       "binwalk",
			Name:     "Binwalk",
			Category: "Files & firmware",
			Summary:  "Looks inside firmware images for embedded files and file systems",
			Binaries: []string{"binwalk"},
			Version:  []string{"--help"},
			Start:    []string{"--help"},
			Install:  "apt install binwalk",
			Recipes: map[auditPlatform]auditRecipe{
				auditPlatformWindows: {Kind: auditKindNone,
					Reason: "Binwalk is a Linux tool. On Windows it runs inside WSL (sudo apt install binwalk there)."},
				auditPlatformHandheld: {Kind: auditKindLinuxPkg, Packages: []string{"binwalk"}, Entry: "binwalk",
					Note: "Signature scans (binwalk image.bin) work as installed. Extracting (-e) calls separate unpackers, such as squashfs-tools for SquashFS images, which Linux packages can add."},
				auditPlatformLinux: {Kind: auditKindManual, Page: "https://github.com/ReFirmLabs/binwalk", Hint: "sudo apt install binwalk"},
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
				auditPlatformHandheld: {Kind: auditKindLinuxPkg, Packages: []string{"john"}, Entry: "john",
					Note: "This is Ubuntu's John the Ripper (the core edition, CPU only), not the larger \"jumbo\" build with its many extra hash formats."},
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
		// What Linux packages installed is not on the app's own PATH.
		if path := linuxPkgWrapperPath(name); path != "" {
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
		// Ubuntu's Python from Linux packages (the handheld).
		if p := linuxPkgWrapperPath("python3"); p != "" {
			return []string{p}, true
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
		if p := linuxPkgWrapperPath("perl"); p != "" {
			return []string{p}, true
		}
	}
	return nil, false
}

// auditInterpreterPackages are the Ubuntu packages that give a script tool
// its interpreter on the handheld: Perl with the TLS modules Nikto uses for
// HTTPS targets.
func auditInterpreterPackages(needs string) []string {
	if needs == "perl" {
		return []string{"perl", "libnet-ssleay-perl", "libio-socket-ssl-perl"}
	}
	return []string{"python3"}
}

// auditCanInstallInterpreter reports whether a missing interpreter is
// installed as part of a script tool's install (Linux packages, handheld).
func auditCanInstallInterpreter() bool {
	return auditCurrentPlatform() == auditPlatformHandheld && linuxPkgSupported()
}

// auditScriptPackages are the Ubuntu packages a script tool runs on in the
// handheld: its interpreter's, then its modules.
func auditScriptPackages(r auditRecipe) []string {
	out := append([]string{}, auditInterpreterPackages(r.Needs)...)
	for _, m := range r.Modules {
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

// auditUsesLinuxPkgInterpreter reports whether a script tool runs (or will
// run) on Ubuntu's interpreter from Linux packages: the handheld has no other
// one, or the one found is that wrapper. An interpreter the firmware brings
// gets nothing installed beside it — Ubuntu's modules would not load into it.
// It walks PATH, so it is asked on a goroutine, never on the frame loop.
func auditUsesLinuxPkgInterpreter(needs string) bool {
	if !auditCanInstallInterpreter() {
		return false
	}
	interp, ok := auditInterpreter(needs)
	if !ok {
		return true
	}
	name := "python3"
	if needs == "perl" {
		name = "perl"
	}
	wrapper := linuxPkgWrapperPath(name)
	return wrapper != "" && len(interp) > 0 && interp[0] == wrapper
}

// auditScriptMissing lists the packages a script tool JukaHub installed at
// path still needs on the handheld (its interpreter, or a module added since
// it was installed — Nikto's XML::Writer and JSON); nil when nothing is
// missing or the tool is not one of those. It reads the card: off the frame
// loop.
func auditScriptMissing(t secAuditTool, path string) []string {
	r := t.Recipe(auditCurrentPlatform())
	if r.Kind != auditKindScript || path == "" {
		return nil
	}
	if rec, ok := auditRecordFor(t.ID); !ok || rec.Path != path {
		return nil
	}
	if !auditUsesLinuxPkgInterpreter(r.Needs) {
		return nil
	}
	return linuxPkgMissing(auditScriptPackages(r))
}

// auditSkipDepsFor is the handheld recipe's SkipDeps for an Ubuntu package
// the Audit toolkit installs, so the Linux packages page installs it the same
// way.
func auditSkipDepsFor(pkg string) []string {
	for _, t := range secAuditCatalog() {
		r := t.Recipes[auditPlatformHandheld]
		if r.Kind == auditKindLinuxPkg && len(r.Packages) == 1 && r.Packages[0] == pkg {
			return r.SkipDeps
		}
	}
	return nil
}

// auditLinuxPkgBusy is the error for a Linux packages change already running
// (the daily pass installing Python and Perl, another tool's install, apt in
// the Terminal), asked before the install bar is taken: a second begin would
// cancel the running one's download, and this install would stop on the
// package lock all the same.
func auditLinuxPkgBusy() error {
	if busy := linuxPkgBusyLabel(); busy != "" {
		return fmt.Errorf("Linux packages are busy (%s); try again when that is done", busy)
	}
	return nil
}

// auditRepairScript installs what a JukaHub-installed script tool is missing
// (auditScriptMissing) through Linux packages on the install bar, before the
// tool is opened or asked for its version. It blocks: callers run it on a
// goroutine. report is the last lines Linux packages printed, for a failure's
// sheet.
func auditRepairScript(t secAuditTool, missing []string) (report string, err error) {
	if len(missing) == 0 {
		return "", nil
	}
	if !IsOnline() {
		return "", errToolsOffline
	}
	if err := auditLinuxPkgBusy(); err != nil {
		return "", err
	}
	logInfo("audit", "%s is missing %s; installing through Linux packages before it opens", t.ID, strings.Join(missing, ", "))
	run, ctx := installBarBeginRun(t.Name)
	tail := &linuxPkgTail{}
	if err := linuxPkgEnsure(ctx, missing, false, linuxPkgBarIOTail(run, tail)); err != nil {
		installBarFailRun(run, installCardDetail(err.Error()))
		logWarn("audit", "%s repair failed: %v", t.ID, err)
		return tail.text(), fmt.Errorf("install %s for %s: %w", strings.Join(missing, ", "), t.Name, err)
	}
	auditInterpAutoRemember(missing)
	linuxPkgSummaryForget()
	secAuditForget()
	installBarDoneRun(run)
	return "", nil
}

// auditMissingModuleHint turns a Perl "module not found" in a script tool's
// output into the command that installs it on this system ("" when there is
// none): Nikto's own "Required module not found: XML::Writer" and Perl's
// "Can't locate XML/Writer.pm in @INC".
func auditMissingModuleHint(text string) string {
	var mods []string
	add := func(m string) {
		m = strings.Trim(strings.TrimSpace(m), ".:;,'\"()")
		if m != "" && len(m) <= 80 && !strings.ContainsAny(m, " \t/\\") && !slices.Contains(mods, m) {
			mods = append(mods, m)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if _, rest, ok := strings.Cut(line, "Required module not found"); ok {
			add(strings.TrimLeft(rest, ": "))
		}
		if _, rest, ok := strings.Cut(line, "Can't locate "); ok {
			if f, _, ok := strings.Cut(rest, ".pm"); ok {
				add(strings.ReplaceAll(f, "/", "::"))
			}
		}
	}
	if len(mods) == 0 {
		return ""
	}
	debs := make([]string, len(mods))
	for i, m := range mods {
		// Debian's name for a Perl module's package: XML::Writer is
		// libxml-writer-perl.
		debs[i] = "lib" + strings.ToLower(strings.ReplaceAll(m, "::", "-")) + "-perl"
	}
	switch auditCurrentPlatform() {
	case auditPlatformWindows:
		return "Missing Perl modules: " + strings.Join(mods, ", ") + ". Install them in a command prompt with: cpanm " + strings.Join(mods, " ") + " (Strawberry Perl includes cpanm)."
	case auditPlatformHandheld:
		return "Missing Perl modules: " + strings.Join(mods, ", ") + ". Install them in the Terminal with: apt install -y " + strings.Join(debs, " ")
	}
	return "Missing Perl modules: " + strings.Join(mods, ", ") + ". Install them with: sudo apt install " + strings.Join(debs, " ") + " (or cpanm " + strings.Join(mods, " ") + ")."
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
	cmd := hiddenCommandContext(ctx, path, "--version")
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
	cmd := hiddenCommandContext(ctx, path, args...)
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
		return auditInstallArchive(t, r, false)
	case auditKindLinuxPkg:
		return auditInstallLinuxPkg(t, r, false)
	}
	return "", fmt.Errorf("%s cannot be installed from here", t.Name)
}

// auditLogName is how a failure log line names what was being installed: the
// Ubuntu packages of a Linux packages recipe, else the tool's ID.
func auditLogName(t secAuditTool, r auditRecipe) string {
	if r.Kind == auditKindLinuxPkg && len(r.Packages) > 0 {
		return strings.Join(r.Packages, " ")
	}
	return t.ID
}

// auditInstallLinuxPkg installs a tool from Ubuntu's arm64 packages (Linux
// packages); refresh checks the index first, which is what Update needs. On a
// failure report is the last lines Linux packages printed (what it was
// fetching or unpacking), for the sheet.
func auditInstallLinuxPkg(t secAuditTool, r auditRecipe, refresh bool) (report string, err error) {
	if !IsOnline() {
		return "", errToolsOffline
	}
	if err := auditLinuxPkgBusy(); err != nil {
		return "", err
	}
	run, ctx := installBarBeginRun(t.Name)
	tail := &linuxPkgTail{}
	defer func() {
		if err != nil {
			installBarFailRun(run, installCardDetail(err.Error()))
			report = tail.text()
		}
	}()
	if err := linuxPkgEnsureSkipping(ctx, r.Packages, r.SkipDeps, refresh, linuxPkgBarIOTail(run, tail)); err != nil {
		return "", fmt.Errorf("%s: %w", t.Name, err)
	}
	path := auditLinuxPkgProgram(t, r)
	if path == "" {
		// Installed, but the run that unpacked it ended before its wrapper
		// was written (installing again then changes nothing): write them
		// again from what is installed.
		werr := linuxPkgRebuildRuntime()
		if path = auditLinuxPkgProgram(t, r); path == "" {
			why := linuxPkgProgramMissing(r.Entry)
			if werr != nil {
				why += " (" + werr.Error() + ")"
			}
			return "", fmt.Errorf("%s was installed, but its program %s cannot be started: %s", t.Name, r.Entry, why)
		}
		logInfo("audit", "%s: its program wrappers were missing and are written again", t.ID)
	}
	version := linuxPkgInstalledVersion(r.Packages[0])
	auditSaveRecord(t.ID, auditRecord{Kind: "linuxpkg", Path: path, Version: linuxPkgShortVersion(version), Verified: true,
		At: time.Now().UTC().Format(time.RFC3339)})
	secAuditForget()
	linuxPkgSummaryForget()
	installBarDoneRun(run)
	return t.Name + " " + version + " installed from Ubuntu's arm64 packages.\n\nUbuntu's package index is signed with its archive key, and every download matched the SHA-256 listed there.", nil
}

// auditLinuxPkgProgram is the wrapper of a Linux-packages tool's program: the
// recipe's Entry, else the first of the tool's Binaries that has one (Ubuntu
// names a few programs differently from their projects — testssl.sh is
// testssl there). "" when none is written.
func auditLinuxPkgProgram(t secAuditTool, r auditRecipe) string {
	if p := linuxPkgWrapperPath(r.Entry); p != "" {
		return p
	}
	for _, name := range t.Binaries {
		if p := linuxPkgWrapperPath(name); p != "" {
			return p
		}
	}
	return ""
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
	if r.Kind == auditKindLinuxPkg {
		return auditInstallLinuxPkg(t, r, true)
	}
	if r.Kind == auditKindArchive || r.Kind == auditKindScript {
		// Update refreshes the package index too, so the interpreter and
		// its modules come up to date with the script.
		return auditInstallArchive(t, r, true)
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
	if r.Kind == auditKindLinuxPkg {
		// The tool's own package goes, with what it brought along that
		// nothing else needs. A companion asked for by name beside it
		// (testssl.sh's bash) stays: the Terminal may use it too.
		names := r.Packages
		if len(names) > 1 {
			names = names[:1]
		}
		if err := linuxPkgRemoveNames(names, &linuxPkgIO{}); err != nil {
			return "", fmt.Errorf("remove %s: %w", t.Name, err)
		}
		auditForgetRecord(t.ID)
		linuxPkgSummaryForget()
		return t.Name + " removed.", nil
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
// On the handheld a script tool's interpreter and modules come first from
// Linux packages, on every Install and Update (nothing is fetched when they
// are installed and current); refresh, for Update, checks the package index
// for newer versions first. On a failure there, report is the last lines
// Linux packages printed.
func auditInstallArchive(t secAuditTool, r auditRecipe, refresh bool) (report string, err error) {
	root := auditInstallRoot()
	if root == "" {
		return "", fmt.Errorf("tools folder unavailable")
	}
	ensure := false
	if r.Kind == auditKindScript {
		ensure = auditUsesLinuxPkgInterpreter(r.Needs)
		if _, ok := auditInterpreter(r.Needs); !ok && !ensure {
			return "", fmt.Errorf("%s needs %s, which is not installed", t.Name, auditInterpreterLabel(r.Needs))
		}
	}
	if !IsOnline() {
		return "", errToolsOffline
	}
	if err := os.MkdirAll(root, PermDir); err != nil {
		return "", err
	}
	if ensure {
		if err := auditLinuxPkgBusy(); err != nil {
			return "", err
		}
	}
	run, ctx := installBarBeginRun(t.Name)
	defer func() {
		if err != nil {
			installBarFailRun(run, installCardDetail(err.Error()))
		}
	}()
	if ensure {
		// The handheld's firmware has no Python or Perl: Ubuntu's comes first
		// from Linux packages, with the modules the tool needs, on this same
		// install bar.
		label := auditInterpreterLabel(r.Needs)
		pkgs := auditScriptPackages(r)
		logInfo("audit", "%s runs on %s; making sure of %s through Linux packages", t.ID, label, strings.Join(pkgs, ", "))
		tail := &linuxPkgTail{}
		if err := linuxPkgEnsure(ctx, pkgs, refresh, linuxPkgBarIOTail(run, tail)); err != nil {
			return tail.text(), fmt.Errorf("install %s for %s: %w", label, t.Name, err)
		}
		auditInterpAutoRemember(pkgs)
		linuxPkgSummaryForget()
		if _, ok := auditInterpreter(r.Needs); !ok {
			// Installed by an earlier run that ended before the wrappers.
			_ = linuxPkgRebuildRuntime()
		}
		if _, ok := auditInterpreter(r.Needs); !ok {
			name := "python3"
			if r.Needs == "perl" {
				name = "perl"
			}
			return tail.text(), fmt.Errorf("%s was installed, but it cannot be started: %s", label, linuxPkgProgramMissing(name))
		}
	}
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
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
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
		relPath := filepath.FromSlash(rel)
		if f.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
			if err := root.MkdirAll(relPath, PermDir); err != nil {
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
		if parent := filepath.Dir(relPath); parent != "." {
			if err := root.MkdirAll(parent, PermDir); err != nil {
				return err
			}
		}
		if err := auditWriteZipEntry(ctx, f, root, relPath, size); err != nil {
			return err
		}
	}
	return nil
}

// auditWriteZipEntry writes one member; empty files are fine here (a Python
// package's __init__.py usually is).
func auditWriteZipEntry(ctx context.Context, f *zip.File, root *os.Root, rel string, size int64) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("%w (%s: %v)", errBadToolArchive, f.Name, err)
	}
	defer rc.Close()
	perm := os.FileMode(0o644)
	if f.Mode()&0o111 != 0 {
		perm = PermExec
	}
	out, err := root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
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
		_ = root.Remove(rel)
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
	cmd := hiddenCommand(path)
	cmd.Dir = filepath.Dir(path)
	cmd.Env = helperToolEnv(path)
	startDetached(cmd)
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
	cmd := hiddenCommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = filepath.Dir(path)
	cmd.Env = helperToolEnv(path)
	cmd.WaitDelay = powerScriptWaitDelay
	out, err := cmd.CombinedOutput()
	text := truncatePowerOutput(string(out))
	if hint := auditMissingModuleHint(string(out)); hint != "" {
		text = strings.TrimRight(text, "\n") + "\n\n" + hint
	}
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

