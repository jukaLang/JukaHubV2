package main

// componentreport.go — the component-versions report.
//
// Patch already stores a SHA-256 for every file it installs (db.json), and the
// release archive carries release-info.json, but the UI only ever showed one
// version string. A bug report that cannot say which yt-dlp, which launcher or
// which installed package a device is running sends support round trips after
// facts the card can list in one screen.
//
// The report is deliberately path-free: names, versions and digests only. It is
// printed by "Component versions" in Patch and appended to DiagnosticsText, so
// the same facts land in the report a user pastes into an issue.

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// componentReportRow is one row of the report.
type componentReportRow struct {
	Name    string
	Version string
	Digest  string // hex SHA-256, empty when the file is absent or unreadable
	Note    string
}

// componentToolVersions caches version probes. Each probe starts a process, and
// the card is opened from a menu that has to answer the same frame. A failed
// probe is tried again after componentToolRetry (yt-dlp, which failed, was
// probed for up to 3 s on the UI thread at every bug report), and a probe
// claimed elsewhere (componentVersionsWarm) is not waited for: the row has no
// version this time. The lock is never held while a probe runs.
var componentToolVersions struct {
	sync.Mutex
	seen    map[string]string
	failed  map[string]time.Time
	running map[string]bool
}

// componentToolRetry spaces probes of a tool whose probe failed.
const componentToolRetry = 10 * time.Minute

// componentToolClaim returns a cached version, or claims the probe of path
// for the caller (claimed); neither when another caller has it or it failed
// recently.
func componentToolClaim(path string) (version string, claimed bool) {
	c := &componentToolVersions
	c.Lock()
	defer c.Unlock()
	if v, ok := c.seen[path]; ok {
		return v, false
	}
	if t, failed := c.failed[path]; c.running[path] || (failed && time.Since(t) < componentToolRetry) {
		return "", false
	}
	if c.running == nil {
		c.seen, c.failed, c.running = map[string]string{}, map[string]time.Time{}, map[string]bool{}
	}
	c.running[path] = true
	return "", true
}

// componentToolRun runs a claimed probe and records how it went.
func componentToolRun(path string) (version string) {
	c := &componentToolVersions
	defer func() {
		c.Lock()
		delete(c.running, path)
		if version != "" {
			c.seen[path] = version
			delete(c.failed, path)
		} else {
			c.failed[path] = time.Now()
		}
		c.Unlock()
	}()
	return toolVersionFromPath(path)
}

func probeComponentToolVersion(path string) string {
	v, claimed := componentToolClaim(path)
	if !claimed {
		return v
	}
	return componentToolRun(path)
}

// componentDigests keeps each component's SHA-256 by path, size and change
// time. Sending a bug report hashed JukaHub's own binary, ffmpeg and ffprobe on
// the UI thread every time, and the stall watchdog caught a 1.7 s frame on a
// Brick (JH-QCKJ9M). componentVersionsWarm claims every file on the main
// thread when Report a bug opens and hashes them in the background; a report
// built while a file is still claimed says so instead of waiting for it.
var componentDigests struct {
	sync.Mutex
	done     map[string]componentDigestEntry
	inflight map[string]chan struct{}
}

type componentDigestEntry struct {
	size int64
	mod  time.Time
	sum  string
}

// componentHashingNote is a row's note while its file is being hashed.
const componentHashingNote = "still being hashed"

// errComponentHashing says the file is being hashed elsewhere right now.
var errComponentHashing = errors.New(componentHashingNote)

// componentHashClaim returns a cached digest (ok), or claims the hash of path
// for the caller (ch, to pass to componentHashRun), or neither when another
// caller holds it (errComponentHashing).
func componentHashClaim(path string, info os.FileInfo) (sum string, ch chan struct{}, err error) {
	componentDigests.Lock()
	defer componentDigests.Unlock()
	if e, ok := componentDigests.done[path]; ok && e.size == info.Size() && e.mod.Equal(info.ModTime()) {
		return e.sum, nil, nil
	}
	if _, busy := componentDigests.inflight[path]; busy {
		return "", nil, errComponentHashing
	}
	if componentDigests.inflight == nil {
		componentDigests.inflight = map[string]chan struct{}{}
	}
	ch = make(chan struct{})
	componentDigests.inflight[path] = ch
	return "", ch, nil
}

