package main

// tvmode_sysfs.go — does a TV hang off the USB-C port? What the kernel says.
//
// The Brick Pro sends its picture out of the USB-C port (DisplayPort alt
// mode), and its firmware drives that output; JukaHub only wants to know
// whether a display is there. Nobody has told us which file on the Brick Pro
// flips when a cable goes in, so every place a Linux kernel of this age may
// report it is read, and each reading is logged once per change (tvmode.go),
// so the first Brick Pro owner's log names the real one:
//
//   - USB Type-C: a partner's alternate mode with the DisplayPort SVID ff01,
//     and its "active" or displayport/hpd file (kernel 4.12 and later; the
//     Brick Pro's 4.9 kernel may not have the class at all);
//   - extcon: a cable state "DP=1" or "HDMI=1";
//   - Android's switch class: /sys/class/switch/hdmi/state;
//   - DRM connectors: card*-DP-*/status, card*-HDMI-*/status = connected (the
//     internal eDP and DSI panels are not counted);
//   - Allwinner's display driver: /sys/class/disp/disp/attr/sys lists each
//     screen and its output ("lcd output", "hdmi output ... 1920x1080"); an
//     output that is not the panel is a display;
//   - the framebuffer's mode: fb0 switched to a size that is not the panel's.
//     Only a switch counts: the watch asks for this check when fb0 had the
//     panel's size at its first reading, so a firmware whose framebuffer is
//     always some other size never reads as "a TV is connected".
//
// The files are read with a size cap, never written, and the functions here
// only parse what they are handed, so the parsing is tested on any machine
// with a fake tree (tvmode_test.go). The probe itself runs on the TV-mode
// goroutine (tvmode_linux.go), every two seconds at most.

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// tvSysfsReadMax bounds one file read. The display driver's dump is the
// largest, a few KB.
const tvSysfsReadMax = 16 << 10

// tvEvidence is one probe's reading.
type tvEvidence struct {
	// Paths are the status places that answered, connected or not:
	// "extcon0 DP", "typec port0", "disp".
	Paths []string
	// Sources are the readings that say a display is connected, in the
	// same words: "extcon0 DP=1", "disp screen 1 hdmi 1920x1080".
	Sources []string
	// Notes are other facts for the log: what the display driver lists,
	// extra framebuffers.
	Notes []string
	// FBW, FBH are fb0's visible size; 0 when unknown.
	FBW, FBH int
	// Both is set when the display driver lists the panel and an outside
	// display at once (mirrored, or a second screen).
	Both bool
}

// connected reports whether any place says a display is there.
func (e tvEvidence) connected() bool { return len(e.Sources) > 0 }

// summary is the reading in one log line. It holds no counters, so it only
// changes when something real changes.
func (e tvEvidence) summary() string {
	var parts []string
	switch {
	case e.connected():
		parts = append(parts, "display: "+strings.Join(e.Sources, ", "))
	case len(e.Paths) > 0:
		parts = append(parts, "no display on "+strings.Join(e.Paths, ", "))
	default:
		parts = append(parts, "no video-out status file found")
	}
	if e.FBW > 0 && e.FBH > 0 {
		parts = append(parts, "fb0 "+tvSizeLabel(e.FBW, e.FBH))
	}
	parts = append(parts, e.Notes...)
	return strings.Join(parts, "; ")
}

// tvSizeLabel is "1920x1080".
func tvSizeLabel(w, h int) string {
	return strconv.Itoa(w) + "x" + strconv.Itoa(h)
}

// tvSizeWide reports whether a size is 16:9 or wider (a TV), not the 4:3 or
// 16:10 of a handheld panel.
func tvSizeWide(w, h int) bool {
	return w > 0 && h > 0 && w*100 >= h*170
}

// tvReadSmall reads a status file, capped; ok is false when it cannot be
// read.
func tvReadSmall(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, tvSysfsReadMax))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// tvSysfsYes reads a sysfs yes/no or 1/0 word.
func tvSysfsYes(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "yes", "y", "on", "true", "connected":
		return true
	}
	return false
}

