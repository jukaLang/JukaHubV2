package main

// systool_hw.go — the Hardware screen (config scene "Hardware"): a live
// monitor.
//
// While the screen is up a scene task samples the device every 1.5 seconds
// with cheap reads — /proc/stat, /proc/meminfo, the thermal zone, cpufreq and
// the battery's sysfs files on the handheld, gopsutil's counters on Windows —
// and the slower ones less often (storage every 6 s, the process list every
// 4.5 s). The screen draws tiles with history lines from those samples; the
// render thread never reads a file. Y opens the process manager, X the Wi-Fi
// settings, and leaving the screen stops the sampling.

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/veandco/go-sdl2/sdl"
)

// sysToolHWEvery is the sampling period: live enough to watch a game load,
// rare enough to cost the handheld nothing measurable.
const sysToolHWEvery = 1500 * time.Millisecond

// sysToolCPUTimes is the aggregate CPU line of /proc/stat, reduced to the two
// numbers usage needs.
type sysToolCPUTimes struct {
	busy, total uint64
}

// sysToolParseCPUTimes reads the "cpu ..." line: user nice system idle iowait
// irq softirq steal. Idle and iowait are idle; the rest is busy.
func sysToolParseCPUTimes(line string) (sysToolCPUTimes, bool) {
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return sysToolCPUTimes{}, false
	}
	var vals [8]uint64
	n := 0
	for _, f := range fields[1:] {
		if n == len(vals) {
			break
		}
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return sysToolCPUTimes{}, false
		}
		vals[n] = v
		n++
	}
	var total uint64
	for i := 0; i < n; i++ {
		total += vals[i]
	}
	idle := vals[3]
	if n > 4 {
		idle += vals[4]
	}
	if idle > total {
		idle = total
	}
	return sysToolCPUTimes{busy: total - idle, total: total}, true
}

// sysToolCPUPercent is the share of time the CPU was busy between two reads.
func sysToolCPUPercent(prev, cur sysToolCPUTimes) float64 {
	if cur.total <= prev.total || cur.busy < prev.busy {
		return 0
	}
	pct := float64(cur.busy-prev.busy) / float64(cur.total-prev.total) * 100
	if pct > 100 {
		pct = 100
	}
	return pct
}

func sysToolReadCPUTimes() (sysToolCPUTimes, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return sysToolCPUTimes{}, false
	}
	line := string(data)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return sysToolParseCPUTimes(line)
}

// sysToolGopsutilCPU is the CPU reading where there is no /proc: usage since
// the previous call. gopsutil can panic on odd systems; that is a missing
// reading, never a crash.
func sysToolGopsutilCPU() (pct float64, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			pct, ok = 0, false
		}
	}()
	vals, err := cpu.Percent(0, false)
	if err != nil || len(vals) == 0 {
		return 0, false
	}
	return vals[0], true
}

// sysToolParseMeminfo reads MemTotal and MemAvailable from /proc/meminfo.
func sysToolParseMeminfo(text string) (used, total uint64, ok bool) {
	var memTotal, avail int64 = -1, -1
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			memTotal = parseKilobytes(strings.TrimPrefix(line, "MemTotal:"))
		case strings.HasPrefix(line, "MemAvailable:"):
			avail = parseKilobytes(strings.TrimPrefix(line, "MemAvailable:"))
		}
	}
	if memTotal <= 0 || avail < 0 || avail > memTotal {
		return 0, 0, false
	}
	return uint64(memTotal - avail), uint64(memTotal), true
}

func sysToolMemory() (used, total uint64, ok bool) {
	if runtime.GOOS == "linux" {
		if data, err := os.ReadFile("/proc/meminfo"); err == nil {
			if u, t, good := sysToolParseMeminfo(string(data)); good {
				return u, t, true
			}
		}
	}
	return sysToolGopsutilMemory()
}

func sysToolGopsutilMemory() (used, total uint64, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			used, total, ok = 0, 0, false
		}
	}()
	vm, err := mem.VirtualMemory()
	if err != nil || vm == nil || vm.Total == 0 {
		return 0, 0, false
	}
	return vm.Used, vm.Total, true
}

