package main

// powertools_backend.go — the host-facing half of the developer & power-user
// tools overlay.
//
// The rules follow the rest of the app:
//   - every command is bounded by a timeout and its output is truncated;
//   - network probes are read-only (TCP connects, DNS lookups, /proc reads);
//   - the file viewer only opens a fixed list of app files;
//   - rsync is invoked directly, never through a shell, and never with
//     --delete, so a typo in the target cannot remove files;
//   - script snippets are user-authored and run with the app's permissions,
//     which the UI states in plain language.
//
// Pure helpers take their inputs as arguments so they can be tested without
// touching the host.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	powerScriptsFile   = "power-scripts.json"
	powerBenchFile     = "power-bench.json"
	powerHistoryDir    = "power-config-history"
	powerHistoryKeep   = 20
	powerBenchKeep     = 50
	powerTimeoutScript = 30 * time.Second
	powerTimeoutRsync  = 2 * time.Minute
	powerMaxOutput     = 16000
	powerMaxRead       = 128 * 1024
)

// powerConfigHistoryEnabled is turned off by the test package's init so a
// config save in a test never litters the checkout with history files. The
// runtime app keeps it on.
var powerConfigHistoryEnabled = true

// ── Snippets ───────────────────────────────────────────────────────────────

// powerScript is one saved shell snippet.
type powerScript struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

// powerBuiltinScripts are read-only entries that work on a fresh install.
// They intentionally do not need arguments: the overlay has no text field.
func powerBuiltinScripts() []powerScript {
	if runtime.GOOS == "windows" {
		return []powerScript{
			{Name: "Network interfaces", Command: "ipconfig /all"},
			{Name: "Wi-Fi signal", Command: "netsh wlan show interfaces"},
			{Name: "Storage drives", Command: "powershell -NoProfile -Command \"Get-PSDrive -PSProvider FileSystem | Select-Object Name,Used,Free\""},
			{Name: "Running processes", Command: "tasklist"},
			{Name: "JukaHub folder", Command: "dir"},
		}
	}
	return []powerScript{
		{Name: "Disk usage", Command: "df -h"},
		{Name: "Network interfaces", Command: "ip -brief addr 2>/dev/null || ifconfig"},
		{Name: "Wi-Fi signal", Command: "cat /proc/net/wireless"},
		{Name: "Battery", Command: "cat /sys/class/power_supply/*/capacity 2>/dev/null || true"},
		{Name: "JukaHub folder", Command: "ls -la"},
	}
}

func powerBuiltinScriptCount() int { return len(powerBuiltinScripts()) }

// loadPowerScriptsFrom reads the user's snippet library. A missing file is an
// empty library, not an error. The save keeps the previous list as .bak, so a
// truncated file comes back as the snippets the user wrote instead of as an
// empty library — and the callers ignoring this error is exactly why silence
// here used to look like the snippets had been deleted.
func loadPowerScriptsFrom(path string) ([]powerScript, error) {
	var list []powerScript
	if _, err := readJSONWithBackup(path, &list); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("power-scripts.json: %w", err)
	}
	out := make([]powerScript, 0, len(list))
	for _, s := range list {
		s.Name = strings.TrimSpace(s.Name)
		s.Command = strings.TrimSpace(s.Command)
		if s.Name == "" || s.Command == "" {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func savePowerScriptsTo(path string, list []powerScript) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, data, 0600)
}

func loadPowerScripts() []powerScript {
	list, _ := loadPowerScriptsFrom(resolvePath(powerScriptsFile))
	return list
}

// allPowerScripts returns built-ins first, then the user's library.
func allPowerScripts() []powerScript {
	out := append([]powerScript(nil), powerBuiltinScripts()...)
	return append(out, loadPowerScripts()...)
}

func addPowerScript(name, command string) error {
	name = strings.TrimSpace(name)
	command = strings.TrimSpace(command)
	if command == "" {
		return errors.New("snippet is empty")
	}
	if len(command) > 4000 {
		return errors.New("snippet is longer than 4000 characters")
	}
	if name == "" {
		name = "Snippet"
	}
	list := loadPowerScripts()
	list = append(list, powerScript{Name: name, Command: command})
	if len(list) > 40 {
		list = list[len(list)-40:]
	}
	return savePowerScriptsTo(resolvePath(powerScriptsFile), list)
}

