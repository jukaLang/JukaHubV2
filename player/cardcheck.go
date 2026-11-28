package main

// cardcheck.go — Card check (Tools → System, Settings → About): is the SD
// card that holds the games healthy?
//
// One run walks up to five steps. Each can be stopped, each has its line in
// the report, and none of them writes to the file system's own structures:
//
//  1. Where: which partition holds the games (/proc/mounts), its file system,
//     size and free space, and the card's name and maker from sysfs.
//  2. Health flags, read from the partition's first sectors and never
//     written. FAT16/FAT32: the boot sector's state byte (bit 0 "dirty",
//     offset 37 on FAT16 and 65 on FAT32) and FAT[1]'s ClnShutBitMask and
//     HrdErrBitMask (0x8000/0x4000 on FAT16, 0x08000000/0x04000000 on FAT32;
//     Microsoft FAT specification). exFAT: VolumeFlags at byte 106,
//     VolumeDirty (bit 1) and MediaFailure (bit 2) (Microsoft exFAT
//     specification 3.1.13). Linux marks a FAT or exFAT volume dirty while it
//     is mounted, so on a card in use "dirty" alone means nothing; the
//     kernel's own warning when it mounted the card ("Volume was not properly
//     unmounted") is what tells a power cut apart, and the step reads the
//     kernel log for it and for I/O errors on the card.
//  3. Read every file (on by default): each file is read to its end at a
//     capped rate on an idle-priority thread, its cached pages dropped first
//     so the card itself answers, and files that fail with an I/O error are
//     listed. Other file systems mounted inside the card, links and the swap
//     file are skipped.
//  4. Fake card test (off by default): files whose every 4 KiB block carries
//     its own offset and a seeded pattern are written into free space (1 GB,
//     or all of it but a margin), read back after the caches are dropped, and
//     compared. A counterfeit card that claims more than it stores loses
//     some of it, and the intact amount says what it really holds. The files
//     are always deleted; a marker in the data folder makes the next start
//     delete them after a crash. Games cannot start while the card is full of
//     test files (a launch gate).
//  5. fsck: when the system has fsck.vfat / fsck.fat / dosfsck / fsck.exfat,
//     it runs read-only (-n) and its summary is shown. A card in use changes
//     under it, so it can raise false alarms; the report says so.
//
// Repair is a computer's job (Windows: chkdsk X: /f): the card holds the
// running system, and the boot paths (jukaboot, the Smart Pro init, the stock
// firmware) have no fsck before they mount it. The report says how.
//
// The disk work runs on one goroutine; cardcheck_ui.go reads its published
// state. Nothing here decides by the wall clock: TrimUI boots before NTP.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cardCheckReportName  = "card-check.txt"
	cardCheckFillDirName = ".jukahub-cardcheck"
	cardCheckMarkerName  = "card-check-fill.json"
	cardCheckFillPrefix  = "fill-"
	cardCheckFillSuffix  = ".bin"
	// cardCheckFillFile is one test file; FAT32 takes 4 GiB at most.
	cardCheckFillFile = 256 << 20
	// cardCheckQuickBytes is the quick fake card test.
	cardCheckQuickBytes = 1 << 30
	// cardCheckMinFill is the least free space a fake card test runs on.
	cardCheckMinFill = 64 << 20
	// cardCheckBlock is the unit the pattern is written and compared in.
	cardCheckBlock = 4096
	// cardCheckChunk is one read or write.
	cardCheckChunk = 1 << 20
	// cardCheckReadRate caps the read test, so the menus stay smooth.
	cardCheckReadRate = 32 << 20
	// cardCheckMaxListed is how many failing files the report names.
	cardCheckMaxListed = 200
	// cardCheckFsckTimeout bounds the read-only fsck.
	cardCheckFsckTimeout = 10 * time.Minute
	// cardCheckFsckOutput bounds what is kept of its output.
	cardCheckFsckOutput = 64 << 10
	// cardCheckMinBattery is the charge a fake card test starts on without a
	// charger; cardCheckLowBattery stops one.
	cardCheckMinBattery = 30
	cardCheckLowBattery = 10
	// cardCheckAwakeEvery is how often a run counts as use, so Auto sleep
	// does not freeze a check the person is waiting for.
	cardCheckAwakeEvery = 30 * time.Second
)

// The fake card test's sizes.
const (
	cardCheckFakeOff = iota
	cardCheckFakeQuick
	cardCheckFakeFull
)

// cardCheckState is one step's outcome.
type cardCheckState string

const (
	ccPending cardCheckState = ""
	ccRunning cardCheckState = "running"
	ccOK      cardCheckState = "ok"
	ccWarn    cardCheckState = "warn"
	ccBad     cardCheckState = "bad"
	ccSkipped cardCheckState = "skipped"
	ccStopped cardCheckState = "stopped"
)

// The steps, in the order they run.
const (
	ccStepWhere = "where"
	ccStepFlags = "flags"
	ccStepRead  = "read"
	ccStepFake  = "fake"
	ccStepFsck  = "fsck"
)

// cardCheckStep is one step as the screen and the report show it.
type cardCheckStep struct {
	id      string
	title   string
	state   cardCheckState
	summary string
	lines   []string
	// done/total is the progress; bytes says the unit (else files).
	done, total int64
	bytes       bool
	phase       string
}

// cardCheckOptions is what the person chose before Start.
type cardCheckOptions struct {
	readAll bool
	fake    int
	// gamesDir is the games folder, found on the main thread at Start.
	gamesDir string
}

// cardCheckVolume is the partition that holds the games.
type cardCheckVolume struct {
	GamesDir    string
	Mount       string
	Device      string
	FSType      string
	ReadOnly    bool
	Total, Free int64
	Disk        string
	CardName    string
	CardMaker   string
	CardDate    string
}

// cardCheck is the run, shared by the worker and the screen.
var cardCheck struct {
	mu      sync.Mutex
	gen     uint64
	running bool
	// filling is set while test files take the card's free space.
	filling bool
	cancel  context.CancelFunc
	steps   []cardCheckStep
	vol     *cardCheckVolume
	report  string
	saved   string
}

// cardCheckSnap is the shared state copied for one frame.
type cardCheckSnap struct {
	gen     uint64
	running bool
	steps   []cardCheckStep
	vol     *cardCheckVolume
	report  string
	saved   string
}

func cardCheckSnapshot() cardCheckSnap {
	cardCheck.mu.Lock()
	defer cardCheck.mu.Unlock()
	s := cardCheckSnap{gen: cardCheck.gen, running: cardCheck.running, vol: cardCheck.vol, report: cardCheck.report, saved: cardCheck.saved}
	s.steps = append([]cardCheckStep(nil), cardCheck.steps...)
	return s
}

// cardCheckSupported reports whether this device has a card to check: the
// handheld, on Linux. A computer's drives are its own system's business.
func cardCheckSupported() bool {
	return runtime.GOOS == "linux" && IsTrimuiDevice()
}

func init() {
	// After the first frame, off the main thread: test files a crash left.
	deferStartupTask("cardcheck-leftovers", func() { go cardCheckCleanupLeftovers() })
	gameLaunchRegisterGate(cardCheckLaunchGate)
}

