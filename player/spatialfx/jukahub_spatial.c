/*
 * jukahub_spatial.c — JukaHub Spatial: virtual surround for games.
 *
 * A faithful C99 port of player/spatialfx/jh_spatial_ref.py, which is the
 * specification: every constant, formula and the order of every operation
 * below follows it, and tests/test_spatial.c checks this file against the
 * golden vectors that script writes (player/testdata/spatial/<case>.txt).
 *
 * Headphones: the stereo input plays through four virtual loudspeakers (a
 * front pair at -/+30 degrees, and a rear pair at -/+110 degrees fed with the
 * band-limited, decorrelated side signal 15 ms later), each reaching each ear
 * through a spherical-head model (Brown & Duda: head-shadow filter + path
 * delay, plus a low-pass on the far ear), with six early reflections per ear
 * and a 4-line feedback delay network for a small room. Speakers: the side
 * signal above 300 Hz is widened. A soft limiter keeps the output in (-1, 1).
 *
 * One shared library, two plugin ABIs, both at the end of this file:
 *   - RetroArch's audio DSP filter (dspfilter_get_implementation), loaded
 *     from the folder of the .dsp preset that audio_dsp_plugin names;
 *   - LADSPA (ladspa_descriptor), for ALSA's "ladspa" PCM or any LADSPA host.
 * Both ABIs are declared here from their public descriptions (no headers are
 * copied); see README.md for what to verify before trusting a release.
 *
 * Numerics: samples and delay lines are float; every coefficient is computed
 * in double exactly as the reference does, then rounded once. exp() is our
 * own (jh_exp): glibc 2.29 re-versioned exp/pow/log, and a plugin that links
 * them on a modern build host would not load on an older handheld. The libm
 * functions used are cos, sin, floor, sqrt and tanhf — at -O2 GCC merges a
 * cos/sin pair of one angle into sincos — all of which carry the oldest
 * symbol versions (GLIBC_2.17 on aarch64; build.sh checks it).
 *
 * Robustness (the reference's rules): an input sample that is not a finite
 * number is read as 0 before it reaches any state, a NaN strength is 0 (off),
 * and the limiter turns a NaN into 0, so a bad sample from a host never
 * becomes a full-scale burst.
 *
 * Real-time rules: process/run never allocate, lock or call the system. The
 * delay-line pool is sized for the largest room at creation, so a room change
 * re-lays the lines inside it.
 */
#include "jukahub_spatial.h"

#include <math.h>
#include <stddef.h>
#include <stdlib.h>
#include <string.h>

#if defined(_WIN32)
#define JH_EXPORT __declspec(dllexport)
#elif defined(__GNUC__)
#define JH_EXPORT __attribute__((visibility("default")))
#else
#define JH_EXPORT
#endif

/* ── Normative constants (jh_spatial_ref.py) ───────────────────────────── */

#define JH_PI 3.14159265358979323846

static const double HEAD_RADIUS = 0.0875;    /* metres */
static const double SOUND_SPEED = 343.0;     /* metres per second */
static const double ALPHA_MIN = 0.1;
static const double THETA_MIN = 150.0;       /* degrees */
static const double FRONT_AZ = 30.0;
static const double REAR_AZ = 110.0;
static const double REAR_DELAY_MS = 15.0;
static const double REAR_HPF_HZ = 120.0;
static const double REAR_LPF_HZ = 7000.0;
static const double CONTRA_LPF_HZ = 1500.0;
static const double AP_MS[4] = { 3.1, 7.3, 4.7, 5.9 }; /* rear-left x2, then rear-right x2 */
static const double AP_GAIN = 0.5;
static const double ER_LPF_HZ = 6000.0;
static const double FDN_DAMP_HZ = 4000.0;
static const double FDN_PREDELAY_MS = 20.0;
static const double SIDE_HPF_HZ = 300.0;
static const double LIMIT_KNEE = 0.9;
static const double NORM_LO_HZ = 100.0; /* ... to NORM_HI_HZ = 8000 Hz: LN80 below */
#define NORM_POINTS 24

/* room: time scale, early-reflection gain, RT60 seconds */
static const double ROOM_SCALE[3] = { 0.6, 1.0, 1.5 };
static const double ROOM_ER_GAIN[3] = { 0.8, 1.0, 1.0 };
static const double ROOM_RT60[3] = { 0.25, 0.40, 0.70 };
#define ROOM_MAX JH_ROOM_LARGE /* the largest scale: the pool is sized for it */

/* Early reflections per ear (left, then right): delay ms, gain, weight of L, weight of R. */
static const double ER_TAPS[2][6][4] = {
   { {  7.1,  0.35, 0.8, 0.2 }, { 11.3, -0.28, 0.3, 0.7 }, { 17.9,  0.22, 0.7, 0.3 },
     { 23.5, -0.17, 0.4, 0.6 }, { 31.1,  0.13, 0.6, 0.4 }, { 38.9, -0.10, 0.5, 0.5 } },
   { {  7.9,  0.35, 0.2, 0.8 }, { 12.7, -0.28, 0.7, 0.3 }, { 16.1,  0.22, 0.3, 0.7 },
     { 25.3, -0.17, 0.6, 0.4 }, { 29.7,  0.13, 0.4, 0.6 }, { 41.3, -0.10, 0.5, 0.5 } },
};

static const double FDN_MS[4] = { 29.7, 37.1, 41.1, 43.7 };
static const float FDN_IN[4] = { 0.5f, -0.5f, 0.5f, -0.5f };

#define DEFAULT_STRENGTH 0.75f

/* Below this a recirculating state is flushed to zero, so a tail never decays
 * into denormals (slow on some FPUs). The reference does not flush; the
 * difference is under 1e-19. */
#define JH_TINY 1e-20f

/* ── Small maths ───────────────────────────────────────────────────────── */

