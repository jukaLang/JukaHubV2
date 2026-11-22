package main

// autoupdate_channel.go — the update channel (backlog REL-4). Settings →
// About & updates → "Update channel" offers Stable (the default) and Beta:
//
//   - Stable follows the newest published JukaHub app release that is not a
//     prerelease.
//   - Beta also includes JukaHub app prereleases. Both channels use the
//     releases list because releases/latest can name a rolling emulator-core
//     release that has no JukaHub app archive.
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
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// updateChannelSetting is the custom variable the Settings row writes.
	updateChannelSetting = "updateChannel"
	updateChannelStable  = "stable"
	updateChannelBeta    = "beta"
	// jukaHubReleasesListAPI includes the rolling emulator releases as well as
	// app builds. Pick only an app release from the latest page.
	jukaHubReleasesListAPI = "https://api.github.com/repos/jukaLang/JukaHubV2/releases?per_page=50"
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

// fetchJukaHubRelease selects an app release from the repository's release
// list. GitHub's releases/latest is unsuitable: a rolling cores-aarch64
// release can be newer than the app and contain no installable app archive.
func fetchJukaHubRelease(ctx context.Context, includePrerelease bool) (*githubRelease, error) {
	data, err := fetchBounded(ctx, newPatchClient(), jukaHubReleasesListAPI, patchMaxMetadata)
	if err != nil {
		return nil, err
	}
	var list []githubReleaseListed
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	rel, ok := pickJukaHubRelease(list, includePrerelease)
	if !ok {
		return nil, errors.New("no published JukaHub release")
	}
	return rel, nil
}

