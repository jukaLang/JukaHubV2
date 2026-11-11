//go:build windows

package main

// files_drives_windows.go — the Windows half of the Files Locations list: every
// drive letter the system reports, the hidden-file attribute, and free space
// (the POSIX statfs behind freeBytes does not exist here).

import (
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// filesDrives lists the drives Windows has mounted, with what kind of drive each
// one is. A drive letter without a root directory (a card reader slot with no
// card, a stale mapping) is left out rather than offered as a folder that will
// not open.
func filesDrives() []filesLocation {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	var out []filesLocation
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		letter := string(rune('A' + i))
		root := letter + `:\`
		kind := "Local disk"
		if p, perr := windows.UTF16PtrFromString(root); perr == nil {
			switch windows.GetDriveType(p) {
			case windows.DRIVE_REMOVABLE:
				kind = "Removable drive"
			case windows.DRIVE_REMOTE:
				kind = "Network drive"
			case windows.DRIVE_CDROM:
				kind = "CD/DVD drive"
			case windows.DRIVE_RAMDISK:
				kind = "RAM disk"
			case windows.DRIVE_NO_ROOT_DIR, windows.DRIVE_UNKNOWN:
				continue
			}
		}
		out = append(out, filesLocation{
			Label:  kind + " (" + letter + ":)",
			Path:   root,
			Kind:   filesLocDrive,
			Detail: kind,
		})
	}
	return out
}

// filesEntryHidden reports whether a listing should hide an entry while "Show
// hidden files" is off: a dotfile, or anything Explorer itself hides (the
// hidden attribute, which is how desktop.ini, $Recycle.Bin and the profile's
// AppData folder stay out of sight).
func filesEntryHidden(e os.DirEntry) bool {
	if strings.HasPrefix(e.Name(), ".") {
		return true
	}
	info, err := e.Info()
	if err != nil {
		return false
	}
	if attrs, ok := info.Sys().(*syscall.Win32FileAttributeData); ok && attrs != nil {
		return attrs.FileAttributes&syscall.FILE_ATTRIBUTE_HIDDEN != 0
	}
	return false
}

// filesFreeSpace is the space left for this user on the volume holding path.
func filesFreeSpace(path string) (int64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, err
	}
	return int64(avail), nil
}

// filesSkipFreeSpace reports whether asking a location for its free space could
// hang: a network drive whose server is gone blocks the call for the network
// timeout, so its row simply goes without the number.
func filesSkipFreeSpace(loc filesLocation) bool {
	return loc.Detail == "Network drive"
}

// filesSetHidden gives path Windows' hidden attribute, so a working folder (the
// staging folder of an extraction) stays out of Explorer as well as out of
// Files.
func filesSetHidden(path string) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return
	}
	_ = windows.SetFileAttributes(p, attrs|windows.FILE_ATTRIBUTE_HIDDEN)
}
