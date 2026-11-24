# Movies & TV

**Media → Movies & TV** (the second chip in Media's strip) streams films and
shows from the places a handheld can reach legitimately: your own Jellyfin
server, the media servers on your home network, and the public-domain classics
of the Internet Archive. It is laid out like Library: rows of cards, and a card
that stands for a list (a show, a server's folder, a whole collection) opens it
one level deeper. **B** goes back up a level, **Y** opens a card's details
(resume, play from the start, Up Next, Favorites), **R2** loads more of a long
collection.

| Row | Where it comes from | Needs |
|-----|---------------------|-------|
| Continue watching, Recently added, Movies, TV shows | Your Jellyfin server | Jellyfin signed in; home network (or Tailscale) |
| On your network | Media servers found on the home network (DLNA / UPnP AV) | Wi-Fi on the same network |
| Free classic films, Sci-fi & horror classics, Silent films, Classic cartoons | The Internet Archive's curated film collections (public domain) | The internet |
| Your own media server | A **Connect Jellyfin** card while no server is set up | Your phone |

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

## Files

`player/media_movies.go` (the tab), `player/dlna.go` (search, descriptions,
Browse, DIDL-Lite), `player/freefilms.go` (the Archive's search and metadata
APIs and the proxy routes), `player/musicsrc_jellyfin.go` and
`player/musicproxy.go` (Jellyfin and the loopback proxy).

## Related

- [Music servers](MUSIC_SERVERS.md) — signing in to Jellyfin
- [Android engine](ANDROID.md) — what the APK Store can and cannot run
