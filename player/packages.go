package main

// packages.go — the JukaLang package registry: what a package is, where it
// installs, and the install itself.
//
// The store screen that lists and manages packages lives in the pkg*.go files
// (pkgcatalog.go: the model; pkgstate.go: state and background work; pkgui.go:
// drawing; pkginput.go: input; pkgverify.go: the checked download;
// pkgstage.go: the staged unpack, the swap and the rollback). This file keeps
// the parts every one of them leans on and that other code calls: the Package
// type, the install folder and the install itself.

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/ttf"
)

// packagesURL points to the upstream JukaLang package registry.
const packagesURL = "https://raw.githubusercontent.com/jukaLang/Packages/refs/heads/main/packages.json"

// Package is a single entry in the JukaLang package registry.
type Package struct {
	Name         string   `json:"name"`
	Author       string   `json:"author"`
	Description  string   `json:"description"`
	Date         string   `json:"date"`
	Version      string   `json:"version"`
	Dependencies []string `json:"dependencies"`
	MainFilename string   `json:"mainfilename"`
	Download     string   `json:"download"`
	Device       []string `json:"device"`
	// SHA256 is the download's published SHA-256 (lowercase hex) and
	// DownloadSize its exact byte count, both optional; HashProblem says why a
	// "sha256" that is present cannot be used. pkgParseCatalog fills them
	// (pkgApplyIntegrity), never the JSON decoder, so a malformed value can
	// never read as "no hash".
	SHA256       string `json:"-"`
	DownloadSize int64  `json:"-"`
	HashProblem  string `json:"-"`
}

// pkgInstallDirOverride points the package folder somewhere else; tests use a
// temporary directory so they never touch the real install.
var pkgInstallDirOverride string

// packageInstallDir returns the directory where downloaded packages are stored.
func packageInstallDir() string {
	dir := pkgInstallDirOverride
	if dir == "" {
		dir = filepath.Join(mustExecutableDir(), "packages")
	}
	os.MkdirAll(dir, 0755)
	return dir
}

// packageDirName is the directory a package installs into. The sanitising lives
// in one place so the install path, the installed check and the store's cards
// can never disagree about which directory a package means. Every path
// separator and every character Windows refuses become "_", and a name that
// would be "." or ".." — or hide among the store's own dot-files — gets a
// leading "_", so a registry entry can never name a folder outside the
// packages folder.
func packageDirName(name string) string {
	safeName := strings.ReplaceAll(name, " ", "_")
	safeName = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		if r < 0x20 {
			return '_'
		}
		return r
	}, safeName)
	if safeName == "" || strings.HasPrefix(safeName, ".") {
		safeName = "_" + safeName
	}
	return safeName
}

// pkgInstall downloads a package, verifies it, unpacks it beside the
// installed copy and swaps it in (pkgstage.go), reporting through the
// installer card, and says whether it worked. Whatever fails, the version that
// was installed stays installed and working: nothing is unpacked over it any
// more.
//
// It publishes through the shared installer card instead of the generic scene
// spinner: the card — and the activity strip that echoes it on every scene —
// names the package, shows the download's progress and its phases, and
// reports the outcome once. The download is tied to the card's run token, so
// the strip's X on this package's chip cancels the transfer.
func pkgInstall(config *Config, p Package) bool {
	// config stays unused: the installer card is global. The parameter is kept
	// so the call sites keep their shape.
	_ = config
	installBarBeginLabelled(p.Name, "Installing "+p.Name)

	if p.Download == "" {
		installBarFail("No download URL for " + p.Name)
		showToast("No download URL for "+p.Name, ToastError())
		return false
	}

	pkgEnsureRecovered()
	ctx := installBarRunContext()
	root := packageInstallDir()
	res, err := pkgInstallFrom(ctx, root, p, func(phase string) {
		installBarPhase(phase)
		if phase == installPhaseExtract {
			installBarResetProgress()
		}
	}, installBarProgress)
	if err != nil {
		if ctx.Err() != nil {
			// Cancelled from the card or the strip: the user already knows.
			return false
		}
		logWarn("packages", "install %s: %v", p.Name, err)
		card, toast := pkgInstallFailure(p, err)
		installBarFail(card)
		showToast(toast, ToastError())
		return false
	}
	if res.Verified {
		logInfo("packages", "%s: download verified against the catalogue (sha256 %s)", p.Name, res.SHA256)
	} else {
		logWarn("packages", "%s: not verified — the catalogue publishes no SHA-256 for it (the download's is %s)", p.Name, res.SHA256)
	}
	pkgRecordInstalled(p, res.Verified, res.SHA256)

	msg := "Installed " + p.Name
	if res.Kept {
		msg += " · the previous version is kept"
	}
	if res.Aside != "" {
		// The old rollback copy held files that would not move (or nothing
		// said they had); it was kept whole rather than deleted (pkgKeepAside).
		msg += pkgAsideNote
	}
	if !res.Verified {
		msg += " · not verified"
	}
	showToast(msg, ToastSuccess())
	installBarDone()
	// The rollback copy the swap replaced is deleted after the card settles.
	pkgEmptyTrash(root)
	return true
}