// cardCheckLaunchGate keeps games from starting while the fake card test has
// the card full: a game's saves would have nowhere to go.
func cardCheckLaunchGate(game libraryGame, launcher string) (bool, error) {
	cardCheck.mu.Lock()
	filling := cardCheck.filling
	cardCheck.mu.Unlock()
	if filling {
		return false, errors.New("the card check is testing the card's free space — stop it or wait for it first (Tools → Card check)")
	}
	return false, nil
}

// cardCheckStepTitles names the steps.
var cardCheckStepTitles = map[string]string{
	ccStepWhere: "Card and partition",
	ccStepFlags: "Health flags",
	ccStepRead:  "Read every file",
	ccStepFake:  "Fake card test",
	ccStepFsck:  "File system check (fsck)",
}

// cardCheckPlanSteps lists the steps of a run with opts, all pending.
func cardCheckPlanSteps(opts cardCheckOptions) []cardCheckStep {
	ids := []string{ccStepWhere, ccStepFlags, ccStepRead, ccStepFake, ccStepFsck}
	out := make([]cardCheckStep, 0, len(ids))
	for _, id := range ids {
		st := cardCheckStep{id: id, title: cardCheckStepTitles[id]}
		switch {
		case id == ccStepRead && !opts.readAll:
			st.state, st.summary = ccSkipped, "Not chosen"
		case id == ccStepFake && opts.fake == cardCheckFakeOff:
			st.state, st.summary = ccSkipped, "Not chosen"
		}
		out = append(out, st)
	}
	return out
}

// cardCheckStart starts a run; false when one is running.
func cardCheckStart(opts cardCheckOptions) bool {
	cardCheck.mu.Lock()
	if cardCheck.running {
		cardCheck.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	cardCheck.gen++
	gen := cardCheck.gen
	cardCheck.running = true
	cardCheck.cancel = cancel
	cardCheck.steps = cardCheckPlanSteps(opts)
	cardCheck.vol = nil
	cardCheck.report, cardCheck.saved = "", ""
	cardCheck.mu.Unlock()
	go cardCheckRun(ctx, gen, opts)
	return true
}

// cardCheckStop asks the running check to stop; the step running says so.
func cardCheckStop() {
	cardCheck.mu.Lock()
	cancel := cardCheck.cancel
	cardCheck.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// cardCheckRunning reports a run in progress.
func cardCheckRunning() bool {
	cardCheck.mu.Lock()
	defer cardCheck.mu.Unlock()
	return cardCheck.running
}

// cardCheckUpdate changes step id of run gen under the lock.
func cardCheckUpdate(gen uint64, id string, fn func(st *cardCheckStep)) {
	cardCheck.mu.Lock()
	defer cardCheck.mu.Unlock()
	if cardCheck.gen != gen {
		return
	}
	for i := range cardCheck.steps {
		if cardCheck.steps[i].id == id {
			fn(&cardCheck.steps[i])
			return
		}
	}
}

// cardCheckFinish records a step's outcome.
func cardCheckFinish(gen uint64, id string, state cardCheckState, summary string, lines []string) {
	cardCheckUpdate(gen, id, func(st *cardCheckStep) {
		st.state, st.summary, st.lines = state, summary, lines
		st.phase = ""
	})
}

// cardCheckStepPending reports whether step id still has to run.
func cardCheckStepPending(gen uint64, id string) bool {
	cardCheck.mu.Lock()
	defer cardCheck.mu.Unlock()
	for _, st := range cardCheck.steps {
		if st.id == id {
			return cardCheck.gen == gen && st.state == ccPending
		}
	}
	return false
}

// cardCheckRun is the worker. Its OS thread becomes an idle-priority one and
// is never handed back (sdMoveLowPriority).
func cardCheckRun(ctx context.Context, gen uint64, opts cardCheckOptions) {
	runtime.LockOSThread()
	sdMoveLowPriority()
	stopAwake := make(chan struct{})
	go cardCheckKeepAwake(stopAwake)
	defer func() {
		close(stopAwake)
		if r := recover(); r != nil {
			logError("cardcheck", "check stopped: %v", r)
			cardCheck.mu.Lock()
			for i := range cardCheck.steps {
				if cardCheck.gen == gen && (cardCheck.steps[i].state == ccRunning || cardCheck.steps[i].state == ccPending) {
					cardCheck.steps[i].state, cardCheck.steps[i].summary = ccStopped, "The check stopped unexpectedly"
				}
			}
			cardCheck.mu.Unlock()
		}
		cardCheckEnd(gen)
	}()

	stopped := func(id string) bool {
		if ctx.Err() == nil {
			return false
		}
		cardCheckFinish(gen, id, ccStopped, "Stopped", nil)
		return true
	}
	cardCheckUpdate(gen, ccStepWhere, func(st *cardCheckStep) { st.state = ccRunning })
	vol, err := cardCheckFindVolume(opts.gamesDir)
	if err != nil {
		cardCheckFinish(gen, ccStepWhere, ccBad, err.Error(), nil)
		cardCheck.mu.Lock()
		for i := range cardCheck.steps {
			if cardCheck.gen == gen && cardCheck.steps[i].state == ccPending {
				cardCheck.steps[i].state, cardCheck.steps[i].summary = ccSkipped, "The games' card was not found"
			}
		}
		cardCheck.mu.Unlock()
		return
	}
	cardCheck.mu.Lock()
	if cardCheck.gen == gen {
		cardCheck.vol = &vol
	}
	cardCheck.mu.Unlock()
	state, summary, lines := cardCheckWhereResult(vol)
	cardCheckFinish(gen, ccStepWhere, state, summary, lines)

	boot := cardCheckBoot{}
	if !stopped(ccStepFlags) {
		cardCheckUpdate(gen, ccStepFlags, func(st *cardCheckStep) { st.state = ccRunning })
		var st cardCheckState
		boot, st, summary, lines = cardCheckFlagsStep(vol)
		cardCheckFinish(gen, ccStepFlags, st, summary, lines)
	}
	if cardCheckStepPending(gen, ccStepRead) && !stopped(ccStepRead) {
		cardCheckUpdate(gen, ccStepRead, func(st *cardCheckStep) {
			st.state, st.bytes, st.total = ccRunning, true, vol.Total-vol.Free
		})
		res := cardCheckReadAll(ctx, vol.Mount, cardCheckSkipper(vol.Mount), func(files, n int64, cur string) {
			cardCheckUpdate(gen, ccStepRead, func(st *cardCheckStep) {
				st.done, st.phase = n, fmt.Sprintf("%s files · %s", formatNumber(files), cur)
			})
		})
		st, summary, lines := cardCheckReadResultText(res)
		cardCheckFinish(gen, ccStepRead, st, summary, lines)
	}
	if cardCheckStepPending(gen, ccStepFake) && !stopped(ccStepFake) {
		cardCheckUpdate(gen, ccStepFake, func(st *cardCheckStep) { st.state, st.bytes = ccRunning, true })
		st, summary, lines := cardCheckFakeStep(ctx, gen, vol, opts.fake)
		cardCheckFinish(gen, ccStepFake, st, summary, lines)
	}
	if !stopped(ccStepFsck) {
		cardCheckUpdate(gen, ccStepFsck, func(st *cardCheckStep) { st.state = ccRunning })
		st, summary, lines := cardCheckFsckStep(ctx, vol, boot)
		if ctx.Err() != nil && st != ccOK {
			st, summary, lines = ccStopped, "Stopped", nil
		}
		cardCheckFinish(gen, ccStepFsck, st, summary, lines)
	}
}

// cardCheckKeepAwake counts the run as use until stop closes.
func cardCheckKeepAwake(stop <-chan struct{}) {
	t := time.NewTicker(cardCheckAwakeEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			noteUserInputTime(now)
		}
	}
}

// cardCheckEnd writes the report (screen and file) and ends run gen.
func cardCheckEnd(gen uint64) {
	snap := cardCheckSnapshot()
	if snap.gen != gen {
		return
	}
	text := cardCheckReport(snap.steps)
	saved := ""
	if dir, err := P().DataDir(); err == nil && dir != "" {
		path := filepath.Join(dir, cardCheckReportName)
		if err := AtomicWrite(path, []byte(text), 0o644); err != nil {
			logWarn("cardcheck", "could not save the report: %v", err)
		} else {
			saved = path
		}
	}
	cardCheck.mu.Lock()
	if cardCheck.gen == gen {
		cardCheck.running = false
		cardCheck.filling = false
		if cardCheck.cancel != nil {
			cardCheck.cancel()
			cardCheck.cancel = nil
		}
		cardCheck.report, cardCheck.saved = text, saved
	}
	cardCheck.mu.Unlock()
	logInfo("cardcheck", "check finished: %s", cardCheckVerdict(snap.steps))
	tbxPost(func() {
		toast, kind := cardCheckDoneToast(snap.steps)
		ShowToast(toast, kind)
	})
}

// cardCheckWorst is the worst state among the steps that ran.
func cardCheckWorst(steps []cardCheckStep) cardCheckState {
	worst := ccOK
	for _, st := range steps {
		switch st.state {
		case ccBad:
			return ccBad
		case ccWarn:
			worst = ccWarn
		case ccStopped:
			if worst == ccOK {
				worst = ccStopped
			}
		}
	}
	return worst
}

// cardCheckVerdict is the run in a few words, for the log and the tile.
func cardCheckVerdict(steps []cardCheckStep) string {
	switch cardCheckWorst(steps) {
	case ccBad:
		return "problems found"
	case ccWarn:
		return "warnings"
	case ccStopped:
		return "stopped"
	}
	return "no problems found"
}

// cardCheckDoneToast is the toast a finished run shows.
func cardCheckDoneToast(steps []cardCheckStep) (string, ToastKind) {
	switch cardCheckWorst(steps) {
	case ccBad:
		return "Card check: problems found — see the report", ToastKindError
	case ccWarn:
		return "Card check finished with warnings", ToastKindWarning
	case ccStopped:
		return "Card check stopped", ToastKindInfo
	}
	return "Card check: no problems found", ToastKindSuccess
}

// ── Step 1: where the games are ──────────────────────────────────────────

// cardCheckMount is one line of /proc/mounts.
type cardCheckMount struct {
	Source, Target, FSType string
	Options                []string
}

// cardCheckUnescape decodes the octal escapes /proc/mounts uses for spaces,
// tabs, newlines and backslashes.
func cardCheckUnescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// cardCheckParseMounts reads /proc/mounts.
func cardCheckParseMounts(text string) []cardCheckMount {
	var out []cardCheckMount
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		out = append(out, cardCheckMount{
			Source:  cardCheckUnescape(f[0]),
			Target:  cardCheckUnescape(f[1]),
			FSType:  f[2],
			Options: strings.Split(f[3], ","),
		})
	}
	return out
}

