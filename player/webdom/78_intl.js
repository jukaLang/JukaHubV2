// 78_intl.js — a small Intl for goja, which has none.
//
// Modern bundles call Intl at start-up (a price, a date, a "3 days ago", Intl.DateTimeFormat().
// resolvedOptions().timeZone) and a missing Intl kills the whole app before it draws anything. This is enough
// Intl for that: NumberFormat (decimal, percent, currency, unit, compact, significant digits, sign display),
// DateTimeFormat (styles and components), Collator, PluralRules, RelativeTimeFormat, ListFormat,
// DisplayNames, Segmenter and Locale. Every locale formats the way en-US does — resolvedOptions() still
// reports the locale that was asked for — and time zones are UTC or the device's own.

var INTL = Symbol('jukahub.intl');

function intlCanonLocale(tag) {
  var t = tag !== null && typeof tag === 'object' && tag[INTL] && tag[INTL].tag ? tag[INTL].tag : str(tag);
  var parts = t.replace(/_/g, '-').split('-');
  if (!/^[A-Za-z]{2,8}$/.test(parts[0])) throw new RangeError('Incorrect locale information provided');
  parts[0] = parts[0].toLowerCase();
  for (var i = 1; i < parts.length; i++) {
    var p = parts[i];
    if (!/^[A-Za-z0-9]{1,8}$/.test(p)) throw new RangeError('Incorrect locale information provided');
    if (i === 1 && /^[A-Za-z]{4}$/.test(p)) parts[i] = p.charAt(0).toUpperCase() + p.slice(1).toLowerCase();
    else if (/^[A-Za-z]{2}$/.test(p) || /^\d{3}$/.test(p)) parts[i] = p.toUpperCase();
    else parts[i] = p.toLowerCase();
  }
  return parts.join('-');
}

function intlLocaleList(locales) {
  if (locales === undefined) return [];
  var list = typeof locales === 'string' || (locales && typeof locales === 'object' && locales[INTL]) ? [locales] : Array.from(locales);
  var out = [];
  for (var i = 0; i < list.length; i++) {
    var c = intlCanonLocale(list[i]);
    if (out.indexOf(c) < 0) out.push(c);
  }
  return out;
}

function intlLocale(locales) {
  var l = intlLocaleList(locales);
  return l.length ? l[0] : str(envCfg('language', 'en-US'));
}

function intlOpt(o, name, allowed, fallback) {
  var v = o[name];
  if (v === undefined) return fallback;
  v = str(v);
  if (allowed && allowed.indexOf(v) < 0) throw new RangeError('Value ' + v + ' out of range for Intl options property ' + name);
  return v;
}

function intlNum(o, name, min, max, fallback) {
  var v = o[name];
  if (v === undefined) return fallback;
  v = +v;
  if (!(v >= min && v <= max)) throw new RangeError(name + ' value is out of range.');
  return Math.floor(v);
}

function intlClass(name, ctor, methods) {
  ctor.prototype = {};
  mix(ctor.prototype, methods);
  defineProperty(ctor.prototype, 'constructor', { value: ctor, writable: true, configurable: true });
  defineProperty(ctor.prototype, Symbol.toStringTag, { value: 'Intl.' + name, configurable: true });
  defineProperty(ctor, 'name', { value: name, configurable: true });
  ctor.supportedLocalesOf = function (locales) { return intlLocaleList(locales); };
  return ctor;
}

function intlState(o, name) {
  var st = o && o[INTL];
  if (!st || st.kind !== name) throw new TypeError('Method Intl.' + name + '.prototype called on incompatible receiver');
  return st;
}

// ---------------------------------------------------------------- decimal arithmetic on digit strings

// intlDecimal splits a finite non-negative number into its shortest decimal digits D (no leading zeros) and
// the position P of the decimal point: value = 0.D × 10^P. Zero is D = ''.
function intlDecimal(x) {
  var s = String(x);
  var m = /^(\d+)(?:\.(\d+))?(?:e([+-]\d+))?$/.exec(s);
  if (!m) return { d: '', p: 0 };
  var all = m[1] + (m[2] || '');
  var point = m[1].length + (m[3] ? +m[3] : 0);
  var lead = /^0*/.exec(all)[0].length;
  var d = all.slice(lead).replace(/0+$/, '');
  return { d: d, p: d ? point - lead : 0 };
}

// intlRound keeps `keep` digits of D, rounding half away from zero (Intl's default halfExpand).
function intlRound(dec, keep) {
  var d = dec.d, p = dec.p;
  if (keep >= d.length) return { d: d, p: p };
  if (keep < 0) return { d: '', p: 0 };
  var up = d.charCodeAt(keep) >= 53; // '5'
  var kept = d.slice(0, keep);
  if (up) {
    var arr = kept.split('');
    var i = arr.length - 1;
    while (i >= 0 && arr[i] === '9') {
      arr[i] = '0';
      i--;
    }
    if (i < 0) {
      arr.unshift('1');
      p++;
    } else {
      arr[i] = String.fromCharCode(arr[i].charCodeAt(0) + 1);
    }
    kept = arr.join('');
  }
  kept = kept.replace(/0+$/, '');
  return { d: kept, p: kept ? p : 0 };
}

function intlZeros(n) { return n > 0 ? new Array(n + 1).join('0') : ''; }

// intlDigits renders a rounded decimal as integer and fraction digit strings.
function intlDigits(dec, minFrac) {
  var d = dec.d, p = dec.p, ints, frac;
  if (!d) {
    ints = '0';
    frac = '';
  } else if (p <= 0) {
    ints = '0';
    frac = intlZeros(-p) + d;
  } else if (p >= d.length) {
    ints = d + intlZeros(p - d.length);
    frac = '';
  } else {
    ints = d.slice(0, p);
    frac = d.slice(p);
  }
  if (frac.length < minFrac) frac += intlZeros(minFrac - frac.length);
  return { ints: ints, frac: frac };
}

// ---------------------------------------------------------------- NumberFormat

var INTL_CURRENCY_DIGITS = { BHD: 3, BIF: 0, CLP: 0, DJF: 0, GNF: 0, IQD: 3, ISK: 0, JOD: 3, JPY: 0, KMF: 0, KRW: 0,
  KWD: 3, LYD: 3, OMR: 3, PYG: 0, RWF: 0, TND: 3, UGX: 0, VND: 0, VUV: 0, XAF: 0, XOF: 0, XPF: 0 };
var INTL_CURRENCY_SYMBOLS = { USD: '$', EUR: '€', GBP: '£', JPY: '¥', INR: '₹', KRW: '₩', CNY: 'CN¥', CAD: 'CA$', AUD: 'A$',
  NZD: 'NZ$', HKD: 'HK$', MXN: 'MX$', BRL: 'R$', ILS: '₪', VND: '₫', TWD: 'NT$', XAF: 'FCFA', XOF: 'F CFA', PHP: '₱' };
var INTL_CURRENCY_NARROW = { USD: '$', CAD: '$', AUD: '$', NZD: '$', HKD: '$', MXN: '$', TWD: '$', SGD: '$', EUR: '€', GBP: '£',
  JPY: '¥', CNY: '¥', INR: '₹', KRW: '₩', BRL: 'R$', RUB: '₽', UAH: '₴', TRY: '₺', PLN: 'zł', THB: '฿', ILS: '₪', PHP: '₱',
  NGN: '₦', VND: '₫', CHF: 'CHF', SEK: 'kr', NOK: 'kr', DKK: 'kr', CZK: 'Kč' };
