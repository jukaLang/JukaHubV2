#!/usr/bin/env python3
"""Build the on-screen keyboard's English word list (vkdata/en_words.txt.gz).

The keyboard's suggestion strip (vkdict.go) needs a list of English words with
a rough frequency rank. This script makes one from two sources:

  * the Hunspell en_US dictionary (SCOWL-derived; see SCOWL-COPYRIGHT.txt next
    to this file for its licence, which travels with the list), expanded from
    stems + affix flags into the word forms people actually type, and
  * the ranking below, written for JukaHub: the few thousand most common English
    words in rough frequency order, with chat words first. Inflections of a
    ranked word inherit most of its rank; everything else gets a base rank by
    kind (plain word, derived form, proper noun).

Output format: gzip of UTF-8 lines "word<TAB>rank", sorted by the lower-cased
word (byte order), rank 1..255 (higher = more common). vkdict.go reads it
lazily on the first keyboard use.

Usage (any Python 3.8+, no packages needed):
    python3 vkdict_build.py [/usr/share/hunspell/en_US.dic] [/usr/share/hunspell/en_US.aff]
Run from the vkdata directory; it rewrites en_words.txt.gz in place.
"""

import gzip
import os
import re
import sys

DIC = sys.argv[1] if len(sys.argv) > 1 else "/usr/share/hunspell/en_US.dic"
AFF = sys.argv[2] if len(sys.argv) > 2 else "/usr/share/hunspell/en_US.aff"
OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "en_words.txt.gz")

# --- ranking -----------------------------------------------------------------
# Bands of common words, most common first. Order inside a band matters a
# little (rank falls linearly across the whole list), the band boundaries a lot.
RANKED = """
the I you to a and it of that in is me what this for my on your we have do no
don't are be not was he can know with I'm all so just there here get it's like
but if they go right out she up about him at one oh now yeah how come we're want
well think gonna her let see good got from why would that's okay ok who will
then there's an back time his really or look as could did take mean sure tell
has man need by us our been where say much something you're yes them way make
very love can't down little thank thanks never more should some I'll over going
too maybe let's thing sorry I've two people even didn't said any life still give
these call before nothing stop first find try made better great wait doing hey
hi hello please because work day home again off talk always into feel night
other doesn't isn't won't wasn't aren't weren't couldn't wouldn't shouldn't
haven't hasn't hadn't you've you'll you'd we've we'll they're they've they'll
he's she's here's what's who's I'd new old next last long big small same each
every many most only also very well than which when were had has having being
been does done went goes gone came comes coming took taken takes taking saw seen
sees looking looked looks wanted wants told tells telling asked ask asks asking
needs needed felt feels feeling found finds became become leave left put meant
keep kept begin began seem seemed help helped talked turn turned start started
show showed shown hear heard play played run ran move moved live lived believe
believed hold held bring brought happen happened write wrote written read
playing watch watched follow followed learn learned change changed understand
understood speak spoke open opened walk walked win won remember remembered loved
buy bought send sent build built stay stayed fall fell cut reach remain suggest
pass sell sold decide pull lose lost pay paid meet met add spend spent grow grew
allow offer create expect serve die died raise report require continue include
"""

RANKED += """
today tomorrow tonight yesterday now soon later morning afternoon evening week
weekend month year years days hours minutes hour minute second seconds moment
always sometimes often usually never ever already yet still just almost enough
quite rather pretty really actually probably definitely maybe perhaps exactly
anyway though although however while since until unless whether both either
neither nor such own same different important possible free real true sure ready
able fine nice cool awesome amazing beautiful happy sad funny crazy weird bad
worse worst better best good great high low hard easy fast slow early late long
short young old new big small large huge tiny full empty hot cold warm clear dark
light heavy strong weak rich poor safe dangerous quiet loud busy tired hungry
sick dead alive wrong right left top bottom inside outside front back middle
around between through across along behind above below under over near far away
together alone else everything anything something nothing everyone anyone
someone nobody everybody somebody anybody everywhere somewhere anywhere nowhere
myself yourself himself herself itself ourselves themselves yourselves mine
yours hers ours theirs whose whom whatever whenever wherever whoever
"""

