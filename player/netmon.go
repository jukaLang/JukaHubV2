package main

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
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
	// portal is the sign-in page a captive portal sent us to, or "". A portal
	// accepts a TCP connection and answers HTTP, so the old dial probe called
	// it online while every real request failed.
	portal string
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
	if netMon.portal != "" {
		return "sign-in required"
	}
	if netMon.reason != "" {
		return "offline (" + netMon.reason + ")"
	}
	return "offline"
}

// PortalSignInURL is the page a captive portal sent the probe to, or "" when
// the network is not behind one. The Wi-Fi page offers "Sign in" while it is
// set, and downloads, updates and searches stay paused because IsOnline is
// false until the probe passes.
func PortalSignInURL() string {
	netMon.mu.RLock()
	defer netMon.mu.RUnlock()
	return netMon.portal
}

// netProbeResult is one connectivity answer: verified internet, a captive
// portal that wants a sign-in, or neither.
type netProbeResult struct {
	online bool
	// portal is the page to open when a portal answered instead of the probe.
	portal string
	reason string
}

// netProbeEndpoints are the generate_204 endpoints the probe asks. A network
// with real internet answers 204 with no body; a hotel, airport or cafe portal
// intercepts the request, so anything else — a redirect, an HTML "sign in"
// page, a 511 — means the connection is not usable yet.
var netProbeEndpoints = []string{
	"http://connectivitycheck.gstatic.com/generate_204",
	"http://clients3.google.com/generate_204",
	"http://connectivitycheck.platform.hicloud.com/generate_204",
}

// netProbeClient follows no redirects: the redirect target is the portal page.
var netProbeClient = &http.Client{
	Timeout: 4 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// probeConnectivity performs one connectivity check against the first endpoint
// that answers, and reports a portal's sign-in page when it finds one.
func probeConnectivity() netProbeResult {
	failure := ""
	for _, endpoint := range netProbeEndpoints {
		result := probeConnectivityAt(endpoint)
		if result.online || result.portal != "" {
			return result
		}
		if failure == "" {
			failure = result.reason
		}
	}
	if failure == "" {
		failure = "no route"
	}
	return netProbeResult{reason: failure}
}

// probeConnectivityAt checks one endpoint. The body is read only far enough to
// tell a page from an empty 204; a portal's page is never downloaded here.
func probeConnectivityAt(endpoint string) netProbeResult {
	resp, err := netProbeClient.Get(endpoint)
	if err != nil {
		return netProbeResult{reason: "no route"}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode == http.StatusNoContent:
		return netProbeResult{online: true}
	case resp.StatusCode >= 300 && resp.StatusCode < 400,
		resp.StatusCode == http.StatusNetworkAuthenticationRequired:
		return netProbeResult{portal: probePortalURL(resp, endpoint), reason: "sign-in required"}
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// A 200 with a page: the portal answered instead of the probe.
		return netProbeResult{portal: probePortalURL(resp, endpoint), reason: "sign-in required"}
	case strings.TrimSpace(string(body)) == "":
		return netProbeResult{reason: "probe rejected"}
	}
	return netProbeResult{reason: "probe rejected"}
}

// probePortalURL is where the portal wants the user to sign in: the redirect's
// Location resolved against the probe URL, else the URL that finally answered.
func probePortalURL(resp *http.Response, endpoint string) string {
	if resp == nil {
		return endpoint
	}
	if loc := strings.TrimSpace(resp.Header.Get("Location")); loc != "" {
		if base, err := url.Parse(endpoint); err == nil {
			if ref, err := url.Parse(loc); err == nil {
				return base.ResolveReference(ref).String()
			}
		}
		return loc
	}
	if resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.String()
	}
	return endpoint
}

