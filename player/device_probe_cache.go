package main

// device_probe_cache.go — the vendor launcher is read once per firmware, not
// once per start.
//
// On stock TrimUI firmware the model is often named only by the vendor
// launcher, /usr/trimui/bin/MainUI: megabytes that mainUIModelNames reads end
// to end, on the main thread, before anything else of JukaHub starts
// (InitLogging asks which device this is). That file only changes with a
// firmware update, so what the scan found is kept in data/device-probe.json
// with the file's identity (size, modification and change times, inode and
// device on Linux) and the list of names searched for, and a later start that
// finds the same file takes the names from there: one stat and a 200-byte
// read instead of the binary.
//
// Without a remembered answer, a cheaper source that already names the board
// (a launcher's exported device, the device tree, cpuinfo, the hostname:
// classifyDeviceDirect) means the binary cannot change the result, so it is
// not read at start at all; its names, which only the evidence lines (About,
// Drivers & hardware) show, are found on a goroutine after the first frame and
// remembered for the next start.
//
// JUKAHUB_DEVICE_PROBE_CACHE=0 brings back the scan on every start.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	deviceProbeCacheFile    = "device-probe.json"
	deviceProbeCacheVersion = 1
	// deviceProbeCacheMax bounds the read: the real file is a few hundred bytes.
	deviceProbeCacheMax = 4 << 10
)

// deviceProbeCache is data/device-probe.json.
type deviceProbeCache struct {
	Version  int      `json:"version"`
	Path     string   `json:"path"`     // the launcher binary that was scanned
	File     string   `json:"file"`     // its identity then (deviceFileStamp)
	Searched string   `json:"searched"` // the names looked for: another list scans again
	Names    []string `json:"names"`    // what the scan found
}

// deviceProbeCacheOff reports whether JUKAHUB_DEVICE_PROBE_CACHE turns the
// shortcut off.
func deviceProbeCacheOff() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("JUKAHUB_DEVICE_PROBE_CACHE"))) {
	case "0", "off", "false", "no":
		return true
	}
	return false
}

// deviceProbeSearched is the list of names a scan looks for, as stored.
func deviceProbeSearched() string { return strings.Join(firmwareModelNames, "|") }

// deviceFileStamp identifies the file at path for the cache: size and
// modification time everywhere, plus change time, inode and device where the
// platform has them (deviceFileStampExtra). ok is false when it is not a
// regular file.
func deviceFileStamp(path string) (string, bool) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	return strconv.FormatInt(fi.Size(), 10) + ":" + strconv.FormatInt(fi.ModTime().UnixNano(), 10) +
		deviceFileStampExtra(fi), true
}

// deviceProbeCachePath is where the answer is kept, "" when there is no data
// folder (or under go test, which must not leave one behind).
func deviceProbeCachePath() string {
	if testing.Testing() {
		return ""
	}
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, deviceProbeCacheFile)
}

// deviceLauncherNames is mainUIModelNames(deviceMainUIPath) for start-up, as
// cheaply as the evidence allows (see the top of the file). id carries the
// sources read so far: the environment, the device tree, cpuinfo, hostname.
func deviceLauncherNames(id deviceIdentity) []string {
	path := deviceMainUIPath
	if deviceProbeCacheOff() {
		return mainUIModelNames(path)
	}
	stamp, ok := deviceFileStamp(path)
	if !ok {
		// No launcher binary: the scan would have found nothing either.
		return nil
	}
	cachePath := deviceProbeCachePath()
	names, ok := deviceProbeCacheLoad(cachePath, path, stamp)
	if logger == nil {
		// Before logging exists (JukaHub OS's --os-session start, jukaosModel):
		// a log line here would start logging and DetectDevice's once-only
		// answer early, so this start only reads the record, silently, and
		// otherwise scans as it always did.
		if ok {
			return names
		}
		return mainUIModelNames(path)
	}
	if ok {
		logInfo("device", "launcher names remembered for this firmware: %q", names)
		return names
	}
	if d, source := classifyDeviceDirect(id); d != DeviceUnknown {
		logInfo("device", "launcher binary not read at start: the %s names the board", source)
		if cachePath != "" {
			// The evidence lines get its names from the next start on.
			deferStartupTask("device-launcher-names", func() {
				go deviceProbeScanAndSave(cachePath, path, stamp)
			})
		}
		return nil
	}
	started := time.Now()
	names = mainUIModelNames(path)
	logInfo("device", "launcher binary scanned in %d ms; remembered for the next start", time.Since(started).Milliseconds())
	if cachePath != "" {
		go deviceProbeCacheSave(cachePath, path, stamp, names)
	}
	return names
}

// deviceProbeScanAndSave scans the launcher off the start-up path and keeps
// the answer, unless the file changed meanwhile.
func deviceProbeScanAndSave(cachePath, path, stamp string) {
	names := mainUIModelNames(path)
	if now, ok := deviceFileStamp(path); !ok || now != stamp {
		return
	}
	deviceProbeCacheSave(cachePath, path, stamp, names)
}

// deviceProbeCacheLoad returns the remembered names when the record is for
// this very file and this list of names. Anything else — no record, another
// file, a damaged or foreign record — is a miss.
func deviceProbeCacheLoad(cachePath, path, stamp string) ([]string, bool) {
	if cachePath == "" {
		return nil, false
	}
	f, err := os.Open(cachePath)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	buf := make([]byte, deviceProbeCacheMax+1)
	n, _ := f.Read(buf)
	if n <= 0 || n > deviceProbeCacheMax {
		return nil, false
	}
	return parseDeviceProbeCache(buf[:n], path, stamp, deviceProbeSearched())
}

// parseDeviceProbeCache checks a record against the file it must describe.
// Only names from firmwareModelNames are accepted, so a hand-edited record
// cannot name a model the scan could never have found.
func parseDeviceProbeCache(data []byte, path, stamp, searched string) ([]string, bool) {
	var rec deviceProbeCache
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, false
	}
	if rec.Version != deviceProbeCacheVersion || rec.Path != path || rec.File != stamp || rec.Searched != searched {
		return nil, false
	}
	names := make([]string, 0, len(rec.Names))
	for _, name := range rec.Names {
		known := false
		for _, want := range firmwareModelNames {
			if name == want {
				known = true
				break
			}
		}
		if !known {
			return nil, false
		}
		names = append(names, name)
	}
	return names, true
}

// deviceProbeCacheSave keeps what a scan found. Best-effort: a card that
// cannot take the write only means the next start scans again.
func deviceProbeCacheSave(cachePath, path, stamp string, names []string) {
	if names == nil {
		names = []string{}
	}
	data, err := json.Marshal(deviceProbeCache{
		Version: deviceProbeCacheVersion, Path: path, File: stamp, Searched: deviceProbeSearched(), Names: names,
	})
	if err != nil {
		return
	}
	if err := AtomicWrite(cachePath, append(data, '\n'), 0o644); err != nil {
		logWarn("device", "the launcher's names could not be remembered: %v", err)
	}
}
