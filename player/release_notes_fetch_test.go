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
		return []byte(`{"tag_name":"JukaHub-20261111075528","body":"## Added\n- Weather animations"}`), nil
	})
	if err != nil || got != "## Added\n- Weather animations" || calls != 1 {
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
	if len(calls) != 3 || !strings.Contains(got, "Changes since "+previous) ||
		!strings.Contains(got, "- Add weather animations") ||
		strings.Contains(got, "JukaHub-20261111100000") {
		t.Fatalf("incorrect fallback notes %q; calls %v", got, calls)
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
