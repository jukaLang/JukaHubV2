package main

// powertools.go — the Developer & power-user tools overlay.
//
// One controller-first panel for jobs that usually need a PC:
//   - network probes (gateway ports, TCP ping, DNS, Wi-Fi signal);
//   - a fixed-list file inspector with hex, pretty JSON and log views;
//   - shell snippets saved from the clipboard and run with visible output;
//   - folder sync through rsync (never --delete) and a Syncthing check;
//   - jukauser.json snapshots with a line diff and rollback;
//   - a local read/write benchmark board with an optional online feed.
//
// It is an overlay so it works on every config, including configs with no
// Misc/Tools scene. Long work runs off the render thread; results land in one
// mutex-protected output buffer that the UI copies each frame.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

type powerPage int

const (
	powerPageMenu powerPage = iota
	powerPageNetwork
	powerPageFiles
	powerPageScripts
	powerPageSync
	powerPageConfigs
	powerPageBench
	powerPageOutput
)

type powerAction int

const (
	powerActionOpenNetwork powerAction = iota
	powerActionOpenFiles
	powerActionOpenScripts
	powerActionOpenSync
	powerActionOpenConfigs
	powerActionOpenBench
	powerActionOpenMemory
	powerActionOpenCtrlDiag
	powerActionScan
	powerActionScanClipboard
	powerActionPing
	powerActionTrace
	powerActionDNS
	powerActionWiFi
	powerActionInspectFile
	powerActionRunScript
	powerActionSaveClipboardScript
	powerActionSetSyncSource
	powerActionSetSyncTarget
	powerActionRsyncDryRun
	powerActionRsyncNow
	powerActionSyncthing
	powerActionSnapshotNow
	powerActionDiffSnapshot
	powerActionRestoreSnapshot
	powerActionRunBenchmark
	powerActionRefreshBenchmark
	powerActionExportBenchmark
	powerActionBack
	powerActionClose
)

type powerToolRow struct {
	label   string
	value   string
	detail  string
	hint    string
	action  powerAction
	arg     string
	enabled bool
}

type powerToolsState struct {
	open       bool
	page       powerPage
	returnPage powerPage
	cursor     int
	scroll     int

	files     []powerFileInfo
	scripts   []powerScript
	snapshots []powerSnapshot
	bench     []powerBenchEntry
	remote    []powerBenchEntry

	syncSource string
	syncTarget string
	gateway    string
	device     string

	confirmKey string
	confirmAt  time.Time

	wrapKey   string
	wrapText  string
	wrapLines []string

	visible int
	busy    int32
	gen     uint64
}

var powerTools powerToolsState

type powerOutputState struct {
	mu   sync.Mutex
	text string
	name string
	raw  []byte
	mode string
}

var powerOutput powerOutputState

// Remote benchmark entries arrive from a goroutine; every read copies the
// slice under this lock. Local entries and the rest of the panel state stay on
// the render thread.
var powerRemoteMu sync.Mutex

func powerRemoteEntries() []powerBenchEntry {
	powerRemoteMu.Lock()
	defer powerRemoteMu.Unlock()
	return append([]powerBenchEntry(nil), powerTools.remote...)
}

// Row rects are filled by the renderer so clicks map to the visible rows.
// Render/event thread only.
var powerRowRects []sdl.Rect
var powerPanelRect sdl.Rect
var powerOutputRect sdl.Rect

func powerRowLines(page powerPage) int32 {
	// The landing page keeps every tool visible with two-line rows; deeper
	// pages get the third detail line and scroll when the font is large.
	if page == powerPageMenu {
		return 2
	}
	return 3
}

func powerVisibleRowLimit() int { return 8 }

// ── Caches ─────────────────────────────────────────────────────────────────

func powerRefreshCaches() {
	powerTools.files = powerKnownFiles()
	powerTools.scripts = allPowerScripts()
	powerTools.snapshots, _ = powerListSnapshots(resolvePath("jukauser.json"))
	powerTools.bench, _ = loadPowerBenchFrom(resolvePath(powerBenchFile))
	powerTools.syncSource = powerSyncSetting(powerSyncSourceKey)
	powerTools.syncTarget = powerSyncSetting(powerSyncTargetKey)
	powerTools.gateway = powerGatewayGuess(localIPv4())
	powerTools.device = deviceDisplayName(DetectDevice())
	if powerTools.syncSource == "" {
		powerTools.syncSource = exeDir()
	}
}

func powerHumanSize(n int64) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%d B", n)
}

func powerShortPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "Not set"
	}
	if len([]rune(p)) > 46 {
		r := []rune(p)
		return string(r[:20]) + "…" + string(r[len(r)-23:])
	}
	return p
}

// ── Rows ───────────────────────────────────────────────────────────────────

