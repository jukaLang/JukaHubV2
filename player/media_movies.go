package main

// media_movies.go — Media › Movies & TV: films and shows to stream from the
// places a handheld can reach legitimately — the user's own Jellyfin server,
// the media servers on the home network (DLNA, dlna.go) and the public-domain
// classics of the Internet Archive (freefilms.go).
//
// Testers asked to install an Android streaming app on the Brick. An APK like
// that cannot start here (the APK Store says why), and sources that serve
// films from torrents or unlicensed hosts are not wired in; this tab is the
// route that works. It is laid out like Library: rows of cards — Continue
// watching, Recently added, Movies and TV shows from Jellyfin (set up from the
// phone or with Quick Connect in the music player's Servers page), the
// servers on the network, four rows of free classics — and a card opens its
// list one level deeper (a show's seasons and episodes, a server's folders, a
// whole collection); B goes back up.
//
// Every request runs off the main thread and lands with tbxPost; the model
// (mtv) is only touched on the main thread, and its gen tells the screen to
// rebuild the cards. Jellyfin and the network's servers only need the home
// network; the free films need the internet.

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// mtvCardKind is what a Movies & TV card stands for.
type mtvCardKind uint8

const (
	mtvCardNone mtvCardKind = iota
	// The three kinds that play.
	mtvCardJfVideo
	mtvCardDlnaVideo
	mtvCardFilm
	// The kinds that open a list one level deeper.
	mtvCardJfShow
	mtvCardJfSeason
	mtvCardJfAllMovies
	mtvCardJfAllShows
	mtvCardDlnaServer
	mtvCardDlnaFolder
	mtvCardFilmsAll
	// mtvCardSetup sets Jellyfin up from the phone; mtvCardRetry asks a
	// source again (its key names the source).
	mtvCardSetup
	mtvCardRetry
)

// mtvCardPlays reports a card that plays a video.
func mtvCardPlays(k mtvCardKind) bool {
	return k == mtvCardJfVideo || k == mtvCardDlnaVideo || k == mtvCardFilm
}

// mtvCardOpens reports a card that opens a list.
func mtvCardOpens(k mtvCardKind) bool {
	return k >= mtvCardJfShow && k <= mtvCardFilmsAll
}

const (
	// mtvFresh is how long an answer is shown before the tab asks again.
	mtvFresh = 5 * time.Minute
	// mtvListsMax bounds the remembered lists (folders, seasons).
	mtvListsMax = 40
	// mtvKeySep joins a server's UDN and a folder's id in a card's key.
	mtvKeySep = "\x1f"
)

// mtvLoad is one source's or list's loading state.
type mtvLoad struct {
	loading bool
	loaded  bool
	at      time.Time
	err     string
}

// stale reports an answer worth asking for again.
func (l mtvLoad) stale() bool {
	return !l.loading && (!l.loaded || l.err != "" || time.Since(l.at) > mtvFresh)
}

// mtvList is one open list: its cards and, for a collection, how far it has
// been paged.
type mtvList struct {
	load  mtvLoad
	cards []mediaCard
	page  int
	total int
}

type mtvJellyfin struct {
	load   mtvLoad
	server string
	cont   []mediaCard
	recent []mediaCard
	movies []mediaCard
	shows  []mediaCard
}

type mtvNetwork struct {
	load    mtvLoad
	servers []dlnaServer
}

// mtv is the tab's model. Main thread only.
var mtv struct {
	gen     uint64
	jf      mtvJellyfin
	network mtvNetwork
	films   []mtvList
	lists   map[string]*mtvList
	// opening is the free film whose copy is being looked up, so a second
	// press does not start it twice.
	opening string
}

// mtvDrill is one level of the drill-down, with where the ring and the scroll
// were when a deeper level opened over it.
type mtvDrill struct {
	kind   mtvCardKind
	key    string
	title  string
	sub    string
	index  int
	scroll float64
}

// mtvNav is the open drill-down, innermost last; empty shows the rows.
var mtvNav struct {
	stack      []mtvDrill
	rowsScroll float64
}

// mtvChanged tells the screen to rebuild.
func mtvChanged() {
	mtv.gen++
	mediaHub.builtAt = 0
}

// ── Loading ─────────────────────────────────────────────────────────────────

// mtvEnterTab runs when Movies & TV is shown: every source that has not
// answered, failed, or answered a while ago is asked again, and so is the open
// list.
func mtvEnterTab() {
	if len(mtv.films) != len(freeFilmShelves) {
		mtv.films = make([]mtvList, len(freeFilmShelves))
	}
	if musicSrcReady(musicSrcJellyfin) && mtv.jf.load.stale() {
		mtvLoadJellyfin(false)
	}
	if mtv.network.load.stale() {
		mtvLoadNetwork()
	}
	if IsOnline() {
		for i := range mtv.films {
			if mtv.films[i].load.stale() {
				mtvLoadFilmsRow(i)
			}
		}
	}
	if n := len(mtvNav.stack); n > 0 {
		mtvLoadList(mtvNav.stack[n-1], false)
	}
}

// mtvLoadJellyfin asks the signed-in Jellyfin for the rows; continueOnly
// asks for Continue watching alone (after a video stopped), leaving the rest
// and their age as they are.
func mtvLoadJellyfin(continueOnly bool) {
	if mtv.jf.load.loading {
		return
	}
	mtv.jf.load.loading = true
	mtvChanged()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		res, err := mtvFetchJellyfin(ctx, continueOnly)
		tbxPost(func() {
			if continueOnly {
				mtv.jf.load.loading = false
				if err == nil {
					mtv.jf.cont = res.cont
				}
				mtvChanged()
				return
			}
			mtv.jf.load = mtvLoad{loaded: true, at: time.Now()}
			if err != nil {
				mtv.jf.load.err = firstLine(err.Error())
			} else {
				mtv.jf.server, mtv.jf.cont, mtv.jf.recent = res.server, res.cont, res.recent
				mtv.jf.movies, mtv.jf.shows = res.movies, res.shows
			}
			mtvChanged()
		})
	}()
}

