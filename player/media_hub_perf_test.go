package main

import (
	"context"
	"testing"
)

func TestCancelMediaSearchStopsHiddenFeed(t *testing.T) {
	const key = "test_hidden_media_feed"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mediaSearchMu.Lock()
	mediaSearchGen[key] = 41
	mediaSearchCancel[key] = cancel
	mediaSearchMu.Unlock()
	mediaPaging.mu.Lock()
	mediaPaging.byVar[key] = &mediaResultsPage{loading: true}
	mediaPaging.mu.Unlock()
	t.Cleanup(func() {
		mediaSearchMu.Lock()
		delete(mediaSearchGen, key)
		delete(mediaSearchCancel, key)
		mediaSearchMu.Unlock()
		mediaPaging.mu.Lock()
		delete(mediaPaging.byVar, key)
		mediaPaging.mu.Unlock()
	})

	if !cancelMediaSearch(key) {
		t.Fatal("running feed was not cancelled")
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("feed context error = %v, want canceled", ctx.Err())
	}
	if !mediaSearchSuperseded(key, 41) {
		t.Fatal("cancelled feed can still publish its result")
	}
	if mediaPageLoading(key) {
		t.Fatal("cancelled page remains loading")
	}
	if cancelMediaSearch(key) {
		t.Fatal("already cancelled feed was cancelled twice")
	}
}

func TestLibraryBuildIgnoresHiddenFeedResults(t *testing.T) {
	oldTab := mediaHub.tab
	mediaHub.tab = mediaTabLibrary
	t.Cleanup(func() { mediaHub.tab = oldTab })

	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	before := mediaHubBuildKey(config)
	config.Variables.Custom[mediaTrendingVar] = []VideoInfo{{Title: "new result"}}
	config.Variables.Custom[mediaResultsVar] = []VideoInfo{{Title: "another result"}}
	if after := mediaHubBuildKey(config); after != before {
		t.Fatalf("hidden feed changed the Library build key: before=%+v after=%+v", before, after)
	}
}
