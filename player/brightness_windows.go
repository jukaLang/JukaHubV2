//go:build windows

package main

// brightness_windows.go — the screen brightness on Windows.
//
// Two real controls, tried in this order by the probe:
//
//   - DDC/CI through dxva2.dll: the external monitor JukaHub's window is on,
//     for a desktop PC. The monitor stores the level itself, and every call is
//     a slow I2C exchange, so it only runs on the worker. It is asked first
//     because a laptop's own panel never answers DDC/CI: on a laptop with
//     JukaHub on an external monitor, that monitor is the one that dims.
//   - WMI's WmiMonitorBrightness(Methods) in root\wmi: the panel of a laptop
//     or a Windows handheld — the level Windows' own brightness slider moves.
//     It is reached through Windows PowerShell's CIM cmdlets: there is no
//     dependency-free way to call WMI from Go, and PowerShell 5.1 ships with
//     every Windows 10 and 11. A call costs a process (about half a second),
//     which is why the controller never polls it with the card closed.
//
// With neither, the controller falls back to the software dimmer. None of
// this has been run on a Windows machine from here.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
	"golang.org/x/sys/windows"
)

// brightnessProbeBlocks: the probe may start PowerShell, so it runs on its own
// goroutine (brightnessStartProbe).
const brightnessProbeBlocks = true

const (
	// brightnessPowerShellTimeout bounds one WMI call. PowerShell's first
	// start after boot can take a few seconds.
	brightnessPowerShellTimeout = 10 * time.Second

	// brightnessWindowsProbeDelay keeps the probe off the first frames, which
	// are busy rasterising fonts; the sun appears once it has answered.
	brightnessWindowsProbeDelay = time.Second

	// winMonitorDefaultToPrimary is MONITOR_DEFAULTTOPRIMARY: a window that is
	// on no monitor still gets one.
	winMonitorDefaultToPrimary = 1

	// ddcMaxPhysicalMonitors bounds the array asked for: a cloned display can
	// put more than one physical monitor behind one HMONITOR, never dozens.
	ddcMaxPhysicalMonitors = 8
)

var (
	brightnessUser32      = windows.NewLazySystemDLL("user32.dll")
	procMonitorFromWindow = brightnessUser32.NewProc("MonitorFromWindow")
	procGetDesktopWindow  = brightnessUser32.NewProc("GetDesktopWindow")

	brightnessDxva2                             = windows.NewLazySystemDLL("dxva2.dll")
	procGetNumberOfPhysicalMonitorsFromHMONITOR = brightnessDxva2.NewProc("GetNumberOfPhysicalMonitorsFromHMONITOR")
	procGetPhysicalMonitorsFromHMONITOR         = brightnessDxva2.NewProc("GetPhysicalMonitorsFromHMONITOR")
	procDestroyPhysicalMonitors                 = brightnessDxva2.NewProc("DestroyPhysicalMonitors")
	procGetMonitorBrightness                    = brightnessDxva2.NewProc("GetMonitorBrightness")
	procSetMonitorBrightness                    = brightnessDxva2.NewProc("SetMonitorBrightness")
)

// brightnessWinMu keeps the poll and the worker from talking to the monitor
// (or starting PowerShell) at the same time: two DDC/CI exchanges on one bus
// fail each other.
var brightnessWinMu sync.Mutex

// brightnessPlatformProbe tries DDC/CI, then WMI, and names the first that
// reads a level, with that level. For WMI it also takes the panel's own
// number of levels, which sets the slider's step (brightnessWMIGrid).
func brightnessPlatformProbe() (string, int, bool) {
	time.Sleep(brightnessWindowsProbeDelay)
	if pct, ok := ddcBrightness(); ok {
		return brightnessBackendDDC, pct, true
	}
	if pct, levels, ok := wmiBrightnessLevels(); ok {
		brightnessWMIGrid.Store(int32(brightnessWMIGridFor(levels)))
		return brightnessBackendWMI, pct, true
	}
	return "", 0, false
}

// ── WMI ─────────────────────────────────────────────────────────────────────

// wmiPick selects the panel both scripts act on: the first active instance (a
// hybrid-graphics laptop can list a second, inactive one), else the first.
const wmiPick = `$m = @($all | Where-Object { $_.Active })[0]; if ($null -eq $m) { $m = $all[0] }; `