// cardCheckMountFor is the mount that holds p: the longest mount point that
// is p or a folder above it. A later line for the same mount point wins, as
// the kernel's newest mount hides the older one. Paths are Linux's.
func cardCheckMountFor(mounts []cardCheckMount, p string) (cardCheckMount, bool) {
	p = path.Clean(p)
	best, found := cardCheckMount{}, false
	for _, m := range mounts {
		t := path.Clean(m.Target)
		if !(p == t || t == "/" || strings.HasPrefix(p, t+"/")) {
			continue
		}
		if !found || len(t) >= len(path.Clean(best.Target)) {
			best, found = m, true
		}
	}
	return best, found
}

// cardCheckSafeDev accepts a block device path the steps may open: /dev/
// and a plain name ("mmcblk0p1", "sda1"), or /dev/block/<name>.
func cardCheckSafeDev(dev string) bool {
	name := strings.TrimPrefix(strings.TrimPrefix(dev, "/dev/block/"), "/dev/")
	if name == dev || name == "" || len(name) > 32 {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// cardCheckGamesDir is the folder whose card is checked: the games folder,
// else the card JukaHub runs from. Main thread.
func cardCheckGamesDir() string {
	if games := nasRomsRoot(); games != "" && dirExists(games) {
		return games
	}
	return sdMoveSourceRoot()
}

// cardCheckFindVolume finds the partition that holds games.
func cardCheckFindVolume(games string) (cardCheckVolume, error) {
	if games == "" {
		return cardCheckVolume{}, errors.New("No games folder was found on this device")
	}
	if real, err := filepath.EvalSymlinks(games); err == nil {
		games = real
	}
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return cardCheckVolume{}, fmt.Errorf("The list of mounted drives could not be read: %v", err)
	}
	m, ok := cardCheckMountFor(cardCheckParseMounts(string(data)), games)
	if !ok || filepath.Clean(m.Target) == "/" {
		return cardCheckVolume{}, errors.New("The games are on the system's own partition, not on a card")
	}
	vol := cardCheckVolume{GamesDir: games, Mount: filepath.Clean(m.Target), FSType: m.FSType}
	for _, o := range m.Options {
		if o == "ro" {
			vol.ReadOnly = true
		}
	}
	if cardCheckSafeDev(m.Source) {
		vol.Device = m.Source
	}
	if total, free, _, err := sdMoveStatfs(vol.Mount); err == nil {
		vol.Total, vol.Free = total, free
	}
	vol.Disk = sdMoveDiskOf(vol.Mount)
	if vol.Disk != "" {
		dev := filepath.Join("/sys/block", vol.Disk, "device")
		vol.CardName = cardCheckSysfsLine(filepath.Join(dev, "name"))
		vol.CardMaker = cardCheckSysfsLine(filepath.Join(dev, "manfid"))
		vol.CardDate = cardCheckSysfsLine(filepath.Join(dev, "date"))
	}
	return vol, nil
}

// cardCheckSysfsLine is a short sysfs value, "" when there is none.
func cardCheckSysfsLine(path string) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 128 {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// cardCheckFSName is a file system type as people know it.
func cardCheckFSName(fstype string) string {
	switch strings.ToLower(fstype) {
	case "vfat", "msdos", "fat":
		return "FAT32"
	case "exfat", "sdfat", "texfat":
		return "exFAT"
	case "ext4", "ext3", "ext2", "f2fs", "btrfs", "ntfs", "ntfs3":
		return strings.ToUpper(fstype)
	}
	return fstype
}

// cardCheckWhereResult is step 1's report.
func cardCheckWhereResult(vol cardCheckVolume) (cardCheckState, string, []string) {
	lines := []string{"Games folder: " + vol.GamesDir, "Mounted at: " + vol.Mount}
	dev := vol.Device
	if dev == "" {
		dev = "unknown"
	}
	lines = append(lines, "Partition: "+dev)
	mode := "read-write"
	if vol.ReadOnly {
		mode = "read-only"
	}
	lines = append(lines, "File system: "+cardCheckFSName(vol.FSType)+" ("+vol.FSType+"), "+mode)
	if vol.Total > 0 {
		lines = append(lines, "Size: "+formatSize(vol.Total)+" · free "+formatSize(vol.Free))
	}
	if vol.CardName != "" || vol.CardMaker != "" {
		card := "Card: " + vol.CardName
		if vol.CardMaker != "" {
			card += " · maker " + vol.CardMaker
		}
		if vol.CardDate != "" {
			card += " · made " + vol.CardDate
		}
		lines = append(lines, strings.TrimSpace(card))
	}
	summary := dev + " · " + cardCheckFSName(vol.FSType)
	if vol.Total > 0 {
		summary += " · " + formatSize(vol.Free) + " free of " + formatSize(vol.Total)
	}
	if vol.ReadOnly {
		// The kernel turns a card read-only when it finds an error on it.
		lines = append(lines, "The card is mounted read-only. Linux does that by itself when it finds an error on the card.")
		return ccWarn, "Read-only · " + summary, lines
	}
	return ccOK, summary, lines
}

// ── Step 2: the file system's own health flags ───────────────────────────

// cardCheckBoot is what the partition's boot sector says.
type cardCheckBoot struct {
	// Kind is "FAT12", "FAT16", "FAT32" or "exFAT".
	Kind string
	// StateDirty is FAT16/FAT32's boot-sector dirty bit.
	StateDirty bool
	// FAT1Offset is the byte offset of the FAT[1] entry (FAT16/FAT32).
	FAT1Offset int64
	// exFAT's VolumeFlags.
	VolumeDirty, MediaFailure bool
}

// cardCheckDecodeBoot reads a partition's first sector (at least 512 bytes).
func cardCheckDecodeBoot(sec []byte) (cardCheckBoot, error) {
	if len(sec) < 512 {
		return cardCheckBoot{}, errors.New("the boot sector is too short")
	}
	if sec[510] != 0x55 || sec[511] != 0xAA {
		return cardCheckBoot{}, errors.New("no boot sector signature: not a FAT or exFAT partition")
	}
	if string(sec[3:11]) == "EXFAT   " {
		flags := binary.LittleEndian.Uint16(sec[106:108])
		return cardCheckBoot{Kind: "exFAT", VolumeDirty: flags&0x0002 != 0, MediaFailure: flags&0x0004 != 0}, nil
	}
	bps := int64(binary.LittleEndian.Uint16(sec[11:13]))
	spc := int64(sec[13])
	rsvd := int64(binary.LittleEndian.Uint16(sec[14:16]))
	nfats := int64(sec[16])
	rootEnt := int64(binary.LittleEndian.Uint16(sec[17:19]))
	tot := int64(binary.LittleEndian.Uint16(sec[19:21]))
	fatSz := int64(binary.LittleEndian.Uint16(sec[22:24]))
	if fatSz == 0 {
		fatSz = int64(binary.LittleEndian.Uint32(sec[36:40]))
	}
	if tot == 0 {
		tot = int64(binary.LittleEndian.Uint32(sec[32:36]))
	}
	switch bps {
	case 512, 1024, 2048, 4096:
	default:
		return cardCheckBoot{}, errors.New("not a FAT or exFAT partition")
	}
	if spc == 0 || spc&(spc-1) != 0 || rsvd == 0 || nfats == 0 || fatSz == 0 || tot == 0 {
		return cardCheckBoot{}, errors.New("not a FAT or exFAT partition")
	}
	rootSecs := (rootEnt*32 + bps - 1) / bps
	data := tot - (rsvd + nfats*fatSz + rootSecs)
	if data <= 0 {
		return cardCheckBoot{}, errors.New("the boot sector's sizes do not add up")
	}
	clusters := data / spc
	b := cardCheckBoot{FAT1Offset: rsvd * bps}
	switch {
	case clusters < 4085:
		b.Kind = "FAT12"
	case clusters < 65525:
		b.Kind = "FAT16"
		b.StateDirty = sec[37]&0x01 != 0
		b.FAT1Offset += 2
	default:
		b.Kind = "FAT32"
		b.StateDirty = sec[65]&0x01 != 0
		b.FAT1Offset += 4
	}
	return b, nil
}

// cardCheckFAT1Flags decodes the FAT[1] entry (2 bytes on FAT16, 4 on
// FAT32): whether the last driver that used the volume finished cleanly and
// whether it met no disk errors. FAT12 keeps neither.
func cardCheckFAT1Flags(kind string, entry []byte) (clean, noErrors, ok bool) {
	switch kind {
	case "FAT16":
		if len(entry) < 2 {
			return false, false, false
		}
		v := binary.LittleEndian.Uint16(entry)
		return v&0x8000 != 0, v&0x4000 != 0, true
	case "FAT32":
		if len(entry) < 4 {
			return false, false, false
		}
		v := binary.LittleEndian.Uint32(entry)
		return v&0x08000000 != 0, v&0x04000000 != 0, true
	}
	return false, false, false
}

// cardCheckKernelFindings is what the kernel log says about the card.
type cardCheckKernelFindings struct {
	lines     []string
	unclean   bool // mounted after a power cut or a pulled card
	fsErrors  bool // the file system driver met an inconsistency
	ioErrors  bool // reads or writes failed
	readOnly  bool // the driver turned the card read-only
	mediaFail bool // exFAT's media failure warning
}

// cardCheckKernelLines picks the kernel log lines about partition part (e.g.
// "mmcblk0p1") or its disk.
func cardCheckKernelLines(log, part, disk string) cardCheckKernelFindings {
	var out cardCheckKernelFindings
	if part == "" && disk == "" {
		return out
	}
	sc := bufio.NewScanner(strings.NewReader(log))
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// "<3>[   12.3] text": the level prefix goes.
		if strings.HasPrefix(line, "<") {
			if i := strings.IndexByte(line, '>'); i > 0 && i < 5 {
				line = strings.TrimSpace(line[i+1:])
			}
		}
		about := (part != "" && strings.Contains(line, part)) || (disk != "" && strings.Contains(line, disk))
		if !about {
			continue
		}
		low := strings.ToLower(line)
		hit := false
		switch {
		case strings.Contains(low, "not properly unmounted"):
			out.unclean, hit = true, true
		case strings.Contains(low, "medium has reported failures"):
			out.mediaFail, hit = true, true
		case strings.Contains(low, "read-only") || strings.Contains(low, "remounting filesystem read-only"):
			out.readOnly, hit = true, true
		case strings.Contains(low, "i/o error") || strings.Contains(low, "blk_update_request") || strings.Contains(low, "timeout") || strings.Contains(low, "crc"):
			out.ioErrors, hit = true, true
		case (strings.Contains(low, "fat-fs") || strings.Contains(low, "exfat") || strings.Contains(low, "sdfat")) && (strings.Contains(low, "error") || strings.Contains(low, "corrupt")):
			out.fsErrors, hit = true, true
		}
		if hit && len(out.lines) < 20 {
			out.lines = append(out.lines, devCheckClip(line, 200))
		}
	}
	return out
}

// cardCheckFlagsStep reads the boot sector and the kernel log.
func cardCheckFlagsStep(vol cardCheckVolume) (cardCheckBoot, cardCheckState, string, []string) {
	var lines []string
	state := ccOK
	worse := func(s cardCheckState) {
		if s == ccBad || (s == ccWarn && state == ccOK) {
			state = s
		}
	}
	var boot cardCheckBoot
	bootOK := false
	if vol.Device == "" {
		lines = append(lines, "The partition's device is unknown, so its flags were not read.")
		worse(ccWarn)
	} else if sec, err := cardCheckReadAt(vol.Device, 0, 512); err != nil {
		lines = append(lines, "The boot sector could not be read: "+err.Error())
		worse(ccWarn)
	} else if b, err := cardCheckDecodeBoot(sec); err != nil {
		lines = append(lines, "Flags not read: "+err.Error()+".")
	} else {
		boot, bootOK = b, true
	}
	mounted := !vol.ReadOnly
	if bootOK {
		lines = append(lines, "File system: "+boot.Kind)
		switch boot.Kind {
		case "exFAT":
			if boot.MediaFailure {
				lines = append(lines, "MediaFailure is set: a driver met an unrecoverable read or write error on this card.")
				worse(ccBad)
			} else {
				lines = append(lines, "MediaFailure: clear")
			}
			lines = append(lines, cardCheckDirtyLine(boot.VolumeDirty, mounted, "VolumeDirty"))
		case "FAT16", "FAT32":
			lines = append(lines, cardCheckDirtyLine(boot.StateDirty, mounted, "Dirty bit"))
			if entry, err := cardCheckReadAt(vol.Device, boot.FAT1Offset, 4); err == nil {
				if clean, noErr, ok := cardCheckFAT1Flags(boot.Kind, entry); ok {
					if clean {
						lines = append(lines, "Clean-shutdown bit (FAT[1]): set")
					} else {
						lines = append(lines, "Clean-shutdown bit (FAT[1]) is clear: the last computer or camera that used the card did not finish with it cleanly.")
						worse(ccWarn)
					}
					if noErr {
						lines = append(lines, "Hard-error bit (FAT[1]): no errors recorded")
					} else {
						lines = append(lines, "Hard-error bit (FAT[1]) says a computer met read or write errors on this card.")
						worse(ccBad)
					}
				}
			}
		default:
			lines = append(lines, boot.Kind+" keeps no health flags.")
		}
	}
	part := filepath.Base(vol.Device)
	if vol.Device == "" {
		part = ""
	}
	if log, err := cardCheckKernelLog(); err != nil {
		lines = append(lines, "The kernel log could not be read: "+err.Error())
	} else {
		k := cardCheckKernelLines(log, part, vol.Disk)
		if k.unclean {
			lines = append(lines, "When it mounted the card, Linux found it was not properly unmounted (a power cut, a crash or the card pulled out). Some files may be damaged: repair it on a computer.")
			worse(ccWarn)
		}
		if k.mediaFail {
			worse(ccBad)
		}
		if k.fsErrors || k.readOnly {
			lines = append(lines, "The file system driver reported errors on this card since the device started.")
			worse(ccBad)
		}
		if k.ioErrors {
			lines = append(lines, "The card reported read or write errors since the device started.")
			worse(ccBad)
		}
		if len(k.lines) > 0 {
			lines = append(lines, "Kernel messages:")
			for _, l := range k.lines {
				lines = append(lines, "  "+l)
			}
		} else {
			lines = append(lines, "The kernel reported nothing wrong with the card since the device started.")
		}
	}
	summary := "No warning signs"
	switch state {
	case ccWarn:
		summary = "Warning signs — see details"
	case ccBad:
		summary = "Errors recorded on the card"
	}
	return boot, state, summary, lines
}

// cardCheckDirtyLine explains a dirty flag: on a card Linux has mounted for
// writing it is set by the mount itself.
func cardCheckDirtyLine(dirty, mounted bool, name string) string {
	switch {
	case !dirty:
		return name + ": clear"
	case mounted:
		return name + ": set — normal while the card is in use (Linux sets it when it mounts the card and clears it when it lets go)"
	}
	return name + " is set: the card was not cleanly unmounted."
}

// ── Step 3: read every file ──────────────────────────────────────────────

// cardCheckParseSwaps lists the swap files /proc/swaps names.
func cardCheckParseSwaps(text string) []string {
	var out []string
	for i, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 2 || f[1] != "file" {
			continue
		}
		out = append(out, cardCheckUnescape(f[0]))
	}
	return out
}

