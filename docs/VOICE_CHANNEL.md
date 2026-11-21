# Voice: clips, the voice room and Voice Channels

Chat supports three kinds of voice:

- **The voice room (Voice tab):** Chat's last tab, **Voice**, is a live voice
  room for everyone in JukaHub. **Click or tap the Voice tab** and you are in
  the room. With a pad, **L1 / R1** (or Left / Right) step onto the Voice tab
  like any other tab; it opens with **Join the voice room** selected, so one
  **A** joins (passing over the tab never turns the microphone on). **L1 / R1**
  step back to the conversations. It is free and there is nothing to sign up
  for or host: see [Where the voice goes](#where-the-voice-goes).
- **Voice clips:** press the microphone beside the paperclip (or **V** on a
  keyboard) to record. Press it again to attach the clip, add a caption if you
  like, then press **Send**. **B** discards an unfinished recording. Clips are
  WAV files up to two minutes long. They are saved under `Downloads/Voice Clips`
  and travel through the tab's existing file flow: Discord attachments in the
  community tab, a recording Juka can hear in Juka AI, or a local reference in
  Saved. Tap a sent clip's **Play** card to hear it.
- **Private Voice Channels:** in the community tab, open **People**, mark
  people with **X** (or click the circle beside a name), then press **Y** or
  the **Voice Channel** button. A person's profile also has a **Voice Channel**
  action, and the Voice page has **Start a private Voice Channel**. JukaHub
  opens a separate room and posts its invitation in the community
  conversation. On another device, select that message and choose **Join
  Voice**.

In a call, the Voice page shows who is in the room (a green dot while someone
speaks, "muted" when they or the server muted them), **Mute** (you keep
listening; **Y** anywhere on the page does the same), **Leave**, and for the
voice room **Join from a phone or PC**, which shows the room's address as a QR
code to scan with a phone. Returning to Chat keeps the call connected; the
Voice tab has a green outline and the number of people while you are
connected. Clicking the Voice tab during a private call only shows that call.
Keyboard: **4** opens the Voice tab; Tab, Shift+Tab and 1–3 go back to the
conversations.

JukaHub only sends sound while you speak (a voice gate that follows the room's
background noise), as Mumble's own apps do. While others are talking it needs
a clearly louder voice to open, so their voices coming out of the speaker are
not sent back to them; headphones still give the best result.

## Where the voice goes

The voice room and private Voice Channels run on **Mumble**, the free,
open-source voice chat made for gamers. JukaHub connects straight to a public
Mumble server; no account, no relay and no sign-up are involved. The default is
GameMania's server, `mumble.gamemania.org:42069`, which is free for everyone
and lets any visitor who changes to **Private User-Channels** open a channel
inside it. The voice room is the channel **JukaHub** there: the first person to
join steps into Private User-Channels and opens it (as GameMania's instructions
tell people to), and the server removes it when the last one leaves. A private
Voice Channel is a channel with a random name (`JukaHub-…`) next to it.

Everything (the control messages and the sound, compressed with Opus at about
20 kbit/s) travels over one TLS connection to the server's port, so no UDP port
has to get through the router, and a Wi-Fi handheld uses far less data than the
self-hosted relay's raw audio.

Things to know about a public server:

- The rooms are open. Anyone on that server can see the channel list and walk
  into the JukaHub room, and anyone who can read a Voice Channel invitation in
  community Chat can join that channel. Treat it like a public voice channel.
- The server's owner runs it; JukaHub does not record calls. GameMania's
  rules apply (no offensive names, no spam, no music in public channels).
- Your Chat name is your name in the room. Mumble does not allow spaces, so
  they become `_`; if the name is taken, a number is added.
- Most Mumble servers use a certificate they made for themselves. JukaHub sends
  no password or account to the server, so such a certificate is accepted and
  noted in the log (`voice server … certificate not verified`); a certificate
  from a known authority is checked as usual.
- Like every Mumble app, JukaHub shows the server a certificate of its own:
  Mumble servers only let a guest open a room with one. It is made once per
  device (`voice/mumble-identity.pem` in JukaHub's data folder) and tells the
  servers you join that it is the same device as last time, nothing more.
- Servers differ in who may open a room. JukaHub first opens it from inside the
  parent channel. If the server says no, it asks for a lasting channel instead
  (voice room only, never for private Voice Channels), and if that is refused
  too, it registers the device's certificate under your Chat name once, as
  **Register** does in Mumble's apps, then reconnects and asks again. After
  that, the server knows you by that name, even if you rename yourself in Chat.
  The log's `chat` lines (`voice: asking to open …`, `voice server refused: …`)
  show each step and the server's permission bits.
- An invitation that names a different voice server is refused with a note to
  switch servers first, so everyone meets on the server chosen in settings.

## Joining from a phone or PC

Any Mumble app reaches the same rooms: **Mumla** (Android), **Mumble** (iOS,
Windows, macOS, Linux). Connect to `mumble.gamemania.org`, port `42069`, with
any name, then open **Private User-Channels → JukaHub**. In a call, **Join from
a phone or PC** (voice room) or **Copy invitation** (private channel) copies
the `mumble://…` address, which Mumble apps open directly.

## Voice settings

The bottom of the Voice page holds the settings; each opens a text prompt and
an empty answer restores the default.

- **Voice server** (`variables.Custom.voiceServer`, `host:port`): any Mumble
  server, including your own (`mumble-server` / Murmur on a home PC or a free
  VPS). The default is the GameMania server above. `JUKAHUB_VOICE_SERVER`
  overrides it for managed installs. Everyone who should meet must use the same
  server.
- **Voice room** (`variables.Custom.voiceRoom`): the channel path from the
  server's root, last part being the room, for example
  `Private User-Channels/JukaHub` (the default) or `Lobby/Our Group`. Private
  Voice Channels open next to it.
- **Own voice relay (optional)** (`variables.Custom.voiceRelayURL`): see below.
  When set, private Voice Channels use it instead of the voice server; the
  voice room always uses the voice server.

If the server refuses (it is full, needs a password, will not open the room by
any of the ways above, has no "Private User-Channels" channel), the Voice page
says so in one sentence and stops retrying; **Try again** retries, or change
the server or room. A dropped connection reconnects by itself, and a name already taken on
the server gets a number added.

## Self-hosted relay (optional)

The original Voice Channel transport is a small relay in this repository. It
carries raw 16 kHz audio (about 256 kbit/s per speaker) between at most eight
people per room and is only needed if you want private Voice Channels on a
server you run without Mumble. Run it on a public server behind an HTTPS
reverse proxy that supports WebSocket upgrades:

```sh
cd player
go build -o voice-relay ./cmd/voice-relay
JUKAHUB_VOICE_LISTEN=127.0.0.1:8790 ./voice-relay
```

Proxy `/voice/` to `127.0.0.1:8790`, with TLS and WebSocket `Upgrade` and
`Connection` headers. The address entered on each device is the **base** URL,
for example `wss://voice.example.com/voice`. In JukaHub, open **Chat → Voice →
Own voice relay** to save it. `variables.Custom.voiceRelayURL` holds that
setting; `JUKAHUB_VOICE_RELAY` overrides it for managed installs. A plain
`ws://` address is accepted only for localhost and private network hosts.

For example, point `voice.example.com` at the server and use this Caddy site
block; Caddy obtains the TLS certificate and handles the WebSocket upgrade:

```caddyfile
voice.example.com {
    reverse_proxy /voice/* 127.0.0.1:8790
}
```

The relay does not record calls. Each room has a random 128-bit invitation URL;
the relay removes the room when the last participant leaves. The invitation is
posted to the community channel, so **anyone who can read that message can
join**, even if the message names selected people. The relay can hear raw
audio while forwarding it; TLS protects it in transit to the relay.

## Building

The Opus codec is the pure-Go `github.com/thesyncim/gopus` (no cgo, nothing to
install on the TrimUI or Windows). After pulling this change, run
`player\update-deps.bat` once so `go.sum` and `player/vendor/` include it.
