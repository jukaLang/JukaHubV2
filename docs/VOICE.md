# Voice command

Say what to open, and JukaHub opens it: a screen, an app, a game, a settings
page or a song. It also does the small things you would otherwise dig for:
the volume, the brightness, a screenshot, Wi-Fi and Bluetooth, the music. And
ask it anything else: a question that names nothing on the handheld goes to
Juka, JukaHub's assistant. Juka can also answer out loud, learn words of your
own and, while you play, help with the game in front of you.

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

## Juka's voice

Juka says its answers out loud: a question asked on the orb, and the
questions about the handheld itself — the time, the date, the battery, "help" —
on the orb and on the Voice command page (a command sent from your phone is
answered on the phone). A command that does something (the volume, Wi-Fi, the
music) is not read back; its chime and the bubble say it. The bubble stays up
while Juka talks; any button, **B**, or **Menu** to ask again stops the voice
at once.

**Settings → Voice & AI → Juka's voice** is *Voice assistant* by default;
*Voice assistant and Chat* also reads a reply that arrives in Chat's **Juka
AI** tab while you are looking at it (leaving the tab stops it); *Off* keeps
every answer on screen. **Voice** picks one of Groq's six voices (Hannah,
Autumn, Diana, Austin, Daniel, Troy) or, where the device has one, its own.
**Hear Juka** says a short line in the chosen voice — or, when it can't, why.

