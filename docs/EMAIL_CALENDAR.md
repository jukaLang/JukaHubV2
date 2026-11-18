# Email and calendar

JukaHub reads and sends your email and shows your calendars on the handheld:
a **Mail** app with an unread count on its Home tile, and your events in the
clock's calendar, on the Calendar widget and as reminders before they start.
Nothing is typed with the D-pad: accounts and calendar links come from your
phone through the Phone Companion.

Open **Tools → Email & calendar** (also **Settings → Network → Email &
calendar**, or search "email"). Mail itself is in the Start menu, the Apps
list and search, and can be pinned to Home (Edit Home → Apps and tools →
Mail).

> Status: built 2026-11-17 and unverified on a device. The protocols follow
> the RFCs and the providers' documentation; the first real runs are a [HW]
> task in `player/MODERNIZATION.md` (MAIL-1).

Code: `player/mailcal_store.go` (the accounts), `player/mail_*.go` (IMAP,
SMTP, the Mail app), `player/calsync*.go` (calendar sync and reminders),
`player/mailcal_ui.go` (the page), `player/companion_mailcal.go` (the phone
cards).

## Adding a mail account

1. On **Email & calendar**, choose **Add an account from your phone**. A QR
   code opens the Phone Companion at its **Email** card (the phone must be on
   the same Wi-Fi).
2. Type your address. The card recognises the big providers, fills in the
   servers and says where to make an **app password** — a password made for
   one app, which most providers require instead of your normal one.
3. Paste the app password and tap **Add on handheld**. The handheld signs in
   to your mail server before it saves anything: a refused password comes back
   as an error on the card. If the server cannot be reached right now, the
   account is saved anyway and the card says so.

Adding the same address again replaces what you typed: its password, name or
servers. A field left empty (the password included) keeps what the handheld
has, and so do the servers filled in for you unless you change them.

### Per provider

| Provider | What to paste | Calendar |
|---|---|---|
| Gmail | An app password from `myaccount.google.com/apppasswords` (needs 2-Step Verification). The spaces Google shows are optional. | Google Calendar has no app-password access: add the calendar's **secret address in iCal format** (below). |
| Outlook, Hotmail, Live | Nothing: choose **Sign in with Microsoft** on the handheld and approve the code on your phone (`microsoft.com/link`). Outlook.com no longer accepts passwords from mail apps. | Publish the calendar and add its ICS link (below). |
| iCloud | An app-specific password from `account.apple.com` → Sign-In and Security → App-Specific Passwords. | Comes with the account (CalDAV). |
| Yahoo, AOL | An app password from the account security page ("Generate app password"). | Yahoo: comes with the account (CalDAV). AOL: an ICS link. |
| Fastmail | An app password (Settings → Privacy & Security → App passwords) with IMAP, SMTP and CalDAV access. | Comes with the account (CalDAV). |
| Zoho | An app password from `accounts.zoho.com` → Security → App Passwords. IMAP must be on in Zoho Mail's settings. | Comes with the account (CalDAV). |
| Yandex | An app password from `id.yandex.com` → Security → App passwords (choose "Mail"; add one for "Calendar" too if the calendar is refused). | Comes with the account (CalDAV). |
| GMX, Mail.ru | Turn IMAP access on in the webmail's settings; Mail.ru wants a "password for external apps". | An ICS link, where the provider offers one. |
| Anything else | Open **Advanced** on the card: IMAP and SMTP server, port and security, and the user name if it is not the address. | A CalDAV address under Advanced, or an ICS link. |

For an unknown domain the card guesses `imap.<domain>` (port 993, TLS) and
`smtp.<domain>` (465, TLS); check them with your provider. Security is **TLS**
(encrypted from the first byte: 993, 465) or **STARTTLS** (143, 587; the
handheld refuses a server that does not offer it rather than send a password
in the clear).

### Microsoft sign-in

**Sign in with Microsoft** appears when the build has a Microsoft client ID.
The Mail screen shows a code and a QR code of `microsoft.com/link`; approve it
on your phone and the account is added. Nothing is typed and no password is
stored, only Microsoft's sign-in token, encrypted.

| Where | What |
|---|---|
| Build | `-ldflags "-X main.microsoftOAuthClientID=…"`, or `JUKAHUB_MS_CLIENT_ID` in the environment |
| Config | `ms_client_id` in the config's variables; `ms_tenant` (default `common`; `consumers` for a registration limited to personal accounts), or `JUKAHUB_MS_TENANT` |

The client is a public one ("Mobile and desktop applications", device code
flow allowed) registered in Microsoft Entra with the delegated permissions
`IMAP.AccessAsUser.All`, `SMTP.Send` and `offline_access`. There is no secret.

