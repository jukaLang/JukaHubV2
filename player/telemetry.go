package main

// telemetry.go — opt-in, anonymous device diagnostics and crash telemetry.
//
// Consent lives in jukauser.json and defaults to off. Informational issues
// contain build, device model and coarse diagnostics, without an install
// identifier. Separately configured crash telemetry adds only the word
// "panic" — no stack, config values, filenames, URLs, names or account data.
//
// Every tenth opted-in launch creates an informational GitHub issue through the
// JukaHub report service. Crash events still use a separately configured
// telemetry collector, when one exists.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const telemetryOptInKey = "telemetryOptIn"
const telemetryIssueInterval = 10

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

// setTelemetryOptedIn records the choice in the cached user config (which
// telemetryOptedIn reads) and on the live config, then saves the live config.
// Saving the cache instead would write its boot copy of every other setting
// back, and a section-style save of it no longer carries a changed key.
func setTelemetryOptedIn(config *Config, on bool) error {
	user := loadUserConfig()
	if user == nil {
		return fmt.Errorf("user settings unavailable")
	}
	if user.Variables.Custom == nil {
		user.Variables.Custom = make(map[string]interface{})
	}
	user.Variables.Custom[telemetryOptInKey] = on
	if !on {
		telemetryClearIssueProgress()
	}
	if config == nil {
		config = appConfig
	}
	if config == nil {
		saveUserConfig(user)
		return nil
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = make(map[string]interface{})
	}
	config.Variables.Custom[telemetryOptInKey] = on
	saveUserConfig(userConfigFromVariables(config))
	return nil
}

func telemetryStatusValue() string {
	if !telemetryOptedIn() {
		return "Off"
	}
	if !bugrepEndpointAllowed(bugrepDefaultEndpoint) {
		return "On (no endpoint)"
	}
	return "On"
}

// telemetryPayload is the wire format for a separately configured crash
// collector. Keep it limited to build and device facts; anything user-authored
// belongs in a manual bug report, where the user reviews it first.
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

// telemetryIssueFact scrubs and bounds a single short device fact.
func telemetryIssueFact(value string, max int) string {
	value = bugrepScrub(strings.TrimSpace(value))
	if len(value) <= max {
		return value
	}
	cut := max
	for cut > 0 && (value[cut]&0xC0) == 0x80 {
		cut--
	}
	return value[:cut]
}

var telemetryVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
var telemetryReleasePattern = regexp.MustCompile(`^(?:JukaHub-[0-9]{14}|v[0-9]+\.[0-9]+\.[0-9]+)$`)

func telemetryIssueVersion(version string) string {
	if telemetryVersionPattern.MatchString(version) {
		return version
	}
	return "unknown"
}

func telemetryIssueRelease(release string) string {
	if telemetryReleasePattern.MatchString(release) {
		return release
	}
	return ""
}

// telemetryIssueReport contains only fixed text and coarse device facts. The
// full diagnostics bundle can contain backup names and other local details,
// so it must never be sent automatically to a public issue.
func telemetryIssueReport(config *Config, now time.Time) bugrepReport {
	version := "unknown"
	if config != nil {
		version = versionString(config)
	}
	release := installedReleaseTag()
	if release == "" {
		release = readInstalledReleaseInfo().Version
	}
	diagnostics := fmt.Sprintf("CPU cores: %d\n", runtime.NumCPU())
	if config != nil {
		elements := 0
		for _, scene := range config.Scenes {
			elements += len(scene.Elements)
		}
		diagnostics += "Config: " + pluralizeCount(len(config.Scenes), "scene", "scenes") + ", " +
			pluralizeCount(elements, "element", "elements") + fmt.Sprintf(", schema %d\n", config.SchemaVersion)
	}
	if safeModeActive() {
		diagnostics += "Safe mode: on\n"
	} else {
		diagnostics += "Safe mode: off\n"
	}
	return bugrepReport{
		Schema:      1,
		Kind:        "anonymous_diagnostics",
		App:         "JukaHub",
		ID:          bugrepNewRef(),
		Category:    "other",
		Title:       "Anonymous device diagnostics",
		Description: "Informational report sent automatically after about ten opted-in launches. No bug is being reported.",
		Version:     telemetryIssueVersion(version),
		Release:     telemetryIssueRelease(release),
		Device:      telemetryIssueFact(deviceDisplayName(DetectDevice()), 120),
		Platform:    telemetryIssueFact(P().Name(), 64),
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Diagnostics: bugrepScrub(diagnostics),
		Created:     now.UTC().Format(time.RFC3339),
	}
}