func deletePowerScript(index int) error {
	list := loadPowerScripts()
	if index < 0 || index >= len(list) {
		return errors.New("snippet not found")
	}
	list = append(list[:index], list[index+1:]...)
	return savePowerScriptsTo(resolvePath(powerScriptsFile), list)
}

// powerShellCommand chooses the platform shell. The command is a single
// user-authored string; it is passed as one argument after -c, not split by
// the app.
func powerShellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c", command}
	}
	return "sh", []string{"-c", command}
}

// runPowerScript runs a snippet with a hard timeout and returns combined
// stdout/stderr. The caller displays the trust warning.
func runPowerScript(command string, timeout time.Duration) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", errors.New("empty snippet")
	}
	if timeout <= 0 {
		timeout = powerTimeoutScript
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	name, args := powerShellCommand(command)
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	text := truncatePowerOutput(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return text + fmt.Sprintf("\n(stopped after %s)", timeout), ctx.Err()
	}
	return text, err
}

func truncatePowerOutput(s string) string {
	if len(s) <= powerMaxOutput {
		return s
	}
	return s[:3000] + "\n… output truncated …\n" + s[len(s)-(powerMaxOutput-3030):]
}

// ── Network probes ─────────────────────────────────────────────────────────

func powerDefaultPorts() []int {
	return []int{22, 53, 80, 139, 443, 445, 631, 1883, 5000, 8080, 8317, 8318, 9090}
}

func powerPortName(port int) string {
	switch port {
	case 22:
		return "ssh"
	case 53:
		return "dns"
	case 80:
		return "http"
	case 139, 445:
		return "smb"
	case 443:
		return "https"
	case 631:
		return "ipp"
	case 1883:
		return "mqtt"
	case 8080, 5000:
		return "http-alt"
	case 8317:
		return "jukahub"
	case 8318:
		return "jukahub-party"
	case 9090:
		return "prometheus"
	}
	return ""
}

// powerGatewayGuess turns the device's IPv4 address into the usual /24
// gateway address. It returns "" when there is no LAN address.
func powerGatewayGuess(ip string) string {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return ""
	}
	v4 := parsed.To4()
	if v4 == nil || v4.IsLoopback() || v4.IsLinkLocalUnicast() {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.1", v4[0], v4[1], v4[2])
}

// powerCleanHost normalises a host copied from a browser or terminal. It
// accepts an IP, host name, or host:port and strips the port; paths, URLs and
// whitespace are rejected so the scanner stays a scanner, not a URL fetcher.
func powerCleanHost(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("clipboard is empty")
	}
	if strings.Contains(raw, "://") || strings.ContainsAny(raw, " \t\r\n/\\") {
		return "", errors.New("copy a host name or IP, not a URL or path")
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	raw = strings.Trim(raw, "[]")
	if raw == "" || len(raw) > 253 {
		return "", errors.New("that does not look like a host")
	}
	return raw, nil
}

// powerScanPorts probes ports concurrently on one host. Only connection
// success is reported; an open port is not otherwise touched.
func powerScanPorts(host string, ports []int, timeout time.Duration) []int {
	if strings.TrimSpace(host) == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = 350 * time.Millisecond
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	open := make([]int, 0, len(ports))
	for _, port := range ports {
		if port <= 0 || port > 65535 {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(p int) {
			defer wg.Done()
			defer func() { <-sem }()
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(p)), timeout)
			if err == nil {
				_ = conn.Close()
				mu.Lock()
				open = append(open, p)
				mu.Unlock()
			}
		}(port)
	}
	wg.Wait()
	sort.Ints(open)
	return open
}

// powerScanText formats one scan result for the output pane.
func powerScanText(host string, timeout time.Duration) string {
	ports := powerScanPorts(host, powerDefaultPorts(), timeout)
	var b strings.Builder
	fmt.Fprintf(&b, "Host: %s\nPorts: 22 53 80 139 443 445 631 1883 5000 8080 8317 8318 9090\n\n", host)
	if len(ports) == 0 {
		b.WriteString("No open ports found. The host may block probes, or be offline.\n")
	} else {
		for _, p := range ports {
			if name := powerPortName(p); name != "" {
				fmt.Fprintf(&b, "%5d/tcp  open  %s\n", p, name)
			} else {
				fmt.Fprintf(&b, "%5d/tcp  open\n", p)
			}
		}
	}
	b.WriteString("\nRead-only: the scanner connected and closed immediately.")
	return b.String()
}

