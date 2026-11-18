package main

// Clock and date formats for the times the panel shows the user.
//
// The settings are the "clock12" toggle and the "dateFormat" choice in
// Settings → Date & time (datetime.go). They are stored in Variables.Custom
// like the other switches, so they persist in jukauser.json and a config
// written by the web builder can set them too. The default is the 24-hour
// clock, which is what every previous build rendered; 12-hour is opt-in and
// adds an upper-case AM/PM suffix. Dates follow the chosen order, or the
// country the connection is in when the choice is Auto.

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

// dateSeen is the clock and date order the main thread last read from a
// config (clockHour12, dateOrder). Code off the main thread must not read the
// config's map, so it passes a nil config and gets these: the user's choices
// once any screen has shown a time or a date, the defaults before that.
var dateSeen struct {
	clock12 atomic.Bool
	order   atomic.Int32 // 1 + the index in dateOrdersExplicit; 0 is Auto
}

// clockHour12 reports whether times are printed with an AM/PM suffix.
func clockHour12(config *Config) bool {
	if config == nil {
		return dateSeen.clock12.Load()
	}
	on := config.Variables.Clock12
	dateSeen.clock12.Store(on)
	return on
}

// clockDigits formats a wall-clock time: "15:04" by default, "3:04 PM" when the
// 12-hour clock is on. The single space before AM/PM keeps the status cluster's
// width predictable as the hour changes. In screenshot mode the present moment
// reads 9:41 (shotClock).
func clockDigits(config *Config, t time.Time) string {
	t = shotClock(t)
	if clockHour12(config) {
		return t.Format("3:04 PM")
	}
	return t.Format("15:04")
}

// clockDigitsSeconds is the clock-app and alarm variant that includes seconds.
func clockDigitsSeconds(config *Config, t time.Time) string {
	t = shotClock(t)
	if clockHour12(config) {
		return t.Format("3:04:05 PM")
	}
	return t.Format("15:04:05")
}

// clockDigitsHM formats an hour and minute that are not a time.Time value — the
// next alarm on the dock, for example — in the same clock as everywhere else.
func clockDigitsHM(config *Config, hour, minute int) string {
	hour = ((hour % 24) + 24) % 24
	minute = ((minute % 60) + 60) % 60
	if !clockHour12(config) {
		return fmt.Sprintf("%02d:%02d", hour, minute)
	}
	suffix := "AM"
	h := hour
	if hour >= 12 {
		suffix = "PM"
	}
	if h > 12 {
		h -= 12
	}
	if h == 0 {
		h = 12
	}
	return fmt.Sprintf("%d:%02d %s", h, minute, suffix)
}

// ── Dates ─────────────────────────────────────────────────────────────────

// dateFormatKey is the Custom setting behind Settings → Date & time → Date
// format. Its values are the dateOrder constants; anything else is Auto.
const dateFormatKey = "dateFormat"

// The orders a date can be written in. Auto ("") follows the country the
// connection is in (weather_units.go), and is day first until that is known.
const (
	dateOrderAuto = ""
	dateOrderDMY  = "dmy"
	dateOrderMDY  = "mdy"
	dateOrderYMD  = "ymd"
	dateOrderDots = "dots"
)

// dateLayouts are one order's Go layouts: Long carries the weekday and the
// full month, Day is Long without the year (the clock faces), Medium is the
// everyday date with the year, and Short leaves the year out. Brief is Short
// after the short weekday ("Mon 9 Nov"), for a date in the coming or the past
// week or two.
type dateLayouts struct {
	Long, Day, Medium, Short, Brief string
}

var (
	dateLayoutsDMY  = dateLayouts{Long: "Monday, 2 January 2006", Day: "Monday, 2 January", Medium: "2 Jan 2006", Short: "2 Jan", Brief: "Mon 2 Jan"}
	dateLayoutsMDY  = dateLayouts{Long: "Monday, January 2, 2006", Day: "Monday, January 2", Medium: "Jan 2, 2006", Short: "Jan 2", Brief: "Mon, Jan 2"}
	dateLayoutsYMD  = dateLayouts{Long: "Monday, 2006-01-02", Day: "Monday, 01-02", Medium: "2006-01-02", Short: "01-02", Brief: "Mon 01-02"}
	dateLayoutsDots = dateLayouts{Long: "Monday, 02.01.2006", Day: "Monday, 02.01.", Medium: "02.01.2006", Short: "02.01.", Brief: "Mon 02.01."}
)

