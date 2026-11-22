package main

// musicsrc_subsonic.go — Navidrome (and any Subsonic-API server: Gonic,
// Airsonic, Ampache's Subsonic mode) as a source: its artists, albums, songs,
// genres, playlists and starred songs, search, streaming through the proxy,
// scrobbling and stars.
//
// Every call is signed with the API's token scheme: md5(password + salt) with
// a fresh salt, so the password itself never goes over the network. The
// password is still needed to make the token, so it is stored (encrypted).

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	ssAPIVersion = "1.16.1"
	ssClientName = "JukaHub"
	ssPageSize   = 500
	ssMaxRows    = 5000
)

// errSubsonicAuth is a refused user name or password (Subsonic error 40).
var errSubsonicAuth = errors.New("Navidrome refused the user name or password — send them again from your phone")

// ssFlex reads a JSON value that one server sends as a number and another as
// a string (ids, counts).
type ssFlex string

func (f *ssFlex) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*f = ""
		return nil
	}
	if strings.HasPrefix(s, "\"") {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*f = ssFlex(v)
		return nil
	}
	*f = ssFlex(s)
	return nil
}

func (f ssFlex) int() int {
	n, _ := strconv.Atoi(strings.SplitN(string(f), ".", 2)[0])
	return n
}

type ssArtist struct {
	ID         ssFlex `json:"id"`
	Name       string `json:"name"`
	AlbumCount ssFlex `json:"albumCount"`
	CoverArt   ssFlex `json:"coverArt"`
	Starred    string `json:"starred"`
}

type ssAlbum struct {
	ID        ssFlex `json:"id"`
	Name      string `json:"name"`
	Title     string `json:"title"`
	Artist    string `json:"artist"`
	ArtistID  ssFlex `json:"artistId"`
	SongCount ssFlex `json:"songCount"`
	Year      ssFlex `json:"year"`
	Genre     string `json:"genre"`
	CoverArt  ssFlex `json:"coverArt"`
	Starred   string `json:"starred"`
}

type ssSong struct {
	ID         ssFlex `json:"id"`
	Title      string `json:"title"`
	Album      string `json:"album"`
	AlbumID    ssFlex `json:"albumId"`
	Artist     string `json:"artist"`
	ArtistID   ssFlex `json:"artistId"`
	Track      ssFlex `json:"track"`
	DiscNumber ssFlex `json:"discNumber"`
	Year       ssFlex `json:"year"`
	Genre      string `json:"genre"`
	Duration   ssFlex `json:"duration"`
	Suffix     string `json:"suffix"`
	Size       ssFlex `json:"size"`
	CoverArt   ssFlex `json:"coverArt"`
	Starred    string `json:"starred"`
	IsVideo    bool   `json:"isVideo"`
}

type ssPlaylist struct {
	ID        ssFlex `json:"id"`
	Name      string `json:"name"`
	SongCount ssFlex `json:"songCount"`
	CoverArt  ssFlex `json:"coverArt"`
}

type ssGenre struct {
	Value      string `json:"value"`
	SongCount  ssFlex `json:"songCount"`
	AlbumCount ssFlex `json:"albumCount"`
}

type ssLists struct {
	Artist []ssArtist `json:"artist"`
	Album  []ssAlbum  `json:"album"`
	Song   []ssSong   `json:"song"`
}

