package main

// ports_manage.go — looking after installed ports from the Ports panel.
//
// Y on an installed port opens its options inside the panel: Play, Update
// (when PortMaster's catalogue has the port: a newer build when the md5
// PortMaster recorded differs from the catalogue's, else "download again"),
// "Why didn't it start?" and Uninstall (A twice: it deletes the port's
// folder, saves inside it included). The rows are built once when the
// options open (and again when the catalogue lands), never per frame.
//
// "Why didn't it start?" is the launch-diagnosis card (launch_errors.go) with
// what can be known without running the port: PortMaster missing, a 32-bit
// build, a runtime its port.json names that is not in PortMaster's libs/
// folder, the game files many ports need from the player (gamediag.go's port
// checks), the port's own install note and the last lines of its log.txt —
// or of JukaHub's launch log for it when the script died before writing one.
//
// Uninstall removes what the port's port.json lists in items / items_opt —
// the launcher script and the port's folder — from the ports folders only,
// as harbourmaster's own uninstall does; a name with a path in it, "..", or
// PortMaster itself is never touched.
//
// The PortMaster row says when a newer PortMaster is out (its GitHub release
// against the version file in its folder; both must be readable — the file
// name is unverified on a device); its options then offer Update PortMaster,
// the same download as Repair, so PortMaster's menu is not needed for that
// either.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// portAction is one row of a port's options.
type portAction int

const (
	portActNone portAction = iota
	portActPlay
	portActUpdate
	portActCheck
	portActWhy
	portActUninstall
	// PortMaster's own row (Y on it).
	portActOpenPM
	portActRepairPM
	portActWhyPM
)

const (
	// portUninstallConfirm is how long the second A has to arrive.
	portUninstallConfirm = 3 * time.Second
	// portMasterReleaseAPI is PortMaster's latest release.
	portMasterReleaseAPI = "https://api.github.com/repos/PortsMaster/PortMaster-GUI/releases/latest"
	// portMasterCheckEvery spaces the PortMaster version checks.
	portMasterCheckEvery = 12 * time.Hour
)

// portsLibraryRefresh is refreshGameLibrary, set in init: called directly
// from here it closes a package initialization cycle (the Home's app tiles
// reach launchPortScript, whose reaper rescans the library).
var portsLibraryRefresh = func() {}

func init() { portsLibraryRefresh = refreshGameLibrary }

// portsPanelOpenOptions shows the options of an installed port, or of
// PortMaster itself.
func portsPanelOpenOptions(e portEntry) {
	if e.Launch == "" || e.Install || e.Info {
		return
	}
	if e.Master {
		portsPanel.opts = true
		portsPanel.optsFor = e
		portsPanel.optsBack = portsPanel.cursor
		portsPanel.cursor, portsPanel.scroll = 0, 0
		portsPanel.confirmAt = time.Time{}
		repair := portEntry{Name: "Repair PortMaster", Detail: "Downloads it again (Wi-Fi) and replaces its program files; ports, runtimes and settings stay", Info: true, Action: portActRepairPM}
		if portMasterUpdateNote() != "" {
			// The same download is the update: PortMaster's own self-update runs
			// in its menu, which JukaHub no longer needs to open.
			repair.Name = "Update PortMaster"
			repair.Detail = "Downloads the new release (Wi-Fi) and replaces its program files; ports, runtimes and settings stay"
		}
		portsPanel.optsRows = []portEntry{
			{Name: "Open PortMaster", Detail: "PortMaster's own menu — not needed to install or play ports", Info: true, Action: portActOpenPM},
			repair,
			{Name: "Why didn't it open?", Detail: "Checks Python, bash and its launcher on this firmware, and shows its log", Info: true, Action: portActWhyPM},
		}
		portsPanel.optsSub = setOr(portMasterLocalVersion(), "PortMaster")
		PlayActivateSound()
		return
	}
	portsPanel.opts = true
	portsPanel.optsFor = e
	portsPanel.optsBack = portsPanel.cursor
	portsPanel.cursor, portsPanel.scroll = 0, 0
	portsPanel.confirmAt = time.Time{}
	portsPanelBuildOptions()
	if len(portsPanel.catalogEntries) == 0 && !portsPanel.catalogLoading && IsOnline() {
		portsPanelLoadCatalog() // the Update row appears when it lands
	}
	PlayActivateSound()
}

