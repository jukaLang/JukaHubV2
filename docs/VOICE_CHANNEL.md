# Voice clips and Voice Channels

Chat supports two kinds of voice:

- **Voice clips:** press the microphone beside the paperclip (or **V** on a
  keyboard) to record. Press it again to attach the clip, add a caption if you
  like, then press **Send**. **B** discards an unfinished recording. Clips are
  WAV files up to two minutes long. They are saved under `Downloads/Voice Clips`
  and travel through the tab's existing file flow: Discord attachments in the
  community tab, a recording Juka can hear in Juka AI, or a local reference in
  Saved. Tap a sent clip's **Play** card to hear it.
- **Voice Channels:** in the community tab, open **People**, mark people with
  **X** (or click the circle beside a name), then press **Y** or the **Voice Channel** button. A person's profile
  also has a **Voice Channel** action. JukaHub opens a live call and posts a
  Voice Channel invitation in the community conversation. On another device,
  select that message and choose **Join Voice**. The **Voice Channel** chip in
  Chat opens call controls and the participant list. **Mute** stops sending
  microphone audio; **Leave** ends the call. Returning to Chat keeps the call
  connected.

## Internet relay setup

Live voice runs inside JukaHub, but devices on different networks need a
reachable relay. This repository includes the relay service; it does not
include a hosted relay address. Run it on a public server behind an HTTPS
reverse proxy that supports WebSocket upgrades:

```sh
cd player
go build -o voice-relay ./cmd/voice-relay
JUKAHUB_VOICE_LISTEN=127.0.0.1:8790 ./voice-relay
```

Proxy `/voice/` to `127.0.0.1:8790`, with TLS and WebSocket `Upgrade` and
`Connection` headers. The address entered on each device is the **base** URL,
for example `wss://voice.example.com/voice`. In JukaHub, open **Chat → Voice
Channel → Voice relay** to save it. `variables.Custom.voiceRelayURL` holds that
setting; `JUKAHUB_VOICE_RELAY` overrides it for managed installs. A plain
`ws://` address is accepted only for localhost and private network hosts.

For example, point `voice.example.com` at the server and use this Caddy site
block; Caddy obtains the TLS certificate and handles the WebSocket upgrade:

```caddyfile
voice.example.com {
    reverse_proxy /voice/* 127.0.0.1:8790
}
```

The relay forwards 20 ms audio frames between at most eight participants in a
room. It does not record calls. Each room has a random 128-bit invitation URL;
the relay removes the room when the last participant leaves. The invitation is
posted to the community channel, so **anyone who can read that message can
join**, even if the message names selected people. Use a channel with the
right audience for the call. The relay can hear raw audio while forwarding it;
TLS protects it in transit to the relay.

If the relay is unavailable, JukaHub shows **Reconnecting** and retries. An
invitation is posted only after the creator connects successfully. Without a
configured and hosted relay, recorded clips still work; live Internet calls
cannot connect.
