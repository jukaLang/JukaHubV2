package main

import (
	"log"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ──────────────────────────────────────────────────────────────────────
// Network connectivity monitor
// ──────────────────────────────────────────────────────────────────────

// netMonitor tracks whether the device currently has internet access. It
// publishes the result to the "network_status" custom variable so any
// textlog/textbrowser element or the status bar can display it, and shows a
// toast when connectivity changes so the user knows why requests are failing.
type netMonitor struct {
	mu        sync.RWMutex
	online    bool
	checked   bool
	lastProbe time.Time
	reason    string
}

var netMon = &netMonitor{}

// IsOnline returns whether the last connectivity probe succeeded. Before the
// first probe completes it returns true (optimistic) so the app doesn't
// show an offline banner during startup.
func IsOnline() bool {
	netMon.mu.RLock()
	defer netMon.mu.RUnlock()
	if !netMon.checked {
		return true
	}
	return netMon.online
}

// NetworkStatus returns the current status string for UI display.
func NetworkStatus() string {
	netMon.mu.RLock()
	defer netMon.mu.RUnlock()
	if !netMon.checked {
		return "checking..."
	}
	if netMon.online {
		return "online"
	}
	if netMon.reason != "" {
		return "offline (" + netMon.reason + ")"
	}
	return "offline"
}

// probeConnectivity performs one connectivity check. It prefers a lightweight
// TCP dial to a well-known host (no DNS dependency), falling back to an ICMP
// ping when available.
func probeConnectivity() bool {
	// Fast path: TCP dial to a public resolver. 3-second timeout keeps the
	// probe from blocking the render loop for long.
	conn, err := net.DialTimeout("tcp", "1.1.1.1:80", 3*time.Second)
	if err == nil {
		conn.Close()
		return true
	}
	// Fallback: try Google's DNS over UDP with a short timeout.
	conn, err = net.DialTimeout("tcp", "8.8.8.8:53", 3*time.Second)
	if err == nil {
		conn.Close()
		return true
	}
	// Last resort: ping the gateway (Linux only, avoids needing root).
	if runtime.GOOS == "linux" {
		if out, err := exec.Command("ping", "-c", "1", "-W", "2", "1.1.1.1").Output(); err == nil {
			if strings.Contains(string(out), "1 received") || strings.Contains(string(out), "1 packets received") {
				return true
			}
		}
	}
	return false
}

// update publishes the current connectivity state and shows a toast on change.
func (m *netMonitor) update() {
	online := probeConnectivity()

	m.mu.Lock()
	firstCheck := !m.checked
	changed := m.checked && online != m.online
	m.online = online
	m.checked = true
	m.lastProbe = time.Now()
	m.reason = ""
	if !online {
		m.reason = "no route"
	}
	m.mu.Unlock()

	// Publish for UI elements (textlog/textbrowser display this variable).
	publishCustom("network_status", NetworkStatus())
	// Refresh the LAN address so the Tools panel and config text elements
	// follow DHCP/WiFi changes without a restart.
	publishCustom("local_ip", localIPv4())
	if ftpSrv.Running() {
		publishCustom("ftp_address", ftpSrv.Address())
	}

	switch {
	case changed && online:
		showToast("Network restored", ToastSuccess())
		log.Printf("[net] connectivity restored")
		// Tools that could not be installed while offline are fetched now, so
		// the user does not have to restart the app after joining WiFi.
		go installMissingToolsAfterReconnect()
	case changed:
		showToast("Offline — connect WiFi to play videos and install tools", ToastWarn())
		log.Printf("[net] connectivity lost")
	case firstCheck && !online:
		// Launched before joining WiFi: say why searches, playback and tool
		// installs will fail instead of leaving the user with a bare error.
		showToast("Offline — connect WiFi to play videos and install tools", ToastWarn())
		log.Printf("[net] started offline")
	}
}

// startNetworkMonitor begins periodic connectivity checks. It runs one probe
// immediately, then every 20s. On TSP this is cheap (TCP dial) and stops
// requests from piling up when the WiFi drops.
func startNetworkMonitor() {
	go func() {
		// First probe slightly delayed so startup isn't blocked.
		time.Sleep(1 * time.Second)
		netMon.update()
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			netMon.update()
		}
	}()
}

// ──────────────────────────────────────────────────────────────────────
// Tool health check
// ──────────────────────────────────────────────────────────────────────

// toolHealth describes the availability of required external tools.
type toolHealth struct {
	mu       sync.Mutex
	status   map[string]string // tool name -> "ok" | "missing" | path
	failures map[string]string // tool name -> last install error (short)
}

var toolHealthState = &toolHealth{
	status:   make(map[string]string),
	failures: make(map[string]string),
}

// toolsAutoAttempted limits each tool's auto-download to one attempt per app
// run so offline launches stay quiet instead of retry-looping. A failed
// install can still be retried explicitly from the install card (A).
var toolsAutoAttempted = make(map[string]bool)

