# Resilience and hardening status

This doc tracks the concrete failure modes the current code is meant to handle better, and the verification status in this environment.

## What is improved

- Tool download/extraction paths now share one safe-zip path instead of several duplicated ones.
- Secret decryption helpers distinguish "not encrypted" from "decryption failed" instead of collapsing both into empty strings.
- Startup tool-health reporting is explicit about which tools are missing and that some features may degrade.
- Build bootstrap has better diagnostics and more defensive install/verification transitions.
- Tool downloads are restricted to an approved GitHub repo allowlist.
- Tool downloads enforce a size cap and refuse suspiciously small files.
- GitHub API auth header uses a current `Bearer` form and the accepted API media type.
- External app execution is now restricted to explicit console-driven runs instead of silent background execution.

## Validation in this environment

- `go build ./...` passes (SDL2 CGO subpackages report build-constraint exclusions, which is expected here).
- `go vet ./...` passes.
- `go test -c` can compile the test binary.

## Remaining concerns that still need a real machine

- Windows build/bootstrap behavior depends on MSYS2 installer switch acceptance, PATH timing, and AV/permissions/proxy conditions that cannot be proven from code alone here.
- Trimui Smart Pro gamepad behavior depends on the actual controller mapping shipped by the device firmware and SDL2 joystick/gamecontroller database in that environment.
- Any feature that relies on yt-dlp / ffplay / mpv depends on those tools being reachable and functional on the target machine.
- Runtime config-driven automation (for example external app triggers) is now more guarded, but the remaining trusted-config assumptions still need human review on the target device.