func powerToolRows(config *Config) []powerToolRow {
	switch powerTools.page {
	case powerPageMenu:
		target := powerShortPath(powerTools.syncTarget)
		scripts := len(powerTools.scripts)
		snaps := len(powerTools.snapshots)
		device := powerTools.device
		if device == "" {
			device = deviceDisplayName(DetectDevice())
		}
		return []powerToolRow{
			{label: "Network probes", value: "Ports · DNS · Wi-Fi", detail: "Gateway scan, TCP ping, resolver and local signal", hint: hintLine(FooterHint{"A", "Open"}), action: powerActionOpenNetwork, enabled: true},
			{label: "File inspector", value: "Hex · JSON · logs", detail: "Reads the app's own files only, never a config path", hint: hintLine(FooterHint{"A", "Open"}), action: powerActionOpenFiles, enabled: true},
			{label: "Script snippets", value: fmt.Sprintf("%d snippets", scripts), detail: "Built-ins plus snippets saved from the clipboard", hint: hintLine(FooterHint{"A", "Open"}), action: powerActionOpenScripts, enabled: true},
			{label: "Folder sync", value: target, detail: "rsync without --delete, or check Syncthing", hint: hintLine(FooterHint{"A", "Open"}), action: powerActionOpenSync, enabled: true},
			{label: "Config history", value: fmt.Sprintf("%d snapshots", snaps), detail: "Diff jukauser.json and roll back a bad edit", hint: hintLine(FooterHint{"A", "Open"}), action: powerActionOpenConfigs, enabled: true},
			{label: "Benchmark board", value: device, detail: "Local read/write results, optional online feed", hint: hintLine(FooterHint{"A", "Open"}), action: powerActionOpenBench, enabled: true},
			{label: "Memory scanner", value: memoryMenuValue(), detail: "Scan and edit a running game's RAM, then write a value back", hint: hintLine(FooterHint{"A", "Open"}), action: powerActionOpenMemory, enabled: true},
			{label: "Controller test", value: "Sticks · triggers · buttons", detail: "Raw axis values, drift verdicts and a button check", hint: hintLine(FooterHint{"A", "Open"}), action: powerActionOpenCtrlDiag, enabled: true},
			{label: "Close", value: "Back to the app", hint: hintLine(FooterHint{"B", "Close"}), action: powerActionClose, enabled: true},
		}
	case powerPageNetwork:
		gateway := powerTools.gateway
		if gateway == "" {
			gateway = "no LAN address"
		}
		return []powerToolRow{
			{label: "Scan gateway ports", value: gateway, detail: "13 common ports, read-only TCP connects", hint: hintLine(FooterHint{"A", "Scan"}), action: powerActionScan, enabled: gateway != "no LAN address"},
			{label: "Scan clipboard host", value: "Copy an IP or host name", detail: "Same 13 ports, limited to the copied host", hint: hintLine(FooterHint{"A", "Scan"}), action: powerActionScanClipboard, enabled: true},
			{label: "Ping", value: "1.1.1.1", detail: "ICMP if the device has ping, then TCP fallback", hint: hintLine(FooterHint{"A", "Run"}), action: powerActionPing, enabled: true},
			{label: "Traceroute", value: "1.1.1.1", detail: "Uses traceroute/tracert when installed", hint: hintLine(FooterHint{"A", "Trace"}), action: powerActionTrace, enabled: true},
			{label: "DNS lookup", value: "github.com · example.com", detail: "Uses the resolver configured on this device", hint: hintLine(FooterHint{"A", "Run"}), action: powerActionDNS, enabled: true},
			{label: "Wi-Fi signal", value: "Local interface", detail: "Link quality and dBm when associated", hint: hintLine(FooterHint{"A", "Read"}), action: powerActionWiFi, enabled: true},
			{label: "Back", value: "Power tools", hint: "B", action: powerActionBack, enabled: true},
		}
	case powerPageFiles:
		rows := make([]powerToolRow, 0, len(powerTools.files)+1)
		for _, f := range powerTools.files {
			rows = append(rows, powerToolRow{
				label:   f.Name,
				value:   powerHumanSize(f.Size),
				detail:  "Hex, JSON and log views",
				hint:    hintLine(FooterHint{"A", "View"}),
				action:  powerActionInspectFile,
				arg:     f.Path,
				enabled: true,
			})
		}
		if len(rows) == 0 {
			rows = append(rows, powerToolRow{label: "No app files found", value: "Start JukaHub once", detail: "jukauser.json and logs appear after the first save", enabled: false})
		}
		return append(rows, powerToolRow{label: "Back", value: "Power tools", hint: "B", action: powerActionBack, enabled: true})
	case powerPageScripts:
		builtins := powerBuiltinScriptCount()
		rows := make([]powerToolRow, 0, len(powerTools.scripts)+2)
		for i, s := range powerTools.scripts {
			kind := "built-in"
			hint := hintLine(FooterHint{"A", "Run"})
			if i >= builtins {
				kind = "saved"
				hint = hintLine(FooterHint{"A", "Run"}, FooterHint{"X", "Delete"})
			}
			rows = append(rows, powerToolRow{
				label:   s.Name,
				value:   kind,
				detail:  s.Command,
				hint:    hint,
				action:  powerActionRunScript,
				arg:     fmt.Sprintf("%d", i),
				enabled: true,
			})
		}
		rows = append(rows, powerToolRow{
			label:   "Save clipboard as snippet",
			value:   "Clipboard command",
			detail:  "Copy a command first, then save it as a button",
			hint:    hintLine(FooterHint{"A", "Save"}),
			action:  powerActionSaveClipboardScript,
			enabled: true,
		})
		return append(rows, powerToolRow{label: "Back", value: "Power tools", hint: "B", action: powerActionBack, enabled: true})
	case powerPageSync:
		return []powerToolRow{
			{label: "Source folder", value: powerShortPath(powerTools.syncSource), detail: "Copy a folder path to the clipboard first", hint: hintLine(FooterHint{"A", "Set"}), action: powerActionSetSyncSource, enabled: true},
			{label: "Target", value: powerShortPath(powerTools.syncTarget), detail: "Local folder or user@host:/path", hint: hintLine(FooterHint{"A", "Set"}), action: powerActionSetSyncTarget, enabled: true},
			{label: "Dry run", value: "Preview changes", detail: "rsync -azn --itemize-changes", hint: hintLine(FooterHint{"A", "Preview"}), action: powerActionRsyncDryRun, enabled: true},
			{label: "Sync now", value: "Copy changed files", detail: "No --delete: the target never loses extra files", hint: hintLine(FooterHint{"A", "Sync"}), action: powerActionRsyncNow, enabled: true},
			{label: "Syncthing status", value: "Check localhost:8384", detail: "Reports binary and web UI without an API key", hint: hintLine(FooterHint{"A", "Check"}), action: powerActionSyncthing, enabled: true},
			{label: "Back", value: "Power tools", hint: "B", action: powerActionBack, enabled: true},
		}
	case powerPageConfigs:
		rows := []powerToolRow{{label: "Snapshot now", value: "jukauser.json", detail: "A timestamped copy appears in power-config-history", hint: hintLine(FooterHint{"A", "Save"}), action: powerActionSnapshotNow, enabled: true}}
		for i, s := range powerTools.snapshots {
			rows = append(rows, powerToolRow{
				label:   s.ModTime.Format("Jan 2 15:04:05"),
				value:   powerHumanSize(s.Size),
				detail:  s.Name,
				hint:    hintLine(FooterHint{"A", "Diff"}, FooterHint{"X", "Restore"}),
				action:  powerActionDiffSnapshot,
				arg:     fmt.Sprintf("%d", i),
				enabled: true,
			})
		}
		if len(powerTools.snapshots) == 0 {
			rows = append(rows, powerToolRow{label: "No snapshots yet", value: "Saves create them automatically", detail: "Install this build and change a setting once", enabled: false})
		}
		return append(rows, powerToolRow{label: "Back", value: "Power tools", hint: "B", action: powerActionBack, enabled: true})
	case powerPageBench:
		url := powerBenchRemoteURL()
		online := "Set powerBenchURL"
		enabled := url != ""
		if enabled {
			online = "Fetch online board"
		}
		combined := append(append([]powerBenchEntry(nil), powerTools.bench...), powerRemoteEntries()...)
		sortPowerBench(combined)
		model := powerTools.device
		if model == "" {
			model = deviceDisplayName(DetectDevice())
		}
		sameModel, bestSame := 0, 0.0
		for _, e := range combined {
			if strings.EqualFold(e.Device, model) {
				sameModel++
				if s := powerBenchScore(e); s > bestSame {
					bestSame = s
				}
			}
		}
		sameValue := "No results yet"
		sameDetail := "Run the benchmark or refresh the online board"
		if sameModel > 0 {
			sameValue = fmt.Sprintf("%d result(s)", sameModel)
			sameDetail = fmt.Sprintf("Best on this model: %.1f MB/s", bestSame)
		}
		rows := []powerToolRow{
			{label: "Run benchmark", value: "32 MiB read/write", detail: "Records this device in power-bench.json", hint: hintLine(FooterHint{"A", "Run"}), action: powerActionRunBenchmark, enabled: true},
			{label: "Refresh online", value: online, detail: "Optional JSON feed of the same entries", hint: hintLine(FooterHint{"A", "Fetch"}), action: powerActionRefreshBenchmark, enabled: enabled},
			{label: "Copy board", value: "Clipboard JSON", detail: "Share local and fetched results", hint: hintLine(FooterHint{"A", "Copy"}), action: powerActionExportBenchmark, enabled: true},
			{label: "Same model", value: sameValue, detail: sameDetail, enabled: false},
		}
		for i, e := range combined {
			if i >= 12 {
				break
			}
			rows = append(rows, powerToolRow{
				label:   e.Device,
				value:   fmt.Sprintf("%.1f MB/s", powerBenchScore(e)),
				detail:  fmt.Sprintf("write %.1f · read %.1f · %s", e.Write, e.Read, e.At),
				hint:    e.Kind,
				enabled: false,
			})
		}
		return append(rows, powerToolRow{label: "Back", value: "Power tools", hint: "B", action: powerActionBack, enabled: true})
	}
	return nil
}

