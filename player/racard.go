package main

// racard.go — the RetroArch a handheld's games start, and whether it can.
//
// JH-X6HRVE (2026-11-25, a Smart Pro with the stock firmware's launch scripts;
// CrossMix-OS's are the same): every game stopped at once with "launch.sh:
// line 7: /mnt/SDCARD/RetroArch/ra64.trimui: not found". JukaHub had seen that
// binary, and the retroarch.cfg beside it, when it started; a minute later
// both were gone (the same start logged "mkdir …: input/output error": a card
// with errors). And JukaHub's install of TrimUI's RetroArch (raget.go) never
// ran — the report said "not tried this session" — because its question "is
// RetroArch missing?" went to netplayRetroArchPath, which also answers with
// any "retroarch" on PATH, in /usr/bin or beside JukaHub, a wrapper script as
// readily as a program: something that was not the RetroArch the games call
// said "present", and the start-up's install, a game's, "Get more cores" and
// the RetroArch tile all stood down.
//
// raCardCheck asks what the games ask. The launch scripts of the stock
// firmware and of CrossMix-OS (Emus/<console>/launch.sh) all start one binary
// — RA_DIR=/mnt/SDCARD/RetroArch … $RA_DIR/ra64.trimui — so the check reads
// them for the RetroArch they call (raScriptCalls, with rafind.go's reading of
// their variables) and looks at that file: there, and a program this handheld
// can start (raBinaryProblem: an ELF for its ARM processor whose loader is on
// the system). When no script names a RetroArch, the card's RetroArch folder's
// ra64.trimui, where JukaHub installs, is the one looked at, and any RetroArch
// program the wider search finds (rafind.go) does instead. RetroArch is
// missing — and JukaHub installs TrimUI's — when the one the games need is not
// such a program; one that is there but cannot start (another processor's,
// its loader absent, empty) is broken, and the install keeps it as
// ra64.trimui.bak.
//
// A RetroArch JukaHub saw earlier this session that is gone now (raCardSaw) is
// said so in the log, a game's diagnosis and the diagnostics, with Card check:
// a card that loses files has errors, or claims more than it holds.
//
// One answer is kept for raCardTTL (the start-up's watch, a game's diagnosis
// and a core install all ask), the scripts' for raCardScriptsTTL, and the last
// one for the diagnostics, which must not read the card on the thread that
// draws. The handheld only (raGetSupported).

import (
	"bytes"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// raCardTTL is how long one answer is kept; raCardScriptsTTL how long the
	// launch scripts' (they change when a firmware is put on the card).
	raCardTTL        = 15 * time.Second
	raCardScriptsTTL = 10 * time.Minute
	// raCardScriptsRead bounds the launch scripts one look reads, and
	// raCardScriptsEnough ends it once that many name one RetroArch.
	raCardScriptsRead   = 80
	raCardScriptsEnough = 8
)

// raCardState is one look at the card's RetroArch.
type raCardState struct {
	// Target is the card's RetroArch folder's ra64.trimui, where JukaHub
	// installs ("" with no card); Calls is the RetroArch the launch scripts
	// call ("" when none names one) and Scripts how many of them do.
	Target  string
	Calls   string
	Scripts int
	// Need is the RetroArch the games need (Calls, else Target) and Problem
	// why it cannot start ("" when it can).
	Need    string
	Problem string
	// Found is a RetroArch program that does instead, when the one the games
	// need is not the card folder's.
	Found string
	// Missing: JukaHub installs RetroArch into Target. Broken: Target is there
	// but cannot start, and the install keeps it as ra64.trimui.bak.
	Missing bool
	Broken  bool
	// Lost is when JukaHub last saw the RetroArch that is missing now (zero
	// when it did not this session).
	Lost time.Time
	at   time.Time
}

// raCard keeps the answers: the last look (gen counts raCardForget calls, so
// a look that began before one does not store its answer), the launch
// scripts' answer, and when each RetroArch binary was last seen (raCardSaw).
var raCard struct {
	mu      sync.Mutex
	st      raCardState
	ok      bool
	gen     uint64
	calls   string
	scripts int
	callsAt time.Time
	saw     map[string]time.Time
}

