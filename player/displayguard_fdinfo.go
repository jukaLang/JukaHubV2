package main

import (
	"strconv"
	"strings"
)

// displayGuardFDInfoReadOnly reads the flags field in Linux fdinfo. The low
// two octal bits are the access mode (0 = read-only, 1 = write-only, 2 =
// read/write). A missing or malformed field remains a possible writer.
func displayGuardFDInfoReadOnly(info string) bool {
	for _, line := range strings.Split(info, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || key != "flags" {
			continue
		}
		flags, err := strconv.ParseUint(strings.TrimSpace(value), 8, 64)
		return err == nil && flags&3 == 0
	}
	return false
}