func powerResolve(host string) ([]string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, errors.New("no host name")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	return net.DefaultResolver.LookupHost(ctx, host)
}

// powerPingSummary prefers the platform's ping binary (real ICMP echo) and
// falls back to TCP connect timing when the binary is missing or blocked.
func powerPingSummary(host string) string {
	bin, err := exec.LookPath("ping")
	if err == nil {
		args := []string{"-c", "3", host}
		if runtime.GOOS == "windows" {
			args = []string{"-n", "3", "-w", "2000", host}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		out, pingErr := exec.CommandContext(ctx, bin, args...).CombinedOutput()
		cancel()
		if pingErr == nil && strings.TrimSpace(string(out)) != "" {
			return truncatePowerOutput(string(out))
		}
	}
	times := powerTCPPing(host, 443, 3, 1500*time.Millisecond)
	var b strings.Builder
	b.WriteString("ICMP ping is unavailable here; TCP connect fallback to " + host + ":443\n\n")
	ok := 0
	for i, d := range times {
		if d < 0 {
			fmt.Fprintf(&b, "try %d: timeout\n", i+1)
			continue
		}
		ok++
		fmt.Fprintf(&b, "try %d: %.0f ms\n", i+1, float64(d)/float64(time.Millisecond))
	}
	fmt.Fprintf(&b, "\n%d/3 connected", ok)
	if ok == 0 {
		b.WriteString(" — offline or TCP 443 is blocked")
	}
	return b.String()
}

// powerTraceRoute uses the platform traceroute/tracert binary if present.
// Unprivileged TCP/UDP probing is not portable, so a missing binary is
// reported plainly instead of pretending to trace.
func powerTraceRoute(host string) (string, error) {
	bin, err := exec.LookPath("traceroute")
	args := []string{"-n", "-m", "12", host}
	if err != nil {
		bin, err = exec.LookPath("tracert")
		args = []string{"-d", "-h", "12", host}
	}
	if err != nil {
		return "", errors.New("traceroute is not installed on this device; TCP ping still works")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return truncatePowerOutput(string(out)) + "\n(traceroute stopped after 30s)", ctx.Err()
	}
	return truncatePowerOutput(string(out)), err
}

// powerTCPPing measures TCP connect time. It deliberately does not claim to
// be ICMP ping: unprivileged apps cannot send echo requests on most devices.
func powerTCPPing(host string, port int, attempts int, timeout time.Duration) []time.Duration {
	if attempts <= 0 {
		attempts = 3
	}
	if timeout <= 0 {
		timeout = time.Second
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	out := make([]time.Duration, 0, attempts)
	for i := 0; i < attempts; i++ {
		start := time.Now()
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			out = append(out, -1)
			continue
		}
		out = append(out, time.Since(start))
		_ = conn.Close()
	}
	return out
}

// parsePowerWireless parses /proc/net/wireless. It returns link quality and
// signal level (dBm), with ok=false when there is no associated interface.
func parsePowerWireless(contents string) (link string, level string, ok bool) {
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, ":") || strings.HasPrefix(line, "Inter-") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}
		// parts[0] is "wlan0:", parts[1] status, parts[2] link, parts[3] level.
		l := strings.TrimSuffix(parts[2], ".")
		lv := strings.TrimSuffix(parts[3], ".")
		if l == "" || lv == "" {
			continue
		}
		return l, lv, true
	}
	return "", "", false
}

