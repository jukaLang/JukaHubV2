// Command make-delta builds the .jkdelta bundle that updates an installed
// JukaHub release to a newer one by downloading only what changed.
//
//	cd player
//	go run ./tools/make-delta \
//	    -old prev/JukaHub-Trimui.zip \
//	    -new dist/JukaHub-Trimui.zip \
//	    -out dist/
//
// -old and -new are release archives (.zip) or unpacked release folders. The
// tags default to the release-info.json inside each; -from-tag and -to-tag
// override them. The bundle is named JukaHub-<platform>-<fromStamp>.jkdelta,
// the name the updater looks for (internal/jdelta.AssetName), and is written
// into -out when that is a folder.
//
// Before it reports success the tool writes -old to a temporary folder and
// rebuilds -new from it with the same code the device runs, so a bundle that
// would not apply is never published. Publish the bundle beside the archives
// and list it in release.json (tools/sign-release -asset ...): the updater only
// uses a delta it can verify, and downloads the full archive otherwise.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"jukahub/JukaHub/internal/jdelta"
)

// deltaMinSize is the size below which a file ships whole: a patch of a few
// kilobytes saves nothing worth the chance that the device's copy was edited.
const deltaMinSize = 64 << 10

// maxTreeBytes bounds what one release may unpack to.
const maxTreeBytes = int64(2 << 30)

// treeFile is one file of a release.
type treeFile struct {
	data []byte
	mode uint32
}

// releaseTree is a release's installable files by slash path, relative to the
// folder that holds the executable.
type releaseTree map[string]treeFile

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "make-delta:", err)
		os.Exit(1)
	}
}

// run is main without the process exit, so the tool can be tested end to end.
func run(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("make-delta", flag.ContinueOnError)
	oldPath := flags.String("old", "", "the previous release: archive (.zip) or unpacked folder (required)")
	newPath := flags.String("new", "", "the new release: archive (.zip) or unpacked folder (required)")
	platform := flags.String("platform", "", "platform word of the archive name (default: from -new, e.g. Trimui)")
	fromTag := flags.String("from-tag", "", "tag of the previous release (default: its release-info.json)")
	toTag := flags.String("to-tag", "", "tag of the new release (default: its release-info.json)")
	out := flags.String("out", ".", "output folder or file")
	verify := flags.Bool("verify", true, "rebuild -new from -old with the bundle before reporting success")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*oldPath) == "" || strings.TrimSpace(*newPath) == "" {
		return errors.New("-old and -new are required")
	}

	oldTree, err := loadTree(*oldPath)
	if err != nil {
		return fmt.Errorf("old release: %w", err)
	}
	newTree, err := loadTree(*newPath)
	if err != nil {
		return fmt.Errorf("new release: %w", err)
	}
	if *fromTag == "" {
		*fromTag = treeTag(oldTree)
	}
	if *toTag == "" {
		*toTag = treeTag(newTree)
	}
	if *platform == "" {
		*platform = jdelta.PlatformFromArchive(filepath.Base(*newPath))
	}
	name := jdelta.AssetName(*platform, *fromTag)
	if name == "" {
		return fmt.Errorf("cannot name the delta from platform %q and tag %q (pass -platform and -from-tag)", *platform, *fromTag)
	}
	if *toTag != "" && strings.EqualFold(*toTag, *fromTag) {
		return fmt.Errorf("both releases are %s", *fromTag)
	}

	dest := *out
	if info, err := os.Stat(dest); err == nil && info.IsDir() {
		dest = filepath.Join(dest, name)
	} else if strings.HasSuffix(dest, "/") || strings.HasSuffix(dest, string(os.PathSeparator)) {
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		dest = filepath.Join(dest, name)
	}

	var buf bytes.Buffer
	meta := jdelta.Bundle{Platform: *platform, FromTag: *fromTag, ToTag: *toTag}
	index, err := jdelta.WriteBundle(&buf, meta, bundleInputs(oldTree, newTree))
	if err != nil {
		return err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	if *verify {
		if err := verifyBundle(tmp, oldTree, newTree); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("the bundle does not rebuild the new release: %w", err)
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	sum := sha256.Sum256(buf.Bytes())
	var unpacked int64
	for _, f := range newTree {
		unpacked += int64(len(f.data))
	}
	fmt.Fprintf(stdout, "Wrote %s (%d bytes, sha256 %x)\n", dest, buf.Len(), sum[:])
	fmt.Fprintf(stdout, "  %s -> %s on %s, %d files\n", *fromTag, *toTag, *platform, len(index.Files))
	if unpacked > 0 {
		fmt.Fprintf(stdout, "  the bundle is %.1f%% of the %d bytes the release unpacks to\n",
			100*float64(buf.Len())/float64(unpacked), unpacked)
	}
	if info, err := os.Stat(*newPath); err == nil && !info.IsDir() {
		fmt.Fprintf(stdout, "  the full archive is %d bytes\n", info.Size())
	}
	for _, f := range index.Files {
		fmt.Fprintf(stdout, "  %-9s %-36s %10d -> %d\n", f.Base, f.Name, f.NewSize, f.Length)
	}
	fmt.Fprintf(stdout, "Publish it beside the archives and sign it with tools/sign-release -asset %s=%s\n", name, dest)
	return nil
}

// bundleInputs lists every file of the new release, patched against the old
// release's copy where that is worth it and safe.
func bundleInputs(oldTree, newTree releaseTree) []jdelta.BundleInput {
	names := make([]string, 0, len(newTree))
	for name := range newTree {
		names = append(names, name)
	}
	sort.Strings(names)
	inputs := make([]jdelta.BundleInput, 0, len(names))
	for _, name := range names {
		nf := newTree[name]
		in := jdelta.BundleInput{Name: name, Mode: nf.mode, New: nf.data}
		if isExecutable(name) {
			in.Mode = 0o755
		}
		if of, ok := oldTree[name]; ok && deltaWorthy(name, of.data, nf.data) {
			in.Old = of.data
			in.Delta = true
		}
		inputs = append(inputs, in)
	}
	return inputs
}

// deltaWorthy decides which files are patched against the device's copy. JSON
// always ships whole: the device edits its own jukaconfig.json, and a patch
// against an edited base cannot apply.
func deltaWorthy(name string, oldData, newData []byte) bool {
	if strings.HasSuffix(strings.ToLower(name), ".json") {
		return false
	}
	return len(oldData) >= deltaMinSize && len(newData) >= deltaMinSize
}

// isExecutable reports whether name is JukaHub's own executable.
func isExecutable(name string) bool {
	base := strings.ToLower(path.Base(name))
	return base == "jukahub" || base == "jukahub.exe"
}

// loadTree reads a release archive or an unpacked release folder.
func loadTree(p string) (releaseTree, error) {
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return loadDirTree(p)
	}
	if strings.HasSuffix(strings.ToLower(p), ".zip") {
		return loadZipTree(p)
	}
	return nil, fmt.Errorf("%s: want a .zip archive or a folder", p)
}

