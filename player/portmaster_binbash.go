package main

// portmaster_binbash.go — a real /bin/bash for PortMaster and its ports, in
// their own mount namespace, on a TrimUI whose /bin/bash is BusyBox or absent.
//
// JukaHub runs PortMaster's launcher and every port script under a real bash
// (portmaster_runtime.go), but a script that PortMaster or a port starts by
// its own `#!/bin/bash` line is handed by the kernel to /bin/bash itself. On
// a stock Brick that is BusyBox's sh or nothing at all (spruceOS #1986,
// nx-handheld #308), and such a script dies at its first bash array:
// PortMaster's device_info.txt when it is run rather than sourced,
// WestonPack's westonwrap.sh, a port's own helper scripts, and every script
// the firmware's Ports launcher starts. Custom firmwares fix this by
// replacing /bin/bash on the root file system; JukaHub leaves the firmware as
// it is and changes only what one launch sees:
//
//   - the launch starts in a mount namespace of its own (CLONE_NEWNS, as
//     RetroArch on the private glibc does: ra_glibc.go);
//   - its first program, a setup script, binds the firmware's /bin read-only
//     at a spare folder and then binds, read-only, over /bin a folder in tmpfs
//     that holds for every entry of the firmware's /bin a link to it in that
//     spare folder — so sh, ls, mount… stay the very same programs — plus
//     `bash`, a link to the bash this launch already uses (Ubuntu's on the
//     private glibc, through its wrapper, or a real bash the firmware has
//     elsewhere);
//   - then the launch's own command runs. The mounts end with its last process.
//
// A file cannot simply be bound over /bin/bash: there it is a link to busybox
// (a bind follows it and would cover busybox itself) or no file at all. Both
// binds are read-only, so nothing a launch writes under /tmp or /bin reaches
// the firmware's /bin. JukaHub never empties or replaces a folder once made
// (a new one gets a new name); one a port deletes through its /tmp path
// breaks only that launch's /bin, and the next launch makes a new one.
//
// The setup script is named `sh` and gets JukaHub's mount namespace through
// the environment, so whatever names a running child after its arguments
// (Close all, Verified on this device) still finds the port's script.
//
// Whatever fails — no namespace, no mount program, a bind — the launch runs as
// it did before, and its log (data/gamediag/ports/<script>.log) says so. The
// Custom setting portBinBash: "off" turns this off.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// pmBinBashKey is the Custom setting: "auto" (default) or "off".
	pmBinBashKey = "portBinBash"
	// pmBinBashSystem is the path a script's `#!/bin/bash` line names.
	pmBinBashSystem = "/bin/bash"
	// pmBinBashMaxEntries bounds the firmware /bin listing that is mirrored.
	pmBinBashMaxEntries = 4096
	// pmBinBashHostEnv hands the setup script JukaHub's mount namespace; the
	// script removes it before the launch's command runs.
	pmBinBashHostEnv = "JUKAHUB_BINBASH_HOST"
)

// pmBinBashMemo keeps the folder made this session, keyed by what it was made
// from.
var pmBinBashMemo struct {
	mu   sync.Mutex
	key  string
	farm string
}

// pmBinBashOff reads the portBinBash setting.
func pmBinBashOff(config *Config) bool {
	if config == nil {
		return false
	}
	v, ok := customString(config.Variables.Custom, pmBinBashKey)
	return ok && strings.EqualFold(strings.TrimSpace(v), "off")
}

// pmBinBashProbe reports whether /bin/bash runs a bash array (the session's
// firmware check, off the main thread), with a note when it does not.
func pmBinBashProbe(ctx context.Context, env []string, c pmRuntimeCheck) (bool, string) {
	if c.BashOK && c.BashPath == pmBinBashSystem {
		return true, ""
	}
	if c.BashGood == pmBinBashSystem {
		return true, ""
	}
	if _, err := os.Lstat(pmBinBashSystem); err != nil {
		return false, pmBinBashSystem + " does not exist"
	}
	if !executableFile(pmBinBashSystem) {
		return false, pmBinBashSystem + " is a broken link or not a program"
	}
	if pmBashWorks(ctx, pmBinBashSystem, env) {
		return true, ""
	}
	note := pmBinBashSystem + " is not bash"
	if resolved, err := filepath.EvalSymlinks(pmBinBashSystem); err == nil && resolved != pmBinBashSystem {
		note += " (it is " + resolved + ")"
	}
	return false, note
}

