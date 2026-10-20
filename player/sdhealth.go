package main

// sdhealth.go — SD-card health.
//
// Most "JukaHub broke" reports come from the card, not the app: a failing card,
// a card too slow for the emulator it feeds, or a FAT32 volume that cannot hold
// a 4 GB file. This check answers three questions the user cannot easily answer
// themselves:
//
//   - what filesystem is this, and what limits come with it?
//   - how fast can it actually write (measured, not guessed)?
//   - does a written block read back unchanged?
//
// It is read-mostly and bounded: one 2 MiB scratch file in the app's own state
// directory, synced, read back and deleted. It never fills the card, because a
// capacity-fill test on a card that already holds someone's saves is not a
// diagnostic, it is data loss. The report says so instead of pretending a
// wrap-around test ran.

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// sdProbeBytes is the write probe size: big enough to get past caches,
	// small enough to finish quickly on a slow card.
	sdProbeBytes = 2 << 20
	// sdProbeChunk is the write cadence used to time the probe.
	sdProbeChunk = 64 << 10
	// sdSlowWriteMBps is where a card is too slow for reliable saves.
	sdSlowWriteMBps = 2.0
	// sdMinimumFreeBytes is the room an update needs for a staged copy.
	sdMinimumFreeBytes = 512 << 20
	// sdHealthTTL caches a report so opening Diagnostics twice does not write
	// to the card twice.
	sdHealthTTL = 10 * time.Minute
)

// sdHealthReport is one card's measured state.
type sdHealthReport struct {
	FSType    string
	Total     int64
	Free      int64
	WriteMBps float64
	Verified  bool
	Probed    bool
	Findings  []string
	Checked   time.Time
}

// sdFindings is the pure half: given measurements, what should the user know?
// A nil slice means the card looks healthy, so the UI can say "no problems".
func sdFindings(fsType string, total, free int64, writeMBps float64, probed, verified bool) []string {
	var out []string
	switch strings.ToLower(fsType) {
	case "vfat", "msdos", "fat", "fat32":
		out = append(out, "FAT32: no journal, and files over 4 GB cannot be saved (use exFAT for large disc images).")
	case "exfat":
		// exFAT is the right choice on these cards; no finding to report.
	case "":
		// Unknown mount (desktop, unusual firmware): say nothing about it.
	}
	if total > 0 && free > total {
		out = append(out, "Filesystem reports more free space than capacity — the card may be corrupt.")
	}
	if free >= 0 && free < sdMinimumFreeBytes {
		out = append(out, fmt.Sprintf("Only %d MiB free: an update needs about 512 MiB for its staged copy.", free>>20))
	}
	if probed {
		if !verified {
			out = append(out, "Write check did not read back correctly — the card may be failing.")
		}
		if writeMBps > 0 && writeMBps < sdSlowWriteMBps {
			out = append(out, fmt.Sprintf("Slow card: %.1f MB/s write (below %.0f MB/s) — expect stutter and long saves.", writeMBps, sdSlowWriteMBps))
		}
	}
	return out
}

// sdStatusLabel summarises findings for a one-line row.
func sdStatusLabel(r sdHealthReport) string {
	switch len(r.Findings) {
	case 0:
		if !r.Probed {
			return "not measured"
		}
		return "OK"
	case 1:
		return "1 warning"
	default:
		return fmt.Sprintf("%d warnings", len(r.Findings))
	}
}

// diskUsage reports total and free bytes for the filesystem holding path.
func diskUsage(path string) (total, free int64, err error) {
	var st syscallStatfs
	if err := statfs(path, &st); err != nil {
		return 0, 0, err
	}
	if st.Bsize <= 0 {
		return 0, 0, fmt.Errorf("statfs reported block size %d", st.Bsize)
	}
	return st.Blocks * st.Bsize, st.Bavail * st.Bsize, nil
}

// mountEntry is one line of /proc/mounts.
type mountEntry struct {
	Point string
	Type  string
}

// parseMounts extracts mount point and filesystem type from /proc/mounts
// content, unescaping the octal sequences the kernel uses for spaces.
func parseMounts(data string) []mountEntry {
	var out []mountEntry
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		out = append(out, mountEntry{Point: unescapeMountPath(fields[1]), Type: fields[2]})
	}
	return out
}

