# Music servers and scrobbling

The music player's iPod layout ([MUSIC_PLAYER.md](MUSIC_PLAYER.md#ipod-layout))
browses your own music servers as well as the SD card, and sends what you
listen to to Last.fm and ListenBrainz.

## Setting a server up

The easy way is your phone: **Settings → Audio → Music servers & scrobbling**
(or Settings › Music Servers › Set Up From Phone on the iPod screen) shows a
code; scan it and the Phone Companion's **Music servers** page opens, with a
card for each service. Nothing secret is sent back to the page.

| Service | What you enter | What the handheld keeps |
|---------|----------------|-------------------------|
| Navidrome (or any Subsonic server: Gonic, Airsonic…) | Address, user name, password | The password, encrypted: the Subsonic API signs each call with a token made from it (md5 of the password and a fresh salt), so the password itself never goes over the network |
| Jellyfin | Address, user name, password | Only the access token Jellyfin returns |
| ListenBrainz | Your user token (listenbrainz.org/settings) | The token, encrypted |
| Last.fm | Nothing: tap **Sign in to Last.fm** and allow JukaHub on the page that opens | The session key, encrypted |

Each is checked with the service before it is saved. Plain `http://` works
only for a server on your home network (or Tailscale), as for WebDAV; use
`https://` for one on the internet.

**Jellyfin without a password**: Settings › Music Servers › Jellyfin Quick
Connect. Type the server's address (the handheld looks for one on the home
network first), then enter the code it shows in any Jellyfin app you are signed
in to (Settings › Quick Connect). An administrator turns Quick Connect on in
the Jellyfin dashboard.

Everything is stored in `musicservers.json` in the data folder (mode 0600).
Settings › Source picks what the iPod screen browses: This device, Navidrome,
Jellyfin, or **All sources**, where a song or album found in two places is one
row and plays from the card first.

## Playing

Remote songs play through a small proxy inside JukaHub on
`127.0.0.1:47533`: ffmpeg is given that address, so no password or token ever
appears on a command line, in `errors.txt` or in a bug report. The proxy
downloads a song into the **song cache** (`music-cache/` in the data folder)
as fast as the network allows while it plays, so a seek, the next play or a
Wi-Fi drop mid-song costs nothing; a song skipped before half of it arrived is
not kept. Settings › Offline Cache sets the cache's size (Off, 512 MB to 8 GB,
2 GB by default) and clears it.

Navidrome's **Stream Quality** (Settings › Music Servers) caps a stream at 320,
192, 128 or 96 kbps (transcoded to MP3 by the server); Original plays the file
as it is.

Jellyfin videos open in the video player: a file the handheld decodes as it is
(8-bit H.264 or MPEG-4 up to 1080p, without DTS or TrueHD sound) streams as it
is; anything else (HEVC, AV1, VP9, 10-bit, 4K, DTS) is transcoded by the server
to H.264/AAC at 720p over HLS, so a seek costs a segment. Jellyfin is told
what plays and where you stopped, so Continue Watching and your other Jellyfin
apps pick up from there.

## Download Offline

The context menu (X) of a server's song, album, artist, playlist, film or
episode has **Download Offline**. Songs go to
`Music/Navidrome/<Artist>/<Album>/NN - Title.ext` (or `Music/Jellyfin/…`),
videos to `Videos/Jellyfin/`; the media library picks them up, and the folded
library plays them from the card from then on. A song already in the cache is
copied from it. Settings › Downloads shows the queue.

## Scrobbling

A song counts as listened to once it has played for half its length or four
minutes, whichever comes first, if it is longer than 30 seconds (Last.fm's
rule; ListenBrainz uses the same). Paused time and skipped-over parts do not
count. Every listen goes into `scrobbles.json` first and is sent from there,
so listens made offline are sent when the handheld is next online (up to
5,000 wait). Settings › Scrobbling shows how many wait.

It goes to Last.fm and ListenBrainz when you are signed in, and to Navidrome's
own play counts when **Navidrome Scrobbling** is on. Jellyfin counts plays from
the playback reports. "Now playing" is sent to each service as a song starts.

### Last.fm API account

Last.fm needs an API account per app. A release built with the repository
secrets `JUKAHUB_LASTFM_API_KEY` and `JUKAHUB_LASTFM_SECRET` (the same
mechanism as the Google and ScreenScraper keys: `build.bat`, `release.sh`,
`.github/scripts/trimui-build.sh` and the Windows job pass them as
`-X main.lastfmAPIKey=… -X main.lastfmAPISecret=…`) signs in with one tap.
A build without them asks for your own: make a free API account at
[last.fm/api/account/create](https://www.last.fm/api/account/create) (any
name, no callback URL) and paste its key and shared secret on the phone page's
Last.fm card. The environment variables `JUKAHUB_LASTFM_API_KEY` and
`JUKAHUB_LASTFM_SECRET` override both.