// dateLayoutsFor returns an order's layouts (day first for an unknown one).
// It hands out the shared tables, so a frame that formats a date allocates
// nothing but the string itself.
func dateLayoutsFor(order string) *dateLayouts {
	switch order {
	case dateOrderMDY:
		return &dateLayoutsMDY
	case dateOrderYMD:
		return &dateLayoutsYMD
	case dateOrderDots:
		return &dateLayoutsDots
	}
	return &dateLayoutsDMY
}

// dateOrderValid reports whether order is one of the explicit choices.
func dateOrderValid(order string) bool {
	switch order {
	case dateOrderDMY, dateOrderMDY, dateOrderYMD, dateOrderDots:
		return true
	}
	return false
}

// dateOrderForCountry is the everyday order in a country: month first in the
// United States and the places that follow its usage, year first in East Asia,
// Hungary, Lithuania and Iran, day first everywhere else and while the country
// is not known.
func dateOrderForCountry(code string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "US", "PR", "GU", "VI", "AS", "MP", "UM", "FM", "MH", "PW", "PH":
		return dateOrderMDY
	case "CN", "JP", "KR", "KP", "TW", "MN", "HU", "LT", "IR":
		return dateOrderYMD
	}
	return dateOrderDMY
}

// dateOrdersExplicit are the explicit orders in the order dateSeen counts them.
var dateOrdersExplicit = [...]string{dateOrderDMY, dateOrderMDY, dateOrderYMD, dateOrderDots}

// dateOrder is the order config writes dates in: the stored choice, or for
// Auto the connection's country. With a config it is main thread only (it
// reads the config's map); a nil config, from any goroutine, gets the choice
// the main thread last saw (dateSeen).
func dateOrder(config *Config) string {
	if config == nil {
		if i := dateSeen.order.Load(); i > 0 && int(i) <= len(dateOrdersExplicit) {
			return dateOrdersExplicit[i-1]
		}
		return dateOrderForCountry(weatherCountryCode())
	}
	if config.Variables.Custom != nil {
		if v, ok := config.Variables.Custom[dateFormatKey].(string); ok && dateOrderValid(v) {
			for i, o := range dateOrdersExplicit {
				if o == v {
					dateSeen.order.Store(int32(i + 1))
				}
			}
			return v
		}
	}
	dateSeen.order.Store(0)
	return dateOrderForCountry(weatherCountryCode())
}

// The date helpers below write the month and weekday names in the interface
// language (dateLocale); in English they are exactly t.Format, so the render
// path allocates nothing but the string, as before.

// formatDate writes a date with its year: "9 Nov 2026", "Nov 9, 2026",
// "2026-11-09" or "09.11.2026".
func formatDate(config *Config, t time.Time) string {
	l := dateLocaleNow()
	return l.format(t, dateLayoutsIn(dateOrder(config), l).Medium)
}

// formatDateShort writes a date without its year ("9 Nov", "Nov 9"), for a
// date the reader knows is this year's.
func formatDateShort(config *Config, t time.Time) string {
	l := dateLocaleNow()
	return l.format(t, dateLayoutsIn(dateOrder(config), l).Short)
}

// formatDateBrief writes the short weekday and the date without its year
// ("Mon 9 Nov", "Mon, Nov 9"), for a day in the weeks around this one.
func formatDateBrief(config *Config, t time.Time) string {
	l := dateLocaleNow()
	return l.format(t, dateLayoutsIn(dateOrder(config), l).Brief)
}

// formatDateNear writes a date without its year when it is in now's year and
// with it otherwise: a file's or a bookmark's date, where old ones are common.
func formatDateNear(config *Config, t, now time.Time) string {
	if t.Year() == now.In(t.Location()).Year() {
		return formatDateShort(config, t)
	}
	return formatDate(config, t)
}

