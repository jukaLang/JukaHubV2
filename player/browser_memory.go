package main

// Browser page data is useful only while its scene is visible. Keep the small
// URL history and saved site data, but release parsed pages, image bytes and
// textures when the scene closes so an AI model can use that memory.

import (
	"runtime/debug"
	"strings"
	"sync/atomic"
)

var browserReclaiming atomic.Bool

func browserPageVariables(config *Config) []string {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return nil
	}
	seen := make(map[string]bool)
	var variables []string
	for _, elem := range config.Scenes[currentSceneIndex].Elements {
		if elem.Type != "textbrowser" || textBrowserElementSource(elem) != textBrowserSourceWeb {
			continue
		}
		variable := elem.Variable
		if variable == "" {
			variable = findTextBrowserVariable(config)
		}
		if seen[variable] {
			continue
		}
		seen[variable] = true
		variables = append(variables, variable)
	}
	return variables
}

// browserStopLoading runs at the start of the exit fade. The old page remains
// drawable during the fade, but no late fetch may publish into it.
func browserStopLoading(config *Config) {
	for _, variable := range browserPageVariables(config) {
		stopTextBrowserRequest(variable)
	}
	webJSStopRunning()
}

// browserReleaseMemory runs after the exit fade, on the render thread that
// owns SDL textures. Browser history and the current URL are only small strings;
// reopening can refetch the page and Back/Forward still work.
func browserReleaseMemory(config *Config) {
	if config == nil {
		return
	}
	variables := browserPageVariables(config)
	if len(variables) == 0 {
		return
	}
	browserStopLoading(config)
	pageURL := brwPageOnScreen(variables[0])
	if from, target := browserTakeLandedURL(); target == pageURL && brwHistoryCurrent() == from {
		browserReplaceURL(from, target)
	}
	browserResumeURL = brwHistoryCurrent()
	if browserResumeURL == "" {
		browserResumeURL = pageURL
	}
	resumeRow := textBrowserScrollY
	browserHomeLoaded = false
	browserPendingURL = ""
	browserPendingAt = 0

	// Every old page publication finished before stopTextBrowserRequest bumped
	// its sequence. Drain already queued Custom updates before deleting the page
	// value; a late result cannot put the old text back after this point.
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	drainCustom(config)
	for _, variable := range variables {
		setBrowserDocument(variable, nil)
		brwSetFailure(variable, nil)
		delete(config.Variables.Custom, variable)
		browserState.mu.Lock()
		delete(browserState.links, variable)
		browserState.mu.Unlock()
		browserAdBlockViews.mu.Lock()
		delete(browserAdBlockViews.pages, variable)
		browserAdBlockViews.mu.Unlock()
	}
	delete(config.Variables.Custom, textBrowserURLVariable)
	delete(config.Variables.Custom, browserFindVariable)
	delete(config.Variables.Custom, browserSearchVariable)
	delete(config.Variables.Custom, browserFormVariable)

	browserClearPageCache()
	getThumbnailTextures().ClearPrefix("webimg:")
	browserSharedTransport().CloseIdleConnections()

	textBrowserWrapCache.mu.Lock()
	textBrowserWrapCache.entry = textBrowserWrapCacheEntry{}
	textBrowserWrapCache.mu.Unlock()
	browserFindReset()
	browserScrollMemory.rows = map[string]int32{}
	browserScrollMemory.order = nil
	browserScrollPendingClear()
	if browserResumeURL != "" {
		browserScrollMemorySave(browserResumeURL, resumeRow)
	}
	browserLinkCursor = browserLinkCursorState{}
	brwFS = brwFSState{}
	brwCard.wrapText = ""
	brwCard.wrapLines = nil
	brwInfoCache.mu.Lock()
	brwInfoCache.entries = map[string]*brwInfoEntry{}
	brwInfoCache.mu.Unlock()
	brwInfoLineCache.entries = map[brwInfoLineKey][]string{}
	brwLanding.mu.Lock()
	brwLanding.pending = false
	brwLanding.variable, brwLanding.from, brwLanding.back = "", "", ""
	brwLanding.mu.Unlock()
	browserFormEditing, browserFormSubmitted = false, false
	textBrowserFindEditing, textBrowserFindSubmitted = false, false
	textBrowserSearchEditing, textBrowserSearchSubmitted = false, false
	textBrowserAddressEditing, textBrowserAddressSubmitted = false, false
	browserClearSessionStorage()

	// Dropping references makes the bytes collectible. Hand free pages back to
	// the OS off the render thread so the next scene can start without a pause.
	if browserReclaiming.CompareAndSwap(false, true) {
		go func() {
			defer browserReclaiming.Store(false)
			debug.FreeOSMemory()
		}()
	}
}

func browserClearSessionStorage() {
	webJSStorage.Lock()
	defer webJSStorage.Unlock()
	for key := range webJSStorage.data {
		if strings.HasPrefix(key, "session ") {
			delete(webJSStorage.data, key)
		}
	}
	used := make([]string, 0, len(webJSStorage.used))
	for _, key := range webJSStorage.used {
		if !strings.HasPrefix(key, "session ") {
			used = append(used, key)
		}
	}
	webJSStorage.used = used
}
