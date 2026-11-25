package main

// weather_location.go — where the forecast is for, shown and chosen.
//
// The forecast used the place the handheld's internet address points to
// (ipwho.is, then ipapi.co, then ipinfo.io), and New York when none of them
// answered, and nothing said which place that was. With a VPN on it is the
// VPN server's city. Now:
//
//   - the place in use is remembered with where it came from and shown: the
//     forecast card's heading, the Weather screen's first lines and the
//     Settings row;
//   - Settings › General › Weather location picks it: Automatic (the internet
//     address, as before) or a city found by name through Open-Meteo's
//     geocoding — the forecast's own provider, no key — chosen from the
//     matches. The choice is four Custom keys (weatherPlace, weatherLat,
//     weatherLon, weatherPlaceCountry); without them it is Automatic.
//
// The fetch runs on the poller's goroutine and never reads the config: the
// main thread publishes the chosen place (weatherFixedPublish).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	weatherPlaceKey        = "weatherPlace"
	weatherLatKey          = "weatherLat"
	weatherLonKey          = "weatherLon"
	weatherPlaceCountryKey = "weatherPlaceCountry"

	// weatherGeocodeURL is Open-Meteo's place search.
	weatherGeocodeURL = "https://geocoding-api.open-meteo.com/v1/search"
	// weatherGeocodeMax is how many matches the pick list offers.
	weatherGeocodeMax = 8
	// weatherPlaceMaxRunes bounds a place's name as typed and as stored.
	weatherPlaceMaxRunes = 80
)

// Where the place in use came from.
const (
	weatherSourceSet      = "set"
	weatherSourceInternet = "internet"
	weatherSourceDefault  = "default"
)

// weatherPlace is a place the forecast can be for.
type weatherPlace struct {
	Name    string
	Lat     float64
	Lon     float64
	Country string // ISO code, for the automatic unit; may be ""
	Region  string // the pick list's second line only
}

// weatherLoc is the chosen place as the main thread published it, and the
// place the last forecast was fetched for. Any goroutine.
var weatherLoc struct {
	mu       sync.Mutex
	fixed    weatherPlace
	fixedSet bool
	used     string
	source   string
}

// weatherFixedFrom reads the chosen place out of config; ok is false for
// Automatic, and for keys that do not make a place on Earth.
func weatherFixedFrom(c *Config) (weatherPlace, bool) {
	if c == nil || c.Variables.Custom == nil {
		return weatherPlace{}, false
	}
	m := c.Variables.Custom
	name, _ := customString(m, weatherPlaceKey)
	latS, _ := customString(m, weatherLatKey)
	lonS, _ := customString(m, weatherLonKey)
	name = strings.TrimSpace(name)
	lat, errLat := strconv.ParseFloat(strings.TrimSpace(latS), 64)
	lon, errLon := strconv.ParseFloat(strings.TrimSpace(lonS), 64)
	if name == "" || errLat != nil || errLon != nil || !weatherOnEarth(lat, lon) {
		return weatherPlace{}, false
	}
	country, _ := customString(m, weatherPlaceCountryKey)
	return weatherPlace{Name: weatherClipName(name), Lat: lat, Lon: lon, Country: strings.TrimSpace(country)}, true
}

// weatherOnEarth accepts a latitude and longitude that name a place.
func weatherOnEarth(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

// weatherClipName keeps a place's name to one line of weatherPlaceMaxRunes.
func weatherClipName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > weatherPlaceMaxRunes {
		s = string([]rune(s)[:weatherPlaceMaxRunes])
	}
	return s
}

// weatherFixedPublish hands the chosen place to the fetch. Main thread: at
// start, after a reload, and when the row changes it.
func weatherFixedPublish(c *Config) {
	p, ok := weatherFixedFrom(c)
	weatherLoc.mu.Lock()
	changed := ok != weatherLoc.fixedSet || p != weatherLoc.fixed
	weatherLoc.fixed, weatherLoc.fixedSet = p, ok
	if changed {
		// The last forecast was for the old place: say nothing until the
		// next one arrives.
		weatherLoc.used, weatherLoc.source = "", ""
	}
	weatherLoc.mu.Unlock()
}

// weatherFixedNow is the chosen place for the fetch, any goroutine.
func weatherFixedNow() (weatherPlace, bool) {
	weatherLoc.mu.Lock()
	defer weatherLoc.mu.Unlock()
	return weatherLoc.fixed, weatherLoc.fixedSet
}

// weatherNoteUsed records the place a forecast was just fetched for.
func weatherNoteUsed(label, source string) {
	weatherLoc.mu.Lock()
	weatherLoc.used, weatherLoc.source = weatherClipName(label), source
	weatherLoc.mu.Unlock()
}

