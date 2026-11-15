//go:build !linux

package main

// jukaos_other.go — JukaHub OS is a handheld (Linux) feature: these are its
// stand-ins elsewhere. The Settings section is hidden (jukaosSupported).

func jukaosSessionEntry() {}

func jukaosSupported() bool { return false }

func jukaosInit() {}

func jukaosKernel() string { return "" }

func jukaosHostFor(appDir string) jukaosHost { return jukaosHost{AppDir: appDir} }

func jukaosRevert(string) []string { return nil }

func jukaosDropCaches() {}

func jukaosMarkStockOnce() {}

func jukaosStockOnce() bool { return false }

func jukaosClearStockOnce() {}
