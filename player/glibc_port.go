package main

// GreenOvercast is the only port for which the private C runtime has been
// checked on a TrimUI. Its PortMaster shell script still runs with the stock
// firmware libraries; a temporary copy changes only the final game command.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const greenOvercastExecLine = `"$GAMEDIR/webrtc_stream.aarch64" "$1"`
const greenOvercastPrivateExecLine = `GCONV_PATH="$JUKAHUB_PRIVATE_LIB/gconv" "$JUKAHUB_PRIVATE_LD" --inhibit-cache --library-path "$JUKAHUB_PRIVATE_LIB:$LD_LIBRARY_PATH" "$GAMEDIR/webrtc_stream.aarch64" "$1"`

func privateGlibcPortEligible(script string) bool {
	return runtime.GOOS == "linux" && runtime.GOARCH == "arm64" && IsTrimuiDevice() &&
		strings.EqualFold(filepath.Base(script), "GreenOvercast.sh")
}

func privateGlibcPortNeedsRuntime(script string) bool {
	if !privateGlibcPortEligible(script) {
		return false
	}
	_, old := goGlibcOld()
	return old
}

// The installed port is launchable once JukaHub can supply its tested runtime,
// even before the download has happened. Other ports keep the firmware check.
func privateGlibcPortCompat(script string, c portCompat) portCompat {
	if c.State != portCompatNeedsNewerLibc || !privateGlibcPortEligible(script) {
		return c
	}
	fields := strings.Fields(c.Note)
	if len(fields) < 2 || fields[0] != "GLIBC" ||
		elfVersionLess("GLIBC_"+privateGlibcVersion, "GLIBC_"+fields[1]) {
		return c
	}
	c.State = portCompatSupported
	c.Note = "JukaHub compatibility runtime"
	return c
}

func privateGlibcCatalogEligible(e mcCatalogEntry, device DeviceModel) bool {
	if !device.IsTrimui() || !strings.EqualFold(e.Zip, goZipName) || e.MinGlibc == "" {
		return false
	}
	need := "GLIBC_" + strings.TrimPrefix(e.MinGlibc, "GLIBC_")
	return !elfVersionLess("GLIBC_"+privateGlibcVersion, need)
}

// privateGlibcPatchGreenOvercast accepts the launcher form tested on-device.
// A changed upstream script is refused so the player sees a useful error
// instead of a game that appears to launch but cannot load its C library.
func privateGlibcPatchGreenOvercast(src []byte) ([]byte, error) {
	if len(src) == 0 || len(src) > 256<<10 {
		return nil, errors.New("GreenOvercast launcher has an unexpected size")
	}
	lines := strings.SplitAfter(string(src), "\n")
	count := 0
	for i, line := range lines {
		body := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if strings.TrimSpace(body) != greenOvercastExecLine {
			continue
		}
		count++
		indent := body[:len(body)-len(strings.TrimLeft(body, " \t"))]
		ending := line[len(body):]
		lines[i] = indent + greenOvercastPrivateExecLine + ending
	}
	if count != 1 {
		return nil, fmt.Errorf("GreenOvercast launcher changed: expected one game command, found %d", count)
	}
	return []byte(strings.Join(lines, "")), nil
}

// privateGlibcPreparePortPlan returns a temporary launcher to clean up after
// the child exits. No LD_LIBRARY_PATH is injected into Bash or firmware tools.
func privateGlibcPreparePortPlan(plan *portLaunchPlan, script string) (string, error) {
	if !privateGlibcPortNeedsRuntime(script) {
		return "", nil
	}
	loader, libDir, ready := privateGlibcPaths()
	if !ready {
		return "", errors.New("GreenOvercast needs its compatibility runtime; connect to Wi-Fi and launch it again")
	}
	if plan.Mode != portLaunchDirect || len(plan.Args) != 2 || plan.Args[1] != script {
		return "", errors.New("GreenOvercast cannot use its compatibility runtime through this PortMaster launcher")
	}
	src, err := os.ReadFile(script)
	if err != nil {
		return "", err
	}
	patched, err := privateGlibcPatchGreenOvercast(src)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "jukahub-greenovercast-*.sh")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err = f.Write(patched); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err = f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	plan.Args[1] = path
	plan.Env = mergeGameProfileEnv(plan.Env, map[string]string{
		"JUKAHUB_PRIVATE_LD":  loader,
		"JUKAHUB_PRIVATE_LIB": libDir,
		"PM_SCRIPTNAME":       filepath.Base(script),
	})
	plan.Notes = append(plan.Notes, "GreenOvercast uses JukaHub's private glibc "+privateGlibcVersion+" loader")
	return path, nil
}