// sysToolCPUFreqMHz is the first core's current clock, or 0.
func sysToolCPUFreqMHz() int {
	data, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq")
	if err != nil {
		return 0
	}
	khz, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || khz <= 0 {
		return 0
	}
	return khz / 1000
}

// sysToolUptime is how long the system has been up.
func sysToolUptime() (time.Duration, bool) {
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(data)); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil && v > 0 {
				return time.Duration(v * float64(time.Second)), true
			}
		}
	}
	return sysToolGopsutilUptime()
}

func sysToolGopsutilUptime() (d time.Duration, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			d, ok = 0, false
		}
	}()
	secs, err := host.Uptime()
	if err != nil || secs == 0 {
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}

// sysToolLoadAvg is the one-minute load average, or "".
func sysToolLoadAvg() string {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return ""
	}
	if f := strings.Fields(string(data)); len(f) > 0 {
		return f[0]
	}
	return ""
}

// sysToolPrimaryIP picks the address people mean by "my IP": a Wi-Fi
// interface first, then wired, then whatever else is up (virtual adapters
// last), IPv4 and not link-local.
func sysToolPrimaryIP() (iface, ip string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", ""
	}
	best := -1
	for _, in := range ifaces {
		if in.Flags&net.FlagLoopback != 0 || in.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := in.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			name := strings.ToLower(in.Name)
			score := 1
			switch {
			case strings.HasPrefix(name, "wl") || strings.Contains(name, "wi-fi") || strings.Contains(name, "wireless"):
				score = 3
			case strings.HasPrefix(name, "eth") || strings.HasPrefix(name, "en") || strings.Contains(name, "ethernet"):
				score = 2
			case strings.Contains(name, "virtual") || strings.Contains(name, "vethernet") || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-"):
				score = 0
			}
			if score > best {
				best, iface, ip = score, in.Name, ipn.IP.String()
			}
		}
	}
	return iface, ip
}

// sysToolHWInfo is what does not change while the screen is up.
type sysToolHWInfo struct {
	host  string
	os    string
	model string
	cores int
}

func sysToolHWReadInfo() sysToolHWInfo {
	info := sysToolHWInfo{cores: runtime.NumCPU()}
	info.host, _ = os.Hostname()
	if runtime.GOOS == "linux" {
		info.os = getOSName()
		if model := getCPUModel(); model != "N/A" {
			info.model = model
		}
	}
	if info.os == "" || info.os == runtime.GOOS || info.model == "" {
		osName, model := sysToolGopsutilInfo()
		if info.os == "" || info.os == runtime.GOOS {
			info.os = osName
		}
		if info.model == "" {
			info.model = model
		}
	}
	if info.os == "" {
		info.os = runtime.GOOS
	}
	return info
}

func sysToolGopsutilInfo() (osName, model string) {
	defer func() {
		if r := recover(); r != nil {
			osName, model = "", ""
		}
	}()
	if hi, err := host.Info(); err == nil && hi != nil {
		osName = strings.TrimSpace(hi.Platform + " " + hi.PlatformVersion)
	}
	if ci, err := cpu.Info(); err == nil && len(ci) > 0 {
		model = strings.TrimSpace(ci[0].ModelName)
	}
	return osName, model
}

// sysToolHWSample is one round of readings. The slow readings are only taken
// on some rounds; their read flags say which.
type sysToolHWSample struct {
	cpuPct    float64
	cpuOK     bool
	memUsed   uint64
	memTotal  uint64
	memOK     bool
	tempC     float64
	tempOK    bool
	freqMHz   int
	battery   int
	charging  bool
	uptime    time.Duration
	uptimeOK  bool
	load      string
	diskRead  bool
	diskTotal uint64
	diskFree  uint64
	netRead   bool
	iface     string
	ip        string
	procsRead bool
	procs     []procRow
}

// sysToolHWProcRow is one line of the busy-process card.
type sysToolHWProcRow struct {
	name string
	cpu  string
	mem  string
}

