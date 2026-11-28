//go:build linux

package main

// xboxboost_linux.go — the hardware half of Xbox boost (xboxboost.go): parked
// cores, the GPU's and the memory controller's devfreq nodes, the card's
// read-ahead, the game's priority and the Wi-Fi radios' power saving.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// xboxBoostCmdTimeout bounds one iw / iwconfig run.
const xboxBoostCmdTimeout = 3 * time.Second

// xboxBoostHardwarePlan is the boost's sysfs writes for a launch of target, in
// the order they must happen: parked cores first (a core must be online before
// the launch profile can set its governor), then the GPU, the memory clock
// and the read-ahead of the disk target is read from.
func xboxBoostHardwarePlan(target string) []saverSysfsWrite {
	plan := xboxBoostCorePlan(cpufreqRoot)
	plan = append(plan, xboxBoostGPUPlan(xboxBoostDevfreqRoot)...)
	plan = append(plan, xboxBoostMemPlan(xboxBoostDevfreqRoot)...)
	return append(plan, xboxBoostReadAheadPlan(target)...)
}

// xboxBoostCorePlan brings every parked CPU online: cpuN/online reading 0.
// cpu0 usually has no switch at all, and a CPU the kernel will not take
// online just refuses the write.
func xboxBoostCorePlan(root string) []saverSysfsWrite {
	matches, _ := filepath.Glob(filepath.Join(root, "cpu[0-9]*", "online"))
	sort.Slice(matches, func(i, j int) bool { return xboxBoostCPUIndex(matches[i]) < xboxBoostCPUIndex(matches[j]) })
	var plan []saverSysfsWrite
	for _, p := range matches {
		if !xboxBoostWritable(p) {
			continue
		}
		if saverReadTrimmed(p) == "0" {
			plan = append(plan, saverSysfsWrite{Path: p, Original: "0", Written: "1"})
		}
	}
	return plan
}

// xboxBoostCPUIndex is N of a .../cpuN/online path, for a numeric order.
func xboxBoostCPUIndex(p string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(filepath.Dir(p)), "cpu"))
	if err != nil {
		return 1 << 30
	}
	return n
}

// xboxBoostGPUPlan raises every GPU devfreq node (a name with "gpu", as the
// JukaHub OS layers find it): the performance governor where the node offers
// one, else the floor up to the ceiling, so whatever governor runs cannot
// clock the GPU down between frames.
func xboxBoostGPUPlan(root string) []saverSysfsWrite {
	return xboxBoostDevfreqPlan(root, func(name string) bool { return strings.Contains(strings.ToLower(name), "gpu") })
}

// xboxBoostMemPlan raises the memory controller's devfreq node the same way
// (xboxBoostMemName: "dramfreq", "dmc"…), where the kernel has one.
func xboxBoostMemPlan(root string) []saverSysfsWrite {
	return xboxBoostDevfreqPlan(root, xboxBoostMemName)
}

