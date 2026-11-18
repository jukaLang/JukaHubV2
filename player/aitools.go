package main

// aitools.go — the tools Juka AI can use on the device.
//
// Testers said it plainly: an assistant that can only talk is not worth a
// button. Juka now has a small set of tools (aitools_*.go) the model may call
// through the agent loop (aiagent.go): look at the device's state, its logs,
// a game that will not start, the card's files and the settings — and, with
// the user's OK on a card in the chat, change a setting, move files to the
// trash or stop a process.
//
// Every tool is one entry here: a name, what it is for (the model reads it),
// its JSON-schema parameters, a risk class and a handler. The risk classes:
//
//   - read: runs without asking (it only looks);
//   - change: pauses on a confirmation card in the chat (A Allow, B Deny,
//     Y Allow for this conversation);
//   - destructive: always asks, even after "Allow for this conversation"
//     (a permanent delete, stopping a process, a terminal command).
//
// A handler never applies a change itself: it validates the arguments and
// returns the one-line question for the card plus the closure that applies
// it. Results are plain text, secrets scrubbed (aiRedact), capped at
// aiResultMax so a tool cannot blow up the request. File tools only reach the
// SD card, the ROM folders and JukaHub's data (aiFileRoots); never a system
// path. docs/AI_TOOLS.md is the user-facing description.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// aiRisk is what running a tool can do to the device.
type aiRisk int

const (
	aiRiskRead aiRisk = iota
	aiRiskChange
	aiRiskDestructive
)

// aiResultMax caps one tool result (bytes) before it goes to the model.
const aiResultMax = 4 << 10

// The three switches in Settings → Voice & AI (aitools_settings.go).
const (
	aiToolsSetting  = "ai_device_tools"   // "Juka can use device tools", default on
	aiChangeSetting = "ai_device_changes" // "Let Juka change settings and files", default on
	aiShellSetting  = "ai_terminal"       // "Let Juka run terminal commands", default off
)

func aiToolsOn() bool {
	if appConfig == nil {
		return true
	}
	return customBool(appConfig.Variables.Custom, aiToolsSetting, true)
}

func aiChangesOn() bool {
	if appConfig == nil {
		return true
	}
	return aiToolsOn() && customBool(appConfig.Variables.Custom, aiChangeSetting, true)
}

func aiShellOn() bool {
	if appConfig == nil {
		return false
	}
	return aiChangesOn() && customBool(appConfig.Variables.Custom, aiShellSetting, false)
}

// aiArgs are a tool call's decoded arguments.
type aiArgs map[string]any

func (a aiArgs) str(k string) string {
	switch v := a[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

// num reads an integer argument, def when missing, clamped to [lo, hi].
func (a aiArgs) num(k string, def, lo, hi int) int {
	n := def
	switch v := a[k].(type) {
	case float64:
		n = int(v)
	case string:
		if p, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			n = p
		}
	}
	if n < lo {
		n = lo
	}
	if n > hi {
		n = hi
	}
	return n
}

func (a aiArgs) flag(k string) bool {
	switch v := a[k].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	}
	return false
}

