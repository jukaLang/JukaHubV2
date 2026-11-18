package main

// aitools_device.go — Juka's device tools: the state of the handheld, its
// logs, the network, the running processes and (behind its own switch) a
// terminal command. See aitools.go for the registry and the risk classes.

import (
	"context"
	"encoding/csv"
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
	"syscall"
	"time"
)

func aiDeviceTools() []aiTool {
	return []aiTool{
		{
			Name:   "device_status",
			Desc:   "The handheld's state now: model, OS, JukaHub version, battery and charging, temperature, CPU clock and speed setting, memory, storage per card, Wi-Fi and Bluetooth.",
			Params: aiObj(nil),
			Risk:   aiRiskRead,
			Run: func(ctx context.Context, _ aiArgs) aiStep {
				return aiStep{Result: aiDeviceStatus(), Status: "Checked the device"}
			},
		},
		{
			Name: "read_logs",
			Desc: "Read recent log lines. source: app (JukaHub's own log, default), errors (errors.txt: warnings, errors, crashes), game (the last RetroArch game's log). filter: only lines containing this text or subsystem (e.g. wifi, bluetooth, emulators, groq).",
			Params: aiObj(map[string]any{
				"source": aiEnumP("Which log", "app", "errors", "game"),
				"filter": aiStrP("Only lines containing this text (case-insensitive)"),
				"lines":  aiIntP("How many of the newest matching lines, 1-120 (default 40)"),
			}),
			Risk: aiRiskRead,
			Run:  aiReadLogs,
		},
		{
			Name:   "network_check",
			Desc:   "Check the connection: online state, Wi-Fi radio and signal, DNS, an HTTPS request to the internet, Bluetooth radio.",
			Params: aiObj(nil),
			Risk:   aiRiskRead,
			Run: func(ctx context.Context, _ aiArgs) aiStep {
				return aiStep{Result: aiNetworkCheck(ctx), Status: "Checked the network"}
			},
		},
		{
			Name:   "list_processes",
			Desc:   "The running processes using the most memory (pid, name, memory).",
			Params: aiObj(map[string]any{"limit": aiIntP("How many, 1-40 (default 15)")}),
			Risk:   aiRiskRead,
			Run:    aiListProcesses,
		},
		{
			Name:   "stop_process",
			Desc:   "Stop a running process by pid (from list_processes). The user is asked first. System processes and JukaHub itself are refused.",
			Params: aiObj(map[string]any{"pid": aiIntP("The process id")}, "pid"),
			Risk:   aiRiskDestructive,
			Run:    aiStopProcess,
		},
		{
			Name:   "run_command",
			Desc:   "Run one shell command on the device (sh on the handheld, cmd on Windows), 20 s limit, output capped. The user sees the exact command and must allow it. Prefer the other tools; use this only when none fits.",
			Params: aiObj(map[string]any{"command": aiStrP("The exact command line")}, "command"),
			Risk:   aiRiskDestructive,
			Run:    aiRunCommand,
		},
	}
}

// ── device_status ────────────────────────────────────────────────────────

