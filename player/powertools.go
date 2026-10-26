package main

// powertools.go — the Developer & power-user tools overlay.
//
// One controller-first panel for jobs that usually need a PC:
//   - a fixed-list file inspector with hex, pretty JSON and log views;
//   - shell snippets saved from the clipboard and run with visible output;
//   - folder sync through rsync (never --delete) and a Syncthing check;
//   - jukauser.json snapshots with a line diff and rollback;
//   - a local read/write benchmark board with an optional online feed.
//
// It is an overlay so it works on every config, including configs with no
// Misc/Tools scene. Long work runs off the render thread; results land in one
// mutex-protected output buffer that the UI copies each frame.
//
// The result viewer is shared: the network probes now live in the Tools panel
// (Tools → FTP & IP), and they show their output here with Back returning to
// Tools instead of to a Power tools page the user never opened.

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
	powerPageFiles
	powerPageScripts
	powerPageSync
	powerPageConfigs
	powerPageBench
	powerPageToolbox
	powerPageSecurity
	powerPageOutput
)

type powerAction int

const (
	powerActionOpenFiles powerAction = iota
	powerActionOpenScripts
	powerActionOpenSync
	powerActionOpenConfigs
	powerActionOpenBench
	powerActionOpenMemory
	powerActionOpenCtrlDiag
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
	powerActionToolboxInstall
	powerActionToolboxRun
	powerActionToolboxArgs
	powerActionToolboxCopy
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
	device     string

	// returnToTools is set while the output viewer shows a probe started from
	// the Tools panel, so Back goes there rather than to a Power tools page.
	returnToTools bool

	confirmKey string
	confirmAt  time.Time
	// authorizeKey/authorizeAt arm the one-time authorized-use acknowledgement
	// for a network-scanning toolbox tool (toolboxAuthorizedUseGate).
	authorizeKey string
	authorizeAt  time.Time

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

func powerVisibleRowLimit() int { return 8 }

// ── Caches ─────────────────────────────────────────────────────────────────

func powerRefreshCaches() {
	powerTools.files = powerKnownFiles()
	powerTools.scripts = allPowerScripts()
	powerTools.snapshots, _ = powerListSnapshots(resolvePath("jukauser.json"))
	powerTools.bench, _ = loadPowerBenchFrom(resolvePath(powerBenchFile))
	powerTools.syncSource = powerSyncSetting(powerSyncSourceKey)
	powerTools.syncTarget = powerSyncSetting(powerSyncTargetKey)
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
			{label: "File inspector", value: "Hex · JSON · logs", detail: "Reads the app's own files only, never a config path", hint: hintLine(FooterHint{"A", "Open"}), action: powerActionOpenFiles, enabled: true},
			{label: "Script snippets", value: fmt.Sprintf("%d snippets", scripts), detail: "Built-ins plus snippets saved from the clipboard", hint: hintLine(FooterHint{"A", "Open"}), action: powerActionOpenScripts, enabled: true},
			{label: "Folder sync", value: target, detail: "rsync without --delete, or check Syncthing", hint: hintLine(FooterHint{"A", "Open"}), action: powerActionOpenSync, enabled: true},
			{label: "Config history", value: fmt.Sprintf("%d snapshots", snaps), detail: "Diff jukauser.json and roll back a bad edit", hint: hintLine(FooterHint{"A", "Open"}), action: powerActionOpenConfigs, enabled: true},
			{label: "Benchmark board", value: device, detail: "Local read/write results, optional online feed", hint: hintLine(FooterHint{"A", "Open"}), action: powerActionOpenBench, enabled: true},
			{label: "Memory scanner", value: memoryMenuValue(), detail: "Scan and edit a running game's RAM, then write a value back", hint: hintLine(FooterHint{"A", "Open"}), action: powerActionOpenMemory, enabled: true},
			{label: "Controller test", value: "Sticks · triggers · buttons", detail: "Raw axis values, drift verdicts and a button check", hint: hintLine(FooterHint{"A", "Open"}), action: powerActionOpenCtrlDiag, enabled: true},
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
		return rows
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
		return rows
	case powerPageSync:
		return []powerToolRow{
			{label: "Source folder", value: powerShortPath(powerTools.syncSource), detail: "Copy a folder path to the clipboard first", hint: hintLine(FooterHint{"A", "Set"}), action: powerActionSetSyncSource, enabled: true},
			{label: "Target", value: powerShortPath(powerTools.syncTarget), detail: "Local folder or user@host:/path", hint: hintLine(FooterHint{"A", "Set"}), action: powerActionSetSyncTarget, enabled: true},
			{label: "Dry run", value: "Preview changes", detail: "rsync -azn --itemize-changes", hint: hintLine(FooterHint{"A", "Preview"}), action: powerActionRsyncDryRun, enabled: true},
			{label: "Sync now", value: "Copy changed files", detail: "No --delete: the target never loses extra files", hint: hintLine(FooterHint{"A", "Sync"}), action: powerActionRsyncNow, enabled: true},
			{label: "Syncthing status", value: "Check localhost:8384", detail: "Reports binary and web UI without an API key", hint: hintLine(FooterHint{"A", "Check"}), action: powerActionSyncthing, enabled: true},
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
		return rows
	case powerPageToolbox:
		// One row per catalog tool. A installs the first time and runs after
		// that, so the same key does the obvious thing without a mode switch.
		tools := toolboxCatalog()
		rows := make([]powerToolRow, 0, len(tools))
		for _, t := range tools {
			value, installed := toolboxRowValue(t)
			if !installed {
				rows = append(rows, powerToolRow{
					label:   t.Name,
					value:   value,
					detail:  toolboxRowDetail(t),
					hint:    hintLine(FooterHint{"A", "Install"}),
					action:  powerActionToolboxInstall,
					arg:     t.ID,
					enabled: true,
				})
				continue
			}
			rows = append(rows, powerToolRow{
				label:   t.Name,
				value:   value,
				detail:  toolboxRowDetail(t) + " · " + t.Licence,
				hint:    hintLine(FooterHint{"A", "Run"}, FooterHint{"X", "Arguments"}, FooterHint{"Y", "Copy Path"}),
				action:  powerActionToolboxRun,
				arg:     t.ID,
				enabled: true,
			})
		}
		return rows
	case powerPageSecurity:
		// Read-only posture lines: the mechanism is the row, and SECURITY.md is
		// the long form. Nothing here needs a button, so none is offered.
		posture := securityPostureRows(config)
		rows := make([]powerToolRow, 0, len(posture))
		for _, s := range posture {
			value := s.Value
			if s.Warn {
				value += " ·"
			}
			rows = append(rows, powerToolRow{
				label:  s.Label,
				value:  value,
				detail: s.Detail,
			})
		}
		return rows
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
				detail:  fmt.Sprintf("%s · write %.1f · read %.1f · %s", e.Kind, e.Write, e.Read, e.At),
				enabled: false,
			})
		}
		return rows
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
	powerTools.returnToTools = false
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
	powerTools.returnToTools = false
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
	if powerTools.returnToTools && (powerTools.page == powerPageToolbox || powerTools.page == powerPageSecurity) {
		// These pages are opened from the Tools panel, so Back returns there
		// instead of to a Power tools menu the user never saw.
		powerTools.returnToTools = false
		powerTools.open = false
		openToolsPanel()
		return
	}
	if powerTools.page == powerPageOutput {
		if powerTools.returnToTools {
			// The probe was started in Tools → FTP & IP, so Back returns there
			// rather than to a Power tools menu the user never opened.
			powerTools.returnToTools = false
			powerTools.open = false
			openToolsPanel()
			return
		}
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

// ── Network probes, run from Tools → FTP & IP ──────────────────────────────
//
// These checks used to be a Power tools page. They are file-transfer and
// network tools, so their rows live in the Tools panel now (Tools → FTP & IP),
// and Power tools no longer has a network page. The results still use the
// shared output viewer, so the hex/JSON toggles and scrolling are the same.

// powerRunFromTools opens the output viewer for a probe started in the Tools
// panel and remembers that Back returns there.
func powerRunFromTools(config *Config, title, working string, fn func() powerAsyncResult) {
	openPowerTools(config)
	powerTools.returnToTools = true
	powerStartAsync(title, working, fn)
}

// powerRunToolFromArgs runs a toolbox tool after its argument line was edited in
// the keyboard. The toolbox page is left behind the output viewer so Back lands
// on the row the user came from.
func powerRunToolFromArgs(id string) {
	t, ok := toolboxFind(id)
	if !ok {
		return
	}
	if toolboxInstalled(t) == "" {
		ShowToast(t.Name+" is not installed", ToastKindWarning)
		return
	}
	if !toolboxAuthorizedUseGate(t, appConfig) {
		return
	}
	if !powerTools.open {
		openPowerTools(appConfig)
		powerTools.returnToTools = true
	}
	powerSetPage(powerPageToolbox)
	args := toolboxArgsFor(id)
	line, _ := toolboxCommandLine(t, args)
	powerStartAsync(t.Name+" · "+line, "Running "+t.Binary+"…", func() powerAsyncResult {
		out, err := toolboxRun(id, args, powerTimeoutScript)
		return powerAsyncResult{text: out, raw: []byte(out), mode: "text", err: err}
	})
	PlayActivateSound()
}

func powerProbeGatewayScan(config *Config) {
	gateway := powerGatewayGuess(toolsIPAddress())
	if gateway == "" {
		ShowToast("No LAN address — connect to Wi-Fi first", ToastKindWarning)
		return
	}
	powerRunFromTools(config, "Gateway port scan", "Scanning "+gateway+"…", func() powerAsyncResult {
		return powerAsyncResult{text: powerScanText(gateway, 400*time.Millisecond)}
	})
}

func powerProbeClipboardScan(config *Config) {
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
	powerRunFromTools(config, "Port scan · "+host, "Scanning "+host+"…", func() powerAsyncResult {
		return powerAsyncResult{text: powerScanText(host, 400*time.Millisecond)}
	})
}

func powerProbePing(config *Config) {
	powerRunFromTools(config, "Ping", "Pinging 1.1.1.1…", func() powerAsyncResult {
		return powerAsyncResult{text: powerPingSummary("1.1.1.1")}
	})
}

func powerProbeTraceroute(config *Config) {
	powerRunFromTools(config, "Traceroute", "Tracing to 1.1.1.1…", func() powerAsyncResult {
		out, err := powerTraceRoute("1.1.1.1")
		return powerAsyncResult{text: out, err: err}
	})
}

func powerProbeDNS(config *Config) {
	powerRunFromTools(config, "DNS lookup", "Resolving…", func() powerAsyncResult {
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
}

func powerProbeWiFiSignal(config *Config) {
	powerRunFromTools(config, "Wi-Fi signal", "Reading the local interface…", func() powerAsyncResult {
		return powerAsyncResult{text: powerWiFiSummary()}
	})
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

// toolboxAuthorizedUseKey is the acknowledgement stored in jukauser.json after
// the first press on a network-scanning tool.
const toolboxAuthorizedUseKey = "networkScanAuthorized"

// toolboxAuthorizedUseAccepted reports whether the user has already answered
// the scanning question on this card.
func toolboxAuthorizedUseAccepted(config *Config) bool {
	if config == nil {
		return false
	}
	v, ok := customValue(config.Variables.Custom, toolboxAuthorizedUseKey)
	return ok && coerceBool(v)
}

// toolboxAcceptAuthorizedUse records the answer once, so the prompt never comes
// back for any scanning tool.
func toolboxAcceptAuthorizedUse(config *Config) {
	if config == nil {
		return
	}
	config.Variables.Custom[toolboxAuthorizedUseKey] = true
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// toolboxAuthorizedUseGate decides whether a run may start. A scanning tool
// asks once: the first press says what the tool does to other hosts, and a
// second press inside five seconds records the acknowledgement and runs — the
// same two-press rule the Forget chip and the Wi-Fi picker use for anything
// that cannot be casually undone. After that the answer is on the card, so
// every later run of every scanning tool goes straight through. Tools that
// only touch this device are never gated.
func toolboxAuthorizedUseGate(t toolboxTool, config *Config) bool {
	if !t.NetworkScan || toolboxAuthorizedUseAccepted(config) {
		return true
	}
	now := time.Now()
	if powerTools.authorizeKey == t.ID && now.Sub(powerTools.authorizeAt) < 5*time.Second {
		powerTools.authorizeKey = ""
		toolboxAcceptAuthorizedUse(config)
		return true
	}
	powerTools.authorizeKey = t.ID
	powerTools.authorizeAt = now
	ShowToast("Only scan networks you own or may test — press again to accept", ToastKindWarning)
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
	case powerActionToolboxInstall:
		t, ok := toolboxFind(row.arg)
		if !ok {
			return
		}
		id := t.ID
		powerStartAsync(t.Name, "Downloading "+t.Name+"…", func() powerAsyncResult {
			if err := toolboxInstall(id, config); err != nil {
				return powerAsyncResult{err: err}
			}
			path := toolboxPath(t)
			return powerAsyncResult{text: t.Name + " installed to " + path + "\n\nPress A to run it, X to type arguments, or use Power tools → Script snippets with " + t.Binary + " on PATH."}
		})
		PlayActivateSound()
	case powerActionToolboxRun:
		t, ok := toolboxFind(row.arg)
		if !ok {
			return
		}
		if toolboxInstalled(t) == "" {
			ShowToast(t.Name+" is not installed", ToastKindWarning)
			return
		}
		if !toolboxAuthorizedUseGate(t, config) {
			return
		}
		id := t.ID
		args := toolboxArgsFor(id)
		line, _ := toolboxCommandLine(t, args)
		powerStartAsync(t.Name+" · "+line, "Running "+t.Binary+"…", func() powerAsyncResult {
			out, err := toolboxRun(id, args, powerTimeoutScript)
			return powerAsyncResult{text: out, raw: []byte(out), mode: "text", err: err}
		})
		PlayActivateSound()
	case powerActionToolboxArgs:
		if _, ok := toolboxFind(row.arg); !ok {
			return
		}
		vkOpenToolArgs(row.arg)
	case powerActionToolboxCopy:
		t, ok := toolboxFind(row.arg)
		if !ok {
			return
		}
		path := toolboxInstalled(t)
		if path == "" {
			ShowToast(t.Name+" is not installed", ToastKindWarning)
			return
		}
		if err := sdl.SetClipboardText(path); err != nil {
			ShowToast("Clipboard unavailable: "+err.Error(), ToastKindError)
			return
		}
		ShowToast(path+" copied", ToastKindSuccess)
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
			if powerTools.returnToTools {
				powerTools.returnToTools = false
				powerTools.open = false
				openToolsPanel()
				return true
			}
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
	case powerPageToolbox:
		return "Toolbox", "Popular CLI tools · installed from curated GitHub releases"
	case powerPageSecurity:
		return "Security & trust", "What this build protects · the long form is SECURITY.md"
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
	return "Power tools", "Files, sync and device diagnostics"
}

// powerFooter lists the page-level actions no row carries: the way back, plus
// a page-wide action such as saving a snippet. A row's own action is announced
// for the focused row only; painting "A Open" on all eight rows turned the list
// into a wall of pills.
func powerFooter(page powerPage) string {
	switch page {
	case powerPageMenu:
		return hintLine(FooterHint{"B", "Close"})
	case powerPageScripts:
		return hintLine(FooterHint{"Y", "Save to Clipboard"}, FooterHint{"B", "Back"})
	case powerPageOutput:
		return hintLine(FooterHint{"Up/Down", "Scroll"}, FooterHint{"X", "Hex"},
			FooterHint{"Y", "JSON"}, FooterHint{"B", "Back"})
	}
	return hintLine(FooterHint{"B", "Back"})
}

// powerFooterFor joins the focused row's own hint with the page-level actions.
func powerFooterFor(page powerPage, rows []powerToolRow, cursor int) string {
	parts := make([]string, 0, 2)
	if cursor >= 0 && cursor < len(rows) {
		if hint := strings.TrimSpace(rows[cursor].hint); hint != "" {
			parts = append(parts, hint)
		}
	}
	if rest := strings.TrimSpace(powerFooter(page)); rest != "" {
		parts = append(parts, rest)
	}
	return strings.Join(parts, "   ")
}

// powerPageSize is the card's size. headerH is the measured title band (the
// shared page header plus the gap under it) rather than a number this function
// guesses, so the card and the bar drawn into it cannot disagree.
func powerPageSize(page powerPage, rowH, headerH int32) (int32, int32) {
	panelW := int32(860)
	if panelW > screenWidth-40 {
		panelW = screenWidth - 40
	}
	footer := int32(36)
	var panelH int32
	if page == powerPageOutput {
		panelH = 640
	} else {
		n := len(powerToolRows(appConfig))
		if n > powerVisibleRowLimit() {
			n = powerVisibleRowLimit()
		}
		// Fit the panel to the rows it can actually show: capping the height
		// afterwards left dead space under the last row, because the renderer
		// had already decided on fewer rows than the size asked for.
		if fit := int((screenHeight - 24 - headerH - footer - 16) / rowH); fit < n {
			n = fit
		}
		if n < 1 {
			n = 1
		}
		panelH = headerH + footer + int32(n)*rowH + 16
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
	// Two lines per row, whatever the page: the tool's name set in the larger
	// face with its value a quiet right-hand note, and the explanation under it.
	// The old menu inverted that — a small name above a large description —
	// which read as a stack of labels rather than a list of tools.
	rowH := 2*rowPad + medH + 2 + smallH
	// The title band is the shared page header, built before the card is sized so
	// the panel and the bar drawn into it measure the same height.
	title, sub := powerPageTitle(powerTools.page)
	if powerTools.page == powerPageOutput && powerOutputIsBusy() {
		sub = "working…"
	}
	header := UIPanelHeader(title, sub, "tools")
	headerH := UIPanelHeaderBand(config, header, 14)
	panelW, panelH := powerPageSize(powerTools.page, rowH, headerH)
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

	header.X = panelX + padX
	header.Y = panelY + UIPanelHeaderInset
	header.W = panelW - 2*padX
	UIPageHeaderDraw(renderer, config, header)

	if powerTools.page == powerPageOutput {
		powerRenderOutput(renderer, config, font, panelX, panelY, panelW, panelH, padX, smallH, headerH)
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
		// Only the selected row is filled. Seven tinted slabs read as a stack
		// of cards; a flat list with one lit row reads as a menu.
		if focused {
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, WithAlpha(focusColor, 42))
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
			fillRoundedRect(renderer, r.X+2, r.Y+10, 3, r.H-20, 2, focusColor)
		}
		labelColor := ColorTextPrimary()
		valueColor := ColorTextSecondary()
		detailColor := ColorTextTertiary()
		if !row.enabled {
			labelColor = ColorTextTertiary()
		} else if focused {
			detailColor = ColorTextSecondary()
		}

		// The value is a note on the right; the label keeps the left edge and
		// as much width as it needs, so a long name is never squeezed by it.
		valueW := int32(0)
		if row.value != "" {
			vw, _, _ := font.SizeUTF8(row.value)
			valueW = int32(vw)
			if maxV := rowW * 2 / 5; valueW > maxV {
				valueW = maxV
			}
		}
		labelW := r.W - 32 - valueW
		if valueW > 0 {
			labelW -= 16
		}
		if labelW < 40 {
			labelW = 40
		}
		renderText(renderer, config, medium, ellipsize(medium, row.label, labelW), labelColor, r.X+16, r.Y+rowPad)
		if valueW > 0 {
			value := ellipsize(font, row.value, valueW)
			vw, _, _ := font.SizeUTF8(value)
			renderText(renderer, config, font, value, valueColor,
				r.X+r.W-16-int32(vw), r.Y+rowPad+(medH-smallH)/2)
		}
		if row.detail != "" {
			detailY := r.Y + rowPad + medH + 2
			if detailY+smallH > r.Y+r.H {
				detailY = r.Y + r.H - smallH
			}
			renderText(renderer, config, font, ellipsize(font, row.detail, r.W-32), detailColor, r.X+16, detailY)
		}
	}

	// A thin scrollbar replaces the ▲/▼ glyphs: it says the same thing without
	// adding two loose marks to the panel.
	if maxScroll := len(rows) - limit; maxScroll > 0 {
		trackX := panelX + panelW - 9
		trackY := panelY + headerH
		trackH := panelH - headerH - footerH - 4
		if trackH > 0 {
			fillRoundedRect(renderer, trackX, trackY, 3, trackH, 2, WithAlpha(ColorBorder, 120))
			thumbH := trackH * int32(limit) / int32(len(rows))
			if thumbH < 24 {
				thumbH = 24
			}
			thumbY := trackY + (trackH-thumbH)*int32(powerTools.scroll)/int32(maxScroll)
			fillRoundedRect(renderer, trackX, thumbY, 3, thumbH, 2, WithAlpha(accentColor, 200))
		}
	}

	// The footer names what the focused row does, so no row needs a pill.
	footer := powerFooterFor(powerTools.page, rows, powerTools.cursor)
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-30)
}

func powerRenderOutput(renderer *sdl.Renderer, config *Config, font *ttf.Font, panelX, panelY, panelW, panelH, padX, lineH, headerH int32) {
	if font == nil {
		return
	}
	_, text, _, _ := powerOutputSnapshot()
	if powerOutputIsBusy() && !strings.HasSuffix(strings.TrimSpace(text), "…") {
		text += "\n\nWorking…"
	}
	areaX := panelX + padX
	areaY := panelY + headerH + 6
	areaW := panelW - 2*padX
	areaH := panelH - headerH - 6 - 42
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
		// Bottom right, beside the footer: the top band is the shared header now,
		// and a count over the subtitle would sit on it.
		renderText(renderer, config, font, label, ColorTextTertiary(), panelX+panelW-padX-int32(lw), panelY+panelH-30)
	}
	footer := powerFooter(powerTools.page)
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-30)
}
