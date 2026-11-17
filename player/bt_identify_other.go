//go:build !linux

package main

import "errors"

// btIdentifyPad is Linux only (bt_identify_linux.go): elsewhere the system
// pairs controllers and the Bluetooth page is not shown.
func btIdentifyPad(string) error {
	return errors.New("identifying a controller needs Linux")
}