static const double LN2_HI = 6.93147180369123816490e-01;
static const double LN2_LO = 1.90821492927058770002e-10;
static const double INV_LN2 = 1.44269504088896338700e+00;
static const double LN10 = 2.30258509299404568402;
static const double LN80 = 4.38202663467388161226; /* ln(8000 Hz / NORM_LO_HZ) */

/* jh_exp is e^x for set-up maths: x = k*ln2 + r with |r| <= ln2/2, a Taylor
 * series for e^r (the 20 terms reach below 1e-28), then exact scaling by 2^k.
 * Never called while processing. */
static double jh_exp(double x)
{
   double kf, r, sum, term;
   int k, i;
   if (x != x)
      return x;
   if (x < -740.0)
      return 0.0;
   if (x > 700.0)
      x = 700.0;
   kf = floor(x * INV_LN2 + 0.5);
   k = (int)kf;
   r = (x - kf * LN2_HI) - kf * LN2_LO;
   sum = 1.0;
   term = 1.0;
   for (i = 1; i <= 20; i++)
   {
      term *= r / (double)i;
      sum += term;
   }
   for (; k > 0; k--)
      sum *= 2.0;
   for (; k < 0; k++)
      sum *= 0.5;
   return sum;
}

static int jh_iround(double x)
{
   return (int)floor(x + 0.5);
}

static double jh_pole(double fc, double fs)
{
   return jh_exp(-2.0 * JH_PI * fc / fs);
}

static int jh_clampi(int v, int lo, int hi)
{
   return v < lo ? lo : (v > hi ? hi : v);
}

/* The reference's clamp_strength: [0, 1], and a NaN is 0 (off). */
static float jh_clamp_strength(float s)
{
   if (s != s)
      return 0.0f;
   if (s < 0.0f)
      return 0.0f;
   if (s > 1.0f)
      return 1.0f;
   return s;
}

static inline float jh_flush(float x)
{
   return (x < JH_TINY && x > -JH_TINY) ? 0.0f : x;
}

/* The reference's finite_or_zero: NaN and +/-inf read as 0. */
static inline float jh_finite_or_zero(float x)
{
   return isfinite(x) ? x : 0.0f;
}

/* The reference's LIMIT_CEIL, 0x3F7FFFFF: the largest float below 1. The
 * curve 0.9 + 0.1 * tanh(t) rounds to exactly 1.0 for large t — from t of
 * about 8.7 in float and about 17.7 in double (inputs of 1.77 and 2.67) — so
 * the reference caps it at the same value. */
static const float LIMIT_CEIL = 0.99999994f;

/* The reference's limit: identity up to the knee, then the capped tanh curve;
 * a NaN comes out as 0. */
static inline float jh_limit(float x)
{
   float a;
   if (x != x)
      return 0.0f;
   a = fabsf(x);
   if (a <= (float)LIMIT_KNEE)
      return x;
   a = (float)LIMIT_KNEE + (float)(1.0 - LIMIT_KNEE) * tanhf((a - (float)LIMIT_KNEE) / (float)(1.0 - LIMIT_KNEE));
   if (!(a < LIMIT_CEIL))
      a = LIMIT_CEIL;
   return x > 0.0f ? a : -a;
}

/* ── Delay lines ───────────────────────────────────────────────────────── */

/* A circular line: write first, then read; tap(0) is the sample just written. */
typedef struct
{
   float *buf;
   int n;
   int w;
} jh_line;

static inline void line_write(jh_line *l, float x)
{
   if (++l->w == l->n)
      l->w = 0;
   l->buf[l->w] = x;
}

static inline float line_tap(const jh_line *l, int i)
{
   int j = l->w - i;
   if (j < 0)
      j += l->n;
   return l->buf[j];
}

/* A stereo line: n slots of an (L, R) pair, so the early reflections read both
 * channels of a tap from one place. Same write-then-read rule. */
static inline void line2_write(jh_line *l, float a, float b)
{
   if (++l->w == l->n)
      l->w = 0;
   l->buf[2 * l->w] = a;
   l->buf[2 * l->w + 1] = b;
}

static inline const float *line2_tap(const jh_line *l, int i)
{
   int j = l->w - i;
   if (j < 0)
      j += l->n;
   return &l->buf[2 * j];
}

/* ── The instance ──────────────────────────────────────────────────────── */

typedef struct
{
   double d;            /* delay, samples (the makeup uses it unsplit) */
   double b0, b1, a1;   /* head shadow: y = b0*x + b1*x1 - a1*y1 */
   int di;              /* floor(d) */
   float df;            /* d - floor(d) */
   float fb0, fb1, fa1;
   float g;             /* the path's gain at the current strength */
} jh_path;

struct jh_spatial
{
   double fs;
   int mode;
   int room;
   float strength;

   /* Set by the rate (room- and strength-independent). */
   jh_path path[4][2]; /* speaker FL, FR, RL, RR -> ear left, right */
   int spk_len;
   int ap_d[4];
   double p_contra;
   float k_contra, k_rear_hp, k_rear_lp, k_er, k_damp, k_side; /* 1 - pole */

   /* Set by the room. */
   int er_d[2][6];
   float er_g[2][6], er_wl[2][6], er_wr[2][6];
   int predelay;
   int er_len;
   int fdn_len[4];
   float fdn_gain[4];
   double er_room_gain;

   /* Set by the strength. */
   double g_contra_d, g_er_d, g_rev_d, makeup_d;
   float g_contra, g_rear, g_er, g_rev, width, makeup;

   /* State. */
   jh_line spk[4];
   float sh_x1[4][2], sh_y1[4][2], contra_lp[4][2];
   float rear_hp, rear_lp;
   jh_line ap_x[4], ap_y[4];
   jh_line er; /* stereo: the low-passed L and R */
   float er_lp[2];
   jh_line fdn[4];
   float fdn_z[4];
   float side_lp;