// ── Open/close ─────────────────────────────────────────────────────────────

func openPowerTools(config *Config) {
	toolsPanel.open = false
	qs.open = false
	communityPanel.open = false
	powerTools.open = true
	powerTools.page = powerPageMenu
	powerTools.returnPage = powerPageMenu
	powerTools.cursor = 0
	powerTools.scroll = 0
	powerTools.confirmKey = ""
	atomic.AddUint64(&powerTools.gen, 1)
	atomic.StoreInt32(&powerTools.busy, 0)
	powerOutput.mu.Lock()
	powerOutput.text = ""
	powerOutput.raw = nil
	powerOutput.mode = ""
	powerOutput.name = ""
	powerOutput.mu.Unlock()
	powerRefreshCaches()
	powerRowRects = nil
	powerPanelRect = sdl.Rect{}
	PlayActivateSound()
}

func closePowerTools() {
	powerTools.open = false
	PlayBackSound()
}

func powerSetPage(page powerPage) {
	powerTools.page = page
	powerTools.cursor = 0
	powerTools.scroll = 0
	powerTools.confirmKey = ""
	if page != powerPageOutput {
		powerRefreshCaches()
	}
}

func powerBack() {
	if powerTools.page == powerPageOutput {
		powerSetPage(powerTools.returnPage)
		PlayBackSound()
		return
	}
	if powerTools.page != powerPageMenu {
		powerSetPage(powerPageMenu)
		PlayBackSound()
		return
	}
	closePowerTools()
}

// ── Output ─────────────────────────────────────────────────────────────────

func powerSetOutput(name, text string, raw []byte, mode string) {
	powerOutput.mu.Lock()
	powerOutput.name = name
	powerOutput.text = text
	powerOutput.raw = raw
	powerOutput.mode = mode
	powerOutput.mu.Unlock()
}

func powerOutputSnapshot() (name, text, mode string, raw []byte) {
	powerOutput.mu.Lock()
	defer powerOutput.mu.Unlock()
	// The slice is replaced, never mutated, so handing it out after the lock is
	// safe and avoids copying a 128 KB file on every frame.
	return powerOutput.name, powerOutput.text, powerOutput.mode, powerOutput.raw
}

type powerAsyncResult struct {
	text string
	raw  []byte
	mode string
	err  error
}

func powerStartAsync(title, working string, fn func() powerAsyncResult) {
	powerTools.returnPage = powerTools.page
	powerSetOutput(title, working, nil, "")
	powerTools.page = powerPageOutput
	powerTools.scroll = 0
	gen := atomic.AddUint64(&powerTools.gen, 1)
	atomic.StoreInt32(&powerTools.busy, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil && atomic.LoadUint64(&powerTools.gen) == gen {
				powerSetOutput(title, "Error: "+fmt.Sprint(r), nil, "")
				atomic.StoreInt32(&powerTools.busy, 0)
			}
		}()
		res := fn()
		if atomic.LoadUint64(&powerTools.gen) != gen {
			return
		}
		text := res.text
		if res.err != nil {
			if strings.TrimSpace(text) == "" {
				text = "Error: " + res.err.Error()
			} else {
				text += "\n\nError: " + res.err.Error()
			}
		}
		if strings.TrimSpace(text) == "" {
			text = "Done."
		}
		powerSetOutput(title, text, res.raw, res.mode)
		atomic.StoreInt32(&powerTools.busy, 0)
	}()
}