// portsPanelCloseOptions goes back to the installed list.
func portsPanelCloseOptions() {
	portsPanel.opts = false
	portsPanel.optsRows = nil
	portsPanel.cursor, portsPanel.scroll = portsPanel.optsBack, 0
	PlayBackSound()
}

// portsPanelBuildOptions fills the option rows (main thread).
func portsPanelBuildOptions() {
	e := portsPanel.optsFor
	if e.Master {
		return // PortMaster's own rows do not change with the catalogue
	}
	meta := portMetaFor(e.Launch)
	rows := make([]portEntry, 0, 5)
	play := "Start the port"
	if portCompatBlocked(e.Compat.State) {
		play = "Its manifest says it cannot run here (" + setOr(e.Compat.Note, portCompatBadge(e.Compat.State)) + ")"
	}
	rows = append(rows, portEntry{Name: "Play", Detail: play, Info: true, Action: portActPlay})
	if entry, ok := portCatalogMatch(meta, e.Launch, portsPanel.catalogEntries); ok {
		detail := "Download it again from PortMaster's catalogue; PortMaster reinstalls it"
		if portUpdateAvailable(meta, entry) {
			detail = "A newer build is in PortMaster's catalogue"
		}
		rows = append(rows, portEntry{Name: "Update", Detail: detail, Info: true, Action: portActUpdate, Catalog: entry})
	} else if len(portsPanel.catalogEntries) == 0 {
		detail := "Reads PortMaster's catalogue (Wi-Fi)"
		if portsPanel.catalogLoading {
			detail = "Reading PortMaster's catalogue…"
		}
		rows = append(rows, portEntry{Name: "Check for updates", Detail: detail, Info: true, Action: portActCheck})
	}
	rows = append(rows, portEntry{Name: "Why didn't it start?", Detail: "Its log, what it needs and what is missing", Info: true, Action: portActWhy})
	rows = append(rows, portEntry{Name: "Uninstall", Detail: "Deletes " + filepath.Base(e.Launch) + " and the port's folder, saves inside it too", Info: true, Action: portActUninstall})
	// A rebuild when the catalogue lands adds or drops the Update row: the
	// focus stays on the same action (Check becomes Update), so a row never
	// slides under the cursor — least of all Uninstall.
	if c := portsPanel.cursor; c >= 0 && c < len(portsPanel.optsRows) {
		prev := portsPanel.optsRows[c].Action
		for i, r := range rows {
			if r.Action == prev || (prev == portActCheck && r.Action == portActUpdate) {
				portsPanel.cursor = i
				break
			}
		}
	}
	if portsPanel.cursor >= len(rows) {
		portsPanel.cursor = len(rows) - 1
	}
	portsPanel.optsRows = rows
	portsPanel.optsSub = portOptionsSubtitle(meta)
}

// portOptionsSubtitle is the options' header line: who ported it and what it
// needs.
func portOptionsSubtitle(meta portMeta) string {
	var parts []string
	if len(meta.Porters) > 0 {
		parts = append(parts, "Ported by "+strings.Join(meta.Porters, ", "))
	}
	if len(meta.Runtime) > 0 {
		parts = append(parts, "Needs "+strings.Join(meta.Runtime, ", "))
	}
	if len(parts) == 0 {
		return "Port options"
	}
	return strings.Join(parts, " · ")
}

