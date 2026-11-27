# Movies & TV

**Media → Movies & TV** (the second chip in Media's strip) streams films and
shows from the places a handheld can reach legitimately: your own Jellyfin or
Plex server, the media servers on your home network, and the public-domain
classics of the Internet Archive. It is laid out like Library: rows of cards, and a card
that stands for a list (a show, a server's folder, a whole collection) opens it
one level deeper. **B** goes back up a level, **Y** opens a card's details
(resume, play from the start, Up Next, Favorites), **R2** loads more of a long
collection.

| Row | Where it comes from | Needs |
|-----|---------------------|-------|
| Continue watching, Recently added, Movies, TV shows | Your Jellyfin server | Jellyfin signed in; home network (or Tailscale) |
| Continue watching, Movies, TV shows | Your Plex server | Plex connected with plex.tv/link; home network (or Plex relay) |
| On your network | Media servers found on the home network (DLNA / UPnP AV) | Wi-Fi on the same network |
| Free classic films, Sci-fi & horror classics, Silent films, Classic cartoons | The Internet Archive's curated film collections (public domain) | The internet |
| Your own media server | **Connect Jellyfin** and **Connect Plex** cards while a server is not set up | Your phone |

## Other Videos

**Tabs Home → Entertainment → Other Videos** is a folder of site shortcuts and
two tools: **Type a video domain** and **Play a link now**. Pressing a site tile
opens a full screen, the way the YouTube tile opens the Media screen — not a
popup. That screen lists videos saved from the domain, favourites and watch
history; **A** plays the highlighted video, **Y** searches that site and **X**
pastes a link. The screen opens with trending videos from the site and shows
keyword results from the same yt-dlp extractor when you search; it has a search
bar, section chips and a card grid with real poster thumbnails. The posters are
fetched with the results into `<data>/site-thumbs` and cached; a site with no
poster just shows its art panel empty. YouTube is not repeated here because
Entertainment already has its tile. Two sites are built in because the bundled
yt-dlp handles them reliably: Bilibili and SoundCloud. They are labels, not a
restriction — any `http` or `https` link yt-dlp can read works, including sites
you type yourself or a plugin adds.

The queue resumes interrupted downloads, skips livestreams (there is no file to
save), sends a browser user agent to sites other than YouTube, and falls back
to a combined video file when a site offers no separate streams. A site that
needs a login still needs cookies configured in the yt-dlp extra arguments; a
failure appears on its Downloads row with a Retry action.

Playback starts faster on non-YouTube sites: only YouTube needs its three
player-client attempts, so a site link makes one neutral yt-dlp call. The first
few results are also resolved in the background while you browse, so Play
usually uses a cached stream URL instead of waiting for a cold helper. A typed
domain whose site offers its own search API reuses its anonymous token for half
an hour and fetches posters in parallel; the token is refreshed automatically
if it expires. Playback prefers a stream that carries both picture and sound,
so a site offering separate audio never plays silent.

The folder has two tools that use the on-screen keyboard: **Type a video
domain** saves a domain you enter in `jukauser.json`, and **Play a link now**
plays a pasted link through the same yt-dlp player YouTube uses. A typed domain
becomes a shortcut. There is no separate “any link” or site-search tile — those
actions are on the site screen. A typed domain can be removed under
**Settings → Media & playback → Remove a video domain**. For example, typing
a short-video host lets you search it and play or save its public links — no
site is named in the built-in list until you add it.

## Jellyfin

Sign in once and the server's films and shows appear here as well as in the
music player: **Connect Jellyfin** opens the phone page (the same one as
**Settings → Audio → Music servers & scrobbling**), or use Jellyfin Quick
Connect from the music player's iPod screen — see
[Music servers](MUSIC_SERVERS.md#setting-a-server-up). A show opens its seasons
(a show with one season opens its episodes directly), a season its episodes.

A file the handheld decodes as it is (8-bit H.264 or MPEG-4 up to 1080p, no DTS
or TrueHD sound) streams as it is; anything else (HEVC, AV1, VP9, 10-bit, 4K,
DTS) is transcoded by the server to H.264 and AAC at 720p. The server hears what
plays, so where you stopped shows up in every Jellyfin app, and Continue
watching here follows what you watched elsewhere.

## Plex

**Connect Plex** starts Plex's own PIN sign-in: the handheld shows a QR code
for **plex.tv/link** and a short code, and after you enter the code on a phone
or computer it finds the Plex servers on your account. It prefers a
home-network connection and falls back to a relay only when no direct address
answers. The token is encrypted in the handheld's server settings and is added
by the loopback proxy, so it never appears in a player address, an ffmpeg
command line or a log. **Try again** on the Plex row repeats sign-in or refresh
after a server error.

On Deck, Movies and TV shows appear as rows. A show opens its seasons (a show
with one season opens its episodes directly) and a season its episodes.

Plex's own playback decision uses the same rule as Jellyfin: an 8-bit H.264 or
MPEG-4 file up to 1080p with no DTS or TrueHD sound plays directly; anything
else goes through Plex's universal transcoder at 720p H.264/AAC. The transcode
playlist and every segment are rewritten through the loopback proxy, and the
player reports its timeline to Plex every 30 seconds and when it stops, so
Continue Watching keeps the place and a stopped transcode releases its slot.
If Plex cannot answer or a video has no playable file, the row shows **Try
again** or the player says why instead of doing nothing.

## Media servers on your network

The row lists every media server that answers a UPnP search on the home
network: most NAS boxes (Synology, QNAP, …), many routers with a USB disk,
Windows media streaming, MiniDLNA, Serviio, Universal Media Server, Emby, and
Plex when its DLNA server is on (Plex **Settings → DLNA → Enable the DLNA
server**). Nothing is set up on the handheld. A server opens its own folders;
videos play straight from the server, as they are, so the handheld's decoder
limits apply (a 4K or HEVC file may not play smoothly — Jellyfin's transcoding
is the answer for those). **Search again** looks once more, for a server that
was asleep.

Only the home network is spoken to: a server's address must be a home-network
address, every request goes through the same LAN-only connection as WebDAV,
and a video whose address points outside the home network is not listed.

## Free classic films

Four rows from the Internet Archive's curated collections — Feature Films,
Sci-Fi & Horror, Silent Films and classic cartoons — most watched first, with
**See all** paging through a whole collection (up to 600 films). These films are
in the public domain or were released by their owners; they stream from
archive.org with no account. The handheld picks the Archive's own H.264 MP4 of
a film when there is one (at most 720 lines, which it decodes comfortably),
else another MP4, Ogg video or the original. A film the Archive cannot serve
says so instead of opening the player.

Films and their posters go through JukaHub's loopback proxy, because the stock
firmware's ffmpeg has no https; resume points, Favorites, Up Next and the
player's queue (L2/R2) work as for any video.

## Android streaming apps

Testers asked whether an Android streaming APK (Colosseum, Stremio and the
like) can be installed through JukaHub. It cannot run: those are Java or Qt
apps built on Android's media framework, and the TrimUI handhelds run Linux,
not Android — AndroidRT runs native game code only ([ANDROID.md](ANDROID.md)).
The APK Store says so for such an APK and points here. Add-ons that stream
films from torrents or from sites without the rights to them are not part of
JukaHub.

## Streaming from the Browser

The Browser plays what a web page offers, as a phone's browser would (see
[Videos and yt-dlp playback](BROWSER.md#videos-and-yt-dlp-playback)): a link
straight to a video, a song or a live playlist (`.mp4`, `.webm`, `.mkv`,
`.m3u8`, `.mp3`, …) opens in the player, an address that answers with one gets
**Play** on its card, and **Select → Play video on this page** finds the video
a page's own player would show — through yt-dlp, which knows most video sites.
Free and public video works this way: the Internet Archive, PeerTube, Vimeo,
Dailymotion, many news sites, and live channels that publish an HLS address.
Services that protect their streams with DRM (Netflix, Disney+, Prime Video
and the like) cannot play on a Linux handheld, and the Browser has no torrent
or scraper add-ons either.

## Files

`player/media_movies.go` (the tab), `player/dlna.go` (search, descriptions,
Browse, DIDL-Lite), `player/freefilms.go` (the Archive's search and metadata
APIs and the proxy routes), `player/musicsrc_jellyfin.go`, `player/plex.go` and
`player/musicproxy.go` (Jellyfin, Plex and the loopback proxy),
`player/brw_stream.go` (the Browser's streams).

## Related

- [Music servers](MUSIC_SERVERS.md) — signing in to Jellyfin
- [Android engine](ANDROID.md) — what the APK Store can and cannot run
