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

	if changed {
		if online {
			showToast("Network restored", ToastSuccess())
			log.Printf("[net] connectivity restored")
		} else {
			showToast("Offline — showing cached content", ToastWarn())
			log.Printf("[net] connectivity lost")
		}
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
	mu     sync.Mutex
	status map[string]string // tool name -> "ok" | "missing" | "path"
}

var toolHealthState = &toolHealth{status: make(map[string]string)}

// checkToolPresence verifies that the given tool is available either in the
// required/ folder or on PATH. Returns the resolved path (or the tool name if
// only on PATH) and whether it was found.
func checkToolPresence(tool string) (string, bool) {
	// Local bundled copy first.
	if p, _ := P().LookPath(tool); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	// System PATH.
	if _, err := exec.LookPath(tool); err == nil {
		return tool, true
	}
	return "", false
}

// refreshToolHealth re-checks the required tools and publishes a summary
// variable plus per-tool status entries.
func refreshToolHealth() {
	tools := []string{"yt-dlp", "ffplay", "mpv"}
	toolHealthState.mu.Lock()
	toolHealthState.status = make(map[string]string)
	var missing []string
	for _, t := range tools {
		path, ok := checkToolPresence(t)
		if ok {
			toolHealthState.status[t] = path
		} else {
			toolHealthState.status[t] = "missing"
			missing = append(missing, t)
		}
	}
	summary := "All tools available"
	if len(missing) > 0 {
		summary = "Missing: " + strings.Join(missing, ", ")
	}
	toolHealthState.mu.Unlock()

	publishCustom("tools_status", summary)
	for t, s := range toolHealthState.status {
		publishCustom("tool_"+t, s)
	}
	log.Printf("[tools] health: %s", summary)
	if len(missing) > 0 {
		showToast("Missing tools: "+strings.Join(missing, ", "), ToastWarn())
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