var INTL_CURRENCY_NAMES = { USD: 'US dollars', EUR: 'euros', GBP: 'British pounds', JPY: 'Japanese yen', CAD: 'Canadian dollars',
  AUD: 'Australian dollars', CNY: 'Chinese yuan', INR: 'Indian rupees', RUB: 'Russian rubles', CHF: 'Swiss francs',
  MXN: 'Mexican pesos', BRL: 'Brazilian reals', KRW: 'South Korean won', SEK: 'Swedish kronor', UAH: 'Ukrainian hryvnias',
  PLN: 'Polish zlotys' };
var INTL_UNITS = {
  acre: ['ac', 'acre'], bit: ['bit', 'bit'], byte: ['byte', 'byte'], celsius: ['°C', 'degree Celsius'],
  centimeter: ['cm', 'centimeter'], day: ['day', 'day'], degree: ['deg', 'degree'], fahrenheit: ['°F', 'degree Fahrenheit'],
  'fluid-ounce': ['fl oz', 'fluid ounce'], foot: ['ft', 'foot'], gallon: ['gal', 'gallon'], gigabit: ['Gb', 'gigabit'],
  gigabyte: ['GB', 'gigabyte'], gram: ['g', 'gram'], hectare: ['ha', 'hectare'], hour: ['hr', 'hour'], inch: ['in', 'inch'],
  kilobit: ['kb', 'kilobit'], kilobyte: ['kB', 'kilobyte'], kilogram: ['kg', 'kilogram'], kilometer: ['km', 'kilometer'],
  liter: ['L', 'liter'], megabit: ['Mb', 'megabit'], megabyte: ['MB', 'megabyte'], meter: ['m', 'meter'],
  microsecond: ['μs', 'microsecond'], mile: ['mi', 'mile'], 'mile-scandinavian': ['smi', 'mile-scandinavian'],
  milliliter: ['mL', 'milliliter'], millimeter: ['mm', 'millimeter'], millisecond: ['ms', 'millisecond'], minute: ['min', 'minute'],
  month: ['mth', 'month'], nanosecond: ['ns', 'nanosecond'], ounce: ['oz', 'ounce'], percent: ['%', 'percent'],
  petabyte: ['PB', 'petabyte'], pound: ['lb', 'pound'], second: ['sec', 'second'], stone: ['st', 'stone'],
  terabit: ['Tb', 'terabit'], terabyte: ['TB', 'terabyte'], week: ['wk', 'week'], yard: ['yd', 'yard'], year: ['yr', 'year'],
  'kilometer-per-hour': ['km/h', 'kilometer per hour'], 'mile-per-hour': ['mph', 'mile per hour'],
  'meter-per-second': ['m/s', 'meter per second'], 'liter-per-kilometer': ['L/km', 'liter per kilometer'],
  'mile-per-gallon': ['mpg', 'mile per gallon']
};
var INTL_COMPACT = [[12, 'T', ' trillion'], [9, 'B', ' billion'], [6, 'M', ' million'], [3, 'K', ' thousand']];

function intlPlural(word, n) {
  if (n === 1) return word;
  if (/(s|x|ch|sh)$/.test(word)) return word + 'es';
  if (word === 'foot') return 'feet';
  if (word === 'inch') return 'inches';
  if (/ per /.test(word)) return word.replace(/^(\S+)/, function (w) { return intlPlural(w, n); });
  if (/^degree /.test(word)) return word.replace(/^degree/, 'degrees');
  return word + 's';
}

function IntlNumberFormat(locales, options) {
  if (!(this instanceof IntlNumberFormat)) return new IntlNumberFormat(locales, options);
  var o = options === undefined ? {} : Object(options);
  var st = { kind: 'NumberFormat', locale: intlLocale(locales) };
  st.style = intlOpt(o, 'style', ['decimal', 'percent', 'currency', 'unit'], 'decimal');
  st.currency = o.currency !== undefined ? str(o.currency).toUpperCase() : undefined;
  if (st.style === 'currency' && !st.currency) throw new TypeError('Currency code is required with currency style.');
  st.currencyDisplay = intlOpt(o, 'currencyDisplay', ['symbol', 'narrowSymbol', 'code', 'name'], 'symbol');
  st.currencySign = intlOpt(o, 'currencySign', ['standard', 'accounting'], 'standard');
  st.unit = o.unit !== undefined ? str(o.unit) : undefined;
  if (st.style === 'unit' && !st.unit) throw new TypeError('Unit is required with unit style.');
  st.unitDisplay = intlOpt(o, 'unitDisplay', ['short', 'narrow', 'long'], 'short');
  st.notation = intlOpt(o, 'notation', ['standard', 'scientific', 'engineering', 'compact'], 'standard');
  st.compactDisplay = intlOpt(o, 'compactDisplay', ['short', 'long'], 'short');
  st.signDisplay = intlOpt(o, 'signDisplay', ['auto', 'never', 'always', 'exceptZero', 'negative'], 'auto');
  st.minimumIntegerDigits = intlNum(o, 'minimumIntegerDigits', 1, 21, 1);
  var cd = st.style === 'currency' ? (INTL_CURRENCY_DIGITS[st.currency] !== undefined ? INTL_CURRENCY_DIGITS[st.currency] : 2) : 0;
  var defMin = st.style === 'currency' && st.notation !== 'compact' ? cd : 0;
  var defMax = st.style === 'currency' && st.notation !== 'compact' ? Math.max(cd, defMin) : st.style === 'percent' || st.notation === 'compact' ? 0 : 3;
  st.compactRounding = st.notation === 'compact' && o.minimumFractionDigits === undefined && o.maximumFractionDigits === undefined &&
    o.minimumSignificantDigits === undefined && o.maximumSignificantDigits === undefined;
  var mnfd = intlNum(o, 'minimumFractionDigits', 0, 100, undefined);
  var mxfd = intlNum(o, 'maximumFractionDigits', 0, 100, undefined);
  if (mnfd === undefined && mxfd === undefined) {
    mnfd = defMin;
    mxfd = defMax;
  } else if (mnfd === undefined) {
    mnfd = Math.min(defMin, mxfd);
  } else if (mxfd === undefined) {
    mxfd = Math.max(defMax, mnfd);
  } else if (mnfd > mxfd) {
    throw new RangeError('maximumFractionDigits value is out of range.');
  }
  st.minimumFractionDigits = mnfd;
  st.maximumFractionDigits = mxfd;
  if (o.minimumSignificantDigits !== undefined || o.maximumSignificantDigits !== undefined) {
    st.minimumSignificantDigits = intlNum(o, 'minimumSignificantDigits', 1, 21, 1);
    st.maximumSignificantDigits = intlNum(o, 'maximumSignificantDigits', st.minimumSignificantDigits, 21, 21);
  }
  var g = o.useGrouping;
  st.useGrouping = g === undefined ? (st.notation === 'compact' ? 'min2' : 'auto') :
    g === false || g === 'false' ? false : g === 'min2' ? 'min2' : g === 'always' || g === true || g === 'true' ? 'always' : 'auto';
  defineProperty(this, INTL, { value: st });
}

function intlGroup(ints, mode) {
  if (mode === false || ints.length <= 3 || (mode === 'min2' && ints.length < 5)) return [ints];
  var out = [];
  var first = ints.length % 3 || 3;
  out.push(ints.slice(0, first));
  for (var i = first; i < ints.length; i += 3) out.push(ints.slice(i, i + 3));
  return out;
}

