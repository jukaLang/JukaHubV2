package main

// companion_drop.go — the Phone Companion's drop card: drop games, covers,
// music and anything else from any browser (over Wi-Fi, or the USB network
// link), and each file goes where it belongs on the handheld.
//
// One file travels in two steps, both behind the PIN (companionServer.guard):
//
//   - POST /api/drop/plan?name=…&size=… asks where it goes. The answer is
//     decided here (companion_drop_route.go) and is a share path, never a
//     folder on the host. When the file's header or a .zip's directory would
//     settle it, the answer asks for those bytes ("need": the page sends the
//     slice of the file again, at most 256 KiB); when nothing can, it offers
//     consoles (or, for a cover, games) to pick from, and the page asks again
//     with the choice. So the file itself is sent once, to its place.
//   - POST /api/drop?plan=<token>[&replace=1] carries the file. The token
//     names the decision the handheld kept, so the page cannot send a file
//     anywhere the plan did not say. It is stored the way the Files card
//     stores an upload (companionFilesStore): the folders are opened afresh
//     as os.Roots, streamed to a hidden temporary name and renamed when
//     complete, read-only folders refused, and a file of the same name kept
//     unless the page asked to replace it (after confirm()).
//
// POST /api/drop/done?batch=… ends a batch: the Games library rescans when it
// is open, the media library looks for new songs and videos, and the handheld
// shows one line, "Phone sent 3 games, 1 cover and 12 songs". Whole folders
// travel their own way (companion_dropdir.go) and join that line.
//
// A cover goes beside its game (Imgs, or NextUI's .media), where the stock
// firmware shows it too. When JukaHub already keeps a cover of its own for
// that game (romArtworkPath), which the library would show first, the picture
// replaces it the way Game options → Choose cover saves an Imgs picture.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// companionDropTicketTTL is how long a decision waits for its file.
	companionDropTicketTTL = 2 * time.Hour
	// companionDropMaxTickets and companionDropMaxBatches bound what a page
	// that never finishes can leave behind.
	companionDropMaxTickets = 4096
	companionDropMaxBatches = 64
	// companionDropLibraryTTL is how long the list of games covers are
	// matched against is reused between files.
	companionDropLibraryTTL = 2 * time.Minute
	// companionDropSharedScanAge is how old the Games library's own last walk
	// (romScanShared) may be to stand in for a walk of our own.
	companionDropSharedScanAge = 10 * time.Minute
	// companionDropCoverTimeout bounds turning a picture into JukaHub's cover.
	companionDropCoverTimeout = 20 * time.Second
	// companionDropEnvTTL is how long the Roms and BIOS folders are reused.
	companionDropEnvTTL = 10 * time.Second
)

// companionDropTicket is a decision waiting for its file.
type companionDropTicket struct {
	dir   string // share path of the folder
	name  string
	kind  string
	batch string
	rom   scannedROM // the game a cover belongs to
	at    time.Time
}

// companionDrops is the drop card's state: decisions, the batches' counts and
// the games covers are matched against. Any goroutine.
var companionDrops struct {
	sync.Mutex
	tickets map[string]companionDropTicket
	batches map[string]*companionDropBatch
	lib     []scannedROM
	libAt   time.Time
	gameAt  time.Time // when a game last arrived: the lists before are stale
	env     companionDropEnv
	envAt   time.Time
}

// companionDropBatch counts what arrived, by kind.
type companionDropBatch struct {
	counts map[string]int
	at     time.Time
}

