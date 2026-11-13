package main

import (
	"context"
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
	// offlineSince is when the probe started failing (zero while online). For
	// two minutes after that it runs every five seconds instead of twenty, so
	// a radio that comes back is noticed in seconds, whatever took it away.
	offlineSince time.Time
	// toldOffline is set once a toast has said the device is offline, so
	// "Network restored" is only said to someone who was told it was gone.
	toldOffline bool
	// wake is the reconnect after the last sleep (wake_resume.go).
	wake netWakeWindow
	// probed is closed when a probe finishes, waking netWaitOnline's callers;
	// the next waiter makes a fresh one.
	probed chan struct{}
}

// netWakeWindow is the minute after a wake in which the device is expected to
// be reconnecting: the probe runs every 1.5 s, "Offline" is not announced, and
// requests wait for the network instead of failing (netWaitOnline). It opens
// only when the device was online before it slept, and closes at the first
// probe the internet answers, or when the minute is up.
type netWakeWindow struct {
	at      time.Time
	open    bool
	toasted bool
}

const (
	// netProbeEvery is the probe's ordinary interval.
	netProbeEvery = 20 * time.Second
	// netOfflineProbeEvery and netOfflineFastFor: how often the probe runs,
	// and for how long, once the network has gone.
	netOfflineProbeEvery = 5 * time.Second
	netOfflineFastFor    = 2 * time.Minute
	// netWakeProbeEvery is the probe's interval inside the wake window, and
	// netWakeProbeBudget the most one probe there may take, all endpoints
	// together.
	netWakeProbeEvery  = 1500 * time.Millisecond
	netWakeProbeBudget = 4 * time.Second
	// netWakeWindowLen is how long after a wake the device counts as
	// reconnecting rather than offline.
	netWakeWindowLen = time.Minute
	// netWakeToastAfter: a radio still down this long after a wake gets one
	// toast, so a person waiting knows it is being worked on.
	netWakeToastAfter = 20 * time.Second
	// netWakeWaitMax is the longest a request waits for the network after a
	// wake (netWaitOnline) before it goes out anyway and says what failed.
	netWakeWaitMax = 15 * time.Second
	// netReconnectingText is what a screen that waits shows meanwhile.
	netReconnectingText = "Reconnecting to Wi-Fi…"
)

var netMon = &netMonitor{}

