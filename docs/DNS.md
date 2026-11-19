# DNS

**Settings → Network → DNS** picks the servers that turn names
(`github.com`, `thumbnails.libretro.com`) into addresses. **Automatic**, the
default, uses your network's own — the router's, handed out with the Wi-Fi
address — and changes nothing.

| Choice | Servers | What it does |
|---|---|---|
| Automatic | the network's own | what the router hands out |
| Cloudflare | 1.1.1.1, 1.0.0.1 | fast and privacy-focused |
| Google | 8.8.8.8, 8.8.4.4 | Google Public DNS |
| Quad9 | 9.9.9.9, 149.112.112.112 | blocks known malware and phishing sites |
| AdGuard | 94.140.14.14, 94.140.15.15 | blocks ads and trackers in the browser, apps and ports |
| Cloudflare for Families | 1.1.1.3, 1.0.0.3 | blocks malware and adult sites |
| OpenDNS | 208.67.222.222, 208.67.220.220 | Cisco OpenDNS |
| Custom | up to three you type | a Pi-hole or AdGuard Home at home, your router, any service |

**Custom** opens the keyboard the first time (and again when you pick it
while it is already chosen; **Custom DNS servers** below edits them too).
Type IPv4 or IPv6 addresses separated by spaces or commas — `192.168.1.2
1.1.1.1` keeps names working when the Pi-hole is off. Emptying the field goes
back to Automatic.

**Find the fastest DNS** asks every choice (and your network's own, when it is
known) for three popular names from where you are, and lists them fastest
first with their times; picking one uses it. The times are from this device on
this network: at a friend's house the answer can differ.

## Where it applies

- **On the handheld** (TrimUI, JukaHub running as root, as the firmware starts
  it) the choice is the whole system's: games, PortMaster ports, Android apps,
  Remote Play and JukaHub itself. The row says "for the whole device".
- **On Windows** it covers JukaHub's own requests — box art, downloads, the
  browser, chat, updates. Windows keeps the PC's own DNS for everything else:
  set that in Windows' network settings if you want it everywhere.
- **On a Linux PC without root** it covers JukaHub only, and the row says why.
- **With the VPN on** and a configuration that names DNS servers (`DNS =` in
  the .conf), the VPN's servers win while it runs — inside the tunnel they are
  the ones that work, and they keep your lookups inside it. The row says so;
  the choice comes back when the VPN stops. A configuration without `DNS =`
  leaves the choice in place, and its lookups go through the tunnel like
  everything else.
- **Safe mode** uses the network's own servers for that start.

After a change, and whenever you join another network, JukaHub asks the
chosen servers for one name. When none answers, the row says "No answer
from … here", and a notification follows if they still do not answer half a
minute later: some hotel, school and work networks block every DNS server but
their own. Pick Automatic there. JukaHub's own lookups also move to the next
server of the choice when the first stops answering, and try the first again
two minutes later.

## How it works on the handheld

The system reads its DNS servers from `/etc/resolv.conf`, which on TrimUI is
usually a link into `/tmp` that the DHCP client rewrites every time the Wi-Fi
connects or renews its address. JukaHub:

1. keeps the network's own file aside — in memory, and in
   `<JukaHub>/data/dns-resolv.json` so a crash or a power cut can be undone —
   and writes the chosen servers in its place, keeping the network's `search`
   domain, under the line
   `# JukaHub DNS (Settings → Network → DNS): Automatic puts the network's own servers back`;
2. looks at the file every three seconds while a choice is set, and when DHCP
   has put the network's own back, keeps that as the new copy and writes the
   choice again;
3. puts the network's own back when you pick Automatic, in safe mode, and when
   JukaHub quits (the quit screen's "Putting the network's DNS back" row).
   After a crash the next start does it.

The file is written in place, so the link and its permissions stay as the
firmware made them. When the VPN has its own file in place (its marker line),
JukaHub leaves it alone; the VPN puts back the file it replaced when it stops,
and JukaHub carries on from there. `dns-resolv.json` stays out of backups:
restored elsewhere it would name another network's servers.

## Troubleshooting

- **Nothing loads after picking a server.** The network blocks outside DNS, or
  the server is down. Pick **Automatic** — that always uses the router — or
  **Find the fastest DNS** to see which ones answer here.
- **Names only your router knows stopped working** (`fritz.box`, `nas.lan`,
  a printer's name). A public service cannot know them: use **Custom** with
  your router or Pi-hole first (`192.168.1.1 1.1.1.1`). On Windows a choice also
  sends JukaHub's own lookups past Windows' name services, so `.local`
  (Bonjour) and bare PC names resolve in JukaHub only on Automatic; type the
  device's IP address instead.
- **A Pi-hole name stopped working.** Add the Pi-hole first and a public
  server second (`192.168.1.2 1.1.1.1`); JukaHub's own lookups fall back in
  order, the system's resolver does the same.
- **The row says "for JukaHub only".** The system file could not be written:
  JukaHub is not running as root, or the firmware's `/etc/resolv.conf` is on a
  read-only partition. The reason is on the row.
- **Tools → Logs** has `dns` lines for every change of the
  file and every server that did not answer.

The settings are `dnsMode` and `dnsServers` in
[CONFIGURATION.md](CONFIGURATION.md). A design pushed from the web builder
cannot change them without your approval: they decide where every name the
device looks up is sent.