// cardCheckSkipper is the read test's skip rule for a card mounted at mount:
// the swap files, the fake card test's own folder, and every mount point
// inside the card — a bind mount there has the card's own device number, so
// the walk's device test would read it twice (or forever, for a folder
// mounted inside itself).
func cardCheckSkipper(mount string) func(path string) bool {
	skip := map[string]bool{filepath.Join(mount, cardCheckFillDirName): true}
	if data, err := os.ReadFile("/proc/swaps"); err == nil {
		for _, p := range cardCheckParseSwaps(string(data)) {
			skip[filepath.Clean(p)] = true
		}
	}
	if data, err := os.ReadFile("/proc/mounts"); err == nil {
		base := filepath.Clean(mount)
		for _, m := range cardCheckParseMounts(string(data)) {
			if t := filepath.Clean(m.Target); strings.HasPrefix(t, base+"/") {
				skip[t] = true
			}
		}
	}
	return func(path string) bool { return skip[filepath.Clean(path)] }
}

// cardCheckReadResult is the read test's outcome.
type cardCheckReadResult struct {
	Files, Bytes int64
	Bad          []string // "path: error", at most cardCheckMaxListed
	BadCount     int
	Unopened     int
	Stopped      bool
}

// cardCheckReadAll reads every regular file under root on root's file system.
// progress is called after every file and every few chunks.
func cardCheckReadAll(ctx context.Context, root string, skip func(string) bool, progress func(files, bytes int64, cur string)) cardCheckReadResult {
	var res cardCheckReadResult
	rootDev, devOK := sdMoveDevOf(root)
	buf := make([]byte, cardCheckChunk)
	start := time.Now()
	chunks := 0
	bad := func(path string, err error) {
		res.BadCount++
		if len(res.Bad) < cardCheckMaxListed {
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				rel = path
			}
			res.Bad = append(res.Bad, filepath.ToSlash(rel)+": "+cardCheckErrText(err))
		}
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			res.Stopped = true
			return filepath.SkipAll
		}
		if err != nil {
			if cardCheckIsIOError(err) {
				bad(path, err)
			} else {
				res.Unopened++
			}
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if skip != nil && skip(path) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// Another file system mounted inside the card is not the card.
			if devOK && path != root {
				if info, err := d.Info(); err == nil {
					if dev, ok := sdMoveFileDev(info); ok && dev != rootDev {
						return filepath.SkipDir
					}
				}
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rerr := cardCheckReadFile(ctx, path, buf, func(chunk int64) {
			res.Bytes += chunk
			cardCheckPace(ctx, start, res.Bytes)
			// A big file (a disc image) moves the bar while it is read.
			if chunks++; progress != nil && chunks%16 == 0 {
				progress(res.Files, res.Bytes, filepath.Base(path))
			}
		})
		if ctx.Err() != nil {
			res.Stopped = true
			return filepath.SkipAll
		}
		switch {
		case rerr == nil:
			res.Files++
		case cardCheckIsIOError(rerr):
			res.Files++
			bad(path, rerr)
		default:
			res.Unopened++
		}
		if progress != nil {
			progress(res.Files, res.Bytes, filepath.Base(path))
		}
		return nil
	})
	return res
}