   /* One allocation for every line; offsets fixed for the largest room. */
   float *pool;
   size_t pool_len;
   size_t er_off;
   size_t fdn_off[4];
};

static double ear_angle(double az, double ear_az)
{
   double d = fabs(az - ear_az);
   while (d >= 360.0)
      d -= 360.0;
   if (d > 180.0)
      d = 360.0 - d;
   return d;
}

static double head_alpha(double theta)
{
   return (1.0 + ALPHA_MIN / 2.0) + (1.0 - ALPHA_MIN / 2.0) * cos(theta / THETA_MIN * JH_PI);
}

static double head_delay_s(double theta)
{
   double ac = HEAD_RADIUS / SOUND_SPEED;
   if (theta < 90.0)
      return ac * (1.0 - cos(theta * JH_PI / 180.0));
   return ac * (1.0 + (theta - 90.0) * JH_PI / 180.0);
}

/* The rate-dependent set-up: the eight ear paths, the line lengths that do
 * not depend on the room, and the one-pole coefficients. */
static void configure_rate(jh_spatial *s)
{
   static const double az[4] = { -1.0, 1.0, -1.0, 1.0 };
   double fs = s->fs;
   double w0 = SOUND_SPEED / HEAD_RADIUS;
   double kk = 2.0 * fs;
   double d0 = 2.0 * w0 + kk;
   double th[8], t[8], tmin, maxd = 0.0;
   int k, e, i;

   for (k = 0; k < 4; k++)
   {
      double a = az[k] * (k < 2 ? FRONT_AZ : REAR_AZ);
      double extra = k < 2 ? 0.0 : REAR_DELAY_MS / 1000.0;
      for (e = 0; e < 2; e++)
      {
         double ear_az = e == 0 ? -90.0 : 90.0;
         th[k * 2 + e] = ear_angle(a, ear_az);
         t[k * 2 + e] = head_delay_s(th[k * 2 + e]) + extra;
      }
   }
   tmin = t[0];
   for (i = 1; i < 8; i++)
      if (t[i] < tmin)
         tmin = t[i];
   for (k = 0; k < 4; k++)
   {
      for (e = 0; e < 2; e++)
      {
         jh_path *p = &s->path[k][e];
         double alpha = head_alpha(th[k * 2 + e]);
         p->d = (t[k * 2 + e] - tmin) * fs;
         p->b0 = (2.0 * w0 + alpha * kk) / d0;
         p->b1 = (2.0 * w0 - alpha * kk) / d0;
         p->a1 = (2.0 * w0 - kk) / d0;
         p->di = (int)floor(p->d);
         p->df = (float)(p->d - floor(p->d));
         p->fb0 = (float)p->b0;
         p->fb1 = (float)p->b1;
         p->fa1 = (float)p->a1;
         if (p->d > maxd)
            maxd = p->d;
      }
   }
   s->spk_len = (int)floor(maxd) + 3;
   for (i = 0; i < 4; i++)
   {
      int d = jh_iround(AP_MS[i] * fs / 1000.0);
      s->ap_d[i] = d < 1 ? 1 : d;
   }
   s->p_contra = jh_pole(CONTRA_LPF_HZ, fs);
   s->k_contra = (float)(1.0 - s->p_contra);
   s->k_rear_hp = (float)(1.0 - jh_pole(REAR_HPF_HZ, fs));
   s->k_rear_lp = (float)(1.0 - jh_pole(REAR_LPF_HZ, fs));
   s->k_er = (float)(1.0 - jh_pole(ER_LPF_HZ, fs));
   s->k_damp = (float)(1.0 - jh_pole(FDN_DAMP_HZ, fs));
   s->k_side = (float)(1.0 - jh_pole(SIDE_HPF_HZ, fs));
}

/* The room-dependent lengths, for sizing (room = ROOM_MAX) and for use. */
static void room_lengths(double fs, int room, int er_d[2][6], int *predelay, int *er_len, int fdn_len[4])
{
   double scale = ROOM_SCALE[room];
   int e, j, i, er_max;
   *predelay = jh_iround(FDN_PREDELAY_MS * scale * fs / 1000.0);
   er_max = *predelay;
   for (e = 0; e < 2; e++)
   {
      for (j = 0; j < 6; j++)
      {
         er_d[e][j] = jh_iround(ER_TAPS[e][j][0] * scale * fs / 1000.0);
         if (er_d[e][j] > er_max)
            er_max = er_d[e][j];
      }
   }
   *er_len = er_max + 2;
   for (i = 0; i < 4; i++)
   {
      fdn_len[i] = jh_iround(FDN_MS[i] * scale * fs / 1000.0);
      if (fdn_len[i] < 1)
         fdn_len[i] = 1;
   }
}

static void line_place(jh_line *l, float *mem, int n)
{
   l->buf = mem;
   l->n = n;
   l->w = 0;
}

/* Points every line into the pool for the current room. */
static void layout_lines(jh_spatial *s)
{
   size_t off = 0;
   int k, i;
   for (k = 0; k < 4; k++)
   {
      line_place(&s->spk[k], s->pool + off, s->spk_len);
      off += (size_t)s->spk_len;
   }
   for (i = 0; i < 4; i++)
   {
      line_place(&s->ap_x[i], s->pool + off, s->ap_d[i] + 1);
      off += (size_t)s->ap_d[i] + 1;
      line_place(&s->ap_y[i], s->pool + off, s->ap_d[i] + 1);
      off += (size_t)s->ap_d[i] + 1;
   }
   line_place(&s->er, s->pool + s->er_off, s->er_len);
   for (i = 0; i < 4; i++)
      line_place(&s->fdn[i], s->pool + s->fdn_off[i], s->fdn_len[i]);
}