// ssBody is the "subsonic-response" object with the parts this file reads.
type ssBody struct {
	Status string `json:"status"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Artists *struct {
		Index []struct {
			Artist []ssArtist `json:"artist"`
		} `json:"index"`
	} `json:"artists"`
	Artist *struct {
		ssArtist
		Album []ssAlbum `json:"album"`
	} `json:"artist"`
	Album *struct {
		ssAlbum
		Song []ssSong `json:"song"`
	} `json:"album"`
	AlbumList2 *struct {
		Album []ssAlbum `json:"album"`
	} `json:"albumList2"`
	RandomSongs *struct {
		Song []ssSong `json:"song"`
	} `json:"randomSongs"`
	Genres *struct {
		Genre []ssGenre `json:"genre"`
	} `json:"genres"`
	SongsByGenre *struct {
		Song []ssSong `json:"song"`
	} `json:"songsByGenre"`
	Playlists *struct {
		Playlist []ssPlaylist `json:"playlist"`
	} `json:"playlists"`
	Playlist *struct {
		ssPlaylist
		Entry []ssSong `json:"entry"`
	} `json:"playlist"`
	SearchResult3 *ssLists `json:"searchResult3"`
	Starred2      *ssLists `json:"starred2"`
}

// ssClient is one signed-in Navidrome.
type ssClient struct {
	base *url.URL
	user string
	pass string
	srv  string // the proxy's short name for this server and account
	conf musicNavidromeConf
}

// ssNewClient is the client for the stored server.
func ssNewClient() (*ssClient, error) {
	conf := musicServersGet().Navidrome
	return ssClientFor(conf, musicDecryptField(conf.Password))
}

func ssClientFor(conf musicNavidromeConf, password string) (*ssClient, error) {
	if conf.URL == "" || conf.User == "" {
		return nil, errors.New("no Navidrome server yet")
	}
	u, err := url.Parse(conf.URL)
	if err != nil || u.Host == "" {
		return nil, errors.New("the Navidrome address is broken — send it again")
	}
	return &ssClient{base: u, user: conf.User, pass: password, conf: conf,
		srv: musicServerTag("nd", conf.URL, conf.User)}, nil
}

// ssSalt is a fresh salt for one call.
func ssSalt() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// endpoint is the signed address of a method with q's parameters. It holds
// the token: only the proxy and this file ever see it, never a log.
func (c *ssClient) endpoint(method string, q url.Values) string {
	if q == nil {
		q = url.Values{}
	}
	salt := ssSalt()
	sum := md5.Sum([]byte(c.pass + salt))
	q.Set("u", c.user)
	q.Set("t", hex.EncodeToString(sum[:]))
	q.Set("s", salt)
	q.Set("v", ssAPIVersion)
	q.Set("c", ssClientName)
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + "/rest/" + method
	u.RawQuery = q.Encode()
	return u.String()
}

// call runs one JSON method.
func (c *ssClient) call(ctx context.Context, method string, q url.Values) (*ssBody, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("f", "json")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(method, q), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub/"+strings.TrimPrefix(versionString(nil), "v"))
	resp, err := musicServerClient(c.base.Scheme).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Navidrome did not answer: %s", firstLine(err.Error()))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errSubsonicAuth
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Navidrome answered %d", resp.StatusCode)
	}
	var wrap struct {
		R ssBody `json:"subsonic-response"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&wrap); err != nil {
		return nil, errors.New("Navidrome sent something that is not a Subsonic answer — check the address")
	}
	if wrap.R.Status != "ok" {
		if wrap.R.Error != nil {
			if wrap.R.Error.Code == 40 || wrap.R.Error.Code == 41 {
				return nil, errSubsonicAuth
			}
			return nil, errors.New("Navidrome: " + wrap.R.Error.Message)
		}
		return nil, errors.New("Navidrome said no")
	}
	return &wrap.R, nil
}

// ssCheck signs in once (ping) — the phone page and setup use it.
func ssCheck(ctx context.Context, conf musicNavidromeConf, password string) error {
	c, err := ssClientFor(conf, password)
	if err != nil {
		return err
	}
	_, err = c.call(ctx, "ping", nil)
	return err
}

// ── Rows ────────────────────────────────────────────────────────────────────

func (c *ssClient) art(id ssFlex) string {
	if id == "" {
		return ""
	}
	return "nd:" + string(id)
}

func (c *ssClient) songItem(s ssSong) musicItem {
	suffix := strings.ToLower(strings.TrimSpace(s.Suffix))
	if suffix == "" {
		suffix = "mp3"
	}
	if c.conf.Quality > 0 {
		suffix = "mp3" // a capped stream is transcoded to MP3
	}
	return musicItem{Kind: "track", Src: musicSrcNavidrome, ID: string(s.ID), Title: s.Title, Artist: s.Artist,
		Album: s.Album, AlbumID: string(s.AlbumID), ArtistID: string(s.ArtistID), Genre: s.Genre,
		Year: s.Year.int(), Track: s.Track.int(), Disc: s.DiscNumber.int(), Duration: float64(s.Duration.int()),
		Art: c.art(s.CoverArt), Suffix: suffix, Size: int64(s.Size.int()), Starred: s.Starred != "",
		Path: musicProxyTrackURL(musicSrcNavidrome, c.srv, string(s.ID), suffix)}
}