// cardCheckReadFile reads path to its end through buf, its cached pages
// dropped first so the card answers. chunk is told every read's size.
func cardCheckReadFile(ctx context.Context, path string, buf []byte, chunk func(int64)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sdMoveDropCache(f)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := f.Read(buf)
		if n > 0 {
			chunk(int64(n))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	// What was read stays out of the page cache: the menus' files keep
	// their place there.
	sdMoveDropCache(f)
	return nil
}

// cardCheckReadAt reads n bytes at off from a block device, read-only.
func cardCheckReadAt(dev string, off int64, n int) ([]byte, error) {
	if !cardCheckSafeDev(dev) {
		return nil, errors.New("not a block device")
	}
	f, err := os.Open(dev)
	if err != nil {
		return nil, errors.New(cardCheckErrText(err))
	}
	defer f.Close()
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, off); err != nil {
		return nil, errors.New(cardCheckErrText(err))
	}
	return buf, nil
}

// cardCheckPace sleeps as long as reading done bytes since start went faster
// than cardCheckReadRate.
func cardCheckPace(ctx context.Context, start time.Time, done int64) {
	want := time.Duration(float64(done) / float64(cardCheckReadRate) * float64(time.Second))
	ahead := want - time.Since(start)
	if ahead <= 0 {
		return
	}
	if ahead > time.Second {
		ahead = time.Second
	}
	t := time.NewTimer(ahead)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// cardCheckErrText is an error without the path os puts in front of it.
func cardCheckErrText(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// cardCheckReadResultText is step 3's report.
func cardCheckReadResultText(res cardCheckReadResult) (cardCheckState, string, []string) {
	lines := []string{
		"Files read: " + formatNumber(res.Files) + " (" + formatSize(res.Bytes) + ")",
	}
	if res.Unopened > 0 {
		lines = append(lines, "Skipped (could not be opened, not an error of the card): "+formatNumber(int64(res.Unopened)))
	}
	if res.BadCount > 0 {
		lines = append(lines, "Files the card could not read: "+formatNumber(int64(res.BadCount)))
		for _, b := range res.Bad {
			lines = append(lines, "  "+b)
		}
		if res.BadCount > len(res.Bad) {
			lines = append(lines, "  …and "+formatNumber(int64(res.BadCount-len(res.Bad)))+" more")
		}
		lines = append(lines, "Copy what you can to a computer, then replace these files from a backup or download them again.")
	}
	switch {
	case res.Stopped:
		return ccStopped, "Stopped after " + formatNumber(res.Files) + " files", lines
	case res.BadCount > 0:
		return ccBad, pluralizeCount(res.BadCount, "file", "files") + " could not be read", lines
	}
	return ccOK, "All " + formatNumber(res.Files) + " files read without an error", lines
}

// ── Step 4: fake card test ───────────────────────────────────────────────

// cardCheckMix is splitmix64's finaliser, never 0 (a xorshift seed).
func cardCheckMix(z uint64) uint64 {
	z += 0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	z ^= z >> 31
	if z == 0 {
		z = 1
	}
	return z
}

// cardCheckPattern fills b, whole blocks starting at byte offset off of the
// test data, with the pattern of run seed: each block opens with its offset
// and the seed, then a xorshift stream seeded from both. A block a fake card
// wraps onto another spot, or one from an earlier run, cannot match.
func cardCheckPattern(seed uint64, off int64, b []byte) {
	for start := 0; start+cardCheckBlock <= len(b); start += cardCheckBlock {
		blk := b[start : start+cardCheckBlock]
		o := uint64(off) + uint64(start)
		binary.LittleEndian.PutUint64(blk[0:8], o)
		binary.LittleEndian.PutUint64(blk[8:16], seed)
		x := cardCheckMix(seed ^ o)
		for i := 16; i+8 <= len(blk); i += 8 {
			x ^= x << 13
			x ^= x >> 7
			x ^= x << 17
			binary.LittleEndian.PutUint64(blk[i:i+8], x)
		}
	}
}

// cardCheckFillMarker is the data folder's note of a test folder on a card.
type cardCheckFillMarker struct {
	Dir string `json:"dir"`
}

// cardCheckMarkerPath is the marker's place ("" without a data folder).
func cardCheckMarkerPath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, cardCheckMarkerName)
}

