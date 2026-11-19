// sdinit is the first program in the Smart Pro SD image's tiny initramfs.
// This firmware rewrites root= at boot, so its U-Boot environment cannot be
// trusted to point at the card. rdinit finds the SD disk itself, mounts the
// card's rootfs partition, and replaces itself with JukaHub's process 1.
// The internal eMMC is never mounted.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type config struct {
	partN int
	name  string
	fs    string
	init  string
}

func readConfig() (config, error) {
	data, err := os.ReadFile("/jukahub-root")
	if err != nil {
		return config{}, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			values[key] = value
		}
	}
	n, err := strconv.Atoi(values["ROOT_PARTN"])
	if err != nil || n < 1 || n > 128 {
		return config{}, fmt.Errorf("invalid root partition number")
	}
	c := config{partN: n, name: values["ROOT_NAME"], fs: values["ROOT_FSTYPE"], init: values["INIT"]}
	if c.name == "" || strings.ContainsAny(c.name, "/ \t\r\n") ||
		(c.fs != "ext4" && c.fs != "squashfs") ||
		(c.init != "/jukahub/jukaboot" && c.init != "/jukahub/init") {
		return config{}, fmt.Errorf("invalid root image configuration")
	}
	return c, nil
}

func mountEarly() error {
	for _, m := range []struct{ source, target, fs string }{
		{"proc", "/proc", "proc"},
		{"sysfs", "/sys", "sysfs"},
		{"devtmpfs", "/dev", "devtmpfs"},
	} {
		if err := os.MkdirAll(m.target, 0755); err != nil {
			return err
		}
		if err := syscall.Mount(m.source, m.target, m.fs, 0, ""); err != nil && err != syscall.EBUSY {
			return fmt.Errorf("mount %s: %w", m.target, err)
		}
	}
	// A stock hotplug rule must not mount the internal storage while we search.
	_ = os.WriteFile("/proc/sys/kernel/hotplug", []byte("\n"), 0644)
	_ = os.WriteFile("/sys/kernel/uevent_helper", []byte("\n"), 0644)
	return nil
}

func ueventValue(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, key+"="); ok {
			return value
		}
	}
	return ""
}

func sdPartition(c config) string {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		disk := entry.Name()
		if !strings.HasPrefix(disk, "mmcblk") {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimPrefix(disk, "mmcblk")); err != nil {
			continue // excludes the eMMC boot and RPMB devices
		}
		kind, err := os.ReadFile(filepath.Join("/sys/block", disk, "device/type"))
		if err != nil || strings.TrimSpace(string(kind)) != "SD" {
			continue
		}
		part := fmt.Sprintf("%sp%d", disk, c.partN)
		uevent := filepath.Join("/sys/class/block", part, "uevent")
		if ueventValue(uevent, "PARTN") != strconv.Itoa(c.partN) ||
			ueventValue(uevent, "PARTNAME") != c.name {
			continue
		}
		path := filepath.Join("/dev", part)
		if st, err := os.Stat(path); err == nil && st.Mode()&os.ModeDevice != 0 {
			return path
		}
	}
	return ""
}

func run() error {
	if err := mountEarly(); err != nil {
		return err
	}
	c, err := readConfig()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	var device string
	for time.Now().Before(deadline) {
		device = sdPartition(c)
		if device != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if device == "" {
		return fmt.Errorf("no %s partition %d on an SD card", c.name, c.partN)
	}
	if err := os.MkdirAll("/newroot", 0755); err != nil {
		return err
	}
	if err := syscall.Mount(device, "/newroot", c.fs, syscall.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("mount %s: %w", device, err)
	}
	if st, err := os.Stat("/newroot" + c.init); err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("the SD root has no %s", c.init)
	}
	if err := syscall.Chroot("/newroot"); err != nil {
		return err
	}
	if err := syscall.Chdir("/"); err != nil {
		return err
	}
	return syscall.Exec(c.init, []string{c.init}, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root"})
}

func main() {
	if err := run(); err != nil {
		message := "jukahub sdinit: " + err.Error() + "\n"
		_, _ = os.Stderr.WriteString(message)
		_ = os.WriteFile("/dev/kmsg", []byte(message), 0644)
		// A failed SD image must not silently boot the internal system. Power
		// off so taking the card out is the obvious recovery path.
		time.Sleep(2 * time.Second)
		_ = syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
		for {
			time.Sleep(time.Hour)
		}
	}
}
