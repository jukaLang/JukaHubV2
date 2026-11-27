package main

// ports_env.go — how a PortMaster port is started, and with what.
//
// The aim is parity with the firmware's own Ports tab: a port started from
// JukaHub must see what it would have seen started from MainUI, because every
// port script is written for that, and PortMaster's own files decide the rest.
// So JukaHub decides very little itself:
//
//   - The environment is the firmware's (screenChildEnv: HOME, PATH and
//     LD_LIBRARY_PATH as they were before launch.sh changed them, JukaHub's
//     own library folders out of the path), not JukaHub's. JukaHub's private
//     variables (JUKAHUB_*, its .NET OpenSSL pin) are dropped.
//   - CFW_NAME, DEVICE_NAME, DEVICE_ARCH, ESUDO, SDL_GAMECONTROLLERCONFIG and
//     the display size are NOT set. A port script sources control.txt, which
//     assigns them (device_info.txt probes the firmware), so a value JukaHub
//     exported would be overwritten anyway — or, where a script only fills a
//     gap, would make PortMaster apply another firmware's mod_<CFW>.txt.
//     Westonwrap for Wine is different (wine_display.go): there JukaHub is the
//     stand-in for PortMaster and says "JukaHub" on purpose.
//   - A script under the card's Roms folder goes through the firmware's own
//     Ports launcher (the Emus/PORTS/config.json launch entry) when there is one, exactly as
//     MainUI runs it, so whatever that launcher sets up (HOME, the folder
//     PortMaster lives in, the CPU) happens here too. Settings key
//     portLaunchVia: "auto" (that), "firmware" or "direct". On a TrimUI with
//     no full bash anywhere on the firmware, "auto" runs the script directly
//     under JukaHub's bash instead, once it is there (BusyBox would run it).
//   - Run directly, the script must still find PortMaster. Port scripts look
//     in /opt/system/Tools/PortMaster, /opt/tools/PortMaster,
//     ${XDG_DATA_HOME:-$HOME/.local/share}/PortMaster, then
//     /roms/ports/PortMaster (portmaster.games packaging docs). When that
//     search would not land on a folder with control.txt, JukaHub links
//     $XDG_DATA_HOME/PortMaster (or ~/.local/share/PortMaster) to the real one
//     — the only fix that leaves the games' own save folders where the stock
//     firmware has them. Only if the link cannot be made (FAT, read-only) is
//     XDG_DATA_HOME pointed at PortMaster's parent, with the known cost that a
//     port which does not set XDG_DATA_HOME itself saves beside PortMaster
//     (minui-portmaster #172 — logged when it happens).
//   - An empty $XDG_DATA_HOME/PortMaster with no control.txt makes every
//     port die sourcing it; an empty one is removed.
//   - A bare `udevadm trigger` (CrossMix's WestonPack preview) makes TrimUI's
//     mdev unmount the SD card; ports get a udevadm on PATH
//     that skips that one call and runs the real tool for everything else.
//   - SDL's audio driver stays pinned to ALSA (portLaunchEnv, ports.go).
//   - data/ports.env (NAME=VALUE lines, like weston.env) wins over all of
//     this, so a device test can change a variable without a new build.
//   - Every launch writes data/gamediag/ports/<port>.log: what JukaHub chose
//     and why, then everything the script printed before it redirected its
//     own output to the port's log.txt — the part that says why a port died
//     sourcing control.txt.
//
// None of this has run on a handheld yet (notes: PORT-HW-1).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// portLaunchViaKey is the Custom setting: "auto", "firmware" or "direct".
	portLaunchViaKey = "portLaunchVia"
	// portEnvFileName is data/ports.env.
	portEnvFileName = "ports.env"
	// portLogDirName is the folder under gamediag/ for the per-port logs.
	portLogDirName = "ports"
	// portLogMaxBytes caps a per-port log; a port that keeps printing past it
	// starts the file again rather than fill the card.
	portLogMaxBytes = 2 << 20
	// portLogWatchEvery paces the size check while the port runs.
	portLogWatchEvery = 20 * time.Second
	// portShimDirName holds the udevadm stand-in (tmpfs).
	portShimDirName = "jukahub-port-shims"
)