// mtvLoadNetwork searches the home network for media servers.
func mtvLoadNetwork() {
	if mtv.network.load.loading {
		return
	}
	mtv.network.load.loading = true
	mtvChanged()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		servers := dlnaDiscover(ctx)
		tbxPost(func() {
			mtv.network.load = mtvLoad{loaded: true, at: time.Now()}
			mtv.network.servers = servers
			mtvChanged()
		})
	}()
}

// mtvLoadFilmsRow asks the Archive for the first films of one collection.
func mtvLoadFilmsRow(i int) {
	if i < 0 || i >= len(mtv.films) || mtv.films[i].load.loading {
		return
	}
	mtv.films[i].load.loading = true
	mtvChanged()
	shelf := freeFilmShelves[i]
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		films, total, err := freeFilmsSearch(ctx, shelf.ID, 1, mediaRowCards)
		cards := mtvFilmCards(films)
		tbxPost(func() {
			if i >= len(mtv.films) {
				return
			}
			l := &mtv.films[i]
			l.load = mtvLoad{loaded: true, at: time.Now()}
			if err != nil {
				l.load.err = firstLine(err.Error())
			} else {
				l.cards, l.total = cards, total
			}
			mtvChanged()
		})
	}()
}

// mtvRetry asks a source again: "jf", "net", "films:<row>", or "list" (the
// open list).
func mtvRetry(key string) {
	switch {
	case key == "jf":
		mtvLoadJellyfin(false)
	case key == "net":
		mtvLoadNetwork()
	case strings.HasPrefix(key, "films:"):
		if i, err := strconv.Atoi(strings.TrimPrefix(key, "films:")); err == nil {
			mtvLoadFilmsRow(i)
		}
	case key == "list":
		if n := len(mtvNav.stack); n > 0 {
			mtvLoadList(mtvNav.stack[n-1], true)
		}
	}
}

// mtvAfterPlayback runs when the player closes over this tab: where a
// Jellyfin video stopped moves Continue watching and the cards' progress, so
// those are asked for again once the server has heard about it.
func mtvAfterPlayback() {
	if mediaHub.tab != mediaTabMovies || !musicSrcReady(musicSrcJellyfin) {
		return
	}
	go func() {
		time.Sleep(2 * time.Second)
		tbxPost(func() {
			mtvLoadJellyfin(true)
			if n := len(mtvNav.stack); n > 0 {
				switch mtvNav.stack[n-1].kind {
				case mtvCardJfShow, mtvCardJfSeason:
					mtvLoadList(mtvNav.stack[n-1], true)
				}
			}
		})
	}()
}

// ── Jellyfin ────────────────────────────────────────────────────────────────

type mtvJfResult struct {
	server string
	cont   []mediaCard
	recent []mediaCard
	movies []mediaCard
	shows  []mediaCard
}

// mtvFetchJellyfin reads the rows' lists (Continue watching alone with
// continueOnly). A list that fails is left empty; the error is only returned
// when nothing came back at all.
func mtvFetchJellyfin(ctx context.Context, continueOnly bool) (mtvJfResult, error) {
	c, err := jfNewClient()
	if err != nil {
		return mtvJfResult{}, err
	}
	res := mtvJfResult{server: dlFirstNonEmpty(c.conf.ServerName, musicHostOf(c.conf.URL))}
	var firstErr error
	got := 0
	ask := func(v url.Values) []mediaCard {
		if firstErr != nil && got == 0 {
			// The first list failed outright (the server is off, or signed
			// this handheld out): the others would only wait for the same.
			return nil
		}
		v.Set("Recursive", "true")
		list, err := c.items(ctx, v)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return nil
		}
		got++
		cards := make([]mediaCard, 0, len(list))
		for _, it := range list {
			cards = append(cards, mtvJfCard(c, it, res.server, false))
		}
		return cards
	}
	res.cont = ask(url.Values{"IncludeItemTypes": {"Movie,Episode"}, "Filters": {"IsResumable"},
		"SortBy": {"DatePlayed"}, "SortOrder": {"Descending"}, "Limit": {"24"}, "Fields": {"MediaSources"}})
	if continueOnly {
		return res, firstErr
	}
	res.recent = ask(url.Values{"IncludeItemTypes": {"Movie,Series"}, "SortBy": {"DateCreated"},
		"SortOrder": {"Descending"}, "Limit": {"24"}, "Fields": {"MediaSources,ChildCount"}})
	res.movies = ask(url.Values{"IncludeItemTypes": {"Movie"}, "SortBy": {"SortName"}, "Limit": {"1000"},
		"Fields": {"MediaSources"}})
	res.shows = ask(url.Values{"IncludeItemTypes": {"Series"}, "SortBy": {"SortName"}, "Limit": {"1000"},
		"Fields": {"ChildCount"}})
	if got == 0 && firstErr != nil {
		return res, firstErr
	}
	return res, nil
}