func powerOutputIsBusy() bool {
	return atomic.LoadInt32(&powerTools.busy) == 1
}

func powerToggleHex() {
	name, _, mode, raw := powerOutputSnapshot()
	if len(raw) == 0 {
		ShowToast("Hex view is available for files and command output", ToastKindInfo)
		return
	}
	if mode == "hex" {
		text, m := powerPreviewBytes(name, raw)
		powerSetOutput(name, text, raw, m)
		PlayBackSound()
		return
	}
	powerSetOutput(name, powerHexDump(raw, 4096), raw, "hex")
	PlayActivateSound()
}

func powerPrettyOutput() {
	name, _, _, raw := powerOutputSnapshot()
	if len(raw) == 0 {
		ShowToast("Nothing to format", ToastKindInfo)
		return
	}
	pretty, err := powerPrettyJSON(raw)
	if err != nil {
		ShowToast("This output is not JSON", ToastKindInfo)
		return
	}
	powerSetOutput(name, string(pretty), raw, "json")
	PlaySuccessSound()
}

// ── Actions ────────────────────────────────────────────────────────────────

// powerConfirm makes destructive actions (delete, restore) require a second
// press of the same button within five seconds.
func powerConfirm(key string) bool {
	if powerTools.confirmKey == key && time.Since(powerTools.confirmAt) < 5*time.Second {
		powerTools.confirmKey = ""
		return true
	}
	powerTools.confirmKey = key
	powerTools.confirmAt = time.Now()
	ShowToast("Press the same button again to confirm", ToastKindWarning)
	return false
}