RANKED += """
people person man woman men women child children kid kids boy girl baby friend
friends family mom dad mother father brother sister son daughter husband wife
guy guys parents team group company world country city town place house home
room school work job money business game games music movie movies video videos
book books story phone computer internet email message messages text chat name
names number numbers word words idea way thing things part point problem
question questions answer reason case fact example kind sort type side end line
car road street water food fire air light sound picture pictures photo photos
door window table bed head hand hands face eyes eye body heart mind life death
health power war history news information information service system program
government state law level area market price cost value interest party member
music song songs show shows series episode season channel stream playlist
"""

RANKED += """
make makes making made give gives giving gave given get gets getting got gotten
go goes going went gone know knows knowing knew known think thinks thinking
thought see sees seeing saw seen come comes coming came take takes taking took
taken want wants wanting wanted use uses using used find finds finding found
tell tells telling told ask asks asking asked work works working worked seem
seems seeming seemed feel feels feeling felt try tries trying tried leave leaves
leaving call calls calling called need needs needing needed keep keeps keeping
let lets letting begin begins beginning help helps helping talk talks talking
turn turns turning start starts starting show shows showing hear hears hearing
play plays playing run runs running move moves moving like likes liking liked
live lives living believe believes bring brings bringing happen happens
happening write writes writing provide provides providing sit sits sitting
stand stands standing lose loses losing pay pays paying meet meets meeting
include includes including continue continues continuing set sets setting learn
learns learning change changes changing lead leads leading understand
understands understanding watch watches watching follow follows following stop
stops stopping create creates creating speak speaks speaking read reads reading
spend spends spending grow grows growing open opens opening walk walks walking
win wins winning offer offers offering remember remembers remembering love loves
loving consider considers considering appear appears appearing buy buys buying
wait waits waiting serve serves serving die dies dying send sends sending expect
expects expecting build builds building stay stays staying fall falls falling
cut cuts cutting reach reaches reaching kill kills killing remain remains
remaining eat eats eating ate eaten drink drinks drinking drank sleep sleeps
sleeping slept wake wakes waking woke drive drives driving drove driven fly
flies flying flew flown sing sings singing sang sung swim swims swimming swam
teach teaches teaching taught catch catches catching caught throw throws throwing
threw thrown draw draws drawing drew drawn choose chooses choosing chose chosen
break breaks breaking broke broken forget forgets forgetting forgot forgotten
forgive wear wears wearing wore worn sell sells selling fight fights fighting
fought hide hides hiding hid hidden hit hits hitting hurt hurts hurting join
joins joining joined check checks checking checked fix fixes fixing fixed
download downloads downloading downloaded upload uploads uploading uploaded
install installs installing installed update updates updating updated save saves
saving saved delete deletes deleting deleted copy copies copying copied paste
pastes pasting pasted share shares sharing shared post posts posting posted
search searches searching searched type types typing typed click clicks clicking
clicked tap taps tapping tapped press presses pressing pressed close closes
closing closed restart reboot connect connects connecting connected
"""

RANKED += """
about after again against ago all almost along also always among another any
around as at away back because before behind below beside besides beyond but by
down during except for from in inside instead into like near of off on onto out
outside over past per since than through throughout till to toward towards under
underneath unlike until up upon via with within without the a an and or nor so
yet if then else when where why how what which who whom whose this that these
those here there it its it's I me my mine you your yours he him his she her hers
we us our ours they them their theirs one ones two three four five six seven
eight nine ten eleven twelve twenty thirty forty fifty hundred thousand million
billion first second third fourth fifth last next half double once twice
"""

RANKED += """
lol omg btw thx pls plz wow yay haha hmm uh um huh oops ugh nope yep yup sup
cheers bye goodbye goodnight morning congrats congratulations welcome awesome
cool nice sweet perfect lovely brilliant fantastic excellent wonderful terrible
horrible annoying boring interesting exciting amazing incredible ridiculous
honestly literally basically seriously totally completely absolutely obviously
apparently hopefully unfortunately fortunately luckily recently finally suddenly
quickly slowly easily simply clearly certainly especially exactly nearly mostly
already instead otherwise anyway meanwhile together tomorrow tonight
"""