// list reads a string-list argument; a single string is a list of one.
func (a aiArgs) list(k string) []string {
	var out []string
	switch v := a[k].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case string:
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

// aiStep is what one tool call produced. A read tool fills Result (and
// Status, the short line the chat shows: "Checked storage"). A change tool
// that validated fills Confirm — the question for the card — and Apply; an
// invalid call fills Result with why.
type aiStep struct {
	Result  string
	Status  string
	Confirm string
	Apply   func(ctx context.Context) string
	// Destructive raises one call of a change tool to the destructive class
	// (delete_files with permanent set): it always asks.
	Destructive bool
}

// aiTool is one entry of the registry.
type aiTool struct {
	Name   string
	Desc   string
	Params map[string]any
	Risk   aiRisk
	Run    func(ctx context.Context, a aiArgs) aiStep
}

// aiToolRegistry is every tool, built on first use. A plain function over
// an uninitialised variable: an initialiser that reaches the tools would put
// the package into an initialisation cycle (the tools reach Settings, which
// reaches Chat, which asks the registry).
func aiToolRegistry() []aiTool {
	aiToolOnce.Do(func() {
		aiToolList = append(aiToolList, aiDeviceTools()...)
		aiToolList = append(aiToolList, aiFileTools()...)
		aiToolList = append(aiToolList, aiGameTools()...)
		aiToolList = append(aiToolList, aiSettingTools()...)
		aiToolList = append(aiToolList, aiMailCalTools()...)
	})
	return aiToolList
}

var (
	aiToolOnce sync.Once
	aiToolList []aiTool
)

// aiToolByName finds a registered tool.
func aiToolByName(name string) (aiTool, bool) {
	for _, t := range aiToolRegistry() {
		if t.Name == name {
			return t, true
		}
	}
	return aiTool{}, false
}

// aiToolAllowed reports whether a tool may be offered right now: read tools
// whenever tools are on; change and destructive ones only with "Let Juka
// change settings and files" on and outside read-only runs (the voice orb,
// offline lite mode); run_command only with the terminal switch on too.
func aiToolAllowed(t aiTool, readOnly bool) bool {
	if !aiToolsOn() {
		return false
	}
	if t.Risk == aiRiskRead {
		return true
	}
	if readOnly || !aiChangesOn() {
		return false
	}
	if t.Name == "run_command" {
		return aiShellOn()
	}
	return true
}

// ── JSON-schema helpers ──────────────────────────────────────────────────

func aiObj(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	o := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

func aiStrP(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func aiIntP(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func aiBoolP(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
func aiStrListP(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}
func aiEnumP(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values, "description": desc}
}

// ── Results ──────────────────────────────────────────────────────────────

// aiFinish is every result's last step: secrets scrubbed, size capped.
func aiFinish(text string) string {
	text = aiRedact(strings.TrimSpace(text))
	if text == "" {
		return "(nothing)"
	}
	if len(text) > aiResultMax {
		cut := aiResultMax - 40
		for cut > 0 && cut < len(text) && text[cut]&0xC0 == 0x80 {
			cut-- // not inside a UTF-8 sequence
		}
		text = text[:cut] + "\n… (cut: result over 4 KB)"
	}
	return text
}

// aiFail is a result that says what went wrong, for the model to explain.
func aiFail(format string, args ...any) aiStep {
	return aiStep{Result: "Error: " + fmt.Sprintf(format, args...)}
}

// ── Secrets ──────────────────────────────────────────────────────────────

var (
	// aiKeyShapes are credentials recognisable by their form: Groq, OpenAI-
	// style and GitHub keys, JWTs, and a supplicant's psk line.
	aiKeyShapes = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`\b(?:gsk_[A-Za-z0-9]{16,}|sk-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})`)
	})
	aiPSKLine = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?i)\b(psk|wep_key\d|sae_password)(\s*=\s*)("[^"\n]*"|\S+)`)
	})
	// aiSecretField is a config line whose key only ends in a credential
	// word ("cheevos_password = …", "\"netplay_token\": …"): the shared
	// redaction wants the word on its own (\bpassword\b), and "_" is a word
	// character, so retroarch.cfg's RetroAchievements password passed it.
	aiSecretField = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*(?:password|passwd|passphrase|token|secret|api[_-]?key|apikey|private[_-]?key|cookie|credential)[A-Za-z0-9_.-]*"?)(\s*[:=]\s*)("[^"\n]*"|'[^'\n]*'|[^\s,;"]+)`)
	})
)

// aiSecretNameWords mark a stored setting whose value is a credential.
var aiSecretNameWords = []string{"key", "token", "password", "passwd", "secret", "psk", "cookie", "auth", "session", "credential"}

// aiRedact scrubs secrets from text that is about to reach the model: the
// shared diagnostics redaction (key=value credentials, bearer tokens, URL
// passwords, home folder names), credentials recognisable by their form,
// and the exact values of every secret JukaHub has stored (the Groq key,
// the RomM and Discord tokens, saved passwords), wherever they appear.
func aiRedact(text string) string {
	return aiRedactWith(text, aiStoredSecrets())
}