// sysToolHWData is the monitor's state as the screen draws it.
type sysToolHWData struct {
	running  bool
	samples  int
	info     sysToolHWInfo
	subtitle string
	cpuHist  sysToolRing
	memHist  sysToolRing
	tempHist sysToolRing
	memFrac  float64
	tempFrac float64
	batFrac  float64
	diskFrac float64
	cpuVal   string
	cpuSub   string
	memVal   string
	memSub   string
	tempVal  string
	tempSub  string
	batVal   string
	batSub   string
	diskVal  string
	diskSub  string
	netVal   string
	netSub   string
	status   string
	tempOK   bool
	batOK    bool
	procs    []sysToolHWProcRow
}

type sysToolHWState struct {
	mu      sync.Mutex
	gen     uint64
	version uint64
	d       sysToolHWData
}

var sysToolHW sysToolHWState

func sysToolHWUpdate(gen uint64, fn func(d *sysToolHWData)) {
	sysToolHW.mu.Lock()
	defer sysToolHW.mu.Unlock()
	if gen != sysToolHW.gen {
		return
	}
	fn(&sysToolHW.d)
	sysToolHW.version++
}

// sysToolHWRead takes one round of readings.
func sysToolHWRead(tick int, prev *sysToolCPUTimes, havePrev *bool) sysToolHWSample {
	var s sysToolHWSample
	if runtime.GOOS == "linux" {
		if cur, ok := sysToolReadCPUTimes(); ok {
			if *havePrev {
				s.cpuPct, s.cpuOK = sysToolCPUPercent(*prev, cur), true
			}
			*prev, *havePrev = cur, true
		}
	} else {
		s.cpuPct, s.cpuOK = sysToolGopsutilCPU()
	}
	s.memUsed, s.memTotal, s.memOK = sysToolMemory()
	s.tempC, s.tempOK = cpuTempValueC()
	s.freqMHz = sysToolCPUFreqMHz()
	s.battery = getBatteryPercent()
	if s.battery >= 0 {
		s.charging = getBatteryCharging()
	}
	s.uptime, s.uptimeOK = sysToolUptime()
	s.load = sysToolLoadAvg()
	if tick%4 == 0 {
		s.diskRead = true
		s.diskTotal, s.diskFree, _ = sysToolVolumeUsage(sysToolAppVolumePath())
		s.netRead = true
		s.iface, s.ip = sysToolPrimaryIP()
	}
	if tick%3 == 0 {
		if rows, err := collectProcesses(5); err == nil {
			s.procsRead, s.procs = true, rows
		}
	}
	return s
}