// xboxBoostDevfreqPlan raises every devfreq node under root whose name match
// accepts: its performance governor, else its floor up to its ceiling.
func xboxBoostDevfreqPlan(root string, match func(name string) bool) []saverSysfsWrite {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var plan []saverSysfsWrite
	for _, e := range entries {
		if !match(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		govPath := filepath.Join(dir, "governor")
		gov := saverReadTrimmed(govPath)
		if gov == "performance" {
			continue
		}
		if gov != "" && xboxBoostListed(saverReadTrimmed(filepath.Join(dir, "available_governors")), "performance") {
			plan = append(plan, saverSysfsWrite{Path: govPath, Original: gov, Written: "performance"})
			continue
		}
		minPath := filepath.Join(dir, "min_freq")
		minRaw := saverReadTrimmed(minPath)
		maxRaw := saverReadTrimmed(filepath.Join(dir, "max_freq"))
		lo, errLo := strconv.ParseInt(minRaw, 10, 64)
		hi, errHi := strconv.ParseInt(maxRaw, 10, 64)
		if errLo == nil && errHi == nil && lo > 0 && hi > lo {
			plan = append(plan, saverSysfsWrite{Path: minPath, Original: minRaw, Written: maxRaw})
		}
	}
	return plan
}

// xboxBoostReadAheadPlan raises the read-ahead of the disk target is read
// from to xboxBoostReadAheadKB, where it is lower.
func xboxBoostReadAheadPlan(target string) []saverSysfsWrite {
	disk := xboxBoostDiskOf(target)
	if disk == "" {
		return nil
	}
	p := filepath.Join(xboxBoostBlockRoot, disk, "queue", "read_ahead_kb")
	cur := saverReadTrimmed(p)
	n, err := strconv.Atoi(cur)
	if err != nil || n < 0 || n >= xboxBoostReadAheadKB || !xboxBoostWritable(p) {
		return nil
	}
	return []saverSysfsWrite{{Path: p, Original: cur, Written: strconv.Itoa(xboxBoostReadAheadKB)}}
}

// xboxBoostDiskOf names the whole disk (not the partition) holding path —
// mmcblk1 for a game on the card — through sdMoveDiskOf (sdmove_linux.go).
// "" for a file on no block device (a network drive, tmpfs, a FUSE mount) or
// a disk without a queue to tune.
func xboxBoostDiskOf(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	name := sdMoveDiskOf(path)
	if !xboxBoostDiskName(name) || !regularFile(filepath.Join(xboxBoostBlockRoot, name, "queue", "read_ahead_kb")) {
		return ""
	}
	return name
}

// xboxBoostListed reports whether a space-separated sysfs list names want.
func xboxBoostListed(list, want string) bool {
	for _, f := range strings.Fields(list) {
		if f == want {
			return true
		}
	}
	return false
}

// xboxBoostRenice sets the game's process group (the launch leads its own:
// screenChildGroupConfigure) to xboxBoostNice. What the launch script starts
// afterwards inherits it.
func xboxBoostRenice(pid int) error {
	return syscall.Setpriority(syscall.PRIO_PGRP, pid, xboxBoostNice)
}

// xboxBoostIface reports a network interface name safe to hand to iw.
func xboxBoostIface(name string) bool {
	if name == "" || len(name) > 15 || strings.HasPrefix(name, "-") {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}

// xboxBoostRun runs one radio command with a time limit.
func xboxBoostRun(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), xboxBoostCmdTimeout)
	defer cancel()
	out, err := hiddenCommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// xboxBoostWiFiOff switches power saving off on every wireless interface that
// has it on — iw where the firmware has it, else iwconfig — and returns what
// it switched ("iw:wlan0", "iwconfig:wlan0") for xboxBoostWiFiOn.
func xboxBoostWiFiOff() []string {
	matches, _ := filepath.Glob("/sys/class/net/*/wireless")
	if len(matches) == 0 {
		return nil
	}
	iw, _ := exec.LookPath("iw")
	iwconfig, _ := exec.LookPath("iwconfig")
	var done []string
	for _, m := range matches {
		name := filepath.Base(filepath.Dir(m))
		if !xboxBoostIface(name) {
			continue
		}
		switch {
		case iw != "":
			out, err := xboxBoostRun(iw, "dev", name, "get", "power_save")
			if err != nil || !strings.Contains(strings.ToLower(out), "save: on") {
				continue
			}
			if _, err := xboxBoostRun(iw, "dev", name, "set", "power_save", "off"); err == nil {
				done = append(done, "iw:"+name)
			}
		case iwconfig != "":
			out, err := xboxBoostRun(iwconfig, name)
			if err != nil || !strings.Contains(out, "Management:on") {
				continue
			}
			if _, err := xboxBoostRun(iwconfig, name, "power", "off"); err == nil {
				done = append(done, "iwconfig:"+name)
			}
		}
	}
	return done
}

// xboxBoostWiFiOn puts power saving back on the radios xboxBoostWiFiOff
// switched. Entries come back from the crash marker too, so each is checked.
func xboxBoostWiFiOn(radios []string) {
	for _, r := range radios {
		tool, name, ok := strings.Cut(r, ":")
		if !ok || !xboxBoostIface(name) {
			continue
		}
		var err error
		switch tool {
		case "iw":
			_, err = xboxBoostRun("iw", "dev", name, "set", "power_save", "on")
		case "iwconfig":
			_, err = xboxBoostRun("iwconfig", name, "power", "on")
		default:
			continue
		}
		if err != nil {
			logInfo("xbox", "Wi-Fi %s: power saving not put back (%v)", name, err)
		}
	}
}
