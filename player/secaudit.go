package main

// secaudit.go — the Audit toolkit page: the security tools already installed on
// this machine, and a one-press check that each one runs.
//
// What this page deliberately does not do:
//   - it never downloads or installs anything. The Toolbox installs curated CLI
//     tools from official releases; a pentest framework is a much larger trust
//     decision than that, so the audit page only reports what the user already
//     put on the machine themselves;
//   - it never sends traffic anywhere. Pressing A runs the tool's own version
//     report (or opens the window, for a GUI app), which touches nothing but the
//     local binary. Real runs belong in Power tools → Script snippets, where the
//     command line is visible before it runs;
//   - no command-and-control platform and no autonomous exploitation driver is
//     in the catalog. Those are not tools this app should start on its own.
//
// Detection is purposefully forgiving: PATH first, then the handful of absolute
// paths where the vendor installers put a GUI application. A row that says "not
// installed" is then an honest answer instead of a broken button.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// secAuditTool is one entry in the audit catalog.
type secAuditTool struct {
	// ID is the stable key used in rows and actions.
	ID string
	// Name is what the row shows.
	Name string
	// Category groups the list: "Web & API", "Network" or "Exploitation".
	Category string
	// Summary is one line about what the tool is for.
	Summary string
	// Binaries are looked up on PATH in order; the first hit wins.
	Binaries []string
	// Paths are absolute candidates for installers that do not touch PATH.
	// $VAR and ~ are expanded before the file is tested.
	Paths []string
	// Version is the argument vector for a version report. Empty means the tool
	// is a GUI application: pressing A opens it instead of probing it.
	Version []string
	// Install is the short hint shown when the tool is not installed. It is
	// what the copy action puts on the clipboard, so it is written to be
	// pasted: a package name, or a vendor page.
	Install string
	// SendsTraffic marks a tool whose normal use talks to other hosts. It is
	// shown on the row so the warning is on screen before a real run, even
	// though the check this page runs stays local.
	SendsTraffic bool
}

// GUI reports whether pressing A opens a window instead of running a probe.
func (t secAuditTool) GUI() bool { return len(t.Version) == 0 }

// secAuditCatalog is the list, grouped the way a security review is usually
// organized. Every entry is a tool the user can have installed already; nothing
// here is fetched by JukaHub.
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
				`C:\Program Files\BurpSuiteCommunity\BurpSuiteCommunity.exe`,
				`C:\Program Files\BurpSuite Professional\BurpSuite Professional.exe`,
				"/opt/BurpSuiteCommunity/BurpSuiteCommunity",
			},
			Install:      "portswigger.net/burp/communitydownload",
			SendsTraffic: true,
		},
		{
			ID:       "zap",
			Name:     "OWASP ZAP",
			Category: "Web & API",
			Summary:  "Open-source web app scanner; the free path to a repeatable check",
			Binaries: []string{"zap.sh", "zaproxy", "zap"},
			Paths: []string{
				`C:\Program Files\ZAP\Zed Attack Proxy\zap.bat`,
				`C:\Program Files (x86)\ZAP\Zed Attack Proxy\zap.bat`,
				"/usr/share/zaproxy/zap.sh",
			},
			Install:      "zaproxy.org/download",
			SendsTraffic: true,
		},
		{
			ID:           "sqlmap",
			Name:         "sqlmap",
			Category:     "Web & API",
			Summary:      "Confirms an SQL injection on an endpoint you are allowed to test",
			Binaries:     []string{"sqlmap", "sqlmap.py"},
			Version:      []string{"--version"},
			Install:      "pip install sqlmap",
			SendsTraffic: true,
		},
		{
			ID:           "nikto",
			Name:         "Nikto",
			Category:     "Web & API",
			Summary:      "Fast server scan for risky files and known misconfigurations",
			Binaries:     []string{"nikto", "nikto.pl"},
			Version:      []string{"-Version"},
			Install:      "apt install nikto",
			SendsTraffic: true,
		},

		// ── Network & traffic analysis ─────────────────────────────────────
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
		},
		{
			ID:           "aircrack",
			Name:         "Aircrack-ng",
			Category:     "Network",
			Summary:      "Audit the encryption strength of a Wi-Fi network you operate",
			Binaries:     []string{"aircrack-ng"},
			Version:      []string{"--help"},
			Install:      "apt install aircrack-ng",
			SendsTraffic: true,
		},

		// ── Validation and password auditing ──────────────────────────────
		{
			ID:           "metasploit",
			Name:         "Metasploit Framework",
			Category:     "Validation",
			Summary:      "Turns a suspected weakness into confirmed evidence, on your own host",
			Binaries:     []string{"msfconsole", "msfvenom"},
			Version:      []string{"--version"},
			Install:      "apt install metasploit-framework",
			SendsTraffic: true,
		},
		{
			ID:       "hashcat",
			Name:     "Hashcat",
			Category: "Validation",
			Summary:  "Recovers passwords from hashes you are authorized to audit",
			Binaries: []string{"hashcat"},
			Version:  []string{"--version"},
			Install:  "apt install hashcat",
		},
		{
			ID:       "john",
			Name:     "John the Ripper",
			Category: "Validation",
			Summary:  "Slower than Hashcat, better at odd hash formats and CPU-only hosts",
			Binaries: []string{"john", "john-the-ripper"},
			Version:  []string{"--list=build-info"},
			Install:  "apt install john",
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
	if strings.HasPrefix(p, "~"+string(os.PathSeparator)) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			p = filepath.Join(home, p[2:])
		}
	}
	return p
}

