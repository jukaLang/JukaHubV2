package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setWeatherCountryForTest(t *testing.T, code string) {
	t.Helper()
	prev := weatherCountryCode()
	setWeatherCountryDirect(code)
	t.Cleanup(func() { setWeatherCountryDirect(prev) })
}

// setWeatherCountryDirect also allows clearing the country, which production
// code intentionally refuses so a failed lookup keeps the last known value.
func setWeatherCountryDirect(code string) {
	weatherUnitMu.Lock()
	weatherCountry = code
	weatherUnitMu.Unlock()
}

func TestWeatherUnitFollowsCountryUnlessSet(t *testing.T) {
	setWeatherCountryForTest(t, "")
	cases := []struct {
		name       string
		country    string
		configured string
		want       string
	}{
		{"US defaults to Fahrenheit", "US", "", "F"},
		{"US territory defaults to Fahrenheit", "PR", "", "F"},
		{"Cayman Islands defaults to Fahrenheit", "KY", "", "F"},
		{"UK defaults to Celsius", "GB", "", "C"},
		{"Germany defaults to Celsius", "DE", "", "C"},
		{"explicit Celsius wins in the US", "US", "C", "C"},
		{"explicit Fahrenheit wins in the UK", "GB", "F", "F"},
		{"lowercase override is accepted", "DE", "f", "F"},
		{"unknown location falls back to Celsius", "", "", "C"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setWeatherCountryDirect(tc.country)
			config := &Config{Variables: Variables{WeatherUnit: tc.configured}}
			if got := weatherUnit(config); got != tc.want {
				t.Errorf("weatherUnit(country=%q, configured=%q) = %q, want %q",
					tc.country, tc.configured, got, tc.want)
			}
		})
	}
}

type geoTransport func(*http.Request) (*http.Response, error)

