//go:build linux

package main

// displaytune_linux.go — the Allwinner display driver's picture nodes for
// displaytune.go. The disp2 driver publishes them under one attribute group;
// they are root-writable (0644), and JukaHub runs as root on the TrimUI.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// displayTuneDir is the disp2 driver's attribute group.
const displayTuneDir = "/sys/class/disp/disp/attr"

// displayTuneProbe reports whether a node is there, reads as a number and
// opens for writing.
func displayTuneProbe(node string) bool {
	if _, ok := displayTuneRead(node); !ok {
		return false
	}
	return canWriteFile(filepath.Join(displayTuneDir, node))
}

// displayTuneRead is a node's value: the first number it prints.
func displayTuneRead(node string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(displayTuneDir, node))
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, false
	}
	return v, true
}

func displayTuneWrite(node string, v int) error {
	return os.WriteFile(filepath.Join(displayTuneDir, node), []byte(strconv.Itoa(v)), 0o644)
}
