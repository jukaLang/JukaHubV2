package main

// mlib_test.go — the Media Library without a card, ffprobe or ffmpeg: reading
// TV episodes, films and songs out of file names and folders, grouping them,
// the walk that skips unchanged folders, keeping what the slow pass learnt,
// the index round trip, a whole scan of a temporary folder, the poster
// scaler, and the folder list.

import (
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mlibResetState empties the live library: the start of every test.
func mlibResetState() {
	mlibState.mu.Lock()
	mlibState.loaded = false
	mlibState.items = nil
	mlibState.dirs = nil
	mlibState.resume = nil
	mlibState.model = nil
	mlibState.gen++
	mlibState.cancel = nil
	mlibState.found = 0
	mlibState.scanned = time.Time{}
	mlibState.mu.Unlock()
}

// mlibWaitIdle waits until no scan and no index write is running. The scan is
// read first: a scan raises saving before it clears mlibScanning, so a scan
// seen finished cannot hide a save that is still running.
func mlibWaitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !mlibScanning.Load() {
			mlibState.mu.Lock()
			saving := mlibState.saving
			mlibState.mu.Unlock()
			if !saving {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Error("the library was still scanning or saving after 10 s")
}

// mlibTestState gives one test the library to itself: an empty live list, the
// index and the posters in a fresh folder, no ffprobe and no ffmpeg, and the
// scan pointed at roots instead of the machine's own folders.
func mlibTestState(t *testing.T, roots ...string) string {
	t.Helper()
	dir := t.TempDir()
	prevDir, prevProbe, prevRoots, prevGrab := mlibDirOverride, mlibProbe, mlibBaseRoots, dlPosterGrabber
	mlibDirOverride = dir
	mlibProbe = func(context.Context, string, string) (mlibTags, error) { return mlibTags{}, os.ErrNotExist }
	mlibBaseRoots = func() []string { return append([]string(nil), roots...) }
	dlPosterGrabber = func(string) []byte { return nil }
	mlibResetState()
	t.Cleanup(func() {
		mlibCancelScan()
		mlibWaitIdle(t)
		mlibResetState()
		mlibDirOverride, mlibProbe, mlibBaseRoots, dlPosterGrabber = prevDir, prevProbe, prevRoots, prevGrab
	})
	return dir
}

// mlibTestPicture writes a w×h PNG.
func mlibTestPicture(t *testing.T, path string, w, h int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
}

func TestMlibParseEpisodeReadsTheCommonNames(t *testing.T) {
	cases := []struct {
		name            string
		show            string
		season, episode int
		title           string
		ok              bool
	}{
		{"The.Office.S02E05.720p.WEB-DL.x264", "The Office", 2, 5, "", true},
		{"Breaking Bad - S01E01 - Pilot", "Breaking Bad", 1, 1, "Pilot", true},
		{"show_1x02_the_title", "show", 1, 2, "the title", true},
		{"S03E04", "", 3, 4, "", true},
		{"Foo s1e2 Bar", "Foo", 1, 2, "Bar", true},
		{"Doctor.Who.S10E01E02.The.Pilot", "Doctor Who", 10, 1, "The Pilot", true},
		{"Holiday.1920x1080", "", 0, 0, "", false},
		{"Heat.1995.1080p", "", 0, 0, "", false},
	}
	for _, c := range cases {
		show, season, episode, title, ok := mlibParseEpisode(c.name)
		if ok != c.ok || show != c.show || season != c.season || episode != c.episode || title != c.title {
			t.Errorf("mlibParseEpisode(%q) = %q S%d E%d %q %v, want %q S%d E%d %q %v",
				c.name, show, season, episode, title, ok, c.show, c.season, c.episode, c.title, c.ok)
		}
	}
}

func TestMlibMovieTitleCutsTheReleaseTail(t *testing.T) {
	cases := []struct {
		name  string
		title string
		year  int
	}{
		{"Heat.1995.1080p.BluRay.x264", "Heat", 1995},
		{"2001.A.Space.Odyssey.1968", "2001 A Space Odyssey", 1968},
		{"1917 (2019)", "1917", 2019},
		{"Some_Home_Video", "Some Home Video", 0},
		{"Arrival.2016.2160p", "Arrival", 2016},
		{"Charlottes.Web.2006", "Charlottes Web", 2006},
	}
	for _, c := range cases {
		if title, year := mlibMovieTitle(c.name); title != c.title || year != c.year {
			t.Errorf("mlibMovieTitle(%q) = %q, %d; want %q, %d", c.name, title, year, c.title, c.year)
		}
	}
}

func TestMlibSongTitleReadsTheTrackNumber(t *testing.T) {
	cases := []struct {
		name  string
		track int
		title string
	}{
		{"01 - Song", 1, "Song"},
		{"03. Another One", 3, "Another One"},
		{"1-07 Disc Song", 7, "Disc Song"},
		{"09_Under_Score", 9, "Under Score"},
		{"7 Years", 0, "7 Years"},
		{"Plain Song", 0, "Plain Song"},
	}
	for _, c := range cases {
		if track, title := mlibSongTitle(c.name); track != c.track || title != c.title {
			t.Errorf("mlibSongTitle(%q) = %d, %q; want %d, %q", c.name, track, title, c.track, c.title)
		}
	}
}

// Folders fill in what a name leaves out: the show above a Season folder, the
// artist and album of a song, and a disc folder that belongs to its album.
func TestMlibNameItemReadsTheFolders(t *testing.T) {
	base := t.TempDir()
	tv := filepath.Join(base, "TV")
	music := filepath.Join(base, "Music")
	cases := []struct {
		name  string
		path  string
		root  string
		video bool
		want  mlibItem
	}{
		{name: "season folder", path: filepath.Join(tv, "The Office", "Season 2", "S02E05.mkv"), root: tv, video: true,
			want: mlibItem{Show: "The Office", Season: 2, Episode: 5}},
		{name: "show in the name", path: filepath.Join(tv, "Show Name", "Show.Name.S01E03.Pilot.mp4"), root: tv, video: true,
			want: mlibItem{Show: "Show Name", Season: 1, Episode: 3, Title: "Pilot"}},
		{name: "season folder in the root", path: filepath.Join(tv, "Season 1", "S01E01.mkv"), root: tv, video: true,
			want: mlibItem{Show: "Unknown show", Season: 1, Episode: 1}},
		{name: "film", path: filepath.Join(tv, "Heat.1995.mkv"), root: tv, video: true,
			want: mlibItem{Title: "Heat", Year: 1995}},
		{name: "artist and album", path: filepath.Join(music, "Artist", "Album", "01 - Song.mp3"), root: music,
			want: mlibItem{Artist: "Artist", Album: "Album", Track: 1, Title: "Song"}},
		{name: "disc folder", path: filepath.Join(music, "Artist", "Album", "CD1", "02 - Two.flac"), root: music,
			want: mlibItem{Artist: "Artist", Album: "Album", Track: 2, Title: "Two"}},
		{name: "album only", path: filepath.Join(music, "Album Only", "03 - Three.mp3"), root: music,
			want: mlibItem{Album: "Album Only", Track: 3, Title: "Three"}},
		{name: "loose song", path: filepath.Join(music, "Loose.mp3"), root: music,
			want: mlibItem{Album: "Music", Title: "Loose"}},
		{name: "artist in the title", path: filepath.Join(music, "Queen", "Hits", "Queen - Bohemian Rhapsody.mp3"), root: music,
			want: mlibItem{Artist: "Queen", Album: "Hits", Title: "Bohemian Rhapsody"}},
	}
	for _, c := range cases {
		it := mlibItem{Path: c.path, Video: c.video}
		mlibNameItem(&it, c.root)
		w := c.want
		if it.Show != w.Show || it.Season != w.Season || it.Episode != w.Episode || it.Title != w.Title ||
			it.Year != w.Year || it.Artist != w.Artist || it.Album != w.Album || it.Track != w.Track {
			t.Errorf("%s: got show %q S%d E%d title %q year %d artist %q album %q track %d", c.name,
				it.Show, it.Season, it.Episode, it.Title, it.Year, it.Artist, it.Album, it.Track)
		}
	}
}

// The model groups and orders the library: films by title, a show's episodes
// in order, an album's songs by track, what is in progress last played first,
// and the newest arrivals with an album shown once.
func TestMlibBuildModelGroupsTheLibrary(t *testing.T) {
	items := []mlibItem{
		{Path: "/l/Zeta.mkv", Video: true, Title: "Zeta", Added: 100, Duration: 100},
		{Path: "/l/alpha.mkv", Video: true, Title: "alpha", Added: 300, Duration: 100},
		{Path: "/l/tv/o/s2e1.mkv", Video: true, Show: "The Office", Season: 2, Episode: 1, Added: 50},
		{Path: "/l/tv/o/s1e2.mkv", Video: true, Show: "The Office", Season: 1, Episode: 2, Added: 60, Duration: 100},
		{Path: "/l/tv/o/s1e1.mkv", Video: true, Show: "the office", Season: 1, Episode: 1, Added: 70},
		{Path: "/l/m/two.mp3", Title: "Two", Artist: "Artist", Album: "Album", Track: 2, Added: 500},
		{Path: "/l/m/bonus.mp3", Title: "Bonus", Artist: "Artist", Album: "Album", Added: 500},
		{Path: "/l/m/one.mp3", Title: "One", Artist: "artist", Album: "album", Track: 1, Added: 500, Art: "/art/a.jpg"},
	}
	resume := map[string]mlibResume{
		dlPathKey("/l/Zeta.mkv"):      {Position: 30, Played: 1000},
		dlPathKey("/l/tv/o/s1e2.mkv"): {Position: 10, Played: 2000},
		dlPathKey("/l/alpha.mkv"):     {Position: 99, Played: 3000},
	}
	m := mlibBuildModel(items, resume)

	if len(m.movies) != 2 || m.items[m.movies[0]].Title != "alpha" || m.items[m.movies[1]].Title != "Zeta" {
		t.Fatalf("movies = %v, want alpha then Zeta", m.movies)
	}
	if len(m.shows) != 1 || m.shows[0].seasons != 2 || len(m.shows[0].items) != 3 {
		t.Fatalf("shows = %+v, want one show with 3 episodes in 2 seasons", m.shows)
	}
	var order []int
	for _, i := range m.shows[0].items {
		order = append(order, m.items[i].Season*10+m.items[i].Episode)
	}
	if order[0] != 11 || order[1] != 12 || order[2] != 21 {
		t.Fatalf("episode order = %v, want S1E1 S1E2 S2E1", order)
	}
	if len(m.albums) != 1 || m.albums[0].art != "/art/a.jpg" {
		t.Fatalf("albums = %+v, want one album with the cover of its first song", m.albums)
	}
	var songs []string
	for _, i := range m.albums[0].items {
		songs = append(songs, m.items[i].Title)
	}
	if len(songs) != 3 || songs[0] != "One" || songs[1] != "Two" || songs[2] != "Bonus" {
		t.Fatalf("songs = %v, want One, Two, then the unnumbered Bonus", songs)
	}
	if len(m.cont) != 2 || m.items[m.cont[0]].Path != "/l/tv/o/s1e2.mkv" || m.items[m.cont[1]].Path != "/l/Zeta.mkv" {
		t.Fatalf("continue = %v, want S1E2 (played last) then Zeta, and not the finished alpha", m.cont)
	}
	if len(m.recent) != 6 || m.recent[0].album != 0 || m.recent[1].item < 0 || m.items[m.recent[1].item].Title != "alpha" {
		t.Fatalf("recent = %+v, want the album once, then alpha", m.recent)
	}
	if i, ok := m.byPath[dlPathKey("/l/Zeta.mkv")]; !ok || m.items[i].Position != 30 {
		t.Fatal("the resume position did not reach the model")
	}
}

func TestMlibNextEpisodePicksWhereToGoOn(t *testing.T) {
	items := []mlibItem{
		{Path: "/s/1.mkv", Video: true, Show: "S", Season: 1, Episode: 1, Duration: 100},
		{Path: "/s/2.mkv", Video: true, Show: "S", Season: 1, Episode: 2, Duration: 100},
		{Path: "/s/3.mkv", Video: true, Show: "S", Season: 1, Episode: 3, Duration: 100},
	}
	cases := []struct {
		name   string
		resume map[string]mlibResume
		want   string
	}{
		{"nothing watched", nil, "/s/1.mkv"},
		{"one in progress", map[string]mlibResume{dlPathKey("/s/2.mkv"): {Position: 40, Played: 10}}, "/s/2.mkv"},
		{"after the last finished", map[string]mlibResume{
			dlPathKey("/s/1.mkv"): {Position: 0, Played: 10},
			dlPathKey("/s/2.mkv"): {Position: 0, Played: 20},
		}, "/s/3.mkv"},
		{"the last one finished starts over", map[string]mlibResume{dlPathKey("/s/3.mkv"): {Position: 0, Played: 5}}, "/s/1.mkv"},
	}
	for _, c := range cases {
		m := mlibBuildModel(append([]mlibItem(nil), items...), c.resume)
		next := mlibNextEpisode(m, m.shows[0])
		if next < 0 || m.items[next].Path != c.want {
			t.Errorf("%s: next episode %d, want %s", c.name, next, c.want)
		}
	}
}

// A rescan keeps what the slow pass learnt about an unchanged file, starts a
// changed one afresh (keeping when it arrived) and forgets a deleted one.
func TestMlibReconcileKeepsWhatTheSlowPassLearnt(t *testing.T) {
	root := t.TempDir()
	p1 := filepath.Join(root, "Kept.mkv")
	p2 := filepath.Join(root, "Changed.mkv")
	p3 := filepath.Join(root, "Gone.mkv")
	p4 := filepath.Join(root, "New.mp3")
	old := []mlibItem{
		{Path: p1, Size: 10, Mod: 100, Video: true, Title: "Kept title", Probed: true, ArtDone: true, Art: "x.jpg", Added: 5},
		{Path: p2, Size: 10, Mod: 100, Video: true, Title: "Changed", Probed: true, Added: 6},
		{Path: p3, Size: 10, Mod: 100, Video: true, Title: "Gone"},
	}
	files := []mlibFile{
		{path: p1, root: root, size: 10, mod: 100, video: true},
		{path: p2, root: root, size: 20, mod: 200, video: true},
		{path: p4, root: root, size: 1, mod: int64(3 * time.Second)},
	}
	got := mlibReconcile(old, files)
	if len(got) != 3 {
		t.Fatalf("reconciled %d entries, want 3: %+v", len(got), got)
	}
	if !got[0].Probed || got[0].Art != "x.jpg" || got[0].Title != "Kept title" {
		t.Errorf("unchanged file = %+v, want its tags and poster kept", got[0])
	}
	if got[1].Probed || got[1].Size != 20 || got[1].Added != 6 {
		t.Errorf("changed file = %+v, want a fresh entry that keeps when it arrived", got[1])
	}
	if got[2].Video || got[2].Title != "New" || got[2].Added != 3 {
		t.Errorf("new file = %+v, want a song titled from its name, added at its file time", got[2])
	}
}

// A second walk lists only the folder whose time moved; an unchanged folder's
// files come from the index (here a row only the index knows).
func TestMlibWalkSkipsUnchangedFolders(t *testing.T) {
	root := t.TempDir()
	dlWriteTestFile(t, filepath.Join(root, "Movies", "A.mkv"), 3)
	dlWriteTestFile(t, filepath.Join(root, "Shows", "S01E01.mkv"), 3)
	dlWriteTestFile(t, filepath.Join(root, "notes.txt"), 3)
	dlWriteTestFile(t, filepath.Join(root, ".hidden", "B.mkv"), 3)

	dirs := map[string]mlibDirState{}
	files, done := mlibWalkRoot(context.Background(), root, nil, nil, dirs, 100)
	if !done || len(files) != 2 {
		t.Fatalf("first walk found %d files (done=%v), want the two videos", len(files), done)
	}

	knownFiles := mlibFilesByDir(mlibReconcile(nil, files))
	shows := dlPathKey(filepath.Join(root, "Shows"))
	knownFiles[shows] = append(knownFiles[shows], mlibItem{Path: filepath.Join(root, "Shows", "S01E02.mkv"), Size: 9, Mod: 1, Video: true})
	dlWriteTestFile(t, filepath.Join(root, "Movies", "B.mp4"), 3)
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(root, "Movies"), later, later); err != nil {
		t.Fatal(err)
	}

	again, done := mlibWalkRoot(context.Background(), root, dirs, knownFiles, map[string]mlibDirState{}, 100)
	if !done {
		t.Fatal("the second walk did not finish")
	}
	names := map[string]bool{}
	for _, f := range again {
		names[filepath.Base(f.path)] = true
	}
	for _, want := range []string{"A.mkv", "B.mp4", "S01E01.mkv", "S01E02.mkv"} {
		if !names[want] {
			t.Errorf("second walk is missing %s: %v", want, names)
		}
	}
	if len(again) != 4 {
		t.Errorf("second walk found %d files, want 4", len(again))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, done := mlibWalkRoot(ctx, root, nil, nil, map[string]mlibDirState{}, 100); done {
		t.Error("a cancelled walk reported that it finished")
	}
}

// The index keeps the entries, the folders and where each video was left, and
// the next start reads them back into the model at once.
func TestMlibIndexRoundTrip(t *testing.T) {
	mlibTestState(t)
	path := filepath.Join(string(filepath.Separator)+"m", "a.mkv")
	mlibWriteIndex(mlibIndexFile{
		Version: mlibIndexVersion,
		Items:   []mlibItem{{Path: path, Size: 5, Mod: 7, Video: true, Title: "A", Duration: 60, Probed: true, ArtDone: true}},
		Dirs:    map[string]mlibDirState{"/m": {Mod: 9, Subdirs: []string{"sub"}}},
		Resume:  map[string]mlibResume{dlPathKey(path): {Position: 12, Duration: 60, Played: 1700000000}},
	})
	mlibLoad()
	m := mlibCurrentModel()
	if m == nil || len(m.items) != 1 {
		t.Fatalf("model after load = %+v", m)
	}
	it := m.items[0]
	if it.Path != path || it.Title != "A" || it.Duration != 60 || !it.Probed || !it.ArtDone || it.Position != 12 || it.Played != 1700000000 {
		t.Fatalf("entry after load = %+v", it)
	}
	if got := mlibResumeAt(path); got != 12 {
		t.Fatalf("resume point = %v, want 12", got)
	}
	mlibState.mu.Lock()
	dir := mlibState.dirs["/m"]
	mlibState.mu.Unlock()
	if dir.Mod != 9 || len(dir.Subdirs) != 1 || dir.Subdirs[0] != "sub" {
		t.Fatalf("folder after load = %+v", dir)
	}
}

// A whole scan of a folder: films, a show and an album, with the album's cover
// scaled into the poster cache and the index written.
func TestMlibScanBuildsTheLibrary(t *testing.T) {
	root := t.TempDir()
	dir := mlibTestState(t, root)
	dlWriteTestFile(t, filepath.Join(root, "Movies", "Heat.1995.mkv"), 3)
	dlWriteTestFile(t, filepath.Join(root, "TV", "The Office", "Season 1", "The.Office.S01E02.mkv"), 3)
	dlWriteTestFile(t, filepath.Join(root, "TV", "The Office", "Season 1", "The.Office.S01E01.mkv"), 3)
	dlWriteTestFile(t, filepath.Join(root, "Music", "Artist", "Album", "02 - Two.mp3"), 3)
	dlWriteTestFile(t, filepath.Join(root, "Music", "Artist", "Album", "01 - One.mp3"), 3)
	mlibTestPicture(t, filepath.Join(root, "Music", "Artist", "Album", "Cover.PNG"), 600, 600)

	mlibStartScan(&Config{Variables: Variables{Custom: map[string]interface{}{}}}, true)
	mlibWaitIdle(t)

	m := mlibCurrentModel()
	if m == nil {
		t.Fatal("the scan published no model")
	}
	if len(m.movies) != 1 || m.items[m.movies[0]].Title != "Heat" || m.items[m.movies[0]].Year != 1995 {
		t.Fatalf("movies = %+v", m.movies)
	}
	if len(m.shows) != 1 || m.shows[0].title != "The Office" || len(m.shows[0].items) != 2 ||
		m.items[m.shows[0].items[0]].Episode != 1 {
		t.Fatalf("shows = %+v, want The Office with its two episodes in order", m.shows)
	}
	if len(m.albums) != 1 || m.albums[0].title != "Album" || m.albums[0].sub != "Artist" || len(m.albums[0].items) != 2 {
		t.Fatalf("albums = %+v, want Artist's Album with two songs", m.albums)
	}
	art := m.albums[0].art
	if art == "" || !isPathWithin(art, mlibArtDir()) {
		t.Fatalf("album poster = %q, want a file in %s", art, mlibArtDir())
	}
	f, err := os.Open(art)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(f)
	f.Close()
	if err != nil || format != "jpeg" || cfg.Width > mlibArtWidth || cfg.Height > mlibArtMaxH {
		t.Fatalf("poster is %s %dx%d (%v), want a JPEG within %dx%d", format, cfg.Width, cfg.Height, err, mlibArtWidth, mlibArtMaxH)
	}
	if !mlibFileExists(filepath.Join(dir, "index.json")) {
		t.Fatal("the scan saved no index")
	}
	for _, it := range m.items {
		if !it.ArtDone {
			t.Errorf("%s was not given its poster pass", filepath.Base(it.Path))
		}
	}
}

func TestMlibShrinkKeepsTheShape(t *testing.T) {
	cases := []struct {
		w, h, wantW, wantH int
	}{
		{1000, 500, 320, 160},
		{600, 1200, 240, 480},
		{200, 100, 200, 100},
	}
	for _, c := range cases {
		got := mlibShrink(image.NewRGBA(image.Rect(0, 0, c.w, c.h)), mlibArtWidth, mlibArtMaxH).Bounds()
		if got.Dx() != c.wantW || got.Dy() != c.wantH {
			t.Errorf("%dx%d shrank to %dx%d, want %dx%d", c.w, c.h, got.Dx(), got.Dy(), c.wantW, c.wantH)
		}
	}
}

func TestMlibRootsAreCleaned(t *testing.T) {
	base := t.TempDir()
	videos := filepath.Join(base, "Videos")
	inside := filepath.Join(videos, "Kids")
	music := filepath.Join(base, "Music")
	got := mlibCleanRoots([]string{videos, " ", inside, music, videos + string(filepath.Separator)})
	if len(got) != 2 || got[0] != videos || got[1] != music {
		t.Fatalf("roots = %q, want Videos and Music once each, without the folder inside Videos", got)
	}
	if got := mlibConfiguredRoots("/a; /b\n/c ;"); len(got) != 3 || got[0] != "/a" || got[2] != "/c" {
		t.Fatalf("roots from a string = %q", got)
	}
	if got := mlibConfiguredRoots([]interface{}{"/a", 3, " ", "/b"}); len(got) != 2 || got[1] != "/b" {
		t.Fatalf("roots from a list = %q", got)
	}
	if got := mlibConfiguredRoots(nil); len(got) != 0 {
		t.Fatalf("roots from nothing = %q", got)
	}
}

func TestMlibParseProbeReadsTagsInAnyCase(t *testing.T) {
	out := "duration=245.120000\nTAG:ARTIST=Band\nTAG:album_artist=Various\nTAG:ALBUM=Hits\nTAG:title=Song\nTAG:track=3/12\nTAG:DATE=2019-05-01\n"
	got := mlibParseProbe(out)
	if got.duration != 245.12 || got.artist != "Band" || got.albumArtist != "Various" || got.album != "Hits" ||
		got.title != "Song" || got.track != 3 || got.year != 2019 {
		t.Fatalf("parsed %+v", got)
	}
	song := mlibItem{Title: "file name", Artist: "Folder", Album: "Folder album"}
	mlibApplyTags(&song, got)
	if song.Title != "Song" || song.Artist != "Various" || song.Album != "Hits" || song.Track != 3 || song.Year != 2019 || song.Duration != 245.12 {
		t.Fatalf("song after its tags = %+v", song)
	}
	film := mlibItem{Video: true, Title: "Heat"}
	mlibApplyTags(&film, mlibTags{duration: 600, title: "Heat.1995.1080p-GROUP"})
	if film.Title != "Heat" || film.Duration != 600 {
		t.Fatalf("film after its tags = %+v, want only the length taken", film)
	}
}