RANKED += """
able about above accept according account across act action activity actually
add address admit adult affect afraid after afternoon again against age agency
agent agree agreement ahead air allow almost alone along already although amount
analysis animal another answer anyone anything appear apply approach area argue
arm army around arrive art article artist assume attack attention attorney
audience author authority available avoid away baby back bad bag ball bank bar
base beat beautiful become bed before behavior behind believe benefit best
better beyond bill billion bit black blood blue board body book born both box
boy break bring brother budget build building business buy call camera campaign
cancer candidate capital car card care career carry case catch cause cell center
central century certain certainly chair challenge chance change character charge
check child choice choose church citizen city civil claim class clear clearly
close coach cold collection college color come commercial common community
company compare computer concern condition conference congress consider consumer
contain continue control cost could country couple course court cover create
crime cultural culture cup current customer cut dark data daughter dead deal
death debate decade decide decision deep defense degree democrat democratic
describe design despite detail determine develop development difference
different difficult dinner direction director discover discuss discussion
disease doctor dog door down draw dream drive drop drug during each early east
easy eat economic economy edge education effect effort eight either election
else employee end energy enjoy enough enter entire environment environmental
especially establish even evening event ever every everybody everyone everything
evidence exactly example executive exist expect experience expert explain eye
face fact factor fail fall family far fast father fear federal feel feeling few
field fight figure fill film final finally financial find fine finger finish
fire firm first fish five floor fly focus follow food foot for force foreign
forget form former forward four free friend from front full fund future game
garden gas general generation get girl give glass go goal good government great
green ground group grow growth guess gun guy hair half hand hang happen happy
hard have he head health hear heart heat heavy help her here herself high him
himself his history hit hold home hope hospital hot hotel hour house how however
huge human hundred husband idea identify if image imagine impact important
improve include including increase indeed indicate individual industry
information inside instead institution interest interesting international
interview into investment involve issue it item its itself job join just keep
key kid kill kind kitchen know knowledge land language large last late later
laugh law lawyer lay lead leader learn least leave left leg legal less let
letter level lie life light like likely line list listen little live local long
look lose loss lot love low machine magazine main maintain major majority make
manage management manager many market marriage material matter may maybe me
mean measure media medical meet meeting member memory mention message method
middle might military million mind minute miss mission model modern moment
money month more morning most mother mouth move movement movie much music must
myself name nation national natural nature near nearly necessary need network
never new news newspaper next nice night none nor north not note nothing notice
now number occur of off offer office officer official often oh oil ok old on
once one only onto open operation opportunity option or order organization
other others our out outside over own owner page pain painting paper parent
part participant particular particularly partner party pass past patient
pattern pay peace people per perform performance perhaps period person personal
phone physical pick picture piece place plan plant play player point police
policy political politics poor popular population position positive possible
power practice prepare present president pressure pretty prevent price private
probably problem process produce product production professional professor
program project property protect prove provide public pull purpose push put
quality question quickly quite race radio raise range rate rather reach read
ready real reality realize really reason receive recent recently recognize
record red reduce reflect region relate relationship religious remain remember
remove report represent require research resource respond response
responsibility rest result return reveal rich right rise risk road rock role
room rule run safe same save say scene school science scientist score sea
season seat second section security see seek seem sell send senior sense series
serious serve service set seven several shake share she shoot short shot
should shoulder show side sign significant similar simple simply since sing
single sister sit site situation six size skill skin small smile so social
society soldier some somebody someone something sometimes son song soon sort
sound source south southern space speak special specific speech spend sport
spring staff stage stand standard star start state statement station stay step
still stock stop store story strategy street strong structure student study
stuff style subject success successful such suddenly suffer suggest summer
support sure surface system table take talk task tax teach teacher team
technology television tell ten tend term test than thank that the their them
themselves then theory there these they thing think third this those though
thought thousand threat three through throughout throw thus time to today
together tonight too top total tough toward town trade traditional training
travel treat treatment tree trial trip trouble true truth try turn two type
under understand unit until up upon us use usually value various very victim
view violence visit voice vote wait walk wall want war watch water way we
weapon wear week weight well west western what whatever when where whether
which while white who whole whom whose why wide wife will win wind window wish
with within without woman wonder word work worker world worry would write
writer wrong yard yeah year yes yet you young your yourself
"""

