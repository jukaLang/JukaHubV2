/* chime_reference.c — reference renderer for JukaHub's startup and shutdown
 * chimes. It has the same structure and constants as player/bootchime.go
 * (streaming blocks, wavetables, FM bells, Freeverb room) and writes what the
 * device plays as a WAV, so the sound can be designed and listened to without
 * building the app. Change both together.
 *
 *   cc -O2 -o chime chime_reference.c -lm
 *   ./chime start 48000 startup.wav 1.55
 *   ./chime stop  48000 shutdown.wav 1.50
 *
 * The last argument is the master gain (chimeStartGain / chimeStopGain); the
 * program also prints the gain that would put the peak at 0.56 of full scale.
 * analyze.py prints level, spectrum and stereo figures and draws a spectrogram.
 */
#include <math.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define TAB 4096
static double sinTab[TAB + 1], padTab[TAB + 1];

static void tab_init(void) {
    for (int i = 0; i <= TAB; i++) {
        double x = 2 * M_PI * (double)i / TAB;
        sinTab[i] = sin(x);
        /* the pad's waveform: fundamental, octave, twelfth */
        padTab[i] = (sin(x) + 0.30 * sin(2 * x) + 0.11 * sin(3 * x)) / 1.41;
    }
}
static double lookup(const double *tab, double cycles) {
    double x = (cycles - floor(cycles)) * TAB;
    int i = (int)x;
    if (i >= TAB) { /* a phase a hair below a whole cycle can round up to it */
        i = 0;
        x = 0;
    }
    return tab[i] + (tab[i + 1] - tab[i]) * (x - i);
}
static double rcos(double x) {
    if (x <= 0) return 0;
    if (x >= 1) return 1;
    return 0.5 - 0.5 * lookup(sinTab, x * 0.5 + 0.25);
}

enum { PAD, BELL, AIR };
typedef struct {
    int kind;
    double at, freq, length, attack, release, decay, level, pan, index, indexTau;
} note;

#define DB4 277.1826
#define AB4 415.3047
#define C5 523.2511
#define DB5 554.3653
#define EB5 622.2540
#define F5 698.4565
#define AB5 830.6094
#define DB6 1108.7305
#define EB6 1244.5079
#define AB6 1661.2188
#define DB7 2217.4610
#define EB7 2489.0159
#define AB7 3322.4376

static const note startScore[] = {
    {AIR, 0.00, 0, 1.05, 0.36, 0.70, 0, 0.034, 0, 0, 0},
    {PAD, 0.04, DB4, 2.90, 0.55, 1.40, 0.9, 0.081, -0.30, 0, 0},
    {PAD, 0.06, AB4, 2.88, 0.55, 1.40, 0.9, 0.070, 0.25, 0, 0},
    {PAD, 0.08, C5, 2.86, 0.60, 1.40, 0.9, 0.054, -0.15, 0, 0},
    {PAD, 0.10, EB5, 2.84, 0.60, 1.40, 0.9, 0.054, 0.35, 0, 0},
    {PAD, 0.12, F5, 2.82, 0.65, 1.40, 0.9, 0.049, 0.05, 0, 0},
    {BELL, 0.30, AB5, 1.60, 0.006, 0.30, 0.50, 0.150, -0.40, 1.4, 0.09},
    {BELL, 0.42, DB6, 1.60, 0.006, 0.30, 0.50, 0.140, -0.15, 1.4, 0.09},
    {BELL, 0.54, EB6, 1.70, 0.006, 0.30, 0.55, 0.135, 0.15, 1.4, 0.09},
    {BELL, 0.66, AB6, 2.10, 0.006, 0.40, 0.80, 0.150, 0.40, 1.3, 0.11},
    {BELL, 0.84, DB7, 0.70, 0.004, 0.20, 0.16, 0.055, -0.55, 0.9, 0.05},
    {BELL, 0.88, EB7, 0.70, 0.004, 0.20, 0.16, 0.050, 0.00, 0.9, 0.05},
    {BELL, 0.92, AB7, 0.70, 0.004, 0.20, 0.16, 0.045, 0.55, 0.9, 0.05},
};
static const note stopScore[] = {
    {PAD, 0.00, DB4, 1.70, 0.08, 1.20, 0.6, 0.050, -0.30, 0, 0},
    {PAD, 0.00, AB4, 1.70, 0.08, 1.20, 0.6, 0.044, 0.25, 0, 0},
    {PAD, 0.00, F5, 1.60, 0.08, 1.10, 0.6, 0.032, 0.05, 0, 0},
    {BELL, 0.02, AB5, 1.20, 0.006, 0.30, 0.42, 0.140, 0.35, 1.3, 0.09},
    {BELL, 0.17, EB5, 1.30, 0.006, 0.30, 0.48, 0.140, 0.00, 1.3, 0.09},
    {BELL, 0.32, DB5, 1.55, 0.006, 0.45, 0.70, 0.150, -0.35, 1.2, 0.11},
};