// intlNumberParts is the whole NumberFormat pipeline, producing formatToParts() output.
function intlNumberParts(st, value) {
  var parts = [];
  var x;
  if (typeof value === 'bigint') x = Number(value);
  else x = +value;
  var negative = x < 0 || (x === 0 && 1 / x < 0);
  var abs = Math.abs(x);
  if (st.style === 'percent') abs *= 100;
  var numberParts = [];
  var exponent = null, compact = null;
  if (isNaN(abs)) {
    numberParts.push({ type: 'nan', value: 'NaN' });
    negative = false;
  } else if (!isFinite(abs)) {
    numberParts.push({ type: 'infinity', value: '∞' });
  } else {
    var scaled = abs;
    if (st.notation === 'compact') {
      for (var c = 0; c < INTL_COMPACT.length; c++) {
        if (abs >= Math.pow(10, INTL_COMPACT[c][0])) {
          compact = INTL_COMPACT[c];
          scaled = abs / Math.pow(10, compact[0]);
          break;
        }
      }
    } else if (st.notation === 'scientific' || st.notation === 'engineering') {
      var e = abs === 0 ? 0 : Math.floor(Math.log10(abs));
      if (st.notation === 'engineering') e = Math.floor(e / 3) * 3;
      scaled = abs / Math.pow(10, e);
      exponent = e;
    }
    var dec = intlDecimal(scaled);
    var minFrac = st.minimumFractionDigits;
    if (st.minimumSignificantDigits !== undefined) {
      dec = intlRound(dec, st.maximumSignificantDigits);
      minFrac = Math.max(0, st.minimumSignificantDigits - Math.max(dec.p, dec.d ? 1 : 0));
      if (dec.p <= 0 && dec.d) minFrac = Math.max(0, st.minimumSignificantDigits - dec.p);
    } else if (st.compactRounding && compact !== null) {
      dec = intlRound(dec, dec.p >= 2 ? dec.p : 2);
    } else {
      dec = intlRound(dec, dec.p + st.maximumFractionDigits);
    }
    if (compact !== null && dec.d && dec.p > 3) {
      // 999,950 rounds to 1000K: move up a unit.
      for (var c2 = 0; c2 < INTL_COMPACT.length; c2++) {
        if (INTL_COMPACT[c2][0] === compact[0] + 3) {
          compact = INTL_COMPACT[c2];
          dec = { d: dec.d, p: dec.p - 3 };
          break;
        }
      }
    }
    var dg = intlDigits(dec, minFrac);
    var ints = dg.ints;
    if (ints.length < st.minimumIntegerDigits) ints = intlZeros(st.minimumIntegerDigits - ints.length) + ints;
    var groups = intlGroup(ints, st.useGrouping);
    for (var gi = 0; gi < groups.length; gi++) {
      if (gi) numberParts.push({ type: 'group', value: ',' });
      numberParts.push({ type: 'integer', value: groups[gi] });
    }
    if (dg.frac) {
      numberParts.push({ type: 'decimal', value: '.' });
      numberParts.push({ type: 'fraction', value: dg.frac });
    }
    if (!dec.d) negative = negative && st.signDisplay === 'negative' ? false : negative;
    if (exponent !== null) {
      numberParts.push({ type: 'exponentSeparator', value: 'E' });
      if (exponent < 0) numberParts.push({ type: 'exponentMinusSign', value: '-' });
      numberParts.push({ type: 'exponentInteger', value: String(Math.abs(exponent)) });
    }
    if (compact !== null) {
      if (st.compactDisplay === 'long') numberParts.push({ type: 'literal', value: ' ' }, { type: 'compact', value: compact[2].slice(1) });
      else numberParts.push({ type: 'compact', value: compact[1] });
    }
    var isZero = !dec.d;
    if (isZero && (st.signDisplay === 'exceptZero' || st.signDisplay === 'negative')) negative = false;
    if (isZero && st.signDisplay === 'auto' && negative) negative = true;
  }
  var sign = '';
  var zeroish = numberParts.length === 1 && numberParts[0].value === '0';
  switch (st.signDisplay) {
    case 'never': sign = ''; break;
    case 'always': sign = negative ? '-' : '+'; break;
    case 'exceptZero': sign = zeroish ? '' : negative ? '-' : '+'; break;
    default: sign = negative ? '-' : '';
  }
  var accounting = st.style === 'currency' && st.currencySign === 'accounting' && sign === '-';
  if (accounting) parts.push({ type: 'literal', value: '(' });
  else if (sign) parts.push({ type: sign === '-' ? 'minusSign' : 'plusSign', value: sign });
  if (st.style === 'currency') {
    var cur = st.currency;
    if (st.currencyDisplay === 'name') {
      parts = parts.concat(numberParts);
      var count = numberParts.length === 1 && numberParts[0].value === '1' ? 1 : 2;
      var nm = INTL_CURRENCY_NAMES[cur] || cur;
      parts.push({ type: 'literal', value: ' ' }, { type: 'currency', value: count === 1 ? nm.replace(/s$/, '') : nm });
    } else {
      var sym = st.currencyDisplay === 'code' ? null : st.currencyDisplay === 'narrowSymbol' ? (INTL_CURRENCY_NARROW[cur] || INTL_CURRENCY_SYMBOLS[cur]) : INTL_CURRENCY_SYMBOLS[cur];
      if (sym) {
        parts.push({ type: 'currency', value: sym });
      } else {
        parts.push({ type: 'currency', value: cur }, { type: 'literal', value: '\u00a0' });
      }
      parts = parts.concat(numberParts);
    }
  } else {
    parts = parts.concat(numberParts);
  }
  if (accounting) parts.push({ type: 'literal', value: ')' });
  if (st.style === 'percent') parts.push({ type: 'percentSign', value: '%' });
  if (st.style === 'unit') {
    var u = INTL_UNITS[st.unit] || [st.unit, st.unit];
    var one = numberParts.length === 1 && numberParts[0].value === '1';
    if (st.unit === 'percent') parts.push({ type: 'unit', value: '%' });
    else if (st.unitDisplay === 'long') parts.push({ type: 'literal', value: ' ' }, { type: 'unit', value: intlPlural(u[1], one ? 1 : 2) });
    else if (st.unitDisplay === 'narrow') parts.push({ type: 'unit', value: u[0] === 'byte' ? 'B' : u[0] });
    else parts.push({ type: 'literal', value: ' ' }, { type: 'unit', value: (u[0] === 'day' || u[0] === 'byte') && !one ? u[0] + 's' : u[0] });
  }
  return parts;
}

intlClass('NumberFormat', IntlNumberFormat, {
  get format() {
    var st = intlState(this, 'NumberFormat');
    if (!st.bound) st.bound = function (v) { return intlNumberParts(st, v).map(function (p) { return p.value; }).join(''); };
    return st.bound;
  },
  formatToParts: function (v) { return intlNumberParts(intlState(this, 'NumberFormat'), v); },
  formatRange: function (a, b) { var f = this.format; return f(a) + '–' + f(b); },
  formatRangeToParts: function (a, b) {
    var st = intlState(this, 'NumberFormat');
    return intlNumberParts(st, a).concat([{ type: 'literal', value: '–' }], intlNumberParts(st, b));
  },
  resolvedOptions: function () {
    var st = intlState(this, 'NumberFormat');
    var out = { locale: st.locale, numberingSystem: 'latn', style: st.style };
    if (st.style === 'currency') {
      out.currency = st.currency;
      out.currencyDisplay = st.currencyDisplay;
      out.currencySign = st.currencySign;
    }
    if (st.style === 'unit') {
      out.unit = st.unit;
      out.unitDisplay = st.unitDisplay;
    }
    out.minimumIntegerDigits = st.minimumIntegerDigits;
    out.minimumFractionDigits = st.minimumFractionDigits;
    out.maximumFractionDigits = st.maximumFractionDigits;
    if (st.minimumSignificantDigits !== undefined) {
      out.minimumSignificantDigits = st.minimumSignificantDigits;
      out.maximumSignificantDigits = st.maximumSignificantDigits;
    }
    out.useGrouping = st.useGrouping;
    out.notation = st.notation;
    if (st.notation === 'compact') out.compactDisplay = st.compactDisplay;
    out.signDisplay = st.signDisplay;
    out.roundingIncrement = 1;
    out.roundingMode = 'halfExpand';
    out.roundingPriority = 'auto';
    out.trailingZeroDisplay = 'auto';
    return out;
  }
});