RANKED += """
app apps online offline website site link links browser download file files
folder folders screen screenshot photo video stream streaming channel playlist
podcast episode settings setting wifi bluetooth battery charger charge charging
update updates password username login account profile device devices console
controller gamepad button buttons keyboard mouse speaker speakers headphones
volume brightness display theme themes emulator emulators retro handheld
arcade cartridge save saves level levels boss score scores player players
multiplayer online server servers network internet router signal google youtube
netflix discord github linux windows android iphone ipad laptop tablet desktop
gaming gamer games console consoles controller playstation nintendo steam
weather news chat chats message messages reply replies send sent group groups
call calls video calls friend friends like likes comment comments follow
followers subscribe subscribers notification notifications music songs album
albums artist artists song lyrics radio movie movies film films show shows
series season episodes trailer watch watched watching
"""

RANKED += """
ability absolutely academic accident accompany accomplish accurate achieve
achievement acknowledge acquire active actor actress actual adapt addition
additional adjust administration admire adopt advance advantage adventure
advertising advice advise afford afterwards agenda aggressive aid aim airline
airport alarm album alcohol alive alliance alter alternative amazing ambition
ancient angle angry announce annual anxiety anybody anyway apart apartment
apologize apparent appeal apple appointment appreciate appropriate approve
architecture arrangement arrest arrival aside asleep aspect assault assess
asset assist assistance assistant associate association atmosphere attach
attempt attend attitude attract attractive aunt automatic autumn average award
aware awareness awful background bake balance band barely barrier baseball
basic basically basket basketball bath bathroom battery battle beach bean bear
beautiful beauty bedroom beef beer beginning belief bell belong belt bench bend
beneath bet bicycle bike bird birth birthday bitter blame blank blanket blind
block blog blow boat bomb bone bonus border bored boring borrow boss bother
bottle bottom bowl brain branch brand brave bread breakfast breath breathe brick
bridge brief bright brilliant broad brown brush bubble buck bug bunch burden
burn burst bury bus butter button cabin cable cake calculate calendar calm
camp campus cancel candle candy cap capable capacity captain capture carbon
careful carpet cash cast castle cat category cave ceiling celebrate celebration
celebrity chain chairman champion championship channel chapter charity chart
chase cheap cheat cheek cheese chef chemical chest chicken chief childhood chip
chocolate chorus circle circumstance cite civilian classic classroom clean
client climate climb clock closet cloth clothes clothing cloud club clue coat
code coffee coin collapse colleague collect combination combine comedy comfort
comfortable command comment commission commit commitment committee communicate
communication comparison compete competition competitive complain complaint
complete completely complex complicated component computer concentrate concept
concert conclude conclusion concrete conduct confidence confident confirm
conflict confused confusion congratulations connect connection conscious
consequence conservative considerable consist constant constantly construct
construction consult contact content contest context contract contrast
contribute contribution convention conversation convert convince cook cookie
cooking cool cooperation cope copy core corn corner corporate correct cottage
cotton couch council count counter county courage cousin cow crack craft crash
crazy cream creative creature credit crew crisis criteria critic critical
criticism crop cross crowd crucial cruel cry cultural curious currently curtain
curve custom cycle daily damage dance danger dangerous dare database date dawn
deadline deaf dear debt decline decorate decrease deer defeat defend define
definitely definition delay deliver delivery demand demonstrate dentist deny
department depend dependent deposit depressed depression depth deputy derive
desert deserve desk desperate dessert destination destroy destruction detailed
detect detective device devote diagnosis diamond diary dictionary diet differ
digital dimension dining dirt dirty disabled disagree disappear disaster
discipline discount discovery dish dismiss display distance distinct distinguish
distribute distribution district disturb divide division divorce document
domestic dominant donate double doubt downtown dozen draft drag drama dramatic
drawer dress drink dropped dry duck due dust duty eager earn earth ease easily
eastern edit edition educate educational effective efficient egg elderly elect
electric electricity electronic elegant element elevator eliminate elsewhere
embarrassed emerge emergency emotion emotional emphasis emphasize employ
employment empty enable encounter encourage enemy engage engine engineer
engineering enhance enormous ensure entertainment enthusiasm entrance entry
environment episode equal equally equipment era error escape essay essential
estate estimate ethnic evaluate eventually everyday evil evolution exact exam
examination examine excellent exception exchange excited excitement exciting
excuse exercise exhibit exhibition existence exit expand expansion expectation
expense expensive experiment explanation explode explore explosion export expose
exposure express expression extend extension extensive extent external extra
extraordinary extreme extremely fabric facility faculty fade fair fairly faith
false familiar famous fan fancy fantastic fantasy farm farmer fashion fat fault
favor favorite feature fee feed female fence festival fever fiction fifteen
fifty file finance finding fitness flag flame flat flavor flee flesh flight
float flood flour flow flower fold folk following fool football forest forever
formal format formula fortune forty founder fourth frame frankly freedom
freeze frequency frequent frequently fresh fridge friendly friendship frighten
fruit frustrated fuel fully fun function funding funeral funny furniture
furthermore gain gallery gap garage gate gather gay gaze gear gene generally
generate generous genetic genius gentle gentleman gently genuine gesture ghost
giant gift gifted given glad global glove god gold golden golf grab grade
gradually graduate grain grand grandfather grandmother grant grass grave gray
greatest grocery guarantee guard guest guide guideline guilty guitar habit hall
handle handsome happiness harbor hardly harm hat hate headache headline headquarters
healthy heaven height hello helmet helpful hero hey hidden highlight highly
highway hill hint hire historic historical hobby holiday holy homeless honest
honey honor hook horizon horrible horror horse host hostage household housing
hug humor hunger hungry hunt hunting hurry ice icon ideal identity ignore ill
illegal illness illustrate imagination immediate immediately immigrant impact
implement implication imply import impose impossible impress impression
impressive improvement incident income incorporate increasingly incredible
independence independent index indoor industrial inevitable infant infection
inflation influence inform initial initially initiative injury inner innocent
innovation input inquiry insect insight insist inspire install instance
instant instruction instrument insurance intellectual intelligence intelligent
intend intense intention interaction interior internal interpret
interpretation interrupt interval introduce introduction invent invention
invest investigate investigation investor invite involved iron island isolated
jacket jail jeans jewelry joint joke journal journalist journey joy judge
judgment juice jump junior jury justice justify keen kick kiss kitchen knee knife
knock label labor laboratory lack ladder lady lake lamp landscape lane lap
largely laser lately latest latter launch laundry layer lazy leading leaf league
lean leather lecture legacy legend lemon lend length lens lesson liberal library
license lid lift lighting likewise limit limited lip liquid literally
literature load loan lobby location lock logic lonely loose lord loud lovely
lover lower loyal luck lucky lunch luxury mad mail mainly maker makeup mall
manner manufacturer map margin marine mark marry mask mass massive master match
mate math maximum meal meaning meanwhile meat mechanism medal medicine medium
melt membership mental menu mere merely mess metal meter middle midnight mild
milk mine minimum minister minor minority miracle mirror missile missing mistake
mix mixture mobile mode moderate modest monitor monkey monster mood moral
moreover mostly motion motivation motor mount mountain movement multiple murder
muscle museum musical musician mutual mystery naked narrative narrow nasty
native naturally neat necessarily neck negative negotiate neighbor neighborhood
neither nephew nervous nest net neutral nevertheless newly nod noise noisy
nominate normal normally northern nose novel nowhere nuclear numerous nurse nut
obey object objective obligation observation observe obtain obvious obviously
occasion occasionally occupation occupy ocean odd odds offense offensive
official ongoing onion opening opera operate operator opinion opponent oppose
opposite opposition orange ordinary organic organize orientation origin
original originally otherwise ought ourselves outcome output overall overcome
overlook owe ownership oxygen pace pack package pain painful paint painter pair
palace pale pan panel panic pants paper parade parking participate particle
partly passage passenger passion passionate password pasta patience patient
patrol pause peak peer pen penalty pencil pension pepper percent percentage
perfect perfectly permanent permission permit personality personally
perspective persuade pet phase phenomenon philosophy photographer phrase pile
pill pilot pink pipe pitch pizza planet plastic plate platform pleasant please
pleased pleasure plenty plot pocket poem poet poetry pole poll pollution pond
pool pop porch portion portrait pose possess possibility possibly post pot
potato potential potentially pound pour poverty powder powerful practical
pray prayer precisely predict prediction prefer preference pregnant
preparation presence presentation preserve press pretend primarily prime
prince princess principal principle print printer prior priority prison
prisoner privacy prize procedure proceed profession profit progress promise
promote promotion prompt proof proper properly proportion proposal propose
prospect protection protein protest proud provider province psychological
publish punch punish pupil purchase pure purple pursue puzzle qualify quarter
queen quick quietly quit quote rabbit racing rail rain random rank rapid
rapidly rare rarely rat rating ratio raw react reaction reader readily
realistic reasonable recall receipt receiver recipe recognition recommend
recommendation recover recovery recruit reduction refer reference reform
refrigerator refuse regard regardless register regret regular regularly
regulation reject relation relative relatively relax release relevant
reliable relief religion rely remark remarkable remind remote rent repair
repeat replace reply representative reputation request rescue reserve resident
resign resist resolution resolve resort respect respond restaurant restore
restriction retail retain retire retirement reverse review revolution reward
rhythm rice ride ring rival river rob robot rocket romantic roof root rope rose
rough roughly round route routine row royal rub rubber rude ruin rumor rural
rush sacred sacrifice sad sadly safety sail salad salary sale salt sample sand
sandwich satellite satisfaction satisfy sauce scale scared scary schedule
scheme scholar scholarship screen script sector secure seed seldom select
selection self sensitive sentence separate sequence settle setup severe shade
shadow shall shame shape sharp shed sheet shelf shell shelter shift shine
shirt shock shoe shop shopping shore shortly shout shower shut shy sick sight
signal signature silence silent silly silver similarly sin sink sir sixth ski
skill sky slave sleep slice slide slight slightly slip slope smart smell smoke
smooth snake snap snow soap soccer soft software soil solar sole solid
solution solve somehow somewhat sophisticated sorry soul soup southern spare
spark speaker species speed spell spicy spin spirit spiritual split spokesman
sponsor spot spray spread square squeeze stable stadium stair stake stamp
stance stare starting status steady steal steel steep stem stick stiff
stomach stone storage storm straight strange stranger strategic straw stream
strength stress stretch strict strike string strip stroke struggle stuck
studio stupid submit substance substantial succeed suck sudden sugar suicide
suit suitable sum summary summit sun sunny super supply supporter suppose
supreme surely surgery surprise surprised surprising surround survey survival
survive suspect suspend sustain swear sweater sweep sweet swing switch symbol
sympathy symptom tablespoon tackle tail tale talent tall tank tap tape target
taste tea teaspoon tear technical technique teen teenager telephone telescope
temperature temple temporary tempt tender tennis tension tent terrible
terribly territory terror terrorist testing textbook thank theater theme
therapy therefore thick thin thirty thoroughly thread threaten throat ticket
tide tie tight timing tiny tip tired tissue title tobacco toe toilet tomato
tone tongue tool tooth topic toss touch tour tourist tournament tower toy
trace track trail train transfer transform transition translate transport
trap trash treasure treaty trend tribe trick troop trophy truck truly trust
tube tune tunnel twelve twenty twin twist typical typically ugly ultimate
ultimately unable uncle unemployment unexpected unfortunately uniform union
unique universal universe university unknown unless unlike unlikely unusual
upper upset urban urge useful user usual vacation valley valuable van variable
variation variety vast vegetable vehicle venture version versus vessel veteran
via vice victory video viewer village violent virtual virtually virtue
visible vision visitor visual vital volume volunteer vulnerable wage wake
wallet wander warm warn warning wash waste wealth wealthy weather wedding
weekend weekly weird welcome welfare wet whale wheel whenever whereas whisper
whoever widely widow wild wildlife willing wine wing winner winter wire wise
witness wolf wooden wool worried worse worst worth wound wrap wrist yell
yellow yesterday yield youth zero zone
"""

