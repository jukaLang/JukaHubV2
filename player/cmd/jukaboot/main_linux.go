//go:build linux

package main

// The boot, in the order os/smartpro/init runs it, with what runs side by side:
//
//	proc, sys (hotplug helper off), dev, pts, shm, tmp     system calls, no forks
//	writable copies of /etc /var /root, /run, hostname     beside the card search
//	the JUKAHUB partition on the SD card, mounted           (card fs a module: drivers first)
//	os.log, /mnt/UDISK, boot.env, read-ahead
//	JukaHub (the launch.sh loop, in Go) and the boot frame  beside the drivers (EARLY_START=1)
//	kernel modules, side by side along their dependencies   display, GPU, input, sound first
//	GPIO lines, mixer, stick daemon, its pad                as the shell
//	/tmp/jukahub-display-ready                              JukaHub opens the display
//	radios after JukaHub's first frame, the kernel log, the timeline
//
// Process 1 never returns: it reaps every orphan, turns busybox's halt,
// poweroff and reboot signals into a clean shutdown, and parks.

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	jRoot        = "/jukahub"
	shellInit    = "/jukahub/init"
	cardDir      = "/mnt/SDCARD"
	udiskDir     = "/mnt/UDISK"
	dataDir      = cardDir + "/.userdata/jukahub"
	rwRoot       = "/tmp/.jukahub-rw"
	displayReady = "/tmp/jukahub-display-ready"
	firstFrame   = "/tmp/jukahub-first-frame"
	rebootMarker = "/tmp/jukahub-native-reboot"
	initPath     = "/usr/sbin:/usr/bin:/sbin:/bin:/usr/trimui/bin"
	// cardWait is how long the JUKAHUB partition may take to appear (the
	// shell init's 50 naps of 0.2 s).
	cardWait = 10 * time.Second
	// modulesWait bounds the wait for the modules before the GPIO lines,
	// the daemons and the marker: a driver that hangs in its start must not
	// hold the sticks back for good (the shell init waited forever).
	modulesWait = 10 * time.Second
)

type booter struct {
	start   int64    // CLOCK_BOOTTIME when process 1 began
	origEnv []string // what the kernel gave process 1 (for the shell init)
	env     []string // children's: the kernel's environment with init's PATH
	nullFD  int      // /dev/null, -1 when there is none

	board    boardEnv
	settings bootSettings
	daemons  []daemon

	tl    timeline
	oslog cardLog
	procs procTable
	frame frameState

	// fbMu orders handing the boot to the shell init against starting
	// JukaHub or a daemon: once one runs (committed), there is no hand-over.
	fbMu         sync.Mutex
	fellBack     bool
	committed    atomic.Bool
	shuttingDown atomic.Bool
	shutOnce     sync.Once
	timelineOnce sync.Once
	launched     atomic.Bool

	mountsMu sync.Mutex
	mounts   []string // what jukaboot mounted, in order

	rwDone   chan struct{}
	modsDone chan struct{}
	modsMu   sync.Mutex
	modsBusy map[string]bool // modules being loaded right now
}

func main() {
	if os.Getpid() != 1 {
		os.Exit(runTool(os.Args[1:]))
	}
	b := &booter{start: bootNow(), origEnv: os.Environ(), nullFD: -1}
	b.env = setEnv(b.origEnv, "PATH", initPath)
	b.procs.waiters = map[int]chan int{}
	b.modsBusy = map[string]bool{}
	b.watchSignals()
	b.boot()
}

// boot runs the start-up; it never returns.
func (b *booter) boot() {
	defer func() {
		if r := recover(); r != nil {
			b.crashed("the boot", r)
			// Carrying on (JukaHub's start was under way): what the boot did
			// not reach, so JukaHub does not wait 8 s for the marker and the
			// radios still come up.
			if !b.shuttingDown.Load() {
				_ = os.WriteFile(displayReady, nil, 0o644)
				go b.safe("the radios", b.radios)
			}
			park()
		}
	}()
	b.earlyMounts()
	b.log("process 1 at %ss (jukaboot)", secs(b.start))
	b.readPlanFiles()
	b.settings = defaultSettings(b.board)

	b.rwDone = make(chan struct{})
	go b.safe("the writable copies", func() {
		defer close(b.rwDone)
		b.rwSetup()
	})

	dev := b.waitCard()
	if dev == "" {
		b.fallback("the " + b.board.DataName + " partition is not on the SD card")
	}
	drivers := false
	if err := b.mountCard(dev); err != nil {
		// The card's file system is a module (exFAT, say): drivers first.
		b.tl.mark("card not mountable yet (" + err.Error() + "): drivers first")
		b.startModules()
		b.waitModules()
		b.replayGPIO()
		b.restoreMixer()
		drivers = true
		if err := b.mountCard(dev); err != nil {
			b.fallback(fmt.Sprintf("cannot mount %s (%s): %v", dev, b.board.DataFSType, err))
		}
	}
	b.cardReady()
	b.readBootEnv()
	if !b.settings.Booter {
		b.fallback("BOOTER=0 in boot.env")
	}
	b.readahead(dev)

	if !drivers {
		b.startModules()
	}
	if b.settings.EarlyStart && !drivers {
		b.log("JukaHub starts at %ss, beside the drivers", secs(bootNow()))
		b.launch()
	}
	if !drivers {
		b.waitModules()
		b.replayGPIO()
		b.restoreMixer()
	}
	inputs := inputCount()
	b.startDaemons("hardware")
	settle := b.inputSettle(inputs)
	b.frame.beforeMarker()
	if err := os.WriteFile(displayReady, nil, 0o644); err != nil {
		b.log("cannot write %s: %v", displayReady, err)
	}
	b.tl.mark("display-ready marker")
	if settle != "" {
		settle = " (" + settle + ")"
	}
	b.log("display, input and sound ready at %ss%s", secs(bootNow()), settle)
	if !b.launched.Load() {
		b.log("JukaHub starts at %ss, after the drivers", secs(bootNow()))
		b.launch()
	}
	go b.safe("the radios", b.radios)
	park()
}

