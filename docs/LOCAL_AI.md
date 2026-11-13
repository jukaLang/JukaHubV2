# Offline AI on the TrimUI Smart Pro

The original Smart Pro can hear voice commands without a Groq key. Its four
Cortex-A53 CPU cores run the bundled multilingual Whisper tiny recognizer.
**Settings → Voice & AI → Speech recognition → Automatic** uses Groq when a
key and connection are available, then falls back to the local recognizer.
**Offline on device** keeps recognition local all the time. Short commands use
a smaller Whisper search and three CPU threads; longer voice typing keeps the
more careful search. The app logs recognition time, without logging speech,
to help measure the change on a handheld.

The original Smart Pro's PowerVR GE8300 graphics chip has no usable OpenCL or
Vulkan compute runtime on the supported firmware. Whisper and the optional
text model therefore run on the CPU. The model is unloaded when each answer
ends, leaving memory available for games.

## Install offline answers

In **Settings → Voice & AI**, press **Offline answers**. JukaHub downloads and
verifies an optional roughly 280 MB package on the SD card, then shows
**Ready**. The first installation needs Wi-Fi and sufficient card space; after
that, answers work without Wi-Fi or a Groq key. The download can resume after
a dropped connection. Groq remains the first choice when a key and connection
are available; the installed model answers if Groq fails or is unavailable.

Press **Menu** and ask something that is not a device command, such as “What
is a comet?” Known commands still use JukaHub's normal command parser. If an
unknown action is spoken, the bubble says that the command was not run and
offers an answer or suggestion. The model cannot open apps or change settings.
Offline answers are labelled in the bubble and spoken with Juka's selected
voice. **Download offline voice** installs the separate Piper voice when
spoken playback is needed without Groq.

The package contains
[SmolLM2-360M-Instruct Q4_K_M](https://huggingface.co/bartowski/SmolLM2-360M-Instruct-GGUF),
a 270,590,880-byte quantized GGUF model, and a CPU-only
[llama.cpp](https://github.com/ggml-org/llama.cpp) runner built against an old
enough Linux C library for the handheld. Both sources and model files are
pinned by revision and SHA-256 in `player/native/build-llm.sh`; the release
package has a checksum for every file. The installer checks the release asset,
the package checksums, and whether the runner starts on the device. Inference
uses a 512-token context and at most 80 new tokens, and needs at least 500 MB
of available RAM before it starts. Close a running game if the memory check
asks for space. The small model is slower and less capable than Groq, and
current facts may be unknown to it. Pictures, video, and Chat's Juka AI tab
still use Groq.

The optional model package is `JukaHub-OfflineAI-linux-arm64.zip` on JukaHub
releases. JukaHub stores an installed copy in its data folder as `juka-llm/`.
Once installed, it can be used entirely offline.