func powerWiFiSummary() string {
	if runtime.GOOS == "windows" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "netsh", "wlan", "show", "interfaces").CombinedOutput()
		if err != nil {
			return "Wi-Fi details unavailable on this device.\n" + strings.TrimSpace(string(out))
		}
		var keep []string
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			lower := strings.ToLower(line)
			if strings.HasPrefix(lower, "name") || strings.HasPrefix(lower, "state") ||
				strings.HasPrefix(lower, "ssid") || strings.HasPrefix(lower, "signal") ||
				strings.HasPrefix(lower, "receive rate") || strings.HasPrefix(lower, "transmit rate") ||
				strings.HasPrefix(lower, "channel") {
				keep = append(keep, line)
			}
		}
		if len(keep) == 0 {
			// netsh is localized; fall back to the raw report rather than
			// claiming there is no interface on a non-English Windows.
			raw := strings.TrimSpace(string(out))
			if raw == "" {
				return "No Wi-Fi interface reported by Windows."
			}
			return truncatePowerOutput(raw)
		}
		return strings.Join(keep, "\n")
	}
	// The kernel's table first, then the supplicant: the same order and the same
	// reasons as the status meter (see wifiMeterLevelFrom). An image without
	// wireless extensions has no /proc/net/wireless even while associated.
	link, level, ok := "", "", false
	if data, err := os.ReadFile("/proc/net/wireless"); err == nil {
		link, level, ok = parsePowerWireless(string(data))
	}
	if ok {
		summary := fmt.Sprintf("Source: /proc/net/wireless\nLink quality: %s/70\nSignal: %s dBm", link, level)
		if n, err := strconv.ParseFloat(level, 64); err == nil {
			summary += " " + wifiSignalVerdict(n)
		}
		return summary
	}
	sample, pollErr := wifiSignalPoll()
	if pollErr != nil {
		return "No signal report: /proc/net/wireless is missing or lists no interface, and " + pollErr.Error() + "."
	}
	summary := fmt.Sprintf("Source: wpa_cli signal_poll\nSignal: %d dBm %s", sample.RSSI, wifiSignalVerdict(float64(sample.RSSI)))
	if sample.LinkKbps > 0 {
		summary += fmt.Sprintf("\nLink speed: %d Mbit/s", sample.LinkKbps/1000)
	}
	if sample.FreqMHz > 0 {
		summary += fmt.Sprintf("\nFrequency: %d MHz", sample.FreqMHz)
	}
	if sample.Noise < 0 {
		summary += fmt.Sprintf("\nNoise floor: %d dBm", sample.Noise)
	}
	return summary
}

// wifiSignalVerdict turns a dBm reading into the word the probe shows, so the
// /proc path and the signal_poll path cannot describe the same value two ways.
func wifiSignalVerdict(dbm float64) string {
	switch {
	case dbm >= -50:
		return "(excellent)"
	case dbm >= -60:
		return "(good)"
	case dbm >= -70:
		return "(fair)"
	}
	return "(weak)"
}

// ── File inspection ────────────────────────────────────────────────────────

type powerFileInfo struct {
	Name string
	Path string
	Size int64
}

// powerKnownFiles is the complete allowlist for the viewer. Keeping it fixed
// means a hand-edited config cannot turn this into an arbitrary file reader.
func powerKnownFiles() []powerFileInfo {
	names := []string{
		"jukauser.json",
		"jukaconfig.json",
		"power-scripts.json",
		"power-bench.json",
		"logs.txt",
		"errors.txt",
		"crash.log",
	}
	var out []powerFileInfo
	for _, name := range names {
		path := resolvePath(name)
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			out = append(out, powerFileInfo{Name: name, Path: path, Size: st.Size()})
		}
	}
	return out
}

func powerFileKind(path string, data []byte) string {
	if strings.EqualFold(filepath.Ext(path), ".json") || json.Valid(data) {
		return "json"
	}
	if bytes.IndexByte(data[:minInt(len(data), 8000)], 0) >= 0 {
		return "binary"
	}
	base := strings.ToLower(filepath.Base(path))
	if strings.Contains(base, "log") || strings.Contains(base, "error") {
		return "log"
	}
	return "text"
}

func powerReadFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, powerMaxRead))
	if err != nil {
		return nil, err
	}
	return data, nil
}