// cardCheckRemoveFill deletes the test files in dir and dir itself. Only
// names the test makes are touched, so a folder of that name holding
// anything else keeps it.
func cardCheckRemoveFill(dir string) error {
	if filepath.Base(dir) != cardCheckFillDirName || !filepath.IsAbs(dir) {
		return fmt.Errorf("refusing to clean %q", dir)
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var firstErr error
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(name, cardCheckFillPrefix) || !strings.HasSuffix(name, cardCheckFillSuffix) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// cardCheckCleanupLeftovers deletes the test files a crash or a power cut
// left on a card: the marker's folder, and the card root's own.
func cardCheckCleanupLeftovers() {
	if cardCheckRunning() {
		return
	}
	dirs := []string{}
	marker := cardCheckMarkerPath()
	if marker != "" {
		if data, err := os.ReadFile(marker); err == nil {
			var m cardCheckFillMarker
			if json.Unmarshal(data, &m) == nil && m.Dir != "" {
				dirs = append(dirs, filepath.Clean(m.Dir))
			}
		}
	}
	if root := fileServerCardRoot(); root != "" {
		dirs = append(dirs, filepath.Join(root, cardCheckFillDirName))
	}
	clean := true
	for _, dir := range dirs {
		if _, err := os.Lstat(dir); err != nil {
			continue
		}
		if err := cardCheckRemoveFill(dir); err != nil {
			clean = false
			logWarn("cardcheck", "test files left in %s: %v", dir, err)
		} else {
			logInfo("cardcheck", "removed the test files a stopped card check left in %s", dir)
		}
	}
	if clean && marker != "" {
		if err := os.Remove(marker); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logWarn("cardcheck", "could not remove %s: %v", marker, err)
		}
	}
}

// cardCheckFillResult is the fake card test's outcome.
type cardCheckFillResult struct {
	Planned, Written int64
	Intact, Lost     int64
	FirstLost        int64 // offset of the first lost block, -1 when none
	WriteErr         string
	ReadErr          string
	Stopped          bool
	Battery          bool // stopped for the battery
}

// cardCheckFakeMargin is the free space the full test leaves: 1% of the card,
// at least 256 MiB, at most 2 GiB.
func cardCheckFakeMargin(total int64) int64 {
	m := total / 100
	if m < 256<<20 {
		m = 256 << 20
	}
	if m > 2<<30 {
		m = 2 << 30
	}
	return m
}

// cardCheckFakeStep runs the fake card test of size mode.
func cardCheckFakeStep(ctx context.Context, gen uint64, vol cardCheckVolume, mode int) (cardCheckState, string, []string) {
	if vol.ReadOnly {
		return ccSkipped, "The card is read-only", nil
	}
	pct, charging := getBatteryPercent(), getBatteryCharging()
	if !charging && pct >= 0 && pct < cardCheckMinBattery {
		return ccSkipped, fmt.Sprintf("Needs the charger or %d%% battery", cardCheckMinBattery), nil
	}
	_, free, _, err := sdMoveStatfs(vol.Mount)
	if err != nil {
		return ccSkipped, "The card's free space could not be read", nil
	}
	want := free - cardCheckFakeMargin(vol.Total)
	if mode == cardCheckFakeQuick && want > cardCheckQuickBytes {
		want = cardCheckQuickBytes
	}
	want -= want % cardCheckBlock
	if want < cardCheckMinFill {
		return ccSkipped, "Not enough free space to test", []string{"Free: " + formatSize(free)}
	}
	cardCheckUpdate(gen, ccStepFake, func(st *cardCheckStep) { st.total = want })
	res := cardCheckFill(ctx, vol.Mount, want, func(phase string, done int64) {
		cardCheckUpdate(gen, ccStepFake, func(st *cardCheckStep) { st.phase, st.done = phase, done })
	})
	return cardCheckFakeResultText(res, vol, mode)
}

// cardCheckSetFilling marks test files on the card (the launch gate's flag).
func cardCheckSetFilling(on bool) {
	cardCheck.mu.Lock()
	cardCheck.filling = on
	cardCheck.mu.Unlock()
}

// cardCheckFill writes want bytes of test files into mount's free space,
// reads them back and deletes them.
func cardCheckFill(ctx context.Context, mount string, want int64, progress func(phase string, done int64)) (res cardCheckFillResult) {
	res.Planned, res.FirstLost = want, -1
	var seedBytes [8]byte
	if _, err := rand.Read(seedBytes[:]); err != nil {
		res.WriteErr = "no random seed: " + err.Error()
		return res
	}
	seed := binary.LittleEndian.Uint64(seedBytes[:])
	dir := filepath.Join(mount, cardCheckFillDirName)
	// The marker goes first and stays until the files are gone: a crash
	// in between leaves the next start something to clean.
	marker := cardCheckMarkerPath()
	if marker != "" {
		data, _ := json.Marshal(cardCheckFillMarker{Dir: dir})
		if err := AtomicWrite(marker, data, 0o644); err != nil {
			logWarn("cardcheck", "could not write the test marker: %v", err)
		}
	}
	cardCheckSetFilling(true)
	defer func() {
		if err := cardCheckRemoveFill(dir); err != nil {
			logWarn("cardcheck", "could not delete the test files: %v", err)
			res.WriteErr = strings.TrimSpace(res.WriteErr + " The test files could not all be deleted (the next start tries again): " + err.Error())
		} else if marker != "" {
			if err := os.Remove(marker); err != nil && !errors.Is(err, fs.ErrNotExist) {
				logWarn("cardcheck", "could not remove the test marker: %v", err)
			}
		}
		cardCheckSetFilling(false)
	}()
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		res.WriteErr = "the test folder could not be made: " + cardCheckErrText(err)
		return res
	}
	// Any files an earlier run left go first.
	_ = cardCheckFillClear(dir)

	buf := make([]byte, cardCheckChunk)
	var sizes []int64
	battCheck := time.Now()
	for res.Written < want && res.WriteErr == "" {
		if ctx.Err() != nil {
			res.Stopped = true
			return res
		}
		size := want - res.Written
		if size > cardCheckFillFile {
			size = cardCheckFillFile
		}
		name := filepath.Join(dir, cardCheckFillName(len(sizes)))
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			res.WriteErr = cardCheckErrText(err)
			break
		}
		var put int64
		for put < size {
			if ctx.Err() != nil {
				break
			}
			n := int64(len(buf))
			if size-put < n {
				n = size - put
			}
			cardCheckPattern(seed, res.Written+put, buf[:n])
			w, err := f.Write(buf[:n])
			put += int64(w) - int64(w)%cardCheckBlock
			if err != nil {
				res.WriteErr = cardCheckErrText(err)
				break
			}
			progress("Writing", res.Written+put)
			if time.Since(battCheck) > 30*time.Second {
				battCheck = time.Now()
				if p := getBatteryPercent(); p >= 0 && p < cardCheckLowBattery && !getBatteryCharging() {
					res.Battery = true
					break
				}
			}
		}
		syncErr := f.Sync()
		sdMoveDropCache(f)
		closeErr := f.Close()
		if res.WriteErr == "" && syncErr != nil {
			res.WriteErr = cardCheckErrText(syncErr)
		}
		if res.WriteErr == "" && closeErr != nil {
			res.WriteErr = cardCheckErrText(closeErr)
		}
		sizes = append(sizes, put)
		res.Written += put
		if res.Battery {
			return res
		}
		if ctx.Err() != nil {
			res.Stopped = true
			return res
		}
	}
	// Read it all back from the card itself, not from memory.
	sdMoveDropAllCaches()
	want2 := make([]byte, cardCheckChunk)
	var off int64
	for i, size := range sizes {
		if ctx.Err() != nil {
			res.Stopped = true
			return res
		}
		cardCheckVerifyFile(ctx, filepath.Join(dir, cardCheckFillName(i)), seed, off, size, buf, want2, &res, func(done int64) {
			progress("Reading back", done)
		})
		off += size
	}
	if ctx.Err() != nil {
		res.Stopped = true
	}
	return res
}