// pkgAsideNote is what a toast adds when an install kept an old rollback copy
// whole instead of deleting it (pkgKeepAside) — whether the install then
// worked or not, the copy stays there.
const pkgAsideNote = " · older files kept in packages/" + pkgRollbackDir

// pkgInstallFailure is the installer card's short reason and the toast's line
// for a failed install, from the step that failed.
func pkgInstallFailure(p Package, err error) (card, toast string) {
	switch {
	case errors.Is(err, errPkgChecksum):
		return "Checksum mismatch", p.Name + ": the download does not match the catalogue's checksum — not installed"
	case errors.Is(err, errPkgSize):
		return "Wrong size", p.Name + ": the download is not the size the catalogue publishes — not installed"
	}
	var step *pkgStepError
	if errors.As(err, &step) {
		switch step.Step {
		case pkgStepCatalogue:
			return "Not installed", p.Name + ": " + firstLine(step.Err.Error()) + " — not installed"
		case pkgStepUnpack:
			return "Could not unpack", "Could not unpack " + p.Name + ": " + firstLine(step.Err.Error())
		case pkgStepSwap:
			// What the toast promises follows what is on the card: a swap that
			// stopped halfway, an installed version left as it was, or a first
			// install that did not happen. A stranded swap is finished by the
			// next install of the package (pkgRecoverKeyLocked) or by the next
			// run's recovery pass, which opening Packages starts — not by
			// opening Packages again in this run (pkgEnsureRecovered runs once).
			aside := ""
			if step.Aside != "" {
				aside = pkgAsideNote
			}
			switch {
			case step.Stranded:
				return "Stopped halfway", p.Name + ": " + firstLine(step.Err.Error()) + " — install it again, or restart JukaHub and open Packages, to finish it; your files are kept" + aside
			case step.Installed:
				return "Could not replace the old version", p.Name + ": " + firstLine(step.Err.Error()) + " — the installed version is unchanged" + aside
			}
			return "Not installed", p.Name + ": " + firstLine(step.Err.Error()) + " — not installed" + aside
		case pkgStepRecover:
			return "Earlier install unfinished", p.Name + ": " + firstLine(step.Err.Error())
		}
	}
	return "Download failed", "Download failed — " + plainNetworkError(err)
}

// packageRepository uses the source's real host, and only labels GitHub URLs
// as repositories when they include an owner and repo. Other web downloads
// remain selectable without pretending they are GitHub repositories.
func packageRepository(raw string) (label, target string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	host := strings.ToLower(u.Hostname())
	if host == "api.github.com" && len(parts) >= 3 && parts[0] == "repos" {
		parts = parts[1:]
	}
	if (host == "github.com" || host == "www.github.com" || host == "raw.githubusercontent.com" || host == "api.github.com" || host == "codeload.github.com") && len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
		owner, e1 := url.PathUnescape(parts[0])
		repo, e2 := url.PathUnescape(parts[1])
		repo = strings.TrimSuffix(repo, ".git")
		if e1 == nil && e2 == nil && owner != "" && repo != "" && !strings.ContainsAny(owner+repo, "/\\") {
			return owner + "/" + repo, "https://github.com/" + url.PathEscape(owner) + "/" + url.PathEscape(repo), true
		}
	}
	return u.Hostname() + u.EscapedPath(), u.String(), true
}

// packageInstallHook is the install a press starts. Production queues the real
// download; a test swaps it to record the call instead of touching the
// network.
var packageInstallHook func(config *Config, p Package)

// startPackageInstall queues the install for a press. Installs run one at a
// time (pkgEnqueueInstall), because the installer card shows a single run and
// a second one used to cancel the first.
func startPackageInstall(config *Config, p Package) {
	if packageInstallHook != nil {
		packageInstallHook(config, p)
		return
	}
	pkgEnqueueInstall(config, p)
}

// wrapPackageText breaks text into lines that fit within maxWidth pixels using
// the given font's measured glyph widths.
func wrapPackageText(text string, font *ttf.Font, maxWidth int32) []string {
	var lines []string
	if font == nil {
		return []string{text}
	}
	for _, para := range strings.Split(text, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		cur := ""
		for _, w := range words {
			test := w
			if cur != "" {
				test = cur + " " + w
			}
			wid, _, _ := font.SizeUTF8(test)
			if cur != "" && int32(wid) > maxWidth {
				lines = append(lines, cur)
				cur = w
			} else {
				cur = test
			}
		}
		if cur != "" {
			lines = append(lines, cur)
		}
	}
	return lines
}