func powerPrettyJSON(data []byte) ([]byte, error) {
	if !json.Valid(data) {
		return nil, errors.New("file is not valid JSON")
	}
	var out bytes.Buffer
	if err := json.Indent(&out, data, "", "  "); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func powerTailLines(text string, n int) string {
	if n <= 0 {
		return text
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return "… " + strconv.Itoa(len(lines)-n) + " earlier lines hidden …\n" +
		strings.Join(lines[len(lines)-n:], "\n")
}

func powerHexDump(data []byte, maxBytes int) string {
	if maxBytes <= 0 || maxBytes > len(data) {
		maxBytes = len(data)
	}
	var b strings.Builder
	for off := 0; off < maxBytes; off += 16 {
		end := off + 16
		if end > maxBytes {
			end = maxBytes
		}
		fmt.Fprintf(&b, "%08x  ", off)
		for i := off; i < off+16; i++ {
			if i < end {
				fmt.Fprintf(&b, "%02x ", data[i])
			} else {
				b.WriteString("   ")
			}
			if i == off+7 {
				b.WriteByte(' ')
			}
		}
		b.WriteString(" |")
		for i := off; i < end; i++ {
			c := data[i]
			if c < 32 || c > 126 {
				c = '.'
			}
			b.WriteByte(c)
		}
		b.WriteString("|\n")
	}
	if maxBytes < len(data) {
		fmt.Fprintf(&b, "… %d more bytes …", len(data)-maxBytes)
	}
	return b.String()
}

// powerPreviewBytes renders the default inspection mode for a file.
func powerPreviewBytes(path string, data []byte) (string, string) {
	kind := powerFileKind(path, data)
	switch kind {
	case "json":
		if pretty, err := powerPrettyJSON(data); err == nil {
			return powerTailLines(string(pretty), 400), "json"
		}
	case "binary":
		return powerHexDump(data, 4096), "hex"
	case "log":
		return powerTailLines(string(data), 300), "log"
	}
	return powerTailLines(string(data), 400), "text"
}

// ── Config history ─────────────────────────────────────────────────────────

type powerSnapshot struct {
	Name    string
	Path    string
	Size    int64
	ModTime time.Time
}

func powerHistoryPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), powerHistoryDir)
}

// snapshotPowerConfig copies the current config aside before it is overwritten.
// Identical content is skipped so a debounced save loop cannot fill the disk.
func snapshotPowerConfig(configPath string) error {
	if strings.TrimSpace(configPath) == "" {
		return nil
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	dir := powerHistoryPath(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	snaps, err := powerListSnapshots(configPath)
	if err != nil {
		return err
	}
	if len(snaps) > 0 {
		if newest, err := os.ReadFile(snaps[0].Path); err == nil && bytes.Equal(newest, data) {
			return nil
		}
	}
	base := strings.TrimSuffix(filepath.Base(configPath), filepath.Ext(configPath))
	stamp := time.Now().Format("20060102-150405.000")
	name := base + "-" + stamp + ".json"
	for i := 1; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("%s-%s-%d.json", base, stamp, i)
	}
	// History holds an older copy of every credential in jukauser.json; keep
	// it owner-only where the filesystem supports permissions.
	if err := AtomicWrite(filepath.Join(dir, name), data, 0600); err != nil {
		return err
	}
	return powerPruneSnapshots(configPath)
}

func powerPruneSnapshots(configPath string) error {
	snaps, err := powerListSnapshots(configPath)
	if err != nil {
		return err
	}
	for i := powerHistoryKeep; i < len(snaps); i++ {
		_ = os.Remove(snaps[i].Path)
	}
	return nil
}

func powerListSnapshots(configPath string) ([]powerSnapshot, error) {
	dir := powerHistoryPath(configPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []powerSnapshot
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, powerSnapshot{
			Name:    e.Name(),
			Path:    filepath.Join(dir, e.Name()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ModTime.Equal(out[j].ModTime) {
			return out[i].Name > out[j].Name
		}
		return out[i].ModTime.After(out[j].ModTime)
	})
	return out, nil
}

func powerRestoreSnapshot(configPath, snapshotPath string) error {
	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.Ext(configPath), ".json") && !json.Valid(data) {
		return errors.New("snapshot is not valid JSON")
	}
	return AtomicWrite(configPath, data, 0644)
}

// powerDiffLines returns a compact line diff. Only changed lines are shown;
// identical context is omitted so the pane stays readable on a handheld.
func powerDiffLines(oldText, newText string, maxLines int) string {
	if oldText == newText {
		return "No changes."
	}
	if maxLines <= 0 {
		maxLines = 240
	}
	oldLines := strings.Split(strings.ReplaceAll(oldText, "\r\n", "\n"), "\n")
	newLines := strings.Split(strings.ReplaceAll(newText, "\r\n", "\n"), "\n")
	// Trim a long identical prefix/suffix before the quadratic LCS.
	start := 0
	for start < len(oldLines) && start < len(newLines) && oldLines[start] == newLines[start] {
		start++
	}
	oldEnd, newEnd := len(oldLines), len(newLines)
	for oldEnd > start && newEnd > start && oldLines[oldEnd-1] == newLines[newEnd-1] {
		oldEnd--
		newEnd--
	}
	a := oldLines[start:oldEnd]
	b := newLines[start:newEnd]
	if len(a) > 1200 || len(b) > 1200 {
		var out []string
		for _, line := range a {
			out = append(out, "- "+line)
		}
		for _, line := range b {
			out = append(out, "+ "+line)
		}
		if len(out) > maxLines {
			out = append(out[:maxLines], "… diff truncated …")
		}
		return strings.Join(out, "\n")
	}
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			i++
			j++
		case j >= len(b) || (i < len(a) && dp[i+1][j] >= dp[i][j+1]):
			out = append(out, "- "+a[i])
			i++
		default:
			out = append(out, "+ "+b[j])
			j++
		}
		if len(out) > maxLines {
			out = append(out, "… diff truncated …")
			return strings.Join(out, "\n")
		}
	}
	if len(out) == 0 {
		return "No changes."
	}
	return strings.Join(out, "\n")
}

