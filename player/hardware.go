package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// In-memory log buffer (Log Exporter)
// ---------------------------------------------------------------------------

type logBuffer struct {
	mu       sync.Mutex
	lines    []string
	bytes    int
	max      int
	maxBytes int
}

func (b *logBuffer) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	b.mu.Lock()
	b.lines = append(b.lines, line)
	b.bytes += len(line) + 1
	if b.max <= 0 {
		b.lines = nil
		b.bytes = 0
	} else if len(b.lines) > b.max {
		drop := len(b.lines) - b.max
		for i := 0; i < drop; i++ {
			b.bytes -= len(b.lines[i]) + 1
			b.lines[i] = "" // release the string before the backing array is kept
		}
		b.lines = b.lines[drop:]
	}
	// The line cap alone is not a byte cap: one malformed stack trace can hold
	// most of the heap on the handheld. Trim the oldest lines, then the head of
	// a single overlong line.
	for b.maxBytes > 0 && b.bytes > b.maxBytes && len(b.lines) > 1 {
		b.bytes -= len(b.lines[0]) + 1
		b.lines[0] = ""
		b.lines = b.lines[1:]
	}
	if b.maxBytes > 0 && len(b.lines) == 1 && len(b.lines[0]) > b.maxBytes {
		b.lines[0] = b.lines[0][len(b.lines[0])-b.maxBytes:]
		b.bytes = len(b.lines[0]) + 1
	}
	b.mu.Unlock()
	// Also mirror to stderr so launch.sh's errors.txt capture still works.
	return os.Stderr.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.lines, "\n")
}

func (b *logBuffer) Clear() {
	b.mu.Lock()
	b.lines = nil
	b.bytes = 0
	b.mu.Unlock()
}

var appLog = &logBuffer{max: 2000, maxBytes: logBufferMaxBytes}

// initLogging redirects the standard logger to the in-memory buffer (which
// also forwards to stderr).
func initLogging() {
	log.SetOutput(appLog)
}

// exportLogs writes the captured log buffer to logs.txt in the app folder,
// rotating the previous export to logs.txt.1 and capping the file size.
func exportLogs() {
	// Cap here as well so the reported size matches the file (the writer caps
	// again, which is a no-op on an already-capped slice).
	data := capLogData([]byte(appLog.String()), logExportMaxBytes)
	if err := writeRotatedLog("logs.txt", data, logExportMaxBytes); err != nil {
		log.Printf("exportLogs: %v", err)
		return
	}
	lines := 0
	if len(data) > 0 {
		lines = strings.Count(string(data), "\n") + 1
	}
	log.Printf("Logs exported to logs.txt (%d lines, %d bytes)", lines, len(data))
}

// ---------------------------------------------------------------------------
// Hardware Monitor
// ---------------------------------------------------------------------------

func readProcFirstMatch(path, prefix string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "N/A"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return "N/A"
}

func parseKilobytes(s string) int64 {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, " kB")
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v * 1024
}

func getCPUTempC() string {
	if v, ok := cpuTempValueC(); ok {
		return fmt.Sprintf("%.1f C", v)
	}
	return "N/A"
}

// cpuTempValueC is the same read without the display formatting, for callers
// that map the value (the LED temperature policy) rather than print it.
func cpuTempValueC() (float64, bool) {
	candidates := []string{
		"/sys/class/thermal/thermal_zone0/temp",
		"/sys/class/thermal/thermal_zone1/temp",
	}
	for _, c := range candidates {
		if data, err := os.ReadFile(c); err == nil {
			if v, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && v > 0 {
				return float64(v) / 1000.0, true
			}
		}
	}
	return 0, false
}

func getDiskInfo() (total, used int64, pct int) {
	out, err := exec.Command("df", "-k", "/").Output()
	if err != nil {
		return 0, 0, 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, 0, 0
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 5 {
		return 0, 0, 0
	}
	totalK, _ := strconv.ParseInt(fields[1], 10, 64)
	usedK, _ := strconv.ParseInt(fields[2], 10, 64)
	total = totalK * 1024
	used = usedK * 1024
	if totalK > 0 {
		pct = int(usedK * 100 / totalK)
	}
	return
}

// ---------------------------------------------------------------------------
// Disk Pie Chart Data (auto-refresh every 5 seconds)
// ---------------------------------------------------------------------------

type diskPieData struct {
	Total uint64
	Used  uint64
	Pct   int
}

var (
	diskPie      diskPieData
	diskPieMutex sync.Mutex
)

// sceneHasPieChart reports whether the scene that is up draws the disk pie
// chart. The poller below and the scene-entry refresh both use it.
func sceneHasPieChart(config *Config) bool {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	for _, elem := range config.Scenes[currentSceneIndex].Elements {
		if elem.Type == "piechart" {
			return true
		}
	}
	return false
}

func startDiskPieAutoRefresh() {
	// One reading at startup so the first frame that needs the chart has data.
	refreshDiskPie()
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			// A hidden screen does not poll. The pie lives on one scene, and
			// statfs-ing the card every five seconds for a chart nobody is looking
			// at keeps the CPU awake on a battery device for nothing; entering the
			// scene takes a fresh reading instead (completeSceneTransition).
			if !sceneHasPieChart(appConfig) {
				continue
			}
			refreshDiskPie()
		}
	}()
}