// update publishes the current connectivity state and shows a toast on change.
func (m *netMonitor) update() {
	result := probeConnectivity()

	m.mu.Lock()
	firstCheck := !m.checked
	prevPortal := m.portal
	changed := m.checked && result.online != m.online
	m.online = result.online
	m.checked = true
	m.lastProbe = time.Now()
	m.portal = result.portal
	m.reason = result.reason
	if result.online {
		m.reason = ""
	} else if m.reason == "" {
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
	if sshSrv.Running() {
		publishCustom("ssh_address", sshSrv.Address())
	}
	// Network time follows the connection (ntpsync.go): on every connect and
	// hourly while online. A captive portal is not online.
	netTimeAfterProbe(result.online, result.online && (firstCheck || changed))

	switch {
	case result.portal != "" && (firstCheck || prevPortal == ""):
		// Said once, not every 20 seconds while the user walks to the login
		// page. Downloads and updates stay paused because IsOnline is false.
		showToast("Wi-Fi needs a sign-in — open Wi-Fi and press Sign in", ToastWarn())
		logWarn("net", "captive portal: %s", result.portal)
	case result.portal != "":
		// Still behind the same portal.
	case changed && result.online:
		showToast("Network restored", ToastSuccess())
		logDebug("net", "connectivity restored")
		// Tools that could not be installed while offline are fetched now, so
		// the user does not have to restart the app after joining WiFi.
		go installMissingToolsAfterReconnect()
	case changed:
		showToast("Offline — connect WiFi to play videos and install tools", ToastWarn())
		logWarn("net", "connectivity lost")
	case firstCheck && !result.online:
		// Launched before joining WiFi: say why searches, playback and tool
		// installs will fail instead of leaving the user with a bare error.
		showToast("Offline — connect WiFi to play videos and install tools", ToastWarn())
		logDebug("net", "started offline")
	}
}

// startNetworkMonitor begins periodic connectivity checks. It runs one probe
// immediately, then every 20s. On TSP this is one small HTTP request to a
// generate_204 endpoint and stops requests from piling up when WiFi drops.
func startNetworkMonitor() {
	go func() {
		// First probe slightly delayed so startup isn't blocked.
		time.Sleep(1 * time.Second)
		netMon.update()
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		wake := subscribeForegroundWake()
		for {
			// A probe is a TCP dial; while a game owns the screen nobody can see
			// the offline chip it keeps current. The first tick after coming
			// back answers for the network the game may have just left.
			if !waitForPoll(ticker.C, wake) {
				continue
			}
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
// ffplay is not one of them: nothing runs it (the embedded player decodes with
// ffmpeg), and BtbN's build needs X11, which the handheld does not have. It
// used to be fetched first on every launch, so a failing bundle showed up as
// "Could not install ffplay".
var autoInstallTools = []string{"yt-dlp", "ffmpeg", "ffprobe"}

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
	tools := autoInstallTools
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
	// ffmpeg, ffprobe). One attempt per tool per app run; installs run
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
	logDebug("tools", "health: %s", summary)

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
			bundleFailed := false
			for _, t := range list {
				// ffmpeg and ffprobe come out of one bundle: once installing it
				// has failed for one of them, the next would only fail the same way.
				if bundleFailed && ffmpegFamilyTool(t) {
					continue
				}
				if err := EnsureHelperTool(t, appConfig); err != nil {
					logWarn("tools", "%s auto-download failed: %v", t, err)
					toolHealthState.mu.Lock()
					toolHealthState.failures[t] = toolInstallFailureReason(err)
					toolHealthState.mu.Unlock()
					if ffmpegFamilyTool(t) {
						bundleFailed = true
					}
					continue
				}
				if _, ok := checkToolPresence(t); !ok {
					logWarn("tools", "%s reported success but is still missing", t)
					continue
				}
				installed = append(installed, t)
				logDebug("tools", "%s installed and ready", t)
			}
			if len(installed) == 1 {
				showToast(installed[0]+" installed — restart JukaHub to use it", ToastSuccess())
				for _, t := range installed {
					noteAwayToolInstalled(t)
				}
			} else if len(installed) > 1 {
				showToast("Tools installed — restart JukaHub to use them", ToastSuccess())
				for _, t := range installed {
					noteAwayToolInstalled(t)
				}
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