# Words the Hunspell list lacks but people type on a handheld. They are added
# as ordinary entries with a mid rank.
SUPPLEMENT = """
it's that's what's let's he's she's there's here's who's where's how's when's
why's don't doesn't didn't isn't aren't wasn't weren't won't wouldn't can't
couldn't shouldn't haven't hasn't hadn't I'm I'll I've I'd you're you've you'll
you'd we're we've we'll we'd they're they've they'll they'd he'll she'll it'll
that'll there'll he'd she'd y'all ain't
ok lol omg thx pls plz idk tbh imo brb gg wp afk irl ttyl np ty yw smh fyi asap
roms spotify Xbox JukaHub TrimUI RetroArch SNES NES GBA PSP PSX DS N64 Switch
emoji emojis wifi hotspot livestream livestreams playthrough speedrun speedruns
"""

# Never suggested, whatever their rank. The Hunspell NOSUGGEST flag already
# drops the worst; this catches the rest a keyboard should not volunteer.
BLOCKLIST = set("""
fuck fucks fucked fucking fucker fuckers motherfucker shit shits shitty shitting
bitch bitches bitchy bastard bastards asshole assholes cunt cunts dick dicks
cock cocks pussy pussies whore whores slut sluts nigger niggers nigga faggot
faggots fag fags retard retards retarded tranny trannies spic spics kike kikes
chink chinks wetback wetbacks dyke dykes twat twats wanker wankers
""".split())