func refreshDiskPie() {
	total, used, pct := getDiskInfo()
	diskPieMutex.Lock()
	diskPie = diskPieData{Total: uint64(total), Used: uint64(used), Pct: pct}
	diskPieMutex.Unlock()
	publishCustom("diskpie_data", diskPie)
}

func getCPUModel() string {
	model := readProcFirstMatch("/proc/cpuinfo", "model name")
	if model == "N/A" {
		model = readProcFirstMatch("/proc/cpuinfo", "Hardware")
	}
	return model
}

func getCoreCount() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "N/A"
	}
	cores := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "processor") {
			cores++
		}
	}
	if cores == 0 {
		return "N/A"
	}
	return strconv.Itoa(cores)
}

func getOSName() string {
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
			}
		}
	}
	return runtime.GOOS
}

func getNetworkInfo() string {
	var sb strings.Builder
	ifaces, err := net.Interfaces()
	if err != nil {
		return "Network: N/A"
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		var ips []string
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				ips = append(ips, ipnet.IP.String())
			}
		}
		if len(ips) > 0 {
			sb.WriteString(fmt.Sprintf("%s: %s\n", iface.Name, strings.Join(ips, ", ")))
		}
	}
	if sb.Len() == 0 {
		return "No active network interfaces"
	}
	return strings.TrimRight(sb.String(), "\n")
}

// getHardwareInfo builds a multi-line hardware summary. All reads are
// best-effort and degrade to "N/A" on non-Linux/dev environments.
func getHardwareInfo() string {
	total := parseKilobytes(readProcFirstMatch("/proc/meminfo", "MemTotal:"))
	avail := parseKilobytes(readProcFirstMatch("/proc/meminfo", "MemAvailable:"))
	used := total - avail
	memPct := 0
	if total > 0 {
		memPct = int(used * 100 / total)
	}

	diskTotal, diskUsed, diskPct := getDiskInfo()

	uptimeSec := int64(0)
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			if v, err := strconv.ParseFloat(fields[0], 64); err == nil {
				uptimeSec = int64(v)
			}
		}
	}
	uptime := time.Duration(uptimeSec) * time.Second
	uptimeStr := fmt.Sprintf("%dd %dh %dm", int(uptime.Hours())/24, int(uptime.Hours())%24, int(uptime.Minutes())%60)

	host, _ := os.Hostname()
	load := readProcFirstMatch("/proc/loadavg", "")

	var sb strings.Builder
	sb.WriteString("=== Hardware Monitor ===\n")
	fmt.Fprintf(&sb, "Hostname     : %s\n", host)
	fmt.Fprintf(&sb, "OS           : %s\n", getOSName())
	fmt.Fprintf(&sb, "Architecture : %s / %s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&sb, "CPU          : %s\n", getCPUModel())
	fmt.Fprintf(&sb, "Cores        : %s\n", getCoreCount())
	fmt.Fprintf(&sb, "Temperature  : %s\n", getCPUTempC())
	fmt.Fprintf(&sb, "Load (1/5/15): %s\n", load)
	fmt.Fprintf(&sb, "Uptime       : %s\n", uptimeStr)
	fmt.Fprintf(&sb, "Memory       : %.1f / %.1f MB (%d%%)\n", float64(used)/1048576.0, float64(total)/1048576.0, memPct)
	fmt.Fprintf(&sb, "Disk (/)     : %.1f / %.1f MB (%d%%)\n", float64(diskUsed)/1048576.0, float64(diskTotal)/1048576.0, diskPct)
	sb.WriteString("\n=== Network ===\n")
	sb.WriteString(getNetworkInfo())

	// Active processes: the list the Processes panel manages interactively.
	sb.WriteString("\n=== Active Processes ===\n")
	if rows, err := collectProcesses(processHardwarePreview); err == nil {
		sb.WriteString(formatProcessList(rows))
		sb.WriteString("* protected - cannot be killed\n")
	} else {
		sb.WriteString("Process list unavailable: " + firstLine(err.Error()) + "\n")
	}

	return sb.String()
}

// ---------------------------------------------------------------------------
// Cron / Startup Items
// ---------------------------------------------------------------------------

// loadStartupItems collects apps that launch on boot from common Linux
// mechanisms (init.d, systemd wants, rc.local, supervisor).
func loadStartupItems() string {
	var items []string

	if entries, err := os.ReadDir("/etc/init.d"); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			items = append(items, "init.d: "+e.Name())
		}
	}

	for _, wants := range []string{
		"/etc/systemd/system/multi-user.target.wants",
		"/etc/systemd/system/graphical.target.wants",
	} {
		if entries, err := os.ReadDir(wants); err == nil {
			for _, e := range entries {
				items = append(items, "systemd: "+e.Name())
			}
		}
	}

	if info, err := os.Stat("/etc/rc.local"); err == nil && info.Mode()&0111 != 0 {
		items = append(items, "rc.local")
	}

	if entries, err := os.ReadDir("/etc/supervisor/conf.d"); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".conf") {
				items = append(items, "supervisor: "+e.Name())
			}
		}
	}

	if len(items) == 0 {
		return "No startup items detected."
	}
	return "Startup Items:\n" + strings.Join(items, "\n")
}

// cronStartupVar is the variable the Cron scene's startup textlog reads.
const cronStartupVar = "startup_text"