// portsPanelActivateOption runs the focused option.
func portsPanelActivateOption(e portEntry) {
	port := portsPanel.optsFor
	switch e.Action {
	case portActPlay:
		// Back on the installed list (its row focused) whether or not the
		// port may start: a refused one leaves the panel open.
		portsPanel.opts = false
		portsPanel.optsRows = nil
		portsPanel.cursor, portsPanel.scroll = portsPanel.optsBack, 0
		portsPanelLaunchInstalled(port)
	case portActUpdate:
		if err := portCatalogInstallStart(e.Catalog); err != nil {
			ShowToast(err.Error(), ToastKindWarning)
		}
	case portActCheck:
		if !IsOnline() {
			ShowToast("Connect to Wi-Fi to read PortMaster's catalogue", ToastKindInfo)
			return
		}
		portsPanelLoadCatalog()
		portsPanelBuildOptions()
	case portActWhy:
		PlayActivateSound()
		portWhyRun(port.Name, port.Launch)
	case portActUninstall:
		if portsPanel.confirmAt.IsZero() || time.Since(portsPanel.confirmAt) > portUninstallConfirm {
			portsPanel.confirmAt = time.Now()
			ShowToast("Press A again to delete "+port.Name+" — its saves go too", ToastKindWarning)
			return
		}
		portsPanel.confirmAt = time.Time{}
		portUninstallStart(port.Name, port.Launch)
		portsPanelCloseOptions()
	case portActOpenPM:
		portsPanel.opts = false
		portsPanel.optsRows = nil
		portsPanel.cursor, portsPanel.scroll = portsPanel.optsBack, 0
		portsPanel.open = false
		launchPortScript(portMasterAppName, port.Launch)
	case portActRepairPM:
		if portMasterInstalling() {
			ShowToast("The PortMaster installer is already running", ToastKindInfo)
			return
		}
		if err := startPortMasterRepair(); err != nil {
			ShowToast(firstLine(err.Error()), ToastKindWarning)
			return
		}
		portsPanelCloseOptions()
	case portActWhyPM:
		PlayActivateSound()
		portMasterWhyRun(port.Launch)
	}
}

// portMasterWhyRun checks the firmware again (bash, python3) and shows what
// keeps PortMaster from opening. Main thread; the check runs off it.
func portMasterWhyRun(launcher string) {
	env := portFirmwareEnv(appConfig)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("portmaster", "checking PortMaster failed: %v", r)
			}
		}()
		if pmRuntimeApplies() {
			c := pmRuntimeProbe(env, launcher)
			pmRuntime.mu.Lock()
			if !pmRuntime.running {
				pmRuntime.check = c
			}
			pmRuntime.mu.Unlock()
		}
		gameDiagPost(portMasterWhyReport(launcher))
	}()
}

// portMasterWhyReport is what can be said about PortMaster without opening it.
// Off the main thread.
func portMasterWhyReport(launcher string) gameDiagReport {
	report := gameDiagReport{Title: "PortMaster: why it may not open"}
	if portRealControlFolder() == "" {
		report.Issues = append(report.Issues, launchIssue{
			Level:  int(gameDiagSure),
			Title:  "PortMaster's folder has no control.txt",
			Where:  filepath.Dir(launcher),
			Detail: "The install is incomplete or damaged. Y → Repair PortMaster downloads it again and keeps your ports.",
		})
	}
	if p := portMasterLaunchProblem(launcher); p != "" {
		report.Issues = append(report.Issues, launchIssue{Level: int(gameDiagLikely), Title: "Its launcher needs TRIMUI_EX", Detail: p})
	}
	report.Issues = append(report.Issues, pmRuntimeIssues(true)...)
	// Its splash outlived or covered the menu (portmaster_splash.go).
	report.Issues = append(report.Issues, pmSplashIssue()...)
	if p := portLogPath(portMasterAppName, launcher); p != "" {
		if lines := gameDiagReadTail(p, time.Time{}); len(lines) > 0 {
			report.LogLines, report.LogPath = portLastLines(lines, 12), p
		}
	}
	// PortMaster's launcher sends its output to its own log.txt once it is
	// running: pugwash's Python traceback is there, not in JukaHub's launch
	// log (portmaster_report.go).
	if own := pmOwnLogPath(); own != "" {
		if info, err := os.Stat(own); err == nil && time.Since(info.ModTime()) <= pmReportRecent {
			lines := pmTailLines(own, 12)
			report.Issues = append(report.Issues, pmOwnLogFindings(lines)...)
			if len(lines) > 0 {
				report.LogLines = append(append(report.LogLines, "— PortMaster's log.txt —"), lines...)
				if report.LogPath == "" {
					report.LogPath = own
				}
			}
		}
	}
	switch {
	case len(report.Issues) == 0 && len(report.LogLines) == 0:
		report.Summary = "Python, bash and its launcher are in order, and PortMaster has not written a log yet."
	case len(report.Issues) == 0:
		report.Summary = "Python, bash and its launcher are in order. The last lines of its log are below."
	default:
		report.Summary = "Found " + pluralizeCount(len(report.Issues), "thing", "things") + " to look at:"
	}
	return report
}

