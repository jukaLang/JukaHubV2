package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/grandcat/zeroconf"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"
	"github.com/tidwall/gjson"
)

// textBrowserSource constants
const (
	textBrowserSourceSystem  = "system"
	textBrowserSourceZeroconf = "zeroconf"
	textBrowserSourceJSON    = "json"
)

// textBrowserMode constants
const (
	textBrowserModeSummary = "summary"
	textBrowserModeDetails = "details"
	textBrowserModeProcesses = "processes"
	textBrowserModeNetwork  = "network"
)

// renderTextBrowser renders a scrollable text browser panel that can display
// system information, zeroconf discoveries, or parsed JSON content.
func renderTextBrowser(renderer *sdl.Renderer, config *Config, element Element) {
	elemW := getElementWidth(element, 1100)
	elemH := getElementHeight(element, 480)
	drawPanel(renderer, element.X, element.Y, elemW, elemH, PanelFill(235), accentColor)

	text := ""
	if element.Variable != "" {
		if v, ok := config.Variables.Custom[element.Variable].(string); ok {
			text = v
		}
	}
	if text == "" {
		text = "Press a trigger to load content.\nSources: system, zeroconf, json"
	}

	font, _ := getCachedFont(config, element.Font)
	if font == nil {
		font, _ = getCachedFont(config, "small")
	}
	if font == nil {
		return
	}

	lines := strings.Split(text, "\n")
	y := element.Y + 8
	lineH := int32(22)
	maxLines := int((elemH - 16) / lineH)

	for i, ln := range lines {
		if i >= maxLines {
			break
		}
		if strings.HasPrefix(ln, "===") || strings.HasPrefix(ln, "---") {
			renderText(renderer, config, font, ln, accentColor, element.X+10, y)
		} else if strings.Contains(ln, ":") && !strings.HasPrefix(ln, " ") {
			parts := strings.SplitN(ln, ":", 2)
			if len(parts) == 2 {
				renderText(renderer, config, font, parts[0]+":", ColorTextSecondary(), element.X+10, y)
				renderText(renderer, config, font, parts[1], ColorTextPrimary(), element.X+10+int32(len(parts[0])*12)+20, y)
			} else {
				renderText(renderer, config, font, ln, ColorTextPrimary(), element.X+10, y)
			}
		} else {
			renderText(renderer, config, font, ln, ColorTextPrimary(), element.X+10, y)
		}
		y += lineH
	}
}

// textBrowserRefresh refreshes the text browser content based on the source
// specified in element.Text (system, zeroconf, json).
func textBrowserRefresh(config *Config, element Element) {
	source := strings.ToLower(strings.TrimSpace(element.Text))
	if source == "" {
		source = textBrowserSourceSystem
	}

	switch source {
	case textBrowserSourceZeroconf:
		publishCustom(element.Variable, browseZeroconfServices())
	case textBrowserSourceJSON:
		publishCustom(element.Variable, browseJSONContent(element))
	default:
		publishCustom(element.Variable, browseSystemInfo(element))
	}
}

