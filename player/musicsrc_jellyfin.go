package main

// musicsrc_jellyfin.go — Jellyfin as a source: music (artists, albums, songs,
// genres, playlists, favourites), films and series (seasons, episodes,
// Continue watching), Quick Connect sign-in, playback reports (so the server
// shows what plays and keeps where a film was left) and the addresses the
// proxy streams from.
//
// Video: a file the handheld decodes as it is (8-bit H.264 or MPEG-4 at up
// to 1080p, with no DTS or TrueHD sound) is streamed as it is ("direct
// stream", no work for the server); anything else (HEVC, AV1, VP9, 10-bit,
// 4K, DTS) is transcoded by the server to H.264 and AAC at 720p.
//
// The access token goes in the Authorization header, never in an address.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// errJellyfinAuth is a token the server no longer takes.
var errJellyfinAuth = errors.New("Jellyfin signed this handheld out — sign in again (Settings › Music servers)")

type jfItem struct {
	ID                string            `json:"Id"`
	Name              string            `json:"Name"`
	Type              string            `json:"Type"`
	Album             string            `json:"Album"`
	AlbumID           string            `json:"AlbumId"`
	AlbumArtist       string            `json:"AlbumArtist"`
	Artists           []string          `json:"Artists"`
	IndexNumber       int               `json:"IndexNumber"`
	ParentIndexNumber int               `json:"ParentIndexNumber"`
	ProductionYear    int               `json:"ProductionYear"`
	RunTimeTicks      int64             `json:"RunTimeTicks"`
	ChildCount        int               `json:"ChildCount"`
	SongCount         int               `json:"SongCount"`
	AlbumCount        int               `json:"AlbumCount"`
	SeriesName        string            `json:"SeriesName"`
	SeriesID          string            `json:"SeriesId"`
	SeasonID          string            `json:"SeasonId"`
	Genres            []string          `json:"Genres"`
	ImageTags         map[string]string `json:"ImageTags"`
	AlbumPrimaryImage string            `json:"AlbumPrimaryImageTag"`
	UserData          struct {
		PlaybackPositionTicks int64 `json:"PlaybackPositionTicks"`
		Played                bool  `json:"Played"`
		IsFavorite            bool  `json:"IsFavorite"`
	} `json:"UserData"`
	MediaSources []jfMediaSource `json:"MediaSources"`
	// The wide pictures Media › Movies & TV puts on its 16:9 cards
	// (media_movies.go).
	BackdropImageTags       []string `json:"BackdropImageTags"`
	ParentBackdropItemID    string   `json:"ParentBackdropItemId"`
	ParentBackdropImageTags []string `json:"ParentBackdropImageTags"`
	ParentThumbItemID       string   `json:"ParentThumbItemId"`
	ParentThumbImageTag     string   `json:"ParentThumbImageTag"`
}

type jfMediaSource struct {
	ID           string `json:"Id"`
	Container    string `json:"Container"`
	Size         int64  `json:"Size"`
	MediaStreams []struct {
		Type     string `json:"Type"`
		Codec    string `json:"Codec"`
		Width    int    `json:"Width"`
		Height   int    `json:"Height"`
		BitDepth int    `json:"BitDepth"`
	} `json:"MediaStreams"`
}

type jfItems struct {
	Items            []jfItem `json:"Items"`
	TotalRecordCount int      `json:"TotalRecordCount"`
}

// jfClient is one signed-in Jellyfin.
type jfClient struct {
	base  *url.URL
	token string
	user  string
	srv   string
	conf  musicJellyfinConf
}

func jfNewClient() (*jfClient, error) {
	conf := musicServersGet().Jellyfin
	if conf.URL == "" {
		return nil, errors.New("no Jellyfin server yet")
	}
	u, err := url.Parse(conf.URL)
	if err != nil || u.Host == "" {
		return nil, errors.New("the Jellyfin address is broken — send it again")
	}
	return &jfClient{base: u, token: musicDecryptField(conf.Token), user: conf.UserID, conf: conf,
		srv: musicServerTag("jf", conf.URL, conf.UserID)}, nil
}

