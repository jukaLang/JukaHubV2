//go:build linux

package main

// wine_display_linux.go — the parts of the WestonPack display that need the
// Linux kernel: mounting the runtime's SquashFS image, and labelling input
// devices for udev so Weston's libinput accepts the pad and JukaHub's mouse
// (wine_display.go explains both).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	// westonMountDir is where JukaHub mounts a WestonPack image: a folder of its
	// own, so a PortMaster port's /tmp/weston is never unmounted under it, nor
	// JukaHub's under a port.
	westonMountDir = "/tmp/jukahub-weston"
	// wineLoopClearFD is LOOP_CLR_FD: detach a loop device from its file.
	wineLoopClearFD = 0x4C01
	// wineUdevRuleName is the rule file JukaHub adds to udev's runtime rules.
	wineUdevRuleName = "61-jukahub-input-id.rules"
)

// wineUdevRule is the rule a standard udev ships as 60-input-id.rules and the
// firmware lacks: it tags every input device (ID_INPUT, ID_INPUT_KEYBOARD,
// ID_INPUT_MOUSE, ID_INPUT_JOYSTICK, ...), which is what libinput checks.
const wineUdevRule = `# Written by JukaHub for Windows apps (WestonPack). The firmware's udev has no
# input_id rule, and Weston's libinput ignores input devices udev did not tag.
# This folder is in RAM: the rule is gone after a restart.
ACTION=="remove", GOTO="jukahub_input_id_end"
SUBSYSTEM=="input", ENV{ID_INPUT}=="", IMPORT{builtin}="input_id"
LABEL="jukahub_input_id_end"
`

// wineUdevRuntimeDirs are where udev reads its runtime rules on the firmwares
// JukaHub knows (the stock TrimUI build keeps /run in /tmp/run).
var wineUdevRuntimeDirs = []string{"/run/udev", "/var/run/udev", "/tmp/run/udev"}

// wineMountWestonImage makes image's westonwrap.sh reachable: an existing mount
// of the same image is reused (PortMaster's /tmp/weston, or JukaHub's own after
// a crash), otherwise the image is mounted read-only at westonMountDir. mounted
// reports that the caller owns the mount and unmounts it after the session.
func wineMountWestonImage(image string, log wineLog) (string, bool, error) {
	if strings.TrimSpace(image) == "" {
		return "", false, withLaunchFailureKind(launchFailureRuntimeMissing, errors.New("WestonPack is not installed"))
	}
	if os.Geteuid() != 0 {
		return "", false, errors.New("mounting WestonPack needs root, and JukaHub is not running as root")
	}
	abs, err := filepath.Abs(image)
	if err != nil {
		return "", false, err
	}
	mounts := wineReadMounts()
	for _, m := range mounts {
		if m.fstype != "squashfs" || !wineSameFile(wineLoopBackingFile(m.source), abs) {
			continue
		}
		if !regularFile(filepath.Join(m.dir, "westonwrap.sh")) {
			continue
		}
		log.printf("display: WestonPack is already mounted at %s; using it\n", m.dir)
		return m.dir, m.dir == westonMountDir, nil
	}
	// JukaHub's mount point holding something else (an older image): let it go
	// before mounting this one there.
	for _, m := range mounts {
		if m.dir == westonMountDir {
			wineUnmountWeston(westonMountDir, log)
			break
		}
	}
	if err := os.MkdirAll(westonMountDir, 0o755); err != nil {
		return "", false, fmt.Errorf("prepare %s for WestonPack: %w", westonMountDir, err)
	}
	mountBin := "mount"
	if path, err := exec.LookPath("mount"); err == nil && path != "" {
		mountBin = path
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, mountBin, "-t", "squashfs", "-o", "loop,ro", abs, westonMountDir).CombinedOutput()
	if err != nil {
		reason := strings.TrimSpace(string(out))
		if reason == "" {
			reason = err.Error()
		}
		return "", false, fmt.Errorf("the system refused to mount WestonPack (%s): %s", filepath.Base(abs), firstLine(reason))
	}
	log.printf("display: mounted %s at %s\n", abs, westonMountDir)
	return westonMountDir, true, nil
}

// wineUnmountWeston unmounts dir — lazily when something still holds it, so the
// session's end never waits on it — and frees its loop device when the mount
// did not ask the kernel to.
func wineUnmountWeston(dir string, log wineLog) {
	loop := ""
	for _, m := range wineReadMounts() {
		if m.dir == dir {
			loop = m.source
		}
	}
	err := syscall.Unmount(dir, 0)
	if errors.Is(err, syscall.EBUSY) {
		log.printf("display: %s is still in use; unmounting it lazily\n", dir)
		err = syscall.Unmount(dir, syscall.MNT_DETACH)
	}
	if err != nil {
		if !errors.Is(err, syscall.EINVAL) { // EINVAL: nothing mounted there
			log.printf("display: unmount %s: %v\n", dir, err)
		}
		return
	}
	log.printf("display: unmounted %s\n", dir)
	wineReleaseLoop(loop, log)
}