// mtvJfCard is one Jellyfin item's card: a film or an episode plays, a show or
// a season opens. inList is true inside a show, where the card need not
// repeat the show's name.
func mtvJfCard(c *jfClient, it jfItem, server string, inList bool) mediaCard {
	card := mediaCard{title: dlnaCleanText(it.Name), mtvKey: it.ID, glyph: "video", detail: server}
	card.art = mtvJfArt(c, it)
	card.video = VideoInfo{Title: card.title, Thumbnail: card.art[0]}
	switch it.Type {
	case "Series":
		card.mtvKind = mtvCardJfShow
		card.meta = "TV show"
		if it.ChildCount > 0 {
			card.meta = pluralizeCount(it.ChildCount, "season", "seasons")
		}
		if it.ProductionYear > 0 {
			card.meta += " · " + strconv.Itoa(it.ProductionYear)
		}
		return card
	case "Season":
		card.mtvKind = mtvCardJfSeason
		card.mtvKey = it.ID + mtvKeySep + it.SeriesID
		card.meta = dlFirstNonEmpty(it.SeriesName, "Season")
		if it.ChildCount > 0 {
			card.meta = pluralizeCount(it.ChildCount, "episode", "episodes")
		}
		return card
	}
	row := c.videoItem(it)
	card.mtvKind = mtvCardJfVideo
	card.title = dlnaCleanText(row.Title)
	card.video = VideoInfo{Title: card.title, Uploader: server, Duration: row.Duration, URL: row.Path, Thumbnail: card.art[0]}
	if row.Duration > 0 {
		card.duration = formatDuration(row.Duration)
	}
	if !row.Played {
		card.progress = mediaProgressFraction(row.Position, row.Duration)
	}
	switch {
	case it.Type == "Episode" && inList:
		card.meta = "Episode"
		if it.IndexNumber > 0 {
			card.meta = "Episode " + strconv.Itoa(it.IndexNumber)
		}
	case it.Type == "Episode":
		card.meta = dlFirstNonEmpty(it.SeriesName, "Episode")
		if it.ParentIndexNumber > 0 && it.IndexNumber > 0 {
			card.meta += " · S" + strconv.Itoa(it.ParentIndexNumber) + " E" + strconv.Itoa(it.IndexNumber)
		}
	case it.ProductionYear > 0:
		card.meta = "Movie · " + strconv.Itoa(it.ProductionYear)
	default:
		card.meta = "Movie"
	}
	if row.Played {
		card.meta += " · Watched"
	}
	return card
}

// mtvJfArt is a card's pictures: a wide one first, because the card is 16:9
// (the item's Thumb or Backdrop, an episode's own still, the show's), then the
// poster.
func mtvJfArt(c *jfClient, it jfItem) [2]string {
	wide := ""
	switch {
	case it.ImageTags["Thumb"] != "":
		wide = mtvJfImageURL(c.srv, it.ID, "Thumb", it.ImageTags["Thumb"])
	case len(it.BackdropImageTags) > 0:
		wide = mtvJfImageURL(c.srv, it.ID, "Backdrop", it.BackdropImageTags[0])
	case it.Type == "Episode" && it.ImageTags["Primary"] != "":
		return [2]string{mtvJfImageURL(c.srv, it.ID, "Primary", it.ImageTags["Primary"]), ""}
	case it.ParentThumbItemID != "" && it.ParentThumbImageTag != "":
		wide = mtvJfImageURL(c.srv, it.ParentThumbItemID, "Thumb", it.ParentThumbImageTag)
	case it.ParentBackdropItemID != "" && len(it.ParentBackdropImageTags) > 0:
		wide = mtvJfImageURL(c.srv, it.ParentBackdropItemID, "Backdrop", it.ParentBackdropImageTags[0])
	}
	poster := ""
	if tag := it.ImageTags["Primary"]; tag != "" {
		poster = mtvJfImageURL(c.srv, it.ID, "Primary", tag)
	}
	if wide == "" {
		return [2]string{poster, ""}
	}
	return [2]string{wide, poster}
}

// mtvJfImageURL is a Jellyfin picture through the loopback proxy. The image
// tag is part of the address, so a picture changed on the server is a new
// address and the thumbnail cache never shows the old one.
func mtvJfImageURL(srv, id, kind, tag string) string {
	return musicProxyBase() + "/jfi/" + srv + "/" + url.PathEscape(id) + "/" + kind + "/" + mtvSafeTag(tag) + ".jpg"
}

// mtvSafeTag keeps an image tag to letters and digits. Pure.
func mtvSafeTag(tag string) string {
	var b strings.Builder
	for _, r := range tag {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
		if b.Len() >= 64 {
			break
		}
	}
	if b.Len() == 0 {
		return "-"
	}
	return b.String()
}

// mtvJfImageProxy serves one Jellyfin picture, 480 pixels wide (route jfi:
// <server>/<item>/<kind>/<tag>.jpg).
func mtvJfImageProxy(w http.ResponseWriter, r *http.Request, srv, rest string) {
	c, err := jfNewClient()
	if err != nil || c.srv != srv {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}
	id, err := url.PathUnescape(parts[0])
	if err != nil || id == "" || strings.ContainsAny(id, "/\\?#") {
		http.NotFound(w, r)
		return
	}
	kind := parts[1]
	switch kind {
	case "Primary", "Thumb", "Backdrop":
	default:
		http.NotFound(w, r)
		return
	}
	q := url.Values{"maxWidth": {"480"}, "quality": {"80"}}
	if tag := strings.TrimSuffix(parts[2], ".jpg"); tag != "" && tag != "-" {
		q.Set("tag", mtvSafeTag(tag))
	}
	target := musicJoinURL(c.base, "/Items/"+url.PathEscape(id)+"/Images/"+kind, q)
	auth := jfAuthHeader(c.token)
	musicProxyPass(w, r, func(ctx context.Context, _ string) (*http.Response, error) {
		return musicGet(ctx, c.base.Scheme, target, auth, "")
	})
}