// portsPanelLaunchInstalled starts an installed port from the panel, refusing
// a build its own manifest says cannot run here.
func portsPanelLaunchInstalled(e portEntry) {
	if portCompatBlocked(e.Compat.State) && !portCompatIgnored(appConfig) {
		ShowToast(e.Name+" cannot run here: "+setOr(e.Compat.Note, portCompatBadge(e.Compat.State)), ToastKindWarning)
		return
	}
	portsPanel.open = false
	launchPortScript(e.Name, e.Launch)
}

// ── Updates ──────────────────────────────────────────────────────────────

// portCatalogMatch finds an installed port's catalogue entry: by the zip
// name its port.json records, else by its launcher among an entry's items.
func portCatalogMatch(meta portMeta, script string, entries []mcCatalogEntry) (mcCatalogEntry, bool) {
	if meta.Zip != "" {
		for _, e := range entries {
			if strings.EqualFold(e.Zip, meta.Zip) {
				return e, true
			}
		}
	}
	base := filepath.Base(script)
	for _, e := range entries {
		for _, item := range e.Items {
			if strings.EqualFold(filepath.Base(strings.TrimRight(item, "/")), base) {
				return e, true
			}
		}
	}
	return mcCatalogEntry{}, false
}

// portUpdateAvailable: both md5s known and different.
func portUpdateAvailable(meta portMeta, e mcCatalogEntry) bool {
	return meta.MD5 != "" && e.MD5 != "" && !strings.EqualFold(meta.MD5, e.MD5)
}

// ── Why didn't it start? ─────────────────────────────────────────────────

// portWhyRun builds the report on a goroutine and hands it to the main loop
// (gameDiagShowPending opens the card).
func portWhyRun(name, script string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("portmaster", "checking %s failed: %v", name, r)
			}
		}()
		gameDiagPost(portWhyReport(name, script))
	}()
}

// portWhyReport is everything that can be said about a port without running
// it. Off the main thread.
func portWhyReport(name, script string) gameDiagReport {
	report, _ := diagnosePort(name, script, time.Time{})
	meta := portMetaFor(script)
	var issues []launchIssue
	control := portRealControlFolder()
	if control == "" {
		issues = append(issues, launchIssue{
			Level:  int(gameDiagSure),
			Title:  "PortMaster is not installed",
			Detail: "Every port runs on PortMaster's files (control.txt, its runtimes). Install PortMaster from the first row of the Ports panel.",
		})
	}
	issues = append(issues, pmRuntimeIssues(false)...)
	if c := portCompatForLauncher(script); portCompatBlocked(c.State) {
		detail := "Its manifest lists " + setOr(c.Note, "32-bit builds only") + "; this handheld runs 64-bit ARM (aarch64) programs."
		if c.State == portCompatNeedsNewerLibc {
			detail = "Its manifest requires " + c.Note + "; this firmware cannot load this build."
		}
		issues = append(issues, launchIssue{
			Level:  int(gameDiagSure),
			Title:  "This build cannot run on this handheld",
			Detail: detail,
		})
	}
	if control != "" {
		for _, rt := range meta.Runtime {
			if !portRuntimePresent(control, rt) {
				issues = append(issues, launchIssue{
					Level:  int(gameDiagLikely),
					Title:  "A runtime it needs is missing",
					Files:  []string{rt},
					Where:  filepath.Join(control, "libs"),
					Detail: "JukaHub downloads it from PortMaster's catalogue before the port starts — start it with Wi-Fi on, or reinstall the port (Y → Update).",
				})
			}
		}
	}
	// The port's log can name the same cause as the checks above ("This
	// firmware has no full bash"): once is enough.
	have := map[string]bool{}
	for _, is := range issues {
		have[is.Title] = true
	}
	for _, is := range report.Issues {
		if !have[is.Title] {
			have[is.Title] = true
			issues = append(issues, is)
		}
	}
	report.Issues = issues
	if meta.Inst != "" {
		detail := meta.Inst
		if readme := filepath.Join(meta.Dir, "README.md"); meta.Dir != "" && regularFile(readme) {
			detail += " (more in " + readme + ")"
		}
		report.Issues = append(report.Issues, launchIssue{
			Level:  int(gameDiagMaybe),
			Title:  "What this port needs from you",
			Where:  meta.Dir,
			Detail: detail,
		})
	}
	// The folder its game files go in, before one is found missing: the
	// diagnosis names a folder only once a data folder is there and empty or
	// the script checks for a file, and a port with neither leaves the player
	// to guess where a copy goes.
	if meta.Dir != "" {
		haveData := false
		for _, is := range report.Issues {
			if is.Title == "The game's own files are not there yet" {
				haveData = true
				break
			}
		}
		folder, sub := portGameDataFolder(meta.Dir)
		if !haveData && !gameDiagDirHasGameFiles(folder, 2) {
			what := "the port's folder"
			if sub != "" {
				what = "its " + sub + " folder"
			}
			report.Issues = append(report.Issues, launchIssue{
				Level:  int(gameDiagMaybe),
				Title:  "Where its game files go",
				Where:  folder,
				Detail: "This port runs the original game's data. Copy the game's own files into " + what + " on the card (" + gameDiagPortCardPath(folder) + "), keeping the folders they come in, then start the port again.",
			})
		}
	}
	if len(report.LogLines) == 0 {
		if p := portLogPath(name, script); p != "" {
			if lines := gameDiagReadTail(p, time.Time{}); len(lines) > 0 {
				report.LogLines, report.LogPath = portLastLines(lines, 12), p
			}
		}
	}
	report.Title = name + ": why it may not start"
	switch {
	case len(report.Issues) == 0 && len(report.LogLines) == 0:
		report.Summary = "Nothing is missing that JukaHub can check, and the port has not written a log yet."
	case len(report.Issues) == 0:
		report.Summary = "Nothing is missing that JukaHub can check. The last lines of its log are below."
	default:
		report.Summary = "Found " + pluralizeCount(len(report.Issues), "thing", "things") + " to look at:"
	}
	return report
}

