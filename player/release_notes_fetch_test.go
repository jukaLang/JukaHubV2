package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestPublishedReleaseNotesUseMatchingReleaseBody(t *testing.T) {
	tag := "JukaHub-20261111075528"
	calls := 0
	got, err := fetchPublishedReleaseNotesWith(context.Background(), tag, func(_ context.Context, raw string) ([]byte, error) {
		calls++
		if raw != githubReleasesAPI+"/tags/"+tag {
			t.Fatalf("requested %q instead of the installed release", raw)
		}
		return []byte(`{"tag_name":"JukaHub-20261111075528","name":"Weather update","body":"## Added\n- Weather animations"}`), nil
	})
	if err != nil || got != "## Weather update\n\n## Added\n- Weather animations" || calls != 1 {
		t.Fatalf("notes = %q, calls = %d, err = %v", got, calls, err)
	}
}

func TestPublishedReleaseNotesFallBackToThisReleaseCommits(t *testing.T) {
	tag := "JukaHub-20261111075528"
	previous := "JukaHub-20261111042006"
	wantCompare := githubCompareAPI + previous + "..." + tag + "?per_page=100&page=1"
	var calls []string
	got, err := fetchPublishedReleaseNotesWith(context.Background(), tag, func(_ context.Context, raw string) ([]byte, error) {
		calls = append(calls, raw)
		switch raw {
		case githubReleasesAPI + "/tags/" + tag:
			return []byte(`{"tag_name":"JukaHub-20261111075528","body":""}`), nil
		case githubReleasesAPI + "?per_page=100&page=1":
			return []byte(`[{"tag_name":"JukaHub-20261111100000"},{"tag_name":"JukaHub-20261111075528"},{"tag_name":"JukaHub-20261111042006"}]`), nil
		case wantCompare:
			return []byte(`{"total_commits":1,"commits":[{"commit":{"message":"Add weather animations\n\nanimate weekly forecast conditions"}}]}`), nil
		default:
			return nil, fmt.Errorf("unexpected request %s", raw)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || !strings.HasPrefix(got, "## "+tag) ||
		!strings.Contains(got, "Changes since "+previous) ||
		!strings.Contains(got, "- Add weather animations") ||
		!strings.Contains(got, "animate weekly forecast conditions") ||
		strings.Contains(got, "JukaHub-20261111100000") {
		t.Fatalf("incorrect fallback notes %q; calls %v", got, calls)
	}
}

func TestPublishedReleaseNotesExpandFullChangelogLink(t *testing.T) {
	tag := "JukaHub-20261112025745"
	previous := "JukaHub-20261112004539"
	link := "https://github.com/jukaLang/JukaHubV2/compare/" + previous + "..." + tag
	wantCompare := githubCompareAPI + previous + "..." + tag + "?per_page=100&page=1"
	var calls []string
	got, err := fetchPublishedReleaseNotesWith(context.Background(), tag, func(_ context.Context, raw string) ([]byte, error) {
		calls = append(calls, raw)
		switch raw {
		case githubReleasesAPI + "/tags/" + tag:
			return []byte(fmt.Sprintf(`{"tag_name":%q,"name":"Bluetooth and packages","body":%q}`,
				tag, "**Full Changelog**: "+link)), nil
		case wantCompare:
			return []byte(`{"total_commits":1,"commits":[{"commit":{"message":"Expand Bluetooth, package, and image support\n\nAdd Bluetooth controls to the top bar and streamline device connection."}}]}`), nil
		default:
			return nil, fmt.Errorf("unexpected request %s", raw)
		}
	})
	if err != nil || len(calls) != 2 || !strings.HasPrefix(got, "## Bluetooth and packages") ||
		!strings.Contains(got, "Expand Bluetooth, package, and image support") ||
		!strings.Contains(got, "Add Bluetooth controls to the top bar") ||
		strings.Contains(got, link) {
		t.Fatalf("link-only notes = %q; calls %v; err %v", got, calls, err)
	}
	lines := strings.Join(plainReleaseNotes(got), "\n")
	if !strings.Contains(lines, "Bluetooth and packages") || !strings.Contains(lines, "Add Bluetooth controls") {
		t.Fatalf("handheld notes omit the title or description: %q", lines)
	}
}

func TestReleaseNotesKeepAuthoredBodyAndExpandFullChangelog(t *testing.T) {
	tag, previous := "v2.0.0", "v1.9.0"
	link := "https://github.com/jukaLang/JukaHubV2/compare/" + previous + "..." + tag
	rel := &githubRelease{TagName: tag, Name: "Version 2", Body: "## What's changed\n- New home screen\n\n**Full Changelog**: " + link}
	got, err := publishedReleaseNotesWith(context.Background(), rel, func(_ context.Context, raw string) ([]byte, error) {
		if raw != githubCompareAPI+previous+"..."+tag+"?per_page=100&page=1" {
			return nil, fmt.Errorf("unexpected request %s", raw)
		}
		return []byte(`{"total_commits":1,"commits":[{"commit":{"message":"Improve home screen\n\nRemember tile sizes."}}]}`), nil
	})
	if err != nil || !strings.Contains(got, "## What's changed") ||
		!strings.Contains(got, "Remember tile sizes.") || strings.Contains(got, link) {
		t.Fatalf("authored notes and comparison = %q; err %v", got, err)
	}
}

func TestFullChangelogFetchFailureKeepsLinkForRetry(t *testing.T) {
	withTempPatchState(t)
	patchStateMu.Lock()
	oldState := *patchState
	patchState.ReleaseNotesTag, patchState.ReleaseNotesText = "", ""
	patchStateMu.Unlock()
	t.Cleanup(func() {
		patchStateMu.Lock()
		*patchState = oldState
		patchStateMu.Unlock()
	})
	tag, previous := "v2.0.0", "v1.9.0"
	original := "## What's changed\n- New home screen\n\n**Full Changelog**: https://github.com/jukaLang/JukaHubV2/compare/" + previous + "..." + tag
	rel := &githubRelease{TagName: tag, Body: original}
	err := fillMissingReleaseNotesWith(context.Background(), rel, func(context.Context, string) ([]byte, error) {
		return nil, fmt.Errorf("offline")
	})
	if err == nil || rel.Body != original || !releaseNotesNeedExpansion(rel.Body) {
		t.Fatalf("failed comparison cannot be retried: body %q, err %v", rel.Body, err)
	}
}

func TestCachedFullChangelogLinkIsExpandedForUpdate(t *testing.T) {
	withTempPatchState(t)
	tag, previous := "JukaHub-20261112025745", "JukaHub-20261112004539"
	link := "https://github.com/jukaLang/JukaHubV2/compare/" + previous + "..." + tag
	patchStateMu.Lock()
	oldState := *patchState
	patchState.ReleaseNotesTag, patchState.ReleaseNotesText = tag, "**Full Changelog**: "+link
	patchStateMu.Unlock()
	t.Cleanup(func() {
		patchStateMu.Lock()
		*patchState = oldState
		patchStateMu.Unlock()
	})
	rel := &githubRelease{TagName: tag, Body: "**Full Changelog**: " + link}
	err := fillMissingReleaseNotesWith(context.Background(), rel, func(_ context.Context, raw string) ([]byte, error) {
		if raw != githubCompareAPI+previous+"..."+tag+"?per_page=100&page=1" {
			return nil, fmt.Errorf("unexpected request %s", raw)
		}
		return []byte(`{"total_commits":1,"commits":[{"commit":{"message":"Fix controller navigation\n\nThe D-pad now reaches every row."}}]}`), nil
	})
	if err != nil || !strings.Contains(rel.Body, "The D-pad now reaches every row.") ||
		strings.Contains(rel.Body, link) || len(plainReleaseNotes("**Full Changelog**: "+link)) != 0 {
		t.Fatalf("cached link was not replaced: %q; err %v", rel.Body, err)
	}
	if !releaseNotesNeedExpansion("## Changes since " + previous + "\n\n- Fix controller navigation") {
		t.Fatal("older subject-only notes would not be refreshed")
	}
}

func TestReleaseNotesRejectUnsafeTagBeforeRequest(t *testing.T) {
	called := false
	_, err := fetchPublishedReleaseNotesWith(context.Background(), "../../latest?x=1", func(context.Context, string) ([]byte, error) {
		called = true
		return nil, nil
	})
	if err == nil || called {
		t.Fatalf("unsafe tag reached the release API: called=%t, err=%v", called, err)
	}
}

func TestChangelogKeepsInstalledNotesWhenUpdateNotesChange(t *testing.T) {
	withTempPatchState(t)
	installed := "JukaHub-20261111042006"
	newer := "JukaHub-20261111075528"

	releaseInfoMu.Lock()
	oldInfo, oldLoaded := releaseInfoCache, releaseInfoLoaded
	releaseInfoCache, releaseInfoLoaded = releaseInfo{Tag: installed}, true
	releaseInfoMu.Unlock()
	t.Cleanup(func() {
		releaseInfoMu.Lock()
		releaseInfoCache, releaseInfoLoaded = oldInfo, oldLoaded
		releaseInfoMu.Unlock()
	})

	patchStateMu.Lock()
	oldState := *patchState
	patchState.ReleaseNotesTag = installed // old state before separate caches
	patchState.ReleaseNotesText = "## Fixed\n- Installed build fix"
	patchState.InstalledNotesTag = ""
	patchState.InstalledNotesText = ""
	patchStateMu.Unlock()
	t.Cleanup(func() {
		patchStateMu.Lock()
		*patchState = oldState
		patchStateMu.Unlock()
	})

	startChangelog(nil) // migrates the old installed notes
	patchStateMu.Lock()
	patchState.ReleaseNotesTag = newer
	patchState.ReleaseNotesText = "## Added\n- Newer update feature"
	patchStateMu.Unlock()
	text := strings.Join(communityChangelogText(nil), "\n")
	if !strings.Contains(text, "Installed build fix") || strings.Contains(text, "Newer update feature") {
		t.Fatalf("changelog showed the wrong release: %s", text)
	}
}
