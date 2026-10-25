package main

import (
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// docsLinkTarget matches the destination of an inline Markdown link or image:
// [text](target) and ![alt](target), with an optional title.
var docsLinkTarget = regexp.MustCompile(`!?\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)

// docsLinkSkip are the trees whose Markdown is not documentation: vendored
// modules, downloaded dependencies, the build cache and the website sources
// (which live in a separate repository with their own site generator).
var docsLinkSkip = map[string]bool{
	".git":         true,
	".sdl2":        true,
	"vendor":       true,
	"node_modules": true,
	"tmp":          true,
	"dist":         true,
}

// TestDocsRelativeLinksExist keeps documentation links honest. Relative links
// rot silently when pages are renamed, moved or never land: docs/ANDROID.md
// linked three times to a study page that was not in the tree, and nothing
// failed. Every relative destination in every Markdown file must resolve to a
// path in this checkout.
//
// Anchors (docs/STYLE.md#tests) are checked as far as the file: the fragment
// addresses a heading, not a path, and heading renames are a review concern
// rather than something this test can usefully assert.
func TestDocsRelativeLinksExist(t *testing.T) {
	const root = ".."

	var files []string
	walkErr := filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if docsLinkSkip[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(file), ".md") {
			files = append(files, file)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk repository: %v", walkErr)
	}
	if len(files) == 0 {
		t.Fatal("no Markdown files found; is the test running from player/?")
	}

	var missing []string
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("read %s: %v", file, err)
			continue
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			t.Fatalf("relative path for %s: %v", file, err)
		}
		pageDir := path.Dir(filepath.ToSlash(rel))
		inFence := false
		for lineNo, line := range strings.Split(string(body), "\n") {
			if fence := strings.TrimSpace(line); strings.HasPrefix(fence, "```") || strings.HasPrefix(fence, "~~~") {
				// Links inside fenced code are examples, not navigation.
				inFence = !inFence
				continue
			}
			if inFence {
				continue
			}
			for _, match := range docsLinkTarget.FindAllStringSubmatch(line, -1) {
				resolved, ok := docsLinkPath(pageDir, strings.TrimSpace(match[1]))
				if !ok {
					continue
				}
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(resolved))); err != nil {
					missing = append(missing, filepath.ToSlash(rel)+":"+strconv.Itoa(lineNo+1)+" -> "+resolved)
				}
			}
		}
	}

	if len(missing) > 0 {
		t.Fatalf("%d broken documentation link(s):\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
}

// docsLinkPath turns a Markdown link destination into a repository-relative
// path, or reports that the destination is not this test's business: absolute
// URLs, mailto:, anchors and template placeholders all resolve elsewhere.
func docsLinkPath(pageDir, target string) (string, bool) {
	if target == "" || strings.HasPrefix(target, "#") {
		return "", false
	}
	if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "tel:") {
		return "", false
	}
	if strings.HasPrefix(target, "<") || strings.ContainsAny(target, "{}") {
		// <placeholder> and ${...} destinations are documentation templates.
		return "", false
	}
	if cut := strings.IndexAny(target, "#?"); cut >= 0 {
		target = target[:cut]
	}
	// Markdown allows %20 and friends in destinations; the filesystem does not.
	if unescaped, err := url.PathUnescape(target); err == nil {
		target = unescaped
	}
	if target == "" || strings.HasPrefix(target, "/") {
		return "", false
	}
	return path.Join(pageDir, target), true
}