func powerClipboardText() (string, error) {
	text, err := sdl.GetClipboardText()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

func powerActivate(config *Config) {
	rows := powerToolRows(config)
	if powerTools.cursor < 0 || powerTools.cursor >= len(rows) {
		return
	}
	row := rows[powerTools.cursor]
	if !row.enabled {
		return
	}
	switch row.action {
	case powerActionOpenNetwork:
		powerSetPage(powerPageNetwork)
		PlayActivateSound()
	case powerActionOpenFiles:
		powerSetPage(powerPageFiles)
		PlayActivateSound()
	case powerActionOpenScripts:
		powerSetPage(powerPageScripts)
		PlayActivateSound()
	case powerActionOpenSync:
		powerSetPage(powerPageSync)
		PlayActivateSound()
	case powerActionOpenConfigs:
		powerSetPage(powerPageConfigs)
		PlayActivateSound()
	case powerActionOpenBench:
		powerSetPage(powerPageBench)
		PlayActivateSound()
	case powerActionOpenMemory:
		// openMemPanel closes Power tools: two full-screen overlays stacked
		// would be unreadable.
		openMemPanel()
	case powerActionOpenCtrlDiag:
		openCtrldiag()
	case powerActionScan:
		gateway := powerTools.gateway
		if gateway == "" {
			ShowToast("No LAN address — connect to Wi-Fi first", ToastKindWarning)
			return
		}
		powerStartAsync("Gateway port scan", "Scanning "+gateway+"…", func() powerAsyncResult {
			return powerAsyncResult{text: powerScanText(gateway, 400*time.Millisecond)}
		})
	case powerActionScanClipboard:
		text, err := powerClipboardText()
		if err != nil || text == "" {
			ShowToast("Clipboard is empty — copy a host or IP first", ToastKindWarning)
			return
		}
		host, err := powerCleanHost(text)
		if err != nil {
			ShowToast(err.Error(), ToastKindWarning)
			return
		}
		powerStartAsync("Port scan · "+host, "Scanning "+host+"…", func() powerAsyncResult {
			return powerAsyncResult{text: powerScanText(host, 400*time.Millisecond)}
		})
	case powerActionPing:
		powerStartAsync("Ping", "Pinging 1.1.1.1…", func() powerAsyncResult {
			return powerAsyncResult{text: powerPingSummary("1.1.1.1")}
		})
	case powerActionTrace:
		powerStartAsync("Traceroute", "Tracing to 1.1.1.1…", func() powerAsyncResult {
			out, err := powerTraceRoute("1.1.1.1")
			return powerAsyncResult{text: out, err: err}
		})
	case powerActionDNS:
		powerStartAsync("DNS lookup", "Resolving…", func() powerAsyncResult {
			var b strings.Builder
			for _, host := range []string{"github.com", "example.com"} {
				ips, err := powerResolve(host)
				if err != nil {
					fmt.Fprintf(&b, "%s: %v\n", host, err)
					continue
				}
				fmt.Fprintf(&b, "%s:\n", host)
				for _, ip := range ips {
					fmt.Fprintf(&b, "  %s\n", ip)
				}
			}
			return powerAsyncResult{text: b.String()}
		})
	case powerActionWiFi:
		powerStartAsync("Wi-Fi signal", "Reading the local interface…", func() powerAsyncResult {
			return powerAsyncResult{text: powerWiFiSummary()}
		})
	case powerActionInspectFile:
		path := row.arg
		powerStartAsync(filepath.Base(path), "Reading "+filepath.Base(path)+"…", func() powerAsyncResult {
			data, err := powerReadFile(path)
			if err != nil {
				return powerAsyncResult{err: err}
			}
			text, mode := powerPreviewBytes(path, data)
			return powerAsyncResult{text: text, raw: data, mode: mode}
		})
	case powerActionRunScript:
		idx := 0
		fmt.Sscanf(row.arg, "%d", &idx)
		scripts := allPowerScripts()
		if idx < 0 || idx >= len(scripts) {
			ShowToast("Snippet not found", ToastKindError)
			return
		}
		script := scripts[idx]
		powerStartAsync(script.Name, "Running: "+script.Command, func() powerAsyncResult {
			out, err := runPowerScript(script.Command, powerTimeoutScript)
			return powerAsyncResult{text: out, raw: []byte(out), mode: "text", err: err}
		})
	case powerActionSaveClipboardScript:
		powerSaveClipboardScript()
	case powerActionSetSyncSource, powerActionSetSyncTarget:
		text, err := powerClipboardText()
		if err != nil || text == "" {
			ShowToast("Clipboard is empty — copy a folder path first", ToastKindWarning)
			return
		}
		key := powerSyncSourceKey
		if row.action == powerActionSetSyncTarget {
			key = powerSyncTargetKey
		}
		if err := setPowerSyncSetting(key, text); err != nil {
			ShowToast(err.Error(), ToastKindError)
			return
		}
		powerRefreshCaches()
		PlaySuccessSound()
		ShowToast("Sync setting saved", ToastKindSuccess)
	case powerActionRsyncDryRun, powerActionRsyncNow:
		dry := row.action == powerActionRsyncDryRun
		source, target := powerTools.syncSource, powerTools.syncTarget
		title := "rsync sync"
		working := "Syncing…"
		if dry {
			title = "rsync dry run"
			working = "Checking what would change…"
		}
		powerStartAsync(title, working, func() powerAsyncResult {
			out, err := runPowerRsync(source, target, dry, powerTimeoutRsync)
			return powerAsyncResult{text: out, err: err}
		})
	case powerActionSyncthing:
		powerStartAsync("Syncthing status", "Checking localhost:8384…", func() powerAsyncResult {
			return powerAsyncResult{text: powerSyncthingStatus()}
		})
	case powerActionSnapshotNow:
		path := resolvePath("jukauser.json")
		if err := snapshotPowerConfig(path); err != nil {
			ShowToast("Snapshot failed: "+err.Error(), ToastKindError)
			return
		}
		powerRefreshCaches()
		PlaySuccessSound()
		ShowToast("Snapshot saved", ToastKindSuccess)
	case powerActionDiffSnapshot:
		idx := 0
		fmt.Sscanf(row.arg, "%d", &idx)
		if idx < 0 || idx >= len(powerTools.snapshots) {
			return
		}
		snap := powerTools.snapshots[idx]
		currentPath := resolvePath("jukauser.json")
		powerStartAsync("Diff · "+snap.Name, "Comparing…", func() powerAsyncResult {
			current, err := os.ReadFile(currentPath)
			if err != nil {
				return powerAsyncResult{err: err}
			}
			old, err := os.ReadFile(snap.Path)
			if err != nil {
				return powerAsyncResult{err: err}
			}
			text := "Snapshot → current\n" + powerDiffLines(string(old), string(current), 400)
			return powerAsyncResult{text: text, mode: "text"}
		})
	case powerActionRestoreSnapshot:
		idx := 0
		fmt.Sscanf(row.arg, "%d", &idx)
		if idx < 0 || idx >= len(powerTools.snapshots) {
			return
		}
		if !powerConfirm(fmt.Sprintf("restore-%d", idx)) {
			return
		}
		snap := powerTools.snapshots[idx]
		path := resolvePath("jukauser.json")
		if err := powerRestoreSnapshot(path, snap.Path); err != nil {
			ShowToast("Restore failed: "+err.Error(), ToastKindError)
			return
		}
		userConfigCache = nil
		powerRefreshCaches()
		PlaySuccessSound()
		ShowToast("Restored. Restart JukaHub to apply it.", ToastKindSuccess)
	case powerActionRunBenchmark:
		version := ""
		if config != nil {
			version = config.Version
		}
		powerStartAsync("Read/write benchmark", "Measuring 32 MiB…", func() powerAsyncResult {
			write, read, err := powerMeasureIO(".jukabench.tmp", 32*1024*1024)
			if err != nil {
				return powerAsyncResult{err: err}
			}
			if err := recordPowerBenchmark("sequential", write, read, version); err != nil {
				return powerAsyncResult{text: fmt.Sprintf("Write: %.1f MB/s\nRead:  %.1f MB/s\n\nCould not save the board: %v", write, read, err)}
			}
			return powerAsyncResult{text: fmt.Sprintf("Write: %.1f MB/s\nRead:  %.1f MB/s\n\nSaved to the local board.", write, read)}
		})
	case powerActionRefreshBenchmark:
		url := powerBenchRemoteURL()
		if url == "" {
			ShowToast("Set powerBenchURL in the config to fetch an online board", ToastKindInfo)
			return
		}
		powerStartAsync("Online benchmark board", "Fetching…", func() powerAsyncResult {
			list, err := fetchPowerBench(url)
			if err != nil {
				return powerAsyncResult{err: err}
			}
			powerRemoteMu.Lock()
			powerTools.remote = list
			powerRemoteMu.Unlock()
			return powerAsyncResult{text: fmt.Sprintf("Fetched %d entries from the online board.\n\nPress B to return to the board.", len(list))}
		})
	case powerActionExportBenchmark:
		list := append(append([]powerBenchEntry(nil), powerTools.bench...), powerRemoteEntries()...)
		sortPowerBench(list)
		data, err := json.MarshalIndent(list, "", "  ")
		if err != nil {
			ShowToast(err.Error(), ToastKindError)
			return
		}
		if err := sdl.SetClipboardText(string(data)); err != nil {
			ShowToast("Could not copy the board", ToastKindError)
			return
		}
		PlaySuccessSound()
		ShowToast("Board copied as JSON", ToastKindSuccess)
	case powerActionBack:
		powerBack()
	case powerActionClose:
		closePowerTools()
	}
}

// ── Input ──────────────────────────────────────────────────────────────────

func powerRowCount(config *Config) int {
	if powerTools.page == powerPageOutput {
		return 0
	}
	return len(powerToolRows(config))
}

func powerMove(delta int) {
	rows := powerToolRows(appConfig)
	if len(rows) == 0 {
		return
	}
	powerTools.cursor += delta
	if powerTools.cursor < 0 {
		powerTools.cursor = 0
	}
	if powerTools.cursor >= len(rows) {
		powerTools.cursor = len(rows) - 1
	}
	limit := powerTools.visible
	if limit < 1 {
		limit = powerVisibleRowLimit()
	}
	if powerTools.cursor < powerTools.scroll {
		powerTools.scroll = powerTools.cursor
	}
	if powerTools.cursor >= powerTools.scroll+limit {
		powerTools.scroll = powerTools.cursor - limit + 1
	}
	if powerTools.scroll > len(rows)-limit {
		powerTools.scroll = len(rows) - limit
	}
	if powerTools.scroll < 0 {
		powerTools.scroll = 0
	}
}

func powerScrollOutput(delta int) {
	powerTools.scroll += delta
	if powerTools.scroll < 0 {
		powerTools.scroll = 0
	}
}

func handlePowerToolsKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !powerTools.open {
		return false
	}
	if powerTools.page == powerPageOutput {
		switch e.Keysym.Sym {
		case sdl.K_UP:
			powerScrollOutput(-1)
		case sdl.K_DOWN:
			powerScrollOutput(1)
		case sdl.K_PAGEUP:
			powerScrollOutput(-10)
		case sdl.K_PAGEDOWN:
			powerScrollOutput(10)
		case sdl.K_HOME:
			powerTools.scroll = 0
		case sdl.K_END:
			powerTools.scroll = 1 << 20
		case sdl.K_x:
			powerToggleHex()
		case sdl.K_y:
			powerPrettyOutput()
		case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE, sdl.K_RETURN:
			powerBack()
		}
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		powerMove(-1)
	case sdl.K_DOWN:
		powerMove(1)
	case sdl.K_PAGEUP:
		powerMove(-powerVisibleRowLimit())
	case sdl.K_PAGEDOWN:
		powerMove(powerVisibleRowLimit())
	case sdl.K_HOME:
		powerTools.cursor = 0
		powerTools.scroll = 0
	case sdl.K_END:
		powerTools.cursor = powerRowCount(config) - 1
		powerMove(0)
	case sdl.K_RETURN, sdl.K_SPACE:
		powerActivate(config)
	case sdl.K_x:
		powerActivateExtra(config, false)
	case sdl.K_y:
		powerActivateExtra(config, true)
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		powerBack()
	}
	return true
}