// cardCheckFillName is test file i's name.
func cardCheckFillName(i int) string {
	return fmt.Sprintf("%s%04d%s", cardCheckFillPrefix, i, cardCheckFillSuffix)
}

// cardCheckFillClear deletes test files in dir and keeps dir.
func cardCheckFillClear(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasPrefix(e.Name(), cardCheckFillPrefix) && strings.HasSuffix(e.Name(), cardCheckFillSuffix) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return nil
}

// cardCheckVerifyFile reads one test file of size bytes that holds the
// pattern from offset off, and counts its blocks into res.
func cardCheckVerifyFile(ctx context.Context, path string, seed uint64, off, size int64, buf, want []byte, res *cardCheckFillResult, progress func(done int64)) {
	lost := func(at, n int64) {
		if n <= 0 {
			return
		}
		if res.FirstLost < 0 || at < res.FirstLost {
			res.FirstLost = at
		}
		res.Lost += n
	}
	f, err := os.Open(path)
	if err != nil {
		if res.ReadErr == "" {
			res.ReadErr = cardCheckErrText(err)
		}
		lost(off, size)
		return
	}
	defer f.Close()
	sdMoveDropCache(f)
	var read int64
	for read < size {
		if ctx.Err() != nil {
			return
		}
		n := int64(len(buf))
		if size-read < n {
			n = size - read
		}
		got, err := io.ReadFull(f, buf[:n])
		cardCheckPattern(seed, off+read, want[:n])
		for b := int64(0); b+cardCheckBlock <= int64(got); b += cardCheckBlock {
			if bytes.Equal(buf[b:b+cardCheckBlock], want[b:b+cardCheckBlock]) {
				res.Intact += cardCheckBlock
			} else {
				lost(off+read+b, cardCheckBlock)
			}
		}
		if err != nil {
			if res.ReadErr == "" {
				res.ReadErr = cardCheckErrText(err)
			}
			whole := int64(got) - int64(got)%cardCheckBlock
			lost(off+read+whole, size-read-whole)
			return
		}
		read += n
		progress(off + read)
	}
}

