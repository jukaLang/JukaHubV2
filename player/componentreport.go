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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// componentReportRow is one row of the report.
type componentReportRow struct {
	Name    string
	Version string
	Digest  string // hex SHA-256, empty when the file is absent or unreadable
	Note    string
}

// componentToolVersions caches version probes. Each probe starts a process, and
// the card is opened from a menu that has to answer the same frame. Failures
// are not cached: a tool installed a moment ago should report its version the
// next time the card is opened, without a restart.
var componentToolVersions struct {
	sync.Mutex
	seen map[string]string
}

func probeComponentToolVersion(path string) string {
	componentToolVersions.Lock()
	defer componentToolVersions.Unlock()
	if v, ok := componentToolVersions.seen[path]; ok {
		return v
	}
	v := toolVersionFromPath(path)
	if v != "" {
		if componentToolVersions.seen == nil {
			componentToolVersions.seen = map[string]string{}
		}
		componentToolVersions.seen[path] = v
	}
	return v
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
	sum, err := SHA256File(path)
	if err != nil {
		return "", "unreadable"
	}
	return sum, ""
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
		if appDigest == "" {
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
		note := fmt.Sprintf("package · %d file(s)", len(p.Files))
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
		sb.WriteString("\nPress A to copy the full 64-character hashes.")
	}
	return strings.TrimRight(sb.String(), "\n")
}
