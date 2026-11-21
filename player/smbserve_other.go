//go:build !linux

package main

// smbserve_other.go — no SMB share off Linux: Windows (and macOS) keep port
// 445 for their own file sharing, so the share runs on the handheld only.

import "errors"

var errSMBServeHandheldOnly = errors.New("the SMB share runs on the handheld only — this computer keeps port 445 for its own file sharing")

func smbServeSupported() error { return errSMBServeHandheldOnly }

func smbServeDiskSpace(string) (total, free uint64, ok bool) { return 0, 0, false }

func smbServeErrnoStatus(error) (uint32, bool) { return 0, false }
