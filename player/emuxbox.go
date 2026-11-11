package main

// emuxbox.go — the original Xbox: xemu on Windows, and the setup sheet the
// Xbox tile opens on both platforms.
//
// On Windows JukaHub installs xemu from its GitHub releases like every other
// emulator (emuwininstall.go), the game library lists Roms/XBOX (.iso/.xiso)
// and boots a game with xemu's documented "-dvd_path <iso>". What xemu cannot
// ship is the Xbox's own firmware, so the xemu tile opens a setup sheet (Y):
//
//   - MCPX boot ROM (mcpx_1.0.bin, 512 bytes; its MD5 tells a good dump from
//     the known-bad one) and a flash ROM / BIOS (Complex_4627.bin and friends)
//     are user-supplied: the sheet says what they are, where to put them, and
//     which ones it found.
//   - The hard-disk image has a copyright-free version published by the xemu
//     project (xemu-dashboard's xbox_hdd.qcow2); the sheet downloads it.
//   - The EEPROM is optional: xemu creates one.
//   - "Apply to xemu" writes the found paths into xemu.toml's [sys.files], the
//     settings xemu itself reads, so nothing has to be browsed to by hand.
//     Starting xemu applies them too, and a game started without them stops
//     with what to add and where (emuStandaloneLaunchCheck) instead of opening
//     an xemu window that cannot boot it.
//
// The Xbox 360 has no setup sheet: Xenia Canary needs no console files, only
// the Visual C++ runtime its tile and install toast name when it is missing.
//
// On the TrimUI Smart Pro the Xbox runs through the experimental hakuX port
// instead (hakux.go installs it, hakuxpage.go builds its checklist), and the
// same sheet lists the package, the Roms/XBOX games and whatever firmware the
// installed build's own settings name — current hakuX boots games on its own
// Xbox kernel and normally asks for none — with the in-game controls under
// them. Its buttons install or update hakuX and open the Xbox games. The BIOS
// doctor prints the same checklist on both platforms.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	emuXboxMCPXSize    = 512
	emuXboxMCPXGoodMD5 = "d49c52a4102f6df7bcf8d0617ac475ed"
	emuXboxMCPXBadMD5  = "196a5f59a13382c185636e691d6c323d"
	emuXboxEEPROMSize  = 256
	// emuXboxHDDRepo publishes the copyright-free HDD image the xemu docs
	// recommend (the older xemu-hdd-image repository is archived).
	emuXboxHDDRepo     = "xemu-project/xemu-dashboard"
	emuXboxHDDName     = "xbox_hdd.qcow2"
	emuXboxHDDMaxBytes = 64 << 20
)

// emuXboxFile is one item of the setup checklist.
type emuXboxFile struct {
	// Key is the xemu.toml name without "_path": bootrom, flashrom, hdd, eeprom.
	Key      string
	Label    string
	Want     string
	Path     string
	Note     string
	Problem  string
	Required bool
}

// ok reports a file that is there and usable.
func (f emuXboxFile) ok() bool { return f.Path != "" && f.Problem == "" }

// emuXboxReport is the checklist's state.
type emuXboxReport struct {
	Files []emuXboxFile
	// Folder is where the sheet asks for the files; ConfigPath is the
	// emulator's settings file (xemu.toml, or the hakuX config that names
	// firmware); Configured says it already names working files.
	Folder     string
	ConfigPath string
	Configured bool
	XemuExe    string
	// Emulator names what the checklist is for: "xemu" on Windows, "hakuX" on
	// the handheld.
	Emulator string
	// Installed reports the emulator is on this machine; Supported is false on
	// a handheld the hakuX port is not offered for.
	Installed bool
	Supported bool
	// Notes are printed under the checklist: on the handheld, the in-game
	// controls and what to expect from an experimental port.
	Notes []string
}

// ready reports that every required file is there.
func (r emuXboxReport) ready() bool {
	for _, f := range r.Files {
		if f.Required && !f.ok() {
			return false
		}
	}
	return len(r.Files) > 0
}

// missing lists the required items still to supply.
func (r emuXboxReport) missing() []string {
	var out []string
	for _, f := range r.Files {
		if f.Required && !f.ok() {
			out = append(out, f.Label)
		}
	}
	return out
}

// emuIsXemuLauncher reports whether a launcher path is xemu's executable,
// whichever separator the path uses.
func emuIsXemuLauncher(launcher string) bool {
	base := launcher
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.ToLower(strings.TrimSpace(base))
	return base == "xemu.exe" || base == "xemu"
}

// emuXboxFolder is where the sheet asks for the Xbox files: BIOS\xbox next to
// JukaHub when that is writable, else the data folder's BIOS\xbox. Both are
// inside the BIOS doctor's roots.
func emuXboxFolder() string {
	if exe, err := os.Executable(); err == nil {
		base := filepath.Dir(exe)
		dir := filepath.Join(base, "BIOS", "xbox")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
		// Probing the JukaHub folder itself leaves nothing behind; the BIOS
		// folder is only created when something is put in it.
		if emuDirWritable(base) {
			return dir
		}
	}
	if data, err := P().DataDir(); err == nil && data != "" {
		return filepath.Join(data, "BIOS", "xbox")
	}
	return filepath.Join("BIOS", "xbox")
}

// emuXemuConfigPaths lists the xemu.toml files xemu may read: the per-user one
// under %APPDATA%\xemu\xemu, and a portable one beside xemu.exe when present.
func emuXemuConfigPaths(xemuExe string) []string {
	var out []string
	if xemuExe != "" {
		if portable := filepath.Join(filepath.Dir(xemuExe), "xemu.toml"); regularFile(portable) {
			out = append(out, portable)
		}
	}
	if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
		out = append(out, filepath.Join(appData, "xemu", "xemu", "xemu.toml"))
	}
	return out
}

