package main

// toolsui.go — the network helpers every tools page shares: the LAN address,
// the FTP server's toggle and shared folder, and exact scene lookup.
//
// This file used to hold the Tools overlay, the modal of thirty-five tiles
// the classic Tools page opened. The overlay is gone: Tools is a scene now
// (toolshub.go), with the network tools and the Wi-Fi picker on pages of their
// own (nettoolsscreen.go, wifiscreen.go). What stays here is used by those
// pages and by the rest of the app.

import (
	"sync"
	"time"
)

// toolsPanel is the retired overlay's open flag. Nothing sets it any more; it
// stays false and is kept because the panels across the tree that close
// "every other modal" before opening, and the modal audits, still name it.
var toolsPanel struct{ open bool }

// openToolsPanel opens the Tools hub. It is the overlay's old name, kept so
// callers written against it (Power tools' Back, the setup checklist) land on
// the hub; new code calls toolsHubOpen with its config.
func openToolsPanel() {
	toolsHubOpen(appConfig)
}

// toolsIPCache avoids calling net.Interfaces() every frame while a tools page
// is open on a device with a slow /proc.
var toolsIPCache struct {
	mu sync.Mutex
	ip string
	at time.Time
}

// toolsIPAddress is the device's LAN address ("" without one), refreshed at
// most every two seconds.
func toolsIPAddress() string {
	toolsIPCache.mu.Lock()
	defer toolsIPCache.mu.Unlock()
	if time.Since(toolsIPCache.at) < 2*time.Second {
		return toolsIPCache.ip
	}
	toolsIPCache.ip = localIPv4()
	toolsIPCache.at = time.Now()
	return toolsIPCache.ip
}

// sceneIndexExact returns the scene index or -1. findSceneIndex deliberately
// falls back to 0, which would silently navigate back to Home.
func sceneIndexExact(config *Config, name string) int {
	if config == nil {
		return -1
	}
	for i, scene := range config.Scenes {
		if scene.Name == name {
			return i
		}
	}
	return -1
}

// The shared folder is resolved once: the pages redraw every frame and stats
// for a nonexistent /mnt/SDCARD in a render loop are pure waste.
var ftpRootCache struct {
	sync.Once
	root string
}

func cachedFTPRoot() string {
	ftpRootCache.Do(func() { ftpRootCache.root = ftpRootDir() })
	return ftpRootCache.root
}

// toggleFTPServer starts or stops the server and reports the result. It is also
// used by the "ftp_toggle" config trigger.
func toggleFTPServer() {
	if ftpSrv.Running() {
		ftpSrv.Stop()
		ShowToast("FTP server stopped", ToastKindInfo)
		return
	}
	// A LAN address is the only requirement: FTP works on a Wi-Fi network
	// with no internet route, so this does not probe connectivity.
	if toolsIPAddress() == "" {
		ShowToast("Connect to Wi-Fi first — FTP needs a network address", ToastKindWarning)
		return
	}
	if err := ftpSrv.Start(); err != nil {
		ShowToast("FTP failed: "+firstLine(err.Error()), ToastKindError)
		return
	}
	PlaySuccessSound()
	ShowToast("FTP on "+ftpSrv.Address(), ToastKindSuccess)
}