CONTRACTION_S = {"it's", "that's", "what's", "let's", "he's", "she's", "there's",
                 "here's", "who's", "where's", "how's", "when's", "why's"}

# Short entries: the Hunspell list carries every letter of the alphabet and
# hundreds of two-letter abbreviations ("bk", "Gd"), which only ever surface as
# noise in a suggestion strip. One- and two-letter words survive only when they
# are ranked above or listed here.
SHORT_OK = set("""
a I ah aw eh ha hm ho id ma pa ox yo uh um ya ye ad ax ex lo pi
TV PC OK ID UK US EU UN AI AM PM DJ CD VR PS AR IT
""".split())


def parse_aff(path):
    rules = {}
    lines = open(path, encoding="utf-8").read().splitlines()
    i = 0
    while i < len(lines):
        parts = lines[i].split("#")[0].split()
        if len(parts) >= 4 and parts[0] in ("PFX", "SFX") and parts[2] in ("Y", "N") and parts[3].isdigit():
            kind, flag, cross, count = parts[0], parts[1], parts[2] == "Y", int(parts[3])
            entries = []
            for j in range(1, count + 1):
                p = lines[i + j].split()
                strip = "" if p[2] == "0" else p[2]
                add = "" if p[3] == "0" else p[3]
                cond = p[4] if len(p) > 4 else "."
                if cond == ".":
                    rx = None
                elif kind == "SFX":
                    rx = re.compile("(" + cond + ")$")
                else:
                    rx = re.compile("^(" + cond + ")")
                entries.append((strip, add, rx))
            rules[flag] = (kind, cross, entries)
            i += count + 1
            continue
        i += 1
    return rules