// powerActivateExtra maps X/Y to the destructive or secondary action on the
// highlighted row. X is the "manage" button, Y the "save" button.
func powerActivateExtra(config *Config, second bool) {
	rows := powerToolRows(config)
	if powerTools.cursor < 0 || powerTools.cursor >= len(rows) {
		return
	}
	row := rows[powerTools.cursor]
	if !row.enabled {
		return
	}
	switch powerTools.page {
	case powerPageScripts:
		if second {
			powerSaveClipboardScript()
			return
		}
		if row.action != powerActionRunScript {
			return
		}
		idx := 0
		fmt.Sscanf(row.arg, "%d", &idx)
		if idx < powerBuiltinScriptCount() {
			ShowToast("Built-in snippets cannot be deleted", ToastKindInfo)
			return
		}
		if !powerConfirm(fmt.Sprintf("delete-%d", idx)) {
			return
		}
		if err := deletePowerScript(idx - powerBuiltinScriptCount()); err != nil {
			ShowToast(err.Error(), ToastKindError)
			return
		}
		powerRefreshCaches()
		if powerTools.cursor >= len(powerToolRows(config)) {
			powerTools.cursor = len(powerToolRows(config)) - 1
		}
		PlayBackSound()
		ShowToast("Snippet deleted", ToastKindInfo)
	case powerPageConfigs:
		if second || row.action != powerActionDiffSnapshot {
			return
		}
		idx := 0
		fmt.Sscanf(row.arg, "%d", &idx)
		if idx < 0 || idx >= len(powerTools.snapshots) {
			return
		}
		if !powerConfirm(fmt.Sprintf("restore-%d", idx)) {
			return
		}
		snap := powerTools.snapshots[idx]
		if err := powerRestoreSnapshot(resolvePath("jukauser.json"), snap.Path); err != nil {
			ShowToast("Restore failed: "+err.Error(), ToastKindError)
			return
		}
		userConfigCache = nil
		powerRefreshCaches()
		PlaySuccessSound()
		ShowToast("Restored. Restart JukaHub to apply it.", ToastKindSuccess)
	}
}

func powerSaveClipboardScript() {
	text, err := powerClipboardText()
	if err != nil || text == "" {
		ShowToast("Clipboard is empty — copy a command first", ToastKindWarning)
		return
	}
	list := loadPowerScripts()
	taken := make(map[string]bool, len(list))
	for _, s := range list {
		taken[strings.ToLower(s.Name)] = true
	}
	n := len(list) + 1
	name := fmt.Sprintf("Snippet %d", n)
	for taken[strings.ToLower(name)] {
		n++
		name = fmt.Sprintf("Snippet %d", n)
	}
	if err := addPowerScript(name, text); err != nil {
		ShowToast(err.Error(), ToastKindError)
		return
	}
	powerRefreshCaches()
	PlaySuccessSound()
	ShowToast("Saved "+name+" as a button", ToastKindSuccess)
}

func ctrlPowerTools(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || !powerTools.open {
		return false
	}
	if powerTools.page == powerPageOutput {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			powerScrollOutput(-1)
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			powerScrollOutput(1)
		case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
			powerScrollOutput(-10)
		case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
			powerScrollOutput(10)
		case sdl.CONTROLLER_BUTTON_X:
			powerToggleHex()
		case sdl.CONTROLLER_BUTTON_Y:
			powerPrettyOutput()
		case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
			powerBack()
		}
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		powerMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		powerMove(1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		powerMove(-powerVisibleRowLimit())
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		powerMove(powerVisibleRowLimit())
	case sdl.CONTROLLER_BUTTON_A:
		powerActivate(config)
	case sdl.CONTROLLER_BUTTON_X:
		powerActivateExtra(config, false)
	case sdl.CONTROLLER_BUTTON_Y:
		powerActivateExtra(config, true)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		powerBack()
	}
	return true
}