// jfAuthHeader is Jellyfin's client identity, with the token once signed in.
func jfAuthHeader(token string) string {
	h := fmt.Sprintf(`MediaBrowser Client="JukaHub", Device="%s", DeviceId="%s", Version="%s"`,
		jfQuote(deviceModelName()), jfQuote(musicDeviceID()), jfQuote(strings.TrimPrefix(versionString(nil), "v")))
	if token != "" {
		h += fmt.Sprintf(`, Token="%s"`, jfQuote(token))
	}
	return h
}

func jfQuote(s string) string { return strings.NewReplacer(`"`, "", "\\", "", "\n", " ", "\r", " ").Replace(s) }

// deviceModelName is a short name for the handheld in Jellyfin's sessions.
func deviceModelName() string {
	if IsTrimuiDevice() {
		return "TrimUI handheld"
	}
	return "JukaHub"
}

// jfDo runs one request against base with an optional JSON body.
func jfDo(ctx context.Context, base *url.URL, token, method, path string, q url.Values, body interface{}, out interface{}) error {
	u := *base
	u.Path = strings.TrimRight(u.Path, "/") + path
	if q != nil {
		u.RawQuery = q.Encode()
	}
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = strings.NewReader(string(data))
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", jfAuthHeader(token))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := musicServerClient(base.Scheme).Do(req)
	if err != nil {
		return fmt.Errorf("Jellyfin did not answer: %s", firstLine(err.Error()))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return errJellyfinAuth
	case resp.StatusCode == http.StatusNotFound:
		return errJellyfinNotFound
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("Jellyfin answered %d", resp.StatusCode)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 48<<20)).Decode(out); err != nil {
		return errors.New("Jellyfin sent something unexpected — check the address")
	}
	return nil
}

var errJellyfinNotFound = errors.New("not found on the Jellyfin server")

func (c *jfClient) get(ctx context.Context, path string, q url.Values, out interface{}) error {
	return jfDo(ctx, c.base, c.token, http.MethodGet, path, q, nil, out)
}

// items asks for items; Jellyfin 10.9 moved /Users/{id}/Items to /Items, so
// the old address is the fallback.
func (c *jfClient) items(ctx context.Context, q url.Values) ([]jfItem, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("userId", c.user)
	if q.Get("Fields") == "" {
		q.Set("Fields", "MediaSources,Genres,ChildCount,AlbumArtist,DateCreated")
	}
	var r jfItems
	err := c.get(ctx, "/Items", q, &r)
	if errors.Is(err, errJellyfinNotFound) {
		q.Del("userId")
		err = c.get(ctx, "/Users/"+url.PathEscape(c.user)+"/Items", q, &r)
	}
	return r.Items, err
}

// ── Sign-in ─────────────────────────────────────────────────────────────────

type jfAuthResult struct {
	User struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	} `json:"User"`
	AccessToken string `json:"AccessToken"`
	ServerID    string `json:"ServerId"`
}

type jfPublicInfo struct {
	ServerName string `json:"ServerName"`
	Version    string `json:"Version"`
	ID         string `json:"Id"`
}

// jfPublic checks that a Jellyfin answers at base.
func jfPublic(ctx context.Context, base *url.URL) (jfPublicInfo, error) {
	var info jfPublicInfo
	err := jfDo(ctx, base, "", http.MethodGet, "/System/Info/Public", nil, nil, &info)
	if err == nil && info.ID == "" {
		err = errors.New("that address answers, but it is not a Jellyfin server")
	}
	return info, err
}

// jfSignIn signs in with a user name and password and stores the token (the
// password itself is not kept).
func jfSignIn(ctx context.Context, rawURL, user, password string) (string, error) {
	addr, err := musicCleanServerURL(rawURL)
	if err != nil {
		return "", err
	}
	base, _ := url.Parse(addr)
	info, err := jfPublic(ctx, base)
	if err != nil {
		return "", err
	}
	var r jfAuthResult
	err = jfDo(ctx, base, "", http.MethodPost, "/Users/AuthenticateByName", nil,
		map[string]string{"Username": user, "Pw": password}, &r)
	if errors.Is(err, errJellyfinAuth) {
		return "", errors.New("Jellyfin refused that user name or password")
	}
	if err != nil {
		return "", err
	}
	return info.ServerName, jfStoreAuth(addr, info, r)
}

