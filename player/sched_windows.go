//go:build windows

package main

// sched_windows.go — the Windows half of the Scheduled tasks screen: the apps
// that start with Windows, read from the registry's Run and RunOnce keys and
// the two Startup folders, with their enabled state taken from the same
// StartupApproved flags Task Manager reads and writes. Task Scheduler itself is
// read with schtasks (sched.go); nothing here mentions or needs cron.

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"
)

// schedCreateNoWindow is CREATE_NO_WINDOW: schtasks is a console program, and
// started from a windowed app it would flash a console over the UI.
const schedCreateNoWindow = 0x08000000

// schedApprovedRoot is where Task Manager keeps its startup on/off flags.
const schedApprovedRoot = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\`

// schedHideWindow starts a helper without a console window.
func schedHideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: schedCreateNoWindow}
}

// schedApprovedOff reports whether Task Manager's flag for name under key says
// "disabled": the first byte of the 12-byte value is even when the app is on
// (02, 06) and odd when it is off (03, 07). No flag means on.
func schedApprovedOff(root registry.Key, key, name string) bool {
	k, err := registry.OpenKey(root, schedApprovedRoot+key, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	data, _, err := k.GetBinaryValue(name)
	if err != nil || len(data) == 0 {
		return false
	}
	return data[0]&1 == 1
}

// schedRunKey is one Run / RunOnce key to list.
type schedRunKey struct {
	root     registry.Key
	rootName string
	path     string
	view     uint32 // WOW64 view flag, 0 for the shared HKCU keys
	once     bool
	approved string // StartupApproved subkey, "" for RunOnce
	user     bool
}

// schedRegistryStartupItems lists the programs Windows starts at logon from the
// registry: the current user's Run and RunOnce, and the machine-wide ones in
// both the 64-bit and the 32-bit view.
func schedRegistryStartupItems() []schedItem {
	const run = `Software\Microsoft\Windows\CurrentVersion\Run`
	const runOnce = `Software\Microsoft\Windows\CurrentVersion\RunOnce`
	keys := []schedRunKey{
		{root: registry.CURRENT_USER, rootName: "HKCU", path: run, approved: "Run", user: true},
		{root: registry.CURRENT_USER, rootName: "HKCU", path: runOnce, once: true, user: true},
		{root: registry.LOCAL_MACHINE, rootName: "HKLM", path: run, view: registry.WOW64_64KEY, approved: "Run"},
		{root: registry.LOCAL_MACHINE, rootName: "HKLM", path: run, view: registry.WOW64_32KEY, approved: "Run32"},
		{root: registry.LOCAL_MACHINE, rootName: "HKLM", path: runOnce, view: registry.WOW64_64KEY, once: true},
		{root: registry.LOCAL_MACHINE, rootName: "HKLM", path: runOnce, view: registry.WOW64_32KEY, once: true},
	}
	seen := map[string]bool{}
	var out []schedItem
	for _, rk := range keys {
		k, err := registry.OpenKey(rk.root, rk.path, registry.QUERY_VALUE|rk.view)
		if err != nil {
			continue
		}
		names, err := k.ReadValueNames(0)
		if err != nil {
			k.Close()
			continue
		}
		for _, name := range names {
			cmd, typ, err := k.GetStringValue(name)
			if err != nil {
				continue
			}
			if typ == registry.EXPAND_SZ {
				if expanded, xerr := registry.ExpandString(cmd); xerr == nil {
					cmd = expanded
				}
			}
			key := rk.rootName + "|" + rk.path + "|" + name + "|" + cmd
			if seen[key] {
				// 32-bit Windows has one view, so both reads return the same.
				continue
			}
			seen[key] = true
			it := schedItem{
				Name:     name,
				Command:  cmd,
				Schedule: "At logon",
				Status:   "Enabled",
				Enabled:  true,
				Source:   rk.rootName + `\` + rk.path,
				Icon:     "startup",
			}
			if rk.view == registry.WOW64_32KEY {
				it.Source = rk.rootName + `\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\` + filepath.Base(rk.path)
			}
			if rk.once {
				it.Schedule = "At the next logon only"
			}
			if rk.user {
				it.Group = "Your startup apps"
				it.User = "You"
			} else {
				it.Group = "Startup apps for all users"
				it.User = "Every user"
			}
			if rk.approved != "" && schedApprovedOff(rk.root, rk.approved, name) {
				it.Enabled = false
				it.Status = "Disabled"
			}
			if rk.user && rk.approved != "" {
				it.Toggle = schedToggleApproved
				it.ToggleKey = schedApprovedRoot + rk.approved
				it.ToggleName = name
			}
			out = append(out, it)
		}
		k.Close()
	}
	return out
}

// schedStartupFolderItems lists the shortcuts and programs in the user's and
// the shared Startup folders. JukaHub's own launcher is listed separately (as
// the JukaHub item), so it is skipped here.
func schedStartupFolderItems() []schedItem {
	own := ""
	if p, err := autoStartLauncherPath(); err == nil {
		own = strings.ToLower(filepath.Clean(p))
	}
	folders := []struct {
		dir  string
		root registry.Key
		user bool
	}{
		{filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup"), registry.CURRENT_USER, true},
		{filepath.Join(os.Getenv("ProgramData"), "Microsoft", "Windows", "Start Menu", "Programs", "StartUp"), registry.LOCAL_MACHINE, false},
	}
	var out []schedItem
	for _, f := range folders {
		if strings.TrimSpace(f.dir) == "" || !filepath.IsAbs(f.dir) {
			continue
		}
		entries, err := os.ReadDir(f.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || strings.EqualFold(name, "desktop.ini") || strings.HasPrefix(name, ".") {
				continue
			}
			full := filepath.Join(f.dir, name)
			if own != "" && strings.ToLower(filepath.Clean(full)) == own {
				continue
			}
			display := strings.TrimSuffix(name, filepath.Ext(name))
			it := schedItem{
				Name:     display,
				Command:  full,
				Schedule: "At logon",
				Status:   "Enabled",
				Enabled:  true,
				Source:   f.dir,
				Icon:     "startup",
			}
			if f.user {
				it.Group = "Your startup apps"
				it.User = "You"
				it.Toggle = schedToggleApproved
				it.ToggleKey = schedApprovedRoot + "StartupFolder"
				it.ToggleName = name
			} else {
				it.Group = "Startup apps for all users"
				it.User = "Every user"
			}
			if schedApprovedOff(f.root, "StartupFolder", name) {
				it.Enabled = false
				it.Status = "Disabled"
			}
			out = append(out, it)
		}
	}
	return out
}

// schedSetApproved switches one of the current user's startup apps on or off
// the way Task Manager does: a 12-byte flag under StartupApproved (02 = on,
// 03 + the time it was switched off = off). Nothing is deleted, so turning it
// back on restores it exactly.
func schedSetApproved(key, name string, enabled bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, key, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	data := make([]byte, 12)
	if enabled {
		data[0] = 0x02
	} else {
		data[0] = 0x03
		ft := syscall.NsecToFiletime(time.Now().UnixNano())
		binary.LittleEndian.PutUint32(data[4:8], ft.LowDateTime)
		binary.LittleEndian.PutUint32(data[8:12], ft.HighDateTime)
	}
	return k.SetBinaryValue(name, data)
}
