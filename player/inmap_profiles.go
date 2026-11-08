package main

// inmap_profiles.go — the control profiles: the five JukaHub ships, the
// user's files in <data>/input-profiles/*.jkinput and the files plugins
// register; which one is on; and the switches that choose it.
//
// The built-in profiles are written in the language itself, so each one is
// also an example: "Duplicate" on one of them writes it to the folder, where
// Files' editor can change it.
//
// The registry is guarded by a mutex because inmapRegisterProfileFile (the
// plugin contract) may be called off the main thread. Everything else —
// validation, the engine, the screens — runs on the main thread.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	inmapKeyProfile = "inputProfile"
	inmapKeyAuto    = "inputProfileAuto"
	inmapKeyHotkey  = "inputProfileHotkey"

	inmapFileExt      = ".jkinput"
	inmapMaxFileBytes = 64 << 10
	inmapMaxUserFiles = 64
	inmapMaxImports   = 50
	inmapDefaultName  = "Default"
)

// inmapBuiltinTexts are the profiles JukaHub ships, in list order.
var inmapBuiltinTexts = []string{
	`profile: Default
description: Every button does what the screen says
`,
	`profile: Gaming
description: Hold Start for Quick Settings, hold B for Home, double-tap Select for a screenshot
map:
  start hold 600ms -> action:quick_settings
  b hold 800ms     -> action:home
  select double    -> action:screenshot
`,
	`profile: Media browsing
description: The D-pad seeks and a held X plays or pauses while something plays
scenes: Tube, MusicPlayer
map:
  dpad_left    -> seek:-10s
  dpad_right   -> seek:+10s
  x hold 500ms -> action:play_pause
  scene: MusicPlayer / y double -> action:favorite
`,
	`profile: Left-handed
description: The pad mirrored: the D-pad confirms and goes back, the face buttons move
map:
  dpad_down  -> button:a
  dpad_right -> button:b
  dpad_left  -> button:x
  dpad_up    -> button:y
  a          -> button:dpad_down
  b          -> button:dpad_right
  x          -> button:dpad_left
  y          -> button:dpad_up
  l1         -> button:r1
  r1         -> button:l1
`,
	`profile: One-handed
description: Everything on the right: the face buttons move, R1 confirms, R2 goes back
map:
  y             -> button:dpad_up
  a             -> button:dpad_down
  x             -> button:dpad_left
  b             -> button:dpad_right
  r1            -> button:a
  r1 hold 600ms -> action:context_menu
  r2            -> button:b
  start double  -> action:quick_settings
`,
}

// inmapReadmeText is put in the profiles folder once, for whoever opens it
// in Files.
const inmapReadmeText = `JukaHub control profiles
========================

Each .jkinput file here is one control profile. Pick one in
Settings > Controls, or hold Select and press R1 to switch.

  profile: Media browsing
  description: Seek with the D-pad
  scenes: Tube, MusicPlayer        (or * for every screen)
  map:
    dpad_left        -> seek:-10s
    l1               -> key:PageUp
    a hold 500ms     -> action:context_menu
    y double         -> action:favorite
    l1+x             -> profile:next
    r3               -> none

Buttons: a b x y l1 r1 l2 r2 l3 r3 start select guide dpad_up dpad_down
dpad_left dpad_right. "hold 500ms" waits for a held button, "double" for two
taps, "l1+a" means hold L1 and press A.

Targets: button:<button>, key:<key>, action:<name>, profile:next|prev|<name>,
none. Actions: back confirm home start_menu quick_settings screenshot search
context_menu favorite next_tab prev_tab page_up page_down next_item prev_item
play_pause seek:-10s volume:+10 volume_up volume_down brightness:+10
brightness_up brightness_down controls local_play pass_turn.

Lines that cannot be read are listed, with their line and column, on the
Control profiles screen; the rest of the profile still works. Select + Start
always quits JukaHub, and profiles pause on the on-screen keyboard.
`