// handleDropPlan answers POST /api/drop/plan?name=&size=[&part=head|tail]
// [&system=][&game=][&to=drop][&batch=], with the file's head or tail as the
// body when part is set.
func (s *companionServer) handleDropPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	size, err := strconv.ParseInt(q.Get("size"), 10, 64)
	if err != nil || size < 0 {
		http.Error(w, "bad size", http.StatusBadRequest)
		return
	}
	name := sanitizeUploadName(q.Get("name"))
	if size > companionMaxUpload-(64<<10) {
		http.Error(w, name+" is larger than the handheld takes (4 GB).", http.StatusRequestEntityTooLarge)
		return
	}
	in := companionDropIn{
		Name:   name,
		Size:   size,
		System: strings.TrimSpace(q.Get("system")),
		Game:   strings.TrimSpace(q.Get("game")),
		Plain:  q.Get("to") == "drop",
	}
	if part := q.Get("part"); part == "head" || part == "tail" {
		limit := int64(companionDropHeadBytes)
		if part == "tail" {
			limit = companionDropTailBytes
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
		if err != nil {
			http.Error(w, "upload interrupted", http.StatusBadRequest)
			return
		}
		if int64(len(data)) > limit {
			http.Error(w, "too many bytes", http.StatusRequestEntityTooLarge)
			return
		}
		if data == nil {
			data = []byte{}
		}
		if part == "head" {
			in.Head = data
		} else {
			in.Tail = data
		}
	}
	dropDir := s.companionDropPrepare(name)
	set, ok := openCompanionFileShares(w)
	if !ok {
		return
	}
	defer set.close()
	ext := strings.ToLower(path.Ext(name))
	wantGames := companionDropCoverExts[ext] && !in.Plain && in.System == "" &&
		!strings.HasSuffix(strings.ToLower(name), ".p8.png")
	env, games := companionDropEnvFor(set, dropDir, wantGames)
	plan := companionDropRoute(in, env)
	switch plan.Kind {
	case companionDropNeedKind:
		writeCompanionJSON(w, map[string]interface{}{"kind": plan.Kind, "need": plan.Need, "bytes": plan.NeedBytes})
		return
	case companionDropAskKind:
		writeCompanionJSON(w, map[string]interface{}{"kind": plan.Kind, "pick": plan.Pick, "choices": plan.Choices, "note": plan.Note})
		return
	}
	dir, target, err := companionDropTarget(set, plan)
	if err != nil && plan.Kind != companionDropPlainKind {
		logWarn("companion", "drop: %s cannot go to %s: %v", name, plan.Dir, err)
		plan = companionDropPlan{Kind: companionDropPlainKind, Dir: env.Drop, Name: name, Label: "PhoneDrop",
			Note: "Its folder cannot be written from the phone, so it waits in PhoneDrop."}
		dir, target, err = companionDropTarget(set, plan)
	}
	if err != nil {
		logWarn("companion", "drop: PhoneDrop is not available: %v", err)
		http.Error(w, "The PhoneDrop folder cannot be written.", http.StatusServiceUnavailable)
		return
	}
	// What a replace would overwrite, as the question the page asks first.
	ask := ""
	if info, err := target.share.root.Lstat(target.rel); err == nil {
		if info.IsDir() {
			http.Error(w, "A folder named "+plan.Name+" is already in "+dir.display()+".", http.StatusConflict)
			return
		}
		ask = plan.Name + " is already in " + dir.display() + ". Replace it?"
	}
	t := companionDropTicket{dir: dir.virtual, name: plan.Name, kind: plan.Kind, batch: companionDropBatchID(q.Get("batch"))}
	if plan.Kind == companionDropCoverKind {
		for _, g := range games {
			if g.virtual == plan.Game {
				t.rom = g.rom
				break
			}
		}
		if ask == "" && companionDropOwnCover(t.rom) != "" {
			ask = romGameStem(t.rom.Path) + " already has a cover. Use this picture instead?"
		}
	}
	token, err := companionDropRemember(t)
	if err != nil {
		http.Error(w, "The handheld could not keep the plan.", http.StatusInternalServerError)
		return
	}
	writeCompanionJSON(w, map[string]interface{}{
		"kind":    plan.Kind,
		"dest":    target.virtual,
		"label":   plan.Label,
		"note":    plan.Note,
		"system":  companionDropSystemParam(plan.System),
		"exists":  ask != "",
		"confirm": ask,
		"token":   token,
	})
}