// wmiPrelude stops on the first error and keeps progress records out of the
// output the level is parsed from.
const wmiPrelude = `$ErrorActionPreference = 'Stop'; $ProgressPreference = 'SilentlyContinue'; `

// wmiBrightnessReadScript prints "<level> <number of levels>" for that panel.
const wmiBrightnessReadScript = wmiPrelude +
	`$all = @(Get-CimInstance -Namespace root/wmi -ClassName WmiMonitorBrightness); ` + wmiPick +
	`if ($null -eq $m) { throw 'no WMI brightness' }; ` +
	`'{0} {1}' -f $m.CurrentBrightness, $m.Levels`

// brightnessPowerShellPath is Windows PowerShell itself, not whatever a PATH
// entry calls "powershell".
func brightnessPowerShellPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	path := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(path); err != nil {
		return "powershell"
	}
	return path
}

// brightnessPowerShell runs one script in Windows PowerShell with no console
// window, bounded by brightnessPowerShellTimeout. The scripts are constants
// with at most a number formatted in: nothing a user typed reaches them. The
// caller holds brightnessWinMu.
func brightnessPowerShell(script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), brightnessPowerShellTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, brightnessPowerShellPath(), "-NoProfile", "-NonInteractive", "-Command", script)
	auditConfigureHidden(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("powershell: %w (%s)", err, firstLine(strings.TrimSpace(string(out))))
	}
	return string(out), nil
}

// wmiBrightnessLevels reads the panel's level through WMI, with how many
// levels the panel has (0 when it does not say).
func wmiBrightnessLevels() (pct, levels int, ok bool) {
	brightnessWinMu.Lock()
	defer brightnessWinMu.Unlock()
	out, err := brightnessPowerShell(wmiBrightnessReadScript)
	if err != nil {
		return 0, 0, false
	}
	return parseWMIBrightness(out)
}

// wmiBrightness reads the panel's level through WMI.
func wmiBrightness() (int, bool) {
	pct, _, ok := wmiBrightnessLevels()
	return pct, ok
}

// wmiSetBrightness sets the panel's level through WMI, at once (Timeout 0).
func wmiSetBrightness(pct int) error {
	script := fmt.Sprintf(wmiPrelude+
		`$all = @(Get-CimInstance -Namespace root/wmi -ClassName WmiMonitorBrightnessMethods); `+wmiPick+
		`if ($null -eq $m) { throw 'no WMI brightness control' }; `+
		`Invoke-CimMethod -InputObject $m -MethodName WmiSetBrightness `+
		`-Arguments @{ Timeout = [uint32]0; Brightness = [byte]%d } | Out-Null`, brightnessClamp(pct))
	brightnessWinMu.Lock()
	defer brightnessWinMu.Unlock()
	_, err := brightnessPowerShell(script)
	return err
}

// ── DDC/CI ──────────────────────────────────────────────────────────────────

// winPhysicalMonitor is dxva2's PHYSICAL_MONITOR: a HANDLE and a 128-WCHAR
// description.
type winPhysicalMonitor struct {
	handle      windows.Handle
	description [128]uint16
}

// brightnessWindowHandle is JukaHub's window (its HWND), noted on the render
// thread (brightnessNoteWindow) so DDC/CI asks the monitor the window is on;
// brightnessWindowTried keeps that to one attempt.
var (
	brightnessWindowHandle atomic.Uintptr
	brightnessWindowTried  atomic.Bool
)

// brightnessNoteWindow records the main window's handle, once. Main thread:
// SDL is asked for it.
func brightnessNoteWindow() {
	if mainWindow == nil || brightnessWindowTried.Load() {
		return
	}
	brightnessWindowTried.Store(true)
	info, err := mainWindow.GetWMInfo()
	if err != nil || info == nil || info.Subsystem != uint32(sdl.SYSWM_WINDOWS) {
		return
	}
	if w := info.GetWindowsInfo(); w != nil && w.Window != nil {
		brightnessWindowHandle.Store(uintptr(w.Window))
	}
}

// ddcMonitorHandle is the HMONITOR of the display JukaHub's window is on, or
// of the primary display before the window is known.
func ddcMonitorHandle() uintptr {
	hwnd := brightnessWindowHandle.Load()
	if hwnd == 0 {
		hwnd, _, _ = procGetDesktopWindow.Call()
	}
	hmon, _, _ := procMonitorFromWindow.Call(hwnd, winMonitorDefaultToPrimary)
	return hmon
}