// sysToolHWApply folds one round into the state and formats its strings, so
// the render thread only copies them.
func sysToolHWApply(d *sysToolHWData, s sysToolHWSample) {
	d.samples++
	if s.cpuOK {
		d.cpuHist.push(s.cpuPct)
		d.cpuVal = fmt.Sprintf("%.0f", s.cpuPct)
	} else if d.cpuVal == "" {
		d.cpuVal = "…"
	}
	var parts []string
	if s.freqMHz > 0 {
		if s.freqMHz >= 1000 {
			parts = append(parts, fmt.Sprintf("%.1f GHz", float64(s.freqMHz)/1000))
		} else {
			parts = append(parts, fmt.Sprintf("%d MHz", s.freqMHz))
		}
	}
	if s.load != "" {
		parts = append(parts, "load "+s.load)
	}
	if len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%d cores", d.info.cores))
	}
	d.cpuSub = strings.Join(parts, " · ")

	if s.memOK && s.memTotal > 0 {
		d.memFrac = float64(s.memUsed) / float64(s.memTotal)
		d.memHist.push(d.memFrac * 100)
		d.memVal = fmt.Sprintf("%.0f", d.memFrac*100)
		d.memSub = formatBytes(s.memUsed) + " of " + formatBytes(s.memTotal)
	} else if d.memVal == "" {
		d.memVal, d.memSub = "—", "Not reported here"
	}

	d.tempOK = s.tempOK
	if s.tempOK {
		d.tempHist.push(s.tempC)
		d.tempVal = fmt.Sprintf("%.1f", s.tempC)
		d.tempFrac = (s.tempC - 30) / 60
		if d.tempFrac < 0 {
			d.tempFrac = 0
		}
		if d.tempFrac > 1 {
			d.tempFrac = 1
		}
		switch {
		case s.tempC < 50:
			d.tempSub = "Cool"
		case s.tempC < 70:
			d.tempSub = "Warm"
		default:
			d.tempSub = "Hot — give it a rest"
		}
	} else {
		d.tempVal, d.tempSub, d.tempFrac = "—", "Not reported on this system", 0
	}

	d.batOK = s.battery >= 0
	if d.batOK {
		d.batVal = strconv.Itoa(s.battery)
		d.batFrac = float64(s.battery) / 100
		d.batSub = "On battery"
		if s.charging {
			d.batSub = "Charging"
		}
	} else {
		d.batVal, d.batSub, d.batFrac = "—", "No battery reported", 0
	}

	if s.diskRead {
		if s.diskTotal > 0 {
			used := s.diskTotal - s.diskFree
			if s.diskFree > s.diskTotal {
				used = 0
			}
			d.diskFrac = float64(used) / float64(s.diskTotal)
			d.diskVal = fmt.Sprintf("%.0f", d.diskFrac*100)
			d.diskSub = formatBytes(s.diskFree) + " free of " + formatBytes(s.diskTotal)
		} else {
			d.diskVal, d.diskSub, d.diskFrac = "—", "Not reported", 0
		}
	}
	if s.netRead {
		if s.ip != "" {
			d.netVal = s.ip
			d.netSub = s.iface + " · no internet"
			if IsOnline() {
				d.netSub = s.iface + " · online"
			}
		} else {
			d.netVal, d.netSub = "Not connected", "X opens Wi-Fi settings"
		}
	}
	if s.procsRead {
		rows := make([]sysToolHWProcRow, 0, len(s.procs))
		for _, p := range s.procs {
			rows = append(rows, sysToolHWProcRow{
				name: p.Name,
				cpu:  fmt.Sprintf("%.1f%%", p.CPU),
				mem:  fmt.Sprintf("%.1f%%", p.Mem),
			})
		}
		d.procs = rows
	}
	d.status = "Live"
	if s.uptimeOK {
		d.status = "Live · up " + sysToolDuration(s.uptime)
	}
}

// sysToolHWEnter starts the sampler for this visit.
func sysToolHWEnter() {
	sysToolHW.mu.Lock()
	sysToolHW.gen++
	gen := sysToolHW.gen
	sysToolHW.d.running = true
	sysToolHW.version++
	sysToolHW.mu.Unlock()
	runSceneTask(func(ctx context.Context) {
		defer sysToolHWUpdate(gen, func(d *sysToolHWData) { d.running = false })
		info := sysToolHWReadInfo()
		sysToolHWUpdate(gen, func(d *sysToolHWData) {
			d.info = info
			parts := []string{}
			if info.model != "" {
				parts = append(parts, info.model)
			}
			parts = append(parts, fmt.Sprintf("%d cores", info.cores), info.os)
			d.subtitle = strings.Join(parts, " · ")
		})
		var prev sysToolCPUTimes
		havePrev := false
		// A first CPU reading to measure the next one against, so the first
		// tile shows a number after a fraction of a second, not a period.
		if runtime.GOOS == "linux" {
			prev, havePrev = sysToolReadCPUTimes()
		} else {
			sysToolGopsutilCPU()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(400 * time.Millisecond):
		}
		for tick := 0; ; tick++ {
			s := sysToolHWRead(tick, &prev, &havePrev)
			if ctx.Err() != nil {
				return
			}
			sysToolHWUpdate(gen, func(d *sysToolHWData) { sysToolHWApply(d, s) })
			select {
			case <-ctx.Done():
				return
			case <-time.After(sysToolHWEvery):
			}
		}
	})
}

