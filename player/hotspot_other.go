//go:build !linux

package main

// hotspot_other.go — off Linux there is no hotspot: a PC shares its own
// connection from the system's settings. These keep the shared code building;
// the Tools tile and the Settings row hide themselves (hotspotSupported).

import "errors"

func hotspotSupported() bool { return false }

func hotspotToolsLabel() (string, bool) { return "", false }

func hotspotBackendStart(string, string) (hotspotRun, error) {
	return hotspotRun{}, errors.New("the hotspot is for the handheld")
}

func hotspotBackendStop(hotspotRun) {}

func hotspotBackendAlive(hotspotRun) bool { return false }

func hotspotBackendClients(hotspotRun) []hotspotClient { return nil }

func hotspotStartupCleanup() {}
