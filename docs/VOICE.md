# Voice command

Say what to open, and JukaHub opens it: a screen, an app, a game, a settings
page or a song. It also does the small things you would otherwise dig for:
the volume, the brightness, a screenshot, Wi-Fi and Bluetooth, the music. And
ask it anything else: a question that names nothing on the handheld goes to
Juka, JukaHub's assistant.

## Opening it

| Where | How |
|-------|-----|
| Anywhere, on the handheld | **Menu** (the TrimUI button) opens the voice orb |
| Anywhere, on a keyboard | **F4** opens the voice orb (on the Voice command page, F4 starts or stops listening) |
| Start menu | **Tools → Voice command**, the full page |
| Home search | **Y** in the search sheet (**X** still edits the search) |
| Your phone | the **Voice** card on the phone companion page, or **Y** on the voice page for its QR code |

## The voice orb

Press **Menu** — or **F4** on a keyboard — and a glowing orb rises in the
bottom-right corner of whatever screen is up and says **Listening…**. Its
lights follow your voice. Say a command or a question:

* it stops listening by itself about a second after you stop talking (at most
  8 seconds), or press **Menu** again to send at once;
* **hold** Menu (or F4) and it listens for as long as you hold it (up to 8
  seconds) — let go to send, like a walkie-talkie;
* **B** (Esc) cancels at any point, before or after it understood you.

Menu is read the way the TrimUI's own launchers read it: a tap acts when the
button comes up, a hold opens the orb after about half a second, and a press
that another button joins is a chord, not a voice press — so the firmware's
**Menu + volume** (brightness) never opens the orb, and **Menu + Start** still
quits like **Select + Start**. A Bluetooth pad's PS or Xbox button is the
same Menu button on the handheld.

Then the orb says what it did, in a glass bubble beside it: "Volume 50%",
"Wi-Fi off", "It is 6:15 PM". A command that opens something says so
("Opening YouTube") and waits a moment, with a ring around the orb counting
down, so **B** can still stop a misheard one (**A** opens it at once). When a
name could mean several things the bubble lists up to four; the D-pad and
**A** pick one. When nothing matches, **A** opens the Home search sheet with
your words. A question — "what is the capital of France", "how far is the
moon", anything of four words or more that names nothing here — goes to Juka,
the same Groq model as the Chat tab's **Juka AI**, and the answer streams into
the bubble in a sentence or two.

An answer lingers for as long as it takes to read and does not hold the
controls: any button puts it away and does its own job, **Menu** asks again.
Music and a playing video pause while the orb listens (the speaker sits next
to the microphone; a video in a Watch Party keeps playing for the room) and
play on as soon as the clip ends, before the command runs, so "pause" and
"next song" act on the music that was playing.

