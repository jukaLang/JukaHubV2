# Offline AI on the TrimUI Smart Pro

The original Smart Pro can hear voice commands without a Groq key. Its four
Cortex-A53 CPU cores run a compact Vosk recognizer for English. A
multilingual Whisper model handles other languages and a Vosk startup failure.
Both come in a roughly 120 MB package that JukaHub downloads by itself after
ffmpeg (it is not part of the app download; see [VOICE.md](VOICE.md)).
**Settings → Voice & AI → Speech recognition → Automatic** uses Groq when a
key and connection are available, then falls back to the local recognizer.
**Offline on device** keeps recognition local all the time. On a Smart Pro,
Vosk recognized a three-second English question in about six seconds; Whisper
needed about 30 seconds on the same clip. Provisional Chat words and final
voice typing use the fast English path. The app logs recognition time without
logging speech.

The original Smart Pro's PowerVR GE8300 graphics chip has no usable OpenCL or
Vulkan compute runtime on the supported firmware. Speech and the optional
text model therefore run on the CPU. The model loads while you are still
speaking and stays loaded for about a minute after the last answer. It is
unloaded at once when a game starts, or sooner if other apps need the memory.

## Install offline answers

In **Settings → Voice & AI**, press **Offline answers**. JukaHub downloads and
verifies an optional roughly 240 MB package on the SD card, then shows
**Ready**. The first installation needs Wi-Fi and sufficient card space; after
that, answers work without Wi-Fi or a Groq key. The download can resume after
a dropped connection. Groq remains the first choice when a key and connection
are available; the installed model answers if Groq fails or is unavailable.

Press **Menu** and ask something that is not a device command, such as “What
is a comet?” Known commands still use JukaHub's normal command parser. If an
unknown action is spoken, the bubble says that the command was not run and
offers an answer or suggestion. The model cannot open apps or change settings.
Offline answers appear word by word in the bubble and are spoken with Juka's selected
voice. **Download offline voice** installs the separate Piper voice when
spoken playback is needed without Groq. The voice also loads while you speak
and stays loaded between lines, so Juka starts talking as soon as the first
sentence exists. Short command replies, such as "Opening YouTube", use it
too.

## How it stays fast

On a Smart Pro without Groq, pressing **Menu** starts three things while
you talk:

- the CPU runs at full speed for the question, the answer and the spoken
  line, then returns to its usual setting. It returns at once when a game
  starts. If the hub stops unexpectedly, the next start restores the
  original CPU setting. The boost uses the game launch CPU setting; when
  launch profiles are off in settings, there is no boost.
- the offline model loads in the background (`llama-server`, reachable only
  from the handheld itself on a random port with a random key). It reads
  Juka's fixed instructions once, so each question only adds its own words.
  Words appear in the bubble as they are written, and generation stops at
  the first full sentence.
- the Piper voice loads, if Juka will speak with it.

Memory comes first on a 1 GB handheld. Speech recognition keeps about
150 MB free while you talk. The model preloads only with about 480 MB
available; otherwise it loads after your words are recognized, if about
360 MB is available. Piper preloads with about 120 MB more. If memory is
needed elsewhere, the idle model and voice close. If the system runs out of
memory, it stops the model before JukaHub.

The package contains
[SmolLM2-360M-Instruct Q4_0](https://huggingface.co/bartowski/SmolLM2-360M-Instruct-GGUF),
a 229,733,280-byte quantized GGUF model, and a CPU-only
[llama.cpp](https://github.com/ggml-org/llama.cpp) runner built against an old
enough Linux C library for the handheld. Both sources and model files are
pinned by revision and SHA-256 in `player/native/build-llm.sh`; the release
package has a checksum for every file. The installer checks the release asset,
the package checksums, and whether both runners start on the device.
Inference uses a 512-token context, three threads, and up to 32 new tokens.
The model stays in memory while loaded. Close a running game if the memory
check asks for space.

Packages made before `llama-server` was added still work. They load the
model for each answer with `llama-cli`, which is slower. After an offline
answer on Wi-Fi, JukaHub updates the package once if a newer build is
released. You can also update it from **Offline answers** (**Update**) in
Settings. The current package keeps working until the new one has been
verified. The small model is slower and less capable than Groq, and
current facts may be unknown to it. Pictures, video, and Chat's Juka AI tab
still use Groq.

The optional model package is `JukaHub-OfflineAI-linux-arm64.zip` on JukaHub
releases. JukaHub stores an installed copy in its data folder as `juka-llm/`.
Once installed, it can be used entirely offline.