#define BELL_RATIO 3.0
#define BAR_RATIO 2.756
#define BAR_LEVEL 0.08
#define BAR_TAU 0.07
#define DETUNE 0.0029
#define PAD_SUSTAIN 0.72
#define HP_HZ 110.0
#define REV_ROOM 0.80
#define REV_DAMP 0.45
#define REV_IN 0.015
#ifndef REV_WET
#define REV_WET 0.35
#endif
#define TAIL_FADE 0.18
#define BLOCK 1024

static const int combTune[8] = {1116, 1188, 1277, 1356, 1422, 1491, 1557, 1617};
static const int apTune[4] = {556, 441, 341, 225};
#define SPREAD 23

typedef struct { double *buf; int len, pos; double store; } comb;
typedef struct { double *buf; int len, pos; } allpass;

typedef struct {
    const note *n;
    int start, len, i;
    double gl, gr, p1, p2, pm, pb, dec, kdec, idx, kidx, bar, kbar;
    double hi, lo, hi2, lo2, ah, al, drift, vib;
    uint32_t seed;
} voice;

typedef struct {
    voice v[16];
    int nv;
    comb c[2][8];
    allpass a[2][4];
    double fb, damp, rate, hpA, hl, hr, xl, xr, gain;
    int frames, pos;
    double mixL[BLOCK], mixR[BLOCK];
} chime;

static void chime_init(chime *c, const note *score, int count, double rate, double seconds, double gain) {
    memset(c, 0, sizeof *c);
    c->rate = rate;
    c->frames = (int)ceil(seconds * rate);
    c->gain = gain;
    c->nv = count;
    for (int k = 0; k < count; k++) {
        voice *v = &c->v[k];
        const note *n = &score[k];
        v->n = n;
        v->start = (int)(n->at * rate);
        v->len = (int)(n->length * rate);
        v->gl = cos((n->pan + 1) * M_PI / 4);
        v->gr = sin((n->pan + 1) * M_PI / 4);
        if (n->kind == AIR) v->gl = v->gr = 0.70710678;
        v->dec = 1;
        v->kdec = n->decay > 0 ? exp(-1 / (n->decay * rate)) : 1;
        v->idx = n->index;
        v->kidx = n->indexTau > 0 ? exp(-1 / (n->indexTau * rate)) : 1;
        v->bar = BAR_LEVEL;
        v->kbar = exp(-1 / (BAR_TAU * rate));
        v->al = 1 - exp(-2 * M_PI * 380 / rate);
        v->vib = (double)k * 0.37;
        v->drift = 1;
        v->seed = 0x9E3779B9u ^ (uint32_t)(k + 1) * 0x85EBCA6Bu;
    }
    double s = rate / 44100.0;
    for (int side = 0; side < 2; side++) {
        for (int i = 0; i < 8; i++) {
            c->c[side][i].len = (int)((combTune[i] + side * SPREAD) * s);
            c->c[side][i].buf = calloc(c->c[side][i].len, sizeof(double));
        }
        for (int i = 0; i < 4; i++) {
            c->a[side][i].len = (int)((apTune[i] + side * SPREAD) * s);
            c->a[side][i].buf = calloc(c->a[side][i].len, sizeof(double));
        }
    }
    c->fb = REV_ROOM * 0.28 + 0.7;
    c->damp = REV_DAMP * 0.4;
    c->hpA = 1 / (1 + 2 * M_PI * HP_HZ / rate);
}