// portScriptControlDirs are the fixed places port scripts try before
// $XDG_DATA_HOME/PortMaster, in their order.
var portScriptControlDirs = []string{"/opt/system/Tools/PortMaster", "/opt/tools/PortMaster"}

// portScriptControlFallback is where a script looks last.
const portScriptControlFallback = "/roms/ports/PortMaster"

// portFirmwareEmuNames are the Emus folder names a firmware's Ports entry uses.
var portFirmwareEmuNames = []string{"PORTS", "Ports", "PORT", "ports"}

// portLaunchMode says how a port is started.
type portLaunchMode int

const (
	portLaunchDirect   portLaunchMode = iota // the script under bash
	portLaunchFirmware                       // through the firmware's Ports launcher
)

// portLaunchPlan is one port start: the command, its folder and environment,
// and the reasons, which go to the port's log.
type portLaunchPlan struct {
	Mode    portLaunchMode
	Name    string
	Args    []string
	Dir     string
	Env     []string
	Control string // the PortMaster folder the script will find ("" unknown)
	Fix     string // "", "link" or "xdg": what JukaHub did so it finds it
	Notes   []string
}

// portLaunchVia reads the portLaunchVia setting.
func portLaunchVia(config *Config) string {
	if config != nil {
		if v, ok := customString(config.Variables.Custom, portLaunchViaKey); ok {
			switch v = strings.ToLower(strings.TrimSpace(v)); v {
			case "firmware", "direct":
				return v
			}
		}
	}
	return "auto"
}

// portFirmwareLauncher is the firmware's Ports launcher for a script under
// card/Roms, or "". A script elsewhere (Ports/, Data/ports) was never run
// through it by MainUI, so it is not given one.
func portFirmwareLauncher(card, script string) string {
	card, script = strings.TrimSpace(card), strings.TrimSpace(script)
	if card == "" || script == "" {
		return ""
	}
	rel, err := filepath.Rel(card, script)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	top, _, ok := strings.Cut(filepath.ToSlash(rel), "/")
	if !ok || !strings.EqualFold(top, "Roms") {
		return ""
	}
	for _, name := range portFirmwareEmuNames {
		if p := portConfiguredFirmwareLauncher(filepath.Join(card, "Emus", name)); p != "" {
			return p
		}
	}
	return ""
}