// portLastLines is the last n non-empty lines.
func portLastLines(lines []string, n int) []string {
	out := make([]string, 0, n)
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			out = append(out, s)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// portRuntimePresent reports whether PortMaster's libs/ folder holds the
// runtime a port.json names ("mono-6.12.0.122-aarch64.squashfs", or a name
// without the extension).
func portRuntimePresent(control, rt string) bool {
	rt = filepath.Base(strings.TrimSpace(rt))
	if rt == "" || rt == "." {
		return true
	}
	libs := filepath.Join(control, "libs")
	for _, name := range []string{rt, rt + ".squashfs"} {
		if _, ok := gameDiagFileIn(libs, name); ok {
			return true
		}
	}
	return false
}

// ── Uninstall ────────────────────────────────────────────────────────────

// portItemName is a port.json item as a plain name in a ports folder, or ""
// when it is anything else (a path, "..", hidden, PortMaster itself).
func portItemName(item string) string {
	name := strings.TrimSpace(strings.TrimRight(filepath.ToSlash(item), "/"))
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." ||
		strings.HasPrefix(name, ".") || strings.EqualFold(name, "PortMaster") ||
		strings.EqualFold(name, "PortMaster.sh") || strings.EqualFold(name, "Imgs") {
		return ""
	}
	return name
}

// portUninstallTargets lists what removing the port deletes: the script,
// then each item of its manifest found directly in a ports folder (roots),
// and its data folder when that sits directly in one. Pure apart from
// existence checks.
func portUninstallTargets(script string, meta portMeta, roots []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, root := range roots {
		// A ports folder itself is never a target.
		seen[strings.ToLower(filepath.Clean(root))] = true
	}
	seen[strings.ToLower(filepath.Clean(filepath.Dir(script)))] = true
	add := func(p string) {
		clean := filepath.Clean(p)
		key := strings.ToLower(clean)
		if !seen[key] {
			seen[key] = true
			out = append(out, clean)
		}
	}
	add(script)
	all := append([]string{filepath.Dir(script)}, roots...)
	for _, item := range append(append([]string{}, meta.Items...), meta.ItemsOpt...) {
		name := portItemName(item)
		if name == "" {
			continue
		}
		for _, root := range all {
			p := filepath.Join(root, name)
			if _, err := os.Lstat(p); err == nil {
				add(p)
			}
		}
	}
	if meta.Dir != "" && portItemName(filepath.Base(meta.Dir)) != "" {
		for _, root := range all {
			if strings.EqualFold(filepath.Clean(filepath.Dir(meta.Dir)), filepath.Clean(root)) {
				add(meta.Dir)
				break
			}
		}
	}
	return out
}

var portUninstallBusy sync.Mutex

// portUninstallStart deletes a port on a goroutine and refreshes the lists.
func portUninstallStart(name, script string) {
	if !portUninstallBusy.TryLock() {
		ShowToast("Another uninstall is still running", ToastKindInfo)
		return
	}
	meta := portMetaFor(script)
	targets := portUninstallTargets(script, meta, portRoots(portMasterSDRoot))
	ShowToast("Removing "+name+"…", ToastKindInfo)
	go func() {
		defer portUninstallBusy.Unlock()
		var failed []string
		for _, p := range targets {
			logInfo("portmaster", "uninstall %s: removing %s", name, p)
			if err := os.RemoveAll(p); err != nil {
				failed = append(failed, filepath.Base(p))
				logWarn("portmaster", "uninstall %s: %v", name, err)
			}
		}
		tbxPost(func() {
			portMetaForget()
			invalidatePortsCache()
			invalidateEmulatorCache()
			if gameLib.open {
				portsLibraryRefresh()
			}
			if len(failed) > 0 {
				ShowToast("Could not remove "+strings.Join(failed, ", ")+" from "+name, ToastKindError)
				return
			}
			ShowToast(name+" removed", ToastKindSuccess)
		})
	}()
}

// ── After a port or PortMaster ends ──────────────────────────────────────

// portMasterQuickExit is how soon a PortMaster that closes is said to have
// closed at once.
const portMasterQuickExit = 5 * time.Second

// portAfterExit runs on the reaper goroutine once the port's group is gone:
// the helpers it left are ended, and after PortMaster itself the lists are
// read again (it may have installed, updated or removed ports). A PortMaster
// that closed at once says why, when that can be known, and so does one whose
// splash JukaHub had to end (splash: pmSplashOrphaned or pmSplashGaveUp,
// portmaster_splash.go).
func portAfterExit(script string, master bool, ran time.Duration, splash int) {
	if labels := portCleanupLeftovers(); len(labels) > 0 {
		logInfo("portmaster", "ended %d leftover helper(s)", len(labels))
	}
	quick := ""
	if master && ran < portMasterQuickExit+screenSettleDelay {
		quick = "PortMaster closed at once — its output is in gamediag/ports/PortMaster.log"
		if p := portMasterLaunchProblem(script); p != "" {
			quick = p
		} else if p := pmRuntimeQuickExit(); p != "" {
			quick = p
		} else if p := pmOwnLogQuickExit(); p != "" {
			quick = p
		}
		logWarn("portmaster", "PortMaster ended after %s: %s", ran.Round(100*time.Millisecond), quick)
	}
	if master && (quick == "" || splash == pmSplashGaveUp) {
		if p := pmSplashExitToast(splash); p != "" {
			quick = p
			logWarn("portmaster", "PortMaster ended after %s: %s", ran.Round(100*time.Millisecond), quick)
		}
	}
	tbxPost(func() {
		if quick != "" {
			ShowToast(quick, ToastKindWarning)
		}
		portMetaForget()
		invalidatePortsCache()
		if !master {
			return
		}
		invalidateEmulatorCache()
		portMasterVersionForget()
		if portsPanel.open && len(portsPanel.catalogEntries) > 0 {
			portsPanelRebuildCatalogRows()
		}
		if gameLib.open {
			portsLibraryRefresh()
		}
	})
}

// ── PortMaster's own version ─────────────────────────────────────────────

var portMasterVersion struct {
	mu      sync.Mutex
	at      time.Time
	running bool
	local   string
	latest  string
}

// portMasterVersionForget makes the next panel open check again.
func portMasterVersionForget() {
	portMasterVersion.mu.Lock()
	portMasterVersion.at = time.Time{}
	portMasterVersion.mu.Unlock()
}

// portMasterLocalVersion is the first line of PortMaster's version file, or
// "".
func portMasterLocalVersion() string {
	control := portRealControlFolder()
	if control == "" {
		return ""
	}
	raw, ok := portReadSmall(filepath.Join(control, "version"))
	if !ok {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(raw)), "\n")
	return strings.TrimSpace(line)
}

