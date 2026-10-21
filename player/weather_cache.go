package main

// weather_cache.go keeps the last good forecast on disk.
//
// The forecast is the one thing on the home screen that needs the network, and
// a handheld is offline more often than a desktop: booting in a car, on a train,
// or before Wi-Fi has associated. Without a cache the popover said "Weather
// unavailable" until the next successful fetch, which made the field look broken
// rather than offline. The cache is written after every successful fetch and
// read once at startup, so the last forecast is on screen immediately and the
// card says how old it is.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	weatherCacheName = "weather-cache.json"
	// weatherCacheMaxAge is how long a saved forecast is worth showing at all.
	// A week-old forecast for the wrong week is worse than saying nothing.
	weatherCacheMaxAge = 7 * 24 * time.Hour
	// weatherFreshFor is the age at which the card starts calling the data
	// saved: below it the forecast is assumed to be from this session.
	weatherFreshFor = 90 * time.Minute
)

// weatherCacheDoc is the on-disk shape. SavedAt is the fetch time, not the
// write time, so copying the file between devices keeps the age honest.
type weatherCacheDoc struct {
	SavedAt time.Time    `json:"saved_at"`
	Days    []WeatherDay `json:"days"`
}

var (
	weatherCacheMu sync.Mutex
	// weatherSavedAt is when the visible forecast was fetched. Zero means the
	// data is live (or absent), never that it is old.
	weatherSavedAt time.Time
	// Overridable for tests: the data directory is not available in a unit test.
	weatherCachePath = defaultWeatherCachePath
)

func defaultWeatherCachePath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, weatherCacheName)
}

// weatherCacheAge reports how old the visible forecast is. The second result is
// false when the data is live or there is none.
func weatherCacheAge() (time.Duration, bool) {
	weatherCacheMu.Lock()
	defer weatherCacheMu.Unlock()
	if weatherSavedAt.IsZero() {
		return 0, false
	}
	age := time.Since(weatherSavedAt)
	if age < 0 {
		age = 0
	}
	return age, true
}

// saveWeatherCache writes the forecast through a temporary file, so a power cut
// cannot leave half a JSON document behind. Failures are logged by the caller's
// path only: a cache is an optimisation, never a reason to fail a fetch.
func saveWeatherCache(path string, days []WeatherDay, savedAt time.Time) error {
	if strings.TrimSpace(path) == "" || len(days) == 0 {
		return nil
	}
	data, err := json.MarshalIndent(weatherCacheDoc{SavedAt: savedAt.UTC(), Days: days}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readWeatherCache parses a cache file. Anything unexpected (missing file,
// garbage JSON, no usable days, an implausibly old or future stamp) reads as "no
// cache" instead of an error the caller has to explain.
func readWeatherCache(path string, now time.Time) ([]WeatherDay, time.Time, bool) {
	if strings.TrimSpace(path) == "" {
		return nil, time.Time{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	var doc weatherCacheDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		logPatch("weather: ignoring unreadable cache: %v", err)
		return nil, time.Time{}, false
	}
	days := sanitiseWeatherDays(doc.Days)
	if len(days) == 0 || doc.SavedAt.IsZero() {
		return nil, time.Time{}, false
	}
	age := now.Sub(doc.SavedAt)
	if age < 0 || age > weatherCacheMaxAge {
		return nil, time.Time{}, false
	}
	return days, doc.SavedAt, true
}

// sanitiseWeatherDays drops entries without a parseable date and keeps the
// list in date order, so a hand-edited or newer-format file cannot scramble the
// rows or put "yesterday" under Today.
func sanitiseWeatherDays(in []WeatherDay) []WeatherDay {
	out := make([]WeatherDay, 0, len(in))
	for _, d := range in {
		if _, err := time.Parse("2006-01-02", d.Date); err != nil {
			continue
		}
		out = append(out, d)
		if len(out) == 7 {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	// A forecast whose newest day is already in the past is not a forecast.
	if len(out) > 0 {
		last, err := time.Parse("2006-01-02", out[len(out)-1].Date)
		if err == nil && last.Before(time.Now().Add(-48*time.Hour)) {
			return nil
		}
	}
	return out
}

// restoreWeatherCache seeds the in-memory forecast before the first network
// attempt, so an offline boot still has weather. It never overwrites data a
// live fetch already stored: startup calls it first, and a fetch that lands
// during the read wins.
func restoreWeatherCache(now time.Time) bool {
	path := weatherCachePath()
	days, savedAt, ok := readWeatherCache(path, now)
	if !ok {
		return false
	}
	wxMutex.Lock()
	if weatherReady && len(weatherDaily) > 0 {
		wxMutex.Unlock()
		return false
	}
	weatherDaily = days
	weatherReady = true
	wxMutex.Unlock()

	weatherCacheMu.Lock()
	weatherSavedAt = savedAt
	weatherCacheMu.Unlock()

	if len(days) > 0 {
		DynBGUpdateWeather(days[0].Code)
	}
	logPatch("weather: restored the saved forecast from %s (age %s)", filepath.Base(path), now.Sub(savedAt).Round(time.Minute))
	return true
}

// noteWeatherFetched records that the forecast just came from the network and
// persists it for the next offline boot.
func noteWeatherFetched(days []WeatherDay, fetchedAt time.Time) {
	weatherCacheMu.Lock()
	weatherSavedAt = time.Time{}
	weatherCacheMu.Unlock()
	if err := saveWeatherCache(weatherCachePath(), days, fetchedAt); err != nil {
		logPatch("weather: could not save the forecast for offline use: %v", err)
	}
}

// weatherFreshnessNote is the card's supporting line when the forecast came from
// the cache instead of this session, or "" when it is live.
func weatherFreshnessNote(now time.Time) string {
	age, ok := weatherCacheAge()
	if !ok || age < weatherFreshFor {
		return ""
	}
	switch {
	case age < 24*time.Hour:
		return "Saved " + pluralHours(age) + " ago"
	default:
		return "Saved " + pluralDays(age) + " ago"
	}
}

func pluralHours(d time.Duration) string {
	h := int(d.Hours())
	if h < 1 {
		h = 1
	}
	if h == 1 {
		return "1 hour"
	}
	return itoa(h) + " hours"
}

func pluralDays(d time.Duration) string {
	days := int(d.Hours() / 24)
	if days <= 1 {
		return "1 day"
	}
	return itoa(days) + " days"
}