def apply(word, kind, entries):
    out = []
    for strip, add, rx in entries:
        if rx is not None and not rx.search(word):
            continue
        if kind == "SFX":
            if strip:
                if not word.endswith(strip):
                    continue
                out.append(word[: -len(strip)] + add)
            else:
                out.append(word + add)
        else:
            if strip:
                if not word.startswith(strip):
                    continue
                out.append(add + word[len(strip):])
            else:
                out.append(add + word)
    return out


def expand(dic_path, rules):
    """Returns {form: (stem, derived)} for every typeable form."""
    forms = {}
    lines = open(dic_path, encoding="utf-8").read().splitlines()[1:]
    for line in lines:
        word, _, flags = line.partition("/")
        word = word.strip()
        if not word or "!" in flags or "c" in flags:
            continue
        if any(ch.isdigit() for ch in word):
            continue
        flags = flags.replace("M", "")  # possessives are not suggestions
        forms.setdefault(word, (word, False))
        for f in flags:
            rule = rules.get(f)
            if rule is None:
                continue
            kind, cross, entries = rule
            for form in apply(word, kind, entries):
                forms.setdefault(form, (word, True))
                if kind == "PFX" and cross:
                    for f2 in flags:
                        r2 = rules.get(f2)
                        if r2 and r2[0] == "SFX" and r2[1]:
                            for both in apply(form, "SFX", r2[2]):
                                forms.setdefault(both, (word, True))
    return forms