## Adding a calendar

Accounts at iCloud, Yahoo, Fastmail, Zoho and Yandex bring their calendar
with them. For everything else, choose **Add a calendar from your phone**: the
QR code opens the **Calendars** card.

- **A link** (`https://…ics` or `webcal://…`): paste it and tap **Add on
  handheld**, with the user name and password left empty.
  - **Google Calendar**: on a computer, Google Calendar → Settings → click the
    calendar under "Settings for my calendars" → **Secret address in iCal
    format**. Treat it like a password: anyone with it can read the calendar.
  - **Outlook**: outlook.com → Settings → Calendar → Shared calendars →
    **Publish a calendar** → choose the calendar and "Can view all details" →
    copy the **ICS** link.
  - Sports schedules, school terms, holidays: any `.ics` link works.
- **CalDAV** (Nextcloud, Radicale, Baïkal, a provider's CalDAV): paste the
  address and fill in the user name and password.

Links must be `https://` (or `webcal://`, fetched over https). Plain `http://`
is accepted only for a server on your home network. The handheld checks the
calendar answers before it saves it, and names it after the calendar unless
you typed a name.

You can also copy `.ics` files to the handheld: in **Files**, press **A** on an
`.ics` file, then **A** again within a few seconds to add its events to the
calendar.

## On the handheld

- **Mail** lists the newest messages of all inboxes (L1/R1 or Tab switches
  between All inboxes and each account). **A** opens a message, **Y** replies,
  **X** moves it to Trash; **Select** opens the menu: Compose, Sync now, Mark
  all read, Archive, Mark unread, Save attachments (to Downloads), Email
  settings. In Compose, **Start** sends.
- **Home**: the Mail tile shows the unread count; the Calendar widget shows the
  next event (in place of the month name on a small tile, under the month on a
  tall one).
- **The clock's calendar** (press the clock, or search "calendar") lists each
  day's events.
- **Reminders** pop up before an event starts, also during a RetroArch game.
- **Juka AI** can read your newest mail ("Anything new in my inbox?") and
  your agenda ("What's on this week?") with two read-only tools
  ([AI_TOOLS.md](AI_TOOLS.md)). Mail text goes to the AI service you use for
  Juka; leave the question unasked if that matters to you.

The **Email & calendar** page shows each account with its status (or the last
error, in words) and **Sync now**, **Mail On/Off**, **Calendar On/Off** and
**Remove**; each calendar with its switch and **Remove**; and:

| Setting | Choices | Default |
|---|---|---|
| Check for mail and events | every 5, 15, 30 or 60 minutes while JukaHub is open | 15 |
| New-mail notifications | on / off | on |
| Event reminders | on / off | on |
| Remind me | 5, 10, 15, 30 or 60 minutes before | 10 |

## What is stored, and how

Everything lives in `mailcal.json` beside the other settings (file mode
0600), plus caches in the `mailcal/` folder next to it (message headers and
snippets, calendar events).

- Passwords, Microsoft sign-in tokens, calendar links and CalDAV passwords are
  stored **encrypted** (`ENC:…`, the same device-bound encryption as the other
  API keys, `player/crypto.go`). A settings file copied to another device does
  not sign in there.
- Nothing secret is ever sent back to the phone: the cards show the address,
  the provider, the calendar's host and the last four characters of a link.
- Passwords and links never appear in logs or bug reports.
- **Remove** forgets the account or calendar and its cached mail and events.
  Nothing on the server changes.

## Limits

- **Calendars are read-only.** Events are shown and remind you; creating or
  changing events happens in your calendar app.
- **Plain-text mail.** Messages are shown as text (HTML mail is converted);
  replies and new mail are plain text. Images are not shown; attachments can
  be saved.
- **No push.** Mail is checked on the interval above, and when you open Mail
  or press Sync now; there is no IMAP IDLE yet.
- **Inbox first.** Mail lists the inbox of each account.
- Outlook calendars come in as published ICS links, not through the Microsoft
  sign-in.

## Troubleshooting

- **"refused the password"**: make an app password (the table above); a normal
  password is refused by Gmail, iCloud, Yahoo and most others.
- **"could not be found"**: the server name is wrong, or the handheld is
  offline. Check Wi-Fi, then the servers under Advanced.
- **A calendar link stops working**: Google's secret address changes if you
  reset it; add the new one and remove the old one on **Email & calendar**
  (only the same link again replaces a calendar).
- **Times look shifted**: check **Settings → Date & time**, the time zone in
  particular. Reminders wait until the handheld's clock has been set from the
  network.