func jfStoreAuth(addr string, info jfPublicInfo, r jfAuthResult) error {
	if r.AccessToken == "" || r.User.ID == "" {
		return errors.New("Jellyfin did not sign this handheld in")
	}
	enc, err := EncryptAPIToken(r.AccessToken, "Jellyfin access token")
	if err != nil {
		return err
	}
	return musicServersUpdate(func(s *musicServersFile) {
		s.Jellyfin = musicJellyfinConf{URL: addr, User: r.User.Name, UserID: r.User.ID, Token: enc,
			ServerID: dlFirstNonEmpty(r.ServerID, info.ID), ServerName: info.ServerName}
	})
}

// jfQuickConnect is a sign-in waiting for approval: the code the player types
// into any signed-in Jellyfin app (Settings › Quick Connect).
type jfQuickConnect struct {
	Secret string `json:"Secret"`
	Code   string `json:"Code"`
}

// jfQuickStart asks the server for a Quick Connect code.
func jfQuickStart(ctx context.Context, rawURL string) (string, jfQuickConnect, jfPublicInfo, error) {
	var qc jfQuickConnect
	addr, err := musicCleanServerURL(rawURL)
	if err != nil {
		return "", qc, jfPublicInfo{}, err
	}
	base, _ := url.Parse(addr)
	info, err := jfPublic(ctx, base)
	if err != nil {
		return "", qc, info, err
	}
	err = jfDo(ctx, base, "", http.MethodPost, "/QuickConnect/Initiate", nil, nil, &qc)
	if errors.Is(err, errJellyfinAuth) || errors.Is(err, errJellyfinNotFound) {
		return "", qc, info, errors.New("Quick Connect is off on this server — an administrator turns it on in Dashboard › General")
	}
	if err == nil && qc.Code == "" {
		err = errors.New("the server sent no Quick Connect code")
	}
	return addr, qc, info, err
}

// jfQuickPoll checks whether the code was approved; done with an error stops.
func jfQuickPoll(ctx context.Context, addr string, qc jfQuickConnect, info jfPublicInfo) (bool, error) {
	base, err := url.Parse(addr)
	if err != nil {
		return true, err
	}
	var st struct {
		Authenticated bool `json:"Authenticated"`
	}
	if err := jfDo(ctx, base, "", http.MethodGet, "/QuickConnect/Connect", url.Values{"Secret": {qc.Secret}}, nil, &st); err != nil {
		if errors.Is(err, errJellyfinNotFound) {
			return true, errors.New("the Quick Connect code expired — start again")
		}
		return false, nil
	}
	if !st.Authenticated {
		return false, nil
	}
	var r jfAuthResult
	if err := jfDo(ctx, base, "", http.MethodPost, "/Users/AuthenticateWithQuickConnect", nil,
		map[string]string{"Secret": qc.Secret}, &r); err != nil {
		return true, err
	}
	return true, jfStoreAuth(addr, info, r)
}

// jfDiscover finds Jellyfin servers on the home network: UDP 7359, "who is
// JellyfinServer?", as the official apps do.
func jfDiscover(wait time.Duration) []string {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil
	}
	defer conn.Close()
	dst := &net.UDPAddr{IP: net.IPv4bcast, Port: 7359}
	_, _ = conn.WriteTo([]byte("who is JellyfinServer?"), dst)
	_ = conn.SetReadDeadline(time.Now().Add(wait))
	var out []string
	seen := map[string]bool{}
	buf := make([]byte, 2048)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			break
		}
		var r struct {
			Address string `json:"Address"`
		}
		if json.Unmarshal(buf[:n], &r) == nil && r.Address != "" && !seen[r.Address] {
			seen[r.Address] = true
			out = append(out, r.Address)
		}
	}
	return out
}

// ── Rows ────────────────────────────────────────────────────────────────────

func jfTicks(t int64) float64 { return float64(t) / 1e7 }

func (c *jfClient) art(it jfItem) string {
	if tag := it.ImageTags["Primary"]; tag != "" {
		return "jf:" + it.ID
	}
	if it.AlbumID != "" && it.AlbumPrimaryImage != "" {
		return "jf:" + it.AlbumID
	}
	return ""
}

