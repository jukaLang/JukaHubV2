package main

// autoupdate_channel.go — the update channel (backlog REL-4). Settings →
// About & updates → "Update channel" offers Stable (the default) and Beta:
//
//   - Stable follows GitHub's newest release that is not a prerelease
//     (releases/latest), which is what every build followed before.
//   - Beta follows the newest published release, prereleases included
//     (the releases list, newest first; drafts never count).
//
// FetchLatestJukaHubRelease (patch_engine.go) asks updateChannelBetaOn, so the
// startup check, Patch → Check for updates, the offline update cache (upch.go)
// and Install update all follow the channel. Going back from Beta to Stable
// never downgrades: an update is only offered when it was published after the
// installed build (releaseIsNewerThanInstalled), so a device on a beta waits
// for the next stable release, and a prerelease already in the update cache is
// not offered on Stable (upchBestReady). The channel shows in About and in bug
// reports.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
)

const (
	// updateChannelSetting is the custom variable the Settings row writes.
	updateChannelSetting = "updateChannel"
	updateChannelStable  = "stable"
	updateChannelBeta    = "beta"
	// jukaHubReleasesListAPI is the newest releases, prereleases included,
	// newest first. Twenty is plenty: the newest published one wins.
	jukaHubReleasesListAPI = "https://api.github.com/repos/jukaLang/JukaHubV2/releases?per_page=20"
	// jukaHubReleaseTagDownloadBase is one release's download folder; the tag
	// follows. Unlike releases/latest/download it also reaches a prerelease.
	jukaHubReleaseTagDownloadBase = "https://github.com/jukaLang/JukaHubV2/releases/download/"
)

// updateChannelBetaOn mirrors the setting for the update goroutines; the
// Custom map belongs to the main thread (upchLoadPrefs loads it).
var updateChannelBetaOn atomic.Bool

// updateChannelFromConfig reads the setting (main thread only): Stable unless
// Beta was chosen.
func updateChannelFromConfig(config *Config) string {
	if config == nil {
		return updateChannelStable
	}
	if v, ok := customValue(config.Variables.Custom, updateChannelSetting); ok {
		if s, isString := v.(string); isString && strings.EqualFold(strings.TrimSpace(s), updateChannelBeta) {
			return updateChannelBeta
		}
	}
	return updateChannelStable
}

// updateChannelLoad mirrors the setting for the background checks. Main
// thread.
func updateChannelLoad(config *Config) {
	updateChannelBetaOn.Store(updateChannelFromConfig(config) == updateChannelBeta)
}

// updateChannelNow is the channel the update checks follow. Any thread.
func updateChannelNow() string {
	if updateChannelBetaOn.Load() {
		return updateChannelBeta
	}
	return updateChannelStable
}

// updateChannelLabel is a channel's name as the screens show it.
func updateChannelLabel(channel string) string {
	if channel == updateChannelBeta {
		return "Beta"
	}
	return "Stable"
}

// updateChannelAboutValue is About's line. It reads the mirror and returns
// constant strings, so drawing it every frame costs nothing.
func updateChannelAboutValue() string {
	if updateChannelBetaOn.Load() {
		return "Beta channel"
	}
	return "Stable channel"
}

// updateChannelOptions are the Settings row's stops.
func updateChannelOptions() []setOption {
	return []setOption{
		{Value: updateChannelStable, Label: "Stable", Note: "Tested releases only. The default"},
		{Value: updateChannelBeta, Label: "Beta", Note: "Prereleases too: new things first, rougher edges"},
	}
}

// updateChannelIndex is the Settings row's current stop.
func updateChannelIndex(c *Config) int {
	if updateChannelFromConfig(c) == updateChannelBeta {
		return 1
	}
	return 0
}

// updateChannelDetail is the line under the Settings row.
func updateChannelDetail(c *Config) string {
	if updateChannelFromConfig(c) == updateChannelBeta {
		return "Beta: prereleases included · back to Stable waits for the next stable release"
	}
	return "Stable: tested releases only"
}

// updateChannelChoose stores the channel (main thread). A release offered on
// the other channel is dropped and the feed asked again, so the offer and the
// update cache follow the new channel; nothing is ever downgraded.
func updateChannelChoose(c *Config, value string) {
	if value != updateChannelStable && value != updateChannelBeta {
		return
	}
	if c == nil || updateChannelFromConfig(c) == value {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	c.Variables.Custom[updateChannelSetting] = value
	saveUserConfigDebounced(userConfigFromVariables(c))
	updateChannelBetaOn.Store(value == updateChannelBeta)
	logPatch("update channel: %s", value)
	if value == updateChannelStable {
		// A prerelease on offer or downloading is not Stable's to install.
		upchCancelBackground()
		clearAvailableUpdate()
	}
	if !IsOnline() {
		setToast("Update channel: " + updateChannelLabel(value) + " · checks when you are online")
		return
	}
	upchCheckNowFromSettings(c)
}

// githubReleaseListed is one entry of the releases list.
type githubReleaseListed struct {
	githubRelease
	Draft bool `json:"draft"`
}

// fetchNewestJukaHubRelease is the Beta channel's release: the newest
// published release, prereleases included.
func fetchNewestJukaHubRelease(ctx context.Context) (*githubRelease, error) {
	data, err := fetchBounded(ctx, newPatchClient(), jukaHubReleasesListAPI, patchMaxMetadata)
	if err != nil {
		return nil, err
	}
	var list []githubReleaseListed
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	rel, ok := pickNewestJukaHubRelease(list)
	if !ok {
		return nil, errors.New("no published JukaHub release")
	}
	return rel, nil
}

// pickNewestJukaHubRelease picks the newest published, non-draft release with
// a tag from the list; the list order breaks a tie.
func pickNewestJukaHubRelease(list []githubReleaseListed) (*githubRelease, bool) {
	best := -1
	for i := range list {
		r := &list[i]
		if r.Draft || strings.TrimSpace(r.TagName) == "" {
			continue
		}
		t, ok := releasePublishedTime(&r.githubRelease)
		if !ok {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		if bt, _ := releasePublishedTime(&list[best].githubRelease); t.After(bt) {
			best = i
		}
	}
	if best < 0 {
		return nil, false
	}
	rel := list[best].githubRelease
	return &rel, true
}

// jukaHubReleaseAssetURL is where a release's asset downloads when the API
// left the asset list out: the release's own folder (a prerelease is not under
// releases/latest), or the latest stable one when the tag is unknown.
func jukaHubReleaseAssetURL(rel *githubRelease, asset string) string {
	if rel != nil {
		if tag := strings.TrimSpace(rel.TagName); tag != "" && !strings.ContainsAny(tag, "/?#% \\") {
			return jukaHubReleaseTagDownloadBase + tag + "/" + asset
		}
	}
	return jukaHubReleaseDownloadBase + asset
}