static void configure_room(jh_spatial *s)
{
   double fs = s->fs;
   double rt60 = ROOM_RT60[s->room];
   int e, j, i;
   room_lengths(fs, s->room, s->er_d, &s->predelay, &s->er_len, s->fdn_len);
   for (e = 0; e < 2; e++)
   {
      for (j = 0; j < 6; j++)
      {
         s->er_g[e][j] = (float)ER_TAPS[e][j][1];
         s->er_wl[e][j] = (float)ER_TAPS[e][j][2];
         s->er_wr[e][j] = (float)ER_TAPS[e][j][3];
      }
   }
   for (i = 0; i < 4; i++)
      s->fdn_gain[i] = (float)jh_exp(-3.0 * (double)s->fdn_len[i] / (rt60 * fs) * LN10);
   s->er_room_gain = ROOM_ER_GAIN[s->room];
   layout_lines(s);
}

/* Complex response of the direct path k -> ear e at w rad/sample, including
 * the far-ear low-pass: what the loudness makeup averages. */
static void path_response(const jh_spatial *s, int k, int e, double w, double *re, double *im)
{
   const jh_path *p = &s->path[k][e];
   double c = cos(w), sn = sin(w);
   /* (b0 + b1 z^-1) / (1 + a1 z^-1), z^-1 = c - j sn */
   double nr = p->b0 + p->b1 * c, ni = -p->b1 * sn;
   double dr = 1.0 + p->a1 * c, dim = -p->a1 * sn;
   double den = dr * dr + dim * dim;
   double hr = (nr * dr + ni * dim) / den;
   double hi = (ni * dr - nr * dim) / den;
   /* e^{-j w d} */
   double cd = cos(w * p->d), sd = -sin(w * p->d);
   double r2 = hr * cd - hi * sd;
   double i2 = hr * sd + hi * cd;
   if ((k % 2) != e)
   {
      /* (1 - p) / (1 - p z^-1) */
      double pc = s->p_contra;
      double lr = 1.0 - pc * c, li = pc * sn;
      double ld = lr * lr + li * li;
      double gr = (1.0 - pc) * lr / ld, gi = -(1.0 - pc) * li / ld;
      double r3 = r2 * gr - i2 * gi;
      double i3 = r2 * gi + i2 * gr;
      r2 = r3;
      i2 = i3;
   }
   *re = r2;
   *im = i2;
}

static double loudness_makeup(const jh_spatial *s)
{
   double acc = 0.0, direct, er_energy = 0.0, total;
   int i, j;
   for (i = 0; i < NORM_POINTS; i++)
   {
      double f = NORM_LO_HZ * jh_exp(LN80 * ((double)i / (NORM_POINTS - 1.0)));
      double w = 2.0 * JH_PI * f / s->fs;
      double r0, i0, r1, i1, hr, hi;
      path_response(s, 0, 0, w, &r0, &i0);
      path_response(s, 1, 0, w, &r1, &i1);
      hr = r0 + s->g_contra_d * r1;
      hi = i0 + s->g_contra_d * i1;
      acc += hr * hr + hi * hi;
   }
   direct = acc / NORM_POINTS;
   for (j = 0; j < 6; j++)
      er_energy += ER_TAPS[0][j][1] * ER_TAPS[0][j][1];
   total = direct + s->g_er_d * s->g_er_d * er_energy + s->g_rev_d * s->g_rev_d * 0.5;
   return 1.0 / sqrt(total);
}

static void apply_strength(jh_spatial *s)
{
   double v = (double)s->strength;
   s->g_contra_d = 0.6 * v;
   s->g_er_d = 0.6 * v * s->er_room_gain;
   s->g_rev_d = 0.25 * v;
   s->g_contra = (float)s->g_contra_d;
   s->g_rear = (float)(0.4 * v);
   s->g_er = (float)s->g_er_d;
   s->g_rev = (float)s->g_rev_d;
   s->width = (float)(1.0 + 0.8 * v);
   s->makeup_d = loudness_makeup(s);
   s->makeup = (float)s->makeup_d;
   {
      int k, e;
      for (k = 0; k < 4; k++)
         for (e = 0; e < 2; e++)
         {
            if (k < 2)
               s->path[k][e].g = (k % 2) == e ? 1.0f : s->g_contra;
            else
               s->path[k][e].g = s->g_rear;
         }
   }
}

void jh_spatial_reset(jh_spatial *s)
{
   int k, i;
   if (!s)
      return;
   memset(s->pool, 0, s->pool_len * sizeof(float));
   for (k = 0; k < 4; k++)
      s->spk[k].w = 0;
   for (i = 0; i < 4; i++)
   {
      s->ap_x[i].w = 0;
      s->ap_y[i].w = 0;
      s->fdn[i].w = 0;
      s->fdn_z[i] = 0.0f;
   }
   s->er.w = 0;
   memset(s->sh_x1, 0, sizeof(s->sh_x1));
   memset(s->sh_y1, 0, sizeof(s->sh_y1));
   memset(s->contra_lp, 0, sizeof(s->contra_lp));
   s->rear_hp = s->rear_lp = 0.0f;
   s->er_lp[0] = s->er_lp[1] = 0.0f;
   s->side_lp = 0.0f;
}

jh_spatial *jh_spatial_new(double fs, int mode, float strength, int room)
{
   jh_spatial *s;
   int er_d[2][6], predelay, er_len, fdn_len[4];
   size_t off;
   int i;
   if (!(fs >= JH_RATE_MIN && fs <= JH_RATE_MAX))
      return NULL;
   s = (jh_spatial *)calloc(1, sizeof(*s));
   if (!s)
      return NULL;
   s->fs = fs;
   s->mode = jh_clampi(mode, JH_MODE_OFF, JH_MODE_SPEAKERS);
   s->room = jh_clampi(room, JH_ROOM_SMALL, JH_ROOM_LARGE);
   s->strength = jh_clamp_strength(strength);
   configure_rate(s);

   /* Pool: room-independent lines first, then the ER and FDN lines at their
    * largest-room sizes, so every room fits without moving. */
   off = 4 * (size_t)s->spk_len;
   for (i = 0; i < 4; i++)
      off += 2 * ((size_t)s->ap_d[i] + 1);
   room_lengths(fs, ROOM_MAX, er_d, &predelay, &er_len, fdn_len);
   s->er_off = off;
   off += 2 * (size_t)er_len;
   for (i = 0; i < 4; i++)
   {
      s->fdn_off[i] = off;
      off += (size_t)fdn_len[i];
   }
   s->pool_len = off;
   s->pool = (float *)calloc(off, sizeof(float));
   if (!s->pool)
   {
      free(s);
      return NULL;
   }
   configure_room(s);
   apply_strength(s);
   jh_spatial_reset(s);
   return s;
}