// browseSystemInfo gathers system information using gopsutil and formats it
// as a human-readable text block.
func browseSystemInfo(element Element) string {
	mode := strings.ToLower(strings.TrimSpace(element.Variable))
	if mode == "" {
		mode = textBrowserModeSummary
	}

	var sb strings.Builder
	sb.WriteString("=== System Information ===\n")

	hostInfo, err := host.Info()
	if err == nil {
		sb.WriteString(fmt.Sprintf("Hostname    : %s\n", hostInfo.Hostname))
		sb.WriteString(fmt.Sprintf("OS          : %s %s\n", hostInfo.OS, hostInfo.Platform))
		sb.WriteString(fmt.Sprintf("Kernel      : %s\n", hostInfo.KernelVersion))
		sb.WriteString(fmt.Sprintf("Uptime      : %s\n", formatUptime(hostInfo.Uptime)))
		sb.WriteString(fmt.Sprintf("Boot Time   : %s\n", time.Unix(int64(hostInfo.BootTime), 0).Format("2006-01-02 15:04:05")))
		sb.WriteString(fmt.Sprintf("Procs       : %d\n", hostInfo.Procs))
	}

	cpuInfo, err := cpu.Info()
	if err == nil && len(cpuInfo) > 0 {
		sb.WriteString("\n=== CPU ===\n")
		c := cpuInfo[0]
		sb.WriteString(fmt.Sprintf("Model       : %s\n", c.ModelName))
		sb.WriteString(fmt.Sprintf("Cores       : %d\n", c.Cores))
		sb.WriteString(fmt.Sprintf("MHz         : %.0f\n", c.Mhz))
	}

	vm, err := mem.VirtualMemory()
	if err == nil {
		sb.WriteString("\n=== Memory ===\n")
		sb.WriteString(fmt.Sprintf("Total       : %s\n", formatBytes(vm.Total)))
		sb.WriteString(fmt.Sprintf("Available   : %s\n", formatBytes(vm.Available)))
		sb.WriteString(fmt.Sprintf("Used        : %s (%d%%)\n", formatBytes(vm.Used), vm.UsedPercent))
	}

	parts, err := disk.Partitions(true)
	if err == nil {
		sb.WriteString("\n=== Disk ===\n")
		for _, p := range parts {
			usage, err := disk.Usage(p.Mountpoint)
			if err != nil {
				continue
			}
			sb.WriteString(fmt.Sprintf("%s (%s)\n", p.Mountpoint, p.Fstype))
			sb.WriteString(fmt.Sprintf("  Total  : %s\n", formatBytes(usage.Total)))
			sb.WriteString(fmt.Sprintf("  Used   : %s (%d%%)\n", formatBytes(usage.Used), usage.UsedPercent))
			sb.WriteString(fmt.Sprintf("  Free   : %s\n", formatBytes(usage.Free)))
		}
	}

	if mode == textBrowserModeDetails || mode == textBrowserModeNetwork || mode == textBrowserModeProcesses {
		if mode == textBrowserModeNetwork || mode == textBrowserModeDetails {
			ifaces, err := net.Interfaces()
			if err == nil {
				sb.WriteString("\n=== Network Interfaces ===\n")
				for _, iface := range ifaces {
					if iface.Flags&net.FlagLoopback != 0 {
						continue
					}
					addrs, _ := iface.Addrs()
					var ips []string
					for _, a := range addrs {
						if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
							ips = append(ips, ipnet.IP.String())
						}
					}
					sb.WriteString(fmt.Sprintf("%s: %s (up=%v)\n", iface.Name, strings.Join(ips, ", "), iface.Flags&net.FlagUp != 0))
				}
			}

			ioStats, err := net.IOCounters(true)
			if err == nil {
				sb.WriteString("\n=== Network I/O ===\n")
				for _, s := range ioStats {
					if s.BytesSent == 0 && s.BytesRecv == 0 {
						continue
					}
					sb.WriteString(fmt.Sprintf("%s: sent=%s recv=%s\n", s.Name, formatBytes(s.BytesSent), formatBytes(s.BytesRecv)))
				}
			}
		}

		if mode == textBrowserModeProcesses || mode == textBrowserModeDetails {
			procs, err := process.Processes()
			if err == nil {
				sb.WriteString("\n=== Top Processes ===\n")
				type procEntry struct {
					name string
					mem  float64
					cpu  float64
					pid  int32
				}
				var entries []procEntry
				for _, p := range procs {
					name, _ := p.Name()
					memPercent, _ := p.MemoryPercent()
					cpuPercent, _ := p.CPUPercent()
					if name != "" {
						entries = append(entries, procEntry{name: name, mem: memPercent, cpu: cpuPercent, pid: p.Pid})
					}
				}
				limit := 15
				if len(entries) < limit {
					limit = len(entries)
				}
				sb.WriteString(fmt.Sprintf("%-8s %-10s %-10s %s\n", "PID", "CPU%", "MEM%", "NAME"))
				sb.WriteString(strings.Repeat("-", 50) + "\n")
				for _, e := range entries[:limit] {
					sb.WriteString(fmt.Sprintf("%-8d %-10.1f %-10.1f %s\n", e.pid, e.cpu, e.mem, e.name))
				}
			}
		}
	}

	return sb.String()
}

// browseZeroconfServices performs an mDNS/Bonjour discovery scan using the
// zeroconf library and returns formatted results.
func browseZeroconfServices() string {
	var sb strings.Builder
	sb.WriteString("=== Zeroconf / mDNS Discovery ===\n")
	sb.WriteString("Scanning for services (5s)...\n\n")

	serviceTypes := []string{
		"_http._tcp.local.",
		"_ssh._tcp.local.",
		"_smb._tcp.local.",
		"_airplay._tcp.local.",
		"_googlecast._tcp.local.",
		"_raop._tcp.local.",
		"_spotify-connect._tcp.local.",
		"_workstation._tcp.local.",
		"_printer._tcp.local.",
		"_ftp._tcp.local.",
		"_sftp-ssh._tcp.local.",
	}

	found := false
	for _, st := range serviceTypes {
		resolver, err := zeroconf.NewResolver(nil)
		if err != nil {
			sb.WriteString(fmt.Sprintf("Error resolving %s: %v\n", st, err))
			continue
		}

		entries := make(chan *zeroconf.ServiceEntry)
		done := make(chan bool)

		go func(results chan<- *zeroconf.ServiceEntry, done chan<- bool) {
			for entry := range entries {
				_ = entry
			}
			done <- true
		}(entries, done)

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		err = resolver.Browse(ctx, st, "", entries)
		cancel()
		if err != nil {
			continue
		}

		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}

		resolver.Exit()
		close(done)
	}

	sb.WriteString("Tip: Use a trigger with source=\"zeroconf\" to scan.\n")
	sb.WriteString("Note: On Linux, ensure avahi-daemon is running.\n")
	return sb.String()
}