// mtvFetchJfList reads a show (its seasons — or, with one season, that
// season's episodes) or a season (its episodes).
func mtvFetchJfList(ctx context.Context, d mtvDrill) ([]mediaCard, error) {
	c, err := jfNewClient()
	if err != nil {
		return nil, err
	}
	server := dlFirstNonEmpty(c.conf.ServerName, musicHostOf(c.conf.URL))
	episodes := func(seasonID, seriesID string) ([]mediaCard, error) {
		var r jfItems
		err := c.get(ctx, "/Shows/"+url.PathEscape(seriesID)+"/Episodes", url.Values{"userId": {c.user},
			"SeasonId": {seasonID}, "Fields": {"MediaSources"}}, &r)
		cards := make([]mediaCard, 0, len(r.Items))
		for _, it := range r.Items {
			cards = append(cards, mtvJfCard(c, it, server, true))
		}
		return cards, err
	}
	if d.kind == mtvCardJfSeason {
		season, series, _ := strings.Cut(d.key, mtvKeySep)
		return episodes(season, series)
	}
	var r jfItems
	if err := c.get(ctx, "/Shows/"+url.PathEscape(d.key)+"/Seasons", url.Values{"userId": {c.user},
		"Fields": {"ChildCount"}}, &r); err != nil {
		return nil, err
	}
	if len(r.Items) == 1 {
		return episodes(r.Items[0].ID, d.key)
	}
	cards := make([]mediaCard, 0, len(r.Items))
	for _, it := range r.Items {
		card := mtvJfCard(c, it, server, true)
		if card.mtvKind == mtvCardJfSeason && it.SeriesID == "" {
			card.mtvKey = it.ID + mtvKeySep + d.key
		}
		cards = append(cards, card)
	}
	return cards, nil
}

// ── Network servers and free films ──────────────────────────────────────────

// mtvServerCard is a media server's card: it opens the server's top folder.
func mtvServerCard(s dlnaServer) mediaCard {
	c := mediaCard{title: s.Name, meta: dlFirstNonEmpty(s.Model, "Media server"), mtvKind: mtvCardDlnaServer,
		mtvKey: s.UDN, glyph: "server", artContain: true, detail: musicHostOf(s.Location)}
	if s.IconURL != "" {
		c.art[0] = s.IconURL
	}
	c.video = VideoInfo{Title: s.Name}
	return c
}

// mtvDlnaCard is a folder's or a video's card from a server's list.
func mtvDlnaCard(s dlnaServer, e dlnaEntry) mediaCard {
	if e.Folder {
		c := mediaCard{title: e.Title, meta: "Folder", mtvKind: mtvCardDlnaFolder, mtvKey: s.UDN + mtvKeySep + e.ID, glyph: "folder"}
		if e.Count >= 0 {
			c.meta = pluralizeCount(e.Count, "item", "items")
		}
		c.video = VideoInfo{Title: e.Title}
		return c
	}
	c := mediaCard{title: e.Title, meta: s.Name, mtvKind: mtvCardDlnaVideo, mtvKey: e.URL, glyph: "video", detail: s.Name}
	if e.Year > 0 {
		c.meta = strconv.Itoa(e.Year) + " · " + s.Name
	}
	if e.Size > 0 {
		c.detail = humanBytes(e.Size) + " · " + s.Name
	}
	c.art[0] = e.Art
	c.video = VideoInfo{Title: e.Title, Uploader: s.Name, Duration: e.Duration, URL: e.URL, Thumbnail: e.Art}
	if e.Duration > 0 {
		c.duration = formatDuration(e.Duration)
	}
	return c
}

// mtvServerByUDN finds a server the last search found.
func mtvServerByUDN(udn string) (dlnaServer, bool) {
	for _, s := range mtv.network.servers {
		if s.UDN == udn {
			return s, true
		}
	}
	return dlnaServer{}, false
}

// mtvFilmCards are a collection's cards.
func mtvFilmCards(films []freeFilm) []mediaCard {
	cards := make([]mediaCard, 0, len(films))
	for _, f := range films {
		c := mediaCard{title: f.Title, meta: "Public domain", mtvKind: mtvCardFilm, mtvKey: f.ID, glyph: "video",
			detail: "Internet Archive"}
		if f.Year > 0 {
			c.meta = strconv.Itoa(f.Year) + " · Public domain"
		}
		c.art[0] = freeFilmsArtURL(f.ID)
		c.video = VideoInfo{Title: f.Title, Uploader: "Internet Archive", Duration: f.Duration,
			URL: freeFilmsVideoURL(f.ID), Thumbnail: c.art[0]}
		if f.Duration > 0 {
			c.duration = formatDuration(f.Duration)
		}
		cards = append(cards, c)
	}
	return cards
}

// mtvWithProgress marks where the player left the videos of a network server
// or the Archive (Jellyfin's cards carry the server's own progress).
func mtvWithProgress(cards []mediaCard) []mediaCard {
	out := make([]mediaCard, len(cards))
	copy(out, cards)
	for i := range out {
		c := &out[i]
		if (c.mtvKind == mtvCardDlnaVideo || c.mtvKind == mtvCardFilm) && c.video.Duration > 0 {
			c.progress = mediaProgressFraction(favoriteResumePosition(c.video.URL), c.video.Duration)
		}
	}
	return out
}

// mediaLANStreamHost reports a plain-http address at a home-network IP that
// is not this device: a media server, a NAS or a TV tuner, which serves the
// file itself (streaming.go treats it as a direct stream). Pure.
func mediaLANStreamHost(lower string) bool {
	if !strings.HasPrefix(lower, "http://") {
		return false
	}
	u, err := url.Parse(lower)
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && !ip.IsLoopback() && webdavLANIP(ip)
}