void jh_spatial_free(jh_spatial *s)
{
   if (!s)
      return;
   free(s->pool);
   free(s);
}

void jh_spatial_set_mode(jh_spatial *s, int mode)
{
   if (!s)
      return;
   mode = jh_clampi(mode, JH_MODE_OFF, JH_MODE_SPEAKERS);
   if (mode == s->mode)
      return;
   s->mode = mode;
   jh_spatial_reset(s);
}

void jh_spatial_set_room(jh_spatial *s, int room)
{
   if (!s)
      return;
   room = jh_clampi(room, JH_ROOM_SMALL, JH_ROOM_LARGE);
   if (room == s->room)
      return;
   s->room = room;
   configure_room(s);
   apply_strength(s); /* the ER gain belongs to the room */
   jh_spatial_reset(s);
}

void jh_spatial_set_strength(jh_spatial *s, float strength)
{
   if (!s)
      return;
   strength = jh_clamp_strength(strength);
   if (strength == s->strength)
      return;
   s->strength = strength;
   apply_strength(s);
}

int jh_spatial_mode(const jh_spatial *s) { return s ? s->mode : JH_MODE_OFF; }
int jh_spatial_room(const jh_spatial *s) { return s ? s->room : JH_ROOM_MEDIUM; }
float jh_spatial_strength(const jh_spatial *s) { return s ? s->strength : 0.0f; }
double jh_spatial_rate(const jh_spatial *s) { return s ? s->fs : 0.0; }
double jh_spatial_makeup(const jh_spatial *s) { return s ? s->makeup_d : 1.0; }

/* ── Processing ────────────────────────────────────────────────────────── */

static inline float allpass(jh_spatial *s, int i, float v)
{
   jh_line *x = &s->ap_x[i];
   jh_line *y = &s->ap_y[i];
   int d = s->ap_d[i];
   float out;
   line_write(x, v);
   out = -(float)AP_GAIN * v + line_tap(x, d) + (float)AP_GAIN * line_tap(y, d - 1);
   out = jh_flush(out);
   line_write(y, out);
   return out;
}

/* One speaker-to-ear path: the fractional read, the head shadow and, on the
 * far ear, the extra low-pass. is_far is a constant at every call, so each
 * inlined copy keeps only its own branch. A whole-sample delay (the near
 * front paths: 0) reads one tap; (1 - 0) * a + 0 * b is a for every finite b. */
static inline float ear_path(jh_spatial *s, int k, int e, int is_far)
{
   const jh_path *p = &s->path[k][e];
   const jh_line *line = &s->spk[k];
   float v, y;
   if (p->df == 0.0f)
      v = line_tap(line, p->di);
   else
      v = (1.0f - p->df) * line_tap(line, p->di) + p->df * line_tap(line, p->di + 1);
   y = jh_flush(p->fb0 * v + p->fb1 * s->sh_x1[k][e] - p->fa1 * s->sh_y1[k][e]);
   s->sh_x1[k][e] = v;
   s->sh_y1[k][e] = y;
   if (is_far)
   {
      s->contra_lp[k][e] = jh_flush(s->contra_lp[k][e] + s->k_contra * (y - s->contra_lp[k][e]));
      y = s->contra_lp[k][e];
   }
   return y;
}