func handlePowerToolsMouse(mx, my int32, config *Config) bool {
	if !powerTools.open {
		return false
	}
	if powerTools.page == powerPageOutput {
		if powerPanelRect.W > 0 && !pointInRect(mx, my, powerPanelRect) {
			closePowerTools()
		}
		return true
	}
	for i, r := range powerRowRects {
		if r.W > 0 && pointInRect(mx, my, r) {
			powerTools.cursor = i
			powerActivate(config)
			return true
		}
	}
	if powerPanelRect.W > 0 && !pointInRect(mx, my, powerPanelRect) {
		closePowerTools()
	}
	return true
}

func handlePowerToolsWheel(y int32) bool {
	if !powerTools.open {
		return false
	}
	if powerTools.page == powerPageOutput {
		powerScrollOutput(int(y) * 3)
		return true
	}
	powerMove(int(y))
	return true
}

// ── Render ─────────────────────────────────────────────────────────────────

func powerPageTitle(page powerPage) (string, string) {
	switch page {
	case powerPageNetwork:
		return "Network probes", "Read-only checks on the local network"
	case powerPageFiles:
		return "File inspector", "App files only · hex, pretty JSON and logs"
	case powerPageScripts:
		return "Script snippets", "Runs with JukaHub's permissions — save only trusted commands"
	case powerPageSync:
		return "Folder sync", "rsync never deletes on the target · Syncthing status"
	case powerPageConfigs:
		return "Config history", "A snapshot is kept before each jukauser.json save"
	case powerPageBench:
		return "Benchmark board", "Read/write MB/s for this device"
	case powerPageOutput:
		name, _, mode, _ := powerOutputSnapshot()
		if name == "" {
			name = "Output"
		}
		sub := mode
		if powerOutputIsBusy() {
			sub = "working…"
		} else if sub == "" {
			sub = "result"
		}
		return name, sub
	}
	return "Power tools", "Network, files, sync and device diagnostics"
}

func powerFooter(page powerPage) string {
	switch page {
	case powerPageMenu:
		return hintLine(FooterHint{"A", "Select"}, FooterHint{"B", "Close"})
	case powerPageFiles:
		return hintLine(FooterHint{"A", "View"}, FooterHint{"B", "Back"})
	case powerPageScripts:
		return hintLine(FooterHint{"A", "Run"}, FooterHint{"X", "Delete"},
			FooterHint{"Y", "Save to Clipboard"}, FooterHint{"B", "Back"})
	case powerPageConfigs:
		return hintLine(FooterHint{"A", "Diff"}, FooterHint{"X", "Restore"}, FooterHint{"B", "Back"})
	case powerPageOutput:
		return hintLine(FooterHint{"Up/Down", "Scroll"}, FooterHint{"X", "Hex"},
			FooterHint{"Y", "JSON"}, FooterHint{"B", "Back"})
	}
	return hintLine(FooterHint{"A", "Select"}, FooterHint{"B", "Back"})
}

func powerPageSize(page powerPage, rowH int32) (int32, int32) {
	panelW := int32(860)
	if panelW > screenWidth-40 {
		panelW = screenWidth - 40
	}
	header := int32(86)
	footer := int32(36)
	var panelH int32
	if page == powerPageOutput {
		panelH = 640
	} else {
		n := len(powerToolRows(appConfig))
		if n > powerVisibleRowLimit() {
			n = powerVisibleRowLimit()
		}
		if n < 1 {
			n = 1
		}
		panelH = header + footer + int32(n)*rowH + 8
	}
	if panelH > screenHeight-24 {
		panelH = screenHeight - 24
	}
	return panelW, panelH
}

// powerWrapByMeasure wraps output at maxW using the supplied measurement
// function. It is pure so the wrapping rules can be tested without SDL.
func powerWrapByMeasure(text string, maxW int32, measure func(string) int32) []string {
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		runes := []rune(para)
		if len(runes) == 0 {
			out = append(out, "")
			continue
		}
		for len(runes) > 0 {
			n := len(runes)
			for n > 0 && measure(string(runes[:n])) > maxW {
				n--
			}
			if n == 0 {
				n = 1
			}
			cut := n
			if n < len(runes) {
				for i := n; i > n/2; i-- {
					if runes[i-1] == ' ' || runes[i-1] == '\t' {
						cut = i
						break
					}
				}
			}
			out = append(out, string(runes[:cut]))
			runes = runes[cut:]
			for len(runes) > 0 && (runes[0] == ' ' || runes[0] == '\t') {
				runes = runes[1:]
			}
			if len(out) >= 500 {
				return append(out, "… output view truncated …")
			}
		}
	}
	return out
}

func powerWrapOutput(font *ttf.Font, text string, maxW int32) []string {
	key := fmt.Sprintf("%dx", maxW)
	if powerTools.wrapKey == key && powerTools.wrapText == text && powerTools.wrapLines != nil {
		return powerTools.wrapLines
	}
	powerTools.wrapText = text
	limit := func(s string) int32 {
		w, _, _ := font.SizeUTF8(s)
		return int32(w)
	}
	out := powerWrapByMeasure(text, maxW, limit)
	powerTools.wrapKey = key
	powerTools.wrapLines = out
	return out
}