// weatherUsedPlace is the place the forecast on screen is for, and where that
// came from ("" before the first fetch this session).
func weatherUsedPlace() (label, source string) {
	weatherLoc.mu.Lock()
	defer weatherLoc.mu.Unlock()
	return weatherLoc.used, weatherLoc.source
}

// weatherSourceWords says where a place came from, for a person.
func weatherSourceWords(source string) string {
	switch source {
	case weatherSourceSet:
		return "chosen in Settings"
	case weatherSourceInternet:
		return "from your internet address"
	case weatherSourceDefault:
		return "no location found, so New York"
	}
	return ""
}

// geoPlaceLabel is an address lookup's place as a person reads it: the city
// and the country, or what of them the provider gave.
func geoPlaceLabel(g geoLocation) string {
	var parts []string
	for _, p := range []string{g.City, g.Region, g.Country} {
		if p = strings.TrimSpace(p); p != "" && (len(parts) == 0 || !strings.EqualFold(parts[len(parts)-1], p)) {
			parts = append(parts, p)
		}
		if len(parts) == 2 {
			break
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%.2f, %.2f", g.Latitude, g.Longitude)
	}
	return strings.Join(parts, ", ")
}

// weatherKickFetch asks the poller for a forecast now.
func weatherKickFetch() {
	select {
	case weatherKick <- struct{}{}:
	default:
	}
}

// ── Geocoding ─────────────────────────────────────────────────────────────

// weatherGeocodeResult is one match of Open-Meteo's place search.
type weatherGeocodeResult struct {
	Name        string  `json:"name"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	CountryCode string  `json:"country_code"`
	Country     string  `json:"country"`
	Admin1      string  `json:"admin1"`
}

// weatherGeocodeParse reads the place search's answer: the matches that name
// a place, at most weatherGeocodeMax. An answer without "results" is none.
// Pure.
func weatherGeocodeParse(body []byte) ([]weatherPlace, error) {
	var v struct {
		Results []weatherGeocodeResult `json:"results"`
		Error   bool                   `json:"error"`
		Reason  string                 `json:"reason"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, errors.New("the place search answered with something unreadable")
	}
	if v.Error {
		return nil, errors.New(setOr(strings.TrimSpace(v.Reason), "the place search refused the request"))
	}
	var out []weatherPlace
	for _, r := range v.Results {
		name := weatherClipName(r.Name)
		if name == "" || !weatherOnEarth(r.Latitude, r.Longitude) || (r.Latitude == 0 && r.Longitude == 0) {
			continue
		}
		label := name
		if c := strings.TrimSpace(r.Country); c != "" {
			label += ", " + c
		}
		region := strings.TrimSpace(r.Admin1)
		if c := strings.TrimSpace(r.Country); c != "" {
			region = strings.TrimPrefix(region+" · "+c, " · ")
		}
		out = append(out, weatherPlace{Name: weatherClipName(label), Lat: r.Latitude, Lon: r.Longitude,
			Country: strings.ToUpper(strings.TrimSpace(r.CountryCode)), Region: region})
		if len(out) == weatherGeocodeMax {
			break
		}
	}
	return out, nil
}

// weatherGeocode looks a place up by name. lang is the answer's language
// ("en", "ru"). Off the main thread.
func weatherGeocode(ctx context.Context, query, lang string) ([]weatherPlace, error) {
	q := url.Values{}
	q.Set("name", query)
	q.Set("count", strconv.Itoa(weatherGeocodeMax))
	q.Set("language", setOr(lang, "en"))
	q.Set("format", "json")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, weatherGeocodeURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, errors.New("the place search could not be reached")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		return nil, fmt.Errorf("the place search answered %d", resp.StatusCode)
	}
	return weatherGeocodeParse(body)
}

// weatherGeocodeLang is the place search's language for the interface's.
func weatherGeocodeLang() string {
	code, _, _ := strings.Cut(uiLanguageCode(), "-")
	return strings.ToLower(setOr(code, "en"))
}

// ── Settings › General › Weather location ─────────────────────────────────

// weatherLocationRow is the row under Temperature unit.
func weatherLocationRow() setRow {
	return setRow{
		ID: "weatherLocation", Label: "Weather location", Kind: setKindAction,
		Detail:   weatherLocationDetail,
		Disabled: func(c *Config) bool { return !weatherEnabledFor(c) },
		Value:    weatherLocationValue,
		Open:     weatherLocationOpen,
	}
}

func weatherLocationValue(c *Config) string {
	if p, ok := weatherFixedFrom(c); ok {
		return p.Name
	}
	return "Automatic"
}

