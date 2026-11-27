package main

// privacy.go — the Privacy & data page's inventory of everything that uses the
// network, and the one-press export of the user's own files.
//
// The rows name every part of JukaHub that talks to another machine, what it
// sends and where it goes, and whether it is on now. The list is deliberately
// in Settings → Privacy & data, next to the browser sign-in and diagnostics
// switches it describes; it is a read-out, not another set of switches.
//
// "Export my data" writes the same archive the zero-config backup writes
// (bkup.go), with every category, whole plugin folders and the game saves: the
// user's settings, notes, library stores, themes, input profiles, packs and
// saves in one zip. ROMs, media and derived caches stay out, as they do from a
// backup. The write runs off the render thread and reports where it landed.

import (
	"context"
	"path/filepath"
	"time"
)

// privacyNetworkFeature is one network-using part of JukaHub.
type privacyNetworkFeature struct {
	ID    string
	Label string
	// Sends says what leaves the device; Where says which service receives it.
	Sends string
	Where string
	// Status answers whether it is on now. nil means "only when you use it".
	Status func(*Config) string
}

// privacyNetworkFeatures is the inventory. A feature that talks to more than
// one service is grouped by what the user asked for (art, sync, media), so the
// page stays readable while nothing is left out.
func privacyNetworkFeatures() []privacyNetworkFeature {
	return []privacyNetworkFeature{
		{
			ID: "updates", Label: "JukaHub updates and Patch",
			Sends:  "The app version, channel and device facts; downloads are checked against published signatures and digests",
			Where:  "GitHub releases and the JukaHub Patch repository",
			Status: func(*Config) string { return "On" },
		},
		{
			ID: "downloads", Label: "Emulator, core and tool downloads",
			Sends: "The release URL and a user agent; no account or library data",
			Where: "The emulator projects' GitHub releases and the libretro buildbot",
		},
		{
			ID: "catalog", Label: "Homebrew and package catalogues",
			Sends: "The catalogue request and the app version; installs download the package itself",
			Where: "The JukaHub catalogue, itch.io and the configured package index",
		},
		{
			ID: "artwork", Label: "Box art and game details",
			Sends: "Game titles, file names and ROM header hashes, not the ROMs",
			Where: "The configured art and metadata services (ScreenScraper, LaunchBox, SteamGridDB)",
		},
		{
			ID: "weather", Label: "Weather",
			Sends:  "A coarse place name or coordinates, never the device's exact location",
			Where:  "The weather service",
			Status: func(c *Config) string { return privacyOnOff(weatherEnabledFor(c)) },
		},
		{
			ID: "browser", Label: "Browser",
			Sends: "The addresses, searches and form contents you open, and the sites' cookies where you allow them",
			Where: "The sites you visit",
		},
		{
			ID: "media", Label: "Music, radio, podcasts and streaming",
			Sends: "Server sign-ins and the track, station, episode or video you ask for",
			Where: "Your music servers, Radio Browser, podcast directories and the streaming sites yt-dlp reads",
		},
		{
			ID: "cloud", Label: "Cloud save sync",
			Sends: "Encrypted save archives and file names to the cloud you connected",
			Where: "Google Drive, WebDAV, Nextcloud or your NAS",
		},
		{
			ID: "devicesync", Label: "Sync with another JukaHub",
			Sends: "Save file names, checksums and the saves themselves over the paired connection",
			Where: "Another JukaHub you paired (LAN or Tailscale)",
		},
		{
			ID: "romm", Label: "RomM",
			Sends: "The RomM sign-in and the library and save files you sync",
			Where: "Your RomM server",
		},
		{
			ID: "retroachievements", Label: "RetroAchievements",
			Sends: "Your username, API key, game hashes and progress",
			Where: "retroachievements.org",
		},
		{
			ID: "companion", Label: "Phone Companion",
			Sends: "Nothing to the internet: the phone and handheld talk directly after the PIN",
			Where: "This device's LAN address only",
			Status: func(*Config) string {
				if companion.srv != nil && companion.srv.Running() {
					return "Listening"
				}
				return "Off"
			},
		},
		{
			ID: "remoteplay", Label: "Remote Play and game streaming",
			Sends: "The console sign-in, session data and your controller input",
			Where: "PlayStation Network, Xbox and the configured streaming services",
		},
		{
			ID: "steam", Label: "Steam",
			Sends: "The Steam sign-in, profile and library requests",
			Where: "Steam's own sign-in and Web API",
		},
		{
			ID: "smarthome", Label: "Smart Home",
			Sends: "Device commands and the TP-Link or Home Assistant sign-in",
			Where: "TP-Link's cloud, your Home Assistant, or the local device",
		},
		{
			ID: "assistant", Label: "Juka assistant and voice",
			Sends: "The text or audio you give it, and the conversation context",
			Where: "The AI or speech provider whose key is configured",
		},
		{
			ID: "mail", Label: "Mail and calendar",
			Sends: "The IMAP, SMTP and CalDAV sign-ins, and the messages and events you sync",
			Where: "Your mail and calendar servers",
		},
		{
			ID: "chat", Label: "Chat and Discord",
			Sends: "Your messages, attachments and presence",
			Where: "Discord and the chat rooms you joined",
		},
		{
			ID: "diagnostics", Label: "Anonymous diagnostics",
			Sends:  "Anonymous device facts (model, version, settings shape), never logs, accounts or files",
			Where:  "A public JukaHub Info issue every ten launches",
			Status: func(c *Config) string { return privacyOnOff(telemetryOptedInFrom(c.Variables.Custom)) },
		},
		{
			ID: "reports", Label: "Bug reports",
			Sends:  "Only the report you review, plus the redacted diagnostics you leave switched on",
			Where:  "The JukaHub report service, which files a public GitHub issue",
			Status: func(*Config) string { return "Only when you send one" },
		},
		{
			ID: "netplay", Label: "Netplay",
			Sends: "The room list, game identity and the players' controller traffic",
			Where: "The netplay host and the other players",
		},
		{
			ID: "servers", Label: "File servers",
			Sends: "Only what a paired client reads or writes",
			Where: "This device's LAN address (FTP, SFTP, WebDAV, VNC or Telnet while started)",
			Status: func(*Config) string {
				if ftpSrv.Running() || sshSrv.Running() || davSrv.Running() || len(vncTelnetSecurityNames()) > 0 {
					return "Listening"
				}
				return "Off"
			},
		},
		{
			ID: "vpn", Label: "VPN and Tailscale",
			Sends: "The tunnel's own traffic and the tailnet sign-in",
			Where: "The VPN provider or your Tailscale network",
		},
		{
			ID: "community", Label: "Themes, feeds and community",
			Sends: "The catalogue or feed request and the theme you download",
			Where: "The JukaHub theme and community endpoints",
		},
	}
}