// aiRedactWith is aiRedact with the stored secrets given. Pure.
func aiRedactWith(text string, secrets []string) string {
	if text == "" {
		return text
	}
	// Longest first, so a secret that contains another is replaced whole.
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, s := range secrets {
		if len(s) >= 6 && strings.Contains(text, s) {
			text = strings.ReplaceAll(text, s, "[redacted]")
		}
	}
	text = aiPSKLine().ReplaceAllString(text, "${1}${2}[redacted]")
	text = aiSecretField().ReplaceAllString(text, "${1}${2}[redacted]")
	text = aiKeyShapes().ReplaceAllString(text, "[redacted]")
	return redactDiagnosticText(text)
}

// aiStoredSecrets lists the values of the settings whose names say they are
// credentials, decrypted where they are stored encrypted.
func aiStoredSecrets() []string {
	var out []string
	if k := strings.TrimSpace(groqAPIKey()); k != "" {
		out = append(out, k)
	}
	return append(out, aiStoredSecretsOf(appConfig)...)
}

// aiStoredSecretsOf is aiStoredSecrets for a config that need not be appConfig
// yet: the mail and calendar secrets, and every setting of config whose name
// says it is a credential (the Groq key among them). It never logs a value.
func aiStoredSecretsOf(config *Config) []string {
	return append(aiMailCalSecrets(), aiCustomSecretsOf(config)...)
}

// aiCustomSecretsOf is the settings of config whose name says they are a
// credential, as stored and decrypted. The name is a guess (a "cookie…" or
// "…Key" setting can hold a label or a button), so the log redaction's list
// (logStoredSecretsRebuild, built while a loaded config is merged) filters
// these more than the mail and calendar secrets. It never logs a value.
func aiCustomSecretsOf(config *Config) []string {
	var out []string
	if config == nil {
		return out
	}
	for name, raw := range config.Variables.Custom {
		v, ok := raw.(string)
		if !ok || len(strings.TrimSpace(v)) < 6 || !aiSecretName(name) {
			continue
		}
		v = strings.TrimSpace(v)
		out = append(out, v)
		if strings.HasPrefix(v, "ENC:") {
			if plain, err := DecryptAPIToken(v, name); err == nil && plain != "" {
				out = append(out, plain)
			}
		}
	}
	return out
}