// componentHash is SHA256File through componentDigests.
func componentHash(path string, info os.FileInfo) (string, error) {
	sum, ch, err := componentHashClaim(path, info)
	if err != nil || ch == nil {
		return sum, err
	}
	return componentHashRun(path, info, ch)
}

// componentHashRun hashes a claimed path and records the result; the claim is
// released however SHA256File ends, a panic included.
func componentHashRun(path string, info os.FileInfo, ch chan struct{}) (sum string, err error) {
	defer func() {
		componentDigests.Lock()
		delete(componentDigests.inflight, path)
		if err == nil && sum != "" {
			if componentDigests.done == nil {
				componentDigests.done = map[string]componentDigestEntry{}
			}
			componentDigests.done[path] = componentDigestEntry{size: info.Size(), mod: info.ModTime(), sum: sum}
		}
		componentDigests.Unlock()
		close(ch)
	}()
	return SHA256File(path)
}

// componentDigest returns the SHA-256 of a component file and a short note when
// it is absent. An empty digest with no note means the file exists but could not
// be hashed.
func componentDigest(path string) (digest string, note string) {
	if strings.TrimSpace(path) == "" {
		return "", "not found"
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", "not installed"
	}
	sum, err := componentHash(path, info)
	if errors.Is(err, errComponentHashing) {
		return "", componentHashingNote
	}
	if err != nil {
		return "", "unreadable"
	}
	return sum, ""
}

// componentVersionsWarm claims, on the main thread, every component file that
// has no current digest and every helper tool with no known version, then
// hashes and probes them in the background, so a report built a moment later
// neither waits for them nor does the work itself on the UI thread.
func componentVersionsWarm(config *Config) {
	type claimedFile struct {
		path string
		info os.FileInfo
		ch   chan struct{}
	}
	var files []claimedFile
	var tools []string
	claimFile := func(p string) {
		if strings.TrimSpace(p) == "" {
			return
		}
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return
		}
		if _, ch, err := componentHashClaim(p, info); err == nil && ch != nil {
			files = append(files, claimedFile{p, info, ch})
		}
	}
	if exe, err := os.Executable(); err == nil {
		claimFile(exe)
		claimFile(filepath.Join(filepath.Dir(exe), "launch.sh"))
	}
	claimFile(resolvePath("jukaconfig.json"))
	for _, tool := range []string{"yt-dlp", "ffmpeg", "ffprobe"} {
		p := getToolPath(tool, config)
		if strings.TrimSpace(p) == "" || !regularFile(p) {
			continue
		}
		claimFile(p)
		if _, claimed := componentToolClaim(p); claimed {
			tools = append(tools, p)
		}
	}
	if len(files) == 0 && len(tools) == 0 {
		return
	}
	go func() {
		// Every claim is released, whatever happens to the ones before it.
		for _, f := range files {
			func() {
				defer func() {
					if r := recover(); r != nil {
						logWarn("diagnostics", "hashing a component failed: %v", r)
					}
				}()
				_, _ = componentHashRun(f.path, f.info, f.ch)
			}()
		}
		for _, p := range tools {
			func() {
				defer func() {
					if r := recover(); r != nil {
						logWarn("diagnostics", "asking a helper tool its version failed: %v", r)
					}
				}()
				componentToolRun(p)
			}()
		}
	}()
}