func (c *ssClient) albumItem(a ssAlbum) musicItem {
	title := a.Name
	if title == "" {
		title = a.Title
	}
	return musicItem{Kind: "album", Src: musicSrcNavidrome, ID: string(a.ID), Title: title, Artist: a.Artist,
		ArtistID: string(a.ArtistID), Genre: a.Genre, Year: a.Year.int(), Count: a.SongCount.int(),
		Art: c.art(a.CoverArt), Starred: a.Starred != ""}
}

func (c *ssClient) artistItem(a ssArtist) musicItem {
	return musicItem{Kind: "artist", Src: musicSrcNavidrome, ID: string(a.ID), Title: a.Name,
		Count: a.AlbumCount.int(), Art: c.art(a.CoverArt), Starred: a.Starred != ""}
}

func (c *ssClient) songs(list []ssSong) []musicItem {
	out := make([]musicItem, 0, len(list))
	for _, s := range list {
		if s.IsVideo {
			continue
		}
		out = append(out, c.songItem(s))
	}
	return out
}

// albumList pages through getAlbumList2.
func (c *ssClient) albumList(ctx context.Context, kind string, limit int) ([]musicItem, error) {
	var out []musicItem
	for offset := 0; offset < limit; offset += ssPageSize {
		size := ssPageSize
		if limit-offset < size {
			size = limit - offset
		}
		b, err := c.call(ctx, "getAlbumList2", url.Values{"type": {kind}, "size": {strconv.Itoa(size)}, "offset": {strconv.Itoa(offset)}})
		if err != nil {
			return out, err
		}
		if b.AlbumList2 == nil || len(b.AlbumList2.Album) == 0 {
			break
		}
		for _, a := range b.AlbumList2.Album {
			out = append(out, c.albumItem(a))
		}
		if len(b.AlbumList2.Album) < size {
			break
		}
	}
	return out, nil
}

// ndFetch answers a query from Navidrome.
func ndFetch(ctx context.Context, q musicQuery) ([]musicItem, error) {
	c, err := ssNewClient()
	if err != nil {
		return nil, err
	}
	switch q.What {
	case "artists":
		b, err := c.call(ctx, "getArtists", nil)
		if err != nil {
			return nil, err
		}
		var out []musicItem
		if b.Artists != nil {
			for _, idx := range b.Artists.Index {
				for _, a := range idx.Artist {
					out = append(out, c.artistItem(a))
				}
			}
		}
		return out, nil
	case "artistAlbums":
		id := q.Parent.ID
		if q.Parent.Src != musicSrcNavidrome || id == "" {
			id = q.Parent.ArtistID
		}
		if id == "" {
			return nil, nil
		}
		b, err := c.call(ctx, "getArtist", url.Values{"id": {id}})
		if err != nil {
			return nil, err
		}
		var out []musicItem
		if b.Artist != nil {
			for _, a := range b.Artist.Album {
				out = append(out, c.albumItem(a))
			}
		}
		return out, nil
	case "artistSongs":
		albums, err := ndFetch(ctx, musicQuery{What: "artistAlbums", Parent: q.Parent})
		if err != nil {
			return nil, err
		}
		var out []musicItem
		for i, a := range albums {
			if i >= 40 || ctx.Err() != nil {
				break
			}
			tracks, err := ndFetch(ctx, musicQuery{What: "albumTracks", Parent: a})
			if err != nil {
				return out, err
			}
			out = append(out, tracks...)
		}
		return out, nil
	case "albums":
		return c.albumList(ctx, "alphabeticalByName", ssMaxRows)
	case "recent":
		return c.albumList(ctx, "newest", 60)
	case "albumTracks":
		b, err := c.call(ctx, "getAlbum", url.Values{"id": {q.Parent.ID}})
		if err != nil {
			return nil, err
		}
		if b.Album == nil {
			return nil, nil
		}
		return c.songs(b.Album.Song), nil
	case "songs":
		// No "every song" call: search3 with an empty query lists them (as
		// Navidrome documents for clients that sync), a page at a time.
		var out []musicItem
		for offset := 0; offset < ssMaxRows; offset += ssPageSize {
			b, err := c.call(ctx, "search3", url.Values{"query": {""}, "songCount": {strconv.Itoa(ssPageSize)},
				"songOffset": {strconv.Itoa(offset)}, "artistCount": {"0"}, "albumCount": {"0"}})
			if err != nil {
				return out, err
			}
			if b.SearchResult3 == nil || len(b.SearchResult3.Song) == 0 {
				break
			}
			out = append(out, c.songs(b.SearchResult3.Song)...)
			if len(b.SearchResult3.Song) < ssPageSize {
				break
			}
		}
		return out, nil
	case "random":
		b, err := c.call(ctx, "getRandomSongs", url.Values{"size": {"200"}})
		if err != nil {
			return nil, err
		}
		if b.RandomSongs == nil {
			return nil, nil
		}
		return c.songs(b.RandomSongs.Song), nil
	case "genres":
		b, err := c.call(ctx, "getGenres", nil)
		if err != nil {
			return nil, err
		}
		var out []musicItem
		if b.Genres != nil {
			for _, g := range b.Genres.Genre {
				if g.Value == "" {
					continue
				}
				out = append(out, musicItem{Kind: "genre", Src: musicSrcNavidrome, ID: g.Value, Title: g.Value, Count: g.SongCount.int()})
			}
		}
		return out, nil
	case "genreTracks":
		b, err := c.call(ctx, "getSongsByGenre", url.Values{"genre": {q.Parent.Title}, "count": {"500"}})
		if err != nil {
			return nil, err
		}
		if b.SongsByGenre == nil {
			return nil, nil
		}
		return c.songs(b.SongsByGenre.Song), nil
	case "playlists":
		b, err := c.call(ctx, "getPlaylists", nil)
		if err != nil {
			return nil, err
		}
		var out []musicItem
		if b.Playlists != nil {
			for _, p := range b.Playlists.Playlist {
				out = append(out, musicItem{Kind: "playlist", Src: musicSrcNavidrome, ID: string(p.ID), Title: p.Name,
					Count: p.SongCount.int(), Art: c.art(p.CoverArt)})
			}
		}
		return out, nil
	case "playlistTracks":
		b, err := c.call(ctx, "getPlaylist", url.Values{"id": {q.Parent.ID}})
		if err != nil {
			return nil, err
		}
		if b.Playlist == nil {
			return nil, nil
		}
		return c.songs(b.Playlist.Entry), nil
	case "favorites":
		b, err := c.call(ctx, "getStarred2", nil)
		if err != nil {
			return nil, err
		}
		if b.Starred2 == nil {
			return nil, nil
		}
		return c.songs(b.Starred2.Song), nil
	case "search":
		b, err := c.call(ctx, "search3", url.Values{"query": {q.Text}, "songCount": {"60"}, "albumCount": {"20"}, "artistCount": {"20"}})
		if err != nil {
			return nil, err
		}
		var out []musicItem
		if r := b.SearchResult3; r != nil {
			for _, a := range r.Artist {
				out = append(out, c.artistItem(a))
			}
			for _, a := range r.Album {
				out = append(out, c.albumItem(a))
			}
			out = append(out, c.songs(r.Song)...)
		}
		return out, nil
	}
	return nil, nil
}