static double noise(uint32_t *s) {
    *s ^= *s << 13; *s ^= *s >> 17; *s ^= *s << 5;
    return (double)*s / 4294967295.0 * 2 - 1;
}

/* voice_add mixes samples [from, to) of the chime's timeline into mix. */
static void voice_add(chime *c, voice *v, int from, int to) {
    const note *n = v->n;
    double rate = c->rate;
    int a = from > v->start ? from : v->start;
    int b = to < v->start + v->len ? to : v->start + v->len;
    for (int f = a; f < b; f++) {
        int i = f - v->start;
        double t = (double)i / rate;
        double e = rcos(t / n->attack) * rcos((n->length - t) / n->release) * n->level;
        double l = 0, r = 0;
        switch (n->kind) {
        case PAD:
            e *= PAD_SUSTAIN + (1 - PAD_SUSTAIN) * v->dec;
            if ((i & 63) == 0) v->drift = 1 + 0.0009 * lookup(sinTab, 0.31 * t + v->vib);
            v->p1 += n->freq * (1 + DETUNE) * v->drift / rate;
            v->p2 += n->freq * (1 - DETUNE) * v->drift / rate;
            if (v->p1 >= 1) v->p1 -= 1;
            if (v->p2 >= 1) v->p2 -= 1;
            l = r = 0.5 * (lookup(padTab, v->p1) + lookup(padTab, v->p2));
            break;
        case BELL:
            e *= v->dec;
            v->pm += n->freq * BELL_RATIO / rate;
            v->p1 += n->freq / rate;
            v->pb += n->freq * BAR_RATIO / rate;
            if (v->pm >= 1) v->pm -= 1;
            if (v->p1 >= 1) v->p1 -= 1;
            if (v->pb >= 1) v->pb -= 1;
            l = r = lookup(sinTab, v->p1 + v->idx * lookup(sinTab, v->pm) * (1 / (2 * M_PI))) + v->bar * lookup(sinTab, v->pb);
            v->idx *= v->kidx;
            v->bar *= v->kbar;
            break;
        case AIR: {
            if ((i & 31) == 0) v->ah = 1 - exp(-2 * M_PI * (1200 + 5800 * rcos(t / 0.50)) / rate);
            double nl = noise(&v->seed), nr = noise(&v->seed);
            v->hi += v->ah * (nl - v->hi); v->lo += v->al * (nl - v->lo);
            v->hi2 += v->ah * (nr - v->hi2); v->lo2 += v->al * (nr - v->lo2);
            l = (v->hi - v->lo) * 2.2;
            r = (v->hi2 - v->lo2) * 2.2;
            break;
        }
        }
        v->dec *= v->kdec;
        c->mixL[f - from] += e * l * v->gl;
        c->mixR[f - from] += e * r * v->gr;
    }
}

/* reverb_side runs one side of the Freeverb network: eight damped combs in
 * parallel, then four allpasses in series. */
static double reverb_side(chime *c, int side, double in) {
    double x = 0;
    for (int i = 0; i < 8; i++) {
        comb *cb = &c->c[side][i];
        double y = cb->buf[cb->pos];
        cb->store = y * (1 - c->damp) + cb->store * c->damp;
        cb->buf[cb->pos] = in + cb->store * c->fb;
        if (++cb->pos >= cb->len) cb->pos = 0;
        x += y;
    }
    for (int i = 0; i < 4; i++) {
        allpass *ap = &c->a[side][i];
        double b = ap->buf[ap->pos];
        double y = b - x;
        ap->buf[ap->pos] = x + b * 0.5;
        if (++ap->pos >= ap->len) ap->pos = 0;
        x = y;
    }
    return x;
}

