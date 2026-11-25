package main

// brw_stream.go — videos, songs and live streams from the Browser.
//
// A tester, told that Colosseum (a Stremio client for Android) cannot run on
// these handhelds, asked on 2026-11-24 whether they "can streaming from
// browser": watch what a web page offers, the way a phone's browser would. The
// reader already turned <video> elements and YouTube or Vimeo embeds into play
// cards (webpage_video.go) and sent YouTube links to Media. Three gaps are
// closed here, each one handing an address to the players JukaHub already has
// — the video player for a file or an HLS playlist, the music player for a
// song, yt-dlp for a page — and none of them fetching or rewriting a stream:
//
//   - A link straight to a video, a song or a live playlist (.mp4, .webm,
//     .m3u8, .mp3…) plays instead of loading. Every way the reader follows a
//     link asks brwOpenYouTubeLink, and a typed address, a bookmark or the
//     history asks brwOpenAddress (browser_youtube.go); both now ask here too.
//   - An address that answers with such a file — a link without the ending, a
//     server that names it only in its Content-Type — showed "Can't show this
//     file" with Download alone. The card now leads with Play (brwStreamCard,
//     brwMediaButtons in brw_offline.go).
//   - A page whose player is built by its scripts has no <video> for the
//     reader to find. The Select menu's "Play video on this page" hands the
//     page to yt-dlp, whose site extractors and generic page scan find the
//     video most sites serve (brwPlayPageVideo); a page with play cards plays
//     its first one.
//
// What plays is what the site serves any browser. Services that protect their
// streams with DRM (Netflix, Disney+, Prime Video) do not play: yt-dlp refuses
// them and the handhelds have no Widevine. No torrent or scraper source is
// wired in anywhere (docs/MOVIES_TV.md, "Android streaming apps").

import (
	"net/url"
	"path"
	"slices"
	"strings"
)

// brwStreamLinkExts are the endings that make an address a video, a song or a
// live playlist the players open straight from the web. Not .ts: on the web
// that is more often TypeScript than an MPEG transport stream, and a real one
// still plays from its card (brwStreamContentType).
var brwStreamLinkExts = []string{
	".mp4", ".m4v", ".webm", ".mkv", ".mov", ".avi", ".flv", ".wmv", ".mpg", ".mpeg", ".m3u8",
	".mp3", ".m4a", ".aac", ".ogg", ".oga", ".opus", ".flac", ".wav",
}

// brwStreamTypes are the response types besides video/* and audio/* that the
// players stream: HLS and DASH playlists, Ogg and Matroska.
var brwStreamTypes = []string{
	"application/vnd.apple.mpegurl", "application/x-mpegurl", "application/mpegurl",
	"application/dash+xml", "application/ogg", "application/x-matroska",
}

// brwMediaAddress reports an http(s) address that names one of those files —
// and not a code host's page about one (…/blob/main/clip.mp4 is HTML).
func brwMediaAddress(address string) bool {
	u, err := url.Parse(strings.TrimSpace(address))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	if strings.Contains(u.Path, "/blob/") {
		return false
	}
	return slices.Contains(brwStreamLinkExts, strings.ToLower(path.Ext(u.Path)))
}

// brwStreamContentType reports a response type the players stream rather than
// a file only worth saving.
func brwStreamContentType(mediaType string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if strings.HasPrefix(mediaType, "video/") || strings.HasPrefix(mediaType, "audio/") {
		return true
	}
	return slices.Contains(brwStreamTypes, mediaType)
}

// brwStreamResponse reports a response that is a stream: its type says so, or
// — a playlist served as text/plain, a file as application/octet-stream — its
// address does. An HTML page never is, whatever its address ends in.
func brwStreamResponse(address, mediaType string) bool {
	if brwDocumentKind(mediaType) == brwDocHTML {
		return false
	}
	return brwStreamContentType(mediaType) || brwMediaAddress(address)
}

// brwStreamCard is the fetch's answer for such an address: the "Can't show
// this file" card with Play first (brw_offline.go). Media keeps the type, so
// Play knows a song from a film.
func brwStreamCard(address, mediaType string) (textBrowserDocument, error) {
	what := "video or stream"
	if brwAudioType(mediaType) {
		what = "song or sound file"
	}
	f := brwNewFailure(brwFailContent, address, "This address is a "+what+" ("+mediaType+"), not a web page. Play streams it in the player.")
	f.Media = mediaType
	return textBrowserDocument{}, &brwFetchError{fail: f}
}

// brwAudioType reports a song's type: audio/*, but not an audio playlist
// (audio/x-mpegurl is HLS or M3U, which the video player reads).
func brwAudioType(mediaType string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	return strings.HasPrefix(mediaType, "audio/") && !strings.Contains(mediaType, "mpegurl")
}