// weatherLocationDetail is the row's detail line, in " · " parts the
// interface language translates one by one (i18n.go).
func weatherLocationDetail(c *Config) string {
	if !weatherEnabledFor(c) {
		return "Turn Weather on to use it"
	}
	if _, ok := weatherFixedFrom(c); ok {
		return "Chosen by you · A picks another place or Automatic"
	}
	used, source := weatherUsedPlace()
	if used == "" || source == weatherSourceSet {
		return "Where your internet address is · A VPN moves it · A picks a city"
	}
	return "Now: " + used + " · " + weatherSourceWords(source) + " · A VPN moves it · A picks a city"
}

// weatherLocationOpen is the row's sheet: Automatic, the place chosen, or a
// search for another one. Main thread.
func weatherLocationOpen(c *Config) {
	fixed, isSet := weatherFixedFrom(c)
	used, source := weatherUsedPlace()
	autoNote := "Where your internet address is · With a VPN on, the VPN server's city"
	if !isSet && used != "" && source != weatherSourceSet {
		autoNote = "Now: " + used + " · " + autoNote
	}
	var opts []setOption
	current := "auto"
	if isSet {
		opts = append(opts, setOption{Value: "set", Label: fixed.Name, Note: "Chosen by you"})
		current = "set"
	}
	opts = append(opts,
		setOption{Value: "auto", Label: "Automatic", Note: autoNote},
		setOption{Value: "search", Label: "Choose a city…", Note: "Type its name, then pick it from the places found"},
	)
	setSheetOpen(&setHub.sheet, "Weather location", "The forecast in the header, on Home and on the Weather screen is for this place", opts, current, func(value string) {
		switch value {
		case "auto":
			if isSet {
				weatherClearFixed(c)
				setToast("Weather location: automatic")
			}
		case "search":
			seed := ""
			if isSet {
				seed, _, _ = strings.Cut(fixed.Name, ",")
			}
			osPromptOpen("Weather location", "City: Moscow, Lisbon, Osaka", "Search", seed, weatherPlaceMaxRunes, func(text string) {
				weatherLocationSearch(c, text)
			})
		}
	})
}

// weatherLocationSearch looks text up and offers the places found. Main
// thread; the search runs on a goroutine.
func weatherLocationSearch(c *Config, text string) {
	query := weatherClipName(text)
	if query == "" {
		return
	}
	lang := weatherGeocodeLang()
	ShowToast("Searching for the place "+query+"…", ToastKindInfo)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		places, err := weatherGeocode(ctx, query, lang)
		tbxPost(func() {
			switch {
			case err != nil:
				ShowToast("Weather location: "+firstLine(err.Error()), ToastKindWarning)
			case len(places) == 0:
				ShowToast("No place called "+strconv.Quote(query)+" was found", ToastKindWarning)
			default:
				opts := make([]setOption, 0, len(places))
				for i, p := range places {
					opts = append(opts, setOption{Value: strconv.Itoa(i), Label: p.Name,
						Note: strings.TrimPrefix(p.Region+fmt.Sprintf(" · %.2f, %.2f", p.Lat, p.Lon), " · ")})
				}
				setSheetOpen(&setHub.sheet, "Places called "+query, "The forecast will be for the place you pick", opts, "", func(value string) {
					i, err := strconv.Atoi(value)
					if err != nil || i < 0 || i >= len(places) {
						return
					}
					weatherSetFixed(c, places[i])
					setToast("Weather location: " + places[i].Name)
				})
			}
		})
	}()
}

// weatherSetFixed stores a chosen place and fetches its forecast. Main thread.
func weatherSetFixed(c *Config, p weatherPlace) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	c.Variables.Custom[weatherPlaceKey] = weatherClipName(p.Name)
	c.Variables.Custom[weatherLatKey] = strconv.FormatFloat(p.Lat, 'f', 4, 64)
	c.Variables.Custom[weatherLonKey] = strconv.FormatFloat(p.Lon, 'f', 4, 64)
	c.Variables.Custom[weatherPlaceCountryKey] = p.Country
	saveUserConfigDebounced(userConfigFromVariables(c))
	weatherFixedPublish(c)
	weatherKickFetch()
}

// weatherClearFixed goes back to Automatic. Main thread.
func weatherClearFixed(c *Config) {
	if c == nil || c.Variables.Custom == nil {
		return
	}
	for _, k := range []string{weatherPlaceKey, weatherLatKey, weatherLonKey, weatherPlaceCountryKey} {
		for key := range c.Variables.Custom {
			if strings.EqualFold(key, k) {
				delete(c.Variables.Custom, key)
			}
		}
	}
	saveUserConfigDebounced(userConfigFromVariables(c))
	weatherFixedPublish(c)
	weatherKickFetch()
}
