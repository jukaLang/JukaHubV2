package main

// voicememo_files.go — Voice Memos' folder: listing, names, rename, delete
// and the recovery of a recording a crash cut off.
//
// A memo is a WAV file in osAppsUserDir("VoiceMemos"); its name is the file
// name without ".wav". The listing runs on a goroutine (a folder of memos on
// a slow card, each header read for its length) and hands the result back to
// the main thread through tbxPost. Names are made safe for the card's FAT
// file system and kept unique with " (2)", " (3)"…; default names count up
// ("Memo 1", "Memo 2"…) from a number kept in voicememos.json, never from the
// clock, which on the handheld can be wrong until network time arrives.
//
// A recording is written to a hidden ".recording-<n>.wav" next to the memos
// and renamed when it stops (voicememo_rec.go). One that is still there when
// the app opens was cut off by a crash or a power cut: its header's sizes are
// fixed from the file's length and it becomes "Recovered memo".

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// vmTempPrefix starts a recording's temporary file name.
	vmTempPrefix = ".recording-"
	// vmListMax bounds one listing.
	vmListMax = 2000
	// vmWAVHeaderLen is the canonical header every memo is written with.
	vmWAVHeaderLen = 44
)

// vmMemo is one memo of the listing.
type vmMemo struct {
	name string // the file name without ".wav"
	file string // the file name in the folder
	size int64
	mod  time.Time
	dur  time.Duration // from the header; 0 when unknown
	wav  vmWAV
	ok   bool // a WAV JukaHub can play
}

// id is the memo's focus id in the list.
func (m vmMemo) id() string { return "m:" + m.file }

// vmWAV is what a WAV header says about its sound.
type vmWAV struct {
	format   int
	channels int
	rate     int
	bits     int
	dataOff  int64
	dataLen  int64
}

// bytesPerSec is the data rate, 0 for a header that makes no sense.
func (w vmWAV) bytesPerSec() int64 {
	if w.channels <= 0 || w.rate <= 0 || w.bits <= 0 {
		return 0
	}
	return int64(w.rate) * int64(w.channels) * int64((w.bits+7)/8)
}

// streamable reports whether playback can feed the file to the output as it
// is: 16-bit PCM, mono or stereo.
func (w vmWAV) streamable() bool {
	return w.format == 1 && w.bits == 16 && (w.channels == 1 || w.channels == 2) && w.rate >= 4000 && w.rate <= 192000
}

var errVMNotWAV = errors.New("not a WAV file")

// vmReadWAVInfo walks a WAV file's chunks for its format and its data. A data
// size of 0 or one past the end of the file (a recording that was cut off)
// counts as "to the end of the file".
func vmReadWAVInfo(f io.ReaderAt, size int64) (vmWAV, error) {
	var w vmWAV
	head := make([]byte, 12)
	if size < 12 {
		return w, errVMNotWAV
	}
	if _, err := f.ReadAt(head, 0); err != nil {
		return w, err
	}
	if string(head[0:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return w, errVMNotWAV
	}
	haveFmt := false
	off := int64(12)
	chunk := make([]byte, 8)
	for i := 0; i < 64 && off+8 <= size; i++ {
		if _, err := f.ReadAt(chunk, off); err != nil {
			return w, err
		}
		id := string(chunk[0:4])
		n := int64(binary.LittleEndian.Uint32(chunk[4:8]))
		body := off + 8
		switch id {
		case "fmt ":
			if n < 16 || body+16 > size {
				return w, errVMNotWAV
			}
			fm := make([]byte, 16)
			if _, err := f.ReadAt(fm, body); err != nil {
				return w, err
			}
			w.format = int(binary.LittleEndian.Uint16(fm[0:]))
			w.channels = int(binary.LittleEndian.Uint16(fm[2:]))
			w.rate = int(binary.LittleEndian.Uint32(fm[4:]))
			w.bits = int(binary.LittleEndian.Uint16(fm[14:]))
			if w.format == 0xFFFE && n >= 40 && body+26 <= size {
				// WAVE_FORMAT_EXTENSIBLE: the real format leads its GUID.
				sub := make([]byte, 2)
				if _, err := f.ReadAt(sub, body+24); err == nil {
					w.format = int(binary.LittleEndian.Uint16(sub))
				}
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return w, errVMNotWAV
			}
			w.dataOff = body
			w.dataLen = size - body
			if n > 0 && n != 0xFFFFFFFF && body+n <= size {
				w.dataLen = n
			}
			return w, nil
		}
		off = body + n + n&1
	}
	return w, errVMNotWAV
}

// vmStatMemo reads one memo's size and header.
func vmStatMemo(dir, file string) (vmMemo, bool) {
	path := filepath.Join(dir, file)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return vmMemo{}, false
	}
	m := vmMemo{
		name: strings.TrimSuffix(file, filepath.Ext(file)),
		file: file,
		size: info.Size(),
		mod:  info.ModTime(),
	}
	f, err := os.Open(path)
	if err != nil {
		return m, true
	}
	defer f.Close()
	if w, err := vmReadWAVInfo(f, m.size); err == nil {
		m.wav = w
		if bps := w.bytesPerSec(); bps > 0 {
			m.dur = time.Duration(w.dataLen * int64(time.Second) / bps)
			m.ok = true
		}
	}
	return m, true
}