// wineReleaseLoop detaches a loop device that still points at its file after
// the unmount (busybox mount does not always set autoclear). A device the
// kernel already freed, or one a lazy unmount still uses, is left alone.
func wineReleaseLoop(dev string, log wineLog) {
	if !strings.HasPrefix(filepath.Base(dev), "loop") || wineLoopBackingFile(dev) == "" {
		return
	}
	fd, err := syscall.Open(dev, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	defer syscall.Close(fd)
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), wineLoopClearFD, 0); e != 0 && e != syscall.ENXIO && e != syscall.EBUSY {
		log.printf("display: free %s: %v\n", dev, e)
	}
}

// wineReadMounts reads the kernel's mount table.
func wineReadMounts() []procMount {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}
	return parseProcMounts(string(data))
}

// wineLoopBackingFile is the file behind a loop device (/dev/loop3 or
// /dev/block/loop3), or "" for anything else.
func wineLoopBackingFile(dev string) string {
	name := filepath.Base(dev)
	if !strings.HasPrefix(name, "loop") {
		return ""
	}
	data, err := os.ReadFile(filepath.Join("/sys/block", name, "loop", "backing_file"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(data)), "(deleted)"))
}

// wineSameFile reports whether two paths name the same file.
func wineSameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

// wineLabelInputDevices makes udev tag input devices for Weston's libinput:
// the rule goes into each runtime rules folder udev has, udev reloads, and the
// input devices — only those, never every device — are re-announced. Done once
// per boot: with the rule loaded, udev tags new devices (JukaHub's mouse) as
// they appear. Every step is bounded and logged; nothing here stops a launch.
func wineLabelInputDevices(log wineLog) {
	udevadm := wineUdevadm()
	if udevadm == "" {
		log.printf("input: udevadm not found; Weston may not find the pad or the mouse\n")
		return
	}
	seen := map[string]bool{}
	found, wrote := false, false
	for _, base := range wineUdevRuntimeDirs {
		real, err := filepath.EvalSymlinks(base)
		if err != nil || seen[real] {
			continue
		}
		seen[real] = true
		if info, err := os.Stat(real); err != nil || !info.IsDir() {
			continue
		}
		found = true
		path := filepath.Join(real, "rules.d", wineUdevRuleName)
		if old, err := os.ReadFile(path); err == nil && string(old) == wineUdevRule {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			log.printf("input: cannot add the udev rule in %s: %v\n", filepath.Dir(path), err)
			continue
		}
		if err := os.WriteFile(path, []byte(wineUdevRule), 0o644); err != nil {
			log.printf("input: cannot add the udev rule %s: %v\n", path, err)
			continue
		}
		wrote = true
		log.printf("input: added the udev input rule %s\n", path)
	}
	if !found {
		log.printf("input: udev has no runtime rules folder (/run/udev); Weston may not find the pad or the mouse\n")
		return
	}
	if !wrote {
		return
	}
	if err := wineRunTool(log, 5*time.Second, udevadm, "control", "--reload"); err != nil {
		return
	}
	if err := wineRunTool(log, 10*time.Second, udevadm, "trigger", "--subsystem-match=input", "--action=change"); err != nil {
		return
	}
	_ = wineRunTool(log, 7*time.Second, udevadm, "settle", "--timeout=5")
}

// wineSettleInput waits a moment for udev to finish with the devices that just
// appeared (JukaHub's mouse), so Weston finds them tagged when it starts.
func wineSettleInput(log wineLog) {
	if udevadm := wineUdevadm(); udevadm != "" {
		_ = wineRunTool(log, 5*time.Second, udevadm, "settle", "--timeout=3")
	}
}

// wineUdevadm finds udevadm on PATH or in the usual system folders.
func wineUdevadm() string {
	if path, err := exec.LookPath("udevadm"); err == nil && path != "" {
		return path
	}
	for _, path := range []string{"/sbin/udevadm", "/bin/udevadm", "/usr/sbin/udevadm", "/usr/bin/udevadm"} {
		if executableFile(path) {
			return path
		}
	}
	return ""
}

// wineRunTool runs one system tool with a time limit and logs a failure with
// what the tool printed.
func wineRunTool(log wineLog, limit time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		log.printf("input: %s %s: %v %s\n", filepath.Base(name), strings.Join(args, " "), err, firstLine(strings.TrimSpace(string(out))))
	}
	return err
}