// unescapeMountPath turns \040 and friends back into their characters.
func unescapeMountPath(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			octal := s[i+1 : i+4]
			var v int
			ok := true
			for j := 0; j < len(octal); j++ {
				if octal[j] < '0' || octal[j] > '7' {
					ok = false
					break
				}
				v = v*8 + int(octal[j]-'0')
			}
			if ok && v > 0 && v < 256 {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// fsTypeFor finds the filesystem type for path: the longest mount point that
// contains it wins, which is how nested mounts (/mnt/SDCARD/Emus) resolve
// correctly.
func fsTypeFor(mounts []mountEntry, path string) string {
	// /proc/mounts is always POSIX, so the comparison uses slash semantics even
	// when this runs on Windows for tests or development.
	clean := pathpkg.Clean(strings.ReplaceAll(path, "\\", "/"))
	best, bestLen := "", -1
	for _, m := range mounts {
		point := pathpkg.Clean(strings.ReplaceAll(m.Point, "\\", "/"))
		if point == "/" || clean == point || strings.HasPrefix(clean, point+"/") {
			if len(point) > bestLen {
				best, bestLen = m.Type, len(point)
			}
		}
	}
	return best
}

// readMounts reads the kernel mount table. Non-Linux systems answer nil, which
// leaves the filesystem column unknown rather than wrong.
func readMounts() []mountEntry {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}
	return parseMounts(string(data))
}

// sdWriteProbe writes sdProbeBytes of deterministic data into dir, syncs it,
// reads it back and deletes it. It returns the measured write speed in MB/s and
// whether the bytes read back unchanged.
func sdWriteProbe(dir string) (mbps float64, verified bool, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, false, err
	}
	path := filepath.Join(dir, ".jukahub-sdcheck")
	defer os.Remove(path)

	chunk := make([]byte, sdProbeChunk)
	rng := rand.New(rand.NewSource(1)) // fixed seed: same card, same work
	start := time.Now()
	f, err := os.Create(path)
	if err != nil {
		return 0, false, err
	}
	written := 0
	for written < sdProbeBytes {
		rng.Read(chunk)
		if _, err := f.Write(chunk); err != nil {
			f.Close()
			return 0, false, err
		}
		written += len(chunk)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return 0, false, err
	}
	if err := f.Close(); err != nil {
		return 0, false, err
	}
	elapsed := time.Since(start)
	if elapsed <= 0 {
		elapsed = time.Nanosecond
	}

	back, err := os.ReadFile(path)
	if err != nil {
		return 0, false, err
	}
	verified = len(back) == written
	if verified {
		// Regenerate and compare in chunks so a 2 MiB allocation is avoided.
		rng = rand.New(rand.NewSource(1))
		for off := 0; off < written && verified; off += sdProbeChunk {
			rng.Read(chunk)
			n := sdProbeChunk
			if off+n > written {
				n = written - off
			}
			if !bytes.Equal(back[off:off+n], chunk[:n]) {
				verified = false
			}
		}
	}
	mbps = float64(written) / (1024 * 1024) / elapsed.Seconds()
	return mbps, verified, nil
}

// sdHealthPaths lists the places worth checking, most important first: the app
// directory (where updates land) and the card root.
func sdHealthPaths() []string {
	var out []string
	if dir, err := P().ExecutableDir(); err == nil && dir != "" {
		out = append(out, dir)
	}
	if dir, err := PatchStateDir(); err == nil && dir != "" {
		out = append(out, dir)
	}
	return out
}

// checkSDHealth measures one directory and builds the report.
func checkSDHealth(dir string) sdHealthReport {
	report := sdHealthReport{FSType: fsTypeFor(readMounts(), dir), Checked: time.Now()}
	total, free, err := diskUsage(dir)
	if err == nil {
		report.Total, report.Free = total, free
	} else {
		report.Free = -1
	}
	if mbps, verified, err := sdWriteProbe(dir); err == nil {
		report.WriteMBps, report.Verified, report.Probed = mbps, verified, true
	} else {
		Log().Warn("sd health: write probe failed", "err", err)
	}
	report.Findings = sdFindings(report.FSType, report.Total, report.Free, report.WriteMBps, report.Probed, report.Verified)
	return report
}

var sdHealthCache struct {
	mu     sync.Mutex
	report *sdHealthReport
	at     time.Time
}

// sdHealthCached returns a report at most sdHealthTTL old, measuring on first
// use. A failed probe is not cached, so a transient error does not stick.
func sdHealthCached() sdHealthReport {
	sdHealthCache.mu.Lock()
	defer sdHealthCache.mu.Unlock()
	if sdHealthCache.report != nil && time.Since(sdHealthCache.at) < sdHealthTTL {
		return *sdHealthCache.report
	}
	paths := sdHealthPaths()
	if len(paths) == 0 {
		return sdHealthReport{Free: -1}
	}
	report := checkSDHealth(paths[0])
	if report.Probed || report.Total > 0 {
		sdHealthCache.report = &report
		sdHealthCache.at = time.Now()
	}
	return report
}

// invalidateSDHealth forces the next Diagnostics visit to measure again.
func invalidateSDHealth() {
	sdHealthCache.mu.Lock()
	sdHealthCache.report = nil
	sdHealthCache.mu.Unlock()
}

// sdHealthLine is the compact Diagnostics form: no paths, no serial numbers.
func sdHealthLine(r sdHealthReport) string {
	parts := []string{}
	if r.FSType != "" {
		parts = append(parts, r.FSType)
	}
	if r.Total > 0 {
		parts = append(parts, fmt.Sprintf("%.1f GiB total", float64(r.Total)/(1<<30)))
	}
	if r.Free >= 0 {
		parts = append(parts, fmt.Sprintf("%.1f GiB free", float64(r.Free)/(1<<30)))
	}
	if len(parts) == 0 {
		parts = append(parts, "unknown")
	}
	return strings.Join(parts, ", ")
}

// sortedFindings is used by the UI and the export so both list warnings the
// same way.
func sortedFindings(findings []string) []string {
	out := append([]string(nil), findings...)
	sort.Strings(out)
	return out
}