Groq's voice is free but small: about a hundred answers and a few minutes of
speech a day. Only the first 600 characters of an answer are spoken ("The rest
is on the screen."), in sentence-sized pieces so the first plays while the
next is fetched; when the day's allowance is used up, Juka says so once and
rests. The first time, Groq may ask the account's owner to accept the voice
model's terms: Juka says so once and **Turn on Groq's voice** appears under
Hear Juka — on a PC it opens Groq's page in your browser, on the handheld it
shows a QR code for your phone. Accept, then press **Hear Juka**.

When Groq's voice can't speak (no key, no internet, the allowance, the terms),
a PC speaks with Windows' own voice, offline. On a TrimUI, **Download offline
voice** in **Settings → Voice & AI** installs Piper's Kristin voice to the SD
card; the row then selects **This device** as Juka's voice. It reads text
without Wi-Fi, a Groq key or a daily speech limit. The download is about 90 MB
and uses about 105 MB on the card. **Hear Juka** checks that it can speak.
Piper is also used as a fallback if you later choose a Groq voice and Groq
cannot speak. The download is optional, checksum verified and can resume after
a dropped connection. The voice files live in `Apps/JukaHub/required/juka-piper/`.
The [Piper engine](https://github.com/rhasspy/piper) is MIT licensed; the
[Kristin voice card](https://huggingface.co/rhasspy/piper-voices/blob/main/en/en_US/kristin/medium/MODEL_CARD)
identifies public-domain LibriVox recordings as its training data.

Other Linux devices may also use espeak-ng, espeak, flite, pico2wave or Piper
(with a voice) in JukaHub's `required/` folder or on its PATH. Music and a
video play softer while Juka talks. Quiet Mode, a ringing alarm and starting a
game, a video or music silence the voice; the Interface sounds switch does
not, and the system volume sets how loud it is. The new download has been
checked with Piper on a PC, but needs its first Smart Pro playback test.

Offline speech reads an answer that is already available. Asking a new open
question, describing a picture, transcribing the handheld microphone and
voice typing still use Groq. The built-in commands and typed commands work
without Groq. An on-device language model would need a separate runtime and
memory test; see [Offline AI options](LOCAL_AI.md).

## Three ways to say it

**The handheld's microphone.** Press **A** (or **Menu** for the orb) and
speak. The first 0.3 seconds after the start chime are ignored, so the chime
itself never counts as speech. Listening stops by itself about a second after
you stop talking (at most 8 seconds); the clip is turned into words by Groq's
Whisper — the same service voice typing on the keyboard uses — and the command
runs. It needs the Groq API key from **Settings → General → Groq API key**
(its **Paste it from your phone** shows a code that opens the companion page
at its **Groq API key** card) and an internet
connection. Along with the clip, Groq receives a short list of the names on
this handheld (screen, app and game titles) so that "JukaLand" or a game's
title comes back spelled right. Nothing is recorded until you ask, the
microphone closes as soon as the clip ends or the page is left (or something
else covers it — the Start menu, Quick Settings, a panel), and neither the
audio nor the words are saved or logged.

Just after the handheld wakes from sleep — its firmware turns Wi-Fi off while
it sleeps — the orb and voice typing work while Wi-Fi reconnects: the clip
waits up to 15 seconds for the network, and the bubble or the keyboard's strip
says **Reconnecting to Wi-Fi…** meanwhile.

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
| "open the emulators", "RetroArch menu", "open PPSSPP", "GBA emulator", "Dolphin" | the Emulators page, or the emulator itself started without a game, on its own menu (RetroArch, a PC emulator, JukaHub's PPSSPP); other tiles open the Emulators page with that tile selected; nothing is installed by voice |
| "control profiles", "update emulators", "core builds", "BIOS doctor", "RetroAchievements" | those pages |
| "open voice shortcuts" | your own words (below) |

"Hey Juka", "please", "can you" and a trailing full stop are ignored; numbers
can be words ("seventy five"). "Hey Juka" on its own, and what speech
recognition writes for a breath or a near-silent clip ("you", "thank you",
"bye"), answer "Didn't catch a command" instead of opening something.
Commands are English for now.

## Your own words

**Settings → Voice & AI → Voice shortcuts** teaches the voice assistant words
of your own (the Voice command page's **Shortcuts** button and "open voice
shortcuts" open the same page). **Teach a word**, then: say it — the way you
will say it later, so speech recognition spells it the same way next time — or
type it; pick what it opens — a screen, a setting, an app, a game, a file, an
emulator, **A voice command…** ("brightness 10", "turn off Wi-Fi": the page
shows what it will do) or **A web page…**; check it and **Save**. On the list,
**A** changes a word, **X** deletes it and **Y** tries it.

A taught word comes before the built-in commands — the page warns when a word
is also one ("home", "back") — except "cancel" and "help". It also works after
"open" or "play" and with "the" or "menu" around it, and a long phrase heard a
letter off still counts when no other word is that close. Up to 50 words of up
to 40 letters. A game or a file that has gone is said plainly ("“kart” was for
Mario Kart, which is not here any more") instead of opening something else.

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

## Asking about the game you're playing

**Settings → Voice & AI → Game assistant** (on by default) lets Juka know
which game you play. JukaHub notes the library game it launches; while it
runs, and for half an hour after it ends, every question to Juka — in Chat,
with the voice orb, about a picture — is told "the player is playing *Title*
(*System*)", so "this game" means that game. Games JukaHub did not start are
not named, and only the title and the system go to Groq, with your question.

On a TrimUI, hold **Select + R2** for a moment in a game. The game pauses (as
it does under a ringing alarm), its last picture stays on screen, dimmed, with
the game's name in the corner, and the orb listens: "Ask about *Title*". Stop
talking and Juka answers in a wide bubble and out loud — **Read answers aloud
in games** (on by default) speaks it even when Juka's voice is off, and
turning it off leaves the text alone. **A** (or R2) sends early or asks again;
**B**, **Menu** or pressing Select + R2 again (no hold needed this time) goes
back to the game. Over a game every sentence is a question — a command
such as "open YouTube" is not run, because it would leave the game paused
behind another screen. The game resumes by itself after three minutes, and an
alarm that rings meanwhile takes over.

**Juka can see the game screen** (off by default, shown once the assistant is
on) sends that paused picture with your question to Groq's vision model, so
Juka can read the level, the menu or the puzzle in front of you; if the screen
holds no picture JukaHub can read, the question goes as text. Nothing is
loaded while you play: the only thing running during a game is a small watcher
of the pad's buttons, and the screen, the microphone and the network are used
only when you hold the chord. On a PC the orb (**F4**) and Chat know the game
after you return to JukaHub; nothing is drawn over a game's own window. None
of this has run on a handheld yet.

## Pictures and videos

In Chat's **Juka AI** tab, attach a picture, a video or a recording (the
paperclip, or **X** in the composer) and ask about it — or send it alone and
Juka says what is in it. Pictures go to Groq's vision model at most 1024
pixels on their long side; a video is shown as three frames with the words of
its first ten minutes (Whisper, the service voice typing uses); a recording is
heard. Follow-up questions keep them until **New conversation**, the bubble
says **Watching the video…** while the handheld prepares one, and HEIC and
other formats JukaHub cannot open are refused when attached. Only what is sent
to Groq with your key leaves the device. Videos need ffmpeg, which installs
itself like the player's.

## Voice typing

Voice typing writes what you say into the on-screen keyboard's field (Groq's
Whisper, with the key from **Settings → General**). Press **R2** while the
keyboard is up — or the mic chip in its top row, **Menu** on a handheld,
**F4** on a keyboard — and press again to send; **B** cancels. R2 works in the
browser's address bar too, and with the pointer up over the page above the
keyboard L2 and R2 still scroll. **Settings → Voice & AI → Start voice typing
when the keyboard opens** makes a Chat message's keyboard listen as soon as it
opens, for people who text by voice; it stops on its own when nothing is said
for a few seconds, and password, key, address and search fields never listen.

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
* `player/tts_*.go` — Juka's voice: the answer made speakable, the Groq
  request, WAV decoding, playback and the duck, the device's own voice, the
  Settings rows.
* `player/voiceshortcuts.go`, `player/voiceshortcuts_ui.go` — taught words:
  storage, matching, what each does; the Voice shortcuts page.
* `player/voiceemu.go` — the emulators and their pages as voice targets.
* `player/gameassist.go`, `player/gameassist_linux.go` — the game assistant:
  what Juka knows, the Select + R2 watcher, the overlay over a paused game.
* `player/groqvision.go`, `player/vision_chat.go`, `player/vision_video.go` —
  pictures and videos for Juka.
* `player/vkvoice_button.go` — R2 on the keyboard, and voice typing that
  starts by itself.
* `player/voiceai_settings.go` — Settings → Voice & AI.

## Not yet

* Languages other than English (the parser's phrases are English; Whisper is
  asked for English).
* Offline speech recognition on the handheld: there is no small offline model
  in JukaHub, and Windows' built-in recogniser (System.Speech) is not used.
* Juka's voice in languages other than English (Groq's Orpheus and the
  downloadable Kristin voice are English).
* A wake word that is always listening: deliberately not — a handheld
  microphone open all the time costs battery and privacy.
* Proof on the hardware: the microphone fallbacks and the codec switches come
  from the Allwinner driver's control names, not from a Smart Pro on a desk;
  the first report from a handheld will say which device answered.