// formatDateLong writes the weekday and the full date: "Monday, 9 November
// 2026" or "Monday, November 9, 2026".
func formatDateLong(config *Config, t time.Time) string {
	l := dateLocaleNow()
	return l.format(t, dateLayoutsIn(dateOrder(config), l).Long)
}

// formatDayLong is formatDateLong without the year, for the clock faces that
// show today under the time: "Monday, 9 November". In screenshot mode today
// reads Saturday 14 November (shotClock).
func formatDayLong(config *Config, t time.Time) string {
	l := dateLocaleNow()
	return l.format(shotClock(t), dateLayoutsIn(dateOrder(config), l).Day)
}

// formatMonthYear is a calendar's heading: "November 2026" ("Ноябрь 2026").
func formatMonthYear(t time.Time) string {
	l := dateLocaleNow()
	if l == nil {
		return t.Format("January 2006")
	}
	return l.monthsAlone[t.Month()-1] + " " + strconv.Itoa(t.Year())
}

// formatMonthName is a month's name on its own ("November"), for a picker.
func formatMonthName(m time.Month) string {
	if m < time.January || m > time.December {
		return m.String()
	}
	if l := dateLocaleNow(); l != nil {
		return l.monthsAlone[m-1]
	}
	return m.String()
}

// formatWeekdayShort is a day's short name ("Mon"), for a forecast or a
// week's chart.
func formatWeekdayShort(t time.Time) string {
	if l := dateLocaleNow(); l != nil {
		return l.daysShort[t.Weekday()]
	}
	return t.Format("Mon")
}

// formatWeekdayLong is a day's full name on its own ("Monday", "Lunes"), for
// a heading: capitalised, because es/fr/it/pt/ru write weekdays in lower case
// inside a date. Outside English it allocates, so a per-frame caller keeps
// the result.
func formatWeekdayLong(t time.Time) string {
	l := dateLocaleNow()
	if l == nil {
		return t.Weekday().String()
	}
	s := l.days[t.Weekday()]
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// weekdayInitialsEN are formatWeekdayInitial's English letters, Sunday first.
var weekdayInitialsEN = [7]string{"S", "M", "T", "W", "T", "F", "S"}

// formatWeekdayInitial is a day's one-letter name ("M", "L", "П") for a
// calendar's column heads. It returns a table's string: no allocation.
func formatWeekdayInitial(d time.Weekday) string {
	if d < time.Sunday || d > time.Saturday {
		return ""
	}
	if l := dateLocaleNow(); l != nil {
		return l.daysNarrow[d]
	}
	return weekdayInitialsEN[d]
}

// formatDateTime writes a date and a time in the user's clock: "9 Nov 2026,
// 15:04" or "Nov 9, 2026, 3:04 PM". The old hard-coded "Jan 2, 15:04" layouts
// ignored the 12-hour setting; this one cannot.
func formatDateTime(config *Config, t time.Time) string {
	return formatDate(config, t) + ", " + clockDigits(config, t)
}

// formatDateTimeNear is formatDateTime for a moment the reader relates to now:
// "Today, 15:04", "Yesterday, 15:04", the date without the year this year, the
// full date otherwise. now and t are compared in t's zone.
func formatDateTimeNear(config *Config, t, now time.Time) string {
	now = now.In(t.Location())
	ty, tm, td := t.Date()
	ny, nm, nd := now.Date()
	l := dateLocaleNow()
	switch {
	case ty == ny && tm == nm && td == nd:
		return l.word(0) + ", " + clockDigits(config, t)
	case t.Before(now):
		y, m, d := now.AddDate(0, 0, -1).Date()
		if ty == y && tm == m && td == d {
			return l.word(-1) + ", " + clockDigits(config, t)
		}
	}
	if ty == ny {
		return formatDateShort(config, t) + ", " + clockDigits(config, t)
	}
	return formatDateTime(config, t)
}

// formatTimeNear writes a moment of the coming week the way a schedule reads:
// "Today 03:00", "Tomorrow 03:00", "Mon 09:00" within six days, "12 Dec
// 03:00" later this year and the full date after that.
func formatTimeNear(config *Config, t, now time.Time) string {
	now = now.In(t.Location())
	clock := clockDigits(config, t)
	l := dateLocaleNow()
	midnight := func(x time.Time) time.Time {
		y, m, d := x.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, x.Location())
	}
	// Whole days between the two dates, rounded: a day across a clock change
	// is 23 or 25 hours long.
	span := midnight(t).Sub(midnight(now)).Hours() / 24
	days := int(span + 0.5)
	if span < 0 {
		days = int(span - 0.5)
	}
	switch {
	case days == 0:
		return l.word(0) + " " + clock
	case days == 1:
		return l.word(1) + " " + clock
	case days > 1 && days < 7:
		return formatWeekdayShort(t) + " " + clock
	case t.Year() == now.Year():
		return formatDateShort(config, t) + " " + clock
	}
	return formatDate(config, t) + " " + clock
}

