package main

// bluetooth_tools.go — finding the Bluetooth programs a handheld ships, and
// running them the way its firmware would.
//
// Every Bluetooth probe used to look its tool up on JukaHub's PATH alone
// (commandExists) and run it with JukaHub's own environment. Neither holds on
// the handhelds:
//
//   - PATH is whatever the launcher handed launch.sh. The Wi-Fi code already
//     learned that it can miss the sbin directories where firmware images keep
//     rfkill (wifiToolDirs); BlueZ puts its daemon in libexec, TrimUI its own
//     tools in /usr/trimui/bin, CrossMix in System/bin and NextUI in .system/bin
//     on the card. A tool missing from PATH made the whole Bluetooth page, the top
//     bar's button and Quick Settings' switch read "no bluetoothctl" on a
//     handheld that has one.
//   - JukaHub's library path puts its own folder and /usr/lib ahead of the
//     firmware's list (launch.sh). `bluetoothctl show` and `power on` ran with
//     it, while the page's sessions ran with the firmware's (bzChildEnv), so the
//     same binary could load different libraries depending on who asked.
//
// btToolPath finds a tool on PATH first and then in fixed system directories,
// never anywhere a config names; btToolCmd runs it with the firmware's
// environment (screenChildEnv) — one rule for every Bluetooth program.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// btToolDirs are the fixed directories a Bluetooth tool is looked for in when
// PATH does not have it, in order: the standard system ones, BlueZ's private
// daemon directories, TrimUI's own tools, and CrossMix/NextUI additions on the card.
var btToolDirs = []string{
	"/usr/bin", "/usr/sbin", "/bin", "/sbin", "/usr/local/bin", "/usr/local/sbin",
	"/usr/libexec/bluetooth", "/usr/lib/bluetooth",
	"/usr/trimui/bin", "/mnt/SDCARD/System/bin", "/mnt/SDCARD/.system/bin",
}

// btToolCache remembers where each tool was found (or that it was not), for
// btToolCacheFor: the bar's button and Settings ask every few frames.
var btToolCache struct {
	mu    sync.Mutex
	at    time.Time
	paths map[string]string
}

// btToolCacheFor is how long a lookup stands. A firmware does not grow a tool
// while JukaHub runs, but CrossMix's card folder can appear after a late mount.
const btToolCacheFor = 30 * time.Second

// btToolPath is the program to run for name ("bluetoothctl", "rfkill",
// "hciattach", …) and whether there is one. It keeps the resolved path: the
// child's firmware PATH may differ from JukaHub's PATH, so a bare name found
// by one can fail to start under the other. Only bare names are looked up.
func btToolPath(name string) (string, bool) {
	if name == "" || strings.ContainsAny(name, `/\`) {
		return "", false
	}
	c := &btToolCache
	c.mu.Lock()
	if c.paths == nil || time.Since(c.at) > btToolCacheFor {
		c.paths, c.at = map[string]string{}, time.Now()
	}
	if p, ok := c.paths[name]; ok {
		c.mu.Unlock()
		return p, p != ""
	}
	c.mu.Unlock()
	p := btToolLookup(name)
	c.mu.Lock()
	if c.paths != nil {
		c.paths[name] = p
	}
	c.mu.Unlock()
	return p, p != ""
}

// btToolLookup finds name now: PATH, then the fixed directories (a regular
// file with an execute bit).
func btToolLookup(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		if absolute, err := filepath.Abs(path); err == nil {
			return absolute
		}
		return path
	}
	for _, dir := range btToolDirs {
		cand := filepath.Join(dir, name)
		if btIsExecutable(cand) {
			return cand
		}
	}
	return ""
}

// btToolExists reports whether the tool can be found at all.
func btToolExists(name string) bool {
	_, ok := btToolPath(name)
	return ok
}

// btToolCmd is name ready to run with args under ctx, with the firmware's
// PATH and library path (screenChildEnv), or nil when there is no such tool.
func btToolCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	path, ok := btToolPath(name)
	if !ok {
		return nil
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = screenChildEnv(os.Environ())
	cmd.WaitDelay = time.Second
	return cmd
}

// btToolRun runs a Bluetooth tool to its end, bounded by systemCommandTimeout,
// and returns its combined output. A missing tool is exec.ErrNotFound.
func btToolRun(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), systemCommandTimeout)
	defer cancel()
	cmd := btToolCmd(ctx, name, args...)
	if cmd == nil {
		return "", exec.ErrNotFound
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}
