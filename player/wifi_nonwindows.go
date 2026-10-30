//go:build !windows

package main

import "errors"

// wifiPOSIXForced pins non-Windows builds to the wpa_supplicant backend.
var wifiPOSIXForced = true

func winWifiActive() bool              { return false }
func resetWinWifiIfaceCache()          {}
func winWifiInterface() string         { return "" }
func winWifiControlAvailable() bool    { return false }
func winWifiUnavailableReason() string { return "Windows Wi-Fi backend is unavailable" }
func winWifiSignalPoll() (wifiSignalSample, error) {
	return wifiSignalSample{}, errors.New(winWifiUnavailableReason())
}
func winWifiStatus() (wifiStatus, error) { return wifiStatus{}, errors.New(winWifiUnavailableReason()) }
func winWifiSavedNetworks() ([]savedNetwork, error) {
	return nil, errors.New(winWifiUnavailableReason())
}
func winWifiScanResults() ([]wifiNetwork, error)  { return nil, errors.New(winWifiUnavailableReason()) }
func winWifiScanNetworks() ([]wifiNetwork, error) { return nil, errors.New(winWifiUnavailableReason()) }
func winWifiReadSnapshot() (wifiSnapshot, error) {
	return wifiSnapshot{}, errors.New(winWifiUnavailableReason())
}
func winWifiJoin(string, string, bool) (wifiJoinResult, error) {
	return wifiJoinResult{}, errors.New(winWifiUnavailableReason())
}
func winWifiSelectSaved(int) (wifiJoinResult, error) {
	return wifiJoinResult{}, errors.New(winWifiUnavailableReason())
}
func winWifiDisconnect() error { return errors.New(winWifiUnavailableReason()) }
func winWifiReconnect() (wifiJoinResult, error) {
	return wifiJoinResult{}, errors.New(winWifiUnavailableReason())
}
func winWifiForget(string) error { return errors.New(winWifiUnavailableReason()) }
func winWifiLinkUp() bool        { return false }