func aiDeviceStatus() string {
	var b strings.Builder
	line := func(key, format string, args ...any) {
		fmt.Fprintf(&b, "%s: %s\n", key, fmt.Sprintf(format, args...))
	}
	line("Device", "%s (%s/%s)", deviceDisplayName(DetectDevice()), runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "linux" {
		if osName := strings.TrimSpace(getOSName()); osName != "" {
			line("OS", "%s", osName)
		}
	}
	line("JukaHub", "%s", versionString(appConfig))
	if pct := getBatteryPercent(); pct >= 0 {
		state := "not charging"
		if getBatteryCharging() {
			state = "charging"
		}
		line("Battery", "%d%% (%s)", pct, state)
	}
	if h := batteryHealthReport(); batteryHealthRelevant(h) {
		line("Battery health", "%s", batteryHealthLine(h))
	}
	if t, ok := cpuTempValueC(); ok {
		line("Temperature", "%.0f °C", t)
	}
	if runtime.GOOS == "linux" {
		gov := aiReadTrim("/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor")
		maxKHz, _ := strconv.Atoi(aiReadTrim("/sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq"))
		if mhz := sysToolCPUFreqMHz(); mhz > 0 || gov != "" {
			line("CPU", "%d cores, %d MHz now, ceiling %d MHz, governor %s, load %s", runtime.NumCPU(), mhz, maxKHz/1000, aiOr(gov, "?"), aiOr(sysToolLoadAvg(), "?"))
		}
	}
	if appConfig != nil {
		line("Automatic CPU speed", "%s", autoPerfSettingDetail(appConfig))
		line("Power mode", "%s", powerModeLabel(currentPowerMode(appConfig)))
	}
	if used, total, ok := sysToolMemory(); ok && total > 0 {
		line("Memory", "%s used of %s (%d MB free)", aiSize(int64(used)), aiSize(int64(total)), freeMemoryMB())
	}
	for _, v := range sysToolVolumes() {
		line("Storage "+v.Name, "%s free of %s", aiSize(int64(v.Free)), aiSize(int64(v.Total)))
	}
	if sd := sdHealthCached(); sd.Total > 0 || len(sd.Findings) > 0 {
		line("SD card", "%s", sdHealthLine(sd))
		for _, f := range sd.Findings {
			line("SD warning", "%s", f)
		}
	}
	line("Network", "%s", NetworkStatus())
	if on, known := wifiEnabled(); known {
		line("Wi-Fi radio", "%s", setOnOff(on))
	}
	if runtime.GOOS == "linux" {
		line("Bluetooth radio", "%s", setOnOff(btRadioIsOn()))
	}
	if up, ok := sysToolUptime(); ok {
		line("Up for", "%s", up.Round(time.Minute))
	}
	return b.String()
}

func aiReadTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func aiOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// ── read_logs ────────────────────────────────────────────────────────────

func aiReadLogs(ctx context.Context, a aiArgs) aiStep {
	want := a.num("lines", 40, 1, 120)
	filter := strings.ToLower(a.str("filter"))
	source := strings.ToLower(a.str("source"))
	var lines []string
	label := "JukaHub's log"
	switch source {
	case "errors":
		dir, err := crashLogDir()
		if err != nil {
			return aiFail("no log folder: %v", err)
		}
		lines = aiTailLines(filepath.Join(dir, "errors.txt"), 96<<10)
		label = "errors.txt"
	case "game":
		dir := gameDiagLogDir()
		if dir == "" {
			return aiFail("no game log folder")
		}
		lines = aiTailLines(filepath.Join(dir, "retroarch.log"), 96<<10)
		label = "the last game's RetroArch log"
	default:
		lines, _ = appLog.Snapshot()
	}
	var kept []string
	for i := len(lines) - 1; i >= 0 && len(kept) < want; i-- {
		l := strings.TrimRight(lines[i], "\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(l), filter) {
			continue
		}
		if len(l) > 300 {
			l = l[:300] + "…"
		}
		kept = append(kept, l)
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	status := fmt.Sprintf("Read %d log lines", len(kept))
	if len(kept) == 0 {
		msg := "No lines in " + label
		if filter != "" {
			msg += " mention " + strconvQuote(filter)
		}
		return aiStep{Result: msg + ".", Status: status}
	}
	return aiStep{Result: "From " + label + " (oldest first):\n" + strings.Join(kept, "\n"), Status: status}
}

// strconvQuote quotes a short string for a result line.
func strconvQuote(s string) string { return strconv.Quote(s) }

// aiTailLines reads the last max bytes of a text file as lines.
func aiTailLines(path string, max int64) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	start := info.Size() - max
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, max))
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // the first line was cut in the middle
	}
	return lines
}

// ── network_check ────────────────────────────────────────────────────────