// raCardCheck is the card's RetroArch as the games need it (see the top of the
// file), looked at again after raCardTTL or raCardForget. It reads the card:
// never on the main thread. Empty where JukaHub installs no RetroArch.
func raCardCheck() raCardState {
	if !raGetSupported() {
		return raCardState{}
	}
	raCard.mu.Lock()
	if raCard.ok && time.Since(raCard.st.at) < raCardTTL {
		st := raCard.st
		raCard.mu.Unlock()
		return st
	}
	gen := raCard.gen
	raCard.mu.Unlock()

	target := ""
	if dir := raGetTargetDir(); dir != "" {
		target = filepath.Join(dir, raGetBinaryName)
	}
	calls, scripts := raCardScriptCalls(raLocateCardRoots())
	st := raCardJudge(target, calls, scripts, raCardElsewhere)
	switch {
	case st.Problem == "":
		raCardSaw(st.Need)
	case st.Missing:
		st.Lost = raCardSawAt(st.Need)
	}
	st.at = time.Now()

	raCard.mu.Lock()
	if raCard.gen == gen {
		raCard.st, raCard.ok = st, true
	}
	raCard.mu.Unlock()
	raCardNote(st)
	return st
}

// raCardJudge decides from what the launch scripts call (calls, named by
// scripts of them): the RetroArch the games need, whether it can start, and
// whether JukaHub installs one into target. elsewhere is the wider search
// (raCardElsewhere), asked only when the games' RetroArch is not the card
// folder's.
func raCardJudge(target, calls string, scripts int, elsewhere func() string) raCardState {
	st := raCardState{Target: target, Calls: calls, Scripts: scripts, Need: calls}
	if st.Need == "" {
		st.Need = target
	}
	if st.Need == "" {
		st.Problem = "no SD card to look on"
		return st
	}
	// The binary the scripts call may be a wrapper of the user's, so a script
	// counts there; anywhere else a RetroArch must be a program.
	problem, present := raBinaryProblem(st.Need, calls != "")
	st.Problem = problem
	if problem == "" || target == "" {
		return st
	}
	if calls != "" && raSamePath(calls, target) {
		// The games call the card folder's RetroArch, and it cannot start.
		st.Missing, st.Broken = true, present
		return st
	}
	// No script names a RetroArch, or the one they name is somewhere JukaHub
	// does not install to: the card folder's, or any RetroArch program the
	// search finds, does; with none, the card's RetroArch folder gets one.
	tProblem, tPresent := problem, present
	if calls != "" {
		tProblem, tPresent = raBinaryProblem(target, false)
	}
	if tProblem == "" {
		st.Found = target
		return st
	}
	if elsewhere != nil {
		if bin := elsewhere(); bin != "" && !raSamePath(bin, target) {
			st.Found = bin
			return st
		}
	}
	st.Missing, st.Broken = true, tPresent
	return st
}

// raCardElsewhere is a RetroArch program the wider search finds (rafind.go),
// "" when it finds none that can start.
func raCardElsewhere() string {
	loc := raLocateDevice()
	if loc.Bin == "" {
		return ""
	}
	if problem, _ := raBinaryProblem(loc.Bin, false); problem != "" {
		return ""
	}
	return loc.Bin
}

// raCardCached is the last look without looking again (Diagnostics); ok is
// false before any.
func raCardCached() (raCardState, bool) {
	raCard.mu.Lock()
	defer raCard.mu.Unlock()
	return raCard.st, raCard.ok
}

// raCardForget drops the kept answers, so the next look reads the card again:
// a launch script just failed, RetroArch was just installed.
func raCardForget() {
	raCard.mu.Lock()
	raCard.ok = false
	raCard.callsAt = time.Time{}
	raCard.gen++
	raCard.mu.Unlock()
}

// raCardSaw notes that the RetroArch binary bin was there just now — the
// catalog finds it at every build (deviceRetroArchLocate) — so one that goes
// missing later in the session is told as lost.
func raCardSaw(bin string) {
	if strings.TrimSpace(bin) == "" {
		return
	}
	key := strings.ToLower(filepath.Clean(bin))
	raCard.mu.Lock()
	if raCard.saw == nil || len(raCard.saw) > 16 {
		raCard.saw = map[string]time.Time{}
	}
	raCard.saw[key] = time.Now()
	raCard.mu.Unlock()
}