// ── Lists ───────────────────────────────────────────────────────────────────

// mtvListKey keys a drill level's list.
func mtvListKey(d mtvDrill) string {
	return strconv.Itoa(int(d.kind)) + ":" + d.key
}

// mtvLoadList loads the list a drill level shows, unless a fresh one is
// there (force asks again anyway). Lists that come from the rows' answers
// (all films, all shows) need nothing.
func mtvLoadList(d mtvDrill, force bool) {
	switch d.kind {
	case mtvCardJfAllMovies, mtvCardJfAllShows:
		if force {
			mtvLoadJellyfin(false)
		}
		return
	}
	key := mtvListKey(d)
	if mtv.lists == nil {
		mtv.lists = map[string]*mtvList{}
	}
	l := mtv.lists[key]
	if l != nil && !force && !l.load.stale() {
		return
	}
	if l != nil && l.load.loading {
		return
	}
	if l == nil {
		mtvPruneLists()
		l = &mtvList{}
		mtv.lists[key] = l
	}
	var server dlnaServer
	if d.kind == mtvCardDlnaServer || d.kind == mtvCardDlnaFolder {
		udn, _, _ := strings.Cut(d.key, mtvKeySep)
		s, ok := mtvServerByUDN(udn)
		if !ok {
			l.load = mtvLoad{loaded: true, at: time.Now(), err: "This server is not on the network any more — go back and search again."}
			mtvChanged()
			return
		}
		server = s
	}
	l.load.loading = true
	l.load.err = ""
	mtvChanged()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		var (
			cards []mediaCard
			total int
			err   error
		)
		switch d.kind {
		case mtvCardJfShow, mtvCardJfSeason:
			cards, err = mtvFetchJfList(ctx, d)
		case mtvCardDlnaServer, mtvCardDlnaFolder:
			_, id, found := strings.Cut(d.key, mtvKeySep)
			if !found {
				id = "0"
			}
			var entries []dlnaEntry
			entries, err = dlnaBrowse(ctx, server, id)
			for _, e := range entries {
				cards = append(cards, mtvDlnaCard(server, e))
			}
		case mtvCardFilmsAll:
			if i, convErr := strconv.Atoi(d.key); convErr == nil && i >= 0 && i < len(freeFilmShelves) {
				var films []freeFilm
				films, total, err = freeFilmsSearch(ctx, freeFilmShelves[i].ID, 1, freeFilmsPageSize)
				cards = mtvFilmCards(films)
			}
		}
		tbxPost(func() {
			l := mtv.lists[key]
			if l == nil {
				return
			}
			l.load = mtvLoad{loaded: true, at: time.Now()}
			if err != nil {
				l.load.err = firstLine(err.Error())
			} else {
				l.cards, l.total, l.page = cards, total, 1
			}
			mtvChanged()
		})
	}()
}

// mtvPruneLists forgets the lists no open level shows once there are many.
func mtvPruneLists() {
	if len(mtv.lists) < mtvListsMax {
		return
	}
	keep := map[string]bool{}
	for _, d := range mtvNav.stack {
		keep[mtvListKey(d)] = true
	}
	for k, l := range mtv.lists {
		if !keep[k] && !l.load.loading {
			delete(mtv.lists, k)
		}
	}
}

// mtvLoadMore fetches the next page of an open collection; it reports whether
// a page is on its way.
func mtvLoadMore(quiet bool) bool {
	n := len(mtvNav.stack)
	if n == 0 || mtvNav.stack[n-1].kind != mtvCardFilmsAll {
		return false
	}
	d := mtvNav.stack[n-1]
	key := mtvListKey(d)
	l := mtv.lists[key]
	if l == nil || !l.load.loaded || l.load.err != "" {
		return false
	}
	if l.load.loading {
		return true
	}
	if len(l.cards) >= l.total || len(l.cards) >= freeFilmsListMax {
		if !quiet {
			ShowToast("That is the whole collection", ToastKindInfo)
		}
		return false
	}
	if !IsOnline() {
		if !quiet {
			ShowToast(offlinePlainNotice, ToastKindWarning)
		}
		return false
	}
	i, err := strconv.Atoi(d.key)
	if err != nil || i < 0 || i >= len(freeFilmShelves) {
		return false
	}
	page := l.page + 1
	l.load.loading = true
	mtvChanged()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		films, total, err := freeFilmsSearch(ctx, freeFilmShelves[i].ID, page, freeFilmsPageSize)
		cards := mtvFilmCards(films)
		tbxPost(func() {
			l := mtv.lists[key]
			if l == nil {
				return
			}
			l.load.loading = false
			if err != nil {
				if !quiet {
					ShowToast(firstLine(err.Error()), ToastKindWarning)
				}
				mtvChanged()
				return
			}
			seen := make(map[string]bool, len(l.cards))
			for _, c := range l.cards {
				seen[c.mtvKey] = true
			}
			for _, c := range cards {
				if !seen[c.mtvKey] {
					l.cards = append(l.cards, c)
				}
			}
			l.page, l.total = page, total
			if len(cards) == 0 {
				// The collection ended sooner than it said.
				l.total = len(l.cards)
			}
			mtvChanged()
		})
	}()
	return true
}

// mtvPageLoading reports a page of the open collection on its way.
func mtvPageLoading() bool {
	n := len(mtvNav.stack)
	if n == 0 || mtvNav.stack[n-1].kind != mtvCardFilmsAll {
		return false
	}
	l := mtv.lists[mtvListKey(mtvNav.stack[n-1])]
	return l != nil && l.load.loading && len(l.cards) > 0
}

