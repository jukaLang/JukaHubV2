package main

import (
	"encoding/json"
	"testing"
)

// Saved tickers come back from jukauser.json as []interface{}; they must still
// be the user's list after a restart.
func TestCustomTickerListSurvivesARestart(t *testing.T) {
	var custom map[string]interface{}
	if err := json.Unmarshal([]byte(`{"custom_tickers":["AAPL","btc", 7]}`), &custom); err != nil {
		t.Fatal(err)
	}
	config := &Config{Variables: Variables{Custom: custom}}
	got := customTickerList(config)
	if len(got) != 2 || got[0] != "AAPL" || got[1] != "btc" {
		t.Fatalf("restored tickers = %v, want [AAPL btc]", got)
	}
	config.Variables.Custom["custom_tickers"] = []string{"MSFT"}
	if got := customTickerList(config); len(got) != 1 || got[0] != "MSFT" {
		t.Fatalf("session tickers = %v, want [MSFT]", got)
	}
}