// readPlanFiles reads what build_image.py wrote into /jukahub. A missing file
// keeps the defaults, as in the shell init.
func (b *booter) readPlanFiles() {
	b.board = defaultBoardEnv()
	if data, err := os.ReadFile(jRoot + "/board.env"); err == nil {
		b.board = parseBoardEnv(string(data))
	}
	if data, err := os.ReadFile(jRoot + "/daemons.list"); err == nil {
		b.daemons = parseDaemons(string(data))
	}
}

func (b *booter) readBootEnv() {
	if data, err := os.ReadFile(dataDir + "/boot.env"); err == nil {
		b.settings.apply(string(data))
	}
}

// startModules loads the stock system's modules (but the radios') side by
// side along their dependencies; modsDone closes when all have returned.
func (b *booter) startModules() {
	if b.modsDone != nil {
		return
	}
	nodes, note := planModules(b.board.Modules, b.board.ModuleDeps, !b.settings.Parallel)
	workers := runtime.NumCPU()
	if workers > 4 {
		workers = 4
	}
	b.modsDone = make(chan struct{})
	go b.safe("the modules", func() {
		defer close(b.modsDone)
		began := bootNow()
		var mu sync.Mutex
		var failed []string
		var sum int64
		loaded, running, most := 0, 0, 0
		runModules(nodes, workers, func(i int) {
			n := nodes[i]
			mu.Lock()
			running++
			if running > most {
				most = running
			}
			mu.Unlock()
			b.modsMu.Lock()
			b.modsBusy[n.Name] = true
			b.modsMu.Unlock()
			t0 := bootNow()
			err := loadModule(n.Path)
			t1 := bootNow()
			b.modsMu.Lock()
			delete(b.modsBusy, n.Name)
			b.modsMu.Unlock()
			label := "module " + n.Name + " (" + classNames[n.Class] + ")"
			if err != nil {
				label += ": " + err.Error()
			}
			b.tl.span(label, t0, t1)
			mu.Lock()
			running--
			sum += t1 - t0
			if err != nil {
				failed = append(failed, n.Name+" ("+err.Error()+")")
			} else {
				loaded++
			}
			mu.Unlock()
			b.frame.tryLate()
		})
		wall := bootNow() - began
		msg := fmt.Sprintf("modules: %d of %d loaded in %.2f s", loaded, len(nodes), float64(wall)/1e9)
		if note == "" && len(nodes) > 1 {
			msg += fmt.Sprintf(", up to %d at once (one by one they took %.2f s)", most, float64(sum)/1e9)
		} else if note != "" {
			msg += ", " + note
		}
		if len(failed) > 0 {
			msg += "; not loaded: " + strings.Join(failed, ", ")
		}
		b.log("%s", msg)
	})
}

// waitModules waits for every module, modulesWait at most.
func (b *booter) waitModules() {
	if b.modsDone == nil {
		return
	}
	done := b.tl.begin("waiting for the modules")
	defer done()
	select {
	case <-b.modsDone:
	case <-time.After(modulesWait):
		b.modsMu.Lock()
		var busy []string
		for name := range b.modsBusy {
			busy = append(busy, name)
		}
		b.modsMu.Unlock()
		b.log("modules still loading after %s (%s): going on without them", modulesWait, strings.Join(busy, ", "))
	}
}