func (c *jfClient) trackItem(it jfItem) musicItem {
	suffix := "mp3"
	var size int64
	if len(it.MediaSources) > 0 {
		if s := strings.ToLower(strings.Split(it.MediaSources[0].Container, ",")[0]); s != "" {
			suffix = s
		}
		size = it.MediaSources[0].Size
	}
	artist := it.AlbumArtist
	if len(it.Artists) > 0 {
		artist = it.Artists[0]
	}
	genre := ""
	if len(it.Genres) > 0 {
		genre = it.Genres[0]
	}
	return musicItem{Kind: "track", Src: musicSrcJellyfin, ID: it.ID, Title: it.Name, Artist: artist,
		Album: it.Album, AlbumID: it.AlbumID, Genre: genre, Year: it.ProductionYear, Track: it.IndexNumber,
		Disc: it.ParentIndexNumber, Duration: jfTicks(it.RunTimeTicks), Art: c.art(it), Suffix: suffix,
		Size: size, Starred: it.UserData.IsFavorite,
		Path: musicProxyTrackURL(musicSrcJellyfin, c.srv, it.ID, suffix)}
}

func (c *jfClient) row(it jfItem) musicItem {
	switch it.Type {
	case "Audio":
		return c.trackItem(it)
	case "MusicAlbum":
		return musicItem{Kind: "album", Src: musicSrcJellyfin, ID: it.ID, Title: it.Name, Artist: it.AlbumArtist,
			Year: it.ProductionYear, Count: max(it.ChildCount, it.SongCount), Art: c.art(it), Starred: it.UserData.IsFavorite}
	case "MusicArtist":
		return musicItem{Kind: "artist", Src: musicSrcJellyfin, ID: it.ID, Title: it.Name, Count: it.AlbumCount, Art: c.art(it)}
	case "MusicGenre", "Genre":
		return musicItem{Kind: "genre", Src: musicSrcJellyfin, ID: it.ID, Title: it.Name, Count: it.SongCount}
	case "Playlist":
		return musicItem{Kind: "playlist", Src: musicSrcJellyfin, ID: it.ID, Title: it.Name, Count: it.ChildCount, Art: c.art(it)}
	case "Series":
		return musicItem{Kind: "series", Src: musicSrcJellyfin, ID: it.ID, Title: it.Name, Year: it.ProductionYear,
			Count: it.ChildCount, Art: c.art(it)}
	case "Season":
		return musicItem{Kind: "season", Src: musicSrcJellyfin, ID: it.ID, Title: it.Name, Track: it.IndexNumber,
			Count: it.ChildCount, Art: c.art(it), ArtistID: it.SeriesID, Album: it.SeriesName}
	}
	return c.videoItem(it)
}

// videoItem is a film or an episode, with the way it will play.
func (c *jfClient) videoItem(it jfItem) musicItem {
	kind := "movie"
	title := it.Name
	if it.Type == "Episode" {
		kind = "episode"
		if it.IndexNumber > 0 {
			title = strconv.Itoa(it.IndexNumber) + ". " + it.Name
		}
	}
	row := musicItem{Kind: kind, Src: musicSrcJellyfin, ID: it.ID, Title: title, Year: it.ProductionYear,
		Duration: jfTicks(it.RunTimeTicks), Art: c.art(it), Position: jfTicks(it.UserData.PlaybackPositionTicks),
		Played: it.UserData.Played, Video: true, Album: it.SeriesName}
	if len(it.MediaSources) > 0 {
		ms := it.MediaSources[0]
		row.MediaID, row.Size, row.Suffix = ms.ID, ms.Size, strings.ToLower(strings.Split(ms.Container, ",")[0])
		row.Direct = jfCanDirectPlay(ms)
	}
	mode := "t"
	if row.Direct {
		mode = "d"
	}
	row.Path = musicProxyVideoURL(c.srv, it.ID, row.MediaID, mode, row.Suffix)
	return row
}

// jfCanDirectPlay is the handheld's own decoder's reach: 8-bit H.264 or
// MPEG-4 up to 1080p, no DTS or TrueHD sound. Pure.
func jfCanDirectPlay(ms jfMediaSource) bool {
	video := false
	for _, s := range ms.MediaStreams {
		codec := strings.ToLower(s.Codec)
		switch s.Type {
		case "Video":
			if codec != "h264" && codec != "mpeg4" && codec != "msmpeg4v3" {
				return false
			}
			if s.BitDepth > 8 || s.Width > 1920 || s.Height > 1088 {
				return false
			}
			video = true
		case "Audio":
			if strings.HasPrefix(codec, "dts") || codec == "truehd" || codec == "mlp" {
				return false
			}
		}
	}
	return video
}

