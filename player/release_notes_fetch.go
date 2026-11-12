package main

// Published releases may have an empty body (the timestamped release workflow
// did this for years). In that case, use the commits between published release
// tags as a factual changelog for the installed build.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

const githubReleasesAPI = "https://api.github.com/repos/jukaLang/JukaHubV2/releases"
const githubCompareAPI = "https://api.github.com/repos/jukaLang/JukaHubV2/compare/"

type releaseNotesFetch func(context.Context, string) ([]byte, error)

func fetchReleaseNotesURL(ctx context.Context, raw string) ([]byte, error) {
	return fetchBounded(ctx, newPatchClient(), raw, patchMaxMetadata)
}

func validReleaseNotesTag(tag string) bool {
	if tag == "" || len(tag) > 128 {
		return false
	}
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			(i > 0 && (c == '.' || c == '-' || c == '_')) {
			continue
		}
		return false
	}
	return true
}

func fetchPublishedReleaseNotes(ctx context.Context, tag string) (string, error) {
	return fetchPublishedReleaseNotesWith(ctx, tag, fetchReleaseNotesURL)
}

func fetchPublishedReleaseNotesWith(ctx context.Context, tag string, fetch releaseNotesFetch) (string, error) {
	if !validReleaseNotesTag(tag) {
		return "", fmt.Errorf("invalid release tag %q", tag)
	}
	data, err := fetch(ctx, githubReleasesAPI+"/tags/"+url.PathEscape(tag))
	if err != nil {
		return "", err
	}
	var rel githubRelease
	if err := json.Unmarshal(data, &rel); err != nil {
		return "", fmt.Errorf("read release %s: %w", tag, err)
	}
	if !strings.EqualFold(rel.TagName, tag) {
		return "", fmt.Errorf("release response did not match %s", tag)
	}
	if body := strings.TrimSpace(rel.Body); body != "" {
		return body, nil
	}
	return releaseNotesFromCommitsWith(ctx, tag, fetch)
}

// fillMissingReleaseNotes gives update prompts notes for older releases with
// empty GitHub bodies. A failed fallback must not prevent an update check.
func fillMissingReleaseNotes(ctx context.Context, rel *githubRelease) error {
	if rel == nil || strings.TrimSpace(rel.Body) != "" {
		return nil
	}
	if !validReleaseNotesTag(rel.TagName) {
		return fmt.Errorf("invalid release tag %q", rel.TagName)
	}
	if cachedTag, cachedBody := cachedReleaseNotes(); strings.EqualFold(cachedTag, rel.TagName) && cachedBody != "" {
		rel.Body = cachedBody
		return nil
	}
	body, err := releaseNotesFromCommitsWith(ctx, rel.TagName, fetchReleaseNotesURL)
	if err == nil {
		rel.Body = body
	}
	return err
}

func releaseNotesFromCommitsWith(ctx context.Context, tag string, fetch releaseNotesFetch) (string, error) {
	previous, err := previousPublishedReleaseTagWith(ctx, tag, fetch)
	if err != nil {
		return "", err
	}
	type commit struct {
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	var subjects []string
	var total int
	for page := 1; page <= 3; page++ {
		raw := fmt.Sprintf("%s%s...%s?per_page=100&page=%d", githubCompareAPI,
			url.PathEscape(previous), url.PathEscape(tag), page)
		data, err := fetch(ctx, raw)
		if err != nil {
			return "", err
		}
		var comparison struct {
			TotalCommits int      `json:"total_commits"`
			Commits      []commit `json:"commits"`
		}
		if err := json.Unmarshal(data, &comparison); err != nil {
			return "", fmt.Errorf("read commits for %s: %w", tag, err)
		}
		total = comparison.TotalCommits
		for _, c := range comparison.Commits {
			subject := strings.TrimSpace(strings.SplitN(c.Commit.Message, "\n", 2)[0])
			if subject != "" {
				subjects = append(subjects, subject)
			}
		}
		if len(comparison.Commits) < 100 || page*100 >= total {
			break
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "## Changes since %s\n\n", previous)
	out.WriteString("Based on the commits in this release:\n\n")
	if len(subjects) == 0 {
		out.WriteString("No source changes were recorded between these releases.")
	} else {
		for _, subject := range subjects {
			fmt.Fprintf(&out, "- %s\n", subject)
		}
	}
	if total > len(subjects) {
		fmt.Fprintf(&out, "\nShowing %d of %d commits; see the GitHub comparison for the rest.", len(subjects), total)
	}
	return strings.TrimSpace(out.String()), nil
}

func previousPublishedReleaseTagWith(ctx context.Context, tag string, fetch releaseNotesFetch) (string, error) {
	found := false
	for page := 1; page <= 50; page++ {
		raw := fmt.Sprintf("%s?per_page=100&page=%d", githubReleasesAPI, page)
		data, err := fetch(ctx, raw)
		if err != nil {
			return "", err
		}
		var releases []struct {
			TagName string `json:"tag_name"`
		}
		if err := json.Unmarshal(data, &releases); err != nil {
			return "", fmt.Errorf("read release list: %w", err)
		}
		for _, rel := range releases {
			if !found {
				found = strings.EqualFold(rel.TagName, tag)
				continue
			}
			if validReleaseNotesTag(rel.TagName) && sameReleaseTagFamily(tag, rel.TagName) {
				return rel.TagName, nil
			}
		}
		if len(releases) < 100 {
			break
		}
	}
	return "", fmt.Errorf("no preceding published release found for %s", tag)
}

func sameReleaseTagFamily(a, b string) bool {
	if strings.HasPrefix(a, "JukaHub-") {
		return strings.HasPrefix(b, "JukaHub-")
	}
	if strings.HasPrefix(a, "v") {
		return strings.HasPrefix(b, "v")
	}
	return true
}