// pmBinBashTarget is the bash to put at /bin/bash for a launch: a real bash
// the firmware has elsewhere, else JukaHub's (Ubuntu's on the private glibc,
// through its wrapper). "" when there is none.
func pmBinBashTarget(c pmRuntimeCheck) string {
	if c.BashGood != "" && c.BashGood != pmBinBashSystem && filepath.IsAbs(c.BashGood) && executableFile(c.BashGood) {
		return c.BashGood
	}
	if d := privateGlibcBashWrapperDir(); d != "" {
		return filepath.Join(d, "bash")
	}
	return ""
}

// pmBinBashLink is where the farm's entry for one /bin entry points. Most
// point at the same name in orig, where the setup script binds the firmware's
// /bin. A link with a relative target that climbs out of its folder
// ("../../bin/busybox", common when /bin is usr/bin) would climb out of orig
// instead, so it points at what it reaches from bin: inside bin, that file in
// orig; elsewhere, that path as it is.
func pmBinBashLink(bin, orig, name, target string) string {
	if target == "" || filepath.IsAbs(target) || !strings.Contains(target, "..") {
		return filepath.Join(orig, name)
	}
	abs := filepath.Clean(filepath.Join(bin, target))
	if rel, err := filepath.Rel(bin, abs); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Join(orig, rel)
	}
	return abs
}

// pmBinBashFarm makes (once per firmware /bin and target) the folder that is
// bound over /bin: a link per entry of the firmware's /bin (pmBinBashLink) and
// bash → target. bin is /bin with its links followed (the folder that is
// actually covered), orig the spare folder the setup script binds it at.
func pmBinBashFarm(target string) (farm, orig, bin string, err error) {
	bin, err = filepath.EvalSymlinks("/bin")
	if err != nil {
		return "", "", "", err
	}
	if !filepath.IsAbs(bin) || !dirExists(bin) {
		return "", "", "", fmt.Errorf("%s is not a folder", bin)
	}
	entries, err := os.ReadDir(bin)
	if err != nil {
		return "", "", "", err
	}
	if len(entries) == 0 || len(entries) > pmBinBashMaxEntries {
		return "", "", "", fmt.Errorf("%s has %d entries", bin, len(entries))
	}
	base := filepath.Join(os.TempDir(), pmTmpDirName, "ns")
	orig = filepath.Join(base, "orig-bin")
	type link struct{ name, to string }
	links := make([]link, 0, len(entries))
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%s\n", target, bin, orig)
	for _, e := range entries {
		name := e.Name()
		if name == "bash" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
			continue
		}
		t := ""
		if e.Type()&os.ModeSymlink != 0 {
			t, _ = os.Readlink(filepath.Join(bin, name))
		}
		to := pmBinBashLink(bin, orig, name, t)
		links = append(links, link{name, to})
		fmt.Fprintf(h, "%s\x00%s\n", name, to)
	}
	key := hex.EncodeToString(h.Sum(nil))[:16]
	pmBinBashMemo.mu.Lock()
	defer pmBinBashMemo.mu.Unlock()
	if pmBinBashMemo.key == key && pmBinBashFarmComplete(pmBinBashMemo.farm, target, len(links)) && dirExists(orig) {
		return pmBinBashMemo.farm, orig, bin, nil
	}
	if err := os.MkdirAll(orig, 0o755); err != nil {
		return "", "", "", err
	}
	farm = filepath.Join(base, "bin-"+key)
	// A folder of this name was renamed into place only after its last link,
	// but a launch may have changed it since: such a one is left alone (a
	// running launch may still see it) and a new one is made beside it.
	if !pmBinBashFarmComplete(farm, target, len(links)) {
		if _, err := os.Lstat(farm); err == nil {
			farm = fmt.Sprintf("%s-%d", farm, time.Now().UnixNano())
		}
		tmp := fmt.Sprintf("%s.tmp-%d", farm, os.Getpid())
		_ = os.RemoveAll(tmp) // only ever a half-made folder of this process
		if err := os.MkdirAll(tmp, 0o755); err != nil {
			return "", "", "", err
		}
		for _, l := range links {
			if err := os.Symlink(l.to, filepath.Join(tmp, l.name)); err != nil {
				_ = os.RemoveAll(tmp)
				return "", "", "", err
			}
		}
		if err := os.Symlink(target, filepath.Join(tmp, "bash")); err != nil {
			_ = os.RemoveAll(tmp)
			return "", "", "", err
		}
		if err := os.Rename(tmp, farm); err != nil {
			_ = os.RemoveAll(tmp)
			return "", "", "", err
		}
	}
	pmBinBashMemo.key, pmBinBashMemo.farm = key, farm
	return farm, orig, bin, nil
}