// inmapRegistry holds every profile and which one is on.
type inmapRegistry struct {
	mu       sync.Mutex
	builtins []*inmapProfile
	user     []*inmapProfile
	external []*inmapProfile
	// base is the profile the user chose; auto is the one the screen on
	// display switched on, "" when none did.
	base string
	auto string
	// autoOn and hotkeyOn are the two switches in Settings → Controls.
	autoOn   bool
	hotkeyOn bool
	// gen bumps on every change; validGen is the generation the profiles were
	// last validated at.
	gen      int
	validGen int
}

var inmapReg = inmapRegistry{hotkeyOn: true, validGen: -1}

// inmapEnsureBuiltinsLocked reads the shipped profiles the first time any
// profile is asked for.
func inmapEnsureBuiltinsLocked() {
	if inmapReg.builtins != nil {
		return
	}
	for _, text := range inmapBuiltinTexts {
		p := inmapParse(text)
		p.builtin = true
		inmapReg.builtins = append(inmapReg.builtins, p)
	}
}

// inmapAllLocked is every profile in list order: the built-ins, the user's,
// then the plugins'.
func inmapAllLocked() []*inmapProfile {
	inmapEnsureBuiltinsLocked()
	out := make([]*inmapProfile, 0, len(inmapReg.builtins)+len(inmapReg.user)+len(inmapReg.external))
	out = append(out, inmapReg.builtins...)
	out = append(out, inmapReg.user...)
	return append(out, inmapReg.external...)
}

// inmapProfileList is every profile, in the order the switcher steps.
func inmapProfileList() []*inmapProfile {
	inmapReg.mu.Lock()
	defer inmapReg.mu.Unlock()
	return inmapAllLocked()
}

// inmapFindProfileLocked finds a profile by name, ignoring case; "" is
// Default. It allocates nothing: the engine asks on every press.
func inmapFindProfileLocked(name string) *inmapProfile {
	inmapEnsureBuiltinsLocked()
	name = strings.TrimSpace(name)
	if name == "" {
		name = inmapDefaultName
	}
	for _, group := range [3][]*inmapProfile{inmapReg.builtins, inmapReg.user, inmapReg.external} {
		for _, p := range group {
			if strings.EqualFold(p.name, name) {
				return p
			}
		}
	}
	return nil
}

// inmapFindProfile is inmapFindProfileLocked for callers off the lock.
func inmapFindProfile(name string) *inmapProfile {
	inmapReg.mu.Lock()
	defer inmapReg.mu.Unlock()
	return inmapFindProfileLocked(name)
}

// inmapEffectiveLocked is the profile the engine runs now: the one the
// screen switched on, else the chosen one, else Default.
func inmapEffectiveLocked() *inmapProfile {
	if inmapReg.auto != "" {
		if p := inmapFindProfileLocked(inmapReg.auto); p != nil {
			return p
		}
	}
	if p := inmapFindProfileLocked(inmapReg.base); p != nil {
		return p
	}
	return inmapFindProfileLocked(inmapDefaultName)
}

// inmapEffective is inmapEffectiveLocked for callers off the lock.
func inmapEffective() *inmapProfile {
	inmapReg.mu.Lock()
	defer inmapReg.mu.Unlock()
	return inmapEffectiveLocked()
}

// inmapChosen is the profile the user chose (not one a screen switched on).
func inmapChosen() *inmapProfile {
	inmapReg.mu.Lock()
	defer inmapReg.mu.Unlock()
	if p := inmapFindProfileLocked(inmapReg.base); p != nil {
		return p
	}
	return inmapFindProfileLocked(inmapDefaultName)
}

// inmapGeneration is the registry's change counter, for caches.
func inmapGeneration() int {
	inmapReg.mu.Lock()
	defer inmapReg.mu.Unlock()
	return inmapReg.gen
}

// inmapHotkeyEnabled reports whether hold Select + R1 switches profiles.
func inmapHotkeyEnabled() bool {
	inmapReg.mu.Lock()
	defer inmapReg.mu.Unlock()
	return inmapReg.hotkeyOn
}

