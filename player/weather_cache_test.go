package main

// weather_cache_test.go covers the offline forecast: what gets written after a
// good fetch, what is refused as too old or unreadable, and that a boot with a
// saved forecast shows weather before any network call.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func withWeatherCacheFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), weatherCacheName)
	prev := weatherCachePath
	weatherCachePath = func() string { return path }
	prevDaily, prevReady := weatherDaily, weatherReady
	weatherCacheMu.Lock()
	prevSaved := weatherSavedAt
	weatherCacheMu.Unlock()
	t.Cleanup(func() {
		weatherCachePath = prev
		wxMutex.Lock()
		weatherDaily, weatherReady = prevDaily, prevReady
		wxMutex.Unlock()
		weatherCacheMu.Lock()
		weatherSavedAt = prevSaved
		weatherCacheMu.Unlock()
	})
	return path
}

func cacheSampleDays(now time.Time) []WeatherDay {
	days := make([]WeatherDay, 0, 3)
	for i := 0; i < 3; i++ {
		days = append(days, WeatherDay{
			Date: now.AddDate(0, 0, i).Format("2006-01-02"),
			TMax: 24 + float64(i),
			TMin: 12,
			Code: 2,
			Pop:  10 * i,
		})
	}
	return days
}

func TestWeatherCacheRoundTrip(t *testing.T) {
	path := withWeatherCacheFile(t)
	now := time.Now()
	days := cacheSampleDays(now)

	if err := saveWeatherCache(path, days, now.Add(-30*time.Minute)); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, savedAt, ok := readWeatherCache(path, now)
	if !ok {
		t.Fatal("saved forecast was not readable")
	}
	if len(got) != len(days) {
		t.Fatalf("read %d days, want %d", len(got), len(days))
	}
	if got[0].Date != days[0].Date {
		t.Fatalf("first day = %q, want %q", got[0].Date, days[0].Date)
	}
	if age := now.Sub(savedAt); age < 29*time.Minute || age > 31*time.Minute {
		t.Errorf("saved age = %v, want ~30m", age)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("save left its temporary file behind")
	}
}

func TestWeatherCacheRefusesUnusableData(t *testing.T) {
	path := withWeatherCacheFile(t)
	now := time.Now()

	if _, _, ok := readWeatherCache(filepath.Join(t.TempDir(), "missing.json"), now); ok {
		t.Error("a missing cache read as usable")
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := readWeatherCache(path, now); ok {
		t.Error("garbage JSON read as usable")
	}

	// Too old to be a forecast.
	stale := weatherCacheDoc{SavedAt: now.Add(-weatherCacheMaxAge - time.Hour), Days: cacheSampleDays(now)}
	if err := saveWeatherCache(path, stale.Days, stale.SavedAt); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := readWeatherCache(path, now); ok {
		t.Error("a week-old cache read as usable")
	}

	// Saved on a device whose clock was wrong (in the future).
	if err := saveWeatherCache(path, cacheSampleDays(now), now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := readWeatherCache(path, now); ok {
		t.Error("a future-stamped cache read as usable")
	}

	// A forecast whose last day is long past.
	old := now.AddDate(0, 0, -30)
	if err := saveWeatherCache(path, cacheSampleDays(old), now); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := readWeatherCache(path, now); ok {
		t.Error("a forecast for last month read as usable")
	}

	// Entries without a valid date are dropped, and the rest still loads.
	doc := weatherCacheDoc{SavedAt: now, Days: append([]WeatherDay{{Date: "not-a-date", TMax: 1}}, cacheSampleDays(now)...)}
	if err := saveWeatherCache(path, doc.Days, doc.SavedAt); err != nil {
		t.Fatal(err)
	}
	got, _, ok := readWeatherCache(path, now)
	if !ok || len(got) != 3 {
		t.Fatalf("got %d days (ok=%v), want the 3 valid ones", len(got), ok)
	}
}

// TestRestoreWeatherCacheShowsWeatherOffline pins the point of the cache: the
// popover has data before any network attempt, and says how old it is.
func TestRestoreWeatherCacheShowsWeatherOffline(t *testing.T) {
	path := withWeatherCacheFile(t)
	now := time.Now()
	if err := saveWeatherCache(path, cacheSampleDays(now), now.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	wxMutex.Lock()
	weatherDaily, weatherReady = nil, false
	wxMutex.Unlock()

	if !restoreWeatherCache(now) {
		t.Fatal("restore ignored a usable cache")
	}
	wxMutex.Lock()
	ready, days := weatherReady, len(weatherDaily)
	wxMutex.Unlock()
	if !ready || days != 3 {
		t.Fatalf("after restore ready=%v days=%d, want the saved forecast", ready, days)
	}
	if note := weatherFreshnessNote(now); note != "Saved 3 hours ago" {
		t.Errorf("freshness note = %q", note)
	}

	// A live fetch clears the note.
	noteWeatherFetched(cacheSampleDays(now), now)
	if note := weatherFreshnessNote(now); note != "" {
		t.Errorf("note after a live fetch = %q, want none", note)
	}
}

// TestRestoreKeepsLiveData covers the ordering at startup: a fetch that lands
// before the read finishes must not be replaced by the older saved copy.
func TestRestoreKeepsLiveData(t *testing.T) {
	path := withWeatherCacheFile(t)
	now := time.Now()
	if err := saveWeatherCache(path, cacheSampleDays(now), now.Add(-5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	live := []WeatherDay{{Date: now.Format("2006-01-02"), TMax: 99, TMin: 1, Code: 0}}
	wxMutex.Lock()
	weatherDaily, weatherReady = live, true
	wxMutex.Unlock()

	if restoreWeatherCache(now) {
		t.Error("restore overwrote a live forecast")
	}
	wxMutex.Lock()
	got := weatherDaily[0].TMax
	wxMutex.Unlock()
	if got != 99 {
		t.Errorf("live temperature = %v, want it kept", got)
	}
}
