//go:build linux

package main

// The launcher: os/smartpro/launch.sh in Go, so JukaHub starts without a
// shell and its forks. Same folders, boot.log lines, environment, boot frame
// checks and restart rules (the in-place update restart, the screen
// hand-off, "Restart JukaHub", a start that failed only because the drivers
// were still loading, three quick failures). launch.sh stays in the image for
// the shell init; a change to one belongs in the other.
//
// One addition: when /dev/fb0 is not there yet at the start (the display
// driver is a module still loading), the boot frame is put on the panel as
// soon as the driver is in, still before the display-ready marker, so before
// JukaHub draws (launch.sh skipped it then).

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const appDir = cardDir + "/Apps/JukaHub"

// runLauncher runs JukaHub until it quits for good; then process 1 shuts down.
func (b *booter) runLauncher() {
	// JukaHub needs the writable /etc.
	if b.rwDone != nil {
		select {
		case <-b.rwDone:
		case <-time.After(5 * time.Second):
		}
	}
	logPath := dataDir + "/boot.log"
	launchUp := bootNow()
	// The folders exist after the first boot.
	if !isDir(dataDir + "/logs") {
		if err := os.MkdirAll(dataDir+"/logs", 0o755); err != nil {
			b.log("launcher: %v", err)
			return
		}
	}
	if isFile(logPath) {
		_ = os.Rename(logPath, logPath+".1")
	}
	_ = os.WriteFile(logPath, nil, 0o644)
	appendLine(logPath, "launcher at "+secs(launchUp)+"s since boot (jukaboot)")
	// Started after the drivers (the marker is there): the frame goes up
	// first, as in launch.sh. Started beside them: JukaHub draws only after
	// the marker, so the frame is written meanwhile and the marker waits for it.
	frameOn := b.frame.start(dataDir, logPath, !exists(displayReady))
	env := b.jukahubEnv()
	bootframe := frameOn
	fails, earlyRetries := 0, 0
	for !b.shuttingDown.Load() {
		if unix.Access(appDir+"/JukaHub", unix.X_OK) != nil {
			appendLine(logPath, "JukaHub is missing from "+appDir)
			break
		}
		up := bootNow()
		start := up / 1e9
		appendLine(logPath, "JukaHub starting at "+secs(up)+"s since boot")
		// Started before the drivers were in (the marker not there yet)?
		early := !exists(displayReady)
		if !isDir(appDir) {
			break
		}
		out, err := unix.Open(logPath, unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT|unix.O_CLOEXEC, 0o644)
		if err != nil {
			out = -1
		}
		flag := "0"
		if bootframe {
			flag = "1"
		}
		pid, done, err := b.spawn(appDir+"/JukaHub", []string{"./JukaHub"}, spawnOpts{
			dir: appDir, env: setEnv(env, "JUKAHUB_BOOTFRAME", flag), out: out, commit: true, wait: true})
		if out >= 0 {
			_ = unix.Close(out)
		}
		// Only the first start of a boot finds the boot frame on the panel.
		bootframe = false
		status := 127
		if err != nil {
			if errors.Is(err, errStopping) {
				return
			}
			appendLine(logPath, "./JukaHub: "+err.Error())
		} else {
			b.frame.started(up)
			b.tl.mark("JukaHub started (pid " + strconv.Itoa(pid) + ")")
			status = <-done
			b.tl.mark("JukaHub ended with status " + strconv.Itoa(status))
		}
		_ = os.WriteFile(appDir+"/.last-exit", []byte(strconv.Itoa(status)+" "+strconv.Itoa(pid)+"\n"), 0o644)
		if b.shuttingDown.Load() {
			return
		}
		// A JukaHub from before the display wait opens the display at once and
		// fails while the drivers still load. That start does not count as a
		// failure: the next one waits for the marker, 10 s at most (twice a
		// boot at most).
		if status != 0 && early && earlyRetries < 2 {
			earlyRetries++
			appendLine(logPath, "JukaHub exited with status "+strconv.Itoa(status)+
				" while the drivers were loading; starting it again once they are in")
			for i := 0; i < 100 && !exists(displayReady); i++ {
				time.Sleep(100 * time.Millisecond)
			}
			continue
		}
		if isFile(appDir + "/.screen-handoff") {
			handoff := firstLine(appDir + "/.screen-handoff")
			_ = os.Remove(appDir + "/.screen-handoff")
			if allDigits(handoff) {
				for isDir("/proc/"+handoff) && !b.shuttingDown.Load() {
					time.Sleep(250 * time.Millisecond)
				}
			}
			fails = 0
			continue
		}
		if isFile(appDir+"/.restart-pending") || isFile(appDir+"/.restart-now") {
			_ = os.Remove(appDir + "/.restart-pending")
			_ = os.Remove(appDir + "/.restart-now")
			fails = 0
			continue
		}
		if status == 0 {
			break
		}
		if bootNow()/1e9-start < 5 {
			fails++
		} else {
			fails = 0
		}
		appendLine(logPath, "JukaHub exited with status "+strconv.Itoa(status)+" ("+strconv.Itoa(fails)+" quick failures)")
		if fails >= 3 {
			break
		}
		time.Sleep(time.Second)
	}
	unix.Sync()
}