// ── Benchmark board ────────────────────────────────────────────────────────

type powerBenchEntry struct {
	Device  string  `json:"device"`
	Kind    string  `json:"kind,omitempty"`
	Write   float64 `json:"write"`
	Read    float64 `json:"read"`
	At      string  `json:"at"`
	Version string  `json:"version,omitempty"`
}

func powerBenchScore(e powerBenchEntry) float64 {
	if e.Write < e.Read {
		return e.Write
	}
	return e.Read
}

func loadPowerBenchFrom(path string) ([]powerBenchEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var list []powerBenchEntry
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]powerBenchEntry, 0, len(list))
	for _, e := range list {
		if e.Device == "" || e.Write <= 0 || e.Read <= 0 {
			continue
		}
		out = append(out, e)
	}
	sortPowerBench(out)
	return out, nil
}

func sortPowerBench(list []powerBenchEntry) {
	sort.SliceStable(list, func(i, j int) bool {
		return powerBenchScore(list[i]) > powerBenchScore(list[j])
	})
}

func recordPowerBenchmarkTo(path string, entry powerBenchEntry) error {
	if entry.Device == "" || entry.Write <= 0 || entry.Read <= 0 {
		return errors.New("incomplete benchmark result")
	}
	if entry.At == "" {
		entry.At = time.Now().UTC().Format(time.RFC3339)
	}
	if entry.Kind == "" {
		entry.Kind = "sequential"
	}
	list, err := loadPowerBenchFrom(path)
	if err != nil {
		list = nil
	}
	list = append(list, entry)
	sortPowerBench(list)
	if len(list) > powerBenchKeep {
		list = list[:powerBenchKeep]
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, data, 0644)
}

func recordPowerBenchmark(kind string, write, read float64, version string, extra ...powerBenchEntry) error {
	entry := powerBenchEntry{
		Device:  deviceDisplayName(DetectDevice()),
		Kind:    kind,
		Write:   write,
		Read:    read,
		Version: version,
	}
	if len(extra) > 0 && extra[0].Device != "" {
		entry.Device = extra[0].Device
	}
	return recordPowerBenchmarkTo(resolvePath(powerBenchFile), entry)
}