// mtvDrillLoad is the loading state behind a drill level.
func mtvDrillLoad(d mtvDrill) mtvLoad {
	switch d.kind {
	case mtvCardJfAllMovies, mtvCardJfAllShows:
		return mtv.jf.load
	}
	if l := mtv.lists[mtvListKey(d)]; l != nil {
		return l.load
	}
	return mtvLoad{}
}

// mtvDrillCards are the cards of an open list.
func mtvDrillCards(d mtvDrill) []mediaCard {
	switch d.kind {
	case mtvCardJfAllMovies:
		return mtv.jf.movies
	case mtvCardJfAllShows:
		return mtv.jf.shows
	}
	l := mtv.lists[mtvListKey(d)]
	if l == nil {
		return nil
	}
	return mtvWithProgress(l.cards)
}

// ── Rows ────────────────────────────────────────────────────────────────────

// mtvRetryCard is the card a source that failed shows: A asks it again.
func mtvRetryCard(key, title, why string) mediaCard {
	return mediaCard{title: title, meta: why, mtvKind: mtvCardRetry, mtvKey: key, glyph: "refresh"}
}

// mtvSeeAllCard ends a row that holds more than it shows.
func mtvSeeAllCard(kind mtvCardKind, key string, n int, one, many string) mediaCard {
	meta := "Open the whole list"
	if n > 0 {
		meta = pluralizeCount(n, one, many)
	}
	return mediaCard{title: "See all", meta: meta, mtvKind: kind, mtvKey: key, glyph: "grid"}
}

// mtvCapRow is the first cards of a list, with See all after them when there
// are more.
func mtvCapRow(cards []mediaCard, more mediaCard) []mediaCard {
	if len(cards) <= mediaRowCards {
		return cards
	}
	out := make([]mediaCard, 0, mediaRowCards+1)
	out = append(out, cards[:mediaRowCards]...)
	return append(out, more)
}

// mtvBuildRows builds the tab's rows from the model, keeping each row's focus
// and scroll across a rebuild.
func mtvBuildRows(prev []mediaRow) []mediaRow {
	rows := make([]mediaRow, 0, 7+len(freeFilmShelves))
	jfReady := musicSrcReady(musicSrcJellyfin)
	if jfReady {
		jf := &mtv.jf
		server := dlFirstNonEmpty(jf.server, "Jellyfin")
		has := len(jf.cont)+len(jf.recent)+len(jf.movies)+len(jf.shows) > 0
		switch {
		case !has && (jf.load.loading || !jf.load.loaded):
			rows = append(rows, mediaRow{kind: mediaRowMtvMovies, title: "Jellyfin", empty: "Asking your Jellyfin server…"})
		case !has && jf.load.err != "":
			rows = append(rows, mediaRow{kind: mediaRowMtvMovies, title: "Jellyfin",
				cards: []mediaCard{mtvRetryCard("jf", "Try again", jf.load.err)}})
		case !has:
			rows = append(rows, mediaRow{kind: mediaRowMtvMovies, title: server,
				empty: "No films or shows on " + server + " yet"})
		default:
			if len(jf.cont) > 0 {
				rows = append(rows, mediaRow{kind: mediaRowMtvContinue, title: "Continue watching", cards: jf.cont})
			}
			if len(jf.recent) > 0 {
				rows = append(rows, mediaRow{kind: mediaRowMtvRecent, title: "Recently added · " + server, cards: jf.recent})
			}
			if len(jf.movies) > 0 {
				rows = append(rows, mediaRow{kind: mediaRowMtvMovies, title: "Movies · " + server,
					cards: mtvCapRow(jf.movies, mtvSeeAllCard(mtvCardJfAllMovies, "", len(jf.movies), "movie", "movies"))})
			}
			if len(jf.shows) > 0 {
				rows = append(rows, mediaRow{kind: mediaRowMtvShows, title: "TV shows · " + server,
					cards: mtvCapRow(jf.shows, mtvSeeAllCard(mtvCardJfAllShows, "", len(jf.shows), "show", "shows"))})
			}
		}
	}

	netRow := mediaRow{kind: mediaRowMtvNetwork, title: "On your network"}
	for _, s := range mtv.network.servers {
		netRow.cards = append(netRow.cards, mtvServerCard(s))
	}
	switch {
	case mtv.network.load.loading:
		if len(netRow.cards) == 0 {
			netRow.empty = "Looking for media servers on your network…"
		}
	case len(netRow.cards) == 0:
		netRow.cards = append(netRow.cards, mtvRetryCard("net", "Search again",
			"No media server answered — a NAS, Plex or Windows media sharing shows up here"))
	default:
		netRow.cards = append(netRow.cards, mtvRetryCard("net", "Search again", "Look for servers once more"))
	}
	rows = append(rows, netRow)

	online := IsOnline()
	for i, shelf := range freeFilmShelves {
		if i >= len(mtv.films) {
			break
		}
		row := mediaRow{kind: mediaRowMtvFilms + mediaRowKind(i), title: shelf.Title}
		l := &mtv.films[i]
		switch {
		case len(l.cards) > 0:
			// A row holds one page of the collection; See all pages on.
			row.cards = mtvWithProgress(l.cards)
			if l.total > len(l.cards) {
				row.cards = append(row.cards, mtvSeeAllCard(mtvCardFilmsAll, strconv.Itoa(i), l.total, "film", "films"))
			}
		case !online:
			row.empty = "Free films need the internet"
		case l.load.loading || !l.load.loaded:
			row.empty = "Loading…"
		case l.load.err != "":
			row.cards = []mediaCard{mtvRetryCard("films:"+strconv.Itoa(i), "Try again", l.load.err)}
		default:
			continue
		}
		rows = append(rows, row)
	}

	if !jfReady {
		rows = append(rows, mediaRow{kind: mediaRowMtvSetup, title: "Your own media server", cards: []mediaCard{{
			title: "Connect Jellyfin", meta: "Sign in from your phone", mtvKind: mtvCardSetup, glyph: "phone",
			detail: "Your films and shows, Continue watching and transcoding by your server",
		}}})
	}
	mediaCarryRowFocus(rows, prev)
	return rows
}