func loadZipTree(p string) (releaseTree, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	all := map[string]treeFile{}
	var total int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !f.Mode().IsRegular() {
			continue
		}
		name := path.Clean(strings.ReplaceAll(f.Name, "\\", "/"))
		if f.UncompressedSize64 > jdelta.MaxFileSize {
			return nil, fmt.Errorf("%s: %s is too large", p, name)
		}
		total += int64(f.UncompressedSize64)
		if total > maxTreeBytes {
			return nil, fmt.Errorf("%s unpacks to more than %d bytes", p, maxTreeBytes)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(rc, jdelta.MaxFileSize+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", p, name, err)
		}
		all[name] = treeFile{data: data, mode: uint32(f.Mode().Perm())}
	}
	return rootTree(p, all)
}

func loadDirTree(dir string) (releaseTree, error) {
	all := map[string]treeFile{}
	var total int64
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > jdelta.MaxFileSize {
			return fmt.Errorf("%s is too large", p)
		}
		total += info.Size()
		if total > maxTreeBytes {
			return fmt.Errorf("%s holds more than %d bytes", dir, maxTreeBytes)
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		all[filepath.ToSlash(rel)] = treeFile{data: data, mode: uint32(info.Mode().Perm())}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rootTree(dir, all)
}

// rootTree re-roots a release at the folder that holds its executable, which
// is what the updater installs: the archives wrap everything in a folder
// (JukaHub-Trimui/). Helper tools under required/ are never replaced by an
// update, so they are left out.
func rootTree(src string, all map[string]treeFile) (releaseTree, error) {
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	root, found := "", false
	for _, name := range names {
		if isExecutable(name) {
			root, found = path.Dir(name), true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("%s holds no JukaHub executable", src)
	}
	prefix := ""
	if root != "." {
		prefix = root + "/"
	}
	tree := releaseTree{}
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rel := strings.TrimPrefix(name, prefix)
		first, _, _ := strings.Cut(rel, "/")
		if strings.EqualFold(first, "required") || strings.HasPrefix(path.Base(rel), "apply-update") {
			continue
		}
		if !jdelta.ValidName(rel) {
			return nil, fmt.Errorf("%s: unusable file name %q", src, rel)
		}
		tree[rel] = all[name]
	}
	return tree, nil
}

// treeTag reads the tag a release's release-info.json names.
func treeTag(tree releaseTree) string {
	f, ok := tree["release-info.json"]
	if !ok {
		return ""
	}
	var info struct {
		Tag string `json:"tag"`
	}
	if json.Unmarshal(f.data, &info) != nil {
		return ""
	}
	return strings.TrimSpace(info.Tag)
}

// verifyBundle writes oldTree to a temporary folder and rebuilds newTree from
// it with the bundle, exactly as the device does.
func verifyBundle(bundlePath string, oldTree, newTree releaseTree) error {
	work, err := os.MkdirTemp("", "make-delta-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	base := filepath.Join(work, "installed")
	for name, f := range oldTree {
		p := filepath.Join(base, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, f.data, 0o644); err != nil {
			return err
		}
	}
	rebuilt := filepath.Join(work, "rebuilt")
	files, err := jdelta.ApplyBundle(context.Background(), bundlePath, base, rebuilt)
	if err != nil {
		return err
	}
	if len(files) != len(newTree) {
		return fmt.Errorf("the bundle rebuilds %d files, the release has %d", len(files), len(newTree))
	}
	for name, f := range newTree {
		got, err := os.ReadFile(filepath.Join(rebuilt, filepath.FromSlash(name)))
		if err != nil {
			return fmt.Errorf("%s was not rebuilt: %w", name, err)
		}
		if !bytes.Equal(got, f.data) {
			return fmt.Errorf("%s was rebuilt wrong", name)
		}
	}
	return nil
}