// tvProbeRoot reads every place under root (the real one is "/sys").
// panelW, panelH are the model's panel, for the framebuffer check; 0 skips
// it (tvProbeSystem's fbCheck).
func tvProbeRoot(root string, panelW, panelH int) tvEvidence {
	var ev tvEvidence
	tvProbeTypeC(root, &ev)
	tvProbeExtcon(root, &ev)
	tvProbeSwitch(root, &ev)
	tvProbeDRM(root, &ev)
	tvProbeDisp(root, &ev)
	tvProbeFB(root, &ev)
	// A framebuffer that left the panel's size is a display too, when
	// nothing else said so: the firmware moved the picture somewhere.
	if !ev.connected() && panelW > 0 && panelH > 0 && ev.FBW > 0 && ev.FBH > 0 &&
		(ev.FBW != panelW || ev.FBH != panelH) {
		ev.Sources = append(ev.Sources, "fb0 "+tvSizeLabel(ev.FBW, ev.FBH)+
			" (panel "+tvSizeLabel(panelW, panelH)+")")
	}
	return ev
}

// tvGlob is filepath.Glob, sorted, nil on a bad pattern.
func tvGlob(pattern string) []string {
	m, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	sort.Strings(m)
	return m
}

// tvProbeTypeC reads the Type-C class: the ports, and a partner's
// DisplayPort alternate mode.
func tvProbeTypeC(root string, ev *tvEvidence) {
	for _, p := range tvGlob(filepath.Join(root, "class", "typec", "port*")) {
		base := filepath.Base(p)
		if strings.Contains(base, "-") || strings.Contains(base, ".") {
			continue // a partner, a cable or an alternate mode, not a port
		}
		ev.Paths = append(ev.Paths, "typec "+base)
	}
	for _, svid := range tvGlob(filepath.Join(root, "class", "typec", "*-partner", "*", "svid")) {
		s, ok := tvReadSmall(svid)
		if !ok || !strings.EqualFold(strings.TrimSpace(s), "ff01") {
			continue
		}
		dir := filepath.Dir(svid)
		name := "typec " + filepath.Base(dir) + " DisplayPort"
		if hpd, ok := tvReadSmall(filepath.Join(dir, "displayport", "hpd")); ok {
			if tvSysfsYes(hpd) {
				ev.Sources = append(ev.Sources, name+" (hot-plug)")
			} else {
				ev.Notes = append(ev.Notes, name+" adapter, no display on it")
			}
			continue
		}
		if active, ok := tvReadSmall(filepath.Join(dir, "active")); ok && !tvSysfsYes(active) {
			ev.Notes = append(ev.Notes, name+" adapter, mode not entered")
			continue
		}
		ev.Sources = append(ev.Sources, name)
	}
}

// tvExtconDisplay reports whether an extcon cable name is a display:
// "DP", "HDMI", "DISP-DP", "DISP-HDMI".
func tvExtconDisplay(name string) bool {
	u := strings.ToUpper(strings.TrimSpace(name))
	return u == "DP" || strings.Contains(u, "HDMI") || strings.Contains(u, "DISPLAYPORT") ||
		strings.HasSuffix(u, "-DP") || strings.HasPrefix(u, "DP-")
}

// tvParseExtconState splits an extcon state file ("USB=0\nDP=1") into its
// display cables and whether each is attached. A state without names (an
// old single-cable device) is returned as one entry under fallback when
// fallback names a display.
func tvParseExtconState(state, fallback string) (names []string, on []bool) {
	for _, line := range strings.Split(state, "\n") {
		line = strings.TrimSpace(line)
		name, val, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		if tvExtconDisplay(name) {
			names = append(names, strings.ToUpper(strings.TrimSpace(name)))
			on = append(on, strings.TrimSpace(val) != "0" && strings.TrimSpace(val) != "")
		}
	}
	if names == nil && !strings.Contains(state, "=") && tvExtconDisplay(fallback) {
		v := strings.TrimSpace(state)
		if v != "" {
			names = append(names, strings.ToUpper(strings.TrimSpace(fallback)))
			on = append(on, v != "0" && v != "0x0" && v != "0x00000000")
		}
	}
	return names, on
}