func privacyOnOff(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

// privacyNetworkRows turns the inventory into read-only Settings rows. They are
// appended to Settings → Privacy & data, after the switches they describe.
func privacyNetworkRows() []setRow {
	features := privacyNetworkFeatures()
	rows := make([]setRow, 0, len(features))
	for _, f := range features {
		f := f
		status := f.Status
		rows = append(rows, setRow{
			ID:     "net_" + f.ID,
			Label:  f.Label,
			Kind:   setKindInfo,
			Detail: setStatic(f.Sends + " · " + f.Where),
			Value: func(c *Config) string {
				if status != nil {
					return status(c)
				}
				return "Only when you use it"
			},
		})
	}
	return rows
}

// privacyExportCats is every category a data export carries.
func privacyExportCats() map[string]bool {
	cats := map[string]bool{}
	for _, cat := range bkupCategoryOrder {
		cats[cat] = true
	}
	return cats
}

// privacyExportData writes the user's settings, stores, plugins and saves as a
// zip. It is bkupCreate with every category and whole plugin folders, named
// for what the user asked for rather than for a restore.
func privacyExportData(ctx context.Context, dir, name string, doc []byte) (string, bkupManifest, error) {
	return bkupCreate(ctx, dir, name, privacyExportCats(), bkupOptions{PluginFiles: true, Saves: true},
		"data export", versionString(nil), doc, nil)
}

// privacyExportAllData is the Settings action: write the archive off the
// render thread and say where it landed.
func privacyExportAllData() {
	go func() {
		name := "JukaHub data " + time.Now().Format("2006-01-02 15-04") + ".zip"
		path, _, err := privacyExportData(context.Background(), bkupExportDir(), name, nil)
		tbxPost(func() {
			if err != nil {
				ShowToast("Could not export your data: "+err.Error(), ToastKindWarning)
				return
			}
			ShowToast("Your data is in "+filepath.Base(filepath.Dir(path))+"/"+filepath.Base(path), ToastKindSuccess)
		})
	}()
}
