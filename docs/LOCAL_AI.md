# Offline AI options for the TrimUI Smart Pro

The Smart Pro has four Cortex-A53 cores and 1 GB of RAM. An offline feature
must share that memory with JukaHub, the firmware and any running game. These
are the practical layers, in the order to add and test them.

| Feature | What is available now | Next useful step |
|---|---|---|
| Device commands | Typed commands, saved voice shortcuts and the command parser work without Groq. Spoken input still needs recognition. | Keep commands usable from the keyboard and the phone's own speech keyboard. |
| Spoken answers | **Download offline voice** installs Piper and Kristin from Settings → Voice & AI. After one download, speaking needs no Groq key, Wi-Fi or daily allowance. | Measure first-word delay, peak memory and battery use on a Smart Pro. |
| Speech recognition | The handheld microphone currently sends clips to Groq Whisper. | Test [whisper.cpp tiny.en](https://github.com/ggml-org/whisper.cpp#memory-usage) separately: its model is 75 MiB and its published memory figure is about 273 MB before JukaHub and the firmware. Only add an on-device download after timing and memory tests. |
| Open-ended answers | Chat, the voice orb and vision currently use Groq. | Test a small quantized model, then make it an optional fallback for text-only questions. Keep images and long answers on the cloud. |

The first text model candidate is [Qwen2.5-0.5B-Instruct Q2_K](https://huggingface.co/Qwen/Qwen2.5-0.5B-Instruct-GGUF/blob/main/qwen2.5-0.5b-instruct-q2_k.gguf), a 415 MB GGUF file, with a CPU-only [llama.cpp](https://github.com/ggml-org/llama.cpp) runner built against the Smart Pro's older C library. The model file alone takes roughly 40% of the device's RAM. A useful JukaHub integration would therefore need a short context, capped replies, a free-memory check before launch, and unloading before a game starts. It should show that the answer came from a small local model, because its quality will differ from Groq. The binary and model need a hardware benchmark before an install button is offered; a desktop test cannot establish latency or safety under memory pressure on the handheld.

A local server on a PC or phone on the same Wi-Fi is a second option. It could
offer larger models without using the handheld's RAM and work when the internet
or Groq is unavailable, as long as the local network remains available. That
would require a separate provider setting and a clear indication of which
device receives each question. JukaHub does not currently connect to such a
server.

“Always” can mean the controls and spoken playback continue offline. New
open-ended answers still need either a working local model or a reachable
service. The optional Piper download solves playback only; it does not create
answers or transcribe speech.
