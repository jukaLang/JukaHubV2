# `player/` — the Go application

This directory is the whole runtime: SDL2 initialisation, the scene renderer,
the focus engine, config loading, media, the package manager and the plugin
runner. The web builder that generates `jukaconfig.json` lives one level up
(`index.html`, `style.css`, `script.js`).

| I want to… | Read |
|------------|------|
| build or package it | [../docs/BUILDING.md](../docs/BUILDING.md) |
| change the interface the config describes | [../docs/CONFIGURATION.md](../docs/CONFIGURATION.md) |
| understand the architecture and design tokens | [../docs/DEVELOPMENT.md](../docs/DEVELOPMENT.md) |
| add a test or open a pull request | [../CONTRIBUTING.md](../CONTRIBUTING.md) |
| work on packages or plugins | [../docs/PACKAGES.md](../docs/PACKAGES.md), [../docs/PLUGINS.md](../docs/PLUGINS.md) |
| handle secrets safely | [SECURITY.md](SECURITY.md) |
| build the TrimUI SD-card package | [trimui/README.md](trimui/README.md) |

Quick checks, from this directory:

```bash
go build ./...
go vet ./...
go test ./...
```

Note that `go test ./...` may need the SDL2 development libraries on the
`CGO_CFLAGS` / `CGO_LDFLAGS` include and library paths; `build.bat` sets them up
on Windows. See [../docs/BUILDING.md](../docs/BUILDING.md).