// raCardSawAt is when bin was last seen there, zero when it was not this
// session.
func raCardSawAt(bin string) time.Time {
	raCard.mu.Lock()
	defer raCard.mu.Unlock()
	return raCard.saw[strings.ToLower(filepath.Clean(bin))]
}

// raCardNote logs a look's answer once per change.
func raCardNote(st raCardState) {
	key := fmt.Sprintf("%v|%v|%s|%s|%s", st.Missing, st.Broken, st.Need, st.Problem, st.Found)
	if !raPathChanged("card", key) {
		return
	}
	calls := "no launch script names a RetroArch"
	if st.Calls != "" {
		calls = fmt.Sprintf("%d launch scripts call it", st.Scripts)
	}
	switch {
	case st.Problem == "":
		logInfo("emulators", "RetroArch for the games: %s (%s), and it can start", st.Need, calls)
	case st.Found != "":
		logInfo("emulators", "RetroArch for the games: %s is %s (%s); %s does instead", st.Need, st.Problem, calls, st.Found)
	case st.Missing:
		lost := ""
		if !st.Lost.IsZero() {
			lost = "; it was there at " + st.Lost.Format("15:04:05") +
				" — a card that loses files has errors or is not as large as it claims (Tools → System → Card check)"
		}
		logWarn("emulators", "RetroArch for the games: %s is %s (%s)%s; JukaHub installs TrimUI's into %s",
			st.Need, st.Problem, calls, lost, filepath.Dir(st.Target))
	default:
		logWarn("emulators", "RetroArch for the games: %s is %s (%s)", st.Need, st.Problem, calls)
	}
}

// raCardDiagnostics is the diagnostics' line on the RetroArch the games call,
// as the last look found it: program names, not folders ("" off the
// handheld).
func raCardDiagnostics() string {
	if !raGetSupported() {
		return ""
	}
	st, ok := raCardCached()
	if !ok {
		return "not looked at yet"
	}
	where := func(p string) string {
		return filepath.Base(p) + " in " + raCardFolderWords(p, st.Target)
	}
	var parts []string
	switch {
	case st.Calls != "":
		parts = append(parts, "the launch scripts call "+where(st.Calls)+" ("+pluralizeCount(st.Scripts, "script", "scripts")+")")
	case st.Need != "":
		parts = append(parts, "no launch script names a RetroArch; "+where(st.Need))
	}
	switch {
	case st.Problem == "":
		parts = append(parts, "there, and it can start")
	case st.Found != "":
		parts = append(parts, st.Problem+"; "+where(st.Found)+" does instead")
	default:
		parts = append(parts, st.Problem)
	}
	if !st.Lost.IsZero() {
		parts = append(parts, "it was there at "+st.Lost.Format("15:04")+" (run Card check)")
	}
	if st.Missing {
		parts = append(parts, "JukaHub installs TrimUI's")
	}
	return strings.Join(parts, " · ")
}

// raCardFolderWords names the folder p is in, for a report without its path:
// the card's RetroArch folder, the firmware, or another folder by its name.
func raCardFolderWords(p, target string) string {
	dir := filepath.Dir(p)
	slash := filepath.ToSlash(dir)
	switch {
	case target != "" && raSamePath(dir, filepath.Dir(target)):
		return "the card's RetroArch folder"
	case strings.HasPrefix(slash, "/usr/"), strings.HasPrefix(slash, "/opt/"):
		return "the firmware"
	}
	return "a folder named " + filepath.Base(dir)
}

// ── The launch scripts ───────────────────────────────────────────────────────

// raCardScriptCalls is raScriptCalls, kept for raCardScriptsTTL.
func raCardScriptCalls(cards []string) (string, int) {
	raCard.mu.Lock()
	if !raCard.callsAt.IsZero() && time.Since(raCard.callsAt) < raCardScriptsTTL {
		calls, n := raCard.calls, raCard.scripts
		raCard.mu.Unlock()
		return calls, n
	}
	raCard.mu.Unlock()
	calls, n := raScriptCalls(cards)
	raCard.mu.Lock()
	raCard.calls, raCard.scripts, raCard.callsAt = calls, n, time.Now()
	raCard.mu.Unlock()
	return calls, n
}