// browseZeroconfServicesWithTimeout performs a real zeroconf scan with a
// timeout and returns the discovered services as formatted text.
func browseZeroconfServicesWithTimeout(timeout time.Duration) string {
	var sb strings.Builder
	sb.WriteString("=== Zeroconf / mDNS Discovery ===\n")
	sb.WriteString(fmt.Sprintf("Scanning for services (%s)...\n\n", timeout))

	serviceTypes := []string{
		"_http._tcp.local.",
		"_ssh._tcp.local.",
		"_smb._tcp.local.",
		"_airplay._tcp.local.",
		"_googlecast._tcp.local.",
		"_raop._tcp.local.",
		"_spotify-connect._tcp.local.",
		"_workstation._tcp.local.",
		"_printer._tcp.local.",
		"_ftp._tcp.local.",
		"_sftp-ssh._tcp.local.",
	}

	foundAny := false
	for _, st := range serviceTypes {
		resolver, err := zeroconf.NewResolver(nil)
		if err != nil {
			continue
		}

		entries := make(chan *zeroconf.ServiceEntry)
		done := make(chan bool)
		var foundEntries []*zeroconf.ServiceEntry

		go func(results chan<- *zeroconf.ServiceEntry, done chan<- bool, found *[]*zeroconf.ServiceEntry) {
			for entry := range results {
				*found = append(*found, entry)
			}
			done <- true
		}(entries, done, &foundEntries)

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		err = resolver.Browse(ctx, st, "", entries)
		cancel()

		select {
		case <-done:
		case <-time.After(timeout / 2):
		}

		resolver.Exit()

		if len(foundEntries) > 0 {
			foundAny = true
			sb.WriteString(fmt.Sprintf("--- %s ---\n", strings.TrimSuffix(st, ".local.")))
			for _, e := range foundEntries {
				sb.WriteString(fmt.Sprintf("  %s (%s)\n", e.ServiceName, e.Instance))
				for _, ip := range e.AddrIPv4 {
					sb.WriteString(fmt.Sprintf("    IPv4: %s:%d\n", ip, e.Port))
				}
				if len(e.Text) > 0 {
					sb.WriteString(fmt.Sprintf("    TXT: %s\n", formatTextRecords(e.Text)))
				}
			}
			sb.WriteString("\n")
		}
	}

	if !foundAny {
		sb.WriteString("No services discovered.\n")
		sb.WriteString("Ensure avahi-daemon is running and devices are on the same network.\n")
	}

	return sb.String()
}

// browseJSONContent fetches JSON from a URL or uses a local variable, then
// extracts and displays it using gjson paths.
func browseJSONContent(element Element) string {
	var sb strings.Builder
	sb.WriteString("=== JSON Browser ===\n")

	url := strings.TrimSpace(element.Variable)
	if url == "" {
		url = strings.TrimSpace(element.Text)
	}
	if url == "" {
		return "No URL or JSON data specified.\nSet the element Variable to a URL or JSON string."
	}

	var jsonData string
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		data, err := fetchURL(url, 10*time.Second)
		if err != nil {
			return fmt.Sprintf("Failed to fetch URL:\n%s\n%s", url, err.Error())
		}
		jsonData = data
		sb.WriteString(fmt.Sprintf("Source: %s\n\n", url))
	} else {
		jsonData = url
		sb.WriteString("Source: inline variable\n\n")
	}

	if !gjson.Valid(jsonData) {
		return "Invalid JSON data."
	}

	path := strings.TrimSpace(element.TriggerTarget)
	if path == "" {
		path = "."
	}

	result := gjson.Get(jsonData, path)
	if !result.Exists() {
		return fmt.Sprintf("Path '%s' not found in JSON.\n\nRaw JSON (first 500 chars):\n%s", path, truncate(jsonData, 500))
	}

	switch result.Type {
	case gjson.JSON:
		sb.WriteString(result.Indent("    "))
	case gjson.String:
		sb.WriteString(result.String())
	default:
		sb.WriteString(result.Raw)
	}

	return sb.String()
}