// ---------------------------------------------------------------- DateTimeFormat

var INTL_MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'];
var INTL_DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
var INTL_DATE_FIELDS = ['weekday', 'era', 'year', 'month', 'day', 'dayPeriod', 'hour', 'minute', 'second', 'fractionalSecondDigits', 'timeZoneName'];

function intlZoneUTC(z) { return /^(utc|etc\/utc|gmt|etc\/gmt|z|zulu|universal|etc\/universal)$/i.test(z); }

function intlFixedZone(z) {
  var m = /^etc\/gmt([+-])(\d{1,2})$/i.exec(z);
  return m ? (m[1] === '+' ? -1 : 1) * +m[2] * 60 : null;
}

function IntlDateTimeFormat(locales, options) {
  if (!(this instanceof IntlDateTimeFormat)) return new IntlDateTimeFormat(locales, options);
  var o = options === undefined ? {} : Object(options);
  var st = { kind: 'DateTimeFormat', locale: intlLocale(locales) };
  var local = str(envCfg('timeZone', 'UTC'));
  st.timeZone = o.timeZone !== undefined ? str(o.timeZone) : local;
  if (intlZoneUTC(st.timeZone)) {
    st.timeZone = 'UTC';
    st.offset = 0;
  } else if (intlFixedZone(st.timeZone) !== null) {
    st.offset = intlFixedZone(st.timeZone);
  } else if (st.timeZone === local && intlZoneUTC(local)) {
    st.offset = 0;
  } else {
    st.offset = null; // the device's local zone (or one this browser cannot convert to)
  }
  st.dateStyle = intlOpt(o, 'dateStyle', ['full', 'long', 'medium', 'short'], undefined);
  st.timeStyle = intlOpt(o, 'timeStyle', ['full', 'long', 'medium', 'short'], undefined);
  var any = false;
  st.f = {};
  var allowed = {
    weekday: ['narrow', 'short', 'long'], era: ['narrow', 'short', 'long'], year: ['numeric', '2-digit'],
    month: ['numeric', '2-digit', 'narrow', 'short', 'long'], day: ['numeric', '2-digit'], dayPeriod: ['narrow', 'short', 'long'],
    hour: ['numeric', '2-digit'], minute: ['numeric', '2-digit'], second: ['numeric', '2-digit'],
    timeZoneName: ['short', 'long', 'shortOffset', 'longOffset', 'shortGeneric', 'longGeneric']
  };
  for (var k in allowed) {
    var v = intlOpt(o, k, allowed[k], undefined);
    if (v !== undefined) {
      st.f[k] = v;
      if (k !== 'timeZoneName') any = true;
    }
  }
  if (o.fractionalSecondDigits !== undefined) {
    st.f.fractionalSecondDigits = intlNum(o, 'fractionalSecondDigits', 1, 3, 1);
    any = true;
  }
  if ((st.dateStyle || st.timeStyle) && any) throw new TypeError("Can't set option " + 'dateStyle/timeStyle together with individual fields');
  if (!any && !st.dateStyle && !st.timeStyle) {
    st.f.year = 'numeric';
    st.f.month = 'numeric';
    st.f.day = 'numeric';
  }
  var hc = intlOpt(o, 'hourCycle', ['h11', 'h12', 'h23', 'h24'], undefined);
  if (o.hour12 !== undefined) hc = o.hour12 ? 'h12' : 'h23';
  st.hourCycle = hc || 'h12';
  defineProperty(this, INTL, { value: st });
}

function intlFields(st, t) {
  var d = new Date(t);
  if (st.offset === null) {
    return { y: d.getFullYear(), mo: d.getMonth(), d: d.getDate(), wd: d.getDay(), h: d.getHours(), mi: d.getMinutes(), s: d.getSeconds(),
      ms: d.getMilliseconds(), off: -d.getTimezoneOffset() };
  }
  var u = new Date(t + st.offset * 60000);
  return { y: u.getUTCFullYear(), mo: u.getUTCMonth(), d: u.getUTCDate(), wd: u.getUTCDay(), h: u.getUTCHours(), mi: u.getUTCMinutes(),
    s: u.getUTCSeconds(), ms: u.getUTCMilliseconds(), off: st.offset };
}

function intlPad2(n) { return n < 10 ? '0' + n : String(n); }

function intlZoneName(st, v, style) {
  if (st.timeZone === 'UTC') return style === 'long' ? 'Coordinated Universal Time' : 'UTC';
  var off = v.off;
  if (off === 0) return style === 'long' || style === 'longOffset' ? 'GMT' : 'GMT';
  var sign = off < 0 ? '-' : '+';
  var a = Math.abs(off), h = Math.floor(a / 60), m = a % 60;
  if (style === 'long' || style === 'longOffset') return 'GMT' + sign + intlPad2(h) + ':' + intlPad2(m);
  return 'GMT' + sign + h + (m ? ':' + intlPad2(m) : '');
}

function intlStyleFields(st) {
  var f = {};
  switch (st.dateStyle) {
    case 'full': f.weekday = 'long'; f.month = 'long'; f.day = 'numeric'; f.year = 'numeric'; break;
    case 'long': f.month = 'long'; f.day = 'numeric'; f.year = 'numeric'; break;
    case 'medium': f.month = 'short'; f.day = 'numeric'; f.year = 'numeric'; break;
    case 'short': f.month = 'numeric'; f.day = 'numeric'; f.year = '2-digit'; break;
  }
  switch (st.timeStyle) {
    case 'full': f.hour = 'numeric'; f.minute = '2-digit'; f.second = '2-digit'; f.timeZoneName = 'long'; break;
    case 'long': f.hour = 'numeric'; f.minute = '2-digit'; f.second = '2-digit'; f.timeZoneName = 'short'; break;
    case 'medium': f.hour = 'numeric'; f.minute = '2-digit'; f.second = '2-digit'; break;
    case 'short': f.hour = 'numeric'; f.minute = '2-digit'; break;
  }
  return f;
}