// tvProbeExtcon reads every extcon device's cable states.
func tvProbeExtcon(root string, ev *tvEvidence) {
	for _, st := range tvGlob(filepath.Join(root, "class", "extcon", "*", "state")) {
		state, ok := tvReadSmall(st)
		if !ok {
			continue
		}
		dir := filepath.Dir(st)
		devName, _ := tvReadSmall(filepath.Join(dir, "name"))
		names, on := tvParseExtconState(state, strings.TrimSpace(devName))
		for i, name := range names {
			where := filepath.Base(dir) + " " + name
			ev.Paths = append(ev.Paths, where)
			if on[i] {
				ev.Sources = append(ev.Sources, where+"=1")
			}
		}
	}
}

// tvProbeSwitch reads Android's switch class (an hdmi switch).
func tvProbeSwitch(root string, ev *tvEvidence) {
	for _, st := range tvGlob(filepath.Join(root, "class", "switch", "*", "state")) {
		base := strings.ToLower(filepath.Base(filepath.Dir(st)))
		if !strings.Contains(base, "hdmi") && !strings.Contains(base, "displayport") && base != "dp" {
			continue
		}
		state, ok := tvReadSmall(st)
		if !ok {
			continue
		}
		ev.Paths = append(ev.Paths, "switch "+base)
		v := strings.TrimSpace(state)
		if _, after, found := strings.Cut(v, "="); found {
			v = strings.TrimSpace(after)
		}
		if v != "" && v != "0" {
			ev.Sources = append(ev.Sources, "switch "+base+"=1")
		}
	}
}

// tvDRMExternal reports whether a DRM connector ("card0-HDMI-A-1") is an
// outside display, not the handheld's own eDP, DSI or DPI (parallel RGB)
// panel. The kind is matched with its dash: "DP-1" is DisplayPort, "DPI-1"
// is a panel.
func tvDRMExternal(conn string) bool {
	_, kind, found := strings.Cut(conn, "-")
	if !found {
		return false
	}
	for _, p := range []string{"HDMI-", "DP-", "DVI-", "VGA-", "TV-", "Composite-", "SVIDEO-", "Component-"} {
		if strings.HasPrefix(kind, p) {
			return true
		}
	}
	return false
}

// tvProbeDRM reads the DRM connectors' status files.
func tvProbeDRM(root string, ev *tvEvidence) {
	for _, st := range tvGlob(filepath.Join(root, "class", "drm", "card*-*", "status")) {
		conn := filepath.Base(filepath.Dir(st))
		if !tvDRMExternal(conn) {
			continue
		}
		state, ok := tvReadSmall(st)
		if !ok {
			continue
		}
		ev.Paths = append(ev.Paths, "drm "+conn)
		if strings.TrimSpace(state) == "connected" {
			ev.Sources = append(ev.Sources, "drm "+conn+" connected")
		}
	}
}

// tvDispOutput is one output line of the display driver's dump.
type tvDispOutput struct {
	Screen int
	Kind   string // "lcd", "hdmi", "tv", "vga", "edp", "dp"
	W, H   int
}

// tvDispSizeRe finds a size in a dump line: "1920x1080", " 1280x 720".
var tvDispSizeRe = regexp.MustCompile(`(\d{3,5})\s*x\s*(\d{3,5})`)

// tvParseDispSys reads Allwinner's /sys/class/disp/disp/attr/sys: "screen N:"
// starts a screen, and a line with "<kind> output" is that screen's output,
// with its size at the end of the line when the driver prints one.
func tvParseDispSys(text string) []tvDispOutput {
	var out []tvDispOutput
	screen := -1
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(t, "screen "); ok {
			if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(rest), ":")); err == nil {
				screen = n
			}
			continue
		}
		fields := strings.Fields(t)
		for i := 1; i < len(fields); i++ {
			if fields[i] != "output" {
				continue
			}
			o := tvDispOutput{Screen: screen, Kind: strings.ToLower(fields[i-1])}
			if m := tvDispSizeRe.FindAllStringSubmatch(t, -1); len(m) > 0 {
				last := m[len(m)-1]
				o.W, _ = strconv.Atoi(last[1])
				o.H, _ = strconv.Atoi(last[2])
			}
			out = append(out, o)
			break
		}
	}
	return out
}

