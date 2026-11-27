package main

// steam_achievements.go — a game's Steam achievements, read with the Web API
// key the Account page already stores. The key never leaves the device except
// in the request to Steam, and is never shown in the report.
//
// GetSchemaForGame supplies the names and descriptions; GetUserStatsForGame
// supplies which ones this account has unlocked and when. Either answer can
// stand alone: with no schema the internal names are shown, and with no stats
// every achievement is shown as locked.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"
)

// steamAchBase is a seam for tests: every request is made against it.
var steamAchBase = "https://api.steampowered.com"

type steamAchievement struct {
	Name     string
	Desc     string
	Unlocked bool
	When     time.Time
}

type steamAchState struct {
	mu      sync.Mutex
	title   string
	list    []steamAchievement
	loading bool
	err     string
}

var steamAchUI steamAchState

type steamAchSchemaAnswer struct {
	Game struct {
		AvailableGameStats struct {
			Achievements []struct {
				Name        string `json:"name"`
				DisplayName string `json:"displayName"`
				Description string `json:"description"`
			} `json:"achievements"`
		} `json:"availableGameStats"`
	} `json:"game"`
}

type steamAchUserAnswer struct {
	PlayerStats struct {
		Achievements []struct {
			Name       string `json:"name"`
			Achieved   int    `json:"achieved"`
			UnlockTime int64  `json:"unlocktime"`
		} `json:"achievements"`
	} `json:"playerstats"`
}

// steamAchFetch reads one game's achievements for an account.
func steamAchFetch(ctx context.Context, key, steamID string, appID int) ([]steamAchievement, error) {
	if key == "" || steamID == "" || appID <= 0 {
		return nil, fmt.Errorf("sign in and add a Web API key first")
	}
	schemaURL := steamAchBase + "/ISteamUserStats/GetSchemaForGame/v2/?" + url.Values{
		"key": {key}, "appid": {strconv.Itoa(appID)},
	}.Encode()
	statsURL := steamAchBase + "/ISteamUserStats/GetUserStatsForGame/v2/?" + url.Values{
		"key": {key}, "steamid": {steamID}, "appid": {strconv.Itoa(appID)},
	}.Encode()

	schemaData, schemaErr := steamGet(ctx, schemaURL, 8<<20)
	statsData, statsErr := steamGet(ctx, statsURL, 8<<20)
	if schemaErr != nil && statsErr != nil {
		return nil, fmt.Errorf("Steam did not answer: %v", statsErr)
	}

	var schema steamAchSchemaAnswer
	if schemaErr == nil {
		_ = json.Unmarshal(schemaData, &schema)
	}
	var stats steamAchUserAnswer
	if statsErr == nil {
		_ = json.Unmarshal(statsData, &stats)
	}
	unlocked := map[string]time.Time{}
	for _, a := range stats.PlayerStats.Achievements {
		if a.Achieved != 0 {
			unlocked[a.Name] = time.Unix(a.UnlockTime, 0)
		}
	}
	out := make([]steamAchievement, 0, len(schema.Game.AvailableGameStats.Achievements)+len(unlocked))
	seen := map[string]bool{}
	for _, def := range schema.Game.AvailableGameStats.Achievements {
		name := def.DisplayName
		if name == "" {
			name = def.Name
		}
		out = append(out, steamAchievement{
			Name: name, Desc: def.Description,
			Unlocked: unlocked[def.Name].Unix() > 0, When: unlocked[def.Name],
		})
		seen[def.Name] = true
	}
	// A game can have unlocked an achievement the schema no longer lists.
	for name, when := range unlocked {
		if seen[name] {
			continue
		}
		out = append(out, steamAchievement{Name: name, Unlocked: true, When: when})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Unlocked != out[j].Unlocked {
			return out[i].Unlocked
		}
		if !out[i].When.Equal(out[j].When) {
			return out[i].When.After(out[j].When)
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// steamAchStart begins a fetch off the event thread. The page shows the
// loading state until the answer is posted back.
func steamAchStart(g steamGame) {
	acct, key := steamCurrentAccount()
	if acct.SteamID == "" || key == "" {
		steamAchUI.mu.Lock()
		steamAchUI.title, steamAchUI.list, steamAchUI.loading = g.Name, nil, false
		steamAchUI.err = "Add a Web API key on the Steam Account page first"
		steamAchUI.mu.Unlock()
		return
	}
	steamAchUI.mu.Lock()
	steamAchUI.title, steamAchUI.list, steamAchUI.loading, steamAchUI.err = g.Name, nil, true, ""
	steamAchUI.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		list, err := steamAchFetch(ctx, key, acct.SteamID, g.AppID)
		tbxPost(func() {
			steamAchUI.mu.Lock()
			steamAchUI.loading = false
			steamAchUI.list = list
			if err != nil {
				steamAchUI.err = firstLine(err.Error())
			} else {
				steamAchUI.err = ""
			}
			steamAchUI.mu.Unlock()
		})
	}()
}

// steamAchLines is the card's text, newest unlock first.
func steamAchLines(title string, list []steamAchievement, loading bool, errText string) []string {
	var lines []string
	if title != "" {
		lines = append(lines, title)
	}
	if loading {
		lines = append(lines, "Reading achievements from Steam…")
		return lines
	}
	if errText != "" {
		lines = append(lines, errText)
		return lines
	}
	if len(list) == 0 {
		lines = append(lines, "No achievements for this game.")
		return lines
	}
	unlocked := 0
	for _, a := range list {
		if a.Unlocked {
			unlocked++
		}
	}
	lines = append(lines, "Unlocked "+itoa(unlocked)+" of "+itoa(len(list)))
	for _, a := range list {
		mark := "○"
		if a.Unlocked {
			mark = "✓"
		}
		line := mark + " " + a.Name
		if a.Unlocked && !a.When.IsZero() {
			line += " · " + a.When.Format("2006-01-02")
		}
		if a.Desc != "" {
			line += " — " + a.Desc
		}
		lines = append(lines, line)
	}
	return lines
}

// steamAchSnapshot is the page's view of the fetch.
func steamAchSnapshot() (title string, list []steamAchievement, loading bool, errText string) {
	steamAchUI.mu.Lock()
	defer steamAchUI.mu.Unlock()
	return steamAchUI.title, append([]steamAchievement(nil), steamAchUI.list...), steamAchUI.loading, steamAchUI.err
}
