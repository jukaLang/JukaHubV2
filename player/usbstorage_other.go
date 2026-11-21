//go:build !linux

package main

// usbstorage_other.go — USB storage mode off Linux. Only the handheld lends
// its SD card to a computer; on a computer the row is hidden
// (usbStorageSupported) and this keeps the shared code compiling.

func usbStorageCheck() usbStoragePlan {
	return usbStoragePlan{problem: "USB storage mode works on the handheld only."}
}
