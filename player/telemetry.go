package main

// telemetry.go — opt-in, anonymous device and crash telemetry.
//
// Consent lives in jukauser.json and defaults to off. The payload is
// deliberately tiny: build, device model, platform and architecture. Crash
// events add the word "panic" and nothing else — no stack, config values,
// filenames, URLs, names or account data. There is no install identifier, so
// the collector can count devices and crashes but cannot track a person.
//
// A collector has to be configured at build time or with JUKAHUB_TELEMETRY_URL;
// this keeps an open-source build from silently shipping data to a third party.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

const telemetryOptInKey = "telemetryOptIn"

// telemetryEndpoint is set by the release build, e.g.
// -ldflags "-X main.telemetryEndpoint=https://telemetry.example/v1/events".
// An empty endpoint makes the feature a no-op.
var telemetryEndpoint string

func telemetryEndpointURL() string {
	if v := strings.TrimSpace(os.Getenv("JUKAHUB_TELEMETRY_URL")); v != "" {
		return v
	}
	return strings.TrimSpace(telemetryEndpoint)
}

func telemetryOptedInFrom(custom map[string]interface{}) bool {
	if custom == nil {
		return false
	}
	v, ok := custom[telemetryOptInKey]
	if !ok {
		return false
	}
	return coerceBool(v)
}

func telemetryOptedIn() bool {
	user := loadUserConfig()
	if user == nil {
		return false
	}
	return telemetryOptedInFrom(user.Variables.Custom)
}

// setTelemetryOptedIn persists the choice and mirrors it onto the live config.
func setTelemetryOptedIn(config *Config, on bool) error {
	user := loadUserConfig()
	if user == nil {
		return fmt.Errorf("user settings unavailable")
	}
	if user.Variables.Custom == nil {
		user.Variables.Custom = make(map[string]interface{})
	}
	user.Variables.Custom[telemetryOptInKey] = on
	saveUserConfig(user)
	if config != nil {
		if config.Variables.Custom == nil {
			config.Variables.Custom = make(map[string]interface{})
		}
		config.Variables.Custom[telemetryOptInKey] = on
	}
	return nil
}

func telemetryStatusValue() string {
	if !telemetryOptedIn() {
		return "Off"
	}
	if telemetryEndpointURL() == "" {
		return "On (no endpoint)"
	}
	return "On"
}

// telemetryPayload is the complete wire format. Keep it limited to facts that
// describe the build and the device; anything user-authored belongs in a bug
// report, where the user reviews it before submitting.
func telemetryPayload(config *Config, kind, crash string) map[string]interface{} {
	version := "unknown"
	if config != nil {
		version = versionString(config)
	}
	release := installedReleaseTag()
	if release == "" {
		release = readInstalledReleaseInfo().Version
	}
	payload := map[string]interface{}{
		"schema":   1,
		"kind":     kind,
		"app":      "JukaHub",
		"version":  version,
		"release":  release,
		"device":   deviceDisplayName(DetectDevice()),
		"platform": P().Name(),
		"os":       runtime.GOOS,
		"arch":     runtime.GOARCH,
		"at":       time.Now().UTC().Format(time.RFC3339),
	}
	if crash != "" {
		payload["crash"] = crash
	}
	return payload
}

func postTelemetry(endpoint string, payload map[string]interface{}) error {
	if strings.TrimSpace(endpoint) == "" {
		return fmt.Errorf("no telemetry endpoint configured")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "JukaHub-Telemetry/"+fmt.Sprint(payload["version"]))
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telemetry endpoint returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// TelemetryBoot sends one device-census event when the user has opted in. It is
// fire-and-forget so startup never waits on the network.
func TelemetryBoot(config *Config) {
	if !telemetryOptedIn() {
		return
	}
	endpoint := telemetryEndpointURL()
	if endpoint == "" {
		return
	}
	payload := telemetryPayload(config, "device", "")
	go func() {
		if err := postTelemetry(endpoint, payload); err != nil {
			log.Printf("telemetry: device event not sent: %v", err)
		}
	}()
}

// TelemetryCrash records a panic in the crash telemetry stream. Only the fact
// that the app panicked is sent; the panic value and stack can contain private
// data, so they stay in the local log.
func TelemetryCrash(config *Config) {
	if !telemetryOptedIn() {
		return
	}
	endpoint := telemetryEndpointURL()
	if endpoint == "" {
		return
	}
	if err := postTelemetry(endpoint, telemetryPayload(config, "crash", "panic")); err != nil {
		log.Printf("telemetry: crash event not sent: %v", err)
	}
}
