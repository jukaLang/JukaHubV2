# Party Mode

Party Mode turns the handheld into a game-show host. Friends scan the QR code
on its screen with their phones and play from the phone's browser — no app,
no account and no internet. The handheld shows the round, the picture and the
scoreboard; each phone is a buzzer with its own copy of the picture.

The first game is **Guess the Game**, played with the box art of your own
library.

## Starting a party

Open **Tools → Party Mode** (or search Home for "party"). The screen opens a
room at once:

- The left card is the way in: a QR code, the party code under it, and the
  address to type for a phone that cannot scan (`192.168.1.42:8319` and the
  code). Phones must be on the same Wi-Fi as the handheld.
- With no network yet, the card offers **Wi-Fi** and, on a TrimUI,
  **Hotspot**. While the handheld's own hotspot is on, the card shows two QR
  codes: the left one joins the hotspot's Wi-Fi, the right one opens the
  game. That is the "play anywhere" setup — a park, a train — and needs
  nothing but the handheld and the phones (see [Hotspot](HOTSPOT.md); its
  device check is still open).
- The right side shows how many games can be asked about ("212 games with box
  art"), the game's length (**Rounds**: 5, 10, 15 or 20 — Left/Right to
  change), who has joined, **Start game**, and **Play on this handheld**.

A phone opens the page, types a name and presses **Join the party**. Names are
cut to 16 characters; a second "Sam" becomes "Sam 2". Up to 12 players, at most
two seats per phone.

**Play on this handheld** (or **Y**) gives the handheld a seat too, so two
people can play with one phone — or one person can try the game alone.

## Guess the Game

Each round takes one game from the library at random, from the games that
have a cover on the card and are not hidden:

1. **Round 3 of 10** — a short "get ready" while the cover is prepared.
2. **The reveal.** A small square of the cover appears, then zooms out in
   five steps until the whole cover shows. The points at stake drop at every
   step: **500, 400, 300, 200, 100**. The crop starts below the top third,
   where most covers print the title.
3. **Buzz.** Anyone who knows it presses **BUZZ** on their phone (or **A** on
   the handheld seat). The reveal pauses and the buzzer alone gets four
   titles to pick from, with 10 seconds to choose. The wrong titles come from
   the same console when the library has enough of them.
   - Right: the points, and the round ends.
   - Wrong, or no pick in time: **−100** (a score never drops below zero), that
     player is out for the round, and the reveal carries on for everyone
     else.
4. **The answer.** The whole cover, the title and console, who got it and who
   missed. If nobody did, the cover simply shows.

After the last round the winner screen shows the podium (ties share a place),
the full ranking and some confetti. **Play again** starts a new game with the
same players; **Lobby** (or **X**) goes back to the lobby.

During a game, **X** ends it (everyone goes back to the lobby, nobody has to
join again). **B** leaves the screen — which closes the party — after asking
when phones are seated or a game is running. While the handheld seat is
picking an answer, B and X do nothing: the tiles are lettered A–D, and "B"
there means the answer.

### What the phones see

The page fits a phone in portrait: the round, the points, the picture with
the reveal steps, a timer bar, the big **BUZZ** button, the four answers when
it is your turn, and the scoreboard. It plays short sounds and vibrates on a
buzz, a right answer, a miss and the win. Reloading the page keeps the seat.

If the handheld goes to sleep or leaves the Wi-Fi, the page says it lost the
handheld and offers **Try again**. If the party was closed and a new one
opened, it asks the player to scan the new code.

## Covers

Guess the Game uses the same covers as the game library: downloaded box art,
covers already on the card (NextUI's `.media`, MinUI's `.res`) and pictures
taken while a game played. It needs at least **4** games with a cover. With
fewer, the lobby says so — download covers from the game library first
([Game library](GAME_LIBRARY.md)).

A cover that will not open is swapped for another game before its round
starts. If covers stop opening altogether, the game ends at the rounds played
so far.

## The screen and power

While phones are seated and still polling, the idle screen and Auto sleep
wait: everyone is playing on their phones, not on the handheld. A phone whose
page has been closed or locked for 20 seconds counts as away — its score
stays, but it no longer holds a round open. In the lobby, a phone away for two
minutes is taken off the list.

## Network and privacy

- The game server runs **only while the Party Mode screen is open**: entering
  the screen starts it, leaving the screen by any route stops it.
- It listens on port **8319** (or any free port when that one is taken; the
  QR code carries the real one). `JUKAHUB_PARTYMODE_PORT` overrides it.
- It is plain HTTP on the local network, like Watch Party. Joining needs the
  party code; every later call needs the random token the join returned.
  Wrong codes and unknown tokens from one address are throttled.
- A phone only ever receives the picture the round has already shown — never
  a later step, a file path or anything else from the handheld. There are no
  files, settings or controls behind the page.
- Screenshot mode shows the stand-in address and no hotspot Wi-Fi code, and
  the handheld seat is called "Handheld".

Security & trust lists Party Mode with the other servers.

## For developers

| File | What it holds |
| --- | --- |
| `player/partymode.go` | The room: seats, the round flow, scoring, the deck and decoys, crop rectangles, the views for the handheld and the phones. No SDL, no HTTP; time is passed in. |
| `player/partymode_net.go` | The HTTP server and its API (`/api/pm/join`, `state` long-poll, `buzz`, `answer`, `leave`, `img`), the crop JPEGs, the throttle, and `pmodeRun` (the scene task that owns the room, the cover decoder and the ticker). |
| `player/partymode_page.go` | The phone page: one self-contained HTML document. |
| `player/partymode_ui.go` | The handheld screen (a tool scene on `tbxui.go`). |

Phones long-poll `GET /api/pm/state?t=<token>&v=<version>`: the server answers
at once when the room has moved past that version, else holds the request up
to 15 seconds. Every change to the room bumps the version.