// ── Locale ────────────────────────────────────────────────────────────────

// dateLocale is what the interface language (i18n.go) changes in dates and
// numbers; English is nil (Go's own names, what every earlier build printed).
// The names are CLDR's: months and days in their "format" forms, the ones a
// date with its day uses (Russian months are genitive there, "9 ноября"), and
// monthsAlone the stand-alone month a heading or a picker uses. daysNarrow
// are CLDR's one-letter weekdays, for a calendar's column heads.
type dateLocale struct {
	months, monthsShort, monthsAlone [12]string
	days, daysShort, daysNarrow      [7]string // Sunday first, like time.Weekday
	dmy                              dateLayouts
	near                             [3]string // yesterday, today, tomorrow
	group, decimal                   string
	groupMin                         int // digits a whole number needs before it is grouped
}

// dateLocales holds the languages Settings offers (uiLanguages) besides
// English. Their day-first layouts (dmy) are the everyday ones of the
// language; the other orders use the shared tables with these names. A
// language missing here falls back to English names and separators.
var dateLocales = map[string]*dateLocale{
	"es": {
		months:      [12]string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"},
		monthsShort: [12]string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sept", "oct", "nov", "dic"},
		monthsAlone: [12]string{"Enero", "Febrero", "Marzo", "Abril", "Mayo", "Junio", "Julio", "Agosto", "Septiembre", "Octubre", "Noviembre", "Diciembre"},
		days:        [7]string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"},
		daysShort:   [7]string{"dom", "lun", "mar", "mié", "jue", "vie", "sáb"},
		daysNarrow:  [7]string{"D", "L", "M", "X", "J", "V", "S"},
		dmy:         dateLayouts{Long: "Monday, 2 de January de 2006", Day: "Monday, 2 de January", Medium: "2 Jan 2006", Short: "2 Jan", Brief: "Mon 2 Jan"},
		near:        [3]string{"Ayer", "Hoy", "Mañana"},
		group:       ".",
		decimal:     ",",
		groupMin:    5, // Spanish writes 1234 but 12.345
	},
	"pt-BR": {
		months:      [12]string{"janeiro", "fevereiro", "março", "abril", "maio", "junho", "julho", "agosto", "setembro", "outubro", "novembro", "dezembro"},
		monthsShort: [12]string{"jan.", "fev.", "mar.", "abr.", "mai.", "jun.", "jul.", "ago.", "set.", "out.", "nov.", "dez."},
		monthsAlone: [12]string{"Janeiro", "Fevereiro", "Março", "Abril", "Maio", "Junho", "Julho", "Agosto", "Setembro", "Outubro", "Novembro", "Dezembro"},
		days:        [7]string{"domingo", "segunda-feira", "terça-feira", "quarta-feira", "quinta-feira", "sexta-feira", "sábado"},
		daysShort:   [7]string{"dom.", "seg.", "ter.", "qua.", "qui.", "sex.", "sáb."},
		daysNarrow:  [7]string{"D", "S", "T", "Q", "Q", "S", "S"},
		dmy:         dateLayouts{Long: "Monday, 2 de January de 2006", Day: "Monday, 2 de January", Medium: "2 Jan 2006", Short: "2 Jan", Brief: "Mon, 2 Jan"},
		near:        [3]string{"Ontem", "Hoje", "Amanhã"},
		group:       ".",
		decimal:     ",",
		groupMin:    4,
	},
	"fr": {
		months:      [12]string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"},
		monthsShort: [12]string{"janv.", "févr.", "mars", "avr.", "mai", "juin", "juil.", "août", "sept.", "oct.", "nov.", "déc."},
		monthsAlone: [12]string{"Janvier", "Février", "Mars", "Avril", "Mai", "Juin", "Juillet", "Août", "Septembre", "Octobre", "Novembre", "Décembre"},
		days:        [7]string{"dimanche", "lundi", "mardi", "mercredi", "jeudi", "vendredi", "samedi"},
		daysShort:   [7]string{"dim.", "lun.", "mar.", "mer.", "jeu.", "ven.", "sam."},
		daysNarrow:  [7]string{"D", "L", "M", "M", "J", "V", "S"},
		dmy:         dateLayouts{Long: "Monday 2 January 2006", Day: "Monday 2 January", Medium: "2 Jan 2006", Short: "2 Jan", Brief: "Mon 2 Jan"},
		near:        [3]string{"Hier", "Aujourd’hui", "Demain"},
		group:       "\u00a0", // a no-break space
		decimal:     ",",
		groupMin:    4,
	},
	"de": {
		months:      [12]string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"},
		monthsShort: [12]string{"Jan.", "Feb.", "März", "Apr.", "Mai", "Juni", "Juli", "Aug.", "Sept.", "Okt.", "Nov.", "Dez."},
		monthsAlone: [12]string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"},
		days:        [7]string{"Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"},
		daysShort:   [7]string{"So.", "Mo.", "Di.", "Mi.", "Do.", "Fr.", "Sa."},
		daysNarrow:  [7]string{"S", "M", "D", "M", "D", "F", "S"},
		dmy:         dateLayouts{Long: "Monday, 2. January 2006", Day: "Monday, 2. January", Medium: "2. Jan 2006", Short: "2. Jan", Brief: "Mon, 2. Jan"},
		near:        [3]string{"Gestern", "Heute", "Morgen"},
		group:       ".",
		decimal:     ",",
		groupMin:    4,
	},
	"it": {
		months:      [12]string{"gennaio", "febbraio", "marzo", "aprile", "maggio", "giugno", "luglio", "agosto", "settembre", "ottobre", "novembre", "dicembre"},
		monthsShort: [12]string{"gen", "feb", "mar", "apr", "mag", "giu", "lug", "ago", "set", "ott", "nov", "dic"},
		monthsAlone: [12]string{"Gennaio", "Febbraio", "Marzo", "Aprile", "Maggio", "Giugno", "Luglio", "Agosto", "Settembre", "Ottobre", "Novembre", "Dicembre"},
		days:        [7]string{"domenica", "lunedì", "martedì", "mercoledì", "giovedì", "venerdì", "sabato"},
		daysShort:   [7]string{"dom", "lun", "mar", "mer", "gio", "ven", "sab"},
		daysNarrow:  [7]string{"D", "L", "M", "M", "G", "V", "S"},
		dmy:         dateLayouts{Long: "Monday 2 January 2006", Day: "Monday 2 January", Medium: "2 Jan 2006", Short: "2 Jan", Brief: "Mon 2 Jan"},
		near:        [3]string{"Ieri", "Oggi", "Domani"},
		group:       ".",
		decimal:     ",",
		groupMin:    4,
	},
	"ru": {
		months:      [12]string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"},
		monthsShort: [12]string{"янв.", "февр.", "мар.", "апр.", "мая", "июн.", "июл.", "авг.", "сент.", "окт.", "нояб.", "дек."},
		monthsAlone: [12]string{"Январь", "Февраль", "Март", "Апрель", "Май", "Июнь", "Июль", "Август", "Сентябрь", "Октябрь", "Ноябрь", "Декабрь"},
		days:        [7]string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"},
		daysShort:   [7]string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"},
		daysNarrow:  [7]string{"В", "П", "В", "С", "Ч", "П", "С"},
		dmy:         dateLayouts{Long: "Monday, 2 January 2006 г.", Day: "Monday, 2 January", Medium: "2 Jan 2006 г.", Short: "2 Jan", Brief: "Mon, 2 Jan"},
		near:        [3]string{"Вчера", "Сегодня", "Завтра"},
		group:       "\u00a0",
		decimal:     ",",
		groupMin:    4,
	},
}