// vmIsMemoFile reports whether a folder entry is a memo: a visible .wav.
func vmIsMemoFile(name string) bool {
	return !strings.HasPrefix(name, ".") && strings.EqualFold(filepath.Ext(name), ".wav")
}

// vmListDir lists the memos in dir, newest first. Off the main thread.
func vmListDir(dir string) []vmMemo {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]vmMemo, 0, len(entries))
	for _, e := range entries {
		if len(out) >= vmListMax {
			break
		}
		if e.IsDir() || !vmIsMemoFile(e.Name()) {
			continue
		}
		if m, ok := vmStatMemo(dir, e.Name()); ok {
			out = append(out, m)
		}
	}
	vmSortMemos(out)
	return out
}

// vmSortMemos orders memos newest first, then by name.
func vmSortMemos(list []vmMemo) {
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].mod.Equal(list[j].mod) {
			return list[i].mod.After(list[j].mod)
		}
		return strings.ToLower(list[i].name) < strings.ToLower(list[j].name)
	})
}

// vmScanAsync lists the folder on a goroutine; fixTemps first recovers a
// recording a crash left behind. The newest scan wins.
func vmScanAsync(fixTemps bool) {
	vm.scanGen++
	gen := vm.scanGen
	dir := vm.dir
	vm.scanBusy = true
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("voicememo", "listing the memos failed: %v", r)
			}
		}()
		recovered := 0
		if fixTemps {
			recovered = vmRecoverTemps(dir)
		}
		list := vmListDir(dir)
		tbxPost(func() {
			if recovered > 0 {
				msg := "Recovered a memo that was cut off"
				if recovered > 1 {
					msg = "Recovered " + strconv.Itoa(recovered) + " memos that were cut off"
				}
				ShowToast(msg, ToastKindInfo)
			}
			if gen != vm.scanGen {
				return
			}
			vm.memos = list
			vm.loaded = true
			vm.scanBusy = false
			vmHubCount.Store(int32(len(list)))
		})
	}()
}

// vmActiveTemp is the temporary file a recording is writing now: recovery
// must never touch it. Set before the file is created, cleared after it is
// renamed; the recovery holds the lock from its check to its rename.
var vmActiveTemp struct {
	mu   sync.Mutex
	path string
}

func vmSetActiveTemp(path string) {
	vmActiveTemp.mu.Lock()
	vmActiveTemp.path = path
	vmActiveTemp.mu.Unlock()
}

// vmRecoverTemps turns the recordings a crash left in dir into memos named
// "Recovered memo" and reports how many it saved. One too short to hold any
// sound is removed. Off the main thread.
func vmRecoverTemps(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	saved := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, vmTempPrefix) || !strings.EqualFold(filepath.Ext(name), ".wav") {
			continue
		}
		path := filepath.Join(dir, name)
		vmActiveTemp.mu.Lock()
		if vmActiveTemp.path != "" && filepath.Clean(vmActiveTemp.path) == filepath.Clean(path) {
			vmActiveTemp.mu.Unlock()
			continue
		}
		ok := vmRecoverOne(dir, path)
		vmActiveTemp.mu.Unlock()
		if ok {
			saved++
		}
	}
	return saved
}

// vmRecoverOne fixes one temporary recording's header and renames it.
func vmRecoverOne(dir, path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.Size() <= vmWAVHeaderLen+vmBytesPerSec/10 {
		// Less than a tenth of a second: nothing worth keeping.
		_ = os.Remove(path)
		logInfo("voicememo", "removed an empty leftover recording %s", filepath.Base(path))
		return false
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		logWarn("voicememo", "cannot open the leftover recording %s: %v", filepath.Base(path), err)
		return false
	}
	data := info.Size() - vmWAVHeaderLen
	data -= data % 2
	werr := vmWriteWAVSizes(f, data)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		logWarn("voicememo", "cannot fix the leftover recording %s: %v %v", filepath.Base(path), werr, cerr)
		return false
	}
	file := vmUniqueFile(dir, "Recovered memo", "")
	if err := os.Rename(path, filepath.Join(dir, file)); err != nil {
		logWarn("voicememo", "cannot rename the leftover recording %s: %v", filepath.Base(path), err)
		return false
	}
	logInfo("voicememo", "recovered a cut-off recording as %q (%d bytes of sound)", file, data)
	return true
}