// jukahubEnv is launch.sh's environment for JukaHub: games and RetroArch get
// the stock system's (JUKAHUB_ORIG_*); JukaHub's bundled libraries and helpers
// apply to JukaHub only. JUKAHUB_LAUNCHED_BY=launch.sh: this loop speaks
// launch.sh's restart and hand-off protocol (player/patch_swap.go,
// psremote.go read it).
func (b *booter) jukahubEnv() []string {
	env := b.env
	if home, ok := lookupEnv(env, "HOME"); ok {
		env = setEnv(env, "JUKAHUB_ORIG_HOME", home)
	}
	path, _ := lookupEnv(env, "PATH")
	env = setEnv(env, "JUKAHUB_ORIG_PATH", path)
	ld, hasLD := lookupEnv(env, "LD_LIBRARY_PATH")
	if hasLD {
		env = setEnv(env, "JUKAHUB_ORIG_LD_LIBRARY_PATH", ld)
	}
	env = setEnv(env, "JUKAHUB_ORIG_ENV", "HOME PATH LD_LIBRARY_PATH")
	// The stock SDL2 in /usr/lib draws through the PowerVR's fbdev EGL,
	// exactly as when JukaHub runs from the stock menu.
	libs := appDir + ":" + appDir + "/libs:/usr/trimui/lib:/usr/lib:/lib"
	if ld != "" {
		libs += ":" + ld
	}
	for _, kv := range [][2]string{
		{"LD_LIBRARY_PATH", libs},
		{"PATH", appDir + ":" + appDir + "/required:" + path},
		{"HOME", dataDir},
		{"JUKAHUB_LAUNCHED_BY", "launch.sh"},
		{"JUKAHUB_SCREEN_HANDOFF", "1"},
		{"JUKAHUB_DEVICE_MODEL", "trimui-smart-pro"},
		{"CLR_OPENSSL_VERSION_OVERRIDE", "1.1"},
		{"BIOS_PATH", cardDir + "/Bios"},
		{"ROMS_PATH", cardDir + "/Roms"},
		{"SAVES_PATH", cardDir + "/Saves"},
		{"USERDATA_PATH", dataDir},
		{"JUKAHUB_WAIT_DISPLAY", displayReady},
		{"JUKAHUB_BOOTER", "jukaboot"},
	} {
		env = setEnv(env, kv[0], kv[1])
	}
	if tz := jukaTZ(appDir + "/jukatimezone"); tz != "" {
		env = setEnv(env, "TZ", tz)
	}
	return env
}

// jukaTZ is the zone chosen in Settings -> Date & time (the rule of
// player/launch.sh: a plain IANA name only, at most 64 characters).
func jukaTZ(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 128)
	n, _ := f.Read(buf)
	tz, _, _ := strings.Cut(string(buf[:n]), "\n")
	tz = strings.ReplaceAll(tz, "\r", "")
	if tz == "" || len(tz) > 64 || tz[0] == '/' || tz[len(tz)-1] == '/' || strings.Contains(tz, "..") {
		return ""
	}
	for _, c := range tz {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("_+/-", c)) {
			return ""
		}
	}
	return tz
}

// ── the boot frame ─────────────────────────────────────────────────────────

// frameState is the boot frame (player/bootframe.go keeps it on the card) on
// its way to the panel.
type frameState struct {
	mu       sync.Mutex
	data     string        // the card's data folder
	logPath  string        // boot.log
	pending  bool          // waiting for /dev/fb0
	busy     chan struct{} // closed when the write under way is done
	marker   bool          // the marker is out: JukaHub may draw, no frame any more
	deadline int64         // JukaHub's own wait for the marker ends then
}

type framePlan struct {
	path  string
	pages int
	size  int64
}

// start checks the frame against the panel (launch.sh's show_bootframe) and
// writes it: at once (not async, or the marker is already out, so before
// JukaHub is started), or on a goroutine the marker waits for. With no
// /dev/fb0 yet and async, it waits for the display driver (tryLate). It
// reports whether the frame is going on the panel now.
func (f *frameState) start(data, logPath string, async bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data, f.logPath = data, logPath
	if !isFile(data + "/bootframe.info") {
		return false
	}
	if !isCharDev("/dev/fb0") {
		if async && !f.marker {
			f.pending = true
			appendLine(logPath, "boot frame waits for /dev/fb0 (the display driver is still loading)")
		} else {
			appendLine(logPath, "boot frame skipped: no /dev/fb0 yet (the display driver is still loading)")
		}
		return false
	}
	plan, ok := checkBootFrame(data)
	if !ok {
		return false
	}
	if !async || f.marker {
		if writeBootFrame(plan) != nil {
			return false
		}
		appendLine(logPath, "boot frame on screen at "+secs(bootNow())+"s")
		return true
	}
	f.write(plan, "")
	return true
}