// emuXemuTomlFiles reads the [sys.files] paths out of an xemu.toml.
func emuXemuTomlFiles(text string) map[string]string {
	out := map[string]string{}
	inFiles := false
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") {
			inFiles = strings.EqualFold(strings.ReplaceAll(line, " ", ""), "[sys.files]")
			continue
		}
		if !inFiles || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		out[key] = emuTomlUnquote(strings.TrimSpace(line[eq+1:]))
	}
	return out
}

// emuTomlUnquote reads a TOML string value: a 'literal', a "basic" string
// (backslash escapes), or a bare word up to a comment.
func emuTomlUnquote(v string) string {
	switch {
	case strings.HasPrefix(v, "'"):
		if end := strings.Index(v[1:], "'"); end >= 0 {
			return v[1 : 1+end]
		}
		return strings.TrimPrefix(v, "'")
	case strings.HasPrefix(v, `"`):
		var b strings.Builder
		for i := 1; i < len(v); i++ {
			c := v[i]
			if c == '"' {
				break
			}
			if c == '\\' && i+1 < len(v) {
				i++
				switch v[i] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				default:
					b.WriteByte(v[i])
				}
				continue
			}
			b.WriteByte(c)
		}
		return b.String()
	}
	if i := strings.IndexByte(v, '#'); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// emuTomlString writes a path as a TOML string: a literal string (no escaping,
// so Windows backslashes stay as they are) unless the path contains a quote.
func emuTomlString(s string) string {
	if !strings.ContainsAny(s, "'\r\n") {
		return "'" + s + "'"
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", "").Replace(s)
	return `"` + escaped + `"`
}

// emuXemuKeys is the order the [sys.files] keys are written in.
var emuXemuKeys = []string{"bootrom_path", "flashrom_path", "eeprom_path", "hdd_path"}

// emuXemuTomlSetFiles returns text with [sys.files] naming files. Keys already
// in the section are replaced in place, missing ones are added at its end, and
// a config without the section gets one appended; every other line is kept.
func emuXemuTomlSetFiles(text string, files map[string]string) string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	start, end := -1, len(lines)
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if start < 0 {
			if strings.EqualFold(strings.ReplaceAll(line, " ", ""), "[sys.files]") {
				start = i
			}
			continue
		}
		if strings.HasPrefix(line, "[") {
			end = i
			break
		}
	}
	if start < 0 {
		out := strings.TrimRight(normalized, "\n")
		if out != "" {
			out += "\n\n"
		}
		out += "[sys.files]\n"
		for _, key := range emuXemuKeys {
			if v := strings.TrimSpace(files[key]); v != "" {
				out += key + " = " + emuTomlString(v) + "\n"
			}
		}
		return out
	}
	done := map[string]bool{}
	for i := start + 1; i < end; i++ {
		line := strings.TrimSpace(lines[i])
		eq := strings.IndexByte(line, '=')
		if eq <= 0 || strings.HasPrefix(line, "#") {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if v := strings.TrimSpace(files[key]); v != "" {
			lines[i] = key + " = " + emuTomlString(v)
			done[key] = true
		}
	}
	var extra []string
	for _, key := range emuXemuKeys {
		if v := strings.TrimSpace(files[key]); v != "" && !done[key] {
			extra = append(extra, key+" = "+emuTomlString(v))
		}
	}
	insertAt := end
	for insertAt > start+1 && strings.TrimSpace(lines[insertAt-1]) == "" {
		insertAt--
	}
	merged := make([]string, 0, len(lines)+len(extra))
	merged = append(merged, lines[:insertAt]...)
	merged = append(merged, extra...)
	merged = append(merged, lines[insertAt:]...)
	return strings.Join(merged, "\n")
}

// emuXboxSearchDirs lists the folders the checklist looks in: the BIOS
// doctor's roots and their xbox subfolders, the sheet's own folder, xemu's
// folder and xemu's settings folder.
func emuXboxSearchDirs(xemuExe string) []string {
	var dirs []string
	add := func(dir string) {
		dir = filepath.Clean(strings.TrimSpace(dir))
		if dir == "" || dir == "." {
			return
		}
		for _, have := range dirs {
			if strings.EqualFold(have, dir) {
				return
			}
		}
		dirs = append(dirs, dir)
	}
	add(emuXboxFolder())
	for _, root := range biosDoctorRoots() {
		add(filepath.Join(root, "xbox"))
		add(filepath.Join(root, "Xbox"))
		add(root)
	}
	if xemuExe != "" {
		add(filepath.Dir(xemuExe))
		add(filepath.Join(filepath.Dir(xemuExe), "bios"))
	}
	if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
		add(filepath.Join(appData, "xemu", "xemu"))
	}
	return dirs
}

// emuXboxFlashNames are the name fragments a flash ROM dump is known by. A
// size match alone is not enough: a PS1 BIOS is also 512 KB.
var emuXboxFlashNames = []string{"complex", "4627", "5838", "5713", "5530", "4972", "4817", "4034", "3944", "xbox", "evox", "x2_", "m8plus"}

// emuXboxClassify says which checklist item a file could be, by name, size and
// (for the boot ROM) its first and last bytes.
func emuXboxClassify(path string, size int64) string {
	lower := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasSuffix(lower, ".qcow2"):
		return "hdd"
	case strings.Contains(lower, "eeprom") && size == emuXboxEEPROMSize:
		return "eeprom"
	case !strings.HasSuffix(lower, ".bin"):
		return ""
	case size == emuXboxMCPXSize:
		if strings.HasPrefix(lower, "mcpx") {
			return "bootrom"
		}
		if data, err := os.ReadFile(path); err == nil && len(data) == emuXboxMCPXSize &&
			data[0] == 0x33 && data[1] == 0xC0 && data[len(data)-2] == 0x02 && data[len(data)-1] == 0xEE {
			return "bootrom"
		}
	case size == 256<<10 || size == 512<<10 || size == 1<<20:
		for _, frag := range emuXboxFlashNames {
			if strings.Contains(lower, frag) {
				return "flashrom"
			}
		}
	}
	return ""
}