static inline void frame_headphones(jh_spatial *s, float xl, float xr, float *yl, float *yr)
{
   float side, hp, rl, rr;
   float ear[2] = { 0.0f, 0.0f };
   float er_l = 0.0f, er_r = 0.0f, rev_in, zsum, rev_l, rev_r;
   int j, i;

   /* 1. Rear feed: side signal, high-passed, low-passed, decorrelated per side. */
   side = 0.5f * (xl - xr);
   s->rear_hp = jh_flush(s->rear_hp + s->k_rear_hp * (side - s->rear_hp));
   hp = side - s->rear_hp;
   s->rear_lp = jh_flush(s->rear_lp + s->k_rear_lp * (hp - s->rear_lp));
   rl = s->rear_lp;
   rr = -s->rear_lp;
   rl = allpass(s, 0, rl);
   rl = allpass(s, 1, rl);
   rr = allpass(s, 2, rr);
   rr = allpass(s, 3, rr);

   /* 2. Virtual loudspeakers to the two ears. Each line is written first;
    * the ears then accumulate speaker by speaker, in the reference's order. */
   line_write(&s->spk[0], xl);
   line_write(&s->spk[1], xr);
   line_write(&s->spk[2], rl);
   line_write(&s->spk[3], rr);
   ear[0] += s->path[0][0].g * ear_path(s, 0, 0, 0);
   ear[1] += s->path[0][1].g * ear_path(s, 0, 1, 1);
   ear[0] += s->path[1][0].g * ear_path(s, 1, 0, 1);
   ear[1] += s->path[1][1].g * ear_path(s, 1, 1, 0);
   ear[0] += s->path[2][0].g * ear_path(s, 2, 0, 0);
   ear[1] += s->path[2][1].g * ear_path(s, 2, 1, 1);
   ear[0] += s->path[3][0].g * ear_path(s, 3, 0, 1);
   ear[1] += s->path[3][1].g * ear_path(s, 3, 1, 0);

   /* 3. Early reflections from the low-passed inputs. */
   s->er_lp[0] = jh_flush(s->er_lp[0] + s->k_er * (xl - s->er_lp[0]));
   s->er_lp[1] = jh_flush(s->er_lp[1] + s->k_er * (xr - s->er_lp[1]));
   line2_write(&s->er, s->er_lp[0], s->er_lp[1]);
   for (j = 0; j < 6; j++)
   {
      const float *t = line2_tap(&s->er, s->er_d[0][j]);
      er_l += s->er_g[0][j] * (s->er_wl[0][j] * t[0] + s->er_wr[0][j] * t[1]);
   }
   for (j = 0; j < 6; j++)
   {
      const float *t = line2_tap(&s->er, s->er_d[1][j]);
      er_r += s->er_g[1][j] * (s->er_wl[1][j] * t[0] + s->er_wr[1][j] * t[1]);
   }

   /* 4. Late reverb: read every line's oldest sample, damp, mix, write back. */
   {
      const float *t = line2_tap(&s->er, s->predelay);
      rev_in = 0.5f * (t[0] + t[1]);
   }
   for (i = 0; i < 4; i++)
   {
      float y = line_tap(&s->fdn[i], s->fdn_len[i] - 1);
      s->fdn_z[i] = jh_flush(s->fdn_z[i] + s->k_damp * (y - s->fdn_z[i]));
   }
   zsum = s->fdn_z[0] + s->fdn_z[1] + s->fdn_z[2] + s->fdn_z[3];
   for (i = 0; i < 4; i++)
   {
      float fb = s->fdn_z[i] - 0.5f * zsum;
      line_write(&s->fdn[i], jh_flush(FDN_IN[i] * rev_in + s->fdn_gain[i] * fb));
   }
   rev_l = 0.5f * (s->fdn_z[0] + s->fdn_z[2]);
   rev_r = 0.5f * (s->fdn_z[1] + s->fdn_z[3]);

   /* 5. Mix, normalise, limit. */
   *yl = jh_limit(s->makeup * (ear[0] + s->g_er * er_l + s->g_rev * rev_l));
   *yr = jh_limit(s->makeup * (ear[1] + s->g_er * er_r + s->g_rev * rev_r));
}

static inline void frame_speakers(jh_spatial *s, float xl, float xr, float *yl, float *yr)
{
   float mid = 0.5f * (xl + xr);
   float side = 0.5f * (xl - xr);
   float side2;
   s->side_lp = jh_flush(s->side_lp + s->k_side * (side - s->side_lp));
   side2 = side + (s->width - 1.0f) * (side - s->side_lp);
   *yl = jh_limit(mid + side2);
   *yr = jh_limit(mid - side2);
}

/* A NaN or an absurd value that got into the state (a host feeding NaN or
 * infinity) would recirculate for ever; the block after it starts clean. */
static void guard_state(jh_spatial *s)
{
   float probe = s->fdn_z[0] + s->fdn_z[1] + s->fdn_z[2] + s->fdn_z[3] + s->rear_lp + s->side_lp +
                 s->sh_y1[0][0] + s->sh_y1[1][1] + s->sh_y1[2][0] + s->sh_y1[3][1];
   if (!(probe > -1e6f && probe < 1e6f))
      jh_spatial_reset(s);
}

static int active(const jh_spatial *s)
{
   return s && s->mode != JH_MODE_OFF && s->strength > 0.0f;
}

void jh_spatial_process_interleaved(jh_spatial *s, float *lr, unsigned long frames)
{
   unsigned long n;
   if (!active(s) || !lr || frames == 0)
      return;
   if (s->mode == JH_MODE_SPEAKERS)
   {
      for (n = 0; n < frames; n++)
         frame_speakers(s, jh_finite_or_zero(lr[2 * n]), jh_finite_or_zero(lr[2 * n + 1]), &lr[2 * n], &lr[2 * n + 1]);
   }
   else
   {
      for (n = 0; n < frames; n++)
      {
         float yl, yr;
         frame_headphones(s, jh_finite_or_zero(lr[2 * n]), jh_finite_or_zero(lr[2 * n + 1]), &yl, &yr);
         lr[2 * n] = yl;
         lr[2 * n + 1] = yr;
      }
   }
   guard_state(s);
}

void jh_spatial_process_planar(jh_spatial *s, const float *in_l, const float *in_r,
      float *out_l, float *out_r, unsigned long frames)
{
   unsigned long n;
   if (!in_l || !in_r || !out_l || !out_r || frames == 0)
      return;
   if (!active(s))
   {
      if (out_l != in_l)
         memmove(out_l, in_l, frames * sizeof(float));
      if (out_r != in_r)
         memmove(out_r, in_r, frames * sizeof(float));
      return;
   }
   for (n = 0; n < frames; n++)
   {
      float xl = jh_finite_or_zero(in_l[n]), xr = jh_finite_or_zero(in_r[n]), yl, yr;
      if (s->mode == JH_MODE_SPEAKERS)
         frame_speakers(s, xl, xr, &yl, &yr);
      else
         frame_headphones(s, xl, xr, &yl, &yr);
      out_l[n] = yl;
      out_r[n] = yr;
   }
   guard_state(s);
}

/* ── RetroArch audio DSP filter ────────────────────────────────────────────
 *
 * The plain-C ABI of libretro-common's libretro_dspfilter.h (API version 1),
 * declared here from its public description. RetroArch loads every library in
 * the folder of the .dsp preset named by audio_dsp_plugin, calls
 * dspfilter_get_implementation, and matches the preset's "filter0 = jhspatial"
 * against short_ident. Settings come from the preset as jhspatial_mode,
 * jhspatial_strength and jhspatial_room (or filter0_mode, ...), read with the
 * config's get_float — the struct's first member, the one this plugin relies
 * on. Samples are interleaved stereo floats, processed in place.
 */