// pmBinBashFarmComplete reports a farm folder whose bash link names target
// and that holds every other link (entries + bash).
func pmBinBashFarmComplete(farm, target string, entries int) bool {
	if farm == "" {
		return false
	}
	link, err := os.Readlink(filepath.Join(farm, "bash"))
	if err != nil || link != target {
		return false
	}
	list, err := os.ReadDir(farm)
	return err == nil && len(list) == entries+1
}

// pmBinBashSetupText is the first program of a launch with a real bash at
// /bin/bash. JukaHub's own mount namespace comes in pmBinBashHostEnv; the
// arguments are the launch's command. Whatever fails, that command runs, and
// /bin is never left covered by a writable bind.
func pmBinBashSetupText(mountBin, umountBin, readlinkBin, bin, orig, farm string) string {
	m, u := shellQuote(mountBin), shellQuote(umountBin)
	b, o, f := shellQuote(bin), shellQuote(orig), shellQuote(farm)
	return "#!/bin/sh\n" +
		"# Written by JukaHub (portmaster_binbash.go): the first program of a PortMaster\n" +
		"# launch on firmware whose /bin/bash is not bash. In the launch's own mount\n" +
		"# namespace the firmware's /bin is bound read-only at a spare folder, and a\n" +
		"# read-only folder of links to it plus a real bash is bound over /bin; then the\n" +
		"# launch's command runs. Nothing outside the launch changes; its mounts end with it.\n" +
		"host=\"$" + pmBinBashHostEnv + "\"\n" +
		"unset " + pmBinBashHostEnv + "\n" +
		"ns=$(" + shellQuote(readlinkBin) + " /proc/self/ns/mnt 2>/dev/null)\n" +
		"if [ -z \"$host\" ] || [ -z \"$ns\" ] || [ \"$ns\" = \"$host\" ]; then\n" +
		"\techo \"jukahub: /bin/bash stays the firmware's: the launch has no mount namespace of its own\" >&2\n" +
		"\texec \"$@\"\n" +
		"fi\n" +
		// The two-argument remount first: BusyBox's one-argument form looks the
		// folder up in /etc/mtab, which a -n bind never entered when that is a
		// file. The one-argument form keeps the mount's other flags, which a
		// kernel may insist on.
		"jh_ro() {\n" +
		"\t" + m + " -n -o remount,bind,ro \"$1\" \"$2\" 2>/dev/null || " + m + " -n -o remount,bind,ro \"$2\"\n" +
		"}\n" +
		"if " + m + " -n -o bind " + b + " " + o + " && jh_ro " + b + " " + o + "; then\n" +
		"\tif " + m + " -n -o bind " + f + " " + b + "; then\n" +
		"\t\tif jh_ro " + f + " " + b + "; then\n" +
		"\t\t\techo \"jukahub: /bin/bash is a real bash for this launch (its own mount namespace)\" >&2\n" +
		"\t\telse\n" +
		"\t\t\t" + u + " -n " + b + " 2>/dev/null\n" +
		"\t\t\techo \"jukahub: /bin/bash stays the firmware's: /bin could not be made read-only\" >&2\n" +
		"\t\tfi\n" +
		"\telse\n" +
		"\t\techo \"jukahub: /bin/bash stays the firmware's: binding over /bin failed\" >&2\n" +
		"\tfi\n" +
		"else\n" +
		"\t" + u + " -n " + o + " 2>/dev/null\n" +
		"\techo \"jukahub: /bin/bash stays the firmware's: binding /bin read-only failed\" >&2\n" +
		"fi\n" +
		"exec \"$@\"\n"
}

