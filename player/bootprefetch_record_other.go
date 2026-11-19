//go:build !linux

package main

// bootprefetch_record_other.go — the boot prefetch is for the handheld's
// cold start from an SD card (bootprefetch_record_linux.go); the PC build has
// none.

func bootPrefetchFirstFrame() {}

func bootPrefetchReportField() string { return "" }