// autoInstallTools are the tools JukaHub can fetch by itself when missing.
var autoInstallTools = []string{"yt-dlp", "ffplay", "ffmpeg", "ffprobe"}

// checkToolPresence reports whether the given tool is usable in the required/
// folder or on PATH. Only the file is inspected: JukaHub never runs a tool to
// decide whether it is installed (a version probe used to delete good
// downloads on the TrimUI when yt-dlp could not unpack itself).
func checkToolPresence(tool string) (string, bool) {
	usable := func(p string) bool {
		info, err := os.Stat(p)
		return err == nil && info.Mode().IsRegular() && info.Size() > 0
	}
	// Local bundled copy first.
	if p, _ := P().LookPath(tool); p != "" && usable(p) {
		return p, true
	}
	// System PATH.
	if p, err := exec.LookPath(tool); err == nil && usable(p) {
		return p, true
	}
	return "", false
}

// toolUnavailableMessage explains a missing helper tool in one line. Offline
// launches happen on the TrimUI before WiFi is joined, so that case gets its
// own wording instead of a generic exec error.
func toolUnavailableMessage(tool string, err error) string {
	if !IsOnline() {
		return "Offline — connect WiFi to install " + tool
	}
	if err != nil {
		return "Could not install " + tool + " — press A to retry"
	}
	return tool + " is required and could not be installed — press A to retry"
}

// installMissingToolsAfterReconnect clears the one-shot auto-install guard for
// tools that are still missing, so connecting WiFi after an offline launch
// finishes the setup without an app restart.
func installMissingToolsAfterReconnect() {
	toolHealthState.mu.Lock()
	for _, t := range autoInstallTools {
		delete(toolsAutoAttempted, t)
	}
	toolHealthState.mu.Unlock()
	refreshToolHealth()
}

// refreshToolHealth re-checks the required tools and publishes a summary
// variable plus per-tool status entries.
func refreshToolHealth() {
	tools := []string{"yt-dlp", "ffplay", "ffmpeg", "ffprobe"}
	toolHealthState.mu.Lock()
	statuses := make(map[string]string, len(tools))
	var missing []string
	for _, t := range tools {
		path, ok := checkToolPresence(t)
		if ok {
			statuses[t] = path
			delete(toolHealthState.failures, t)
		} else {
			statuses[t] = "missing"
			missing = append(missing, t)
		}
	}
	toolHealthState.status = statuses
	summary := "All tools available"
	if len(missing) > 0 {
		summary = "Missing: " + strings.Join(missing, ", ")
	}
	// Auto-provision any missing tool JukaHub can fetch itself (yt-dlp,
	// ffplay, ffmpeg). One attempt per tool per app run; installs run
	// sequentially so the install bar shows one download at a time.
	var toFetch []string
	for _, t := range autoInstallTools {
		if statuses[t] == "missing" && !toolsAutoAttempted[t] {
			toolsAutoAttempted[t] = true
			toFetch = append(toFetch, t)
		}
	}
	failures := make(map[string]string, len(toolHealthState.failures))
	for t, reason := range toolHealthState.failures {
		failures[t] = reason
	}
	toolHealthState.mu.Unlock()

	publishCustom("tools_status", summary)
	for t, s := range statuses {
		publishCustom("tool_"+t, s)
	}
	log.Printf("[tools] health: %s", summary)

	switch {
	case len(toFetch) > 0:
		// The install card already reports progress; a toast here would only
		// cover it up.
	case len(missing) == 0:
	case !IsOnline():
		showToast("Offline — connect WiFi to install "+strings.Join(missing, ", "), ToastWarn())
	case len(failures) > 0:
		showToast("Could not install "+strings.Join(missing, ", ")+" — press A to retry", ToastError())
	default:
		showToast("Missing tools: "+strings.Join(missing, ", ")+" — restart JukaHub to install", ToastWarn())
	}

	if len(toFetch) > 0 {
		go func(list []string) {
			var installed []string
			for _, t := range list {
				if err := EnsureHelperTool(t, appConfig); err != nil {
					log.Printf("[tools] %s auto-download failed: %v", t, err)
					toolHealthState.mu.Lock()
					toolHealthState.failures[t] = toolInstallFailureReason(err)
					toolHealthState.mu.Unlock()
					continue
				}
				if _, ok := checkToolPresence(t); !ok {
					log.Printf("[tools] %s reported success but is still missing", t)
					continue
				}
				installed = append(installed, t)
				log.Printf("[tools] %s installed and ready", t)
			}
			if len(installed) == 1 {
				showToast(installed[0]+" installed — restart JukaHub to use it", ToastSuccess())
			} else if len(installed) > 1 {
				showToast("Tools installed — restart JukaHub to use them", ToastSuccess())
			}
			refreshToolHealth()
		}(toFetch)
	}
}

// startToolHealthCheck runs an initial tool health check after startup. It is
// non-fatal — missing tools just produce a status entry and a warning toast.
func startToolHealthCheck() {
	go func() {
		time.Sleep(2 * time.Second) // let the app settle first
		refreshToolHealth()
	}()
}