struct jh_dspfilter_info
{
   float input_rate;
};

struct jh_dspfilter_output
{
   float *samples;
   unsigned frames;
};

struct jh_dspfilter_input
{
   float *samples;
   unsigned frames;
};

struct jh_dspfilter_config
{
   int (*get_float)(void *userdata, const char *key, float *value, float default_value);
   int (*get_int)(void *userdata, const char *key, int *value, int default_value);
   int (*get_float_array)(void *userdata, const char *key, float **values, unsigned *out_num_values,
         const float *default_values, unsigned num_default_values);
   int (*get_int_array)(void *userdata, const char *key, int **values, unsigned *out_num_values,
         const int *default_values, unsigned num_default_values);
   int (*get_string)(void *userdata, const char *key, char **output, const char *default_output);
   void (*free)(void *ptr);
};

struct jh_dspfilter_implementation
{
   void *(*init)(const struct jh_dspfilter_info *info, const struct jh_dspfilter_config *config, void *userdata);
   void (*process)(void *data, struct jh_dspfilter_output *output, const struct jh_dspfilter_input *input);
   void (*free)(void *data);
   unsigned api_version;
   const char *ident;
   const char *short_ident;
};

#define JH_DSPFILTER_API_VERSION 1

static float ra_value(const struct jh_dspfilter_config *config, void *userdata, const char *key, float fallback)
{
   float v = fallback;
   if (config && config->get_float)
      config->get_float(userdata, key, &v, fallback);
   if (v != v)
      return fallback;
   return v;
}

static int ra_index(float v, int fallback)
{
   if (!(v > -1000.0f && v < 1000.0f))
      return fallback;
   return (int)floor((double)v + 0.5);
}

static void *ra_init(const struct jh_dspfilter_info *info, const struct jh_dspfilter_config *config, void *userdata)
{
   float mode, strength, room;
   if (!info || !((double)info->input_rate >= JH_RATE_MIN && (double)info->input_rate <= JH_RATE_MAX))
      return NULL;
   mode = ra_value(config, userdata, "mode", (float)JH_MODE_HEADPHONES);
   strength = ra_value(config, userdata, "strength", DEFAULT_STRENGTH);
   room = ra_value(config, userdata, "room", (float)JH_ROOM_MEDIUM);
   return jh_spatial_new((double)info->input_rate, ra_index(mode, JH_MODE_HEADPHONES), strength,
         ra_index(room, JH_ROOM_MEDIUM));
}

static void ra_process(void *data, struct jh_dspfilter_output *output, const struct jh_dspfilter_input *input)
{
   if (!output || !input)
      return;
   output->samples = input->samples;
   output->frames = input->frames;
   if (data && input->samples && input->frames)
      jh_spatial_process_interleaved((jh_spatial *)data, input->samples, (unsigned long)input->frames);
}

static void ra_free(void *data)
{
   jh_spatial_free((jh_spatial *)data);
}

static const struct jh_dspfilter_implementation ra_impl = {
   ra_init,
   ra_process,
   ra_free,
   JH_DSPFILTER_API_VERSION,
   "JukaHub Spatial",
   "jhspatial",
};

JH_EXPORT const struct jh_dspfilter_implementation *dspfilter_get_implementation(unsigned mask);

JH_EXPORT const struct jh_dspfilter_implementation *dspfilter_get_implementation(unsigned mask)
{
   (void)mask;
   return &ra_impl;
}

/* ── LADSPA ────────────────────────────────────────────────────────────────
 *
 * The LADSPA 1.1 plugin ABI, declared here from its public description:
 * ladspa_descriptor(index) hands out descriptors; a host instantiates one at
 * its sample rate, connects every port, activates it and calls run. Two audio
 * inputs, two audio outputs (in-place is fine) and three control inputs.
 * The UniqueID is not registered with ladspa.org; hosts here select the
 * plugin by its label, "jukahub_spatial".
 */

typedef float jh_ladspa_data;
typedef void *jh_ladspa_handle;

typedef struct
{
   int hint;
   jh_ladspa_data lower;
   jh_ladspa_data upper;
} jh_ladspa_range;

typedef struct jh_ladspa_descriptor
{
   unsigned long unique_id;
   const char *label;
   int properties;
   const char *name;
   const char *maker;
   const char *copyright;
   unsigned long port_count;
   const int *port_descriptors;
   const char *const *port_names;
   const jh_ladspa_range *port_range_hints;
   void *implementation_data;
   jh_ladspa_handle (*instantiate)(const struct jh_ladspa_descriptor *descriptor, unsigned long sample_rate);
   void (*connect_port)(jh_ladspa_handle instance, unsigned long port, jh_ladspa_data *location);
   void (*activate)(jh_ladspa_handle instance);
   void (*run)(jh_ladspa_handle instance, unsigned long sample_count);
   void (*run_adding)(jh_ladspa_handle instance, unsigned long sample_count);
   void (*set_run_adding_gain)(jh_ladspa_handle instance, jh_ladspa_data gain);
   void (*deactivate)(jh_ladspa_handle instance);
   void (*cleanup)(jh_ladspa_handle instance);
} jh_ladspa_descriptor;

#define LADSPA_PROPERTY_REALTIME 0x1
#define LADSPA_PROPERTY_HARD_RT_CAPABLE 0x4
#define LADSPA_PORT_INPUT 0x1
#define LADSPA_PORT_OUTPUT 0x2
#define LADSPA_PORT_CONTROL 0x4
#define LADSPA_PORT_AUDIO 0x8
#define LADSPA_HINT_BOUNDED_BELOW 0x1
#define LADSPA_HINT_BOUNDED_ABOVE 0x2
#define LADSPA_HINT_INTEGER 0x20
#define LADSPA_HINT_DEFAULT_MIDDLE 0xC0
#define LADSPA_HINT_DEFAULT_HIGH 0x100
#define LADSPA_HINT_DEFAULT_1 0x240