// handleDrop takes POST /api/drop?plan=<token>[&replace=1] with one
// multipart "file" part and stores it where the plan said.
func (s *companionServer) handleDrop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	token := q.Get("plan")
	t, ok := companionDropTicketFor(token)
	if !ok {
		http.Error(w, "The handheld no longer knows where this file goes. Drop it again.", http.StatusGone)
		return
	}
	limit := int64(companionMaxUpload)
	if t.kind == companionDropCoverKind {
		// The library reads no bigger picture (artworkLocalCover).
		limit = artworkImageMaxBytes + 64<<10
	}
	if r.ContentLength > limit {
		http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	replace := q.Get("replace") == "1"
	if t.kind == companionDropCoverKind && !replace && companionDropOwnCover(t.rom) != "" {
		http.Error(w, romGameStem(t.rom.Path)+" already has a cover.", http.StatusConflict)
		return
	}
	set, ok := openCompanionFileShares(w)
	if !ok {
		return
	}
	defer set.close()
	dir, ok := companionFilesResolve(w, set, t.dir)
	if !ok {
		return
	}
	if dir.isRoot() || dir.protected || companionFilesAliasProtected(dir) {
		http.Error(w, companionFilesReadOnlyMsg, http.StatusForbidden)
		return
	}
	if dir.rel != "." {
		if err := dir.share.root.MkdirAll(dir.rel, 0o755); err != nil {
			logWarn("companion", "drop: cannot make %s: %v", dir.display(), err)
			http.Error(w, "Cannot create the folder "+dir.display()+".", http.StatusInternalServerError)
			return
		}
	}
	file, ok := companionFilesFilePart(w, r)
	if !ok {
		return
	}
	defer file.Close()
	target, written, ok := companionFilesStore(w, set, dir, t.name, file, replace)
	if !ok {
		return
	}
	if t.kind == companionDropCoverKind {
		companionDropCoverArrived(t.rom, filepath.Join(target.share.dir, target.rel))
	}
	companionDropArrived(token, t)
	logInfo("companion", "drop: %s to %s (%d bytes)", t.kind, target.display(), written)
	trophyEvent(tkFileTransfer)
	writeCompanionJSON(w, map[string]interface{}{"ok": true, "dest": target.virtual, "kind": t.kind, "size": written})
}

// handleDropDone takes POST /api/drop/done?batch=… after the page's last file:
// one line on the handheld for the whole batch, and the libraries look again.
func (s *companionServer) handleDropDone(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	batch := companionDropBatchID(r.URL.Query().Get("batch"))
	counts := companionDropTakeBatch(batch)
	// Whole folders sent in the batch (companion_dropdir.go), by kind.
	folders, folderKinds := companionDropDirTakeBatch(batch)
	companionDropDirLog(folders)
	text := companionDropDirSummary(companionDropSummary(counts), folders)
	if text != "" {
		s.sink(companionEvent{Kind: companionEventUpload, Text: text})
		if counts[companionDropGameKind]+counts[companionDropCoverKind]+folderKinds[companionDropGameKind] > 0 {
			tbxPost(func() {
				if gameLib.open {
					refreshGameLibrary()
				}
			})
		}
		if counts[companionDropMusicKind]+counts[companionDropVideoKind]+
			folderKinds[companionDropMusicKind]+folderKinds[companionDropVideoKind] > 0 {
			tbxPost(func() { mlibStartScan(appConfig, true) })
		}
		text = companionDropUpperFirst(text) + " arrived."
	}
	writeCompanionJSON(w, map[string]interface{}{"ok": true, "text": text})
}