func aiSecretName(name string) bool {
	lower := strings.ToLower(name)
	for _, w := range aiSecretNameWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// ── Where file tools may reach ───────────────────────────────────────────

// aiFileRoots are the folders file tools may reach: the SD card on the
// handheld (the home folder on a PC), the Files root when it is not a whole
// drive, the ROM folders and JukaHub's data folder. Never "/" or a drive.
func aiFileRoots() []string {
	var cands []string
	if IsTrimuiDevice() {
		sd := strings.TrimSpace(trimuiSDRootFn())
		if sd == "" {
			sd = "/mnt/SDCARD"
		}
		cands = append(cands, sd)
	} else if home, err := os.UserHomeDir(); err == nil {
		cands = append(cands, home)
	}
	if appConfig != nil {
		cands = append(cands, filesDefaultRoot(appConfig))
	}
	cands = append(cands, romLibraryRoots()...)
	if dir, err := P().DataDir(); err == nil {
		cands = append(cands, dir)
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range cands {
		c = strings.TrimSpace(c)
		if c == "" || c == "." {
			continue
		}
		abs, err := filepath.Abs(c)
		if err != nil || aiSystemRoot(abs) || seen[abs] {
			continue
		}
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
	return out
}

// aiSystemRoot reports a path that is a whole filesystem or a system folder:
// never a root for the file tools, whatever Files is set to.
func aiSystemRoot(p string) bool {
	clean := filepath.Clean(p)
	if clean == string(filepath.Separator) || clean == filepath.VolumeName(clean)+string(filepath.Separator) || clean == filepath.VolumeName(clean) {
		return true
	}
	return aiSystemPath(clean)
}

// aiSystemPath reports a path inside the operating system's own folders.
func aiSystemPath(p string) bool {
	clean := filepath.Clean(p)
	if runtime.GOOS == "windows" {
		lower := strings.ToLower(clean)
		vol := strings.ToLower(filepath.VolumeName(clean))
		for _, d := range []string{`\windows`, `\program files`, `\program files (x86)`, `\programdata`, `\$recycle.bin`, `\system volume information`} {
			if lower == vol+d || strings.HasPrefix(lower, vol+d+`\`) {
				return true
			}
		}
		return false
	}
	for _, d := range []string{"/proc", "/sys", "/dev", "/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/boot", "/run", "/var", "/root", "/overlay", "/rom"} {
		if clean == d || strings.HasPrefix(clean, d+"/") {
			return true
		}
	}
	return false
}

// aiRealPath resolves the symbolic links of the longest part of p that
// exists, and keeps the rest as written: a link inside the card that points
// at /etc is judged by where it points.
func aiRealPath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	cur := p
	for i := 0; i < 64; i++ {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			if rest == "" {
				return real
			}
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		if rest == "" {
			rest = filepath.Base(cur)
		} else {
			rest = filepath.Join(filepath.Base(cur), rest)
		}
		cur = parent
	}
	return p
}

// aiResolvePath turns a path the model gave into a clean absolute path that
// stays inside one of roots — as written and after its links are followed —
// and outside the system's folders. A relative path is taken from the first
// root. Pure apart from reading links.
func aiResolvePath(path string, roots []string) (string, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", errors.New("no path given")
	}
	if strings.ContainsRune(p, 0) {
		return "", errors.New("invalid path")
	}
	if len(roots) == 0 {
		return "", errors.New("no storage is reachable")
	}
	if strings.HasPrefix(p, "~") {
		p = strings.TrimLeft(p[1:], `/\`)
	}
	// Every result is redacted (aiFinish), so on Windows the model only ever
	// saw the home folder as `C:\Users\[user]`: map it back, or no path it
	// read in a result could be used again.
	const winUser = `:\users\[user]`
	if runtime.GOOS == "windows" && len(p) > len(winUser) && strings.EqualFold(p[1:1+len(winUser)], winUser) {
		if home, err := os.UserHomeDir(); err == nil {
			p = home + p[1+len(winUser):]
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(roots[0], p)
	}
	p = filepath.Clean(p)
	real := aiRealPath(p)
	if aiSystemPath(p) || aiSystemPath(real) {
		return "", errors.New("that is a system folder; Juka only reaches the SD card and JukaHub's data")
	}
	for _, r := range roots {
		rr := aiRealPath(r)
		if (isPathWithin(p, r) || isPathWithin(p, rr)) && isPathWithin(real, rr) {
			return p, nil
		}
	}
	return "", errors.New("outside the SD card and JukaHub's data; Juka can't reach it")
}

// aiPath is aiResolvePath against today's roots.
func aiPath(path string) (string, error) {
	return aiResolvePath(path, aiFileRoots())
}

// aiSecretFile reports a file Juka never reads or shows the contents of:
// keys, certificates, Wi-Fi passwords, the user config with its encrypted
// tokens.
func aiSecretFile(p string) bool {
	base := strings.ToLower(filepath.Base(p))
	switch base {
	case "wpa_supplicant.conf", "jukauser.json", "known_hosts", "authorized_keys", ".git-credentials", ".netrc", "cookies.json", "cookies.txt", "cookies.sqlite":
		return true
	}
	for _, ext := range []string{".pem", ".key", ".p12", ".pfx", ".kdbx", ".keystore", ".jks"} {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	if strings.HasPrefix(base, "id_rsa") || strings.HasPrefix(base, "id_ed25519") || strings.HasPrefix(base, "id_ecdsa") {
		return true
	}
	return strings.Contains(filepath.ToSlash(strings.ToLower(p)), "/.ssh/")
}

// aiProtectedForChange reports a path that must not be moved, renamed or
// deleted: a root itself, a top-level folder of a root (Roms, Apps, BIOS,
// RetroArch…), JukaHub's own program folder and its data folder's files
// (the trash inside it excepted).
//
// Paths are compared through isPathWithin (case-insensitive on Windows, so
// `c:\users\me` is the root `C:\Users\Me`) and also with their links
// resolved; a folder that holds a root, JukaHub's program or its data is
// protected like them (deleting `Desktop\JukaHubV2` deletes JukaHub).
func aiProtectedForChange(p string, roots []string) string {
	clean := filepath.Clean(p)
	for _, r := range roots {
		if aiPathCovers(clean, r) {
			return "that is (or holds) a whole storage root"
		}
		if aiSamePath(filepath.Dir(clean), r) && !strings.EqualFold(filepath.Base(clean), aiTrashDirName) {
			if info, err := os.Lstat(clean); err == nil && info.IsDir() {
				return "top-level folders (" + filepath.Base(clean) + ") stay where they are"
			}
		}
	}
	if exe, err := P().ExecutableDir(); err == nil && exe != "" && (aiPathCovers(exe, clean) || aiPathCovers(clean, exe)) {
		return "that is part of JukaHub itself"
	}
	if data, err := P().DataDir(); err == nil && data != "" &&
		(aiPathCovers(clean, data) || (aiPathCovers(data, clean) && !aiInTrash(clean))) {
		return "that is JukaHub's own data"
	}
	if aiSecretFile(clean) {
		return "that file holds keys or passwords"
	}
	return ""
}

// aiProtectedDest says why nothing may be moved into dir ("" when it may):
// JukaHub's program folder and its data folder (the trash excepted).
func aiProtectedDest(dir string) string {
	if exe, err := P().ExecutableDir(); err == nil && exe != "" && aiPathCovers(exe, dir) {
		return "that is part of JukaHub itself"
	}
	if data, err := P().DataDir(); err == nil && data != "" && aiPathCovers(data, dir) && !aiInTrash(dir) {
		return "that is JukaHub's own data"
	}
	return ""
}

// aiPathCovers reports whether inner is outer or inside it, as written or
// with the links of either resolved, and ignoring case: the handheld's card
// is FAT/exFAT, where `apps/jukahub` is `Apps/JukaHub`. Only used to protect,
// so a false match on a case-sensitive disk merely refuses a change.
func aiPathCovers(outer, inner string) bool {
	if isPathWithin(inner, outer) {
		return true
	}
	ro, ri := aiRealPath(outer), aiRealPath(inner)
	if isPathWithin(ri, ro) || isPathWithin(inner, ro) || isPathWithin(ri, outer) {
		return true
	}
	return isPathWithin(strings.ToLower(inner), strings.ToLower(outer)) || isPathWithin(strings.ToLower(ri), strings.ToLower(ro))
}

// aiSamePath reports two spellings of one folder.
func aiSamePath(a, b string) bool {
	return aiPathCovers(a, b) && aiPathCovers(b, a)
}

// aiInTrash reports a path at or under a .jukahub-trash folder.
func aiInTrash(p string) bool {
	return strings.Contains(strings.ToLower(filepath.ToSlash(filepath.Clean(p)))+"/", "/"+aiTrashDirName+"/")
}

// ── The main thread ──────────────────────────────────────────────────────

// aiOnMain runs fn on the main loop (tbxPost) and waits for it, at most
// until ctx ends or 5 s pass. Settings rows and screens live there. A step
// given up on never runs later: a change reported as failed must not apply
// itself behind the user's back.
func aiOnMain(ctx context.Context, fn func()) error {
	done := make(chan struct{})
	var state atomic.Int32 // 0 waiting, 1 running, 2 given up
	tbxPost(func() {
		if !state.CompareAndSwap(0, 1) {
			return
		}
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				logWarn("aitools", "main-thread step recovered: %v", r)
			}
		}()
		fn()
	})
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()
	var why error
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		why = ctx.Err()
	case <-t.C:
		why = errors.New("JukaHub's screen is busy (a game or app is in front)")
	}
	if state.CompareAndSwap(0, 2) {
		return why
	}
	<-done // it had started: a main-thread step is short
	return nil
}

// aiSize is a byte count in the units people read.
func aiSize(n int64) string {
	if n < 0 {
		n = 0
	}
	return humanBytes(n)
}