// mtvRowsHaveCards reports a row with something to focus.
func mtvRowsHaveCards() bool {
	for _, row := range mediaHub.rows {
		if len(row.cards) > 0 {
			return true
		}
	}
	return false
}

// ── Opening, going back, playing ────────────────────────────────────────────

// mtvOpen opens the list a card stands for, one level deeper.
func mtvOpen(config *Config, card mediaCard) {
	if !mtvCardOpens(card.mtvKind) {
		return
	}
	if n := len(mtvNav.stack); n > 0 {
		mtvNav.stack[n-1].index = mediaHub.index
		mtvNav.stack[n-1].scroll = mediaHub.scrollY
	} else {
		mtvNav.rowsScroll = mediaHub.scrollY
	}
	title, sub := card.title, card.meta
	switch card.mtvKind {
	case mtvCardJfAllMovies:
		title = "Movies"
	case mtvCardJfAllShows:
		title = "TV shows"
	case mtvCardFilmsAll:
		if i, err := strconv.Atoi(card.mtvKey); err == nil && i >= 0 && i < len(freeFilmShelves) {
			title = freeFilmShelves[i].Title
		}
		sub = "Public domain · Internet Archive"
	}
	d := mtvDrill{kind: card.mtvKind, key: card.mtvKey, title: title, sub: sub}
	mtvNav.stack = append(mtvNav.stack, d)
	mediaHub.index = 0
	mediaHub.scrollY = 0
	mediaHub.zone = mediaZoneContent
	mediaHub.sheet = mediaSheet{}
	mediaHub.builtAt = 0
	PlayActivateSound()
	mtvLoadList(d, false)
	if config != nil {
		mediaHubEnsureBuilt(config)
	}
}

// mtvDrillBack closes the innermost open list and puts the ring back where it
// was; false when no list is open.
func mtvDrillBack(config *Config) bool {
	n := len(mtvNav.stack)
	if n == 0 {
		return false
	}
	mtvNav.stack = mtvNav.stack[:n-1]
	if n > 1 {
		top := mtvNav.stack[n-2]
		mediaHub.index = top.index
		mediaHub.scrollY = top.scroll
	} else {
		mediaHub.index = 0
		mediaHub.scrollY = mtvNav.rowsScroll
	}
	mediaHub.zone = mediaZoneContent
	mediaHub.builtAt = 0
	PlayBackSound()
	if config != nil {
		mediaHubEnsureBuilt(config)
	}
	return true
}

// mtvPlayCard runs A on a Movies & TV card: a video plays, a list opens, Try
// again asks again, Connect Jellyfin opens the phone page. It reports whether
// the card is one of this tab's.
func mtvPlayCard(config *Config, card mediaCard, list []mediaCard, index int, fromStart bool) bool {
	switch {
	case card.mtvKind == mtvCardNone:
		return false
	case mtvCardOpens(card.mtvKind):
		mtvOpen(config, card)
	case card.mtvKind == mtvCardSetup:
		PlayActivateSound()
		musicServersPhoneOpen("jellyfin")
	case card.mtvKind == mtvCardRetry:
		PlayActivateSound()
		mtvRetry(card.mtvKey)
	case mtvCardPlays(card.mtvKind):
		mtvPlayVideo(config, card, list, index, fromStart)
	}
	return true
}

// mtvPlayVideo plays a film or an episode where it was left, with the visible
// list as the player's queue.
func mtvPlayVideo(config *Config, card mediaCard, list []mediaCard, index int, fromStart bool) {
	v := card.video
	if config == nil || strings.TrimSpace(v.URL) == "" {
		return
	}
	if card.mtvKind == mtvCardFilm && !IsOnline() {
		ShowToast("Free films need the internet", ToastKindWarning)
		return
	}
	v.Position = 0
	if !fromStart {
		if card.progress > 0 && v.Duration > 0 {
			v.Position = card.progress * v.Duration
		} else {
			v.Position = favoriteResumePosition(v.URL)
		}
	}
	if card.mtvKind == mtvCardJfVideo {
		// Jellyfin hears what plays and keeps where it stopped.
		scrobbleStartWatcher()
	}
	if card.mtvKind != mtvCardFilm {
		playVideoInfo(config, v)
		mtvSetQueue(list, index)
		return
	}
	// A film's copy is looked up first, so a film the Archive cannot serve
	// says so instead of opening a player that will not start.
	if mtv.opening == card.mtvKey {
		return
	}
	mtv.opening = card.mtvKey
	ShowToast("Opening "+card.title+"…", ToastKindInfo)
	id := card.mtvKey
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, dur, err := freeFilmsResolve(ctx, id)
		tbxPost(func() {
			if mtv.opening == id {
				mtv.opening = ""
			}
			if err != nil {
				ShowToast(firstLine(err.Error()), ToastKindWarning)
				return
			}
			if !mediaHubIsCurrent(config) || mediaHubPlayerOnScreen() {
				return
			}
			if v.Duration <= 0 {
				v.Duration = dur
			}
			playVideoInfo(config, v)
			mtvSetQueue(list, index)
		})
	}()
}