// MainUI reads the launch name from config.json. Stock 1.1.0 names
// launch_balanced.sh; other cards use launch.sh. Only a simple file name in
// this Emus directory is accepted, so a malformed config cannot run a file
// elsewhere on the card.
func portConfiguredFirmwareLauncher(dir string) string {
	if raw, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil && len(raw) <= 8<<10 {
		var cfg struct {
			Launch string `json:"launch"`
		}
		if json.Unmarshal(raw, &cfg) == nil {
			name := strings.TrimSpace(cfg.Launch)
			if name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\\`) {
				if p := filepath.Join(dir, name); regularFile(p) {
					return p
				}
			}
		}
	}
	p := filepath.Join(dir, "launch.sh")
	if regularFile(p) {
		return p
	}
	return ""
}

// portScriptControlPick is the PortMaster folder a port script would pick
// with env, by the script's own search (isDir is `[ -d … ]`).
func portScriptControlPick(env []string, isDir func(string) bool) string {
	for _, d := range portScriptControlDirs {
		if isDir(d) {
			return d
		}
	}
	if base := portXDGDataBase(env); base != "" && isDir(filepath.Join(base, "PortMaster")) {
		return filepath.Join(base, "PortMaster")
	}
	return portScriptControlFallback
}

// portXDGDataBase is ${XDG_DATA_HOME:-$HOME/.local/share} in env; "" when
// neither is set.
func portXDGDataBase(env []string) string {
	if v, ok := childEnvLookup(env, "XDG_DATA_HOME"); ok && strings.TrimSpace(v) != "" {
		return v
	}
	if home, ok := childEnvLookup(env, "HOME"); ok && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".local", "share")
	}
	return ""
}

// portRealControlFolder is the installed PortMaster folder holding
// control.txt (Apps/PortMaster/PortMaster on TrimUI), or "".
func portRealControlFolder() string {
	for _, d := range westonPortMasterDirs() {
		if regularFile(filepath.Join(d, "control.txt")) {
			return d
		}
	}
	return ""
}

// portEnsureControl makes the script's own search find PortMaster (see the
// file comment). It returns env (changed only in the last-resort case), the
// folder the script will use and what was done.
func portEnsureControl(env []string) ([]string, string, string, []string) {
	var notes []string
	pick := portScriptControlPick(env, dirExists)
	if regularFile(filepath.Join(pick, "control.txt")) {
		return env, pick, "", nil
	}
	real := portRealControlFolder()
	if real == "" {
		return env, "", "", append(notes, "no PortMaster control.txt found; is PortMaster installed?")
	}
	for _, d := range portScriptControlDirs {
		if pick == d {
			// A folder that wins the search but holds no control.txt: nothing
			// later in the search can be reached by any variable.
			return env, pick, "", append(notes, pick+" exists without control.txt and wins the script's search; remove it")
		}
	}
	if base := portXDGDataBase(env); base != "" {
		link := filepath.Join(base, "PortMaster")
		if info, err := os.Lstat(link); err == nil {
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				// A link that does not reach a control.txt (else the pick
				// above had it): a stale one, made for another card layout.
				_ = os.Remove(link)
			case info.IsDir():
				// Only an empty folder goes (os.Remove refuses anything else).
				if os.Remove(link) == nil {
					notes = append(notes, "removed the empty "+link+" (a port dies sourcing a control.txt that is not there)")
				}
			}
		}
		if _, err := os.Lstat(link); os.IsNotExist(err) {
			if err := os.MkdirAll(base, 0o755); err != nil {
				notes = append(notes, "cannot create "+base+": "+err.Error())
			} else if err := os.Symlink(real, link); err != nil {
				notes = append(notes, "cannot link "+link+": "+err.Error())
			} else if regularFile(filepath.Join(link, "control.txt")) {
				return env, real, "link", append(notes, "linked "+link+" -> "+real)
			} else {
				_ = os.Remove(link)
			}
		} else if err == nil {
			notes = append(notes, link+" is in the way (not empty, no control.txt)")
		}
	}
	// Last resort: games that keep XDG_DATA_HOME save beside PortMaster.
	env = mergeGameProfileEnv(env, map[string]string{"XDG_DATA_HOME": filepath.Dir(real)})
	notes = append(notes, "XDG_DATA_HOME set to "+filepath.Dir(real)+" so the script finds PortMaster; a port that does not set its own may save there")
	return env, real, "xdg", notes
}

// portBaseEnv is a port's environment: the firmware's (portFirmwareEnv) with,
// on a TrimUI, what PortMaster needs and the firmware lacks
// (portmaster_runtime.go).
func portBaseEnv(config *Config) []string {
	return pmRuntimeEnv(portFirmwareEnv(config))
}

// portFirmwareEnv is the firmware's environment for a port: screenChildEnv
// without JukaHub's own variables, plus the audio pin and the udevadm guard.
func portFirmwareEnv(config *Config) []string {
	env := screenChildEnv(os.Environ())
	out := make([]string, 0, len(env)+4)
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "JUKAHUB_") || key == "CLR_OPENSSL_VERSION_OVERRIDE" {
			continue
		}
		out = append(out, kv)
	}
	// The audio pin replaces an inherited SDL_AUDIODRIVER / AUDIODEV instead
	// of being appended after it: one value per key.
	pin := map[string]string{}
	for _, kv := range portLaunchEnv(config) {
		if key, value, ok := strings.Cut(kv, "="); ok {
			pin[key] = value
		}
	}
	return portUdevShimEnv(mergeGameProfileEnv(out, pin))
}

// portEnvDropShellVars is env without the variables that change how a shell
// behaves from outside the script: bash's own option list (SHELLOPTS), which
// every bash started from another bash inherits — a port script without a
// `set -u` of its own then ends at its first `$1` with "unbound variable" —
// its BASHOPTS, and the files BASH_ENV and ENV make it run before the port
// starts. The firmware's Ports tab starts a port from a plain sh, which has
// none of them: a port gets the same from JukaHub (report JH-ZBPUTA, a port
// that quit after 700 ms with "line 5: $1: unbound variable").
func portEnvDropShellVars(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "SHELLOPTS", "BASHOPTS", "BASH_ENV", "ENV":
			continue
		}
		out = append(out, kv)
	}
	return out
}

// portEnvOverrides reads data/ports.env; nil when there is none.
func portEnvOverrides() map[string]string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, portEnvFileName))
	if err != nil {
		return nil
	}
	env, _, bad := parseWestonEnv(string(data))
	for _, line := range bad {
		logWarn("portmaster", "%s: ignoring %q", portEnvFileName, line)
	}
	return env
}

// portBuildLaunchPlan decides how script starts. master is PortMaster's own
// app, which MainUI runs directly as an app, so it is always direct.
func portBuildLaunchPlan(config *Config, script string, master bool) portLaunchPlan {
	plan := portLaunchPlan{Env: portBaseEnv(config)}
	via := portLaunchVia(config)
	fw := ""
	if !master && via != "direct" {
		fw = portFirmwareLauncher(portMasterSDRoot, script)
		if fw == "" && via == "firmware" {
			plan.Notes = append(plan.Notes, "portLaunchVia=firmware but no configured Emus/PORTS launcher for this script; running it directly")
		}
		// The firmware's launcher starts the script with the firmware's own
		// shell. With no real bash anywhere on the firmware that is BusyBox,
		// where PortMaster's scripts die at once: run it directly instead,
		// under the bash JukaHub supplies (portmaster_runtime.go), once it is
		// there.
		if fw != "" && via == "auto" && pmFirmwareHasNoBash() && privateGlibcBashReady() {
			plan.Notes = append(plan.Notes, "not through the firmware's Ports launcher "+fw+": this firmware has no full bash, so JukaHub runs the script with its own")
			fw = ""
		}
	}
	if fw != "" {
		plan.Mode = portLaunchFirmware
		plan.Name, plan.Args = firmwareScriptCommand(fw, "sh", []string{fw, firmwareROMArg(fw, script)})
		plan.Dir = filepath.Dir(fw)
		plan.Notes = append(plan.Notes, "through the firmware's Ports launcher "+fw)
		plan.Control = portScriptControlPick(plan.Env, dirExists)
	} else {
		plan.Mode = portLaunchDirect
		if master {
			// PortMaster's own launch.sh sets its folder up itself.
			plan.Control = portRealControlFolder()
			plan.Notes = append(plan.Notes, portMasterPreflight(script)...)
		} else {
			var notes []string
			plan.Env, plan.Control, plan.Fix, notes = portEnsureControl(plan.Env)
			plan.Notes = append(plan.Notes, notes...)
		}
		plan.Name = portShell(plan.Env)
		plan.Args = []string{"--", script}
		plan.Dir = filepath.Dir(script)
	}
	if over := portEnvOverrides(); len(over) > 0 {
		plan.Env = mergeGameProfileEnv(plan.Env, over)
		plan.Notes = append(plan.Notes, fmt.Sprintf("%d variable(s) from %s", len(over), portEnvFileName))
	}
	// Last, so neither JukaHub's own environment nor data/ports.env can hand a
	// port a shell's option set (portEnvDropShellVars).
	plan.Env = portEnvDropShellVars(plan.Env)
	return plan
}

// portMasterExConfig is TRIMUI_EX's settings file (CrossMix includes
// TRIMUI_EX), which PortMaster's TrimUI launch.sh sources (repohubtool PR #23,
// OS-Trimux PR #14).
const portMasterExConfig = "System/etc/ex_config"

// portMasterPreflight prepares what PortMaster's TrimUI launch.sh is reported
// to stop without — the card's Data/home and Data/ports folders, which
// JukaHub creates (empty folders, harmless) — and names what it cannot fix.
// A launch.sh that sources TRIMUI_EX's ex_config on a card without it runs as
// a patched copy on a TrimUI handheld (pmMasterLauncher).
func portMasterPreflight(launcher string) []string {
	var notes []string
	if !IsTrimuiDevice() {
		return nil
	}
	notes = append(notes, portMasterEnsureDataDirs(portMasterSDRoot)...)
	if problem := portMasterLaunchProblem(launcher); problem != "" {
		notes = append(notes, problem)
		logWarn("portmaster", "%s", problem)
	}
	return notes
}

// portMasterEnsureDataDirs creates the card's Data/home and Data/ports, which
// PortMaster's TrimUI launcher expects; the notes say what was done.
func portMasterEnsureDataDirs(sdRoot string) []string {
	var notes []string
	for _, d := range []string{filepath.Join(sdRoot, "Data", "home"), filepath.Join(sdRoot, "Data", "ports")} {
		if !dirExists(d) {
			if err := os.MkdirAll(d, 0o755); err != nil {
				notes = append(notes, "cannot create "+d+": "+err.Error())
			} else {
				notes = append(notes, "created "+d+" (PortMaster's TrimUI launcher expects it)")
			}
		}
	}
	return notes
}

// portMasterLaunchProblem is a reason launch.sh will stop at once, or "". On
// an ARM64 TrimUI the missing ex_config is handled (pmMasterLauncher).
func portMasterLaunchProblem(launcher string) string {
	if pmRuntimeApplies() || !pmLauncherReadsExConfig(launcher) {
		return ""
	}
	if regularFile(filepath.Join(portMasterSDRoot, filepath.FromSlash(portMasterExConfig))) {
		return ""
	}
	return filepath.Base(launcher) + " reads " + portMasterExConfig + ", which only TRIMUI_EX and CrossMix have; PortMaster may close at once on this firmware"
}

// portShell is bash from the child's PATH (port scripts are bash: they use
// arrays and process substitution), else sh.
func portShell(env []string) string {
	path, _ := childEnvLookup(env, "PATH")
	for _, dir := range strings.Split(path, ":") {
		if dir = strings.TrimSpace(dir); dir == "" {
			continue
		}
		if p := filepath.Join(dir, "bash"); executableFile(p) {
			return p
		}
	}
	if p, err := exec.LookPath("bash"); err == nil && p != "" {
		return p
	}
	return "sh"
}

// portUdevadmPath is the system's udevadm, or "".
func portUdevadmPath() string {
	for _, p := range []string{"/sbin/udevadm", "/bin/udevadm", "/usr/sbin/udevadm", "/usr/bin/udevadm"} {
		if executableFile(p) {
			return p
		}
	}
	return ""
}

// portUdevShimScript is the stand-in udevadm: a bare trigger (only options
// that do not narrow it — action, type, verbose, settle — so every device is
// re-announced) is skipped; a trigger with any filter or device argument,
// and everything else, runs real.
func portUdevShimScript(real string) string {
	return "#!/bin/sh\n" +
		"# Written by JukaHub for PortMaster ports: a bare `udevadm trigger` makes\n" +
		"# TrimUI's mdev unmount the SD card. Everything else runs\n" +
		"# the real udevadm.\n" +
		"if [ \"$1\" = trigger ]; then\n" +
		"  bare=1; value=0\n" +
		"  shift\n" +
		"  for a in \"$@\"; do\n" +
		"    if [ $value = 1 ]; then value=0; continue; fi\n" +
		"    case \"$a\" in\n" +
		"      -c|--action|-t|--type) value=1 ;;\n" +
		"      -c?*|--action=*|-t?*|--type=*|-v|--verbose|-n|--dry-run|-w|--settle|--wait-daemon|--wait-daemon=*|-q|--quiet) ;;\n" +
		"      *) bare=0 ;;\n" +
		"    esac\n" +
		"  done\n" +
		"  if [ $bare = 1 ]; then echo \"jukahub: skipped a bare udevadm trigger\" >&2; exit 0; fi\n" +
		"  exec " + shellQuote(real) + " trigger \"$@\"\n" +
		"fi\n" +
		"exec " + shellQuote(real) + " \"$@\"\n"
}

// portUdevShimEnv puts the udevadm stand-in first on PATH, on a TrimUI
// handheld that has udevadm. Any failure leaves env as it was.
func portUdevShimEnv(env []string) []string {
	if runtime.GOOS != "linux" || !IsTrimuiDevice() {
		return env
	}
	real := portUdevadmPath()
	if real == "" {
		return env
	}
	dir := filepath.Join(os.TempDir(), portShimDirName)
	path := filepath.Join(dir, "udevadm")
	text := portUdevShimScript(real)
	if old, err := os.ReadFile(path); err != nil || string(old) != text {
		if os.MkdirAll(dir, 0o755) != nil || os.WriteFile(path, []byte(text), 0o755) != nil {
			return env
		}
	}
	if !executableFile(path) && os.Chmod(path, 0o755) != nil {
		return env
	}
	cur, _ := childEnvLookup(env, "PATH")
	next := dir
	if cur != "" {
		next += ":" + cur
	}
	return mergeGameProfileEnv(env, map[string]string{"PATH": next})
}

// ── Per-port log ─────────────────────────────────────────────────────────

// portLogPath is data/gamediag/ports/<script name>.log ("PortMaster.log"
// for PortMaster itself), or "". Keyed by the script, so the Ports panel, the
// library and the diagnosis find the same file whatever title they show.
func portLogPath(name, script string) string {
	dir := gameDiagLogDir()
	if dir == "" {
		return ""
	}
	if name != portMasterAppName {
		if stem := strings.TrimSuffix(filepath.Base(script), filepath.Ext(script)); stem != "" && stem != "." {
			name = stem
		}
	}
	safe := sanitizeFileName(strings.TrimSpace(name))
	if len(safe) > 80 {
		safe = safe[:80]
	}
	return filepath.Join(dir, portLogDirName, safe+".log")
}

// portLogOpen empties the port's log and writes what JukaHub chose. The
// file is the child's stdout and stderr; nil when it cannot be written.
func portLogOpen(name, script string, plan portLaunchPlan) (*os.File, string) {
	path := portLogPath(name, script)
	if path == "" || os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return nil, ""
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# JukaHub port launch %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "# script: %s\n", script)
	fmt.Fprintf(&b, "# command: %s %s (in %s)\n", plan.Name, strings.Join(plan.Args, " "), plan.Dir)
	fmt.Fprintf(&b, "# PortMaster folder the script finds: %s\n", setOr(plan.Control, "(unknown)"))
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "PATH", "LD_LIBRARY_PATH", "SDL_AUDIODRIVER", "AUDIODEV"} {
		v, ok := childEnvLookup(plan.Env, key)
		if !ok {
			v = "(unset)"
		}
		fmt.Fprintf(&b, "# %s=%s\n", key, v)
	}
	for _, n := range plan.Notes {
		fmt.Fprintf(&b, "# note: %s\n", n)
	}
	b.WriteString(portLogOutputMarker + "\n") // gamediag_port.go reads the output after it
	_, _ = f.WriteString(b.String())
	return f, path
}

// portLogEndPrefix starts the line JukaHub adds to a port's launch log when
// the launch ended (portLogNoteEnd); the diagnosis does not count it as the
// script's output.
const portLogEndPrefix = "# JukaHub: the launch ended"

// portLogNoteEnd adds how long the launch ran and how it ended to its log, so
// a bug report or the diagnosis can tell a launch that died at once from one
// that ran. note, when set, says what JukaHub ended after the launcher
// (portmaster_splash.go). Off the main thread.
func portLogNoteEnd(path string, waitErr error, ran time.Duration, note string) {
	if path == "" {
		return
	}
	how := "exit status 0"
	if waitErr != nil {
		how = waitErr.Error()
	}
	if note != "" {
		how += "); (" + note
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "\n%s after %s (%s)\n", portLogEndPrefix, ran.Round(100*time.Millisecond), how)
	_ = f.Close()
}

// portLogWatch empties path whenever it grows past portLogMaxBytes, until
// done is closed. The child writes with O_APPEND, so it carries on at the
// new end. A nil channel is returned for an empty path (closing it is skipped
// by portLogWatchDone).
func portLogWatch(path string) chan struct{} {
	if path == "" {
		return nil
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(portLogWatchEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if info, err := os.Stat(path); err == nil && info.Size() > portLogMaxBytes {
					_ = os.Truncate(path, 0)
				}
			}
		}
	}()
	return done
}

// portLogWatchDone stops a portLogWatch.
func portLogWatchDone(done chan struct{}) {
	if done != nil {
		close(done)
	}
}