// inmapAutoEnabled reports whether screens switch their own profile on.
func inmapAutoEnabled() bool {
	inmapReg.mu.Lock()
	defer inmapReg.mu.Unlock()
	return inmapReg.autoOn
}

// inmapEnsureValid validates every profile against the current set of
// profile and screen names when either changed. Main thread.
func inmapEnsureValid(config *Config) {
	inmapReg.mu.Lock()
	defer inmapReg.mu.Unlock()
	if inmapReg.validGen == inmapReg.gen {
		return
	}
	all := inmapAllLocked()
	known := inmapKnown{profiles: make(map[string]bool, len(all))}
	for _, p := range all {
		known.profiles[strings.ToLower(p.name)] = true
	}
	if config != nil {
		known.scenes = make([]string, 0, len(config.Scenes))
		for _, s := range config.Scenes {
			known.scenes = append(known.scenes, s.Name)
		}
	}
	for _, p := range all {
		inmapValidate(p, known)
	}
	inmapReg.validGen = inmapReg.gen
}

// ── Settings ─────────────────────────────────────────────────────────────

// inmapInit loads the switches and the user's profile files before the
// first frame.
func inmapInit(config *Config) {
	inmapLoadSettings(config)
	inmapReloadFiles()
	inmapEnsureValid(config)
}

// inmapLoadSettings reads the chosen profile and the switches from the
// config (startup and every reload). A profile a screen switched on is
// forgotten: the next frame asks the screen again.
func inmapLoadSettings(config *Config) {
	if config == nil {
		return
	}
	base, _ := customString(config.Variables.Custom, inmapKeyProfile)
	autoOn := false
	if v, ok := customValue(config.Variables.Custom, inmapKeyAuto); ok {
		autoOn = coerceBool(v)
	}
	hotkeyOn := true
	if v, ok := customValue(config.Variables.Custom, inmapKeyHotkey); ok {
		hotkeyOn = coerceBool(v)
	}
	inmapReg.mu.Lock()
	inmapReg.base = strings.TrimSpace(base)
	inmapReg.autoOn = autoOn
	inmapReg.hotkeyOn = hotkeyOn
	inmapReg.auto = ""
	inmapReg.gen++
	inmapReg.mu.Unlock()
	inmapEngine.lastScene = ""
	navmodeLoadSettings(config)
}

// inmapStoreSettings writes the chosen profile and the switches into the
// config and saves them through the shared debounced writer.
func inmapStoreSettings(config *Config) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	inmapReg.mu.Lock()
	base, autoOn, hotkeyOn := inmapReg.base, inmapReg.autoOn, inmapReg.hotkeyOn
	inmapReg.mu.Unlock()
	config.Variables.Custom[inmapKeyProfile] = base
	config.Variables.Custom[inmapKeyAuto] = autoOn
	config.Variables.Custom[inmapKeyHotkey] = hotkeyOn
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// inmapSetAuto turns "Switch by screen" on or off.
func inmapSetAuto(config *Config, on bool) {
	inmapReg.mu.Lock()
	inmapReg.autoOn = on
	if !on {
		inmapReg.auto = ""
	}
	inmapReg.gen++
	inmapReg.mu.Unlock()
	// Ask the screen on display again on the next frame.
	inmapEngine.lastScene = ""
	inmapStoreSettings(config)
}

// inmapSetHotkey turns the hold Select + R1 switch on or off.
func inmapSetHotkey(config *Config, on bool) {
	inmapReg.mu.Lock()
	inmapReg.hotkeyOn = on
	inmapReg.gen++
	inmapReg.mu.Unlock()
	inmapStoreSettings(config)
}