// mtvSetQueue makes the list the card was in the player's queue (L2/R2), as
// the other tabs do, keeping only the cards that play.
func mtvSetQueue(list []mediaCard, index int) {
	if index < 0 || index >= len(list) {
		return
	}
	vids := make([]VideoInfo, 0, len(list))
	at := -1
	for i, c := range list {
		if !mtvCardPlays(c.mtvKind) || c.video.URL == "" {
			continue
		}
		if i == index {
			at = len(vids)
		}
		vids = append(vids, c.video)
	}
	if at >= 0 {
		playbackQueue = playbackQueueState{videos: vids, index: at}
	}
}

// mtvSheetActions are the details sheet's rows for a Movies & TV card.
func mtvSheetActions(card mediaCard, favorite, queued bool) ([]mediaSheetAction, []string) {
	switch {
	case mtvCardOpens(card.mtvKind):
		return []mediaSheetAction{mediaActOpen}, []string{"Open"}
	case card.mtvKind == mtvCardSetup:
		return []mediaSheetAction{mediaActOpen}, []string{"Set up on your phone"}
	case card.mtvKind == mtvCardRetry:
		return []mediaSheetAction{mediaActOpen}, []string{card.title}
	}
	acts := []mediaSheetAction{mediaActPlay}
	labels := []string{"Play"}
	if card.progress > 0 && card.video.Duration > 0 {
		labels[0] = "Resume from " + formatDuration(card.progress*card.video.Duration)
		acts = append(acts, mediaActPlayStart)
		labels = append(labels, "Play from the start")
	}
	if queued {
		acts, labels = append(acts, mediaActUpNext), append(labels, "Remove from Up Next")
	} else {
		acts, labels = append(acts, mediaActUpNext), append(labels, "Play next (Up Next)")
	}
	if favorite {
		acts, labels = append(acts, mediaActFavorite), append(labels, "Remove from Favorites")
	} else {
		acts, labels = append(acts, mediaActFavorite), append(labels, "Add to Favorites")
	}
	return acts, labels
}

// mtvStateAction is the button the tab's empty state shows: 4 tries the open
// list again, -1 when there is none.
func mtvStateAction() int {
	n := len(mtvNav.stack)
	if n == 0 || len(mediaHub.cards) > 0 {
		return -1
	}
	if l := mtvDrillLoad(mtvNav.stack[n-1]); l.loaded && !l.loading && l.err != "" {
		return 4
	}
	return -1
}

// ── Drawing ─────────────────────────────────────────────────────────────────

// mtvDraw paints the tab: an open list under its header (with its loading,
// error and empty states), or the rows.
func mtvDraw(renderer *sdl.Renderer, config *Config, rect sdl.Rect, online bool) {
	if n := len(mtvNav.stack); n > 0 {
		d := mtvNav.stack[n-1]
		off := mtvDrawCrumb(renderer, config, rect, d)
		rect.Y += off
		rect.H -= off
		if len(mediaHub.cards) > 0 {
			mediaHubDrawGrid(renderer, config, rect, online)
			return
		}
		l := mtvDrillLoad(d)
		switch {
		case l.loading || !l.loaded:
			mediaHubDrawState(renderer, config, rect, "", "Opening "+d.title+"…", "", "", -1, true)
		case l.err != "":
			mediaHubDrawState(renderer, config, rect, "@wifi-off", "Could not open "+d.title, l.err, "Try again", 4, false)
		default:
			mediaHubDrawState(renderer, config, rect, "@folder", "Nothing to play here",
				"No videos the handheld can open are in this list.", "", -1, false)
		}
		return
	}
	if !mtvRowsHaveCards() {
		mediaHubDrawState(renderer, config, rect, "", "Looking for films and shows…", "", "", -1, true)
		return
	}
	mediaHubDrawRows(renderer, config, rect, online)
}

// mtvDrawCrumb paints an open list's header — a Back chip naming the level it
// returns to, then the list's name and what it holds — and returns the height
// it took. The chip is the pointer's way back; the pad's is B.
func mtvDrawCrumb(renderer *sdl.Renderer, config *Config, rect sdl.Rect, d mtvDrill) int32 {
	font := FontForRole(config, TypeLabel)
	if font == nil {
		return 0
	}
	h := int32(34)
	back := mediaTabLabel(mediaTabMovies)
	if n := len(mtvNav.stack); n > 1 {
		back = mtvNav.stack[n-2].title
	}
	back = ellipsize(font, back, 220)
	glyph := int32(16)
	bw, bh := measureText(font, back)
	chip := sdl.Rect{X: rect.X, Y: rect.Y, W: 12 + glyph + 8 + bw + 16, H: h}
	drawGlassSurface(renderer, chip, h/2, glassState{})
	DrawHomeIcon(renderer, "back", sdl.Rect{X: chip.X + 12, Y: chip.Y + (h-glyph)/2, W: glyph, H: glyph}, false)
	drawText(renderer, font, back, chip.X+12+glyph+8, chip.Y+(h-bh)/2, ColorTextSecondary(), textAlignLeft)
	mediaHub.hits = append(mediaHub.hits, mediaHit{kind: mediaHitStateAction, a: 5, rect: chip})
	x := chip.X + chip.W + 16
	avail := rect.X + rect.W - x
	title := ellipsize(font, d.title, avail)
	tw, th := measureText(font, title)
	drawText(renderer, font, title, x, chip.Y+(h-th)/2, ColorTextPrimary(), textAlignLeft)
	if small := FontForRole(config, TypeCaption); small != nil && d.sub != "" {
		if left := avail - tw - 14; left > 60 {
			sub := ellipsize(small, d.sub, left)
			_, sh := measureText(small, sub)
			drawText(renderer, small, sub, x+tw+14, chip.Y+(h-sh)/2, ColorTextTertiary(), textAlignLeft)
		}
	}
	return h + 12
}