func aiNetworkCheck(ctx context.Context) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Network: %s\n", NetworkStatus())
	if on, known := wifiEnabled(); known {
		fmt.Fprintf(&b, "Wi-Fi radio: %s\n", setOnOff(on))
	}
	sig := strings.Split(strings.TrimSpace(powerWiFiSummary()), "\n")
	for i, l := range sig {
		l = strings.TrimSpace(l)
		// The network's name stays on the device.
		if i >= 6 || l == "" || strings.HasPrefix(strings.ToLower(l), "ssid") || strings.HasPrefix(strings.ToLower(l), "bssid") {
			continue
		}
		fmt.Fprintf(&b, "Wi-Fi: %s\n", l)
	}
	dctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	t0 := time.Now()
	addrs, err := net.DefaultResolver.LookupHost(dctx, "www.google.com")
	cancel()
	if err != nil || len(addrs) == 0 {
		fmt.Fprintf(&b, "DNS: failed (%v)\n", aiShortErr(err))
	} else {
		fmt.Fprintf(&b, "DNS: works (%d ms)\n", time.Since(t0).Milliseconds())
	}
	hctx, hcancel := context.WithTimeout(ctx, 6*time.Second)
	defer hcancel()
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, "https://www.gstatic.com/generate_204", nil)
	if err == nil {
		t1 := time.Now()
		resp, herr := http.DefaultClient.Do(req)
		if herr != nil {
			fmt.Fprintf(&b, "HTTPS: failed (%v)\n", aiShortErr(herr))
		} else {
			resp.Body.Close()
			fmt.Fprintf(&b, "HTTPS: HTTP %d in %d ms\n", resp.StatusCode, time.Since(t1).Milliseconds())
		}
	}
	if runtime.GOOS == "linux" {
		fmt.Fprintf(&b, "Bluetooth radio: %s\n", setOnOff(btRadioIsOn()))
	}
	return b.String()
}

func aiShortErr(err error) string {
	if err == nil {
		return "no answer"
	}
	s := err.Error()
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// ── processes ────────────────────────────────────────────────────────────

type aiProc struct {
	pid  int
	name string
	rss  int64
}

// aiProcesses lists the running processes with their resident memory.
func aiProcesses(ctx context.Context) []aiProc {
	if runtime.GOOS == "windows" {
		return aiWindowsProcesses(ctx)
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []aiProc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		p := aiProc{pid: pid}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status"))
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(data), "\n") {
			switch {
			case strings.HasPrefix(l, "Name:"):
				p.name = strings.TrimSpace(strings.TrimPrefix(l, "Name:"))
			case strings.HasPrefix(l, "VmRSS:"):
				p.rss = parseKilobytes(strings.TrimPrefix(l, "VmRSS:"))
			}
		}
		if p.rss == 0 {
			continue // a kernel thread
		}
		out = append(out, p)
	}
	return out
}

func aiWindowsProcesses(ctx context.Context) []aiProc {
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(cctx, "tasklist", "/fo", "csv", "/nh").Output()
	if err != nil {
		return nil
	}
	recs, err := csv.NewReader(strings.NewReader(string(raw))).ReadAll()
	if err != nil {
		return nil
	}
	var out []aiProc
	for _, r := range recs {
		if len(r) < 5 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(r[1]))
		if err != nil {
			continue
		}
		digits := strings.Map(func(c rune) rune {
			if c >= '0' && c <= '9' {
				return c
			}
			return -1
		}, r[4])
		kb, _ := strconv.ParseInt(digits, 10, 64)
		out = append(out, aiProc{pid: pid, name: r[0], rss: kb * 1024})
	}
	return out
}

