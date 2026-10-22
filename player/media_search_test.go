package main

import (
	"context"
	"strings"
	"testing"
)

// resetMediaSearchState clears the per-variable search bookkeeping.
func resetMediaSearchState(t *testing.T) {
	t.Helper()
	mediaSearchMu.Lock()
	prevGen, prevCancel, prevBusy := mediaSearchGen, mediaSearchCancel, mediaSearchBusy
	mediaSearchGen = map[string]uint64{}
	mediaSearchCancel = map[string]context.CancelFunc{}
	mediaSearchBusy = 0
	mediaSearchMu.Unlock()
	t.Cleanup(func() {
		mediaSearchMu.Lock()
		mediaSearchGen, mediaSearchCancel, mediaSearchBusy = prevGen, prevCancel, prevBusy
		mediaSearchMu.Unlock()
	})
}

// The Tube scene's Search button is "youtube_smart"; the keyboard submit used to
// look for a button called "yt_search" and found nothing, so typing a query and
// saving started no search at all.
func TestSceneMediaSearchButtonFindsTheSceneSearch(t *testing.T) {
	scene := SceneConfig{Name: "Tube", Elements: []Element{
		{Type: "input", Variable: "search_query"},
		{Type: "button", Text: "Search", Trigger: "youtube_smart"},
		{Type: "button", Text: "Play Link", Trigger: "youtube_play"},
		{Type: "searchresults", Variable: "search_results"},
	}}
	elem, ok := sceneMediaSearchButton(scene)
	if !ok {
		t.Fatal("no search button found on the Tube scene")
	}
	if elem.Trigger != "youtube_smart" {
		t.Fatalf("picked trigger %q, want youtube_smart", elem.Trigger)
	}

	if _, ok := sceneMediaSearchButton(SceneConfig{Name: "Shorts"}); ok {
		t.Error("a scene without a search button reported one")
	}
}

// Submitting the keyboard has to plan the same search the on-screen button
// runs: this is the path that used to find no trigger and start nothing.
func TestMediaSearchPlanFromTheTubeTrigger(t *testing.T) {
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}

	config.Variables.Custom["search_query"] = "lofi beats"
	plan, handled := planMediaSearchTrigger(config, "youtube_smart", "", "")
	if !handled {
		t.Fatal("youtube_smart was not treated as a search trigger")
	}
	if plan.errMsg != "" {
		t.Fatalf("unexpected error for a filled query: %q", plan.errMsg)
	}
	if plan.resultVar != "search_results" {
		t.Errorf("results variable = %q, want search_results", plan.resultVar)
	}
	resolved := substituteVars(plan.command, config.Variables.Custom)
	if !strings.Contains(resolved, "ytsearch20:lofi beats") {
		t.Errorf("search command %q does not search for the typed query", resolved)
	}

	// A pasted link plays instead of searching.
	config.Variables.Custom["search_query"] = "https://youtu.be/abc123"
	plan, _ = planMediaSearchTrigger(config, "youtube_smart", "", "")
	if plan.playURL != "https://youtu.be/abc123" || plan.command != "" {
		t.Errorf("pasted link planned command %q playURL %q", plan.command, plan.playURL)
	}

	// An empty query reports why instead of running.
	config.Variables.Custom["search_query"] = "   "
	plan, _ = planMediaSearchTrigger(config, "youtube_smart", "", "")
	if plan.errMsg == "" || plan.command != "" {
		t.Errorf("empty query planned command %q errMsg %q", plan.command, plan.errMsg)
	}

	// Play Link is not a search trigger.
	if _, handled := planMediaSearchTrigger(config, "youtube_play", "", ""); handled {
		t.Error("youtube_play was treated as a search trigger")
	}
}

// A newer query must win: the older run may not overwrite its results, and it
// must be cancelled so two extractors never run at once on the handheld.
func TestMediaSearchSupersedesOlderRun(t *testing.T) {
	resetMediaSearchState(t)
	config := &Config{}

	first := beginMediaSearch(config, "search_results")
	cancelled := false
	registerMediaSearchCancel("search_results", func() { cancelled = true })

	second := beginMediaSearch(config, "search_results")
	if !cancelled {
		t.Error("starting a new search did not cancel the previous one")
	}
	if mediaSearchSuperseded("search_results", first) != true {
		t.Error("the older generation still owned the results variable")
	}
	if mediaSearchSuperseded("search_results", second) {
		t.Error("the newest generation was reported as superseded")
	}

	finishMediaSearch(config) // the cancelled run finishes first
	if !config.Variables.LoadingSpinner {
		t.Error("the first run finishing cleared the spinner of the running one")
	}
	finishMediaSearch(config)
	if config.Variables.LoadingSpinner {
		t.Error("the spinner stayed up after the last search finished")
	}
}

// A stray quote typed on the on-screen keyboard used to unbalance the argument
// tokenizer: the command split at the wrong place and the search ran for the
// wrong text (or failed outright).
func TestSearchQueryQuotesDoNotBreakTokenization(t *testing.T) {
	vars := map[string]interface{}{"search_query": `my "song`}
	cmd := substituteVars(`yt-dlp "ytsearch20:$search_query"`, sanitizeSearchVars(vars))
	args := splitShellArgs(cmd)
	if len(args) != 2 {
		t.Fatalf("command split into %d args (%q), want 2", len(args), args)
	}
	if args[1] != "ytsearch20:my  song" {
		t.Fatalf("search argument = %q", args[1])
	}

	// A query without quotes is passed through untouched.
	plain := map[string]interface{}{"search_query": "lofi beats"}
	if got := sanitizeSearchVars(plain)["search_query"]; got != "lofi beats" {
		t.Fatalf("plain query changed to %q", got)
	}
}

func TestMediaSearchVariablesAreIndependent(t *testing.T) {
	resetMediaSearchState(t)
	config := &Config{}

	a := beginMediaSearch(config, "search_results")
	b := beginMediaSearch(config, "shorts_list")
	if mediaSearchSuperseded("search_results", a) {
		t.Error("a search for another variable superseded this one")
	}
	if mediaSearchSuperseded("shorts_list", b) {
		t.Error("a search for another variable superseded this one")
	}
}