func (f geoTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func geoClient(body string) *http.Client {
	return &http.Client{Transport: geoTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
}

func TestLookupLocationParsesProviders(t *testing.T) {
	cases := []struct {
		name    string
		lookup  func(context.Context, *http.Client) (geoLocation, bool)
		body    string
		country string
		lat     float64
	}{
		{"ipwho.is", lookupIPWhoIs,
			`{"success":true,"latitude":35.7,"longitude":-78.6,"country_code":"US"}`, "US", 35.7},
		{"ipapi.co", lookupIPAPICo,
			`{"latitude":51.5,"longitude":-0.1,"country_code":"GB"}`, "GB", 51.5},
		{"ipinfo.io", lookupIPInfo,
			`{"loc":"48.8,2.3","country":"FR"}`, "FR", 48.8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.lookup(context.Background(), geoClient(tc.body))
			if !ok {
				t.Fatal("lookup failed")
			}
			if got.CountryCode != tc.country || got.Latitude != tc.lat {
				t.Errorf("lookup = %+v, want country %s at %.1f", got, tc.country, tc.lat)
			}
		})
	}

	// ipwho.is reports rate limits as a successful HTTP body with success=false;
	// the next provider must then be used.
	client := &http.Client{Transport: geoTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"success":false,"message":"rate limited"}`
		if strings.Contains(r.URL.Host, "ipapi") {
			body = `{"latitude":40.7,"longitude":-74.0,"country_code":"US"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	got, ok := lookupLocation(client)
	if !ok || got.CountryCode != "US" {
		t.Fatalf("fallback lookup = %+v, %v; want US", got, ok)
	}
}

// Older configs pinned Celsius in Custom while the typed field stayed empty.
// Loading those must now yield "auto" instead of re-applying Celsius.
func TestLegacyCelsiusDefaultBecomesAuto(t *testing.T) {
	var auto Variables
	if err := json.Unmarshal([]byte(`{"weatherUnit":"","Custom":{"WeatherUnit":"C"}}`), &auto); err != nil {
		t.Fatal(err)
	}
	if auto.WeatherUnit != "" {
		t.Errorf("legacy default weatherUnit = %q, want empty (auto)", auto.WeatherUnit)
	}
	if _, ok := auto.Custom["WeatherUnit"]; ok {
		t.Error("legacy Celsius override was kept in Custom")
	}

	// A typed choice is authoritative and survives the migration.
	var manual Variables
	if err := json.Unmarshal([]byte(`{"weatherUnit":"F","Custom":{"WeatherUnit":"C"}}`), &manual); err != nil {
		t.Fatal(err)
	}
	if manual.WeatherUnit != "F" {
		t.Errorf("explicit weatherUnit = %q, want F", manual.WeatherUnit)
	}
}

func TestWeatherUnitMigrationClearsLegacyCelsius(t *testing.T) {
	user := &UserConfig{Variables: UserVariables{
		WeatherUnit: "C",
		Custom:      map[string]interface{}{"weatherUnit": "C"},
	}}
	if !migrateWeatherUnitAuto(user) {
		t.Fatal("legacy Celsius default was not migrated")
	}
	if user.Variables.WeatherUnit != "" {
		t.Errorf("weatherUnit = %q, want empty (auto)", user.Variables.WeatherUnit)
	}
	if _, ok := user.Variables.Custom["weatherUnit"]; ok {
		t.Error("legacy custom weatherUnit was not removed")
	}
	if user.Variables.Custom["weatherUnitAuto"] != true {
		t.Error("migration marker was not recorded")
	}

	// Once migrated, a deliberate Celsius choice must survive a later boot.
	user.Variables.WeatherUnit = "C"
	if migrateWeatherUnitAuto(user) {
		t.Error("migration ran a second time")
	}
	if user.Variables.WeatherUnit != "C" {
		t.Errorf("explicit Celsius was cleared: %q", user.Variables.WeatherUnit)
	}
}

// The header used to print the Fahrenheit conversion with a "C" suffix. The
// chip must carry the unit it actually displays and stay clickable.
func TestHeaderWeatherChipUsesLocalUnit(t *testing.T) {
	setWeatherCountryForTest(t, "US")
	withWeatherData(t, []WeatherDay{{Date: time.Now().Format("2006-01-02"), TMax: 21, TMin: 12, Code: 1}})

	hasPart := func(parts []string, want string) bool {
		for _, p := range parts {
			if p == want {
				return true
			}
		}
		return false
	}
	us := headerStatusParts(&Config{})
	if !hasPart(us, "69F / 53F") {
		t.Errorf("US header parts = %v, want 69F / 53F", us)
	}
	if parts := headerStatusParts(&Config{Variables: Variables{WeatherUnit: "C"}}); !hasPart(parts, "21C / 12C") {
		t.Errorf("explicit Celsius parts = %v, want 21C / 12C", parts)
	}

	setWeatherCountry("GB")
	if parts := headerStatusParts(&Config{}); !hasPart(parts, "21C / 12C") {
		t.Errorf("UK header parts = %v, want 21C / 12C", parts)
	}
	if !isWeatherStatusPart("69F / 53F") {
		t.Error("Fahrenheit chip is not recognised as the weather button")
	}
}

// The existing Weather Unit input becomes a cycling button in both designs,
// including older copies of the configs already installed on devices.
func TestWeatherUnitSettingsCycleAndPersistence(t *testing.T) {
	setWeatherCountryForTest(t, "US")
	withWeatherData(t, []WeatherDay{{Date: time.Now().Format("2006-01-02"), TMax: 21, TMin: 12, Code: 1}})
	for _, path := range []string{"jukaconfig.json", "trimui/jukaconfig.device.json"} {
		config, err := loadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		scene := settingsGeneralScene(config)
		if scene == nil {
			t.Fatalf("%s has no SettingsGeneral", path)
		}
		var old Element
		for _, elem := range scene.Elements {
			if elem.Type == "input" && elem.Variable == "weatherUnit" {
				old = elem
				break
			}
		}
		if old.Variable == "" {
			t.Fatalf("%s has no weather unit field", path)
		}
		ensureWeatherUnitSetting(config)
		ensureWeatherUnitSetting(config)
		var row Element
		count := 0
		for _, elem := range scene.Elements {
			if elem.Trigger == "cycle_weather_unit" {
				row = elem
				count++
			}
		}
		if count != 1 || row.Type != "button" || row.X != old.X || row.Y != old.Y || row.Width != old.Width || row.Height != old.Height {
			t.Fatalf("%s: converted row count=%d, row=%+v, old=%+v", path, count, row, old)
		}
	}

	prevWrites, prevDelay := userConfigWritesEnabled, userConfigSaveDelay
	prevScene, prevElement := activeSceneIndex, activeElementIndex
	userConfigWritesEnabled = true
	userConfigSaveDelay = time.Hour
	activeSceneIndex, activeElementIndex = -1, -1
	t.Cleanup(func() {
		cancelPendingUserConfigSave(t)
		userConfigWritesEnabled, userConfigSaveDelay = prevWrites, prevDelay
		activeSceneIndex, activeElementIndex = prevScene, prevElement
	})
	if err := os.MkdirAll("tmp", 0755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("tmp", "weather-unit-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	config := &Config{Variables: Variables{Custom: map[string]interface{}{"WeatherUnit": "F"}}}
	row := Element{Type: "button", Trigger: "cycle_weather_unit"}
	if got := weatherUnitSettingLabel(config); got != "Auto (F)" {
		t.Fatalf("auto label = %q", got)
	}
	cases := []struct{ saved, label, unit, header string }{
		{"C", "Celsius (C)", "C", "21C / 12C"},
		{"F", "Fahrenheit (F)", "F", "69F / 53F"},
		{"", "Auto (F)", "F", "69F / 53F"},
	}
	for _, tc := range cases {
		handleTrigger(nil, config, row)
		if config.Variables.WeatherUnit != tc.saved || weatherUnitSettingLabel(config) != tc.label || weatherUnit(config) != tc.unit {
			t.Fatalf("choice = %q, label = %q, display = %q; want %+v", config.Variables.WeatherUnit, weatherUnitSettingLabel(config), weatherUnit(config), tc)
		}
		if parts := headerStatusParts(config); !strings.Contains(strings.Join(parts, " | "), tc.header) {
			t.Fatalf("header = %v, want %q", parts, tc.header)
		}
		if _, exists := config.Variables.Custom["WeatherUnit"]; exists {
			t.Fatal("stale custom weather unit remains")
		}
		userConfigSaveMutex.Lock()
		pending := pendingUserConfig
		userConfigSaveMutex.Unlock()
		if pending == nil || pending.Variables.WeatherUnit != tc.saved {
			t.Fatalf("pending save = %+v, want %q", pending, tc.saved)
		}
		saveUserConfigToDir(dir, pending)
		saved, err := readUserConfigFile(filepath.Join(dir, "jukauser.json"))
		if err != nil {
			t.Fatal(err)
		}
		migrateWeatherUnitAuto(saved)
		reloaded := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
		mergeUserConfig(reloaded, saved)
		if reloaded.Variables.WeatherUnit != tc.saved || weatherUnitSettingLabel(reloaded) != tc.label {
			t.Fatalf("reloaded choice = %q, label = %q, want %+v", reloaded.Variables.WeatherUnit, weatherUnitSettingLabel(reloaded), tc)
		}
	}
}