// powerBenchRemoteURL is optional. The environment wins for desktop testing;
// otherwise a hand-edited powerBenchURL custom variable works on the device.
// Both point at the same JSON array format.
func powerBenchRemoteURL() string {
	if env := strings.TrimSpace(os.Getenv("JUKAHUB_BENCH_URL")); env != "" {
		return env
	}
	user := loadUserConfig()
	if user != nil && user.Variables.Custom != nil {
		if v, ok := user.Variables.Custom["powerBenchURL"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func fetchPowerBench(url string) ([]powerBenchEntry, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil, errors.New("no leaderboard URL configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("leaderboard returned %s", resp.Status)
	}
	var list []powerBenchEntry
	dec := json.NewDecoder(io.LimitReader(resp.Body, 512*1024))
	if err := dec.Decode(&list); err != nil {
		return nil, err
	}
	clean := make([]powerBenchEntry, 0, len(list))
	for _, e := range list {
		if e.Device == "" || e.Write <= 0 || e.Read <= 0 {
			continue
		}
		clean = append(clean, e)
		if len(clean) >= 100 {
			break
		}
	}
	sortPowerBench(clean)
	return clean, nil
}

// powerMeasureIO runs the same 32 MiB write/read test used by the benchmark
// button and returns MB/s.
func powerMeasureIO(path string, size int64) (float64, float64, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		f.Close()
		os.Remove(path)
	}()
	if size <= 0 {
		size = 32 * 1024 * 1024
	}
	buf := make([]byte, 1024*1024)
	for i := range buf {
		buf[i] = byte(i)
	}
	start := time.Now()
	for written := int64(0); written < size; written += int64(len(buf)) {
		if _, err := f.Write(buf); err != nil {
			return 0, 0, err
		}
	}
	if err := f.Sync(); err != nil {
		return 0, 0, err
	}
	writeDur := time.Since(start)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, 0, err
	}
	start = time.Now()
	buf2 := make([]byte, 1024*1024)
	for {
		_, err := f.Read(buf2)
		if err != nil {
			break
		}
	}
	readDur := time.Since(start)
	if writeDur <= 0 || readDur <= 0 {
		return 0, 0, errors.New("benchmark too fast to measure")
	}
	return float64(size) / writeDur.Seconds() / 1e6, float64(size) / readDur.Seconds() / 1e6, nil
}

// ── Sync ───────────────────────────────────────────────────────────────────

const (
	powerSyncSourceKey = "powerSyncSource"
	powerSyncTargetKey = "powerSyncTarget"
)

func powerSyncSetting(key string) string {
	if key != powerSyncSourceKey && key != powerSyncTargetKey {
		return ""
	}
	user := loadUserConfig()
	if user == nil || user.Variables.Custom == nil {
		return ""
	}
	if v, ok := user.Variables.Custom[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func setPowerSyncSetting(key, value string) error {
	if key != powerSyncSourceKey && key != powerSyncTargetKey {
		return errors.New("unknown sync setting")
	}
	value = strings.TrimSpace(value)
	if len(value) > 512 {
		return errors.New("path is too long")
	}
	user := loadUserConfig()
	if user == nil {
		return errors.New("config unavailable")
	}
	if user.Variables.Custom == nil {
		user.Variables.Custom = make(map[string]interface{})
	}
	user.Variables.Custom[key] = value
	saveUserConfig(user)
	return nil
}

// powerRsyncArgs validates and builds the rsync argv. The target is passed as
// an argument, never through a shell; a leading dash is rejected so a pasted
// path cannot become an option. --delete is deliberately never included.
func powerRsyncArgs(source, target string, dryRun bool) ([]string, error) {
	source = strings.TrimSpace(source)
	target = strings.TrimSpace(target)
	if source == "" || target == "" {
		return nil, errors.New("set both a source and a target first")
	}
	if strings.HasPrefix(source, "-") || strings.HasPrefix(target, "-") {
		return nil, errors.New("paths cannot start with a dash")
	}
	// rsync builds a remote-shell command for user@host:path. Reject shell
	// metacharacters in the target so a pasted clipboard value cannot smuggle
	// a second command into that remote invocation.
	if strings.ContainsAny(target, ";&|`$<>\r\n") {
		return nil, errors.New("target contains characters a remote shell would interpret")
	}
	st, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	if !st.IsDir() {
		return nil, errors.New("source is not a folder")
	}
	args := []string{"-az", "--itemize-changes"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, source, target)
	return args, nil
}

func runPowerRsync(source, target string, dryRun bool, timeout time.Duration) (string, error) {
	args, err := powerRsyncArgs(source, target, dryRun)
	if err != nil {
		return "", err
	}
	bin, err := exec.LookPath("rsync")
	if err != nil {
		return "", errors.New("rsync is not installed on this device")
	}
	if timeout <= 0 {
		timeout = powerTimeoutRsync
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	text := truncatePowerOutput(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return text + "\n(sync stopped after the timeout)", ctx.Err()
	}
	if err != nil {
		return text, err
	}
	if strings.TrimSpace(text) == "" {
		return "Sync finished. Nothing needed updating.", nil
	}
	return text, nil
}

func powerSyncthingStatus() string {
	bin, binErr := exec.LookPath("syncthing")
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:8384/rest/noauth/health")
	if err != nil {
		if binErr == nil {
			return "Syncthing is installed but its web UI is not reachable on port 8384.\nStart Syncthing, then reopen this row."
		}
		return "Syncthing is not installed and no local web UI is reachable.\nUse rsync above, or install Syncthing separately."
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("Syncthing answered %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if binErr == nil {
		return "Syncthing is running on http://127.0.0.1:8384\nBinary: " + bin
	}
	return "Syncthing is running on http://127.0.0.1:8384\n" + strings.TrimSpace(string(body))
}