function intlDateParts(st, date) {
  var t = date === undefined ? +N.now() : +date;
  if (!isFinite(t)) throw new RangeError('Invalid time value');
  var v = intlFields(st, t);
  var f = st.dateStyle || st.timeStyle ? intlStyleFields(st) : st.f;
  var parts = [];
  var lit = function (s) { parts.push({ type: 'literal', value: s }); };
  var push = function (type, value) { parts.push({ type: type, value: value }); };
  var textual = f.month === 'long' || f.month === 'short' || f.month === 'narrow';
  var dateBits = 0;
  if (f.weekday) {
    var wd = INTL_DAYS[v.wd];
    push('weekday', f.weekday === 'long' ? wd : f.weekday === 'short' ? wd.slice(0, 3) : wd.charAt(0));
    dateBits++;
  }
  var year = f.year === '2-digit' ? intlPad2(v.y % 100) : String(v.y);
  if (textual) {
    if (dateBits) lit(', ');
    var mn = INTL_MONTHS[v.mo];
    push('month', f.month === 'long' ? mn : f.month === 'short' ? mn.slice(0, 3) : mn.charAt(0));
    if (f.day) {
      lit(' ');
      push('day', f.day === '2-digit' ? intlPad2(v.d) : String(v.d));
    }
    if (f.year) {
      lit(f.day ? ', ' : ' ');
      push('year', year);
    }
    dateBits++;
  } else if (f.month || f.day || f.year) {
    if (dateBits) lit(', ');
    var seq = [];
    if (f.month) seq.push(['month', f.month === '2-digit' ? intlPad2(v.mo + 1) : String(v.mo + 1)]);
    if (f.day) seq.push(['day', f.day === '2-digit' ? intlPad2(v.d) : String(v.d)]);
    if (f.year) seq.push(['year', year]);
    for (var i = 0; i < seq.length; i++) {
      if (i) lit('/');
      push(seq[i][0], seq[i][1]);
    }
    dateBits++;
  }
  if (f.era) {
    lit(' ');
    push('era', f.era === 'long' ? (v.y > 0 ? 'Anno Domini' : 'Before Christ') : f.era === 'narrow' ? (v.y > 0 ? 'A' : 'B') : (v.y > 0 ? 'AD' : 'BC'));
  }
  if (f.hour || f.minute || f.second) {
    if (dateBits) lit(', ');
    var h12 = st.hourCycle === 'h12' || st.hourCycle === 'h11';
    if (f.hour) {
      var h = v.h;
      if (h12) {
        h = h % 12;
        if (h === 0 && st.hourCycle === 'h12') h = 12;
      } else if (st.hourCycle === 'h24' && h === 0) {
        h = 24;
      }
      push('hour', f.hour === '2-digit' || (!h12 && f.minute) ? intlPad2(h) : String(h));
    }
    if (f.minute) {
      if (f.hour) lit(':');
      push('minute', f.hour || f.minute === '2-digit' ? intlPad2(v.mi) : String(v.mi));
    }
    if (f.second) {
      if (f.hour || f.minute) lit(':');
      push('second', f.hour || f.minute || f.second === '2-digit' ? intlPad2(v.s) : String(v.s));
    }
    if (f.fractionalSecondDigits) {
      lit('.');
      push('fractionalSecond', String(1000 + v.ms).slice(1, 1 + f.fractionalSecondDigits));
    }
    if (f.hour && h12) {
      lit(' ');
      push('dayPeriod', v.h < 12 ? 'AM' : 'PM');
    }
  } else if (f.fractionalSecondDigits) {
    push('fractionalSecond', String(1000 + v.ms).slice(1, 1 + f.fractionalSecondDigits));
  }
  if (f.timeZoneName) {
    lit(' ');
    push('timeZoneName', intlZoneName(st, v, f.timeZoneName));
  }
  return parts;
}

intlClass('DateTimeFormat', IntlDateTimeFormat, {
  get format() {
    var st = intlState(this, 'DateTimeFormat');
    if (!st.bound) st.bound = function (d) { return intlDateParts(st, d).map(function (p) { return p.value; }).join(''); };
    return st.bound;
  },
  formatToParts: function (d) { return intlDateParts(intlState(this, 'DateTimeFormat'), d); },
  formatRange: function (a, b) { var f = this.format; return f(a) + ' – ' + f(b); },
  formatRangeToParts: function (a, b) {
    var st = intlState(this, 'DateTimeFormat');
    return intlDateParts(st, a).concat([{ type: 'literal', value: ' – ', source: 'shared' }], intlDateParts(st, b));
  },
  resolvedOptions: function () {
    var st = intlState(this, 'DateTimeFormat');
    var out = { locale: st.locale, calendar: 'gregory', numberingSystem: 'latn', timeZone: st.timeZone };
    var f = st.dateStyle || st.timeStyle ? intlStyleFields(st) : st.f;
    if (f.hour) {
      out.hourCycle = st.hourCycle;
      out.hour12 = st.hourCycle === 'h12' || st.hourCycle === 'h11';
    }
    for (var i = 0; i < INTL_DATE_FIELDS.length; i++) {
      var k = INTL_DATE_FIELDS[i];
      if (!st.dateStyle && !st.timeStyle && f[k] !== undefined) out[k] = f[k];
    }
    if (st.dateStyle) out.dateStyle = st.dateStyle;
    if (st.timeStyle) out.timeStyle = st.timeStyle;
    return out;
  }
});

// ---------------------------------------------------------------- Collator

function IntlCollator(locales, options) {
  if (!(this instanceof IntlCollator)) return new IntlCollator(locales, options);
  var o = options === undefined ? {} : Object(options);
  var st = { kind: 'Collator', locale: intlLocale(locales) };
  st.usage = intlOpt(o, 'usage', ['sort', 'search'], 'sort');
  st.sensitivity = intlOpt(o, 'sensitivity', ['base', 'accent', 'case', 'variant'], 'variant');
  st.ignorePunctuation = !!o.ignorePunctuation;
  st.numeric = !!o.numeric;
  st.caseFirst = intlOpt(o, 'caseFirst', ['upper', 'lower', 'false'], 'false');
  defineProperty(this, INTL, { value: st });
}