var sysToolHWView struct {
	version  uint64
	built    bool
	d        sysToolHWData
	netRect  sdl.Rect
	procRect sdl.Rect
	scratch  [sysToolRingSize]float64
}

func sysToolHWSync() {
	sysToolHW.mu.Lock()
	if sysToolHWView.built && sysToolHWView.version == sysToolHW.version {
		sysToolHW.mu.Unlock()
		return
	}
	sysToolHWView.version = sysToolHW.version
	sysToolHWView.d = sysToolHW.d
	sysToolHW.mu.Unlock()
	sysToolHWView.built = true
}

// sysToolHWFooterHints is the Hardware screen's footer row.
func sysToolHWFooterHints() []FooterHint {
	return []FooterHint{{Button: "B", Label: "Back"}, {Button: "Y", Label: "Processes"}, {Button: "X", Label: "Wi-Fi"}}
}

func renderSysToolHW(renderer *sdl.Renderer, config *Config) {
	sysToolHWSync()
	v := &sysToolHWView
	d := &v.d
	subtitle := d.subtitle
	if subtitle == "" {
		subtitle = "Reading the hardware…"
	}
	area := sysToolDrawPage(renderer, config, "Hardware", sysToolPageSpec{
		Title:       "Hardware",
		Subtitle:    subtitle,
		Icon:        "hardware",
		Status:      d.status,
		StatusColor: glassAccent(),
	})
	f, ok := sysToolLoadFonts(config)
	if !ok {
		sysToolFinishPage(renderer, config)
		return
	}
	v.netRect, v.procRect = sdl.Rect{}, sdl.Rect{}
	gap := int32(12)
	rowH := int32(f.body.Height()) + 8
	procH := int32(f.body.Height()) + 34 + 5*rowH
	tilesH := area.H
	showProcs := area.H-procH-gap >= 2*130+gap
	if showProcs {
		tilesH = area.H - procH - gap
	}
	tileW := (area.W - 2*gap) / 3
	tileH := (tilesH - gap) / 2
	cell := func(i int) sdl.Rect {
		col, row := int32(i%3), int32(i/3)
		return sdl.Rect{X: area.X + col*(tileW+gap), Y: area.Y + row*(tileH+gap), W: tileW, H: tileH}
	}
	accent := glassAccent()
	waiting := d.samples == 0

	chart := sysToolDrawStat(renderer, f, cell(0), sysToolStat{Label: "Processor", Icon: "cpu", Value: d.cpuVal, Unit: "%", Sub: d.cpuSub, Frac: -1, Dim: waiting})
	sysToolSparkline(renderer, chart, d.cpuHist.ordered(&v.scratch), sysToolRingSize, 100, accent)

	chart = sysToolDrawStat(renderer, f, cell(1), sysToolStat{Label: "Memory", Icon: "memory", Value: d.memVal, Unit: "%", Sub: d.memSub,
		Frac: d.memFrac, Color: sysToolHeatColor(d.memFrac), Dim: waiting})
	sysToolSparkline(renderer, chart, d.memHist.ordered(&v.scratch), sysToolRingSize, 100, accent)

	tempUnit := "°C"
	if !d.tempOK {
		tempUnit = ""
	}
	tempFrac := d.tempFrac
	if !d.tempOK {
		tempFrac = -1
	}
	chart = sysToolDrawStat(renderer, f, cell(2), sysToolStat{Label: "Temperature", Icon: "thermometer", Value: d.tempVal, Unit: tempUnit, Sub: d.tempSub,
		Frac: tempFrac, Color: sysToolHeatColor(d.tempFrac), Dim: !d.tempOK})
	if d.tempOK {
		top := d.tempHist.peak() + 10
		if top < 60 {
			top = 60
		}
		sysToolSparkline(renderer, chart, d.tempHist.ordered(&v.scratch), sysToolRingSize, top, sysToolHeatColor(d.tempFrac))
	}

	batUnit, batFrac := "%", d.batFrac
	batColor := ColorSuccess
	if d.batFrac < 0.2 {
		batColor = ColorDanger
	}
	if !d.batOK {
		batUnit, batFrac = "", -1
	}
	sysToolDrawStat(renderer, f, cell(3), sysToolStat{Label: "Battery", Icon: "battery", Value: d.batVal, Unit: batUnit, Sub: d.batSub,
		Frac: batFrac, Color: batColor, Dim: !d.batOK})

	sysToolDrawStat(renderer, f, cell(4), sysToolStat{Label: "Storage used", Icon: "disk", Value: d.diskVal, Unit: "%", Sub: d.diskSub,
		Frac: d.diskFrac, Color: sysToolHeatColor(d.diskFrac), Dim: d.diskVal == "" || d.diskVal == "—"})

	netCell := cell(5)
	netVal := d.netVal
	if netVal == "" {
		netVal = "…"
	}
	sysToolDrawStat(renderer, f, netCell, sysToolStat{Label: "Network", Icon: "wifi", Value: netVal, Sub: d.netSub, Frac: -1,
		Active: sysToolPointIn(netCell)})
	v.netRect = netCell

	if showProcs {
		card := sdl.Rect{X: area.X, Y: area.Y + tilesH + gap, W: area.W, H: procH}
		sysToolCard(renderer, card, false)
		pad := int32(18)
		drawText(renderer, f.label, "BUSY PROCESSES", card.X+pad, card.Y+14, ColorTextTertiary(), textAlignLeft)
		label := "All processes"
		aw := sysToolActionWidth(f, "Y", label)
		v.procRect = sysToolAction(renderer, f, card.X+card.W-pad-aw, card.Y+8, "Y", label, false)
		colMem := card.X + card.W - pad
		colCPU := colMem - 110
		y := card.Y + 26 + int32(f.label.Height())
		if len(d.procs) == 0 {
			drawText(renderer, f.body, "Reading the process list…", card.X+pad, y+4, ColorTextTertiary(), textAlignLeft)
		}
		for i, p := range d.procs {
			if i >= 5 || y+rowH > card.Y+card.H-6 {
				break
			}
			drawText(renderer, f.body, ellipsize(f.body, p.name, colCPU-120-(card.X+pad)), card.X+pad, y, ColorTextPrimary(), textAlignLeft)
			drawText(renderer, f.body, p.cpu, colCPU, y, ColorTextSecondary(), textAlignRight)
			drawText(renderer, f.body, p.mem, colMem, y, ColorTextTertiary(), textAlignRight)
			y += rowH
		}
	}
	sysToolFinishPage(renderer, config)
}

func sysToolHWPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(config)
	case sdl.CONTROLLER_BUTTON_Y:
		openProcessPanel()
	case sdl.CONTROLLER_BUTTON_X:
		PlayActivateSound()
		wifiScrOpen(config)
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return focusChromeBar()
	default:
		return false
	}
	return true
}

func sysToolHWKey(e *sdl.KeyboardEvent, config *Config) bool {
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		goBackScene(config)
	case sdl.K_y, sdl.K_p:
		openProcessPanel()
	case sdl.K_x, sdl.K_w:
		PlayActivateSound()
		wifiScrOpen(config)
	case sdl.K_UP:
		return focusChromeBar()
	default:
		return false
	}
	return true
}

func sysToolHWClick(mx, my int32, config *Config) bool {
	switch {
	case sysToolHWView.procRect.W > 0 && pointInRect(mx, my, sysToolHWView.procRect):
		openProcessPanel()
		return true
	case sysToolHWView.netRect.W > 0 && pointInRect(mx, my, sysToolHWView.netRect):
		PlayActivateSound()
		wifiScrOpen(config)
		return true
	}
	return false
}

// sysToolHWScene is the registered module for the "Hardware" scene.
type sysToolHWScene struct{ sceneBase }

func (sysToolHWScene) render(r *sdl.Renderer, c *Config) bool { renderSysToolHW(r, c); return true }

func (sysToolHWScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	return sysToolHWKey(e, c)
}

func (sysToolHWScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return sysToolHWPad(e, c)
}

func init() { registerScene(sysToolHWScene{sceneBase{name: "Hardware"}}) }