// dateLocaleNow is the interface language's conventions, nil for English.
// Any goroutine (the language is an atomic load, the table never changes).
func dateLocaleNow() *dateLocale {
	return dateLocales[uiLanguageCode()]
}

// dateLayoutsIn is dateLayoutsFor in a language: its own day-first layouts
// for the day-first order, the shared tables for the others.
func dateLayoutsIn(order string, l *dateLocale) *dateLayouts {
	if l != nil && order != dateOrderMDY && order != dateOrderYMD && order != dateOrderDots {
		return &l.dmy
	}
	return dateLayoutsFor(order)
}

// word is "Yesterday", "Today" or "Tomorrow" (rel -1, 0, 1) in the language.
func (l *dateLocale) word(rel int) string {
	if rel < -1 || rel > 1 {
		rel = 0
	}
	if l == nil {
		return [3]string{"Yesterday", "Today", "Tomorrow"}[rel+1]
	}
	return l.near[rel+1]
}

// format is t.Format(layout) with the month and weekday names in the
// language; for English (nil) it is t.Format itself. The layouts are this
// file's and the call sites' fixed ones, so their only names are Go's
// January / Jan / Monday / Mon tokens, which it finds the way time.Format
// does ("Jan" and "Mon" count only when no lower-case letter follows). It
// builds in a stack buffer, as time.Format does, so the string is its one
// allocation in every language.
func (l *dateLocale) format(t time.Time, layout string) string {
	if l == nil {
		return t.Format(layout)
	}
	var buf [96]byte
	b := buf[:0]
	for layout != "" {
		at, n, name := -1, 0, ""
		for i := 0; i+3 <= len(layout) && at < 0; i++ {
			switch layout[i : i+3] {
			case "Jan":
				if strings.HasPrefix(layout[i:], "January") {
					at, n, name = i, 7, l.months[t.Month()-1]
				} else if !dateLowerAt(layout, i+3) {
					at, n, name = i, 3, l.monthsShort[t.Month()-1]
				}
			case "Mon":
				if strings.HasPrefix(layout[i:], "Monday") {
					at, n, name = i, 6, l.days[t.Weekday()]
				} else if !dateLowerAt(layout, i+3) {
					at, n, name = i, 3, l.daysShort[t.Weekday()]
				}
			}
		}
		if at < 0 {
			b = t.AppendFormat(b, layout)
			break
		}
		if at > 0 {
			b = t.AppendFormat(b, layout[:at])
		}
		b = append(b, name...)
		layout = layout[at+n:]
	}
	return string(b)
}