// inmapChoose makes p the chosen profile and remembers it; announce shows
// the "Controls: …" toast. A choice made by hand wins over the profile the
// screen switched on, until the user moves to another screen.
func inmapChoose(config *Config, p *inmapProfile, announce bool) {
	if p == nil {
		return
	}
	inmapReg.mu.Lock()
	inmapReg.base = p.name
	inmapReg.auto = ""
	inmapReg.gen++
	inmapReg.mu.Unlock()
	inmapStoreSettings(config)
	if announce {
		inmapAnnounce(p)
	}
}

// inmapAnnounce is the toast a switch shows: "Controls: Media browsing", with
// the screens it works on when it is not every screen.
func inmapAnnounce(p *inmapProfile) {
	if p == nil {
		return
	}
	ShowToast("Controls: "+p.name+inmapScopeSuffix(p), ToastKindSwitch)
}

// inmapScopeSuffix is " · Media, Music" for a screen-scoped profile, "" for
// one that works everywhere.
func inmapScopeSuffix(p *inmapProfile) string {
	if p == nil || p.everywhere() {
		return ""
	}
	names := make([]string, 0, len(p.scenes))
	for _, s := range p.scenes {
		names = append(names, sceneDisplayName(s))
	}
	return " · " + strings.Join(names, ", ")
}

// inmapCycleProfile steps the chosen profile through the list (dir +1 or
// -1) and announces the new one.
func inmapCycleProfile(config *Config, dir int) {
	list := inmapProfileList()
	if len(list) == 0 {
		return
	}
	cur := inmapEffective()
	at := 0
	for i, p := range list {
		if p == cur {
			at = i
			break
		}
	}
	at = (at + dir + len(list)) % len(list)
	inmapChoose(config, list[at], true)
}

// inmapApplyProfileTarget runs a profile: target (next, prev, default or a
// name).
func inmapApplyProfileTarget(config *Config, t inmapTarget) {
	switch inmapNormName(t.profile) {
	case "next":
		inmapCycleProfile(config, 1)
		return
	case "prev", "previous":
		inmapCycleProfile(config, -1)
		return
	}
	p := inmapFindProfile(t.profile)
	if p == nil {
		ShowToast("No control profile called "+t.profile, ToastKindWarning)
		return
	}
	inmapChoose(config, p, true)
}

// inmapAutoProfileFor is the profile that switches itself on for a screen:
// the first, user and plugin profiles before the built-ins, that names the
// screen and has not opted out with "auto: off".
func inmapAutoProfileFor(scene string) *inmapProfile {
	if scene == "" {
		return nil
	}
	inmapReg.mu.Lock()
	defer inmapReg.mu.Unlock()
	inmapEnsureBuiltinsLocked()
	for _, group := range [3][]*inmapProfile{inmapReg.user, inmapReg.external, inmapReg.builtins} {
		for _, p := range group {
			if !p.noAuto && !p.everywhere() && inmapSceneMatches(p.scenes, scene) {
				return p
			}
		}
	}
	return nil
}

// inmapSceneEntered runs when the screen on display changes: with "Switch by
// screen" on, a profile naming the screen takes over, and leaving it hands
// the pad back to the chosen profile.
func inmapSceneEntered(scene string) {
	if !inmapAutoEnabled() {
		return
	}
	before := inmapEffective()
	next := ""
	if p := inmapAutoProfileFor(scene); p != nil {
		next = p.name
	}
	inmapReg.mu.Lock()
	changed := inmapReg.auto != next
	inmapReg.auto = next
	if changed {
		inmapReg.gen++
	}
	inmapReg.mu.Unlock()
	if after := inmapEffective(); changed && after != before {
		inmapAnnounce(after)
	}
}

// ── Cross-area contract ──────────────────────────────────────────────────

// inmapActivateByName switches to the profile called name (any case) and
// reports whether there is one. Experience packs call it to bring their own
// controls; "" and "Default" go back to the Default profile.
func inmapActivateByName(name string) bool {
	p := inmapFindProfile(name)
	if p == nil {
		return false
	}
	inmapChoose(appConfig, p, true)
	return true
}