// netProbeNudge asks the monitor's loop to probe now (netNudge).
var netProbeNudge = make(chan struct{}, 1)

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
	if netMon.wake.open && time.Since(netMon.wake.at) < netWakeWindowLen {
		return "reconnecting…"
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
func probeConnectivity() netProbeResult { return probeConnectivityWithin(0) }

// probeConnectivityWithin is probeConnectivity with the whole check bounded by
// budget (0: each endpoint has the client's own timeout). Inside a wake's
// reconnect window a link that is still down must not hold the probe for
// twelve seconds — three endpoints times four — while the radio comes back
// underneath it: the next probe is 1.5 s away anyway.
func probeConnectivityWithin(budget time.Duration) netProbeResult {
	ctx := context.Background()
	if budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}
	failure := ""
	for _, endpoint := range netProbeEndpoints {
		result := probeConnectivityAt(ctx, endpoint)
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
func probeConnectivityAt(ctx context.Context, endpoint string) netProbeResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return netProbeResult{reason: "no route"}
	}
	resp, err := netProbeClient.Do(req)
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

// netNotice is what one probe result tells the user, if anything.
type netNotice int

const (
	netNoticeNone netNotice = iota
	netNoticePortal
	netNoticeRestored
	netNoticeLost
	netNoticeStartedOffline
	netNoticeWakeSlow
)

// update publishes the current connectivity state and shows a toast on change.
//
// Inside a wake's reconnect window (netWakeWindow) the radio being down is the
// expected state, not news: the firmware turns Wi-Fi off for every sleep, so an
// "Offline" toast followed by "Network restored" would greet every wake. The
// window says nothing unless the radio is still down twenty seconds in, and a
// return is only announced to someone who was told the network had gone.
//
// The monitor's loop calls it, and so does the browser's offline card
// (brw_offline.go) on its own goroutine: every decision is taken under the
// lock, so two probes finishing together still close a window, log a wake or
// show a toast once.
func (m *netMonitor) update() {
	result := probeConnectivityWithin(m.probeBudget())
	now := time.Now()

	m.mu.Lock()
	firstCheck := !m.checked
	prevPortal := m.portal
	changed := m.checked && result.online != m.online
	m.online = result.online
	m.checked = true
	m.lastProbe = now
	m.portal = result.portal
	m.reason = result.reason
	if result.online {
		m.reason = ""
		m.offlineSince = time.Time{}
	} else {
		if m.reason == "" {
			m.reason = "no route"
		}
		if m.offlineSince.IsZero() {
			m.offlineSince = now
		}
	}
	// The wake window closes at the first answer from the internet, or when
	// its minute is up. An answer just past the minute (the probe that would
	// have closed it was due) still counts as the return from that wake.
	wakeOpen := m.wake.open && now.Sub(m.wake.at) < netWakeWindowLen
	sinceWake := now.Sub(m.wake.at)
	backAfterWake, wakeExpired, slowWake := false, false, false
	switch {
	case m.wake.open && result.online && sinceWake < netWakeWindowLen+netOfflineProbeEvery:
		m.wake.open, backAfterWake = false, true
	case m.wake.open && !wakeOpen:
		m.wake.open, wakeExpired = false, true
	case wakeOpen && !m.wake.toasted && sinceWake >= netWakeToastAfter && result.portal == "":
		m.wake.toasted, slowWake = true, true
	}
	notice := netNoticeNone
	switch {
	case result.portal != "" && (firstCheck || prevPortal == ""):
		// Said once, not every 20 seconds while the user walks to the login
		// page. Downloads and updates stay paused because IsOnline is false.
		notice = netNoticePortal
	case result.portal != "":
		// Still behind the same portal.
	case slowWake:
		notice = netNoticeWakeSlow
	case changed && result.online:
		if m.toldOffline {
			notice = netNoticeRestored
		}
	case changed && !wakeOpen:
		notice = netNoticeLost
	case firstCheck && !result.online:
		notice = netNoticeStartedOffline
	}
	switch {
	case result.online:
		m.toldOffline = false
	case notice != netNoticeNone:
		m.toldOffline = true
	}
	if m.probed != nil {
		close(m.probed)
		m.probed = nil
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

	switch notice {
	case netNoticePortal:
		showToast("Wi-Fi needs a sign-in — open Wi-Fi and press Sign in", ToastWarn())
		logWarn("net", "captive portal: %s", result.portal)
	case netNoticeWakeSlow:
		showToast("Wi-Fi is still reconnecting…", ToastWarn())
		logInfo("wifi", "still offline %.0f s after wake (%s)", sinceWake.Seconds(), wifiResumeSummary())
	case netNoticeRestored:
		showToast("Network restored", ToastSuccess())
	case netNoticeLost:
		showToast("Offline — connect WiFi to play videos and install tools", ToastWarn())
		logWarn("net", "connectivity lost")
	case netNoticeStartedOffline:
		// Launched before joining WiFi: say why searches, playback and tool
		// installs will fail instead of leaving the user with a bare error.
		showToast("Offline — connect WiFi to play videos and install tools", ToastWarn())
		logDebug("net", "started offline")
	}
	switch {
	case changed && result.online:
		logDebug("net", "connectivity restored")
		// Tools that could not be installed while offline are fetched now, so
		// the user does not have to restart the app after joining WiFi.
		go installMissingToolsAfterReconnect()
	case changed && wakeOpen:
		logDebug("net", "offline after the wake — waiting for the Wi-Fi to come back")
	}
	// One line per wake, for device feedback: how long the internet took to
	// answer and what was done to the radio meanwhile (wifi_resume.go).
	if backAfterWake {
		logInfo("wifi", "back online %.1f s after wake (%s)", sinceWake.Seconds(), wifiResumeSummary())
	}
	if wakeExpired && !result.online {
		logWarn("wifi", "still offline a minute after wake (%s)", wifiResumeSummary())
	}
	if backAfterWake || (changed && result.online) {
		chatNetworkBack()
	}
}

// probeBudget bounds the whole probe inside a wake's reconnect window
// (probeConnectivityWithin); 0 elsewhere.
func (m *netMonitor) probeBudget() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.wake.open && time.Since(m.wake.at) < netWakeWindowLen {
		return netWakeProbeBudget
	}
	return 0
}

// nextInterval is how long the loop waits before the next probe: 1.5 s inside
// a wake's reconnect window, 5 s for two minutes after the network went, and
// 20 s otherwise.
func (m *netMonitor) nextInterval(now time.Time) time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch {
	case m.wake.open && now.Sub(m.wake.at) < netWakeWindowLen:
		return netWakeProbeEvery
	case m.checked && !m.online && !m.offlineSince.IsZero() && now.Sub(m.offlineSince) < netOfflineFastFor:
		return netOfflineProbeEvery
	}
	return netProbeEvery
}