// dateLowerAt reports whether s has a lower-case ASCII letter at i.
func dateLowerAt(s string, i int) bool {
	return i < len(s) && s[i] >= 'a' && s[i] <= 'z'
}

// dateMemo keeps one formatted date for the minute it was made in, so a
// screen that draws a date every frame formats it once a minute, and again
// when the language or the date order changes. Main thread only.
type dateMemo struct {
	minute      int64
	lang, order string
	text        string
}

// get is fn(config, now), remade only when the minute, the interface
// language or config's date order differ from the kept one's.
func (m *dateMemo) get(config *Config, now time.Time, fn func(*Config, time.Time) string) string {
	minute := now.Unix() / 60
	lang, order := uiLanguageCode(), dateOrder(config)
	if m.text == "" || m.minute != minute || m.lang != lang || m.order != order {
		m.minute, m.lang, m.order = minute, lang, order
		m.text = fn(config, now)
	}
	return m.text
}

// ── Numbers and sizes ─────────────────────────────────────────────────────

// formatNumber writes a whole number with the language's grouping: "12,345"
// in English, "12.345" in German, "12 345" in French and Russian (a no-break
// space); four-digit numbers are grouped except in Spanish ("1234").
func formatNumber(n int64) string {
	s := strconv.FormatInt(n, 10)
	sep, minDigits := ",", 4
	if l := dateLocaleNow(); l != nil {
		sep, minDigits = l.group, l.groupMin
	}
	digits := s
	if n < 0 {
		digits = s[1:]
	}
	if len(digits) < minDigits {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + (len(digits)-1)/3*len(sep))
	if n < 0 {
		b.WriteByte('-')
	}
	lead := len(digits) % 3
	if lead == 0 {
		lead = 3
	}
	b.WriteString(digits[:lead])
	for i := lead; i < len(digits); i += 3 {
		b.WriteString(sep)
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}

// isLineBreakSpace reports whether a word wrap may break a line at r: any
// Unicode space except the no-break ones (U+00A0, which formatNumber groups
// French and Russian digits with, U+202F and U+2007), so "12 345" stays on
// one line. For strings.FieldsFunc in place of strings.Fields.
func isLineBreakSpace(r rune) bool {
	return unicode.IsSpace(r) && r != '\u00a0' && r != '\u202f' && r != '\u2007'
}

// formatDecimal writes v with prec decimals and the language's decimal
// separator ("1.5", "1,5"). The whole part is not grouped: it is for sizes,
// rates and levels, not for counts (formatNumber).
func formatDecimal(v float64, prec int) string {
	s := strconv.FormatFloat(v, 'f', prec, 64)
	if l := dateLocaleNow(); l != nil && l.decimal != "." {
		s = strings.Replace(s, ".", l.decimal, 1)
	}
	return s
}

// byteUnits are formatBytes' multiples of 1024 from KB up.
var byteUnits = [...]string{" KB", " MB", " GB", " TB", " PB", " EB"}

// formatBytes is how every screen writes a size: whole bytes below 1 KB,
// then one decimal in multiples of 1024 with the names file managers use
// ("512 B", "1.5 MB", "3.0 GB"; "1,5 MB" in German). humanBytes,
// formatInstallBytes and the other size helpers are this function.
func formatBytes(b uint64) string {
	if b < 1024 {
		return strconv.FormatUint(b, 10) + " B"
	}
	v, i := float64(b)/1024, 0
	for v >= 1024 && i < len(byteUnits)-1 {
		v /= 1024
		i++
	}
	// 1023.96 KB would round to "1024.0 KB": that is the next unit.
	if v >= 1023.95 && i < len(byteUnits)-1 {
		v /= 1024
		i++
	}
	return formatDecimal(v, 1) + byteUnits[i]
}

// formatSize is formatBytes for the signed sizes most code holds; a negative
// (unknown or bogus) size is written as a plain count of bytes.
func formatSize(n int64) string {
	if n < 0 {
		return formatNumber(n) + " B"
	}
	return formatBytes(uint64(n))
}

// isClockStatusPart reports whether a header status part is the clock text. The
// home screen treats the clock as its calendar button, so the header has to pick
// it out of the cluster in either clock format: "15:04" and "3:04 PM" both
// qualify, and nothing else in the cluster does.
func isClockStatusPart(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) >= 3 {
		upper := strings.ToUpper(s)
		if strings.HasSuffix(upper, " AM") || strings.HasSuffix(upper, " PM") {
			s = strings.TrimSpace(s[:len(s)-3])
		}
	}
	// Accept "H:MM" and "HH:MM"; the hour may be one digit only in 12-hour mode.
	if len(s) < 4 || len(s) > 5 {
		return false
	}
	colon := strings.IndexByte(s, ':')
	if colon < 1 || colon > 2 || colon != len(s)-3 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if i == colon {
			continue
		}
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	hour, err := strconv.Atoi(s[:colon])
	if err != nil || hour > 23 {
		return false
	}
	minute, err := strconv.Atoi(s[colon+1:])
	if err != nil || minute > 59 {
		return false
	}
	return true
}