func renderPowerToolsPanel(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !powerTools.open {
		return
	}
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())
	const rowPad = int32(9)
	rowLines := powerRowLines(powerTools.page)
	var rowH int32
	if rowLines == 3 {
		rowH = 2*rowPad + smallH + 2 + medH + 2 + smallH
	} else {
		rowH = 2*rowPad + smallH + 2 + medH
	}
	panelW, panelH := powerPageSize(powerTools.page, rowH)
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	powerPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	const padX = int32(24)

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	title, sub := powerPageTitle(powerTools.page)
	if powerTools.page == powerPageOutput && powerOutputIsBusy() {
		sub = "working…"
	}
	renderText(renderer, config, medium, ellipsize(medium, title, panelW-2*padX), ColorTextPrimary(), panelX+padX, panelY+18)
	renderText(renderer, config, font, ellipsize(font, sub, panelW-2*padX), ColorTextTertiary(), panelX+padX, panelY+20+medH+4)

	if powerTools.page == powerPageOutput {
		powerRenderOutput(renderer, config, font, panelX, panelY, panelW, panelH, padX, smallH)
		return
	}

	rows := powerToolRows(config)
	if len(rows) == 0 {
		return
	}
	if powerTools.cursor >= len(rows) {
		powerTools.cursor = len(rows) - 1
	}
	if powerTools.cursor < 0 {
		powerTools.cursor = 0
	}
	headerH := int32(86)
	footerH := int32(36)
	limit := int((panelH - headerH - footerH - 8) / rowH)
	if limit > powerVisibleRowLimit() {
		limit = powerVisibleRowLimit()
	}
	if limit < 3 {
		limit = 3
	}
	if limit > len(rows) {
		limit = len(rows)
	}
	powerTools.visible = limit
	if powerTools.cursor < powerTools.scroll {
		powerTools.scroll = powerTools.cursor
	}
	if powerTools.cursor >= powerTools.scroll+limit {
		powerTools.scroll = powerTools.cursor - limit + 1
	}
	if powerTools.scroll > len(rows)-limit {
		powerTools.scroll = len(rows) - limit
	}
	if powerTools.scroll < 0 {
		powerTools.scroll = 0
	}
	focusColor := installHighlight()
	rowW := panelW - 2*padX
	powerRowRects = make([]sdl.Rect, len(rows))
	for i := powerTools.scroll; i < len(rows) && i < powerTools.scroll+limit; i++ {
		row := rows[i]
		y := panelY + headerH + int32(i-powerTools.scroll)*rowH
		r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
		powerRowRects[i] = r
		focused := i == powerTools.cursor
		rowBg := WithAlpha(ColorSurfaceRow, 190)
		if focused {
			rowBg = WithAlpha(focusColor, 46)
		}
		if !row.enabled {
			rowBg = WithAlpha(ColorSurfaceRow, 110)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
		if focused {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
		}
		labelColor := ColorTextSecondary()
		valueColor := ColorTextPrimary()
		hintColor := ColorTextSecondary()
		if !row.enabled {
			labelColor = ColorTextTertiary()
			valueColor = ColorTextTertiary()
			hintColor = ColorTextTertiary()
		} else if focused {
			hintColor = focusColor
		}
		labelY := r.Y + rowPad
		valueY := labelY + smallH + 2
		renderText(renderer, config, font, ellipsize(font, row.label, r.W-190), labelColor, r.X+16, labelY)
		renderText(renderer, config, medium, ellipsize(medium, row.value, r.W-190), valueColor, r.X+16, valueY)
		if rowLines == 3 && row.detail != "" {
			detailY := valueY + medH + 2
			renderText(renderer, config, font, ellipsize(font, row.detail, r.W-16), ColorTextTertiary(), r.X+16, detailY)
		}
		if row.hint != "" {
			hw, _, _ := font.SizeUTF8(row.hint)
			renderText(renderer, config, font, row.hint, hintColor, r.X+r.W-int32(hw)-16, valueY)
		}
	}
	// Scroll markers make a clipped list obvious on a handheld.
	if powerTools.scroll > 0 {
		renderText(renderer, config, font, "▲", ColorTextTertiary(), panelX+panelW-padX-int32(font.Height()), panelY+62)
	}
	if powerTools.scroll+limit < len(rows) {
		renderText(renderer, config, font, "▼", ColorTextTertiary(), panelX+panelW-padX-int32(font.Height()), panelY+panelH-58)
	}
	footer := powerFooter(powerTools.page)
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-30)
}

func powerRenderOutput(renderer *sdl.Renderer, config *Config, font *ttf.Font, panelX, panelY, panelW, panelH, padX, lineH int32) {
	if font == nil {
		return
	}
	_, text, _, _ := powerOutputSnapshot()
	if powerOutputIsBusy() && !strings.HasSuffix(strings.TrimSpace(text), "…") {
		text += "\n\nWorking…"
	}
	areaX := panelX + padX
	areaY := panelY + 78
	areaW := panelW - 2*padX
	areaH := panelH - 78 - 42
	powerOutputRect = sdl.Rect{X: areaX, Y: areaY, W: areaW, H: areaH}
	fillRoundedRect(renderer, areaX-8, areaY-8, areaW+16, areaH+16, 12, WithAlpha(ColorSurfaceRow, 170))
	lines := powerWrapOutput(font, text, areaW-12)
	step := lineH + 2
	if step < 1 {
		step = 1
	}
	maxVisible := int(areaH/step) - 1
	if maxVisible < 1 {
		maxVisible = 1
	}
	maxScroll := len(lines) - maxVisible
	if maxScroll < 0 {
		maxScroll = 0
	}
	if powerTools.scroll > maxScroll {
		powerTools.scroll = maxScroll
	}
	if powerTools.scroll < 0 {
		powerTools.scroll = 0
	}
	end := powerTools.scroll + maxVisible
	if end > len(lines) {
		end = len(lines)
	}
	for i := powerTools.scroll; i < end; i++ {
		line := lines[i]
		color := ColorTextPrimary()
		switch {
		case strings.HasPrefix(line, "+"):
			color = ColorToastSuccess
		case strings.HasPrefix(line, "-"):
			color = ColorToastWarn
		case strings.HasPrefix(line, "…"):
			color = ColorTextTertiary()
		}
		renderText(renderer, config, font, line, color, areaX, areaY+int32(i-powerTools.scroll)*step)
	}
	if maxScroll > 0 {
		label := fmt.Sprintf("%d/%d", powerTools.scroll+1, maxScroll+1)
		lw, _, _ := font.SizeUTF8(label)
		renderText(renderer, config, font, label, ColorTextTertiary(), panelX+panelW-padX-int32(lw), panelY+48)
	}
	footer := powerFooter(powerTools.page)
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-30)
}