// companionDropSummary is a batch in words: "3 games, 1 cover and 12 songs".
func companionDropSummary(counts map[string]int) string {
	nouns := []struct{ kind, one, many string }{
		{companionDropGameKind, "game", "games"},
		{companionDropCoverKind, "cover", "covers"},
		{companionDropMusicKind, "song", "songs"},
		{companionDropVideoKind, "video", "videos"},
		{companionDropBIOSKind, "BIOS file", "BIOS files"},
		{companionDropPortKind, "port", "ports"},
		{companionDropPlainKind, "file", "files"},
	}
	var parts []string
	for _, n := range nouns {
		if c := counts[n.kind]; c > 0 {
			parts = append(parts, pluralizeCount(c, n.one, n.many))
		}
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// ── Where things are ────────────────────────────────────────────────────

// companionDropPrepare makes the folders a file may need before the shares
// are opened, since a share is a folder that exists: PhoneDrop, and for a song
// or a video the card's Music or Videos folder the media library reads (on the
// handheld), else JukaHub's own music or video folder. It returns PhoneDrop's
// folder, "" when it cannot be made.
func (s *companionServer) companionDropPrepare(name string) string {
	drop, err := companionDropDirFor(s.root)
	if err != nil {
		logWarn("companion", "drop: cannot make PhoneDrop: %v", err)
		drop = ""
	}
	ext := strings.ToLower(path.Ext(name))
	switch {
	case isAudioFile(name):
		companionDropEnsureMedia([]string{"Music", "Media"}, "Music", downloadsMusicDir())
	case mlibVideoExts[ext]:
		companionDropEnsureMedia([]string{"Videos", "Movies", "TV", "Media"}, "Videos", downloadsVideoDir())
	}
	return drop
}

// companionDropEnsureMedia makes sure a folder for songs or videos exists. On
// the handheld that is one of names among the card folders the media library
// reads, made as card when there is none; on a PC (whose own Music folder is
// not shared) JukaHub's folder fallback, which the audio player plays.
func companionDropEnsureMedia(names []string, card, fallback string) {
	if IsTrimuiDevice() {
		if companionDropMediaRoot(names) != "" {
			return
		}
		for _, root := range mlibBaseRoots() {
			if strings.EqualFold(filepath.Base(root), card) && os.MkdirAll(root, 0o755) == nil {
				return
			}
		}
	}
	if err := os.MkdirAll(fallback, 0o755); err != nil {
		logWarn("companion", "drop: cannot make %s: %v", fallback, err)
	}
}

// companionDropMediaRoot is the first of the media library's folders named
// one of names (in that order) that exists.
func companionDropMediaRoot(names []string) string {
	roots := mlibBaseRoots()
	for _, want := range names {
		for _, root := range roots {
			if strings.EqualFold(filepath.Base(root), want) && dirExists(root) {
				return root
			}
		}
	}
	return ""
}

// companionDropGameRef is a library game with its share path.
type companionDropGameRef struct {
	rom     scannedROM
	virtual string
}

// companionDropEnvFor is the card in share paths. The games (for a cover)
// are looked up only when wanted.
func companionDropEnvFor(set *ftpShareSet, dropDir string, wantGames bool) (companionDropEnv, []companionDropGameRef) {
	// A batch asks once per file: Roms and BIOS are worked out once for a few
	// seconds (biosDoctorRoots reads RetroArch's config). companionDropTarget
	// checks the answer against this request's shares all the same. The
	// folders companionDropPrepare may just have made are looked at each time.
	now := time.Now()
	companionDrops.Lock()
	env, at := companionDrops.env, companionDrops.envAt
	companionDrops.Unlock()
	if at.IsZero() || now.Sub(at) < 0 || now.Sub(at) > companionDropEnvTTL {
		env = companionDropFolders(set)
		companionDrops.Lock()
		companionDrops.env, companionDrops.envAt = env, now
		companionDrops.Unlock()
	}
	env.Drop, _ = companionDropShared(set, dropDir)
	env.Music = companionDropMediaShare(set, []string{"Music", "Media"}, downloadsMusicDir())
	env.Video = companionDropMediaShare(set, []string{"Videos", "Movies", "TV", "Media"}, downloadsVideoDir())
	if !wantGames {
		return env, nil
	}
	var refs []companionDropGameRef
	env.Games = nil
	for _, rom := range companionDropLibrary() {
		v, ok := companionDropVirtualOf(set, rom.Path)
		if !ok {
			continue
		}
		refs = append(refs, companionDropGameRef{rom: rom, virtual: v})
		env.Games = append(env.Games, companionDropGame{Path: v, Stem: romGameStem(rom.Path), System: rom.System})
	}
	return env, refs
}

// companionDropFolders works out Roms (with its console folders) and BIOS as
// share paths.
func companionDropFolders(set *ftpShareSet) companionDropEnv {
	env := companionDropEnv{NextUI: nextuiActive()}
	for _, root := range romLibraryRoots() {
		if !dirExists(root) {
			continue
		}
		if v, ok := companionDropShared(set, root); ok {
			env.Roms = v
			env.RomsDirs = companionDropListDirs(set, v)
			break
		}
	}
	for _, root := range biosDoctorRoots() {
		if !dirExists(root) {
			continue
		}
		if v, ok := companionDropShared(set, root); ok {
			env.BIOS = v
			break
		}
	}
	return env
}

// companionDropMediaShare is the share path of the folder songs or videos go
// in: the media library's own folder, else JukaHub's (fallback).
func companionDropMediaShare(set *ftpShareSet, names []string, fallback string) string {
	if root := companionDropMediaRoot(names); root != "" {
		if v, ok := companionDropShared(set, root); ok {
			return v
		}
	}
	if dirExists(fallback) {
		if v, ok := companionDropShared(set, fallback); ok {
			return v
		}
	}
	return ""
}

// companionDropVirtualOf maps a folder or file on the device to its share
// path, by name only: "" and false when it is in no share.
func companionDropVirtualOf(set *ftpShareSet, host string) (string, bool) {
	host = filepath.Clean(strings.TrimSpace(host))
	if host == "." || host == "" {
		return "", false
	}
	sep := string(filepath.Separator)
	for _, sh := range set.list {
		for _, base := range []string{filepath.Clean(sh.dir), filepath.Clean(sh.real)} {
			if base == "." || base == "" {
				continue
			}
			if strings.EqualFold(host, base) {
				return "/" + sh.name, true
			}
			if len(host) > len(base)+1 && strings.EqualFold(host[:len(base)+1], base+sep) {
				return "/" + sh.name + "/" + filepath.ToSlash(host[len(base)+1:]), true
			}
		}
	}
	return "", false
}

// companionDropShared is companionDropVirtualOf for a folder files may be
// written in: it resolves inside its share and is not read-only.
func companionDropShared(set *ftpShareSet, host string) (string, bool) {
	v, ok := companionDropVirtualOf(set, host)
	if !ok {
		return "", false
	}
	p, err := set.resolvePath("/", v)
	if err != nil || p.isRoot() || p.protected || companionFilesAliasProtected(p) {
		return "", false
	}
	return p.virtual, true
}

// companionDropListDirs lists the folder names right under the share path v.
func companionDropListDirs(set *ftpShareSet, v string) []string {
	p, err := set.resolvePath("/", v)
	if err != nil || p.isRoot() {
		return nil
	}
	f, err := p.share.root.Open(p.rel)
	if err != nil {
		return nil
	}
	defer f.Close()
	entries, _ := f.ReadDir(-1)
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

// companionDropTarget checks a plan against the shares: its folder inside a
// share and writable, and the file a name that folder can take.
func companionDropTarget(set *ftpShareSet, plan companionDropPlan) (ftpPath, ftpPath, error) {
	dir, err := set.resolvePath("/", plan.Dir)
	if err != nil {
		return ftpPath{}, ftpPath{}, err
	}
	if dir.isRoot() || dir.protected || companionFilesAliasProtected(dir) {
		return ftpPath{}, ftpPath{}, errors.New("read-only folder")
	}
	target, err := set.resolvePath(dir.virtual, plan.Name)
	if err != nil || target.share != dir.share || target.isShareTop() || target.protected {
		return ftpPath{}, ftpPath{}, errors.New("unusable name")
	}
	return dir, target, nil
}

// companionDropSystemParam is how the page names a console back: the ROM
// manager's id when it has one ("gba"), else the library's name.
func companionDropSystemParam(name string) string {
	for _, s := range rommSystems {
		if s.Name == name {
			return s.ID
		}
	}
	return name
}

// ── Covers ──────────────────────────────────────────────────────────────

// companionDropScanMu lets one walk of the card run at a time: a second page
// asking meanwhile waits for it and takes its answer.
var companionDropScanMu sync.Mutex

// companionDropLibrary is the list of games covers are matched against: the
// Games library's last walk while it is recent and no game arrived since,
// else a walk of our own, kept for a couple of minutes.
func companionDropLibrary() []scannedROM {
	companionDropScanMu.Lock()
	defer companionDropScanMu.Unlock()
	now := time.Now()
	companionDrops.Lock()
	gameAt := companionDrops.gameAt
	if companionDrops.lib != nil && companionDrops.libAt.After(gameAt) && now.Sub(companionDrops.libAt) < companionDropLibraryTTL {
		lib := companionDrops.lib
		companionDrops.Unlock()
		return lib
	}
	companionDrops.Unlock()
	romScanShared.Lock()
	games, at := romScanShared.games, romScanShared.at
	romScanShared.Unlock()
	if len(games) == 0 || !at.After(gameAt) || now.Sub(at) > companionDropSharedScanAge {
		games = scanROMLibrary(romLibraryRoots()).Games
	}
	if games == nil {
		games = []scannedROM{}
	}
	companionDrops.Lock()
	companionDrops.lib, companionDrops.libAt = games, now
	companionDrops.Unlock()
	return games
}

// companionDropOwnCover is JukaHub's own cover of rom when the library would
// show it before a picture beside the game: never on NextUI, where the card's
// cover comes first (romCoverPath). "" when there is none.
func companionDropOwnCover(rom scannedROM) string {
	if rom.Path == "" || nextuiActive() {
		return ""
	}
	art := romArtworkPath(rom, libraryArtworkRoot())
	if !artworkCoverPresent(art) {
		return ""
	}
	return art
}

// companionDropCoverArrived shows a cover saved at file beside rom: it also
// replaces JukaHub's own cover of the game when there is one (as Choose cover
// saves an Imgs picture), and the library picks the change up.
func companionDropCoverArrived(rom scannedROM, file string) {
	if rom.Path == "" {
		return
	}
	artworkDirForget(filepath.Dir(file))
	if art := companionDropOwnCover(rom); art != "" {
		ctx, cancel := context.WithTimeout(context.Background(), companionDropCoverTimeout)
		data, err := artChooseCoverBytes(ctx, artChooseCandidate{name: filepath.Base(file), path: file, local: true}, rom.Collection)
		cancel()
		if err == nil {
			err = gmetaWriteCache(art, data)
		}
		if err != nil {
			logWarn("companion", "drop: cannot use %s as the cover of %s: %v", filepath.Base(file), rom.Path, err)
		}
	}
	tbxPost(func() {
		artworkClearNoCover(rom) // a removed cover is wanted again (artwork_nocover.go)
		libraryCoverChanged(rom)
	})
}

// ── Tickets and batches ─────────────────────────────────────────────────

// companionDropRemember keeps a decision and returns its token.
func companionDropRemember(t companionDropTicket) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	now := time.Now()
	t.at = now
	companionDrops.Lock()
	defer companionDrops.Unlock()
	if companionDrops.tickets == nil {
		companionDrops.tickets = make(map[string]companionDropTicket)
	}
	if len(companionDrops.tickets) >= companionDropMaxTickets {
		oldest, oldestAt := "", now
		for k, v := range companionDrops.tickets {
			if now.Sub(v.at) > companionDropTicketTTL {
				delete(companionDrops.tickets, k)
				continue
			}
			if oldest == "" || v.at.Before(oldestAt) {
				oldest, oldestAt = k, v.at
			}
		}
		if len(companionDrops.tickets) >= companionDropMaxTickets && oldest != "" {
			delete(companionDrops.tickets, oldest)
		}
	}
	companionDrops.tickets[token] = t
	return token, nil
}

// companionDropTicketFor finds a decision that has not expired. It stays
// until its file arrives, so a refused upload can be sent again (replace).
func companionDropTicketFor(token string) (companionDropTicket, bool) {
	companionDrops.Lock()
	defer companionDrops.Unlock()
	t, ok := companionDrops.tickets[token]
	if !ok || token == "" {
		return companionDropTicket{}, false
	}
	if time.Since(t.at) > companionDropTicketTTL {
		delete(companionDrops.tickets, token)
		return companionDropTicket{}, false
	}
	return t, true
}

// companionDropArrived retires a decision whose file arrived and counts the
// file in its batch.
func companionDropArrived(token string, t companionDropTicket) {
	now := time.Now()
	companionDrops.Lock()
	defer companionDrops.Unlock()
	delete(companionDrops.tickets, token)
	if t.kind == companionDropGameKind {
		companionDrops.gameAt = now
	}
	if t.batch == "" {
		return
	}
	if companionDrops.batches == nil {
		companionDrops.batches = make(map[string]*companionDropBatch)
	}
	b := companionDrops.batches[t.batch]
	if b == nil {
		if len(companionDrops.batches) >= companionDropMaxBatches {
			oldest := ""
			for k, v := range companionDrops.batches {
				if oldest == "" || v.at.Before(companionDrops.batches[oldest].at) {
					oldest = k
				}
			}
			delete(companionDrops.batches, oldest)
		}
		b = &companionDropBatch{counts: make(map[string]int)}
		companionDrops.batches[t.batch] = b
	}
	b.counts[t.kind]++
	b.at = now
}

// companionDropTakeBatch returns a batch's counts and forgets it.
func companionDropTakeBatch(id string) map[string]int {
	if id == "" {
		return nil
	}
	companionDrops.Lock()
	defer companionDrops.Unlock()
	b := companionDrops.batches[id]
	delete(companionDrops.batches, id)
	if b == nil {
		return nil
	}
	return b.counts
}

// companionDropBatchID is the page's batch name when it is a plain short
// word, else "".
func companionDropBatchID(raw string) string {
	if raw == "" || len(raw) > 32 {
		return ""
	}
	for _, r := range raw {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return ""
		}
	}
	return raw
}
