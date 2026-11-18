//go:build !linux

package main

// usbmtp_other.go — "USB file transfer" off Linux. Only the handheld turns
// its port into a USB device; on a computer the switch is hidden
// (usbMtpSupported) and these keep the shared code compiling.

import "errors"

func usbMtpPlatformStart(usbMtpStartConfig) (*mtpServer, func(), error) {
	return nil, nil, errors.New("USB file transfer works on the handheld only")
}

func usbMtpSerial() string { return "" }
