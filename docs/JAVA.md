# Java programs

JukaHub runs Java programs — `.jar`, `.java` and `.class` files — on the
TrimUI handheld and on Windows and Linux PCs. It can install its own Java
(Eclipse Temurin from Adoptium) or use one the computer already has.

Open it from **Tools → Java** (or search Home for "Java").

Code: `player/javart.go` (which Java), `player/javart_install.go` (installing
Temurin), `player/javarun.go` (A on a file in Files), `player/javaterm.go`
(the Terminal), `player/javaui.go` (the Tools screen).

## Which Java

| | What it is | Size |
|---|---|---|
| **JRE** | Runs programs | about 50 MB to download (Java 25, handheld) |
| **JDK** | The JRE plus `javac`, `jshell`, `jar` and running a `.java` file from its source | about 200 MB |

Versions: **25** (the current long-term release, the default), 21, 17, 11 and
8. Pick the version and the kind on the Java screen (Left/Right on the row),
or type `java install 21 jdk` in the Terminal.

JukaHub keeps one Java of its own, in `java/` inside its data folder (on the
handheld that is on the SD card beside the app). A new version or kind
replaces the old one only after the new one has started here. When JukaHub
has not installed one, it uses the computer's: `JAVA_HOME`, the `PATH`,
`/usr/lib/jvm` on Linux, and on Windows the JavaSoft registry keys and the
usual folders (Eclipse Adoptium, Java, Microsoft, Zulu, BellSoft, Amazon
Corretto). JukaHub's own Java always comes first.

### How it is installed

* GitHub's release list of `adoptium/temurin<N>-binaries` is read newest
  first; drafts, pre-releases and early-access builds are skipped, and the
  first release that has both this computer's archive and its `.sha256.txt` is
  used (Adoptium publishes platform by platform, so the newest release may not
  have an ARM build yet).
* The archive is used only when its SHA-256 matches. It is unpacked into a
  stage folder with every path checked, sizes and file counts capped, and
  links turned into copies (the SD card is FAT).
* The new Java must answer `java -version` with the version asked for; a Java
  that cannot start here fails the install with its first line. Only then is
  the marker `.jukahub-java.json` written and the folder moved into place.
* Temurin's Linux builds need glibc 2.17, so they run on the TrimUI's stock
  firmware (glibc 2.33). Windows on ARM has Temurin JDKs for newer versions
  only; where there is no build, the screen says so.
* **Update** checks for a newer build of the same version and kind — by
  itself at most once a day, when the Java screen opens. **Remove** moves the
  folder aside first, so a half-deleted Java never looks ready.

## Running a program

Press **A** on the file in Files.

| File | What happens |
|---|---|
| `.jar` with a `Main-Class` | `java -jar` |
| `.jar` for phones (a `MIDlet`) | a J2ME game: see [J2ME.md](J2ME.md) — they go in `Roms/J2ME` and play from Games |
| `.jar` with neither | a library: there is nothing to start, and Files says so |
| `.java` | runs from its source (`java Hello.java`): needs a JDK of Java 11 or newer |
| `.class` | `java -cp <root> com.example.Hello`: the class names itself, and its folders must match its package |

The class file's version says which Java a program needs (a program built for
Java 21 does not run on Java 17). When the Java at hand is too old, missing,
or a JRE where a JDK is needed, the Java screen opens with an offer that says
why; after the install the program starts.

A file in **Downloads** may have come from the chat: it runs on a second
press, like a script there. The Y menu's **Edit** opens a `.java` file's text.

The **Java folder** is the place for your programs: `Java` at the top of the
SD card on the handheld, `Documents/JukaHub/Java` on a PC. "Run a Java
program" on the Java screen opens it in Files; the first time it gets
`Hello.java` (it asks for your name) and a short `README.txt`.

### Text programs: the Terminal

A program that prints text runs in the [Terminal](TERMINAL.md). While it runs,
what you type goes to it: Enter (Start on a pad) sends the line, Ctrl+D ends
its input, X / Ctrl+C stops it. Leaving the Terminal stops it.

### Programs with windows

A program that uses AWT, Swing, JavaFX or LWJGL (found by looking at its
classes, or a source file's imports) opens windows:

* **Windows**: it starts with `javaw`, on its own, and JukaHub stays open.
* **Linux PC** with a desktop (`DISPLAY` or `WAYLAND_DISPLAY`): it starts on
  its own the same way.
* **The handheld** has no desktop (no X11 or Wayland). Files says so; a second
  press runs it in the Terminal anyway with `java.awt.headless=true`, which
  works for programs that only touch a little AWT and fails honestly for real
  windowed ones.

## The Terminal

`java`, `javac`, `jshell`, `jar` and `javaw` are Terminal commands, plus
`java install [8|11|17|21|25] [jre|jdk]`, `java update`, `java remove` and
`java status`. See [TERMINAL.md](TERMINAL.md#java-and-input-to-running-programs).
The Command Deck has a Java category.

## Limits

* **Not Android apps.** Android apps are written in Java too, but they are
  built for Android: their code is DEX for Android's runtime (ART), and they
  need Android's framework. This desktop Java cannot run an `.apk`;
  [AndroidRT](ANDROID.md) runs native (NDK) Android games only.
* No windows on the handheld (see above). Minecraft Java Edition and other
  LWJGL games need a desktop with OpenGL.
* Memory: a JVM on a 1 GB handheld leaves less for everything else; Java
  picks a quarter of the memory as its largest heap by default.
* Programs run with your rights, like any program; JukaHub does not sandbox
  them. Run programs you trust.

## Status

Written 2026-11-19 without a Go toolchain: statically checked, not yet built,
and unverified on a device — in particular Temurin's aarch64 JRE starting from
the FAT SD card on the TrimUI, the source launcher's speed there, and typed
input reaching programs on Windows.