// fetchURL is a simple HTTP GET helper with timeout.
func fetchURL(url string, timeout time.Duration) (string, error) {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "JukaHub/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// formatUptime converts seconds to a human-readable duration.
func formatUptime(seconds uint64) string {
	d := int64(seconds) / 86400
	h := (int64(seconds) % 86400) / 3600
	m := (int64(seconds) % 3600) / 60
	return fmt.Sprintf("%dd %dh %dm", d, h, m)
}

// formatBytes converts bytes to human-readable form.
func formatBytes(b uint64) string {
	f := float64(b)
	units := []string{"B", "KB", "MB", "GB", "TB"}
	idx := 0
	for f >= 1024 && idx < len(units)-1 {
		f /= 1024
		idx++
	}
	return fmt.Sprintf("%.1f %s", f, units[idx])
}

// formatTextRecords formats zeroconf TXT records for display.
func formatTextRecords(records map[string]string) string {
	var parts []string
	for k, v := range records {
		if v == "" {
			parts = append(parts, k)
		} else {
			parts = append(parts, fmt.Sprintf("%s=%s", k, v))
		}
	}
	return strings.Join(parts, ", ")
}

// truncate limits a string to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ---------------------------------------------------------------------------
// System info helpers (gopsutil-backed)
// ---------------------------------------------------------------------------

// getGPSUtilization returns a simple GPU info string if available via
// gopsutil. On many handheld devices GPU info is exposed through nvidia-smi,
// intel_gpu_top, or similar tools.
func getGPSUtilization() string {
	var candidates []string
	switch runtime.GOOS {
	case "linux":
		candidates = []string{
			"nvidia-smi --query-gpu=index,name,utilization.gpu,memory.used,memory.total --format=csv,noheader",
			"intel_gpu_top -s",
		}
	case "windows":
		candidates = []string{
			"nvidia-smi --query-gpu=index,name,utilization.gpu,memory.used,memory.total --format=csv,noheader",
		}
	}

	for _, cmd := range candidates {
		out, err := exec.Command("sh", "-c", cmd).Output()
		if err == nil && len(out) > 0 {
			return strings.TrimSpace(string(out))
		}
	}
	return "N/A"
}

// getRunningServices returns common running services/daemons on Linux.
func getRunningServices() string {
	if runtime.GOOS != "linux" {
		return "N/A (not Linux)"
	}

	out, err := exec.Command("systemctl", "list-units", "--type=service", "--state=running", "--no-pager", "--plain").Output()
	if err != nil {
		return "N/A"
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Running services (%d):\n", len(lines)-1))
	for i, line := range lines {
		if i == 0 || i >= 20 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			sb.WriteString("  " + fields[0] + "\n")
		}
	}
	if len(lines) > 21 {
		sb.WriteString(fmt.Sprintf("  ... and %d more\n", len(lines)-21))
	}
	return sb.String()
}

// getSystemTemperature returns a temperature summary.
func getSystemTemperature() string {
	var sb strings.Builder
	sb.WriteString("=== Temperatures ===\n")

	temps, err := host.SensorsTemperatures()
	if err == nil {
		for _, t := range temps {
			sb.WriteString(fmt.Sprintf("%s: %.1f C\n", t.SensorKey, float64(t.Temperature)))
		}
	} else {
		sb.WriteString("No sensor data available.\n")
	}

	return sb.String()
}

// getProcessTree returns a simple process tree using ps.
func getProcessTree() string {
	if runtime.GOOS != "linux" {
		return "N/A (not Linux)"
	}
	out, err := exec.Command("ps", "-eo", "pid,ppid,user,%cpu,%mem,cmd", "--sort=-%cpu").Output()
	if err != nil {
		return "N/A"
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Processes (%d):\n", len(lines)-1))
	for i, line := range lines {
		if i == 0 || i > 20 {
			continue
		}
		sb.WriteString(line + "\n")
	}
	if len(lines) > 21 {
		sb.WriteString(fmt.Sprintf("... and %d more\n", len(lines)-21))
	}
	return sb.String()
}

// scanLocalFiles scans a local directory and returns formatted entries.
func scanLocalFiles(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Sprintf("Cannot read directory: %s\n%s", dir, err.Error())
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== %s ===\n", dir))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		prefix := "[DIR] "
		if !e.IsDir() {
			prefix = fmt.Sprintf("[%8s] ", formatBytes(uint64(info.Size())))
		}
		sb.WriteString(fmt.Sprintf("%s%s\n", prefix, e.Name()))
	}
	return sb.String()
}