// ddcWithMonitors opens the physical monitors behind JukaHub's display, runs
// fn on them and closes them again. Opening them per call keeps a monitor that
// was unplugged or switched off from leaving a dead handle behind. It holds
// brightnessWinMu throughout.
func ddcWithMonitors(fn func([]winPhysicalMonitor) error) error {
	for _, p := range []*windows.LazyProc{procMonitorFromWindow, procGetDesktopWindow,
		procGetNumberOfPhysicalMonitorsFromHMONITOR, procGetPhysicalMonitorsFromHMONITOR,
		procDestroyPhysicalMonitors, procGetMonitorBrightness, procSetMonitorBrightness} {
		if err := p.Find(); err != nil {
			return err
		}
	}
	brightnessWinMu.Lock()
	defer brightnessWinMu.Unlock()
	hmon := ddcMonitorHandle()
	if hmon == 0 {
		return errors.New("no monitor for the window")
	}
	var n uint32
	if r, _, e := procGetNumberOfPhysicalMonitorsFromHMONITOR.Call(hmon, uintptr(unsafe.Pointer(&n))); r == 0 {
		return fmt.Errorf("GetNumberOfPhysicalMonitorsFromHMONITOR: %v", e)
	}
	if n == 0 || n > ddcMaxPhysicalMonitors {
		return fmt.Errorf("%d physical monitors behind the display", n)
	}
	mons := make([]winPhysicalMonitor, n)
	if r, _, e := procGetPhysicalMonitorsFromHMONITOR.Call(hmon, uintptr(n), uintptr(unsafe.Pointer(&mons[0]))); r == 0 {
		return fmt.Errorf("GetPhysicalMonitorsFromHMONITOR: %v", e)
	}
	err := fn(mons)
	// Not deferred: the slice must stay referenced by this call expression,
	// which is what keeps it alive while dxva2 reads it.
	procDestroyPhysicalMonitors.Call(uintptr(n), uintptr(unsafe.Pointer(&mons[0])))
	return err
}

// ddcRange reads one monitor's brightness range and level.
func ddcRange(m winPhysicalMonitor) (lo, cur, hi int, err error) {
	var l, c, h uint32
	r, _, e := procGetMonitorBrightness.Call(uintptr(m.handle),
		uintptr(unsafe.Pointer(&l)), uintptr(unsafe.Pointer(&c)), uintptr(unsafe.Pointer(&h)))
	if r == 0 {
		return 0, 0, 0, fmt.Errorf("GetMonitorBrightness: %v", e)
	}
	if h <= l {
		return 0, 0, 0, fmt.Errorf("the monitor reports an empty brightness range %d..%d", l, h)
	}
	return int(l), int(c), int(h), nil
}

// ddcBrightness reads the first monitor that answers over DDC/CI.
func ddcBrightness() (int, bool) {
	pct, ok := 0, false
	_ = ddcWithMonitors(func(mons []winPhysicalMonitor) error {
		for _, m := range mons {
			if lo, cur, hi, err := ddcRange(m); err == nil {
				pct, ok = ddcPercentFor(cur, lo, hi), true
				return nil
			}
		}
		return errors.New("no monitor answered over DDC/CI")
	})
	return pct, ok
}

// ddcSetBrightness sets every monitor behind JukaHub's display that answers
// (a cloned display shows the same picture on each), in each one's own range.
func ddcSetBrightness(pct int) error {
	return ddcWithMonitors(func(mons []winPhysicalMonitor) error {
		var lastErr error
		set := 0
		for _, m := range mons {
			lo, _, hi, err := ddcRange(m)
			if err != nil {
				lastErr = err
				continue
			}
			if r, _, e := procSetMonitorBrightness.Call(uintptr(m.handle), uintptr(ddcRawFor(pct, lo, hi))); r == 0 {
				lastErr = fmt.Errorf("SetMonitorBrightness: %v", e)
				continue
			}
			set++
		}
		if set > 0 {
			return nil
		}
		if lastErr == nil {
			lastErr = errors.New("no monitor took the level")
		}
		return lastErr
	})
}