// raScriptCalls is the RetroArch binary the card's launch scripts call: the
// path most of the scripts read name (each script counted once), and how many
// name it; "" when none does. It reads at most raCardScriptsRead scripts
// (raLaunchScripts: every emulator folder's, launch.sh first, then CrossMix's
// shared ones) and stops once raCardScriptsEnough agree.
func raScriptCalls(cards []string) (string, int) {
	counts := map[string]int{}
	first := map[string]string{}
	var order []string
	read := 0
	for _, script := range raLaunchScripts(cards) {
		if read >= raCardScriptsRead {
			break
		}
		read++
		f, err := os.Open(script)
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(f, raLocateScriptMax))
		_ = f.Close()
		enough := false
		for _, p := range raScriptCallsText(string(data), filepath.Dir(script)) {
			key := strings.ToLower(p)
			if counts[key] == 0 {
				first[key] = p
				order = append(order, key)
			}
			counts[key]++
			enough = enough || counts[key] >= raCardScriptsEnough
		}
		if enough {
			break
		}
	}
	best, n := "", 0
	for _, key := range order {
		if counts[key] > n {
			best, n = first[key], counts[key]
		}
	}
	return best, n
}

// raScriptCdRe is a cd in a script and the folder it goes to.
var raScriptCdRe = regexp.MustCompile(`(?m)(?:^|[;&|(]|\s)cd[ \t]+["']?([^\s;&|"'()]+)`)

// raScriptCallsText is every RetroArch binary a script's text calls by a path
// — a TrimUI build (ra64.*, ra32.*) or a binary named retroarch, retroarch64
// or retroarch32 in lower case (TrimUI's folder is "RetroArch") — with its
// $VARS filled in from the script's own assignments (rafind.go) and a relative
// one taken from the last folder the script went to (cd), else its own. A name
// that is part of a longer one (ra64.trimui.bak, RetroArch/…, ra64.trimui_PSP.sh),
// a bare name (killall ra64.trimui, RA_BIN=ra64.trimui), one whose variables
// the script does not set, and a folder are left out.
func raScriptCallsText(text, scriptDir string) []string {
	text, vars := raScriptPrepare(text, scriptDir)
	slashDir := filepath.ToSlash(scriptDir)
	cds := raScriptCdRe.FindAllStringSubmatchIndex(text, -1)
	var out []string
	seen := map[string]bool{}
	for _, loc := range raScriptRefRe.FindAllStringIndex(text, -1) {
		raw := text[loc[0]:loc[1]]
		if loc[1] < len(text) && raScriptNamePart(text[loc[1]]) {
			continue
		}
		if !strings.Contains(raw, "/") || !raScriptCallName(path.Base(raw)) {
			continue
		}
		p, ok := raExpand(raw, vars)
		if !ok {
			continue
		}
		if !strings.HasPrefix(p, "/") {
			base := slashDir
			for _, cd := range cds {
				if cd[0] >= loc[0] {
					break
				}
				if dir, ok := raExpand(text[cd[2]:cd[3]], vars); ok && strings.HasPrefix(dir, "/") {
					base = dir
				}
			}
			p = base + "/" + p
		}
		p = filepath.Clean(filepath.FromSlash(p))
		if dirExists(p) {
			continue
		}
		if key := strings.ToLower(p); !seen[key] {
			seen[key] = true
			out = append(out, p)
		}
	}
	return out
}

// raScriptNamePart reports a byte that would make a name longer: the name
// matched before it is part of another one.
func raScriptNamePart(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("._-/+~${}", c) >= 0
}

// raScriptCallName reports a file name a launch script starts RetroArch by.
func raScriptCallName(base string) bool {
	lower := strings.ToLower(base)
	if strings.HasPrefix(lower, "ra64.") || strings.HasPrefix(lower, "ra32.") {
		return true
	}
	switch base {
	case "retroarch", "retroarch64", "retroarch32":
		return true
	}
	return false
}

// ── Can it start ─────────────────────────────────────────────────────────────

// raBinaryMemo keeps raBinaryInspect's answers by file, size and time.
var raBinaryMemo struct {
	mu sync.Mutex
	m  map[string][2]string // path|scriptOK → {stamp, problem}
}