func (c *jfClient) rows(list []jfItem) []musicItem {
	out := make([]musicItem, 0, len(list))
	for _, it := range list {
		out = append(out, c.row(it))
	}
	return out
}

// jfFetch answers a query from Jellyfin.
func jfFetch(ctx context.Context, q musicQuery) ([]musicItem, error) {
	c, err := jfNewClient()
	if err != nil {
		return nil, err
	}
	rec := func(v url.Values) url.Values {
		v.Set("Recursive", "true")
		return v
	}
	switch q.What {
	case "artists":
		var r jfItems
		err := c.get(ctx, "/Artists/AlbumArtists", url.Values{"userId": {c.user}, "Recursive": {"true"},
			"SortBy": {"SortName"}, "Limit": {"5000"}}, &r)
		return c.rows(r.Items), err
	case "artistAlbums":
		list, err := c.items(ctx, rec(url.Values{"IncludeItemTypes": {"MusicAlbum"}, "AlbumArtistIds": {q.Parent.ID},
			"SortBy": {"ProductionYear,SortName"}}))
		return c.rows(list), err
	case "artistSongs":
		list, err := c.items(ctx, rec(url.Values{"IncludeItemTypes": {"Audio"}, "ArtistIds": {q.Parent.ID},
			"SortBy": {"Album,ParentIndexNumber,IndexNumber"}, "Limit": {"2000"}}))
		return c.rows(list), err
	case "albums":
		list, err := c.items(ctx, rec(url.Values{"IncludeItemTypes": {"MusicAlbum"}, "SortBy": {"SortName"}, "Limit": {"5000"}}))
		return c.rows(list), err
	case "recent":
		list, err := c.items(ctx, rec(url.Values{"IncludeItemTypes": {"MusicAlbum"}, "SortBy": {"DateCreated"},
			"SortOrder": {"Descending"}, "Limit": {"60"}}))
		return c.rows(list), err
	case "albumTracks":
		list, err := c.items(ctx, rec(url.Values{"ParentId": {q.Parent.ID}, "IncludeItemTypes": {"Audio"},
			"SortBy": {"ParentIndexNumber,IndexNumber,SortName"}}))
		return c.rows(list), err
	case "songs":
		list, err := c.items(ctx, rec(url.Values{"IncludeItemTypes": {"Audio"}, "SortBy": {"SortName"}, "Limit": {"5000"}}))
		return c.rows(list), err
	case "random":
		list, err := c.items(ctx, rec(url.Values{"IncludeItemTypes": {"Audio"}, "SortBy": {"Random"}, "Limit": {"200"}}))
		return c.rows(list), err
	case "genres":
		var r jfItems
		err := c.get(ctx, "/MusicGenres", url.Values{"userId": {c.user}, "Recursive": {"true"}, "SortBy": {"SortName"}}, &r)
		return c.rows(r.Items), err
	case "genreTracks":
		list, err := c.items(ctx, rec(url.Values{"IncludeItemTypes": {"Audio"}, "GenreIds": {q.Parent.ID},
			"SortBy": {"AlbumArtist,Album,ParentIndexNumber,IndexNumber"}, "Limit": {"1000"}}))
		return c.rows(list), err
	case "playlists":
		list, err := c.items(ctx, rec(url.Values{"IncludeItemTypes": {"Playlist"}, "SortBy": {"SortName"}}))
		return c.rows(list), err
	case "playlistTracks":
		list, err := c.items(ctx, url.Values{"ParentId": {q.Parent.ID}})
		return c.rows(list), err
	case "favorites":
		list, err := c.items(ctx, rec(url.Values{"IncludeItemTypes": {"Audio"}, "Filters": {"IsFavorite"}, "SortBy": {"SortName"}}))
		return c.rows(list), err
	case "search":
		list, err := c.items(ctx, rec(url.Values{"searchTerm": {q.Text}, "IncludeItemTypes": {"MusicArtist,MusicAlbum,Audio"},
			"Limit": {"100"}}))
		return c.rows(list), err
	case "movies":
		list, err := c.items(ctx, rec(url.Values{"IncludeItemTypes": {"Movie"}, "SortBy": {"SortName"}, "Limit": {"3000"}}))
		return c.rows(list), err
	case "shows":
		list, err := c.items(ctx, rec(url.Values{"IncludeItemTypes": {"Series"}, "SortBy": {"SortName"}, "Limit": {"3000"}}))
		return c.rows(list), err
	case "seasons":
		var r jfItems
		err := c.get(ctx, "/Shows/"+url.PathEscape(q.Parent.ID)+"/Seasons", url.Values{"userId": {c.user}}, &r)
		return c.rows(r.Items), err
	case "episodes":
		var r jfItems
		err := c.get(ctx, "/Shows/"+url.PathEscape(q.Parent.ArtistID)+"/Episodes", url.Values{"userId": {c.user},
			"SeasonId": {q.Parent.ID}, "Fields": {"MediaSources"}}, &r)
		return c.rows(r.Items), err
	case "continue":
		list, err := c.items(ctx, rec(url.Values{"IncludeItemTypes": {"Movie,Episode"}, "Filters": {"IsResumable"},
			"SortBy": {"DatePlayed"}, "SortOrder": {"Descending"}, "Limit": {"40"}}))
		return c.rows(list), err
	}
	return nil, nil
}