// emuXboxCheckFile fills in a found file's notes: the boot ROM's MD5 decides
// between a good dump, the known-bad one and an unverified one.
func emuXboxCheckFile(f *emuXboxFile) {
	if f.Path == "" {
		return
	}
	info, err := os.Stat(f.Path)
	if err != nil || !info.Mode().IsRegular() {
		f.Problem = "listed but not found"
		return
	}
	if f.Key != "bootrom" {
		return
	}
	data, err := os.ReadFile(f.Path)
	if err != nil {
		f.Problem = "unreadable"
		return
	}
	if len(data) != emuXboxMCPXSize {
		f.Problem = fmt.Sprintf("%d bytes, not 512 — not an MCPX dump", len(data))
		return
	}
	sum := md5.Sum(data)
	switch hex.EncodeToString(sum[:]) {
	case emuXboxMCPXGoodMD5:
		f.Note = "good dump"
	case emuXboxMCPXBadMD5:
		f.Problem = "known bad dump — dump it again"
	default:
		f.Note = "unverified dump"
	}
}

// emuXboxScan builds the checklist: on Windows the paths xemu.toml already
// names first, then whatever the search folders hold; on the handheld hakuX's
// (hakuxSheetReport).
func emuXboxScan() emuXboxReport {
	if !IsWindows() {
		return hakuxSheetReport()
	}
	return emuXemuScan(emuWinExe("xemu"))
}

// emuXemuScan is the Windows checklist for one xemu: the one the catalog finds,
// or the launcher a game is about to start, whose portable xemu.toml is the one
// that counts.
func emuXemuScan(xemuExe string) emuXboxReport {
	report := emuXboxReport{Folder: emuXboxFolder(), XemuExe: xemuExe, Emulator: "xemu", Installed: xemuExe != "", Supported: true}
	report.Files = []emuXboxFile{
		{Key: "bootrom", Label: "MCPX boot ROM", Want: "mcpx_1.0.bin · 512 bytes", Required: true},
		{Key: "flashrom", Label: "Flash ROM (BIOS)", Want: "e.g. Complex_4627.bin · 256 KB or 1 MB", Required: true},
		{Key: "hdd", Label: "Hard disk image", Want: "xbox_hdd.qcow2 · X downloads a free one", Required: true},
		{Key: "eeprom", Label: "EEPROM (optional)", Want: "eeprom.bin · xemu creates one if missing"},
	}
	index := map[string]int{}
	for i, f := range report.Files {
		index[f.Key] = i
	}

	configs := emuXemuConfigPaths(xemuExe)
	if len(configs) > 0 {
		report.ConfigPath = configs[0]
	}
	configured := map[string]string{}
	for _, cfg := range configs {
		raw, err := os.ReadFile(cfg)
		if err != nil {
			continue
		}
		report.ConfigPath = cfg
		for key, value := range emuXemuTomlFiles(string(raw)) {
			k := strings.TrimSuffix(key, "_path")
			if _, ok := index[k]; ok && strings.TrimSpace(value) != "" {
				configured[k] = value
			}
		}
		break
	}
	for key, value := range configured {
		f := &report.Files[index[key]]
		if regularFile(value) {
			f.Path = value
			f.Note = "set in xemu"
		}
	}

	for _, dir := range emuXboxSearchDirs(xemuExe) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			key := emuXboxClassify(path, info.Size())
			i, ok := index[key]
			if !ok || report.Files[i].Path != "" {
				continue
			}
			report.Files[i].Path = path
		}
	}
	allSet := true
	for i := range report.Files {
		emuXboxCheckFile(&report.Files[i])
		f := report.Files[i]
		if f.Required && (!f.ok() || !strings.EqualFold(configured[f.Key], f.Path)) {
			allSet = false
		}
	}
	report.Configured = allSet && len(configured) > 0
	return report
}

// emuXboxApply writes the found files into every xemu.toml xemu may read,
// creating the per-user one when there is none, and returns where it wrote.
func emuXboxApply(report emuXboxReport) (string, error) {
	if !report.ready() {
		return "", fmt.Errorf("still missing: %s", strings.Join(report.missing(), ", "))
	}
	files := map[string]string{}
	for _, f := range report.Files {
		if f.ok() {
			files[f.Key+"_path"] = f.Path
		}
	}
	targets := emuXemuConfigPaths(report.XemuExe)
	if len(targets) == 0 {
		return "", errors.New("xemu's settings folder is unknown (no APPDATA)")
	}
	written := ""
	for _, target := range targets {
		old, err := os.ReadFile(target)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return written, err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return written, err
		}
		if err := AtomicWrite(target, []byte(emuXemuTomlSetFiles(string(old), files)), 0o644); err != nil {
			return written, err
		}
		if written == "" {
			written = target
		}
	}
	return written, nil
}