// raBinaryProblem says why the file at p cannot be the RetroArch a game starts
// — "" when it can: an ELF program for this handheld's ARM processor whose
// loader is on the system, or (scriptOK) a script. present reports a file
// that is there, so a broken one is told from a missing one.
func raBinaryProblem(p string, scriptOK bool) (problem string, present bool) {
	if strings.TrimSpace(p) == "" {
		return "no file named", false
	}
	st, err := os.Stat(p)
	switch {
	case err != nil && errors.Is(err, os.ErrNotExist):
		return "missing", false
	case err != nil:
		return "unreadable (" + raCardErrWords(err) + ")", false
	case st.IsDir():
		return "a folder, not a program", true
	case !st.Mode().IsRegular():
		return "not a regular file", true
	case st.Size() == 0:
		return "empty (0 bytes)", true
	}
	key := p + "|" + strconv.FormatBool(scriptOK)
	stamp := coreFixFileStamp(st)
	raBinaryMemo.mu.Lock()
	hit, ok := raBinaryMemo.m[key]
	raBinaryMemo.mu.Unlock()
	if ok && hit[0] == stamp {
		return hit[1], true
	}
	problem = raBinaryInspect(p, scriptOK)
	raBinaryMemo.mu.Lock()
	if raBinaryMemo.m == nil || len(raBinaryMemo.m) > 64 {
		raBinaryMemo.m = map[string][2]string{}
	}
	raBinaryMemo.m[key] = [2]string{stamp, problem}
	raBinaryMemo.mu.Unlock()
	return problem, true
}

// raBinaryInspect reads the file for raBinaryProblem: its first bytes, and for
// an ELF its processor and the loader it names.
func raBinaryInspect(p string, scriptOK bool) string {
	f, err := os.Open(p)
	if err != nil {
		return "unreadable (" + raCardErrWords(err) + ")"
	}
	head := make([]byte, 4)
	n, err := io.ReadFull(f, head)
	_ = f.Close()
	if n >= 2 && head[0] == '#' && head[1] == '!' {
		if scriptOK {
			return ""
		}
		return "a script, not a program"
	}
	if n < 4 || !bytes.Equal(head, []byte("\x7fELF")) {
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return "unreadable (" + raCardErrWords(err) + ")"
		}
		return "not a program (no ELF header)"
	}
	ef, err := elf.Open(p)
	if err != nil {
		return "a damaged program (" + firstLine(err.Error()) + ")"
	}
	defer ef.Close()
	if ef.Machine != elf.EM_AARCH64 && ef.Machine != elf.EM_ARM {
		return "a program for " + elfArchName(ef.Class, ef.Machine) + ", not for this handheld's ARM processor"
	}
	for _, prog := range ef.Progs {
		if prog.Type != elf.PT_INTERP {
			continue
		}
		if prog.Filesz == 0 || prog.Filesz > 4096 {
			return "a damaged program (the name of its loader)"
		}
		buf := make([]byte, prog.Filesz)
		if _, err := prog.ReadAt(buf, 0); err != nil {
			return "a damaged program (the name of its loader cannot be read)"
		}
		interp := strings.TrimRight(string(buf), "\x00")
		if _, err := os.Stat(interp); filepath.IsAbs(interp) && err != nil {
			return "a " + elfArchName(ef.Class, ef.Machine) + " program whose loader " + interp + " this handheld does not have"
		}
		break
	}
	return ""
}

// raCardErrWords is a file error in a few words, a card's input/output error
// said for what it is.
func raCardErrWords(err error) string {
	text := err.Error()
	var pe *os.PathError
	if errors.As(err, &pe) {
		text = pe.Err.Error()
	}
	if strings.Contains(strings.ToLower(text), "input/output error") {
		return "input/output error: the card has errors"
	}
	return text
}

// raSamePath reports two paths naming one file the way a FAT card compares
// names (without regard to case), through linked folders (/mnt/sdcard).
func raSamePath(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" || !strings.EqualFold(filepath.Base(a), filepath.Base(b)) {
		return false
	}
	da, db := filepath.Clean(filepath.Dir(a)), filepath.Clean(filepath.Dir(b))
	if strings.EqualFold(da, db) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(da)
	rb, errB := filepath.EvalSymlinks(db)
	return errA == nil && errB == nil && strings.EqualFold(ra, rb)
}