// ── Streaming and reports ───────────────────────────────────────────────────

// streamPath is the upstream path of an audio file, as it is.
func (c *jfClient) audioPath(id string) (string, url.Values) {
	return "/Audio/" + url.PathEscape(id) + "/stream", url.Values{"static": {"true"}}
}

// videoPath is the upstream path of a video: as it is (direct), or the
// server's H.264/AAC transcode from start seconds.
func (c *jfClient) videoPath(id, mediaID string, direct bool, start float64, session string) (string, url.Values) {
	if direct {
		q := url.Values{"static": {"true"}}
		if mediaID != "" {
			q.Set("MediaSourceId", mediaID)
		}
		return "/Videos/" + url.PathEscape(id) + "/stream", q
	}
	q := url.Values{
		"VideoCodec": {"h264"}, "AudioCodec": {"aac"}, "MaxWidth": {"1280"}, "MaxHeight": {"720"},
		"VideoBitRate": {"3000000"}, "AudioBitRate": {"160000"}, "MaxAudioChannels": {"2"},
		"DeviceId": {musicDeviceID()}, "PlaySessionId": {session},
	}
	if mediaID != "" {
		q.Set("MediaSourceId", mediaID)
	}
	if start > 0 {
		q.Set("StartTimeTicks", strconv.FormatInt(int64(start*1e7), 10))
	}
	return "/Videos/" + url.PathEscape(id) + "/stream.ts", q
}

// jfReport tells the server about playback: what (playing, progress,
// stopped), the item, the position and the pause state.
func jfReport(ctx context.Context, what, id string, pos float64, paused bool, session string) error {
	c, err := jfNewClient()
	if err != nil {
		return err
	}
	path := "/Sessions/Playing"
	switch what {
	case "progress":
		path += "/Progress"
	case "stopped":
		path += "/Stopped"
	}
	body := map[string]interface{}{"ItemId": id, "PositionTicks": int64(pos * 1e7), "IsPaused": paused,
		"CanSeek": true, "PlaySessionId": session, "PlayMethod": "DirectStream"}
	return jfDo(ctx, c.base, c.token, http.MethodPost, path, nil, body, nil)
}

// jfFavorite marks or unmarks a favourite.
func jfFavorite(ctx context.Context, id string, on bool) error {
	c, err := jfNewClient()
	if err != nil {
		return err
	}
	method := http.MethodDelete
	if on {
		method = http.MethodPost
	}
	err = jfDo(ctx, c.base, c.token, method, "/UserFavoriteItems/"+url.PathEscape(id), url.Values{"userId": {c.user}}, nil, nil)
	if errors.Is(err, errJellyfinNotFound) {
		err = jfDo(ctx, c.base, c.token, method, "/Users/"+url.PathEscape(c.user)+"/FavoriteItems/"+url.PathEscape(id), nil, nil, nil)
	}
	return err
}