// secAuditResolve finds the installed executable for a tool: PATH first, then
// the absolute installer paths. It returns "" when nothing is installed.
func secAuditResolve(t secAuditTool) (string, bool) {
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
		if path == "" {
			continue
		}
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, true
		}
	}
	return "", false
}

// secAuditCache holds one detection pass for a couple of seconds. The panel
// redraws every frame and LookPath walks every directory on PATH, so resolving
// nine tools per frame would stat the card hundreds of times a second.
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

// secAuditInstalledCount is the number the menu row shows.
func secAuditInstalledCount() int {
	n := 0
	for _, path := range secAuditDetected() {
		if path != "" {
			n++
		}
	}
	return n
}

// secAuditRows is the page: one row per catalog tool, in catalog order.
func secAuditRows() []powerToolRow {
	tools := secAuditCatalog()
	detected := secAuditDetected()
	rows := make([]powerToolRow, 0, len(tools))
	for _, t := range tools {
		value := "Not installed"
		hint := hintLine(FooterHint{"A", "Copy install hint"})
		detail := t.Category + " · " + t.Summary
		if t.SendsTraffic {
			detail += " · talks to other hosts"
		}
		if detected[t.ID] != "" {
			if t.GUI() {
				value = "Installed"
				hint = hintLine(FooterHint{"A", "Open"})
			} else {
				value = "Installed"
				hint = hintLine(FooterHint{"A", "Check"})
			}
		}
		rows = append(rows, powerToolRow{
			label:   t.Name,
			value:   value,
			detail:  detail,
			hint:    hint,
			action:  powerActionSecAuditCheck,
			arg:     t.ID,
			enabled: true,
		})
	}
	return rows
}

// secAuditProbe is what pressing A runs: the tool's own version report for a
// command-line tool, or the GUI application itself. It never passes a target, a
// host or a scan profile, so no traffic leaves the device.
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
	cmd := exec.CommandContext(ctx, path, t.Version...)
	cmd.Dir = filepath.Dir(path)
	cmd.Env = helperToolEnv(path)
	out, err := cmd.CombinedOutput()
	text := truncatePowerOutput(string(out))
	header := fmt.Sprintf("%s\n%s\n%s\n\n", t.Name, path, strings.Repeat("─", 40))
	if ctx.Err() == context.DeadlineExceeded {
		return header + text + fmt.Sprintf("\n(stopped after %s)", timeout), ctx.Err()
	}
	if err != nil && strings.TrimSpace(text) == "" {
		return header + "The tool exited with: " + err.Error(), err
	}
	return header + text, err
}

// secAuditOpen starts a GUI tool and lets it run on its own. The child gets its
// own process group so a launcher window stays usable after JukaHub returns to
// its scene.
func secAuditOpen(path string) (string, error) {
	cmd := exec.Command(path)
	cmd.Dir = filepath.Dir(path)
	cmd.Env = helperToolEnv(path)
	screenChildGroupConfigure(cmd)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go func() { _ = cmd.Wait() }()
	return "Opened " + filepath.Base(path) + "\n\n" + path, nil
}

// secAuditCommandLine is the string the copy action puts on the clipboard: the
// resolved path plus the version arguments, ready to paste into a terminal.
func secAuditCommandLine(t secAuditTool, path string) string {
	args := append([]string{path}, t.Version...)
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		if strings.ContainsAny(a, " \t") {
			quoted = append(quoted, `"`+a+`"`)
			continue
		}
		quoted = append(quoted, a)
	}
	return strings.Join(quoted, " ")
}