// pickJukaHubRelease picks the newest published app release for a channel;
// list order breaks a tie.
func pickJukaHubRelease(list []githubReleaseListed, includePrerelease bool) (*githubRelease, bool) {
	best := -1
	for i := range list {
		r := &list[i]
		if r.Draft || !jukaHubReleaseTag(r.TagName) || (!includePrerelease && r.Prerelease) {
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

// jukaHubReleaseTag reports a tag of a JukaHub build: the main-branch builds'
// JukaHub-<time> (JukaGUI-<time> before the rename) and the versioned v1.2.3
// releases. The repository's rolling emulator releases (ppsspp-tg5040,
// cores-aarch64) carry no JukaHub, and the first publish of one would
// otherwise be the newest release a Beta handheld is offered.
func jukaHubReleaseTag(tag string) bool {
	tag = strings.TrimSpace(tag)
	if strings.HasPrefix(tag, "JukaHub-") || strings.HasPrefix(tag, "JukaGUI-") {
		return true
	}
	return len(tag) > 1 && (tag[0] == 'v' || tag[0] == 'V') && tag[1] >= '0' && tag[1] <= '9'
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

// ── When GitHub's API does not answer ──────────────────────────────────────
//
// "can't update the hub. it failed during update" (2026-11-20): the release
// list comes from GitHub's API, which answers 60 lookups an hour per network
// without a sign-in, and every handheld and PC running JukaHub on that network
// spends them on emulators, cores, PortMaster and runtimes too. Once they were
// gone, Update failed at "checking JukaHub releases" with an HTTP 403 even
// though the release had been offered minutes before, and kept failing for up
// to an hour. The release archives themselves download from github.com, which
// that limit does not count, so only the question "which release?" needs
// another answer: the repository's latest-release page gives it.

// jukaHubLatestReleasePage is the repository's latest-release page on
// github.com, not the API. It answers with a redirect to the release's own
// page, whose last path element is the tag. GitHub's "latest" is the newest
// JukaHub build: the rolling emulator releases are published with
// --latest=false.
const jukaHubLatestReleasePage = "https://github.com/jukaLang/JukaHubV2/releases/latest"

// jukaHubReleasePageTimeout bounds the page lookup.
const jukaHubReleasePageTimeout = 20 * time.Second

// fetchJukaHubReleaseOrPage is fetchJukaHubRelease and, when that fails for
// any reason but a cancelled request (GitHub's hourly limit, an API outage, a
// list that did not arrive whole), the release the latest-release page names.
// The API's error is returned when the page names nothing either: it says
// what went wrong. A Beta device gets the newest stable release from the
// page; releaseIsNewerThanInstalled keeps that from ever being a downgrade.
func fetchJukaHubReleaseOrPage(ctx context.Context, includePrerelease bool) (*githubRelease, error) {
	rel, err := fetchJukaHubRelease(ctx, includePrerelease)
	if err == nil || ctx.Err() != nil {
		return rel, err
	}
	page, perr := fetchJukaHubReleaseFromPage(ctx)
	if perr != nil {
		logPatch("auto-update: GitHub's API did not answer (%v), and neither did the release page (%v)", err, perr)
		return nil, err
	}
	// The page names only the newest stable release and has no list behind
	// it. When that is nothing newer, it cannot say whether a prerelease
	// (Beta) or the release on offer is still out, and every caller would
	// take it for "up to date" and drop the offer, which the next answer from
	// the API would bring back: the API's error stands instead.
	if !releaseIsNewerThanInstalled(page) && (includePrerelease || availableUpdateTag() != "") {
		logPatch("auto-update: GitHub's API did not answer (%v); the release page names %s, nothing newer", err, page.TagName)
		return nil, err
	}
	logPatch("auto-update: GitHub's API did not answer (%v); the release page names %s", err, page.TagName)
	return page, nil
}

// fetchJukaHubReleaseFromPage asks the latest-release page which release it
// is. The redirect is the answer; the page it leads to is not fetched.
func fetchJukaHubReleaseFromPage(ctx context.Context) (*githubRelease, error) {
	ctx, cancel := context.WithTimeout(ctx, jukaHubReleasePageTimeout)
	defer cancel()
	client := newPatchClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jukaHubLatestReleasePage, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub-Patch/"+versionString(nil))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, jukaHubLatestReleasePage)
	}
	loc, err := resp.Location()
	if err != nil {
		return nil, fmt.Errorf("%s did not say where the release is: %w", jukaHubLatestReleasePage, err)
	}
	tag, ok := jukaHubTagFromReleasePage(loc)
	if !ok {
		return nil, fmt.Errorf("%s led to %s, not a JukaHub release", jukaHubLatestReleasePage, loc.Redacted())
	}
	return jukaHubReleaseFromPage(tag), nil
}

// jukaHubTagFromReleasePage reads the tag out of a release page's address,
// https://github.com/jukaLang/JukaHubV2/releases/tag/<tag>: on github.com
// only, this repository only, and only a JukaHub build's tag in plain
// characters (it becomes part of a download address and a folder name).
func jukaHubTagFromReleasePage(u *url.URL) (string, bool) {
	if u == nil || !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Hostname(), "github.com") {
		return "", false
	}
	const prefix = "/jukalang/jukahubv2/releases/tag/"
	if len(u.Path) <= len(prefix) || !strings.EqualFold(u.Path[:len(prefix)], prefix) {
		return "", false
	}
	tag := u.Path[len(prefix):]
	if !validReleaseNotesTag(tag) || !jukaHubReleaseTag(tag) {
		return "", false
	}
	return tag, true
}

// jukaHubReleaseFromPage is the release a page lookup names. It has no notes
// and no asset list of the API's, so the archive for this device is listed
// with the address of the release's own folder, and with the digest, size
// and publication time the offer recorded when the API last described this
// very release (markUpdateAvailable). Without that record the archive is not
// checksum-verified, as with any release whose API entry had no digest
// (StageJukaHubUpdate says so in the log); the zip's own checksums still catch
// a download that arrived damaged.
func jukaHubReleaseFromPage(tag string) *githubRelease {
	rel := &githubRelease{TagName: tag}
	if t, ok := releaseTimeFromTag(tag); ok {
		rel.PublishedAt = t.Format(time.RFC3339)
	}
	target, err := jukaHubUpdateTargetFor(DetectDevice(), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return rel
	}
	asset := githubAsset{Name: target.Asset, BrowserDownloadURL: jukaHubReleaseAssetURL(rel, target.Asset)}
	patchStateMu.Lock()
	if strings.EqualFold(patchState.AvailableUpdateTag, tag) && strings.EqualFold(patchState.AvailableUpdateAsset, target.Asset) {
		asset.Digest = patchState.AvailableUpdateDigest
		asset.Size = patchState.AvailableUpdateSize
		if at := strings.TrimSpace(patchState.AvailableUpdateAt); at != "" {
			rel.PublishedAt = at
		}
	}
	patchStateMu.Unlock()
	rel.Assets = []githubAsset{asset}
	return rel
}

// githubLimitNote is what fetchBounded adds to a refusal from GitHub's API
// whose reason is the hourly limit (no lookups left), with the time it resets;
// "" for anything else.
func githubLimitNote(resp *http.Response, now time.Time) string {
	if resp == nil || resp.Request == nil || resp.Request.URL == nil ||
		!strings.EqualFold(resp.Request.URL.Hostname(), "api.github.com") {
		return ""
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
	case http.StatusForbidden:
		if strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) != "0" {
			return ""
		}
	default:
		return ""
	}
	note := " (GitHub's limit of lookups an hour from this network is used up"
	if reset, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")), 10, 64); err == nil && reset > now.Unix() {
		note += "; it resets at " + time.Unix(reset, 0).Local().Format("15:04")
	}
	return note + ")"
}