def keep(form, ranked_lower):
    if len(form) > 24 or len(form) < 1:
        return False
    if form == "i":
        return False  # the pronoun is "I"; a lower-case entry only competes with it
    if len(form) <= 2 and form not in SHORT_OK:
        if form.lower() != form or form.lower() not in ranked_lower:
            return False
    if form.lower() in BLOCKLIST:
        return False
    if form.endswith("'s") and form.lower() not in CONTRACTION_S:
        return False
    if form.endswith("s'"):
        return False
    return True


def main():
    rules = parse_aff(AFF)
    forms = expand(DIC, rules)
    for w in SUPPLEMENT.split():
        forms.setdefault(w, (w, False))

    ranked = []
    seen = set()
    for w in RANKED.split():
        lw = w.lower()
        if lw in seen:
            continue
        seen.add(lw)
        ranked.append(lw)
    total = len(ranked)

    forms = {w: v for w, v in forms.items() if keep(w, seen)}

    # Lower-case lookup for the ranked list: "i'm" ranks "I'm".
    by_lower = {}
    for w in forms:
        by_lower.setdefault(w.lower(), []).append(w)

    rank = {}
    for pos, lw in enumerate(ranked):
        r = 255 - (pos * 105) // max(total, 1)  # 255 .. 150
        for w in by_lower.get(lw, []):
            # A proper noun that shares a common word's spelling ("Will",
            # "May") keeps a proper noun's rank.
            if w[:1].isupper() and w.lower() != w and lw not in ("i", "i'm", "i'll", "i've", "i'd"):
                continue
            rank[w] = max(rank.get(w, 0), r)

    supplement = set(SUPPLEMENT.split())
    out = []
    for w, (stem, derived) in forms.items():
        r = rank.get(w)
        if r is None:
            stem_rank = rank.get(stem)
            if w in supplement:
                r = 130
            elif stem_rank is not None and derived:
                r = max(90, stem_rank - 45)
            elif w[:1].isupper():
                r = 40
            elif derived:
                r = 70
            else:
                r = 95
            if len(w) > 9:
                r = max(1, r - (len(w) - 9) * 3)
        out.append((w.lower(), w, r))
    out.sort(key=lambda t: (t[0], -t[2], t[1]))
    payload = "".join("%s\t%d\n" % (w, r) for _, w, r in out).encode("utf-8")
    with open(OUT, "wb") as fh:
        fh.write(gzip.compress(payload, 9, mtime=0))
    print("words:", len(out), "ranked:", len(rank), "bytes:", len(payload), "gz:", os.path.getsize(OUT))


if __name__ == "__main__":
    main()