// cardCheckFakeResultText is step 4's report.
func cardCheckFakeResultText(res cardCheckFillResult, vol cardCheckVolume, mode int) (cardCheckState, string, []string) {
	lines := []string{
		"Planned: " + formatSize(res.Planned),
		"Written: " + formatSize(res.Written),
		"Came back intact: " + formatSize(res.Intact),
	}
	if res.Lost > 0 {
		lines = append(lines, "Lost or changed: "+formatSize(res.Lost))
	}
	if res.WriteErr != "" {
		lines = append(lines, "Writing stopped: "+res.WriteErr)
	}
	if res.ReadErr != "" {
		lines = append(lines, "Reading back: "+res.ReadErr)
	}
	lines = append(lines, "The test files were deleted.")
	if mode == cardCheckFakeQuick && res.Lost == 0 {
		lines = append(lines, "The quick test checks 1 GB. Only the test of all free space can show a card that claims more than it holds.")
	}
	switch {
	case res.Battery:
		return ccStopped, "Stopped: the battery is low", lines
	case res.Lost > 0:
		holds := vol.Total - vol.Free + res.Intact
		lines = append(lines,
			"Some of what was written did not come back: the card is fake (smaller than it claims) or failing.",
			"It holds about "+formatSize(holds)+" of the "+formatSize(vol.Total)+" it claims. Do not keep games or saves on it.")
		return ccBad, formatSize(res.Lost) + " did not come back — fake or failing card", lines
	case res.Stopped:
		return ccStopped, "Stopped after " + formatSize(res.Intact) + " checked", lines
	case res.WriteErr != "" && res.Written < res.Planned:
		if cardCheckIsFull(res.WriteErr) && res.Written > 0 {
			lines = append(lines, "The card said it was full before the planned amount: it holds less free space than it reported.")
			return ccWarn, "Full early · " + formatSize(res.Intact) + " came back intact", lines
		}
		return ccWarn, "Could not write the test files", lines
	}
	return ccOK, "All " + formatSize(res.Intact) + " came back intact", lines
}

// cardCheckIsFull reports a "no space" error text.
func cardCheckIsFull(text string) bool {
	low := strings.ToLower(text)
	return strings.Contains(low, "no space") || strings.Contains(low, "not enough space")
}

// ── Step 5: fsck ─────────────────────────────────────────────────────────

// cardCheckFsckNames are the checkers for a file system kind, best first.
func cardCheckFsckNames(kind string) []string {
	switch kind {
	case "FAT12", "FAT16", "FAT32":
		return []string{"fsck.vfat", "fsck.fat", "dosfsck"}
	case "exFAT":
		return []string{"fsck.exfat"}
	}
	return nil
}

// cardCheckFsckKind is the boot sector's kind, or the mount's type's.
func cardCheckFsckKind(boot cardCheckBoot, fstype string) string {
	if boot.Kind != "" {
		return boot.Kind
	}
	switch cardCheckFSName(fstype) {
	case "FAT32":
		return "FAT32"
	case "exFAT":
		return "exFAT"
	}
	return ""
}

// cardCheckFindTool finds a program on PATH or in the usual system folders.
func cardCheckFindTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range []string{"/sbin", "/usr/sbin", "/bin", "/usr/bin", "/usr/local/sbin", "/usr/local/bin"} {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// cardCheckFsckStep runs the read-only fsck when there is one.
func cardCheckFsckStep(ctx context.Context, vol cardCheckVolume, boot cardCheckBoot) (cardCheckState, string, []string) {
	if vol.Device == "" {
		return ccSkipped, "The partition's device is unknown", nil
	}
	kind := cardCheckFsckKind(boot, vol.FSType)
	names := cardCheckFsckNames(kind)
	if len(names) == 0 {
		return ccSkipped, "No checker for this file system", nil
	}
	tool := ""
	for _, n := range names {
		if tool = cardCheckFindTool(n); tool != "" {
			break
		}
	}
	if tool == "" {
		return ccSkipped, "No fsck program on this system (" + strings.Join(names, ", ") + ")", nil
	}
	cctx, cancel := context.WithTimeout(ctx, cardCheckFsckTimeout)
	defer cancel()
	cmd := hiddenCommandContext(cctx, tool, "-n", vol.Device)
	cmd.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	var out cardCheckCapped
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		return ccSkipped, filepath.Base(tool) + " could not run: " + err.Error(), nil
	}
	lines := []string{"Ran: " + filepath.Base(tool) + " -n " + vol.Device + " (read-only) · exit " + strconv.Itoa(code)}
	for _, l := range cardCheckTail(out.String(), 14) {
		lines = append(lines, "  "+devCheckClip(l, 200))
	}
	lines = append(lines, "The card is in use while it is checked, so some complaints can be false alarms. A check on a computer is the one to trust.")
	switch {
	case cctx.Err() == context.DeadlineExceeded:
		return ccWarn, filepath.Base(tool) + " took too long and was stopped", lines
	case ctx.Err() != nil:
		return ccStopped, "Stopped", lines
	case code == 0:
		return ccOK, filepath.Base(tool) + " found no problems", lines
	case code == 1 || code == 4:
		return ccWarn, filepath.Base(tool) + " found problems — repair on a computer", lines
	}
	return ccWarn, filepath.Base(tool) + " could not finish (exit " + strconv.Itoa(code) + ")", lines
}

// cardCheckCapped keeps the first cardCheckFsckOutput bytes written to it.
type cardCheckCapped struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *cardCheckCapped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := cardCheckFsckOutput - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

func (c *cardCheckCapped) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// cardCheckTail is the last n non-empty lines of text.
func cardCheckTail(text string, n int) []string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// ── The report ───────────────────────────────────────────────────────────

// cardCheckStateWord is a step's state in the report.
func cardCheckStateWord(s cardCheckState) string {
	switch s {
	case ccOK:
		return "OK"
	case ccWarn:
		return "WARNING"
	case ccBad:
		return "PROBLEM"
	case ccSkipped:
		return "SKIPPED"
	case ccStopped:
		return "STOPPED"
	case ccRunning:
		return "RUNNING"
	}
	return "NOT RUN"
}

// cardCheckRepairText is how to repair a card, for the report and the sheet.
const cardCheckRepairText = "JukaHub does not repair the card it runs from. Turn the handheld off, take the card out and put it in a computer:\n" +
	"• Windows: open Command Prompt as administrator and type chkdsk X: /f (X is the card's drive letter).\n" +
	"• Mac: Disk Utility → select the card → First Aid.\n" +
	"• Linux: unmount it, then sudo fsck.vfat -a /dev/sdX1 (FAT32) or sudo fsck.exfat -y /dev/sdX1 (exFAT).\n" +
	"Copy your saves off first. A card with read errors or a failed fake card test should be replaced, not repaired."

// cardCheckReport is the whole run as plain text.
func cardCheckReport(steps []cardCheckStep) string {
	var b strings.Builder
	b.WriteString("JukaHub card check\n==================\n")
	b.WriteString("Result: " + cardCheckVerdict(steps) + "\n\n")
	for _, st := range steps {
		b.WriteString("[" + cardCheckStateWord(st.state) + "] " + st.title)
		if st.summary != "" {
			b.WriteString(" — " + st.summary)
		}
		b.WriteString("\n")
		for _, l := range st.lines {
			b.WriteString("    " + l + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("How to repair\n-------------\n" + strings.ReplaceAll(cardCheckRepairText, "• ", "- ") + "\n")
	return b.String()
}
