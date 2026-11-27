# Cast receiver

Community → **Cast receiver** turns the handheld into a place to show a
picture sent from a phone. It is a small receiver, not AirPlay or Chromecast:
it accepts one PNG or JPEG at a time and shows it fullscreen until **B**.

On the phone, open the companion's `/cast` page (for example
`http://<handheld>:8080/cast`), enter the companion PIN, choose a picture and
send it. The handheld switches to it at once; **B** or **A** closes the card
and returns to the app.

The upload goes to the PIN-guarded `/api/cast-receiver` route. A body over
16 MB, anything that is not a PNG or JPEG, and a request without the PIN are
refused. The picture is saved as `<data>/cast-receiver/incoming.png` (or
`.jpg`) and is not deleted automatically; sending another replaces it. The
card does not reopen after a restart — a stale file is never shown without a
new POST.

This is the other direction to [Screen Share](CONFIGURATION.md#castquality):
Screen Share sends the handheld's screen to a viewer; the cast receiver shows
a phone's picture on the handheld.

Nothing is sent to an outside service, and the phone page never sees the
handheld's library or settings.