// emuXboxFetchHDD downloads the copyright-free HDD image into folder unless
// one is already there: an existing image holds the user's saves and is never
// replaced.
func emuXboxFetchHDD(ctx context.Context, client *http.Client, folder string, progress func(done, total int64)) (string, error) {
	dest := filepath.Join(folder, emuXboxHDDName)
	if regularFile(dest) {
		return dest, nil
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return "", err
	}
	data, err := fetchUpdateBytes(ctx, client, fmt.Sprintf("%s/repos/%s/releases/latest", standaloneAPIBase, emuXboxHDDRepo), patchMaxMetadata)
	if err != nil {
		return "", err
	}
	var rel emuGitHubRelease
	if err := json.Unmarshal(data, &rel); err != nil {
		return "", errors.New("unreadable HDD image release metadata")
	}
	var pick emuGitHubAsset
	for _, a := range rel.Assets {
		lower := strings.ToLower(a.Name)
		switch {
		case lower == emuXboxHDDName:
			pick = a
		case pick.Name == "" && (strings.HasSuffix(lower, ".qcow2") || strings.HasSuffix(lower, ".qcow2.zip")):
			pick = a
		}
	}
	if pick.Name == "" {
		return "", fmt.Errorf("the %s release has no HDD image", emuXboxHDDRepo)
	}
	if !strings.HasSuffix(strings.ToLower(pick.Name), ".zip") {
		sum, err := emuDownload(ctx, client, pick.BrowserDownloadURL, dest, emuXboxHDDMaxBytes, progress)
		if err != nil {
			return "", err
		}
		if err := emuDigestMatches(sum, pick.Digest); err != nil {
			_ = os.Remove(dest)
			return "", err
		}
		return dest, nil
	}
	archive := dest + ".zip"
	sum, err := emuDownload(ctx, client, pick.BrowserDownloadURL, archive, emuXboxHDDMaxBytes, progress)
	if err != nil {
		return "", err
	}
	defer os.Remove(archive)
	if err := emuDigestMatches(sum, pick.Digest); err != nil {
		return "", err
	}
	if err := emuExtractQcow2(archive, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// emuExtractQcow2 copies the first .qcow2 out of a zip to dest.
func emuExtractQcow2(archive, dest string) error {
	raw, err := os.ReadFile(archive)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return fmt.Errorf("%w (%v)", errBadToolArchive, err)
	}
	for _, f := range zr.File {
		if !f.Mode().IsRegular() || !strings.HasSuffix(strings.ToLower(f.Name), ".qcow2") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(dest + ".part")
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, io.LimitReader(rc, 4<<30))
		rc.Close()
		closeErr := out.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			_ = os.Remove(dest + ".part")
			return copyErr
		}
		return os.Rename(dest+".part", dest)
	}
	return fmt.Errorf("%w (no .qcow2 inside)", errBadToolArchive)
}

// emuXboxAfterInstall is what a fresh xemu install adds on its own: the free
// HDD image, when none is there yet. Failing it never fails the install.
func emuXboxAfterInstall(ctx context.Context, client *http.Client) {
	installBarDetail("Fetching the free Xbox HDD image")
	if _, err := emuXboxFetchHDD(ctx, client, emuXboxFolder(), func(done, total int64) {
		installBarProgress(done, total)
	}); err != nil {
		logPatch("xemu: HDD image not fetched: %v", err)
	}
}

// ── Starting xemu and Xenia ─────────────────────────────────────────────────

// emuXemuMissingMessage says what the checklist still lacks and how to get it,
// with subject ("xemu", "it") as the sentence's subject; "" when nothing is
// missing. The firmware is the user's own dump; the hard-disk image is the
// free one the sheet downloads.
func emuXemuMissingMessage(report emuXboxReport, subject string) string {
	var firmware []string
	hdd := false
	for _, f := range report.Files {
		if !f.Required || f.ok() {
			continue
		}
		if f.Key == "hdd" {
			hdd = true
			continue
		}
		item := f.Label
		if f.Problem != "" {
			item += " (" + f.Problem + ")"
		}
		firmware = append(firmware, item)
	}
	folder := report.Folder
	if folder == "" {
		folder = "the BIOS\\xbox folder"
	}
	switch {
	case len(firmware) > 0 && hdd:
		return subject + " needs " + strings.Join(firmware, " and ") + " — put your own dumps in " + folder +
			" — and a hard-disk image (Emulators → xemu → Y, then X gets a free one)"
	case len(firmware) > 0:
		return subject + " needs " + strings.Join(firmware, " and ") + " — put your own dumps in " + folder +
			" (Emulators → xemu → Y lists them)"
	case hdd:
		return subject + " needs a hard-disk image — Emulators → xemu → Y, then X downloads a free one"
	}
	return ""
}

// emuXemuApplyFound writes the files the checklist found into xemu.toml when
// xemu does not name them yet, so a user who only dropped them into the folder
// never has to open the sheet. ready is false while a required file is still
// missing; nothing is written then.
func emuXemuApplyFound(report emuXboxReport) (ready bool, err error) {
	if !report.ready() {
		return false, nil
	}
	if report.Configured {
		return true, nil
	}
	path, err := emuXboxApply(report)
	if err != nil {
		return true, err
	}
	logInfo("emulators", "xemu: wrote the Xbox files the checklist found into %s", path)
	return true, nil
}

// emuStandaloneLaunchCheck is asked before the Games Library starts a game in
// a standalone Windows emulator. For xemu it is the difference between a game
// and an empty window: without the MCPX boot ROM, a flash ROM and a hard-disk
// image xemu boots no disc and says why only inside its own window. So the
// files the checklist found are applied first, and a missing one stops the
// launch with what to add and where. nil for every other launcher, and off
// Windows.
func emuStandaloneLaunchCheck(launcher string) error {
	if !IsWindows() || !emuIsXemuLauncher(launcher) {
		return nil
	}
	report := emuXemuScan(launcher)
	ready, err := emuXemuApplyFound(report)
	if !ready {
		return errors.New(emuXemuMissingMessage(report, "xemu"))
	}
	if err != nil {
		return fmt.Errorf("could not give xemu the Xbox files: %v", err)
	}
	return nil
}

// emuXemuPrepareLaunch is emuStandaloneLaunchCheck for xemu started from its
// own tile, where it never stops the start — xemu's own menus are a way to
// set it up too. It returns what is still missing, for the launch toast.
func emuXemuPrepareLaunch(xemuExe string) string {
	report := emuXemuScan(xemuExe)
	ready, err := emuXemuApplyFound(report)
	switch {
	case !ready:
		return emuXemuMissingMessage(report, "it")
	case err != nil:
		logWarn("emulators", "xemu: could not apply the Xbox files: %v", err)
		return "the Xbox files could not be written into xemu.toml (" + firstLine(err.Error()) + ")"
	}
	return ""
}

// emuWinSetupNote starts a Windows emulator tile's detail line while something
// outside the emulator still stands between it and a game: xemu's Xbox files,
// Xenia's Visual C++ runtime. "" when nothing does, and off Windows.
func emuWinSetupNote(name, exe string) string {
	if !IsWindows() {
		return ""
	}
	switch {
	case strings.EqualFold(name, "xemu"):
		report := emuXemuScan(exe)
		switch {
		case report.ready() && report.Configured:
			return ""
		case report.ready():
			return "Xbox files found · Y applies them"
		}
		return "Xbox files missing · Y sets them up"
	case strings.EqualFold(name, emuXeniaName) && !emuWinHasVCRuntime():
		return "needs the Visual C++ Redistributable (x64)"
	}
	return ""
}