// portMasterVersionCheck starts a background check, at most every
// portMasterCheckEvery; the panel's PortMaster row picks up the answer.
func portMasterVersionCheck() {
	if !portsLibraryEnabled() || !IsOnline() {
		return
	}
	portMasterVersion.mu.Lock()
	if portMasterVersion.running || (!portMasterVersion.at.IsZero() && time.Since(portMasterVersion.at) < portMasterCheckEvery) {
		portMasterVersion.mu.Unlock()
		return
	}
	portMasterVersion.running = true
	portMasterVersion.mu.Unlock()
	go func() {
		local := portMasterLocalVersion()
		latest := ""
		if local != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			tag, err := portMasterLatestTag(ctx)
			cancel()
			if err != nil {
				logDebug("portmaster", "version check: %v", err)
			}
			latest = tag
		}
		portMasterVersion.mu.Lock()
		portMasterVersion.running = false
		portMasterVersion.at = time.Now()
		portMasterVersion.local, portMasterVersion.latest = local, latest
		portMasterVersion.mu.Unlock()
		if latest != "" {
			tbxPost(invalidatePortsCache)
		}
	}()
}

// portMasterUpdateNote is the PortMaster row's addition when a newer
// release is out, else "".
func portMasterUpdateNote() string {
	portMasterVersion.mu.Lock()
	local, latest := portMasterVersion.local, portMasterVersion.latest
	portMasterVersion.mu.Unlock()
	if local == "" || latest == "" || !portVersionNewer(latest, local) {
		return ""
	}
	// The release the update pass installed (its tag and the version file
	// may be written differently, portmaster_autoupdate.go).
	if emuRuntimeSameBuild(emuRuntimeRecordFor(pmAutoKey).Installed, latest, false) {
		return ""
	}
	if emuCoreUpdateModeNow() == coreUpdateInstall {
		// The daily Emulator updates pass brings it forward by itself
		// (portmaster_autoupdate.go).
		return "PortMaster " + latest + " is out (you have " + local + ") — it updates by itself at the next quiet moment, or " +
			hintKeyFor("Y", "Options") + " → Update PortMaster now"
	}
	return "PortMaster " + latest + " is out (you have " + local + ") — " + hintKeyFor("Y", "Options") + " → Update PortMaster"
}