// ── Streaming, downloads, scrobbles and stars ───────────────────────────────

// ssStreamURL is the upstream address the proxy fetches a song from: the
// original file, or an MP3 at the quality cap.
func (c *ssClient) streamURL(id string) string {
	q := url.Values{"id": {id}}
	if c.conf.Quality > 0 {
		q.Set("maxBitRate", strconv.Itoa(c.conf.Quality))
		q.Set("format", "mp3")
	} else {
		q.Set("format", "raw")
	}
	return c.endpoint("stream", q)
}

// downloadURL is the original file, for "Download offline".
func (c *ssClient) downloadURL(id string) string {
	return c.endpoint("download", url.Values{"id": {id}})
}

// coverURL is a cover at about size pixels.
func (c *ssClient) coverURL(id string, size int) string {
	return c.endpoint("getCoverArt", url.Values{"id": {id}, "size": {strconv.Itoa(size)}})
}

// ndScrobble tells Navidrome a song is playing (submission false) or was
// listened to at when.
func ndScrobble(ctx context.Context, id string, submission bool, when time.Time) error {
	c, err := ssNewClient()
	if err != nil {
		return err
	}
	q := url.Values{"id": {id}, "submission": {strconv.FormatBool(submission)}}
	if submission {
		q.Set("time", strconv.FormatInt(when.UnixMilli(), 10))
	}
	_, err = c.call(ctx, "scrobble", q)
	return err
}

// ndStar stars or unstars a song.
func ndStar(ctx context.Context, id string, on bool) error {
	c, err := ssNewClient()
	if err != nil {
		return err
	}
	method := "unstar"
	if on {
		method = "star"
	}
	_, err = c.call(ctx, method, url.Values{"id": {id}})
	return err
}