// emuIsXeniaLauncher reports whether a launcher path is Xenia's executable:
// Canary's xenia_canary.exe, or upstream's xenia.exe.
func emuIsXeniaLauncher(launcher string) bool {
	base := launcher
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.ToLower(strings.TrimSpace(base))
	return base == "xenia_canary.exe" || base == "xenia.exe"
}

// emuStandaloneGameArgs is the documented command line that boots a game full
// screen in xemu ("-full-screen -dvd_path <iso>", xemu.app/docs/cli) and in
// Xenia ("<game> --fullscreen=true": the game's path comes before the
// options). ok is false for every other launcher.
func emuStandaloneGameArgs(launcher, romPath string) ([]string, bool) {
	switch {
	case emuIsXemuLauncher(launcher):
		return []string{"-full-screen", "-dvd_path", romPath}, true
	case emuIsXeniaLauncher(launcher):
		return []string{romPath, "--fullscreen=true"}, true
	}
	return nil, false
}

// ── Setup sheet ─────────────────────────────────────────────────────────────

// emuXboxSheet is the setup checklist drawn over the Emulators page.
var emuXboxSheet struct {
	mu       sync.Mutex
	open     bool
	scanning bool
	// again asks for one more scan when the running one ends: a rescan
	// requested mid-scan (an install that just finished) must not be lost.
	again   bool
	report  emuXboxReport
	scanned bool
	// scannedAt is when the last scan finished: the open sheet looks again
	// every emuXboxSheetRescanEvery, so files dropped into the folder while
	// it is up turn green on their own.
	scannedAt time.Time
	focus     int // index into emuXboxSheetButtons
	buttons   []emuXboxButtonHit
	rect      sdl.Rect
}

// emuXboxSheetRescanEvery is how often the open sheet checks the folders again.
const emuXboxSheetRescanEvery = 4 * time.Second

// emuXboxHDDTool names the HDD image download on the install card.
const emuXboxHDDTool = "Xbox HDD image"

type emuXboxButtonHit struct {
	index int
	rect  sdl.Rect
}

// emuXboxAction is what one of the sheet's buttons does: Apply, HDD and Folder
// are xemu's (Windows), Games is hakuX's (the handheld), and Install installs
// whichever of the two the device uses.
type emuXboxAction int

const (
	emuXboxActBack emuXboxAction = iota
	emuXboxActApply
	emuXboxActHDD
	emuXboxActFolder
	emuXboxActInstall
	emuXboxActGames
)

// emuXboxButton is one of the sheet's buttons. Key is the pad button that
// presses it from anywhere on the sheet ("" when only A on the focus does).
type emuXboxButton struct {
	Label    string
	Action   emuXboxAction
	Key      string
	Disabled bool
}

// emuXboxSheetButtons are the sheet's actions for a report, in focus order. A
// disabled button still explains itself when pressed.
func emuXboxSheetButtons(report emuXboxReport, scanned bool) []emuXboxButton {
	if !IsWindows() {
		install := "Install hakuX"
		if report.Installed {
			install = "Update hakuX"
		}
		return []emuXboxButton{
			{Label: install, Action: emuXboxActInstall, Key: "X", Disabled: !report.Supported},
			{Label: "Xbox games", Action: emuXboxActGames, Key: "Y", Disabled: !report.Installed},
			{Label: "Back", Action: emuXboxActBack},
		}
	}
	if scanned && !report.Installed {
		// xemu itself first: its install also fetches the free HDD image, and
		// the firmware can go into the folder meanwhile.
		return []emuXboxButton{
			{Label: "Install xemu", Action: emuXboxActInstall, Key: "X"},
			{Label: "Open folder", Action: emuXboxActFolder, Key: "Y"},
			{Label: "Back", Action: emuXboxActBack},
		}
	}
	hddFound := false
	for _, f := range report.Files {
		if f.Key == "hdd" && f.ok() {
			hddFound = true
		}
	}
	return []emuXboxButton{
		{Label: "Apply to xemu", Action: emuXboxActApply, Disabled: !scanned || !report.ready()},
		{Label: "Get HDD image", Action: emuXboxActHDD, Key: "X", Disabled: hddFound},
		{Label: "Open folder", Action: emuXboxActFolder, Key: "Y"},
		{Label: "Back", Action: emuXboxActBack},
	}
}

// emuXboxCurrentButtons is the button row for the sheet's latest report.
func emuXboxCurrentButtons() []emuXboxButton {
	report, scanned, _ := emuXboxSnapshot()
	return emuXboxSheetButtons(report, scanned)
}

// emuXboxSetupAvailable reports whether a tile offers the Xbox setup (Y):
// xemu's tile on Windows, and the Xbox tile on a handheld hakuX is offered on.
func emuXboxSetupAvailable(e emuEntry) bool {
	if IsWindows() {
		return strings.EqualFold(strings.TrimSpace(e.Name), "xemu")
	}
	return hakuxTile(e) && e.Status != emuUnsupported && hakuxOffered()
}

func emuXboxSheetOpen() bool {
	emuXboxSheet.mu.Lock()
	defer emuXboxSheet.mu.Unlock()
	return emuXboxSheet.open
}

// emuOpenXboxSetup shows the sheet and checks the files in the background. On
// the handheld the focus starts on Back: A opens this sheet from a tile where
// hakuX is not installed yet, and a second A must not start a download nobody
// has read about. X (named in the subtitle) or a move to the button installs.
func emuOpenXboxSetup() {
	focus := 0
	if !IsWindows() {
		focus = len(emuXboxSheetButtons(emuXboxReport{}, false)) - 1
	}
	emuXboxSheet.mu.Lock()
	emuXboxSheet.open = true
	emuXboxSheet.focus = focus
	emuXboxSheet.buttons = nil
	emuXboxSheet.mu.Unlock()
	PlayActivateSound()
	emuXboxRescan()
}