// startNetworkMonitor begins the connectivity checks, and the wake watcher
// with them (wake_resume.go). One probe runs a second after start, then every
// 20 s — every 5 s for two minutes after the network goes, every 1.5 s after
// a wake until the internet answers — and at once when something asks
// (netNudge: a wake, a Wi-Fi join, a request waiting for the network). On TSP a
// probe is one small HTTP request to a generate_204 endpoint, and it stops
// requests from piling up when WiFi drops.
func startNetworkMonitor() {
	startWakeWatcher()
	go func() {
		// First probe slightly delayed so startup isn't blocked.
		time.Sleep(1 * time.Second)
		netMon.update()
		timer := time.NewTimer(netMon.nextInterval(time.Now()))
		defer timer.Stop()
		foreground := subscribeForegroundWake()
		for {
			select {
			case <-timer.C:
			case <-foreground:
			case <-netProbeNudge:
			}
			// A probe is a TCP dial; while a game owns the screen nobody can see
			// the offline chip it keeps current. The first tick after coming
			// back answers for the network the game may have just left.
			if !backgroundPaused() {
				netMon.update()
			}
			timer.Reset(netMon.nextInterval(time.Now()))
		}
	}()
}

// netNudge asks for a probe now instead of at the next interval. Any
// goroutine may call it; a nudge that arrives while one is pending is the same
// nudge.
func netNudge() {
	select {
	case netProbeNudge <- struct{}{}:
	default:
	}
}

// netWakeNoted opens the reconnect window for a wake (wake_resume.go) and asks
// for a probe now. A device that was offline before it slept has nothing to
// reconnect to: it is probed at once all the same, but nothing waits for it.
func netWakeNoted() {
	netMon.mu.Lock()
	wasOnline := !netMon.checked || netMon.online
	netMon.wake = netWakeWindow{at: time.Now(), open: wasOnline}
	netMon.mu.Unlock()
	// The keep-alive connection the last probe left in the pool died with the
	// radio; reused, it would hold the first probe for its whole budget
	// instead of dialling afresh.
	netProbeClient.CloseIdleConnections()
	netNudge()
}

// netReconnecting reports whether JukaHub is inside a wake's reconnect window:
// it slept while online and the internet has not answered since. Gates that
// would refuse an offline request wait for the network instead
// (netWaitOnline), and screens say "Reconnecting to Wi-Fi…", not "Offline".
func netReconnecting() bool {
	netMon.mu.RLock()
	defer netMon.mu.RUnlock()
	return netMon.wake.open && time.Since(netMon.wake.at) < netWakeWindowLen
}

// probeSignal returns a channel that is closed when the next probe finishes.
func (m *netMonitor) probeSignal() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.probed == nil {
		m.probed = make(chan struct{})
	}
	return m.probed
}

// netWaitOnline waits, at most limit, for the internet to answer while
// JukaHub is reconnecting after a wake, and reports whether it is online.
// Outside a reconnect it answers at once — online or plainly offline — so it
// never adds a wait to an ordinary request. Any goroutine may call it: it
// wakes on every probe result, asks for one straight away, and gives up as
// soon as ctx ends.
func netWaitOnline(ctx context.Context, limit time.Duration) bool {
	if !netReconnecting() {
		return IsOnline()
	}
	netNudge()
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	for {
		// The channel is taken before the state is read, so a probe that
		// finishes in between still ends this wait.
		probed := netMon.probeSignal()
		if !netReconnecting() {
			return IsOnline()
		}
		select {
		case <-probed:
		case <-deadline.C:
			return IsOnline()
		case <-ctx.Done():
			return false
		}
	}
}

// netWaitLabel is label, or "Reconnecting to Wi-Fi…" while a wake's reconnect
// is under way and requests are held for it.
func netWaitLabel(label string) string {
	if netReconnecting() {
		return netReconnectingText
	}
	return label
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
	ttsVoiceAutoRetry()
	sttAutoRetry()
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
		ttsToolsInstalling.Add(1)
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
			ttsToolsInstalling.Add(-1)
			refreshToolHealth()
		}(toFetch)
	} else {
		// Nothing of the tools' own left to fetch (ffmpeg is in, or failed
		// and waits for A): the Smart Pro's offline speech recognition
		// follows, once (it is not in the app archive), then its offline
		// voice, which waits for it.
		sttAutoInstallOffline()
		ttsAutoInstallOfflineVoice()
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