// componentVersions builds the report. It is intentionally cheap to call once
// per open: files are hashed and tool versions probed, so callers must not run
// it in a render loop.
func componentVersions(config *Config) []componentReportRow {
	out := make([]componentReportRow, 0, 12)

	// The app itself. os.Executable is the file that is actually running, which
	// is the one a support report needs to match against the published digest.
	appDigest, appNote := "", "unknown"
	if exe, err := os.Executable(); err == nil {
		appDigest, appNote = componentDigest(exe)
		if appDigest == "" && appNote != componentHashingNote {
			appNote = "not readable"
		}
	}
	out = append(out, componentReportRow{
		Name:    "JukaHub",
		Version: versionString(config),
		Digest:  appDigest,
		Note:    appNote,
	})

	// The launcher script is what starts the app on the handheld and is
	// replaced by updates, so a stale copy is a real support case.
	launcherPath := ""
	if exe, err := os.Executable(); err == nil {
		launcherPath = filepath.Join(filepath.Dir(exe), "launch.sh")
	}
	launcherDigest, launcherNote := componentDigest(launcherPath)
	if launcherDigest == "" && launcherNote == "not installed" {
		launcherNote = "not installed (desktop start)"
	}
	out = append(out, componentReportRow{
		Name:   "Launcher",
		Digest: launcherDigest,
		Note:   launcherNote,
	})

	// The design config is data, not code: its digest is how support tells a
	// stock layout from one edited in the web builder.
	configDigest, configNote := componentDigest(resolvePath("jukaconfig.json"))
	out = append(out, componentReportRow{
		Name:   "Config",
		Digest: configDigest,
		Note:   configNote,
	})

	// Helper tools download themselves, so their version drifts between
	// installs without any app update.
	for _, tool := range []string{"yt-dlp", "ffmpeg", "ffprobe"} {
		path := getToolPath(tool, config)
		digest, note := componentDigest(path)
		version := ""
		if digest != "" {
			version = probeComponentToolVersion(path)
			note = "helper tool"
		}
		out = append(out, componentReportRow{
			Name:    tool,
			Version: version,
			Digest:  digest,
			Note:    note,
		})
	}

	// Installed packages come from the Patch database. The first owned file's
	// digest identifies the build that was installed; the file count shows how
	// much of the package is on disk.
	patchDBEnsureLoaded()
	patchDBMu.Lock()
	pkgs := make([]DBInstalledPkg, len(patchDB.Installed))
	copy(pkgs, patchDB.Installed)
	patchDBMu.Unlock()
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Name < pkgs[j].Name })
	for _, p := range pkgs {
		name := strings.TrimSpace(p.Title)
		if name == "" {
			name = p.Name
		}
		digest := ""
		for _, f := range p.Files {
			if f.SHA256 != "" {
				digest = f.SHA256
				break
			}
		}
		note := "package · " + pluralizeCount(len(p.Files), "file", "files")
		if p.Held {
			note += " · held"
		}
		out = append(out, componentReportRow{
			Name:    name,
			Version: p.Version,
			Digest:  digest,
			Note:    note,
		})
	}
	return out
}

// componentDigestShort is the form shown on screen; the copyable report carries
// the full 64-character value.
func componentDigestShort(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}

// componentVersionsText renders the report. Compact mode is the on-screen card
// (one short line per component); full mode is what "Copy report" and the
// diagnostics export carry.
func componentVersionsText(config *Config, full bool) string {
	var sb strings.Builder
	sb.WriteString("JukaHub components\n")
	for _, c := range componentVersions(config) {
		digest := c.Digest
		if digest == "" {
			digest = "unavailable"
		} else if !full {
			digest = componentDigestShort(digest)
		}
		line := c.Name
		if c.Version != "" {
			line += " " + c.Version
		}
		line += "  sha256:" + digest
		if full && c.Note != "" {
			line += "  (" + c.Note + ")"
		}
		sb.WriteString(line + "\n")
	}
	if !full {
		sb.WriteString("\n" + hintSentence("Press %s to copy the full 64-character hashes.", "A", "Copy"))
	}
	return strings.TrimRight(sb.String(), "\n")
}