func emuCloseXboxSetup() {
	emuXboxSheet.mu.Lock()
	wasOpen := emuXboxSheet.open
	emuXboxSheet.open = false
	emuXboxSheet.buttons = nil
	emuXboxSheet.rect = sdl.Rect{}
	emuXboxSheet.mu.Unlock()
	if wasOpen {
		PlayBackSound()
	}
}

// emuXboxRescan re-reads the checklist off the event thread. A request that
// arrives while a scan is running is kept, and runs as soon as that one ends.
func emuXboxRescan() {
	emuXboxSheet.mu.Lock()
	if emuXboxSheet.scanning {
		emuXboxSheet.again = true
		emuXboxSheet.mu.Unlock()
		return
	}
	emuXboxSheet.scanning = true
	emuXboxSheet.mu.Unlock()
	go func() {
		for {
			report := emuXboxScan()
			emuXboxSheet.mu.Lock()
			emuXboxSheet.report = report
			emuXboxSheet.scanned = true
			emuXboxSheet.scannedAt = time.Now()
			if !emuXboxSheet.again {
				emuXboxSheet.scanning = false
				emuXboxSheet.mu.Unlock()
				return
			}
			emuXboxSheet.again = false
			emuXboxSheet.mu.Unlock()
		}
	}()
}

// emuXboxRescanIfStale starts a rescan when the open sheet's last one finished
// more than emuXboxSheetRescanEvery ago and none is running. The sheet asks
// every frame; the scan itself runs off the event thread.
func emuXboxRescanIfStale() {
	emuXboxSheet.mu.Lock()
	stale := emuXboxSheet.open && emuXboxSheet.scanned && !emuXboxSheet.scanning &&
		time.Since(emuXboxSheet.scannedAt) > emuXboxSheetRescanEvery
	emuXboxSheet.mu.Unlock()
	if stale {
		emuXboxRescan()
	}
}

// emuXboxInstallLine is the sheet's progress line while an Xbox download it
// started runs (hakuX, xemu or the HDD image): the install card's phase and
// percentage, so the sheet itself shows the work its X began. "" otherwise.
func emuXboxInstallLine() string {
	visible, tool, phase, _, done, total, _, _, ending := installBarSnapshot()
	if !visible || ending {
		return ""
	}
	switch tool {
	case hakuxName, "xemu", emuXboxHDDTool:
	default:
		return ""
	}
	line := tool + " · " + phase
	if total > 0 && done > 0 {
		line += fmt.Sprintf(" %d%%", done*100/total)
	}
	return line
}

// emuXboxSnapshot is the sheet's report for drawing and actions.
func emuXboxSnapshot() (emuXboxReport, bool, bool) {
	emuXboxSheet.mu.Lock()
	defer emuXboxSheet.mu.Unlock()
	return emuXboxSheet.report, emuXboxSheet.scanned, emuXboxSheet.scanning
}

// emuXboxPress presses the button at index in the current row.
func emuXboxPress(index int) {
	buttons := emuXboxCurrentButtons()
	if index < 0 || index >= len(buttons) {
		return
	}
	emuXboxPressAction(buttons[index].Action)
}

// emuXboxPressKey presses the button a pad key names ("X", "Y"), when the
// current row has one.
func emuXboxPressKey(key string) {
	for _, b := range emuXboxCurrentButtons() {
		if b.Key == key {
			emuXboxPressAction(b.Action)
			return
		}
	}
}

// emuXboxPressAction runs one of the sheet's actions.
func emuXboxPressAction(action emuXboxAction) {
	report, scanned, _ := emuXboxSnapshot()
	switch action {
	case emuXboxActApply:
		if !scanned {
			ShowToast("Still checking the Xbox files…", ToastKindInfo)
			return
		}
		path, err := emuXboxApply(report)
		if err != nil {
			PlayErrorSound()
			ShowToast("Xbox setup: "+err.Error(), ToastKindWarning)
			return
		}
		PlaySuccessSound()
		ShowToast("xemu now uses these files · "+path, ToastKindSuccess)
		emuXboxRescan()
	case emuXboxActHDD:
		emuXboxStartHDDDownload()
	case emuXboxActFolder:
		folder := emuXboxFolder()
		if err := os.MkdirAll(folder, 0o755); err != nil {
			ShowToast("Could not create "+folder, ToastKindError)
			return
		}
		if err := P().OpenFile(folder); err != nil {
			ShowToast("Could not open "+folder, ToastKindError)
			return
		}
		PlayActivateSound()
		ShowToast("Put the Xbox files in "+folder, ToastKindInfo)
	case emuXboxActInstall:
		if IsWindows() {
			if spec, ok := emuWinSpecFor("xemu"); ok {
				emuWinStartInstall(spec)
			}
			return
		}
		hakuxStartInstall()
	case emuXboxActGames:
		if !report.Installed {
			ShowToast("Install hakuX first — X on this sheet", ToastKindInfo)
			return
		}
		emuCloseXboxSetup()
		hakuxOpenGames()
	default:
		emuCloseXboxSetup()
	}
}

// emuXboxStartHDDDownload fetches the free HDD image in the background.
func emuXboxStartHDDDownload() {
	report, _, _ := emuXboxSnapshot()
	for _, f := range report.Files {
		if f.Key == "hdd" && f.ok() {
			ShowToast("An Xbox HDD image is already there · "+f.Path, ToastKindInfo)
			return
		}
	}
	if !IsOnline() {
		ShowToast("No internet connection — connect to fetch the HDD image", ToastKindWarning)
		return
	}
	if !tryBeginPatchOp() {
		ShowToast("An update is already running", ToastKindInfo)
		return
	}
	installBarBegin(emuXboxHDDTool)
	installBarPhase(installPhaseResolving)
	go func() {
		defer endPatchOp()
		ctx, cancel := context.WithTimeout(installBarRunContext(), 10*time.Minute)
		defer cancel()
		installBarPhase(installPhaseDownload)
		path, err := emuXboxFetchHDD(ctx, emuWinClient(), emuXboxFolder(), func(done, total int64) {
			installBarProgress(done, total)
		})
		if err != nil {
			installBarFail(installCardDetail(emuFriendlyError(err)))
			ShowToast("HDD image: "+emuFriendlyError(err), ToastKindError)
			return
		}
		installBarPhase(installPhaseDone)
		installBarDone()
		PlaySuccessSound()
		ShowToast("Xbox HDD image ready · "+path, ToastKindSuccess)
		emuXboxRescan()
	}()
}