type telemetryIssueProgress struct {
	Launches int           `json:"launches"`
	Pending  *bugrepReport `json:"pending,omitempty"`
}

var telemetryIssueState struct {
	mu           sync.Mutex
	pathOverride string // test seam
}

var telemetryIssuePost = bugrepPost // test seam
var telemetryIssueOnline = IsOnline // test seam

func telemetryClearIssueProgress() {
	telemetryIssueState.mu.Lock()
	defer telemetryIssueState.mu.Unlock()
	if path := telemetryIssueStatePath(); path != "" {
		for _, file := range []string{path, path + ".tmp"} {
			if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
				logWarn("telemetry", "could not clear pending diagnostics: %v", err)
			}
		}
	}
}

func telemetryIssueStatePath() string {
	if telemetryIssueState.pathOverride != "" {
		return telemetryIssueState.pathOverride
	}
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "telemetry", "launches.json")
}

func telemetryReadIssueProgress(path string) telemetryIssueProgress {
	var progress telemetryIssueProgress
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 16<<10 || json.Unmarshal(data, &progress) != nil ||
		progress.Launches < 0 || progress.Launches > telemetryIssueInterval ||
		(progress.Pending != nil && (!bugrepRefValid(progress.Pending.ID) || progress.Pending.Kind != "anonymous_diagnostics")) {
		return telemetryIssueProgress{}
	}
	return progress
}

func telemetryWriteIssueProgress(path string, progress telemetryIssueProgress) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// telemetryIssueLaunch persists the count and a stable pending reference before
// making a network request. A failed or interrupted send retries next launch;
// the service deduplicates by reference and creation time.
func telemetryIssueLaunch(report bugrepReport) {
	endpoint := strings.TrimSpace(bugrepDefaultEndpoint)
	if !bugrepEndpointAllowed(endpoint) {
		return
	}
	telemetryIssueState.mu.Lock()
	if !telemetryOptedIn() {
		telemetryIssueState.mu.Unlock()
		return
	}
	path := telemetryIssueStatePath()
	if path == "" {
		telemetryIssueState.mu.Unlock()
		return
	}
	progress := telemetryReadIssueProgress(path)
	if progress.Pending == nil {
		if progress.Launches < telemetryIssueInterval {
			progress.Launches++
		}
		if progress.Launches >= telemetryIssueInterval && !bugrepClockWrong(time.Now()) {
			progress.Pending = &report
		}
		if err := telemetryWriteIssueProgress(path, progress); err != nil {
			telemetryIssueState.mu.Unlock()
			logWarn("telemetry", "could not save diagnostics launch count: %v", err)
			return
		}
	}
	pending := progress.Pending
	telemetryIssueState.mu.Unlock()
	if pending == nil || !telemetryOptedIn() || !telemetryIssueOnline() {
		return
	}
	sent, _, err := telemetryIssuePost(endpoint, *pending)
	if err != nil {
		logWarn("telemetry", "informational diagnostics issue not sent: %v", err)
		return
	}
	if sent.Issue <= 0 {
		logWarn("telemetry", "report service did not confirm an informational issue")
		return
	}
	telemetryIssueState.mu.Lock()
	defer telemetryIssueState.mu.Unlock()
	if !telemetryOptedIn() {
		return
	}
	current := telemetryReadIssueProgress(path)
	if current.Pending != nil && current.Pending.ID == pending.ID && current.Pending.Created == pending.Created {
		if err := telemetryWriteIssueProgress(path, telemetryIssueProgress{}); err != nil {
			logWarn("telemetry", "could not clear sent diagnostics issue: %v", err)
		}
	}
}

// TelemetryBoot counts an opted-in launch in the background after the first
// frame. It sends one informational issue at ten launches, then starts again.
func TelemetryBoot(config *Config) {
	if !telemetryOptedIn() {
		return
	}
	report := telemetryIssueReport(config, time.Now())
	go telemetryIssueLaunch(report)
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
		logError("telemetry", "crash event not sent: %v", err)
	}
}