func aiListProcesses(ctx context.Context, a aiArgs) aiStep {
	limit := a.num("limit", 15, 1, 40)
	procs := aiProcesses(ctx)
	if len(procs) == 0 {
		return aiFail("could not read the process list here")
	}
	sort.Slice(procs, func(i, j int) bool { return procs[i].rss > procs[j].rss })
	if len(procs) > limit {
		procs = procs[:limit]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "pid  memory  name (JukaHub is pid %d)\n", os.Getpid())
	for _, p := range procs {
		fmt.Fprintf(&b, "%d  %s  %s\n", p.pid, aiSize(p.rss), p.name)
	}
	return aiStep{Result: b.String(), Status: "Listed running processes"}
}

// aiProtectedProcs are never stopped: the system, its daemons, JukaHub.
var aiProtectedProcs = map[string]bool{
	"init": true, "systemd": true, "kthreadd": true, "udevd": true, "systemd-udevd": true,
	"dbus-daemon": true, "wpa_supplicant": true, "bluetoothd": true, "adbd": true, "sshd": true,
	"dropbear": true, "keymon": true, "mainui": true, "jukahub": true, "jukahub.exe": true,
	"csrss.exe": true, "wininit.exe": true, "winlogon.exe": true, "services.exe": true,
	"lsass.exe": true, "smss.exe": true, "explorer.exe": true, "dwm.exe": true, "svchost.exe": true,
	"system": true, "registry": true, "ash": true, "sh": true, "bash": true,
}

func aiStopProcess(ctx context.Context, a aiArgs) aiStep {
	pid := a.num("pid", 0, 0, 1<<30)
	if pid <= 1 || pid == os.Getpid() || pid == os.Getppid() {
		return aiFail("that process is the system or JukaHub itself")
	}
	var target *aiProc
	for _, p := range aiProcesses(ctx) {
		if p.pid == pid {
			p := p
			target = &p
			break
		}
	}
	if target == nil {
		return aiFail("no running process has pid %d", pid)
	}
	if aiProtectedProcs[strings.ToLower(target.name)] {
		return aiFail("%s is part of the system and is not stopped", target.name)
	}
	name := target.name
	return aiStep{
		Confirm: fmt.Sprintf("Stop “%s” (pid %d, %s)? Anything unsaved in it is lost.", name, pid, aiSize(target.rss)),
		Apply: func(ctx context.Context) string {
			proc, err := os.FindProcess(pid)
			if err != nil {
				return "Error: " + err.Error()
			}
			if runtime.GOOS != "windows" {
				if err := proc.Signal(syscall.SIGTERM); err == nil {
					for i := 0; i < 20; i++ {
						time.Sleep(100 * time.Millisecond)
						if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); err != nil {
							logInfo("aitools", "stopped %s (pid %d) at the user's OK", name, pid)
							return "Stopped " + name + "."
						}
					}
				}
			}
			if err := proc.Kill(); err != nil {
				return "Error: could not stop " + name + ": " + err.Error()
			}
			logInfo("aitools", "killed %s (pid %d) at the user's OK", name, pid)
			return "Stopped " + name + "."
		},
	}
}

// ── run_command ──────────────────────────────────────────────────────────

func aiRunCommand(ctx context.Context, a aiArgs) aiStep {
	if !aiShellOn() {
		return aiFail("terminal commands are off (Settings → Voice & AI → Let Juka run terminal commands)")
	}
	cmd := strings.TrimSpace(a.str("command"))
	// The card must show the exact command: one line, short enough to be
	// shown whole (a longer one cut at 160 hid its tail from the user).
	if cmd == "" || len(cmd) > 160 || strings.ContainsAny(cmd, "\x00\r\n") {
		return aiFail("give one command line under 160 characters")
	}
	return aiStep{
		Confirm: "Run this command on the device?\n" + cmd,
		Apply: func(ctx context.Context) string {
			logInfo("aitools", "running a terminal command at the user's OK")
			out, err := runPowerScript(cmd, 20*time.Second)
			res := strings.TrimSpace(out)
			if res == "" {
				res = "(no output)"
			}
			if err != nil {
				res += "\nExit: " + aiShortErr(err)
			}
			return res
		},
	}
}