// inmapProfileNames lists every profile's name, in list order.
func inmapProfileNames() []string {
	list := inmapProfileList()
	out := make([]string, 0, len(list))
	for _, p := range list {
		out = append(out, p.name)
	}
	return out
}

// inmapRegisterProfileFile adds a profile file that lives outside the
// profiles folder (a plugin's). It is read now and again on every reload,
// listed as a plugin's profile (it can be duplicated, not deleted), and safe
// to call from any goroutine. Registering the same path again replaces it. A
// file with nothing usable in it is refused with its first problem.
func inmapRegisterProfileFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("no control profile file given")
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	p, err := inmapReadFile(path)
	if err != nil {
		return err
	}
	if len(p.bindings) == 0 && len(p.parsed) > 0 {
		first := p.parsed[0]
		return fmt.Errorf("%s: line %d: %s", filepath.Base(path), first.line, first.msg)
	}
	p.external = true
	inmapReg.mu.Lock()
	defer inmapReg.mu.Unlock()
	kept := make([]*inmapProfile, 0, len(inmapReg.external)+1)
	for _, q := range inmapReg.external {
		if !filesSamePath(q.path, path) {
			kept = append(kept, q)
		}
	}
	inmapReg.external = kept
	inmapAddLocked(&inmapReg.external, p)
	inmapReg.gen++
	return nil
}

// ── Files ────────────────────────────────────────────────────────────────

// inmapProfilesDirHook lets a test point the profiles folder at a temporary
// directory.
var inmapProfilesDirHook func() (string, error)

// inmapProfilesDir is <data>/input-profiles, created when missing.
func inmapProfilesDir() (string, error) {
	if inmapProfilesDirHook != nil {
		return inmapProfilesDirHook()
	}
	data, err := P().DataDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(data, "input-profiles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// inmapNameFromFile names a profile that has no "profile:" line after its
// file: "media_v2.jkinput" is "media v2".
func inmapNameFromFile(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	name := strings.TrimSpace(strings.ReplaceAll(base, "_", " "))
	if name == "" || name == "." {
		return "Untitled"
	}
	return name
}

// inmapReadFile reads and parses one profile file.
func inmapReadFile(path string) (*inmapProfile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a folder", filepath.Base(path))
	}
	if info.Size() > inmapMaxFileBytes {
		return nil, fmt.Errorf("%s is larger than a control profile can be", filepath.Base(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p := inmapParse(string(data))
	p.path = path
	if strings.TrimSpace(p.name) == "" {
		p.name = inmapNameFromFile(path)
	}
	return p, nil
}

// inmapReadDir reads every profile file in dir, sorted by name.
func inmapReadDir(dir string) []*inmapProfile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []*inmapProfile
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), inmapFileExt) {
			continue
		}
		if len(out) >= inmapMaxUserFiles {
			break
		}
		p, err := inmapReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			LogScene("input").Warn("control profile skipped", "file", e.Name(), "err", err)
			continue
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].name) < strings.ToLower(out[j].name)
	})
	return out
}

// inmapSeedReadme writes the folder's README.txt the first time.
func inmapSeedReadme(dir string) {
	path := filepath.Join(dir, "README.txt")
	if _, err := os.Stat(path); err == nil {
		return
	}
	if err := os.WriteFile(path, []byte(inmapReadmeText), 0o644); err != nil {
		LogScene("input").Warn("could not write the control profiles README", "err", err)
	}
}

// inmapAddLocked appends p to a list, renaming it when another profile
// already has its name: names are how profiles are chosen and switched to.
func inmapAddLocked(list *[]*inmapProfile, p *inmapProfile) {
	if strings.TrimSpace(p.name) == "" {
		p.name = inmapNameFromFile(p.path)
	}
	if inmapFindProfileLocked(p.name) != nil {
		taken := p.name
		for n := 2; ; n++ {
			candidate := taken + " (" + strconv.Itoa(n) + ")"
			if inmapFindProfileLocked(candidate) == nil {
				p.name = candidate
				break
			}
		}
		p.parsed = append(p.parsed, inmapProblem{
			line: 1, col: 1, warning: true,
			msg: "another profile is already called \"" + taken + "\"; this one is listed as \"" + p.name + "\"",
		})
		p.problems = append([]inmapProblem(nil), p.parsed...)
	}
	*list = append(*list, p)
}