// write puts the frame on the panel on a goroutine (f.mu held).
func (f *frameState) write(plan framePlan, note string) {
	busy := make(chan struct{})
	f.busy = busy
	logPath := f.logPath
	go func() {
		defer close(busy)
		defer func() { _ = recover() }()
		if writeBootFrame(plan) == nil {
			appendLine(logPath, "boot frame on screen at "+secs(bootNow())+"s"+note)
		}
	}()
}

// started notes JukaHub's first start: a frame later than its own display
// wait (8 s) could land over its first frame.
func (f *frameState) started(at int64) {
	f.mu.Lock()
	if f.deadline == 0 {
		f.deadline = at + 7*int64(time.Second)
	}
	f.mu.Unlock()
}

// tryLate writes a waiting frame once /dev/fb0 is there (after each module).
func (f *frameState) tryLate() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.pending || f.marker || !isCharDev("/dev/fb0") {
		return
	}
	f.pending = false
	if f.deadline > 0 && bootNow() > f.deadline || exists(firstFrame) {
		appendLine(f.logPath, "boot frame skipped: the display driver came after JukaHub's own wait")
		return
	}
	if plan, ok := checkBootFrame(f.data); ok {
		f.write(plan, " (once the display driver was in)")
	}
}

// beforeMarker makes a waiting frame's last try and waits (3 s at most) for
// any frame write still under way: after the marker JukaHub draws.
func (f *frameState) beforeMarker() {
	f.tryLate()
	f.mu.Lock()
	f.marker = true
	f.pending = false
	busy := f.busy
	f.mu.Unlock()
	if busy != nil {
		select {
		case <-busy:
		case <-time.After(3 * time.Second):
		}
	}
}

// checkBootFrame is launch.sh's check: the saved picture has exactly the
// panel's geometry (width, a whole number of pages, depth, stride, the mode
// line's size where the driver gives one) and its file exactly stride*height
// bytes. Plain numbers only, as in the shell.
func checkBootFrame(data string) (framePlan, bool) {
	const fb = "/sys/class/graphics/fb0"
	info := strings.Fields(firstLine(data + "/bootframe.info"))
	if len(info) < 4 {
		return framePlan{}, false
	}
	for _, s := range info[:4] {
		if !plainNumber(s, 6) {
			return framePlan{}, false
		}
	}
	w, h, bpp, stride := info[0], info[1], info[2], info[3]
	path := data + "/bootframe.fb"
	if !isFile(path) || !isCharDev("/dev/fb0") {
		return framePlan{}, false
	}
	vs := firstLine(fb + "/virtual_size")
	vw, _, _ := strings.Cut(vs, ",")
	vh := vs[strings.LastIndexByte(vs, ',')+1:]
	if !plainNumber(vw, 6) || !plainNumber(vh, 6) {
		return framePlan{}, false
	}
	if firstLine(fb+"/bits_per_pixel") != bpp || firstLine(fb+"/stride") != stride {
		return framePlan{}, false
	}
	height, _ := strconv.Atoi(h)
	rowBytes, _ := strconv.Atoi(stride)
	virtual, _ := strconv.Atoi(vh)
	if vw != w || height <= 0 || virtual < height || virtual%height != 0 {
		return framePlan{}, false
	}
	if modes := firstLine(fb + "/modes"); modes != "" && !strings.Contains(modes, w+"x"+h) {
		return framePlan{}, false
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() != int64(rowBytes)*int64(height) {
		return framePlan{}, false
	}
	return framePlan{path: path, pages: virtual / height, size: st.Size()}, true
}

// writeBootFrame writes the picture into every page of the framebuffer, so
// whichever one is panned to shows it, with the text cursor's blink off.
func writeBootFrame(plan framePlan) error {
	pixels, err := os.ReadFile(plan.path)
	if err != nil {
		return err
	}
	if int64(len(pixels)) != plan.size {
		return errors.New("the boot frame changed while it was read")
	}
	_ = writeFile("/sys/class/graphics/fbcon/cursor_blink", "0\n")
	fb, err := os.OpenFile("/dev/fb0", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer fb.Close()
	for i := 0; i < plan.pages; i++ {
		if _, err := fb.Write(pixels); err != nil {
			return err
		}
	}
	return nil
}
