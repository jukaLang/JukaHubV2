package main

// weather_units.go — picks Celsius or Fahrenheit from the user's location.
//
// The weather fetch resolves the device's country through keyless IP
// geolocation (ipwho.is, with ipapi.co and ipinfo.io fallbacks). Until the user
// explicitly sets weatherUnit, the header, the Local weather card and the full
// Weather scene use that country's convention instead of a fixed Celsius
// default, so a US device no longer reads 15C / 7C.

import (
	"strings"
	"sync"
)

var (
	weatherUnitMu  sync.Mutex
	weatherCountry string
)

// setWeatherCountry records the country reported by geolocation. Empty or
// unknown values keep the previous country, which matters when a later refresh
// fails offline.
func setWeatherCountry(code string) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 2 {
		return
	}
	weatherUnitMu.Lock()
	weatherCountry = code
	weatherUnitMu.Unlock()
}

func weatherCountryCode() string {
	weatherUnitMu.Lock()
	defer weatherUnitMu.Unlock()
	return weatherCountry
}

// weatherCountryUsesFahrenheit lists the places whose weather services report
// in Fahrenheit. It follows the primary uses: the United States and its
// territories, the freely associated Pacific states (Palau, Micronesia, the
// Marshall Islands) and the Cayman Islands. Places that use both scales can
// still choose a unit in Settings.
func weatherCountryUsesFahrenheit(code string) bool {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "US", "PR", "GU", "VI", "AS", "MP", // United States and territories
		"PW", "FM", "MH", // freely associated states, US National Weather Service
		"KY": // Cayman Islands
		return true
	}
	return false
}

// weatherUnit returns the unit to display. An explicit "C" or "F" in the
// config always wins; an empty or unknown value follows the detected country
// and falls back to Celsius while the location is unknown.
func weatherUnit(config *Config) string {
	if config != nil {
		switch strings.ToUpper(strings.TrimSpace(config.Variables.WeatherUnit)) {
		case "F":
			return "F"
		case "C":
			return "C"
		}
	}
	if weatherCountryUsesFahrenheit(weatherCountryCode()) {
		return "F"
	}
	return "C"
}

// weatherTemp converts a Celsius reading from Open-Meteo into the display unit.
func weatherTemp(celsius float64, unit string) float64 {
	if unit == "F" {
		return celsius*9/5 + 32
	}
	return celsius
}