enum
{
   PORT_IN_L,
   PORT_IN_R,
   PORT_OUT_L,
   PORT_OUT_R,
   PORT_MODE,
   PORT_STRENGTH,
   PORT_ROOM,
   PORT_COUNT
};

static const int lad_ports[PORT_COUNT] = {
   LADSPA_PORT_INPUT | LADSPA_PORT_AUDIO,
   LADSPA_PORT_INPUT | LADSPA_PORT_AUDIO,
   LADSPA_PORT_OUTPUT | LADSPA_PORT_AUDIO,
   LADSPA_PORT_OUTPUT | LADSPA_PORT_AUDIO,
   LADSPA_PORT_INPUT | LADSPA_PORT_CONTROL,
   LADSPA_PORT_INPUT | LADSPA_PORT_CONTROL,
   LADSPA_PORT_INPUT | LADSPA_PORT_CONTROL,
};

static const char *const lad_port_names[PORT_COUNT] = {
   "Left in",
   "Right in",
   "Left out",
   "Right out",
   "Mode (0 off, 1 headphones, 2 speakers)",
   "Strength",
   "Room (0 small, 1 medium, 2 large)",
};

static const jh_ladspa_range lad_ranges[PORT_COUNT] = {
   { 0, 0.0f, 0.0f },
   { 0, 0.0f, 0.0f },
   { 0, 0.0f, 0.0f },
   { 0, 0.0f, 0.0f },
   { LADSPA_HINT_BOUNDED_BELOW | LADSPA_HINT_BOUNDED_ABOVE | LADSPA_HINT_INTEGER | LADSPA_HINT_DEFAULT_1, 0.0f, 2.0f },
   { LADSPA_HINT_BOUNDED_BELOW | LADSPA_HINT_BOUNDED_ABOVE | LADSPA_HINT_DEFAULT_HIGH, 0.0f, 1.0f },
   { LADSPA_HINT_BOUNDED_BELOW | LADSPA_HINT_BOUNDED_ABOVE | LADSPA_HINT_INTEGER | LADSPA_HINT_DEFAULT_MIDDLE, 0.0f, 2.0f },
};

typedef struct
{
   jh_spatial *dsp;
   jh_ladspa_data *port[PORT_COUNT];
} jh_ladspa_instance;

static jh_ladspa_handle lad_instantiate(const jh_ladspa_descriptor *descriptor, unsigned long sample_rate)
{
   jh_ladspa_instance *inst;
   (void)descriptor;
   inst = (jh_ladspa_instance *)calloc(1, sizeof(*inst));
   if (!inst)
      return NULL;
   inst->dsp = jh_spatial_new((double)sample_rate, JH_MODE_HEADPHONES, DEFAULT_STRENGTH, JH_ROOM_MEDIUM);
   if (!inst->dsp)
   {
      free(inst);
      return NULL;
   }
   return inst;
}

static void lad_connect(jh_ladspa_handle h, unsigned long port, jh_ladspa_data *location)
{
   jh_ladspa_instance *inst = (jh_ladspa_instance *)h;
   if (inst && port < PORT_COUNT)
      inst->port[port] = location;
}

static void lad_activate(jh_ladspa_handle h)
{
   jh_ladspa_instance *inst = (jh_ladspa_instance *)h;
   if (inst)
      jh_spatial_reset(inst->dsp);
}

static void lad_run(jh_ladspa_handle h, unsigned long count)
{
   jh_ladspa_instance *inst = (jh_ladspa_instance *)h;
   if (!inst || !inst->dsp)
      return;
   /* Controls first: a room or mode change starts from silence, a strength
    * change only re-derives the gains. An unconnected control keeps the
    * plugin's default. */
   if (inst->port[PORT_ROOM])
      jh_spatial_set_room(inst->dsp, ra_index(*inst->port[PORT_ROOM], JH_ROOM_MEDIUM));
   if (inst->port[PORT_MODE])
      jh_spatial_set_mode(inst->dsp, ra_index(*inst->port[PORT_MODE], JH_MODE_HEADPHONES));
   if (inst->port[PORT_STRENGTH])
      jh_spatial_set_strength(inst->dsp, *inst->port[PORT_STRENGTH]);
   if (!inst->port[PORT_IN_L] || !inst->port[PORT_IN_R] || !inst->port[PORT_OUT_L] || !inst->port[PORT_OUT_R])
      return;
   jh_spatial_process_planar(inst->dsp, inst->port[PORT_IN_L], inst->port[PORT_IN_R],
         inst->port[PORT_OUT_L], inst->port[PORT_OUT_R], count);
}

static void lad_cleanup(jh_ladspa_handle h)
{
   jh_ladspa_instance *inst = (jh_ladspa_instance *)h;
   if (!inst)
      return;
   jh_spatial_free(inst->dsp);
   free(inst);
}

static const jh_ladspa_descriptor lad_descriptor = {
   7310UL,
   "jukahub_spatial",
   LADSPA_PROPERTY_REALTIME | LADSPA_PROPERTY_HARD_RT_CAPABLE,
   "JukaHub Spatial (virtual surround)",
   "JukaHub",
   "AGPL-3.0 (JukaHub)",
   PORT_COUNT,
   lad_ports,
   lad_port_names,
   lad_ranges,
   NULL,
   lad_instantiate,
   lad_connect,
   lad_activate,
   lad_run,
   NULL,
   NULL,
   NULL,
   lad_cleanup,
};

JH_EXPORT const jh_ladspa_descriptor *ladspa_descriptor(unsigned long index);

JH_EXPORT const jh_ladspa_descriptor *ladspa_descriptor(unsigned long index)
{
   return index == 0 ? &lad_descriptor : NULL;
}