// vmWAVHeader is the canonical 44-byte header for dataLen bytes of memo
// sound (16 kHz, mono, 16-bit). Pure.
func vmWAVHeader(dataLen int64) []byte {
	if dataLen < 0 {
		dataLen = 0
	}
	if dataLen > 0xFFFFFFFF-36 {
		dataLen = 0xFFFFFFFF - 36
	}
	h := vkEncodeWAV(nil, vmRate)
	binary.LittleEndian.PutUint32(h[4:], uint32(36+dataLen))
	binary.LittleEndian.PutUint32(h[40:], uint32(dataLen))
	return h
}

// vmWriteWAVSizes rewrites the RIFF and data sizes of a memo's header in
// place.
func vmWriteWAVSizes(f *os.File, dataLen int64) error {
	h := vmWAVHeader(dataLen)
	if _, err := f.WriteAt(h[4:8], 4); err != nil {
		return err
	}
	_, err := f.WriteAt(h[40:44], 40)
	return err
}

// ── Names ────────────────────────────────────────────────────────────────

// vmWindowsReserved are names Windows will not give a file, whatever the
// extension; a memo copied to a PC must keep its name.
var vmWindowsReserved = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// vmCleanName makes a typed name safe as a file name on the card: the
// characters FAT refuses (\ / : * ? " < > | and control characters) become
// spaces, runs of spaces become one, a leading dot (a hidden file) and the
// trailing dots and spaces FAT drops go, ".wav" typed at the end is dropped,
// and it is cut to vmNameMaxRunes. "" when nothing is left. Pure.
func vmCleanName(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r == utf8.RuneError, r < 0x20, r == 0x7f, strings.ContainsRune(`\/:*?"<>|`, r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.Join(strings.Fields(b.String()), " ")
	if strings.EqualFold(filepath.Ext(s), ".wav") {
		s = strings.TrimSpace(s[:len(s)-len(".wav")])
	}
	s = strings.TrimLeft(s, ". ")
	if utf8.RuneCountInString(s) > vmNameMaxRunes {
		s = string([]rune(s)[:vmNameMaxRunes])
	}
	s = strings.TrimRight(s, ". ")
	if vmWindowsReserved[strings.ToLower(s)] {
		s += " memo"
	}
	return s
}

// vmUniqueFile is "<name>.wav" in dir, or "<name> (2).wav", "(3)"… when that
// is taken. keep is a file that may be reused (the memo being renamed, whose
// new name differs only in case).
func vmUniqueFile(dir, name, keep string) string {
	for i := 1; i < 1000; i++ {
		cand := name
		if i > 1 {
			cand = name + " (" + strconv.Itoa(i) + ")"
		}
		file := cand + ".wav"
		if keep != "" && strings.EqualFold(file, keep) {
			return file
		}
		if _, err := os.Lstat(filepath.Join(dir, file)); errors.Is(err, os.ErrNotExist) {
			return file
		}
	}
	return name + " " + strconv.FormatInt(time.Now().UnixNano(), 36) + ".wav"
}

// vmMemoNumber is n for a name "Memo n", or 0. Pure.
func vmMemoNumber(name string) int {
	rest, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(name)), "memo ")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// vmNextNumber is the number the next default name takes: past the stored
// counter and past every "Memo n" listed.
func vmNextNumber() int {
	n := vm.saved.Next
	if n < 1 {
		n = 1
	}
	for _, m := range vm.memos {
		if k := vmMemoNumber(m.name); k >= n {
			n = k + 1
		}
	}
	return n
}

// ── Lookups ──────────────────────────────────────────────────────────────

// vmMemoByID finds a listed memo by its focus id.
func vmMemoByID(id string) (vmMemo, bool) {
	for _, m := range vm.memos {
		if m.id() == id {
			return m, true
		}
	}
	return vmMemo{}, false
}

// vmFocusedMemo is the memo under the focus, if the focus is on one.
func vmFocusedMemo() (vmMemo, bool) {
	return vmMemoByID(vm.nav.focus)
}