/* chime_render writes up to max stereo frames; returns how many (0 = done). */
static int chime_render(chime *c, int16_t *out, int max, double *peak) {
    int todo = c->frames - c->pos;
    if (todo > max) todo = max;
    if (todo > BLOCK) todo = BLOCK;
    if (todo <= 0) return 0;
    memset(c->mixL, 0, sizeof c->mixL);
    memset(c->mixR, 0, sizeof c->mixR);
    for (int k = 0; k < c->nv; k++) voice_add(c, &c->v[k], c->pos, c->pos + todo);
    for (int j = 0; j < todo; j++) {
        double dl = c->mixL[j], dr = c->mixR[j];
        double in = (dl + dr) * REV_IN;
        double ol = dl + REV_WET * reverb_side(c, 0, in);
        double orr = dr + REV_WET * reverb_side(c, 1, in);
        c->hl = c->hpA * (c->hl + ol - c->xl); c->xl = ol;
        c->hr = c->hpA * (c->hr + orr - c->xr); c->xr = orr;
        double fade = rcos((double)(c->frames - c->pos - j) / c->rate / TAIL_FADE) * c->gain;
        double a = c->hl * fade, b = c->hr * fade;
        if (fabs(a) > *peak) *peak = fabs(a);
        if (fabs(b) > *peak) *peak = fabs(b);
        if (a > 0.98) a = 0.98; else if (a < -0.98) a = -0.98;
        if (b > 0.98) b = 0.98; else if (b < -0.98) b = -0.98;
        out[2 * j] = (int16_t)lround(a * 32767);
        out[2 * j + 1] = (int16_t)lround(b * 32767);
    }
    c->pos += todo;
    return todo;
}

static void wav(const char *path, const int16_t *pcm, int frames, int rate) {
    FILE *f = fopen(path, "wb");
    uint32_t dataBytes = frames * 4, riff = 36 + dataBytes, br = rate * 4, fmt = 16, sr = rate;
    uint16_t pcmfmt = 1, ch = 2, align = 4, bits = 16;
    fwrite("RIFF", 1, 4, f); fwrite(&riff, 4, 1, f); fwrite("WAVEfmt ", 1, 8, f);
    fwrite(&fmt, 4, 1, f); fwrite(&pcmfmt, 2, 1, f); fwrite(&ch, 2, 1, f); fwrite(&sr, 4, 1, f);
    fwrite(&br, 4, 1, f); fwrite(&align, 2, 1, f); fwrite(&bits, 2, 1, f);
    fwrite("data", 1, 4, f); fwrite(&dataBytes, 4, 1, f); fwrite(pcm, 4, frames, f);
    fclose(f);
}

int main(int argc, char **argv) {
    if (argc < 5) {
        fprintf(stderr, "usage: %s start|stop RATE OUT.wav GAIN\n", argv[0]);
        return 2;
    }
    tab_init();
    const char *which = argv[1];
    double rate = atof(argv[2]);
    double gain = atof(argv[4]);
    static chime c;
    if (!strcmp(which, "start")) chime_init(&c, startScore, 13, rate, 3.30, gain);
    else chime_init(&c, stopScore, 6, rate, 2.10, gain);
    int16_t *pcm = malloc(c.frames * 4);
    int got = 0, n;
    double peak = 0;
    while ((n = chime_render(&c, pcm + 2 * got, 4096, &peak)) > 0) got += n;
    fprintf(stderr, "%s rate %.0f gain %.5f: frames %d, peak before clamp %.5f -> gain for 0.56 = %.5f\n", which, rate, gain, got, peak, gain * 0.56 / peak);
    wav(argv[3], pcm, got, (int)rate);
    return 0;
}
