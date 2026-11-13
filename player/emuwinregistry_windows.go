//go:build windows

package main

// emuwinregistry_windows.go — where Windows says an emulator was installed.
//
// An installer can put RetroArch (or PPSSPP, Dolphin…) anywhere: D:\Games\
// RetroArch, a renamed folder, a second drive. The fixed folder probes in
// winSearchDirs miss every one of those, which is how a tester's RetroArch
// stayed "not found" next to a working install. Installers record the folder
// in the Uninstall keys Windows' "Installed apps" page reads, so that is where
// the catalog asks.

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// emuWinRegistryDirs lists the install folders of every app in the Uninstall
// keys (per user, per machine, 32-bit view) whose display name starts with one
// of names. A folder is the recorded InstallLocation, else the folder of the
// DisplayIcon or UninstallString file; folders that do not exist are dropped.
func emuWinRegistryDirs(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	lowered := make([]string, 0, len(names))
	for _, n := range names {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			lowered = append(lowered, n)
		}
	}
	wanted := func(display string) bool {
		display = strings.ToLower(strings.TrimSpace(display))
		for _, n := range lowered {
			if strings.HasPrefix(display, n) {
				return true
			}
		}
		return false
	}
	var out []string
	seen := map[string]bool{}
	// add keeps an absolute folder that exists; false sends the caller on to
	// the next value. "MsiExec.exe /X{…}" would otherwise give ".", JukaHub's
	// own working folder.
	add := func(dir string) bool {
		dir = strings.TrimSpace(dir)
		if dir == "" || !filepath.IsAbs(dir) {
			return false
		}
		dir = filepath.Clean(dir)
		if !dirExists(dir) {
			return false
		}
		if key := strings.ToLower(dir); !seen[key] {
			seen[key] = true
			out = append(out, dir)
		}
		return true
	}
	const uninstall = `Software\Microsoft\Windows\CurrentVersion\Uninstall`
	for _, src := range []struct {
		root registry.Key
		path string
	}{
		{registry.CURRENT_USER, uninstall},
		{registry.LOCAL_MACHINE, uninstall},
		{registry.LOCAL_MACHINE, `Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`},
	} {
		parent, err := registry.OpenKey(src.root, src.path, registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			continue
		}
		subs, _ := parent.ReadSubKeyNames(-1)
		_ = parent.Close()
		for _, sub := range subs {
			k, err := registry.OpenKey(src.root, src.path+`\`+sub, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			display, _, _ := k.GetStringValue("DisplayName")
			if !wanted(display) && !wanted(sub) {
				_ = k.Close()
				continue
			}
			// The recorded InstallLocation first; a stale or empty one falls
			// through to the folder of the icon or the uninstaller.
			loc, _, _ := k.GetStringValue("InstallLocation")
			if !add(strings.Trim(strings.TrimSpace(loc), `"`)) {
				for _, value := range []string{"DisplayIcon", "UninstallString"} {
					v, _, err := k.GetStringValue(value)
					if err != nil {
						continue
					}
					if file := emuWinRegistryFile(v); file != "" && add(filepath.Dir(file)) {
						break
					}
				}
			}
			_ = k.Close()
		}
	}
	return out
}

// emuWinRegistryFile is the file a DisplayIcon / UninstallString value names:
// `"C:\RetroArch-Win64\uninstall.exe" /S` and `C:\x\retroarch.exe,0` both
// give the path before the arguments or icon index.
func emuWinRegistryFile(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, `"`) {
		if end := strings.Index(v[1:], `"`); end >= 0 {
			return v[1 : 1+end]
		}
		return ""
	}
	lower := strings.ToLower(v)
	if i := strings.Index(lower, ".exe"); i >= 0 {
		return v[:i+len(".exe")]
	}
	if i := strings.LastIndexByte(v, ','); i > 0 {
		v = v[:i]
	}
	return v
}