// pmBinBashWriteSetup writes the setup script as <ns>/binbash-<hash>/sh
// (named after its content, so a script a running launch reads is never
// rewritten).
func pmBinBashWriteSetup(base, text string) (string, error) {
	sum := sha256.Sum256([]byte(text))
	dir := filepath.Join(base, "binbash-"+hex.EncodeToString(sum[:])[:12])
	file := filepath.Join(dir, "sh")
	if old, err := os.ReadFile(file); err == nil && string(old) == text {
		return file, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o755); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return file, nil
}

// pmBinBashPlan is a prepared /bin/bash cover for one launch.
type pmBinBashPlan struct {
	sh, setup, host string
}

// pmBinBashPrepare decides whether a PortMaster or port launch needs a real
// /bin/bash of its own and gets the folder and the setup script ready. The
// note is for the launch log ("" when nothing was needed); a nil plan leaves
// the launch as it is. Main thread.
func pmBinBashPrepare(config *Config) (*pmBinBashPlan, string) {
	if !pmRuntimeApplies() {
		return nil, ""
	}
	c := pmRuntimeSnapshot()
	if !c.Done || c.BinBashOK {
		return nil, ""
	}
	why := setOr(c.BinBashNote, "/bin/bash is not bash")
	if pmBinBashOff(config) {
		return nil, why + "; portBinBash is off, so a script's own #!/bin/bash line gets the firmware's"
	}
	plan, target, err := pmBinBashMake(c)
	if err != nil {
		logWarn("portmaster", "/bin/bash stays the firmware's for this launch: %v", err)
		return nil, why + "; JukaHub could not give this launch its own: " + err.Error()
	}
	return plan, why + ", so this launch runs in its own mount namespace where /bin/bash is " + target +
		" (the first line of the output below says whether that worked)"
}

// pmBinBashMake does pmBinBashPrepare's work.
func pmBinBashMake(c pmRuntimeCheck) (*pmBinBashPlan, string, error) {
	target := pmBinBashTarget(c)
	if target == "" {
		return nil, "", errors.New("no real bash is installed yet")
	}
	host, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil || host == "" {
		return nil, "", errors.New("this system does not show mount namespaces")
	}
	sh, mountBin, umountBin, readlinkBin := raGlibcTool("sh"), raGlibcTool("mount"), raGlibcTool("umount"), raGlibcTool("readlink")
	if sh == "" || mountBin == "" || umountBin == "" || readlinkBin == "" {
		return nil, "", errors.New("the firmware has no sh, mount, umount or readlink program")
	}
	if !raGlibcNamespaceOK() {
		return nil, "", errors.New("a launch cannot have a mount namespace of its own here")
	}
	farm, orig, bin, err := pmBinBashFarm(target)
	if err != nil {
		return nil, "", fmt.Errorf("preparing the /bin folder: %w", err)
	}
	setup, err := pmBinBashWriteSetup(filepath.Dir(farm), pmBinBashSetupText(mountBin, umountBin, readlinkBin, bin, orig, farm))
	if err != nil {
		return nil, "", fmt.Errorf("writing the setup script: %w", err)
	}
	return &pmBinBashPlan{sh: sh, setup: setup, host: host}, target, nil
}

// apply makes cmd start in a mount namespace of its own through the setup
// script. It runs last, after screenChildGroupConfigure (which reads the
// command as it was) and just before the start; a nil plan does nothing.
func (p *pmBinBashPlan) apply(cmd *exec.Cmd) {
	if p == nil || cmd == nil || cmd.Process != nil || cmd.Err != nil || cmd.Path == "" || len(cmd.Args) == 0 {
		return
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	if !raGlibcNamespace(cmd) {
		return
	}
	cmd.Env = append(pmEnvDrop(env, pmBinBashHostEnv), pmBinBashHostEnv+"="+p.host)
	cmd.Args = append([]string{"sh", p.setup, cmd.Path}, cmd.Args[1:]...)
	cmd.Path = p.sh
}