// inmapReloadFiles reads the profiles folder again, and the files plugins
// registered. It runs at startup, when Control profiles opens and when the
// editor closes over it.
func inmapReloadFiles() {
	var user []*inmapProfile
	if dir, err := inmapProfilesDir(); err == nil {
		inmapSeedReadme(dir)
		user = inmapReadDir(dir)
	} else {
		LogScene("input").Warn("control profiles folder unavailable", "err", err)
	}
	inmapReg.mu.Lock()
	paths := make([]string, 0, len(inmapReg.external))
	for _, p := range inmapReg.external {
		paths = append(paths, p.path)
	}
	inmapReg.mu.Unlock()
	external := make([]*inmapProfile, 0, len(paths))
	for _, path := range paths {
		p, err := inmapReadFile(path)
		if err != nil {
			LogScene("input").Warn("registered control profile unreadable", "file", path, "err", err)
			continue
		}
		p.external = true
		external = append(external, p)
	}
	inmapReg.mu.Lock()
	inmapEnsureBuiltinsLocked()
	inmapReg.user = nil
	inmapReg.external = nil
	for _, p := range user {
		inmapAddLocked(&inmapReg.user, p)
	}
	for _, p := range external {
		inmapAddLocked(&inmapReg.external, p)
	}
	inmapReg.gen++
	inmapReg.mu.Unlock()
}

// inmapFileBase turns a profile name into a file name: letters, digits,
// spaces and a few marks are kept, the rest become underscores.
func inmapFileBase(name string) string {
	var sb strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(name) {
		if n >= 48 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == ' ', r == '-', r == '_', r == '(', r == ')':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
		n++
	}
	base := strings.TrimSpace(sb.String())
	if base == "" {
		return "profile"
	}
	return base
}

// inmapUniqueFile is dir/base.jkinput, numbered when the name is taken.
func inmapUniqueFile(dir, base string) string {
	path := filepath.Join(dir, base+inmapFileExt)
	for n := 2; n < 1000; n++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		}
		path = filepath.Join(dir, base+" "+strconv.Itoa(n)+inmapFileExt)
	}
	return path
}

// inmapUniqueName is base, or base 2, 3 … when a profile already has it.
func inmapUniqueName(base string) string {
	inmapReg.mu.Lock()
	defer inmapReg.mu.Unlock()
	name := base
	for n := 2; inmapFindProfileLocked(name) != nil && n < 1000; n++ {
		name = base + " " + strconv.Itoa(n)
	}
	return name
}

// inmapRenameText gives profile text a new name: its profile: line is
// replaced, or one is added at the top.
func inmapRenameText(text, name string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, line := range lines {
		content := strings.TrimSpace(inmapStripComment(line))
		colon := strings.IndexByte(content, ':')
		if colon < 0 {
			continue
		}
		if at, _ := inmapFindArrow(content); at >= 0 {
			continue
		}
		switch inmapNormName(content[:colon]) {
		case "profile", "name", "title":
			lines[i] = "profile: " + name
			return strings.Join(lines, "\n")
		}
	}
	return "profile: " + name + "\n" + text
}

// inmapOwnedByUser reports whether a profile is a file in the profiles
// folder, which the screen may delete.
func inmapOwnedByUser(p *inmapProfile) bool {
	return p != nil && !p.builtin && !p.external && p.path != ""
}