With the on-screen keyboard up, **Menu** (or F4) is voice typing into the
field instead (not in the browser's address bar, which has its own keyboard),
and on the Voice command page it is that page's own microphone. Over a modal panel, the first-run tour, the arcade, JukaLand or a
running game, Menu keeps its old job (Back). **Settings → Controls → Menu
button: voice** gives Menu back to Back for good; on a keyboard F4 stays the
voice key.

On the page, **A** listens, **X** types a command instead, **Y** shows the
phone QR code and **B** goes back. While it listens, **A** stops early and
**B** cancels.

## Three ways to say it

**The handheld's microphone.** Press **A** (or **Menu** for the orb) and
speak. The first 0.3 seconds after the start chime are ignored, so the chime
itself never counts as speech. Listening stops by itself about a second after
you stop talking (at most 8 seconds); the clip is turned into words by Groq's
Whisper — the same service voice typing on the keyboard uses — and the command
runs. It needs the Groq API key from **Settings → General** (or pasted on
the phone: the companion page has a **Groq API key** card) and an internet
connection. Along with the clip, Groq receives a short list of the names on
this handheld (screen, app and game titles) so that "JukaLand" or a game's
title comes back spelled right. Nothing is recorded until you ask, the
microphone closes as soon as the clip ends or the page is left (or something
else covers it — the Start menu, Quick Settings, a panel), and neither the
audio nor the words are saved or logged.

**Finding the microphone.** The TrimUI Smart Pro has a built-in mono
microphone, but SDL's default capture device — ALSA's "default" PCM, which a
firmware may set up for playback only — would not open on it ("No microphone
found").
The orb, this page and voice typing now share one way in (`voicemic.go`),
which tries in turn: SDL's default device (what a PC uses), every capture
device the kernel lists in `/proc/asound/pcm` through ALSA's plug layer
(`plughw:0,0`, which converts the codec's own rate and channels), every
capture device SDL lists, and finally `arecord` on the same devices. A device
that opens but stays silent for 1.2 seconds is swapped for the next one, and
the one that gave sound is tried first the next time (on a PC only a device
that fails or gives no data at all is passed over: silence there is a muted
microphone, and the next device could be a webcam). The Smart Pro's and the
Brick's codec ships with its microphone inputs switched off, so before the
first recording JukaHub switches on the capture-side microphone switches
("ADCL Input MIC1 Boost Switch", "ADCR Input MIC2 Boost Switch" — never a
playback path) and lifts a microphone or ADC level whose bottom is mute when it
sits there; all of it is put back when JukaHub quits. If the orb still says **No microphone
found** or **The microphone gives no sound**, the log (category `mic`) lists
what was tried and why each one failed — the line a bug report needs — and the
phone (below) works without the handheld's microphone.

**Your phone.** The companion's `/voice` page (scan the QR code with the phone
on the same Wi-Fi) is one text field. Tap it, tap the microphone on the phone's
keyboard (Gboard, the iPhone keyboard, Samsung's) and speak: the keyboard turns
the speech into words on the phone, so no Groq key is needed and it works even
when the handheld's microphone does not. The handheld runs the command at once
and the phone shows the answer, or a list to pick from. A page on a plain-HTTP
address cannot use the phone's microphone itself (browsers only allow that on
HTTPS), which is why the keyboard does the listening. While a game or an app
has the screen, the handheld answers "busy — nothing ran", and that command
is dropped: it never fires later. A command the handheld has already started
(a slow Wi-Fi switch, say) is waited for a little longer; if there is still no
answer, the phone says it may have run. **Try again** resends the very same
request, which the handheld never runs twice — it answers from memory
instead.

**Typing.** **X** opens the keyboard; the same commands work typed, so the
page doubles as a command line.

## What you can say

| Say | It does |
|-----|---------|
| "open YouTube", "launch RetroArch", "go to the browser", "show me the trophies" | opens the screen, app or game with that name |
| "play Mario Kart", "play Kirby's Dream Land" | starts the game (or plays the song) |
| "YouTube" | a name on its own opens it too |
| "Bluetooth settings", "open the display settings" | the settings row rather than the screen |
| "volume 50", "set the volume to seventy percent", "louder", "turn it down", "mute", "volume off", "unmute" | the system volume ("unmute" returns to the last audible level) |
| "brightness 70", "brighter", "dim the screen" | the screen brightness |
| "take a screenshot" | captures the screen you came from |
| "turn off Wi-Fi", "Bluetooth on", "turn on airplane mode" | Quick Settings' own switches; switching a radio **off** (or airplane mode on) waits the same cancellable moment as opening something, because afterwards voice has no internet to switch it back |
| "pause", "resume", "next song", "previous song", "play some music" | the music player |
| "go home", "open the home screen", "go back", "quick settings", "turn off the screen" | moves around |
| "search the web for cat videos", "google pancakes" | the Browser, with your search engine |
| "search for mario", "find my notes" | the Home search sheet, with the words typed in |
| "what time is it", "what's the date", "how much battery is left" | answers on the page |
| "quit JukaHub" | the usual quit confirmation; on a setup without the Exit screen, a one-row list whose press is the confirmation |

"Hey Juka", "please", "can you" and a trailing full stop are ignored; numbers
can be words ("seventy five"). "Hey Juka" on its own, and what speech
recognition writes for a breath or a near-silent clip ("you", "thank you",
"bye"), answer "Didn't catch a command" instead of opening something.
Commands are English for now.

## How it decides

A name is looked up in exactly what Home search finds — every screen and
settings row with the words people use for them, the Apps launcher, recently
played games and the ROM library, the indexed media and book folders and the
Continue timeline — with the same ranking. Three things change because the
words are spoken:

* a settings row loses to a screen with the same name unless you said
  "settings";
* "play" prefers games, music and what you were watching;
* a misheard name still gets somewhere: a title a letter or two off is
  offered in the list. It opens by itself only when it is at least 88% the
  same — in practice one letter off a title of nine letters or more — and no
  other title is close;
* a name of three letters or fewer opens by itself only on a whole title, a
  whole title word or an exact synonym ("tv" opens Live TV), never on the
  start of a longer word ("the" does not open Themes);
* JukaHub's own applets (JukaLand, the Browser, Trophies, PS Remote Play) are
  in the Apps list and are screens too; they count once.

A match opens on its own only when it is clearly the one meant; then the page
shows "Opening …" for a moment, so **B** can still stop a misheard command
(**A** goes at once). When a name could mean several things — two games
called "Super Mario …" — the page lists them. When nothing matches, it offers
**Search for it** (the Home search sheet with your words) and, online,
**Search the web**. **B** on the screen a command opened goes back to where you
were before you spoke, not to the voice page.

## Files

* `player/voiceorb.go` — the orb: the Menu button and F4, listening and
  push-to-talk, the bubble, the choices, Juka's answers, the drawing.
* `player/voicemic.go` — the microphone the orb, the page and voice typing
  share: the devices it tries, the silence check, the codec's microphone
  switches, the channel a mono microphone lands on.
* `player/voicecmd.go` — the page: listening, the Whisper request, the
  confirm pause, the choices, the phone QR sheet.
* `player/voicecmd_intent.go` — the parser: what a sentence asks for (pure).
* `player/voicecmd_resolve.go` — which target a name means (pure ranking).
* `player/voicecmd_exec.go` — what each command does, through the existing
  controls.
* `player/voicecmd_phone.go` — the phone page and `/api/voice`.

## Not yet

* Languages other than English (the parser's phrases are English; Whisper is
  asked for English).
* Offline speech recognition on the handheld: there is no small offline model
  in JukaHub, and Windows' built-in recogniser (System.Speech) is not used.
* A wake word that is always listening: deliberately not — a handheld
  microphone open all the time costs battery and privacy.
* Proof on the hardware: the microphone fallbacks and the codec switches come
  from the Allwinner driver's control names, not from a Smart Pro on a desk;
  the first report from a handheld will say which device answered.