// portMasterLatestTag reads the tag of PortMaster's latest release.
func portMasterLatestTag(ctx context.Context) (string, error) {
	return portMasterLatestTagWith(ctx, &http.Client{Timeout: 20 * time.Second})
}

// portMasterLatestTagWith is portMasterLatestTag on client (the automatic
// update pass hands its own, which shares GitHub answers and holds).
func portMasterLatestTagWith(ctx context.Context, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, portMasterReleaseAPI, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "JukaHub-Ports/1.0")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub answered %d", resp.StatusCode)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return "", err
	}
	return strings.TrimSpace(rel.TagName), nil
}

// portVersionNewer reports whether version a is newer than b, comparing the
// numbers in them in order ("2026.10.18-1234" > "2026.9.30"). Equal or
// unreadable is not newer. Pure.
func portVersionNewer(a, b string) bool {
	na, nb := portVersionNumbers(a), portVersionNumbers(b)
	if len(na) == 0 || len(nb) == 0 {
		return false
	}
	for i := 0; i < len(na) && i < len(nb); i++ {
		if na[i] != nb[i] {
			return na[i] > nb[i]
		}
	}
	return len(na) > len(nb)
}

// portVersionNumbers is the runs of digits in v, in order.
func portVersionNumbers(v string) []int {
	var out []int
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r < '0' || r > '9' }) {
		if n, err := strconv.Atoi(f); err == nil {
			out = append(out, n)
		}
	}
	return out
}