// tvProbeDisp reads the Allwinner display driver's dump and the HDMI
// driver's hot-plug file.
func tvProbeDisp(root string, ev *tvEvidence) {
	if text, ok := tvReadSmall(filepath.Join(root, "class", "disp", "disp", "attr", "sys")); ok {
		ev.Paths = append(ev.Paths, "disp")
		outs := tvParseDispSys(text)
		var seen []string
		panel, other := false, false
		for _, o := range outs {
			label := "screen " + strconv.Itoa(o.Screen) + " " + o.Kind
			if o.W > 0 && o.H > 0 {
				label += " " + tvSizeLabel(o.W, o.H)
			}
			seen = append(seen, label)
			if o.Kind == "lcd" {
				panel = true
				continue
			}
			other = true
			ev.Sources = append(ev.Sources, "disp "+label)
		}
		if len(seen) > 0 {
			ev.Notes = append(ev.Notes, "disp lists "+strings.Join(seen, ", "))
		}
		ev.Both = panel && other
	}
	if hpd, ok := tvReadSmall(filepath.Join(root, "class", "hdmi", "hdmi", "attr", "hpd")); ok {
		ev.Paths = append(ev.Paths, "hdmi hpd")
		if tvSysfsYes(hpd) {
			ev.Sources = append(ev.Sources, "hdmi hpd=1")
		}
	}
}

// tvParseFBMode reads fb0's visible size: the "mode" file ("U:1920x1080p-60")
// when the driver fills it, otherwise "virtual_size" ("1024,1536"), whose
// height holds every buffer the driver flips between: the one, two or three
// buffers that leave a common screen shape are taken as the visible one. ok
// is false when neither says.
func tvParseFBMode(mode, virtual string) (w, h int, ok bool) {
	mode = strings.TrimSpace(strings.Split(mode, "\n")[0])
	if _, rest, found := strings.Cut(mode, ":"); found {
		mode = rest
	}
	if xw, xh, found := strings.Cut(mode, "x"); found {
		end := strings.IndexFunc(xh, func(r rune) bool { return r < '0' || r > '9' })
		if end >= 0 {
			xh = xh[:end]
		}
		w, errW := strconv.Atoi(strings.TrimSpace(xw))
		h, errH := strconv.Atoi(xh)
		if errW == nil && errH == nil && w > 0 && h > 0 {
			return w, h, true
		}
	}
	vw, vh, found := strings.Cut(strings.TrimSpace(virtual), ",")
	if !found {
		return 0, 0, false
	}
	w, errW := strconv.Atoi(strings.TrimSpace(vw))
	full, errH := strconv.Atoi(strings.TrimSpace(vh))
	if errW != nil || errH != nil || w <= 0 || full <= 0 {
		return 0, 0, false
	}
	shapes := [][2]int{{4, 3}, {16, 9}, {16, 10}, {5, 3}, {3, 2}}
	for buffers := 1; buffers <= 3; buffers++ {
		if full%buffers != 0 {
			continue
		}
		h := full / buffers
		for _, s := range shapes {
			if w*s[1] == h*s[0] {
				return w, h, true
			}
		}
	}
	return 0, 0, false
}

// tvProbeFB reads fb0's size and notes any second framebuffer.
func tvProbeFB(root string, ev *tvEvidence) {
	dir := filepath.Join(root, "class", "graphics", "fb0")
	mode, _ := tvReadSmall(filepath.Join(dir, "mode"))
	virtual, _ := tvReadSmall(filepath.Join(dir, "virtual_size"))
	if w, h, ok := tvParseFBMode(mode, virtual); ok {
		ev.FBW, ev.FBH = w, h
	}
	for _, fb := range tvGlob(filepath.Join(root, "class", "graphics", "fb[1-9]")) {
		ev.Notes = append(ev.Notes, filepath.Base(fb)+" present")
	}
}