func emuXboxMoveFocus(delta int) {
	count := len(emuXboxCurrentButtons())
	emuXboxSheet.mu.Lock()
	next := emuXboxSheet.focus + delta
	if next >= count {
		next = count - 1
	}
	if next < 0 {
		next = 0
	}
	changed := next != emuXboxSheet.focus
	emuXboxSheet.focus = next
	emuXboxSheet.mu.Unlock()
	if changed {
		PlayNavSound()
	}
}

func emuXboxFocused() int {
	emuXboxSheet.mu.Lock()
	defer emuXboxSheet.mu.Unlock()
	return emuXboxSheet.focus
}

// ctrlEmuXboxSheet is the pad on the sheet: Left/Right walk the buttons, A
// presses the focused one, X and Y press the buttons they are printed on
// (xemu: install it or fetch the HDD image, and the folder; hakuX: install or
// update, and the Xbox games), B closes the sheet.
func ctrlEmuXboxSheet(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_UP:
		emuXboxMoveFocus(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		emuXboxMoveFocus(1)
	case sdl.CONTROLLER_BUTTON_A:
		emuXboxPress(emuXboxFocused())
	case sdl.CONTROLLER_BUTTON_X:
		emuXboxPressKey("X")
	case sdl.CONTROLLER_BUTTON_Y:
		emuXboxPressKey("Y")
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		emuCloseXboxSetup()
	}
	return true
}