var INTL_MARKS = /[\u0300-\u036f]/g;
var INTL_PUNCT = /[\s!-\/:-@\[-`{-~\u2000-\u206f]/g;
var INTL_RUNS = /(\d+)|(\D+)/g;
var INTL_DIGIT = /^\d/;
var INTL_ZEROS = /^0+/;
var INTL_ASCII_PLAIN = /^[\x20-\x7e]*$/;
// Folded keys are memoised (bounded): a sort compares each string many times, and folding runs
// normalize/replace/toLowerCase, which an interpreter pays for on every call.
var intlFoldCache = objCreate(null);
var intlFoldCount = 0;
function intlFold(s, accents, cases, punct) {
  var t = str(s);
  var key = (accents ? 'a' : '-') + (cases ? 'c' : '-') + (punct ? 'p' : '-') + t;
  var hit = intlFoldCache[key];
  if (hit !== undefined) return hit;
  var out = t;
  // ASCII text has no combining marks to strip.
  if (!accents && !INTL_ASCII_PLAIN.test(out)) {
    try { out = out.normalize('NFD').replace(INTL_MARKS, ''); } catch (e) { /* normalize missing */ }
  }
  if (!cases) out = out.toLowerCase();
  if (punct) out = out.replace(INTL_PUNCT, '');
  if (t.length <= 256) {
    if (intlFoldCount >= 4096) {
      intlFoldCache = objCreate(null);
      intlFoldCount = 0;
    }
    intlFoldCache[key] = out;
    intlFoldCount++;
  }
  return out;
}

function intlCompareKeys(a, b, numeric) {
  if (!numeric) return a < b ? -1 : a > b ? 1 : 0;
  var pa = a.match(INTL_RUNS) || [], pb = b.match(INTL_RUNS) || [];
  for (var i = 0; i < pa.length && i < pb.length; i++) {
    var x = pa[i], y = pb[i];
    var dx = INTL_DIGIT.test(x), dy = INTL_DIGIT.test(y);
    if (dx && dy) {
      var nx = x.replace(INTL_ZEROS, ''), ny = y.replace(INTL_ZEROS, '');
      if (nx.length !== ny.length) return nx.length < ny.length ? -1 : 1;
      if (nx !== ny) return nx < ny ? -1 : 1;
    } else if (x !== y) {
      return x < y ? -1 : 1;
    }
  }
  return pa.length === pb.length ? 0 : pa.length < pb.length ? -1 : 1;
}

function intlCollate(st, a, b) {
  var p = st.ignorePunctuation;
  var r = intlCompareKeys(intlFold(a, false, false, p), intlFold(b, false, false, p), st.numeric);
  if (r || st.sensitivity === 'base') return r;
  if (st.sensitivity === 'accent' || st.sensitivity === 'variant') {
    r = intlCompareKeys(intlFold(a, true, false, p), intlFold(b, true, false, p), st.numeric);
    if (r || st.sensitivity === 'accent') return r;
  }
  // Case: lower before upper unless caseFirst says otherwise.
  var ca = intlFold(a, st.sensitivity === 'variant', true, p), cb = intlFold(b, st.sensitivity === 'variant', true, p);
  if (ca === cb) return 0;
  for (var i = 0; i < ca.length && i < cb.length; i++) {
    var x = ca.charAt(i), y = cb.charAt(i);
    if (x === y) continue;
    var xu = x !== x.toLowerCase(), yu = y !== y.toLowerCase();
    if (xu !== yu) return (xu ? 1 : -1) * (st.caseFirst === 'upper' ? -1 : 1);
    return x < y ? -1 : 1;
  }
  return ca.length < cb.length ? -1 : ca.length > cb.length ? 1 : 0;
}

// The engine's own String.prototype.localeCompare (goja's is a Go Unicode collator, root collation) does the
// default comparison better and far faster than intlCollate; it is captured before the shim touches it.
var intlNativeCompare = typeof String.prototype.localeCompare === 'function' ? String.prototype.localeCompare : null;
function intlDefaultOptions(st) {
  return st.sensitivity === 'variant' && !st.numeric && !st.ignorePunctuation && st.caseFirst === 'false';
}
intlClass('Collator', IntlCollator, {
  get compare() {
    var st = intlState(this, 'Collator');
    if (!st.bound) {
      st.bound = intlNativeCompare && intlDefaultOptions(st) ?
        function (a, b) { return intlNativeCompare.call(str(a), str(b)); } :
        function (a, b) { return intlCollate(st, a, b); };
    }
    return st.bound;
  },
  resolvedOptions: function () {
    var st = intlState(this, 'Collator');
    return { locale: st.locale, usage: st.usage, sensitivity: st.sensitivity, ignorePunctuation: st.ignorePunctuation,
      collation: 'default', numeric: st.numeric, caseFirst: st.caseFirst };
  }
});

// ---------------------------------------------------------------- PluralRules

function IntlPluralRules(locales, options) {
  if (!(this instanceof IntlPluralRules)) throw new TypeError("Constructor Intl.PluralRules requires 'new'");
  var o = options === undefined ? {} : Object(options);
  var st = { kind: 'PluralRules', locale: intlLocale(locales), type: intlOpt(o, 'type', ['cardinal', 'ordinal'], 'cardinal') };
  st.nf = new IntlNumberFormat(locales, { minimumFractionDigits: o.minimumFractionDigits, maximumFractionDigits: o.maximumFractionDigits });
  defineProperty(this, INTL, { value: st });
}

function intlPluralSelect(st, n) {
  var x = +n;
  if (!isFinite(x)) return 'other';
  var shown = st.nf.format(Math.abs(x)).replace(/,/g, '');
  if (st.type === 'ordinal') {
    var i = Math.floor(Math.abs(x)), m10 = i % 10, m100 = i % 100;
    if (m10 === 1 && m100 !== 11) return 'one';
    if (m10 === 2 && m100 !== 12) return 'two';
    if (m10 === 3 && m100 !== 13) return 'few';
    return 'other';
  }
  return shown === '1' ? 'one' : 'other';
}

intlClass('PluralRules', IntlPluralRules, {
  select: function (n) { return intlPluralSelect(intlState(this, 'PluralRules'), n); },
  selectRange: function (a, b) { return intlPluralSelect(intlState(this, 'PluralRules'), b); },
  resolvedOptions: function () {
    var st = intlState(this, 'PluralRules');
    var nfo = st.nf.resolvedOptions();
    return { locale: st.locale, type: st.type, minimumIntegerDigits: 1, minimumFractionDigits: nfo.minimumFractionDigits,
      maximumFractionDigits: nfo.maximumFractionDigits,
      pluralCategories: st.type === 'ordinal' ? ['few', 'one', 'two', 'other'] : ['one', 'other'], roundingIncrement: 1,
      roundingMode: 'halfExpand', roundingPriority: 'auto', trailingZeroDisplay: 'auto' };
  }
});

// ---------------------------------------------------------------- RelativeTimeFormat

var INTL_RT_UNITS = ['year', 'quarter', 'month', 'week', 'day', 'hour', 'minute', 'second'];
var INTL_RT_SHORT = { year: 'yr.', quarter: 'qtr.', month: 'mo.', week: 'wk.', day: 'day', hour: 'hr.', minute: 'min.', second: 'sec.' };
var INTL_RT_SHORT_PLURAL = { year: 'yr.', quarter: 'qtrs.', month: 'mo.', week: 'wk.', day: 'days', hour: 'hr.', minute: 'min.', second: 'sec.' };

function IntlRelativeTimeFormat(locales, options) {
  if (!(this instanceof IntlRelativeTimeFormat)) throw new TypeError("Constructor Intl.RelativeTimeFormat requires 'new'");
  var o = options === undefined ? {} : Object(options);
  defineProperty(this, INTL, {
    value: {
      kind: 'RelativeTimeFormat', locale: intlLocale(locales), style: intlOpt(o, 'style', ['long', 'short', 'narrow'], 'long'),
      numeric: intlOpt(o, 'numeric', ['always', 'auto'], 'always'), nf: new IntlNumberFormat(locales)
    }
  });
}

function intlRTParts(st, value, unit) {
  var u = str(unit).replace(/s$/, '');
  if (INTL_RT_UNITS.indexOf(u) < 0) throw new RangeError('Invalid unit argument for format() \'' + str(unit) + '\'');
  var v = +value;
  if (!isFinite(v)) throw new RangeError('Invalid number value');
  if (st.numeric === 'auto') {
    var word = null;
    if (u === 'day') word = { '-1': 'yesterday', '0': 'today', '1': 'tomorrow' }[String(v)];
    else if (u === 'second' && v === 0) word = 'now';
    else if ((u === 'minute' || u === 'hour') && v === 0) word = 'this ' + u;
    else if (v === -1 || v === 0 || v === 1) word = (v === -1 ? 'last ' : v === 0 ? 'this ' : 'next ') + u;
    if (word) return [{ type: 'literal', value: word }];
  }
  var neg = v < 0 || (v === 0 && 1 / v < 0);
  var abs = Math.abs(v);
  var numParts = intlNumberParts(intlState(st.nf, 'NumberFormat'), abs).map(function (p) {
    return { type: p.type, value: p.value, unit: u };
  });
  var label;
  if (st.style === 'long') label = abs === 1 ? u : u + 's';
  else label = abs === 1 ? INTL_RT_SHORT[u] : INTL_RT_SHORT_PLURAL[u];
  if (neg) return numParts.concat([{ type: 'literal', value: ' ' + label + ' ago' }]);
  return [{ type: 'literal', value: 'in ' }].concat(numParts, [{ type: 'literal', value: ' ' + label }]);
}

intlClass('RelativeTimeFormat', IntlRelativeTimeFormat, {
  format: function (value, unit) {
    return intlRTParts(intlState(this, 'RelativeTimeFormat'), value, unit).map(function (p) { return p.value; }).join('');
  },
  formatToParts: function (value, unit) { return intlRTParts(intlState(this, 'RelativeTimeFormat'), value, unit); },
  resolvedOptions: function () {
    var st = intlState(this, 'RelativeTimeFormat');
    return { locale: st.locale, style: st.style, numeric: st.numeric, numberingSystem: 'latn' };
  }
});

// ---------------------------------------------------------------- ListFormat

function IntlListFormat(locales, options) {
  if (!(this instanceof IntlListFormat)) throw new TypeError("Constructor Intl.ListFormat requires 'new'");
  var o = options === undefined ? {} : Object(options);
  defineProperty(this, INTL, {
    value: {
      kind: 'ListFormat', locale: intlLocale(locales), type: intlOpt(o, 'type', ['conjunction', 'disjunction', 'unit'], 'conjunction'),
      style: intlOpt(o, 'style', ['long', 'short', 'narrow'], 'long')
    }
  });
}

function intlListParts(st, list) {
  var items = Array.from(list || []).map(function (x) {
    if (typeof x !== 'string') throw new TypeError('Iterable yielded ' + String(x) + ' which is not a string');
    return x;
  });
  var parts = [];
  var word = st.type === 'conjunction' ? (st.style === 'long' ? 'and' : st.style === 'short' ? '&' : '') :
    st.type === 'disjunction' ? 'or' : '';
  var sep = st.type === 'unit' && st.style === 'narrow' ? ' ' : ', ';
  for (var i = 0; i < items.length; i++) {
    if (i > 0) {
      var last = i === items.length - 1;
      var lit;
      if (!last || !word) lit = sep;
      else lit = items.length === 2 ? ' ' + word + ' ' : ', ' + word + ' ';
      parts.push({ type: 'literal', value: lit });
    }
    parts.push({ type: 'element', value: items[i] });
  }
  return parts;
}

intlClass('ListFormat', IntlListFormat, {
  format: function (list) { return intlListParts(intlState(this, 'ListFormat'), list).map(function (p) { return p.value; }).join(''); },
  formatToParts: function (list) { return intlListParts(intlState(this, 'ListFormat'), list); },
  resolvedOptions: function () {
    var st = intlState(this, 'ListFormat');
    return { locale: st.locale, type: st.type, style: st.style };
  }
});

// ---------------------------------------------------------------- DisplayNames

var INTL_NAMES = {
  region: { US: 'United States', GB: 'United Kingdom', CA: 'Canada', AU: 'Australia', DE: 'Germany', FR: 'France', ES: 'Spain',
    IT: 'Italy', JP: 'Japan', CN: 'China', IN: 'India', BR: 'Brazil', MX: 'Mexico', RU: 'Russia', KR: 'South Korea',
    NL: 'Netherlands', SE: 'Sweden', NO: 'Norway', PL: 'Poland', UA: 'Ukraine', TR: 'Türkiye', CH: 'Switzerland', IE: 'Ireland' },
  language: { en: 'English', es: 'Spanish', fr: 'French', de: 'German', it: 'Italian', pt: 'Portuguese', ru: 'Russian',
    ja: 'Japanese', zh: 'Chinese', ko: 'Korean', ar: 'Arabic', hi: 'Hindi', nl: 'Dutch', sv: 'Swedish', pl: 'Polish',
    uk: 'Ukrainian', tr: 'Turkish', 'en-US': 'American English', 'en-GB': 'British English' },
  currency: { USD: 'US Dollar', EUR: 'Euro', GBP: 'British Pound', JPY: 'Japanese Yen', CAD: 'Canadian Dollar',
    AUD: 'Australian Dollar', CNY: 'Chinese Yuan', INR: 'Indian Rupee', RUB: 'Russian Ruble', CHF: 'Swiss Franc' },
  script: { Latn: 'Latin', Cyrl: 'Cyrillic', Arab: 'Arabic', Hans: 'Simplified Han', Hant: 'Traditional Han' }
};

function IntlDisplayNames(locales, options) {
  if (!(this instanceof IntlDisplayNames)) throw new TypeError("Constructor Intl.DisplayNames requires 'new'");
  var o = options === undefined ? {} : Object(options);
  var type = intlOpt(o, 'type', ['language', 'region', 'script', 'currency', 'calendar', 'dateTimeField'], undefined);
  if (!type) throw new TypeError('Required option type is missing');
  defineProperty(this, INTL, {
    value: { kind: 'DisplayNames', locale: intlLocale(locales), type: type, style: intlOpt(o, 'style', ['narrow', 'short', 'long'], 'long'),
      fallback: intlOpt(o, 'fallback', ['code', 'none'], 'code') }
  });
}
intlClass('DisplayNames', IntlDisplayNames, {
  of: function (code) {
    var st = intlState(this, 'DisplayNames');
    var c = str(code);
    var table = INTL_NAMES[st.type] || {};
    var key = st.type === 'region' || st.type === 'currency' ? c.toUpperCase() : c;
    if (table[key]) return table[key];
    if (st.type === 'language' && table[key.split('-')[0]]) return table[key.split('-')[0]];
    return st.fallback === 'none' ? undefined : key;
  },
  resolvedOptions: function () {
    var st = intlState(this, 'DisplayNames');
    return { locale: st.locale, style: st.style, type: st.type, fallback: st.fallback };
  }
});

// ---------------------------------------------------------------- Segmenter

function IntlSegmenter(locales, options) {
  if (!(this instanceof IntlSegmenter)) throw new TypeError("Constructor Intl.Segmenter requires 'new'");
  var o = options === undefined ? {} : Object(options);
  defineProperty(this, INTL, {
    value: { kind: 'Segmenter', locale: intlLocale(locales), granularity: intlOpt(o, 'granularity', ['grapheme', 'word', 'sentence'], 'grapheme') }
  });
}

var INTL_EXTEND = /^[\u0300-\u036f\u1ab0-\u1aff\u1dc0-\u1dff\u20d0-\u20ff\ufe00-\ufe0f\ufe20-\ufe2f\u200d]$/;

function intlSegments(st, input) {
  var s = str(input);
  var out = [];
  var re, m;
  if (st.granularity === 'grapheme') {
    var cps = Array.from(s), idx = 0, cur = '', start = 0, joined = false;
    for (var i = 0; i < cps.length; i++) {
      var cp = cps[i];
      var mod = cp.length === 2 && cp.charCodeAt(0) === 0xD83C && cp.charCodeAt(1) >= 0xDFFB && cp.charCodeAt(1) <= 0xDFFF;
      if (cur && (INTL_EXTEND.test(cp) || mod || joined)) {
        cur += cp;
      } else {
        if (cur) out.push({ segment: cur, index: start, input: s });
        cur = cp;
        start = idx;
      }
      joined = cp === '\u200d';
      idx += cp.length;
    }
    if (cur) out.push({ segment: cur, index: start, input: s });
    return out;
  }
  if (st.granularity === 'word') {
    re = /[A-Za-z0-9_\u00c0-\u024f\u0370-\u03ff\u0400-\u04ff\u0590-\u06ff\u3040-\u30ff\u4e00-\u9fff\uac00-\ud7af]+(?:['\u2019][A-Za-z]+)*|\s+|[\s\S]/gu;
    while ((m = re.exec(s)) !== null) {
      var w = m[0];
      var astral = w.length === 2 && w.charCodeAt(0) >= 0xD800 && w.charCodeAt(0) <= 0xDBFF;
      out.push({ segment: w, index: m.index, input: s, isWordLike: !astral && !/^\s+$/.test(w) && /[A-Za-z0-9\u00c0-\ud7ff]/.test(w) });
    }
    return out;
  }
  re = /[^.!?]+(?:[.!?]+|$)\s*|[.!?]+\s*/g;
  while ((m = re.exec(s)) !== null) {
    if (!m[0]) break;
    out.push({ segment: m[0], index: m.index, input: s });
  }
  return out;
}

intlClass('Segmenter', IntlSegmenter, {
  segment: function (input) {
    var segs = intlSegments(intlState(this, 'Segmenter'), input);
    var res = {
      containing: function (index) {
        var i = index === undefined ? 0 : Math.trunc(+index) || 0;
        for (var k = 0; k < segs.length; k++) {
          if (i >= segs[k].index && i < segs[k].index + segs[k].segment.length) return segs[k];
        }
        return undefined;
      }
    };
    res[Symbol.iterator] = function () { return netIterator(segs, 'Segmenter String Iterator'); };
    return res;
  },
  resolvedOptions: function () {
    var st = intlState(this, 'Segmenter');
    return { locale: st.locale, granularity: st.granularity };
  }
});

// ---------------------------------------------------------------- Locale

function IntlLocale(tag, options) {
  if (!(this instanceof IntlLocale)) throw new TypeError("Constructor Intl.Locale requires 'new'");
  if (typeof tag !== 'string' && !(tag && typeof tag === 'object')) throw new TypeError('First argument to Intl.Locale constructor can\'t be empty or missing');
  var canon = intlCanonLocale(tag);
  var parts = canon.split('-');
  var st = { kind: 'Locale', tag: canon, language: parts[0], script: undefined, region: undefined };
  for (var i = 1; i < parts.length; i++) {
    if (/^[A-Z][a-z]{3}$/.test(parts[i])) st.script = parts[i];
    else if (/^([A-Z]{2}|\d{3})$/.test(parts[i])) st.region = parts[i];
  }
  var o = options || {};
  if (o.language) st.language = str(o.language).toLowerCase();
  if (o.script) st.script = str(o.script);
  if (o.region) st.region = str(o.region).toUpperCase();
  st.tag = [st.language, st.script, st.region].filter(Boolean).join('-');
  defineProperty(this, INTL, { value: st });
}
intlClass('Locale', IntlLocale, {
  get baseName() { return intlState(this, 'Locale').tag; },
  get language() { return intlState(this, 'Locale').language; },
  get script() { return intlState(this, 'Locale').script; },
  get region() { return intlState(this, 'Locale').region; },
  get calendar() { return undefined; },
  get collation() { return undefined; },
  get hourCycle() { return undefined; },
  get numberingSystem() { return undefined; },
  get numeric() { return false; },
  get caseFirst() { return undefined; },
  maximize: function () {
    var st = intlState(this, 'Locale');
    var likely = { en: ['Latn', 'US'], es: ['Latn', 'ES'], fr: ['Latn', 'FR'], de: ['Latn', 'DE'], ru: ['Cyrl', 'RU'], ja: ['Jpan', 'JP'],
      zh: ['Hans', 'CN'], ko: ['Kore', 'KR'], pt: ['Latn', 'BR'], it: ['Latn', 'IT'], uk: ['Cyrl', 'UA'] }[st.language] || ['Latn', undefined];
    return new IntlLocale([st.language, st.script || likely[0], st.region || likely[1]].filter(Boolean).join('-'));
  },
  minimize: function () { return new IntlLocale(intlState(this, 'Locale').language); },
  toString: function () { return intlState(this, 'Locale').tag; },
  getCalendars: function () { return ['gregory']; },
  getTimeZones: function () { return [str(envCfg('timeZone', 'UTC'))]; },
  getWeekInfo: function () { return { firstDay: 7, weekend: [6, 7], minimalDays: 1 }; },
  getTextInfo: function () { return { direction: /^(ar|he|fa|ur)$/.test(intlState(this, 'Locale').language) ? 'rtl' : 'ltr' }; }
});

// ---------------------------------------------------------------- install

function intlInstall() {
  var Intl = {};
  defineProperty(Intl, Symbol.toStringTag, { value: 'Intl', configurable: true });
  var ctors = { NumberFormat: IntlNumberFormat, DateTimeFormat: IntlDateTimeFormat, Collator: IntlCollator, PluralRules: IntlPluralRules,
    RelativeTimeFormat: IntlRelativeTimeFormat, ListFormat: IntlListFormat, DisplayNames: IntlDisplayNames, Segmenter: IntlSegmenter,
    Locale: IntlLocale };
  for (var k in ctors) defineProperty(Intl, k, { value: ctors[k], writable: true, configurable: true });
  defineProperty(Intl, 'getCanonicalLocales', { value: function (l) { return intlLocaleList(l); }, writable: true, configurable: true });
  defineProperty(Intl, 'supportedValuesOf', {
    value: function (key) {
      switch (str(key)) {
        case 'calendar': return ['gregory'];
        case 'collation': return ['default'];
        case 'currency': return Object.keys(INTL_CURRENCY_SYMBOLS).sort();
        case 'numberingSystem': return ['latn'];
        case 'timeZone': return [str(envCfg('timeZone', 'UTC'))];
        case 'unit': return Object.keys(INTL_UNITS).sort();
      }
      throw new RangeError('Invalid key : ' + str(key));
    },
    writable: true,
    configurable: true
  });
  defineGlobal('Intl', Intl);

  var NF = IntlNumberFormat, DTF = IntlDateTimeFormat;
  var numberToLocale = function (locales, options) { return new NF(locales, options).format(this.valueOf()); };
  defineProperty(Number.prototype, 'toLocaleString', { value: numberToLocale, writable: true, configurable: true });
  if (typeof BigInt === 'function') {
    defineProperty(BigInt.prototype, 'toLocaleString', { value: numberToLocale, writable: true, configurable: true });
  }
  var withDefaults = function (options, date, time) {
    var o = {};
    if (options && typeof options === 'object') for (var k in options) o[k] = options[k];
    var hasDate = o.weekday || o.year || o.month || o.day || o.dateStyle;
    var hasTime = o.hour || o.minute || o.second || o.timeStyle || o.dayPeriod || o.fractionalSecondDigits;
    if (!hasDate && !hasTime) {
      if (date) { o.year = 'numeric'; o.month = 'numeric'; o.day = 'numeric'; }
      if (time) { o.hour = 'numeric'; o.minute = 'numeric'; o.second = 'numeric'; }
    }
    return o;
  };
  var dateMethod = function (date, time) {
    return function (locales, options) {
      var t = this.getTime();
      if (isNaN(t)) return 'Invalid Date';
      return new DTF(locales, withDefaults(options, date, time)).format(t);
    };
  };
  defineProperty(Date.prototype, 'toLocaleString', { value: dateMethod(true, true), writable: true, configurable: true });
  defineProperty(Date.prototype, 'toLocaleDateString', { value: dateMethod(true, false), writable: true, configurable: true });
  defineProperty(Date.prototype, 'toLocaleTimeString', { value: dateMethod(false, true), writable: true, configurable: true });
  var nativeCompare = intlNativeCompare;
  defineProperty(String.prototype, 'localeCompare', {
    value: function (that, locales, options) {
      if (options === undefined && typeof nativeCompare === 'function') return nativeCompare.call(str(this), str(that));
      return new IntlCollator(locales, options).compare(str(this), str(that));
    },
    writable: true,
    configurable: true
  });
}


if (typeof global.Intl === 'undefined' || (N && N.__forceIntl === true)) intlInstall();
