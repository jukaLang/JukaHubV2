package main

// ppsspp_resume.go — a PSP game in JukaHub's own PPSSPP build resumes exactly
// after a power off or a flat battery (instantresume.go).
//
// Upstream PPSSPP can load a state at start (--state=) but has no way to be
// asked for one from outside, and no save on exit. JukaHub's own build
// (player/native/build-ppsspp.sh) carries one small change for it: SIGUSR1
// asks the running game for a save state at $JUKAHUB_PPSSPP_SAVE_TO, written
// by PPSSPP's own SaveState::Save on its next frame, and "<file>.done" then
// says "ok" or "fail". A build where the change went in carries
// ppssppResumeFlagName beside its program; JukaHub never sends the signal to
// any other (SIGUSR1 would end it).
//
// Here: every PSP start through the managed launcher of such a build gets
// JUKAHUB_PPSSPP_SAVE_TO — one file per game in the managed folder's resume/,
// outside every build folder and apart from the player's own PPSSPP_STATE
// slots — and, when JukaHub saved one for this game (sessionsafe.go's record,
// Resume), JUKAHUB_PPSSPP_STATE, which jukahub-ppsspp.sh hands to PPSSPP as
// --state=. A state is tens of megabytes, so only the newest one is kept.
//
// minui-psp's build (the one PSP games use by default on a tg5040) has no
// such change: the PPSSPP card offers JukaHub's own build for it
// (ppsspp_managed_ui.go, "Resume PSP games after a power off").

import (
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// ppssppResumeFlagName is the file build-ppsspp.sh puts beside the
	// binary when the save-on-request change went in; it holds
	// ppssppResumeCapability on a line of its own. The release notes carry
	// the same word (BUILDINFO.txt's Resume line).
	ppssppResumeFlagName   = "jukahub-resume.txt"
	ppssppResumeCapability = "save-on-request"
	// ppssppEnvSaveTo is where the build writes a state on request;
	// ppssppEnvState the state the launcher starts the game from.
	ppssppEnvSaveTo = "JUKAHUB_PPSSPP_SAVE_TO"
	ppssppEnvState  = "JUKAHUB_PPSSPP_STATE"
	// ppssppResumeDirName is the folder for them in the managed root.
	ppssppResumeDirName = "resume"
	// ppssppResumeExt is a requested state's extension, PPSSPP's own.
	ppssppResumeExt = ".ppst"
)

// ppssppBinaryCanResume reports a PPSSPP program from JukaHub's own build with
// the save-on-request change: the flag file beside it names the capability.
func ppssppBinaryCanResume(binary string) bool {
	if strings.TrimSpace(binary) == "" {
		return false
	}
	f, err := os.Open(filepath.Join(filepath.Dir(binary), ppssppResumeFlagName))
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4<<10))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == ppssppResumeCapability {
			return true
		}
	}
	return false
}

// ppssppActiveCanResume reports that PSP games use a JukaHub build that takes
// the save request.
func ppssppActiveCanResume(st ppssppState) bool {
	b, ok := st.build(st.Active)
	return ok && ppssppBinaryCanResume(b.binary())
}

// ppssppResumeDir holds the states saved on request ("" without the managed
// root).
func ppssppResumeDir() string {
	root := ppssppManagedRootFn()
	if root == "" {
		return ""
	}
	return filepath.Join(root, ppssppResumeDirName)
}

// ppssppResumeFile is where a game's state on request goes: one file per
// game, its name in plain letters (no more than 40) and a hash of its path,
// so two games of one name never share it. "" without the folder.
func ppssppResumeFile(romPath string) string {
	dir := ppssppResumeDir()
	romPath = strings.TrimSpace(romPath)
	if dir == "" || romPath == "" {
		return ""
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(filepath.Clean(romPath)))
	base := strings.TrimSuffix(filepath.Base(romPath), filepath.Ext(romPath))
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '_'
	}, base)
	if len(name) > 40 {
		name = name[:40]
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%08x%s", name, h.Sum32(), ppssppResumeExt))
}

// ppssppResumeTargetFor is the state file a start of romPath through launcher
// may save on request: only through the managed launcher of the build PSP
// games use, and only when that build takes the request. "" otherwise.
func ppssppResumeTargetFor(launcher, romPath string) string {
	if !ppssppIsManagedLauncher(launcher) {
		return ""
	}
	st := ppssppSnapshot()
	b, ok := st.build(st.Active)
	if !ok || !strings.EqualFold(filepath.Clean(b.script()), filepath.Clean(launcher)) || !ppssppBinaryCanResume(b.binary()) {
		return ""
	}
	to := ppssppResumeFile(romPath)
	if to == "" {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		logWarn("ppsspp", "no folder for the states saved on request: %v", err)
		return ""
	}
	return to
}

// ppssppPendingResume is the state JukaHub saved for romPath's next start
// (sessionsafe.go's record) while it is still on the card; "" otherwise.
func ppssppPendingResume(romPath string) string {
	m, ok := sessionSafeLastMarker()
	if !ok || m.Resume == "" || m.Path == "" || m.Path != romPath || !regularFile(m.Resume) {
		return ""
	}
	return m.Resume
}

// ppssppResumeEnv adds a PSP start's two variables to env: where the build
// saves on request, and the state to start from when JukaHub saved one for
// this game. Only for the managed launcher: the state reaches PPSSPP through
// jukahub-ppsspp.sh, never the firmware's launcher. changed reports whether
// anything was added.
func ppssppResumeEnv(launcher, romPath string, env []string) (out []string, changed bool) {
	if !ppssppIsManagedLauncher(launcher) || strings.TrimSpace(romPath) == "" {
		return env, false
	}
	out = env
	if to := ppssppResumeTargetFor(launcher, romPath); to != "" {
		out = append(childEnvWithout(out, ppssppEnvSaveTo), ppssppEnvSaveTo+"="+to)
		changed = true
	}
	if state := ppssppPendingResume(romPath); state != "" {
		out = append(childEnvWithout(out, ppssppEnvState), ppssppEnvState+"="+state)
		changed = true
		logInfo("ppsspp", "%s starts from the state JukaHub saved for it (%s)", filepath.Base(romPath), filepath.Base(state))
	}
	return out, changed
}

// ppssppPrepareResumeLaunch is ppssppResumeEnv for a start beside JukaHub
// (launchLibraryGame): the child's environment is the firmware's, as
// screenChildGroupConfigure would give it, plus these two. Main thread,
// before the start.
func ppssppPrepareResumeLaunch(launcher, romPath string, cmd *exec.Cmd) {
	if cmd == nil || !ppssppIsManagedLauncher(launcher) {
		return
	}
	base := cmd.Env
	if base == nil {
		base = screenChildEnv(os.Environ())
	}
	if env, changed := ppssppResumeEnv(launcher, romPath, base); changed {
		cmd.Env = env
	}
}

// ppssppResumePrune keeps one state in the resume folder, keep (and its
// answer file): only the last session is resumed.
func ppssppResumePrune(keep string) {
	dir := ppssppResumeDir()
	if dir == "" || keep == "" || filepath.Dir(filepath.Clean(keep)) != filepath.Clean(dir) {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	keepName := filepath.Base(keep)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == keepName || name == keepName+sessionSafeDoneSuffix {
			continue
		}
		if strings.HasSuffix(name, ppssppResumeExt) || strings.HasSuffix(name, ppssppResumeExt+sessionSafeDoneSuffix) {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				logWarn("ppsspp", "could not remove an older saved state %s: %v", name, err)
			}
		}
	}
}