// ── Rename and delete ────────────────────────────────────────────────────

// vmAskRename opens the keyboard on a memo's name.
func vmAskRename(m vmMemo) {
	PlayActivateSound()
	file := m.file
	osPromptOpen("Rename memo", "Memo name", "Rename", m.name, vmNameMaxRunes, func(text string) {
		vmRename(file, text)
	})
}

// vmRename gives the memo in file a new name. An empty or unchanged name
// changes nothing; a name in use gets " (2)".
func vmRename(file, typed string) {
	name := vmCleanName(typed)
	if name == "" {
		ShowToast("The name was empty — the memo keeps its name", ToastKindInfo)
		return
	}
	if name+".wav" == file {
		return
	}
	// Windows will not rename a file that is open.
	vmPlayRelease(file)
	dir := vm.dir
	newFile := vmUniqueFile(dir, name, file)
	if err := os.Rename(filepath.Join(dir, file), filepath.Join(dir, newFile)); err != nil {
		logWarn("voicememo", "rename %q to %q: %v", file, newFile, err)
		ShowToast("Couldn't rename the memo", ToastKindError)
		PlayErrorSound()
		return
	}
	for i := range vm.memos {
		if vm.memos[i].file == file {
			vm.memos[i].file = newFile
			vm.memos[i].name = strings.TrimSuffix(newFile, filepath.Ext(newFile))
		}
	}
	if vm.nav.focus == "m:"+file {
		vm.nav.focus = "m:" + newFile
	}
	PlaySuccessSound()
	ShowToast("Renamed to “"+strings.TrimSuffix(newFile, ".wav")+"”", ToastKindSuccess)
	vmScanAsync(false)
}

// vmAskDelete asks before deleting a memo.
func vmAskDelete(c *Config, m vmMemo) {
	PlayActivateSound()
	file, name := m.file, m.name
	tbxOpenSheet(c, tbxSheetSpec{
		title:    "Delete this memo?",
		subtitle: "“" + name + "”",
		icon:     "trash",
		body:     "The recording is removed from the card for good (" + vmMemoDetail(m) + ").",
		buttons: []tbxSheetButton{
			{label: "Keep"},
			{label: "Delete", primary: true, run: func() { vmDelete(file, name) }},
		},
	})
}

// vmDelete removes a memo and keeps the focus on the row that takes its
// place.
func vmDelete(file, name string) {
	vmPlayRelease(file)
	if err := os.Remove(filepath.Join(vm.dir, file)); err != nil && !errors.Is(err, os.ErrNotExist) {
		logWarn("voicememo", "delete %q: %v", file, err)
		ShowToast("Couldn't delete the memo", ToastKindError)
		PlayErrorSound()
		return
	}
	at := -1
	for i, m := range vm.memos {
		if m.file == file {
			at = i
			break
		}
	}
	if at >= 0 {
		vm.memos = append(vm.memos[:at:at], vm.memos[at+1:]...)
		switch {
		case len(vm.memos) == 0:
			vm.nav.focus = vmIDRecord
		case at < len(vm.memos):
			vm.nav.focus = vm.memos[at].id()
		default:
			vm.nav.focus = vm.memos[len(vm.memos)-1].id()
		}
		vmHubCount.Store(int32(len(vm.memos)))
	}
	ShowToast("Deleted “"+name+"”", ToastKindInfo)
	vmScanAsync(false)
}

// vmDisplayDir is the memos' folder as the page shows it: under the home
// folder on a PC it starts with "~", so a screenshot or a shared screen does
// not carry the account name.
func vmDisplayDir() string {
	dir := vm.dir
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, dir); err == nil && filepath.IsLocal(rel) {
			return filepath.Join("~", rel)
		}
	}
	return dir
}

// vmMemoDetail is a memo's "1:23 · 2.6 MB" line.
func vmMemoDetail(m vmMemo) string {
	size := formatSize(m.size)
	if !m.ok {
		return "Not a recording JukaHub can play · " + size
	}
	return vmClock(m.dur) + " · " + size
}

// vmClock writes a duration as m:ss, or h:mm:ss from an hour. Pure.
func vmClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int64(d / time.Second)
	h, m, sec := s/3600, (s/60)%60, s%60
	pad := func(n int64) string {
		if n < 10 {
			return "0" + strconv.FormatInt(n, 10)
		}
		return strconv.FormatInt(n, 10)
	}
	if h > 0 {
		return strconv.FormatInt(h, 10) + ":" + pad(m) + ":" + pad(sec)
	}
	return strconv.FormatInt(m, 10) + ":" + pad(sec)
}