// inputSettle: the stock stick daemon makes the pad JukaHub reads (a virtual
// input device) a moment after it starts, and SDL looks for pads when JukaHub
// opens the display, right after the marker. So the marker waits for one input
// device more than before: 0.4 s at most, and only when the daemon is in the
// plan.
func (b *booter) inputSettle(before int) string {
	if !stickDaemon(b.daemons) {
		return ""
	}
	done := b.tl.begin("waiting for the stick pad")
	defer done()
	began := bootNow()
	for i := 0; i < 20; i++ {
		if inputCount() > before {
			return fmt.Sprintf("stick pad up after %d ms", (bootNow()-began)/1e6)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return "no new input device 0.4 s after the stick daemon"
}

// radios: the Wi-Fi and Bluetooth modules and the Wi-Fi daemons once
// JukaHub's first frame is up (it writes "<uptime>" to /tmp/jukahub-first-frame),
// so they never compete with its start; twenty seconds at most. The kernel's
// log and the timeline are saved then too.
func (b *booter) radios() {
	done := b.tl.begin("waiting for JukaHub's first frame")
	seen := false
	for i := 0; i < 400 && !b.shuttingDown.Load(); i++ {
		if isFile(firstFrame) {
			seen = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	done()
	if seen {
		ff, _, _ := strings.Cut(firstLine(firstFrame), " ")
		b.log("JukaHub first frame at %ss; radios at %ss", ff, secs(bootNow()))
	} else {
		b.log("no JukaHub first frame after 20 s; radios at %ss", secs(bootNow()))
	}
	for _, path := range b.board.RadioModules {
		if b.shuttingDown.Load() {
			return
		}
		t0 := bootNow()
		err := loadModule(path)
		label := "radio module " + moduleName(path)
		if err != nil {
			label += ": " + err.Error()
		}
		b.tl.span(label, t0, bootNow())
	}
	b.saveKernelLog()
	if b.settings.Wifi && !b.shuttingDown.Load() {
		b.wifiConfIn()
		// The driver names its interface a moment after it loads.
		wait := b.tl.begin("waiting for wlan")
		for i := 0; i < 100 && !hasWlan(); i++ {
			time.Sleep(50 * time.Millisecond)
		}
		wait()
		b.startDaemons("radio")
	}
	b.saveTimeline()
}

// launch starts the launcher loop; when it ends, process 1 powers off (or
// restarts, when JukaHub left /tmp/jukahub-native-reboot), as the shell init.
func (b *booter) launch() {
	if b.launched.Swap(true) {
		return
	}
	go b.safe("the launcher", func() {
		// A panic in the loop is logged (before JukaHub ran, the shell init
		// takes over) and still ends in the power-off below: never a device
		// left with neither JukaHub nor a way off.
		b.safe("the launcher loop", b.runLauncher)
		if b.shuttingDown.Load() {
			park()
		}
		b.wifiConfOut()
		if isFile(rebootMarker) {
			b.shutdown("reboot")
		}
		b.shutdown("poweroff")
	})
}

// safe runs fn and turns a panic into crashed instead of the end of process 1.
func (b *booter) safe(where string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			b.crashed(where, r)
		}
	}()
	fn()
}

// crashed: before JukaHub or a daemon runs, the shell init takes the boot
// over; after, the booter carries on, and makes sure JukaHub still starts.
func (b *booter) crashed(where string, r any) {
	stack := string(debug.Stack())
	if len(stack) > 1500 {
		stack = stack[:1500]
	}
	why := fmt.Sprintf("jukaboot: %s stopped: %v", where, r)
	b.oslog.add(why + "\n" + stack)
	b.fallback(why)
	if !b.launched.Load() && !b.shuttingDown.Load() {
		_ = os.WriteFile(displayReady, nil, 0o644)
		b.launch()
	}
}

// fallback hands the boot to the shell process 1 (/jukahub/init) while
// nothing of the boot's own runs yet: what jukaboot mounted is let go and the
// shell starts from the beginning. Once JukaHub or a daemon runs it only logs
// and returns.
func (b *booter) fallback(why string) {
	b.fbMu.Lock()
	if b.committed.Load() || b.shuttingDown.Load() {
		b.fbMu.Unlock()
		b.log("%s (carrying on: JukaHub's start is under way)", why)
		return
	}
	b.fellBack = true
	b.log("%s: the shell process 1 (%s) takes over at %ss", why, shellInit, secs(bootNow()))
	b.saveTimeline()
	if b.rwDone != nil {
		select {
		case <-b.rwDone:
		case <-time.After(3 * time.Second):
		}
	}
	syncAll()
	b.undoMounts()
	err := execInit(shellInit, os.Args[1:], b.origEnv)
	// Only reached when the shell init cannot be started either.
	kmsg("jukahub-os: cannot start " + shellInit + ": " + err.Error())
	tty0("JukaHub OS cannot start: " + why)
	tty0("Remove the SD card and hold Power to start the stock system.")
	time.Sleep(30 * time.Second)
	b.fbMu.Unlock()
	b.shutdown("poweroff")
}

// log writes a line to the kernel log and to os.log on the card (kept in
// memory until the card is mounted).
func (b *booter) log(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	kmsg("jukahub-os: " + line)
	b.oslog.add(line)
}

// saveTimeline appends the timeline to os.log, once.
func (b *booter) saveTimeline() {
	b.timelineOnce.Do(func() {
		b.oslog.add(b.tl.text())
	})
}

// park keeps a goroutine (process 1's main one, above all) alive for good.
// The sleep is also a timer, so the runtime never takes the booter for a
// deadlocked program.
func park() {
	for {
		time.Sleep(time.Hour)
	}
}
