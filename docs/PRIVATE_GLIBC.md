# Private C runtime on TrimUI

Stock TrimUI firmware provides glibc 2.33. GreenOvercast's ARM64 binary needs
at least glibc 2.38. JukaHub can download a private glibc 2.39 when
GreenOvercast is first launched on an older ARM64 TrimUI. It starts only the
GreenOvercast binary through the private loader. JukaHub, PortMaster's shell
script, firmware tools, and other games continue to use the firmware's own
libraries. Nothing in `/lib` is replaced.

The source is Ubuntu Noble's arm64
[`libc6_2.39-0ubuntu8.12_arm64.deb`](https://ports.ubuntu.com/ubuntu-ports/pool/main/g/glibc/libc6_2.39-0ubuntu8.12_arm64.deb).
JukaHub requires exactly 2,774,244 bytes and SHA-256
`2578bc27d4e077f5e93c283d2502bc693c516d9590a20a27840b548a4d436820`
before extracting. That digest was checked against Ubuntu's Noble Updates
arm64 package index. The package and extracted files live under
`<JukaHub>/data/private-glibc/` on the SD card. A partial or changed package is
not used. Downloads require Wi-Fi; JukaHub retries a failed or interrupted
installation on the next launch. JukaHub tries HTTPS first. Stock TrimUI's
certificate bundle could not validate the Ubuntu server in our device test,
so it can retry this public package over HTTP; the fixed size and SHA-256 are
required on either route. An HTTP download from the device matched the pinned
digest.

The private loader was tested on a stock TrimUI with Linux 4.9 and glibc 2.33.
It resolved the installed GreenOvercast binary and JukaHub, ran the firmware's
`curl`, and started GreenOvercast past its glibc requirement in a display-free
test. A full GreenOvercast sign-in and stream through a JukaHub build using this
code remain to be tested. GreenOvercast selected `cedar-h616` at startup on
the tested A133 TrimUI, but live playback is not verified. Use 720p at 30 fps
if playback stutters.

This is deliberately limited to GreenOvercast's known PortMaster launcher and
ARM64 TrimUI devices. Other ports and firmware retain their existing
compatibility checks. Updating the pinned package requires a new hash and a
device test; JukaHub does not fetch a moving "latest glibc" build.