// handleEmuXboxKey is the keyboard on the sheet.
func handleEmuXboxKey(e *sdl.KeyboardEvent) bool {
	switch e.Keysym.Sym {
	case sdl.K_LEFT, sdl.K_UP:
		emuXboxMoveFocus(-1)
	case sdl.K_RIGHT, sdl.K_DOWN, sdl.K_TAB:
		emuXboxMoveFocus(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		emuXboxPress(emuXboxFocused())
	case sdl.K_a:
		// On the handheld the sheet opens on Back so a second A never starts
		// a download; the keyboard's A follows the same focus there.
		if IsWindows() {
			emuXboxPress(0)
		} else {
			emuXboxPress(emuXboxFocused())
		}
	case sdl.K_x, sdl.K_h:
		emuXboxPressKey("X")
	case sdl.K_y, sdl.K_o, sdl.K_g:
		emuXboxPressKey("Y")
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		emuCloseXboxSetup()
	}
	return true
}

// handleEmuXboxMouse presses a clicked button; a click outside the sheet
// closes it.
func handleEmuXboxMouse(mx, my int32) bool {
	emuXboxSheet.mu.Lock()
	buttons := append([]emuXboxButtonHit(nil), emuXboxSheet.buttons...)
	rect := emuXboxSheet.rect
	emuXboxSheet.mu.Unlock()
	for _, hit := range buttons {
		if pointInRect(mx, my, hit.rect) {
			emuXboxSheet.mu.Lock()
			emuXboxSheet.focus = hit.index
			emuXboxSheet.mu.Unlock()
			emuXboxPress(hit.index)
			return true
		}
	}
	if rect.W > 0 && pointInRect(mx, my, rect) {
		return true
	}
	emuCloseXboxSetup()
	return true
}

// emuXboxSheetRect centres the sheet on the Emulators page.
func emuXboxSheetRect(page sdl.Rect) sdl.Rect {
	w := page.W - 48
	if w > 860 {
		w = 860
	}
	h := page.H - 40
	if h > 560 {
		h = 560
	}
	if w < 0 {
		w = page.W
	}
	if h < 0 {
		h = page.H
	}
	return sdl.Rect{X: page.X + (page.W-w)/2, Y: page.Y + (page.H-h)/2, W: w, H: h}
}

// emuXboxSheetHeading is the sheet's subtitle and status chip for a report.
func emuXboxSheetHeading(report emuXboxReport, scanned bool) (string, string, sdl.Color) {
	if !scanned {
		return "Checking the Xbox files…", "", ColorTextTertiary()
	}
	missing := report.missing()
	if report.Emulator == hakuxName {
		switch {
		case !report.Installed && !report.Supported:
			return "No hakuX build for this device", "Not supported", ToastWarnInk()
		case !report.Installed:
			return "hakuX is not installed — X installs the Smart Pro port (experimental)", "Not installed", ColorTextTertiary()
		case len(missing) > 0:
			return "Missing: " + strings.Join(missing, ", "), itoa(len(missing)) + " missing", ToastWarnInk()
		}
		return "Ready — Y opens your Xbox games", "Ready", ToastSuccessInk()
	}
	switch {
	case !report.Installed:
		return "xemu is not installed — X installs it, and the files can go in the folder first", "Not installed", ColorTextTertiary()
	case report.ready() && report.Configured:
		return "Ready — xemu has everything it needs", "Ready", ToastSuccessInk()
	case report.ready():
		return "All files found — A applies them to xemu (starting xemu does too)", "Not applied", ToastWarnInk()
	}
	return "Missing: " + strings.Join(missing, ", "), itoa(len(missing)) + " missing", ToastWarnInk()
}

// renderEmuXboxSheet draws the checklist over the page: a row per item with a
// found / missing mark, the lines under it (where the files go, the settings
// file, on the handheld the controls), and the actions as glass buttons. What
// does not fit above the buttons gives way: the lines first, then the rows.
func renderEmuXboxSheet(renderer *sdl.Renderer, config *Config, page sdl.Rect) {
	if renderer == nil || config == nil || !emuXboxSheetOpen() {
		return
	}
	emuXboxRescanIfStale()
	report, scanned, scanning := emuXboxSnapshot()
	small, _ := getCachedFont(config, "small")
	if small == nil {
		return
	}
	label := FontForRole(config, TypeBody)
	if label == nil {
		label = small
	}
	note := FontForRole(config, TypeLabel)
	if note == nil {
		note = small
	}

	sheet := emuXboxSheetRect(page)
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, sheet)
	drawOverlayCardAccent(renderer, sheet.X, sheet.Y, sheet.W)

	subtitle, statusText, statusColor := emuXboxSheetHeading(report, scanned)
	if scanning && scanned {
		statusText = "Checking…"
	}
	if line := emuXboxInstallLine(); line != "" {
		// The X just pressed is running: its progress replaces what is
		// missing until it ends and the rescan after it updates the rows.
		subtitle, statusText, statusColor = line, "Working…", ColorTextSecondary()
	}
	header := UIPanelHeader("Xbox setup", subtitle, "emu-xbox")
	header.Status = statusText
	header.StatusColor = statusColor
	header = overlayHeaderOnCard(header, sheet)
	bodyY := UIPageHeaderDraw(renderer, config, header) + 14

	padX := OverlayPadX
	rowW := sheet.W - 2*padX
	labelH := int32(label.Height())
	noteH := int32(note.Height())
	rowH := labelH + noteH + 18

	buttons := emuXboxSheetButtons(report, scanned)
	btnFont := FontForRole(config, TypeBody)
	if btnFont == nil {
		btnFont = small
	}
	btnH := int32(btnFont.Height()) + 18
	btnY := sheet.Y + sheet.H - 18 - btnH
	limit := btnY - 10

	y := bodyY
	files := report.Files
	if !scanned {
		files = nil
		renderText(renderer, config, small, "Looking for the Xbox files…", ColorTextSecondary(), sheet.X+padX, y)
		y += int32(small.Height()) + 10
	}
	for _, f := range files {
		if y+rowH > limit {
			break
		}
		row := sdl.Rect{X: sheet.X + padX, Y: y, W: rowW, H: rowH}
		drawGlassSurface(renderer, row, glassRowRadius, glassState{})
		icon, ink := "close", ToastErrorInk()
		switch {
		case f.ok():
			icon, ink = "check", ToastSuccessInk()
		case !f.Required:
			icon, ink = "info", ColorTextTertiary()
		case f.Problem != "":
			icon, ink = "warning", ToastWarnInk()
		}
		mark := sdl.Rect{X: row.X + 12, Y: row.Y + (row.H-26)/2, W: 26, H: 26}
		fillRoundedRect(renderer, mark.X-4, mark.Y-4, mark.W+8, mark.H+8, 10, WithAlpha(ink, 40))
		drawHomeGlyphTinted(renderer, icon, mark, ink)

		textX := mark.X + mark.W + 18
		state := "Missing"
		stateColor := ToastErrorInk()
		switch {
		case f.ok():
			state, stateColor = "Found", ToastSuccessInk()
			if f.Note != "" {
				state = "Found · " + f.Note
			}
		case f.Problem != "":
			state, stateColor = f.Problem, ToastWarnInk()
		case !f.Required:
			state, stateColor = "Optional", ColorTextTertiary()
			if f.Note != "" {
				state = f.Note
			}
		}
		stateW, _ := measureText(note, state)
		renderText(renderer, config, note, state, stateColor, row.X+row.W-14-stateW, row.Y+9+(labelH-noteH)/2)
		nameW := row.X + row.W - 28 - stateW - textX
		renderText(renderer, config, label, ellipsize(label, f.Label, nameW), ColorTextPrimary(), textX, row.Y+8)
		detail := f.Want
		if f.Path != "" {
			detail = f.Path
		}
		renderText(renderer, config, note, ellipsize(note, detail, row.X+row.W-14-textX), ColorTextTertiary(), textX, row.Y+10+labelH)
		y += rowH + 8
	}

	y += 4
	var lines []emuFooterSegment
	if report.Folder != "" {
		lines = append(lines, emuFooterSegment{"Put the files in " + report.Folder, ColorTextSecondary()})
	}
	if report.ConfigPath != "" {
		cfg := report.Emulator + " settings: " + report.ConfigPath
		if report.Configured && report.Emulator != hakuxName {
			cfg += " · set up"
		}
		lines = append(lines, emuFooterSegment{cfg, ColorTextTertiary()})
	}
	for _, line := range report.Notes {
		lines = append(lines, emuFooterSegment{line, ColorTextSecondary()})
	}
	for _, line := range lines {
		if y+noteH > limit {
			break
		}
		renderText(renderer, config, note, ellipsize(note, line.text, rowW), line.color, sheet.X+padX, y)
		y += noteH + 4
	}

	// The actions: glass buttons along the bottom, the focused one ringed and
	// the first one tinted while it can act.
	focus := emuXboxFocused()
	gap := int32(12)
	count := int32(len(buttons))
	btnW := (rowW - gap*(count-1)) / count
	hits := make([]emuXboxButtonHit, 0, len(buttons))
	for i, b := range buttons {
		rect := sdl.Rect{X: sheet.X + padX + int32(i)*(btnW+gap), Y: btnY, W: btnW, H: btnH}
		tint := sdl.Color{}
		if i == 0 && !b.Disabled {
			tint = glassButtonTint(glassAccent())
		}
		drawGlassButton(renderer, config, rect, b.Label, btnFont, tint, glassState{
			Focused:  i == focus,
			Hovered:  pointInRect(mouseX, mouseY, rect),
			Disabled: b.Disabled,
		})
		hits = append(hits, emuXboxButtonHit{index: i, rect: rect})
	}
	emuXboxSheet.mu.Lock()
	emuXboxSheet.buttons = hits
	emuXboxSheet.rect = sheet
	emuXboxSheet.mu.Unlock()
}
