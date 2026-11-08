#!/bin/sh
# quote — a short quote for the day, or one at random.
#
# Actions: today, random.
#   today   the same quote all day long (picked by the day of the year)
#   random  a different one on every press
#
# Every quote is old enough to be in the public domain and short enough for
# one toast line. Add your own at the end of the list; one per line.
action=${1-}
if [ "$#" -gt 0 ]; then
    shift
fi

quotes='Well begun is half done. — Aristotle
The journey of a thousand miles begins with one step. — Lao Tzu
It does not matter how slowly you go, as long as you do not stop. — Confucius
Fortune favours the bold. — Virgil
Knowledge is power. — Francis Bacon
Patience is bitter, but its fruit is sweet. — Jean-Jacques Rousseau
The best way out is always through. — Robert Frost
Fall seven times, stand up eight. — Japanese proverb
Energy and persistence conquer all things. — Benjamin Franklin
Well done is better than well said. — Benjamin Franklin
Lost time is never found again. — Benjamin Franklin
One today is worth two tomorrows. — Benjamin Franklin
Waste no more time arguing what a good person should be. Be one. — Marcus Aurelius
Dwell on the beauty of life. — Marcus Aurelius
No great thing is created suddenly. — Epictetus
First say to yourself what you would be; then do what you have to do. — Epictetus
All that glitters is not gold. — William Shakespeare
A merry heart goes all the day. — William Shakespeare
Hope is the thing with feathers. — Emily Dickinson
The only way to have a friend is to be one. — Ralph Waldo Emerson
The harder the conflict, the more glorious the triumph. — Thomas Paine
Turn your face to the sun and the shadows fall behind you. — Maori proverb
Game over is just a long loading screen. Press Start. — JukaHub'

count=$(printf '%s\n' "$quotes" | wc -l | tr -d ' ')
case "$count" in
    ''|*[!0-9]*|0)
        printf 'No quotes in the list\n' >&2
        exit 1
        ;;
esac

# line N prints quote number N (1-based).
line() {
    printf '%s\n' "$quotes" | sed -n "${1}p"
}

case "$action" in
    today)
        # %j is zero-padded ("045"), which sh arithmetic would read as octal.
        day=$(date +%j | sed 's/^0*//')
        [ -n "$day" ] || day=0
        line $((day % count + 1))
        ;;
    random)
        if ! command -v awk >/dev/null 2>&1; then
            printf 'Randomness needs awk, which is not on PATH\n' >&2
            exit 1
        fi
        seed=$(date +%s%N 2>/dev/null)
        case "$seed" in
            ''|*[!0-9]*) seed=$(date +%s) ;;
        esac
        # Keep the last nine digits: awk holds numbers as doubles, and a full
        # nanosecond stamp loses exactly the digits two quick presses differ in.
        if [ "${#seed}" -gt 9 ]; then
            seed=${seed#"${seed%?????????}"}
        fi
        n=$(awk -v n="$count" -v s="$seed" 'BEGIN { srand(s); printf "%d\n", int(rand() * n) + 1 }')
        line "$n"
        ;;
    *)
        printf 'Unknown action: %s (try today, random)\n' "$action" >&2
        exit 1
        ;;
esac