// brwStreamReachable reports whether address can play now: online, or a
// server on the home network, which needs no internet.
func brwStreamReachable(address string) bool {
	return IsOnline() || mediaLANStreamHost(strings.ToLower(strings.TrimSpace(address)))
}

// brwOpenMediaLink plays target when it names a video, a song or a playlist,
// and reports whether it was one (offline it says so instead): a song in the
// music player, the rest in the video player, which reads files and HLS
// playlists itself. The page is remembered for the way back
// (brwRememberReturn), like a YouTube link's.
func brwOpenMediaLink(config *Config, target string) bool {
	if config == nil || !brwMediaAddress(target) {
		return false
	}
	if !brwStreamReachable(target) {
		ShowToast("You're offline — streams need an internet connection", ToastKindWarning)
		return true
	}
	brwRememberReturn(config)
	PlayActivateSound()
	if isAudioURL(target) {
		openAudioPlayer(config, target)
		return true
	}
	playStream(config, target)
	return true
}

// brwOpenMediaAddress is brwOpenAddress for such a file: a typed or pasted
// address, a bookmark or a link from another screen plays, and the address
// bar goes back to the page on screen (Reload would replay the file, Save keep
// it as a page). With no page on screen the reader says where it went.
func brwOpenMediaAddress(config *Config, variable, address string) bool {
	if config == nil || !brwMediaAddress(address) {
		return false
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	page := brwPageOnScreen(variable)
	config.Variables.Custom[textBrowserURLVariable] = page
	reachable := brwStreamReachable(address)
	brwOpenMediaLink(config, address)
	if page == "" {
		note := "That address is a video or a song: it opened in the player."
		if !reachable {
			note = "That address is a video or a song. Open it again when online to play it."
		}
		config.Variables.Custom[variable] = note
	}
	return true
}

// brwPlayStreamCard is Play on the card of an address that answered with a
// stream (brwStreamCard): a song in the music player, anything else — a film,
// an HLS or DASH playlist — in the video player, which reads them itself.
func brwPlayStreamCard(config *Config, f brwFailure) {
	address := strings.TrimSpace(f.URL)
	if config == nil || address == "" {
		return
	}
	if !brwStreamReachable(address) {
		PlayErrorSound()
		ShowToast("You're offline — streams need an internet connection", ToastKindWarning)
		return
	}
	brwRememberReturn(config)
	PlayActivateSound()
	if brwAudioType(f.Media) {
		openAudioPlayer(config, address)
		return
	}
	playStream(config, address)
}

// brwPageVideoCount is how many play cards the page on screen has (the Select
// menu's read-out).
func brwPageVideoCount(config *Config, elem Element) int {
	layout := browserPageLayout(config, elem)
	if layout == nil {
		return 0
	}
	n := 0
	for _, v := range layout.Videos {
		if !v.Invisible {
			n++
		}
	}
	return n
}

// brwPlayPageVideo is the Select menu's "Play video on this page": the page's
// first play card when the reader found one (a <video>, an embed), else the
// page itself through yt-dlp, whose site extractors and generic scan find the
// video a page's own scripts build. A YouTube page or card plays in Media.
func brwPlayPageVideo(config *Config, elem Element) {
	doc := browserDocumentFor(elem.Variable)
	page := ""
	if doc != nil {
		page = strings.TrimSpace(doc.URL)
	}
	if config == nil || page == "" {
		PlayNavSound()
		ShowToast("Open a page first", ToastKindInfo)
		return
	}
	if !brwStreamReachable(page) {
		PlayErrorSound()
		ShowToast("You're offline — videos need an internet connection", ToastKindWarning)
		return
	}
	if layout := browserPageLayout(config, elem); layout != nil {
		for _, v := range layout.Videos {
			if v.Invisible {
				continue
			}
			target, ok := browserResolveLink(page, v.Src)
			if !ok {
				continue
			}
			if brwOpenYouTubeLink(config, target, v.Title) {
				return
			}
			brwRememberReturn(config)
			PlayActivateSound()
			playSmartURLTitled(config, target, brwVideoTitle(v.Title))
			return
		}
	}
	if brwOpenYouTubeLink(config, page, doc.Title) {
		return
	}
	logInfo("browser", "play video on this page: handing %s to yt-dlp", sanitizeLogURL(page))
	brwRememberReturn(config)
	PlayActivateSound()
	ShowToast("Looking for the video on this page…", ToastKindInfo)
	playSmartURLTitled(config, page, brwVideoTitle(doc.Title))
}