// inmapDuplicate copies a profile into a new file in the profiles folder,
// named "<name> copy", and returns the copy.
func inmapDuplicate(p *inmapProfile) (*inmapProfile, error) {
	if p == nil {
		return nil, errors.New("no profile to copy")
	}
	dir, err := inmapProfilesDir()
	if err != nil {
		return nil, err
	}
	name := inmapUniqueName(p.name + " copy")
	path := inmapUniqueFile(dir, inmapFileBase(name))
	if err := os.WriteFile(path, []byte(inmapRenameText(p.text, name)), 0o644); err != nil {
		return nil, err
	}
	inmapReloadFiles()
	return inmapProfileAt(path), nil
}

// inmapProfileAt finds the loaded profile read from path.
func inmapProfileAt(path string) *inmapProfile {
	inmapReg.mu.Lock()
	defer inmapReg.mu.Unlock()
	for _, p := range inmapAllLocked() {
		if p.path != "" && filesSamePath(p.path, path) {
			return p
		}
	}
	return nil
}

// inmapDelete removes a user profile's file. The chosen profile falls back to
// Default when it was the one deleted.
func inmapDelete(config *Config, p *inmapProfile) error {
	if !inmapOwnedByUser(p) {
		return errors.New("only your own profiles can be deleted")
	}
	if err := os.Remove(p.path); err != nil {
		return err
	}
	inmapReg.mu.Lock()
	wasChosen := strings.EqualFold(inmapReg.base, p.name)
	if wasChosen {
		inmapReg.base = ""
	}
	if strings.EqualFold(inmapReg.auto, p.name) {
		inmapReg.auto = ""
	}
	inmapReg.mu.Unlock()
	inmapReloadFiles()
	if wasChosen {
		inmapStoreSettings(config)
	}
	return nil
}

// inmapIsDir reports whether path is an existing folder.
func inmapIsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// inmapExportDir is where Export writes a profile to share: Downloads when
// there is one, the card on a handheld, else the data folder.
func inmapExportDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if dl := filepath.Join(home, "Downloads"); inmapIsDir(dl) {
			return dl
		}
	}
	if IsTrimuiDevice() {
		if root := filesDefaultRoot(appConfig); inmapIsDir(root) {
			return root
		}
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return dir
	}
	return "."
}

// inmapExport writes a profile's text to the export folder and returns the
// file's path.
func inmapExport(p *inmapProfile) (string, error) {
	if p == nil {
		return "", errors.New("no profile to export")
	}
	path := inmapUniqueFile(inmapExportDir(), inmapFileBase(p.name))
	if err := os.WriteFile(path, []byte(p.text), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// inmapImportDirs are the folders Import looks in: Downloads, home, the card
// and the data folder.
func inmapImportDirs() []string {
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, "Downloads"), home)
	}
	if appConfig != nil {
		dirs = append(dirs, filesDefaultRoot(appConfig))
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		dirs = append(dirs, dir)
	}
	return dirs
}

// inmapScanImports lists the profile files in dirs, leaving out the profiles
// folder itself. It reads the card, so it runs off the render thread.
func inmapScanImports(dirs []string, profilesDir string) []string {
	var out []string
	var seen []string
	for _, d := range dirs {
		if d == "" || (profilesDir != "" && filesSamePath(d, profilesDir)) {
			continue
		}
		dup := false
		for _, s := range seen {
			if filesSamePath(s, d) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		seen = append(seen, d)
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), inmapFileExt) {
				continue
			}
			out = append(out, filepath.Join(d, e.Name()))
			if len(out) >= inmapMaxImports {
				return out
			}
		}
	}
	return out
}

// inmapImport copies a shared profile file into the profiles folder and
// returns the profile as loaded from there.
func inmapImport(src string) (*inmapProfile, error) {
	p, err := inmapReadFile(src)
	if err != nil {
		return nil, err
	}
	dir, err := inmapProfilesDir()
	if err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	dst := inmapUniqueFile(dir, inmapFileBase(base))
	if err := os.WriteFile(dst, []byte(p.text), 0o644); err != nil {
		return nil, err
	}
	inmapReloadFiles()
	if q := inmapProfileAt(dst); q != nil {
		return q, nil
	}
	return p, nil
}
