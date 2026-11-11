/*
 * test_spatial.c — host tests for the JukaHub Spatial game-audio plugin.
 *
 *   test_spatial <golden-dir> <path/to/jukahub-spatial.so>
 *
 * 1. The C API (jukahub_spatial.c linked in) against every golden vector
 *    jh_spatial_ref.py wrote, processed in odd-sized chunks.
 * 2. The built .so through dlopen, as RetroArch drives it: the audio DSP
 *    filter ABI (get_implementation -> init with a config -> in-place
 *    process -> free), against the same golden vectors.
 * 3. The same .so as a LADSPA host drives it: descriptor -> instantiate ->
 *    connect ports -> activate -> run, with separate and in-place buffers.
 * 4. Off and strength 0 are bit-exact passthrough on all three paths.
 * 5. Robustness: the limiter's bound at full scale, NaN input, runtime room
 *    and mode changes, rates from 8 kHz to 192 kHz (and a fractional one),
 *    refused rates, reset determinism.
 * 6. A rough speed figure (informational).
 *
 * Run through run-tests.sh, which builds the .so with build.sh first.
 */
#define _POSIX_C_SOURCE 200809L

#include "../jukahub_spatial.h"

#include <dlfcn.h>
#include <math.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

/* The golden files print 7 significant digits and the port runs in float, so
 * the real error is below 5e-7; 5e-6 still catches a constant that is off by
 * a little (a small-room RT60 of 0.27 instead of 0.25, or 23 normalisation
 * points instead of 24, both fail). -std=c99 keeps GCC from contracting into
 * FMA, so other CPUs stay well inside it. */
#define TOLERANCE 5e-6

static int checks = 0;
static int failures = 0;

#define CHECK(cond, ...)                                                                 \
   do                                                                                     \
   {                                                                                      \
      checks++;                                                                           \
      if (!(cond))                                                                        \
      {                                                                                   \
         failures++;                                                                      \
         fprintf(stderr, "FAIL %s:%d: ", __FILE__, __LINE__);                             \
         fprintf(stderr, __VA_ARGS__);                                                    \
         fputc('\n', stderr);                                                             \
      }                                                                                   \
   } while (0)

/* The cases of jh_spatial_ref.py, in the same order. */
typedef struct
{
   const char *name;
   double fs;
   int mode;
   float strength;
   int room;
   const char *input;
   int frames;
} test_case;

static const test_case CASES[] = {
   { "hp48_impl_med", 48000, JH_MODE_HEADPHONES, 0.75f, JH_ROOM_MEDIUM, "impL", 4800 },
   { "hp48_impr_full_large", 48000, JH_MODE_HEADPHONES, 1.0f, JH_ROOM_LARGE, "impR", 4800 },
   { "hp441_impl_half_small", 44100, JH_MODE_HEADPHONES, 0.5f, JH_ROOM_SMALL, "impL", 4410 },
   { "hp22_impl_med", 22050, JH_MODE_HEADPHONES, 0.75f, JH_ROOM_MEDIUM, "impL", 2205 },
   { "hp48_noise_full", 48000, JH_MODE_HEADPHONES, 1.0f, JH_ROOM_MEDIUM, "noise", 4800 },
   { "sp48_noise", 48000, JH_MODE_SPEAKERS, 0.75f, JH_ROOM_MEDIUM, "noise", 2400 },
   { "off48_noise", 48000, JH_MODE_OFF, 0.75f, JH_ROOM_MEDIUM, "noise", 256 },
};
#define NCASES ((int)(sizeof(CASES) / sizeof(CASES[0])))

/* Chunk sizes cycle through these, so state carried across calls is tested. */
static const unsigned CHUNKS[] = { 1, 2, 3, 5, 7, 11, 64, 127, 333, 1000 };
#define NCHUNKS ((int)(sizeof(CHUNKS) / sizeof(CHUNKS[0])))

/* ── Inputs, exactly as the reference makes them ── */

static void make_input(const test_case *c, float *lr)
{
   int i;
   memset(lr, 0, sizeof(float) * 2 * (size_t)c->frames);
   if (strcmp(c->input, "impL") == 0)
   {
      lr[0] = 1.0f;
   }
   else if (strcmp(c->input, "impR") == 0)
   {
      lr[1] = 1.0f;
   }
   else
   {
      uint64_t state = 12345;
      for (i = 0; i < c->frames; i++)
      {
         double a, b, tone;
         state = (1103515245ULL * state + 12345ULL) & 0x7FFFFFFFULL;
         a = (double)state / 2147483647.0 - 0.5;
         state = (1103515245ULL * state + 12345ULL) & 0x7FFFFFFFULL;
         b = (double)state / 2147483647.0 - 0.5;
         tone = 0.6 * sin(2.0 * 3.14159265358979323846 * 440.0 * (double)i / c->fs);
         lr[2 * i] = (float)(a + tone);
         lr[2 * i + 1] = (float)(b + tone);
      }
   }
}

static double *load_golden(const char *dir, const test_case *c)
{
   char path[4096], line[256];
   FILE *f;
   double *out;
   int n = 0;
   snprintf(path, sizeof(path), "%s/%s.txt", dir, c->name);
   f = fopen(path, "r");
   if (!f)
   {
      fprintf(stderr, "cannot open %s\n", path);
      return NULL;
   }
   out = (double *)calloc(2 * (size_t)c->frames, sizeof(double));
   while (out && fgets(line, sizeof(line), f))
   {
      char *end;
      double l, r;
      if (line[0] == '#' || line[0] == '\n' || line[0] == '\r')
         continue;
      l = strtod(line, &end);
      r = strtod(end, NULL);
      if (n >= c->frames)
      {
         n++;
         break;
      }
      out[2 * n] = l;
      out[2 * n + 1] = r;
      n++;
   }
   fclose(f);
   if (n != c->frames)
   {
      fprintf(stderr, "%s: %d frames, expected %d\n", path, n, c->frames);
      free(out);
      return NULL;
   }
   return out;
}

static double max_error(const float *got, const double *want, int frames, int *where)
{
   double worst = 0.0;
   int i;
   *where = -1;
   for (i = 0; i < 2 * frames; i++)
   {
      double e = fabs((double)got[i] - want[i]);
      if (!(e <= worst)) /* also catches NaN */
      {
         worst = isnan(e) ? INFINITY : e;
         *where = i / 2;
      }
   }
   return worst;
}

/* ── The ABIs, as a host sees them (layouts mirror the plugin's) ── */

struct dsp_info
{
   float input_rate;
};
struct dsp_output
{
   float *samples;
   unsigned frames;
};
struct dsp_input
{
   float *samples;
   unsigned frames;
};
struct dsp_config
{
   int (*get_float)(void *userdata, const char *key, float *value, float default_value);
   int (*get_int)(void *userdata, const char *key, int *value, int default_value);
   void *get_float_array;
   void *get_int_array;
   void *get_string;
   void (*free)(void *ptr);
};
struct dsp_impl
{
   void *(*init)(const struct dsp_info *info, const struct dsp_config *config, void *userdata);
   void (*process)(void *data, struct dsp_output *output, const struct dsp_input *input);
   void (*free)(void *data);
   unsigned api_version;
   const char *ident;
   const char *short_ident;
};
typedef const struct dsp_impl *(*dsp_get_impl_t)(unsigned mask);

typedef struct
{
   float mode, strength, room;
   int asked;
} fake_conf;

static int fake_get_float(void *userdata, const char *key, float *value, float default_value)
{
   fake_conf *c = (fake_conf *)userdata;
   c->asked++;
   if (strcmp(key, "mode") == 0)
      *value = c->mode;
   else if (strcmp(key, "strength") == 0)
      *value = c->strength;
   else if (strcmp(key, "room") == 0)
      *value = c->room;
   else
   {
      *value = default_value;
      return 0;
   }
   return 1;
}

static int fake_get_int(void *userdata, const char *key, int *value, int default_value)
{
   (void)userdata;
   (void)key;
   *value = default_value;
   return 0;
}

typedef struct
{
   int hint;
   float lower;
   float upper;
} lad_range;

typedef struct lad_desc
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
   const lad_range *port_range_hints;
   void *implementation_data;
   void *(*instantiate)(const struct lad_desc *descriptor, unsigned long sample_rate);
   void (*connect_port)(void *instance, unsigned long port, float *location);
   void (*activate)(void *instance);
   void (*run)(void *instance, unsigned long sample_count);
   void (*run_adding)(void *instance, unsigned long sample_count);
   void (*set_run_adding_gain)(void *instance, float gain);
   void (*deactivate)(void *instance);
   void (*cleanup)(void *instance);
} lad_desc;
typedef const lad_desc *(*lad_get_t)(unsigned long index);

/* ── 1. The C API against the golden vectors ── */

static void test_api(const char *dir)
{
   int ci;
   for (ci = 0; ci < NCASES; ci++)
   {
      const test_case *c = &CASES[ci];
      double *want = load_golden(dir, c);
      float *buf = (float *)malloc(sizeof(float) * 2 * (size_t)c->frames);
      float *orig = (float *)malloc(sizeof(float) * 2 * (size_t)c->frames);
      jh_spatial *s = jh_spatial_new(c->fs, c->mode, c->strength, c->room);
      int done = 0, k = 0, where;
      double err;
      CHECK(want && buf && orig && s, "%s: set-up failed", c->name);
      if (!want || !buf || !orig || !s)
         goto next;
      make_input(c, buf);
      memcpy(orig, buf, sizeof(float) * 2 * (size_t)c->frames);
      while (done < c->frames)
      {
         unsigned n = CHUNKS[k++ % NCHUNKS];
         if (n > (unsigned)(c->frames - done))
            n = (unsigned)(c->frames - done);
         jh_spatial_process_interleaved(s, buf + 2 * done, n);
         done += (int)n;
      }
      err = max_error(buf, want, c->frames, &where);
      CHECK(err <= TOLERANCE, "%s (C API): max error %.3g at frame %d (tolerance %.1g)", c->name, err, where, TOLERANCE);
      printf("  api      %-24s max |err| %.2e\n", c->name, err);
      if (c->mode == JH_MODE_OFF)
         CHECK(memcmp(buf, orig, sizeof(float) * 2 * (size_t)c->frames) == 0, "%s: off is not bit-exact", c->name);
   next:
      jh_spatial_free(s);
      free(want);
      free(buf);
      free(orig);
   }
}

/* ── 2. The .so as RetroArch's DSP filter ── */

static void test_retroarch(void *lib, const char *dir)
{
   dsp_get_impl_t get = (dsp_get_impl_t)(uintptr_t)dlsym(lib, "dspfilter_get_implementation");
   const struct dsp_impl *impl;
   struct dsp_config config;
   int ci;
   CHECK(get != NULL, "dspfilter_get_implementation is not exported");
   if (!get)
      return;
   impl = get(0);
   CHECK(impl != NULL, "get_implementation returned NULL");
   if (!impl)
      return;
   CHECK(impl->api_version == 1, "api_version %u, want 1", impl->api_version);
   CHECK(impl->short_ident && strcmp(impl->short_ident, "jhspatial") == 0, "short_ident %s",
         impl->short_ident ? impl->short_ident : "(null)");
   CHECK(impl->ident && impl->ident[0], "no ident");
   CHECK(impl->init && impl->process && impl->free, "missing callbacks");
   memset(&config, 0, sizeof(config));
   config.get_float = fake_get_float;
   config.get_int = fake_get_int;
   for (ci = 0; ci < NCASES; ci++)
   {
      const test_case *c = &CASES[ci];
      double *want = load_golden(dir, c);
      float *buf = (float *)malloc(sizeof(float) * 2 * (size_t)c->frames);
      float *orig = (float *)malloc(sizeof(float) * 2 * (size_t)c->frames);
      fake_conf fc;
      struct dsp_info info;
      void *data;
      int done = 0, k = 3, where;
      double err;
      fc.mode = (float)c->mode;
      fc.strength = c->strength;
      fc.room = (float)c->room;
      fc.asked = 0;
      info.input_rate = (float)c->fs;
      data = impl->init(&info, &config, &fc);
      CHECK(data != NULL, "%s: init returned NULL", c->name);
      CHECK(fc.asked >= 3, "%s: init asked %d settings, want mode, strength and room", c->name, fc.asked);
      if (!data || !want || !buf || !orig)
         goto next;
      make_input(c, buf);
      memcpy(orig, buf, sizeof(float) * 2 * (size_t)c->frames);
      while (done < c->frames)
      {
         struct dsp_input in;
         struct dsp_output out;
         unsigned n = CHUNKS[k++ % NCHUNKS];
         if (n > (unsigned)(c->frames - done))
            n = (unsigned)(c->frames - done);
         in.samples = buf + 2 * done;
         in.frames = n;
         out.samples = NULL;
         out.frames = 0;
         impl->process(data, &out, &in);
         CHECK(out.samples == in.samples && out.frames == n, "%s: process did not hand back its input buffer", c->name);
         done += (int)n;
      }
      err = max_error(buf, want, c->frames, &where);
      CHECK(err <= TOLERANCE, "%s (RetroArch ABI): max error %.3g at frame %d", c->name, err, where);
      printf("  retroarch %-23s max |err| %.2e\n", c->name, err);
      if (c->mode == JH_MODE_OFF)
         CHECK(memcmp(buf, orig, sizeof(float) * 2 * (size_t)c->frames) == 0, "%s: RetroArch off is not bit-exact", c->name);
   next:
      if (data)
         impl->free(data);
      free(want);
      free(buf);
      free(orig);
   }

   /* Strength 0 through the ABI: untouched. */
   {
      test_case c = CASES[4];
      fake_conf fc = { 1.0f, 0.0f, 1.0f, 0 };
      struct dsp_info info = { 48000.0f };
      float *buf = (float *)malloc(sizeof(float) * 2 * (size_t)c.frames);
      float *orig = (float *)malloc(sizeof(float) * 2 * (size_t)c.frames);
      void *data = impl->init(&info, &config, &fc);
      struct dsp_input in;
      struct dsp_output out;
      make_input(&c, buf);
      memcpy(orig, buf, sizeof(float) * 2 * (size_t)c.frames);
      in.samples = buf;
      in.frames = (unsigned)c.frames;
      impl->process(data, &out, &in);
      CHECK(memcmp(buf, orig, sizeof(float) * 2 * (size_t)c.frames) == 0, "RetroArch strength 0 is not bit-exact");
      impl->free(data);
      free(buf);
      free(orig);
   }
   /* A config without get_float, an empty buffer, a refused rate. */
   {
      struct dsp_config none;
      struct dsp_info info = { 44100.0f };
      struct dsp_input in = { NULL, 0 };
      struct dsp_output out = { NULL, 0 };
      void *data;
      memset(&none, 0, sizeof(none));
      data = impl->init(&info, &none, NULL);
      CHECK(data != NULL, "init with an empty config failed");
      impl->process(data, &out, &in);
      CHECK(out.frames == 0, "empty input produced frames");
      impl->free(data);
      {
         fake_conf fc = { 1.0f, 0.75f, 1.0f, 0 };
         info.input_rate = 1000.0f;
         data = impl->init(&info, &config, &fc);
         CHECK(data == NULL, "a 1 kHz rate was accepted");
         CHECK(fc.asked == 0, "a refused rate still read the settings");
         CHECK(impl->init(NULL, &config, &fc) == NULL, "init without info did not fail");
      }
   }
}

/* ── 3. The .so as a LADSPA plugin ── */

static void run_ladspa_case(const lad_desc *d, const test_case *c, const double *want, int in_place)
{
   float *in_l = (float *)calloc((size_t)c->frames, sizeof(float));
   float *in_r = (float *)calloc((size_t)c->frames, sizeof(float));
   float *out_l = in_place ? in_l : (float *)calloc((size_t)c->frames, sizeof(float));
   float *out_r = in_place ? in_r : (float *)calloc((size_t)c->frames, sizeof(float));
   float *lr = (float *)malloc(sizeof(float) * 2 * (size_t)c->frames);
   float *got = (float *)malloc(sizeof(float) * 2 * (size_t)c->frames);
   float mode = (float)c->mode, strength = c->strength, room = (float)c->room;
   void *h = d->instantiate(d, (unsigned long)c->fs);
   int i, done = 0, k = 5, where;
   double err;
   CHECK(h != NULL, "%s: instantiate failed", c->name);
   if (!h)
      goto out;
   make_input(c, lr);
   for (i = 0; i < c->frames; i++)
   {
      in_l[i] = lr[2 * i];
      in_r[i] = lr[2 * i + 1];
   }
   d->connect_port(h, 4, &mode);
   d->connect_port(h, 5, &strength);
   d->connect_port(h, 6, &room);
   if (d->activate)
      d->activate(h);
   while (done < c->frames)
   {
      unsigned n = CHUNKS[k++ % NCHUNKS];
      if (n > (unsigned)(c->frames - done))
         n = (unsigned)(c->frames - done);
      /* Hosts move the buffers between runs; reconnect every chunk. */
      d->connect_port(h, 0, in_l + done);
      d->connect_port(h, 1, in_r + done);
      d->connect_port(h, 2, out_l + done);
      d->connect_port(h, 3, out_r + done);
      d->run(h, n);
      done += (int)n;
   }
   for (i = 0; i < c->frames; i++)
   {
      got[2 * i] = out_l[i];
      got[2 * i + 1] = out_r[i];
   }
   err = max_error(got, want, c->frames, &where);
   CHECK(err <= TOLERANCE, "%s (LADSPA%s): max error %.3g at frame %d", c->name, in_place ? ", in place" : "", err, where);
   printf("  ladspa%s %-24s max |err| %.2e\n", in_place ? "=" : " ", c->name, err);
   if (c->mode == JH_MODE_OFF)
   {
      int exact = 1;
      for (i = 0; i < c->frames; i++)
         exact = exact && got[2 * i] == lr[2 * i] && got[2 * i + 1] == lr[2 * i + 1];
      CHECK(exact, "%s: LADSPA off is not bit-exact", c->name);
   }
   if (d->deactivate)
      d->deactivate(h);
   d->cleanup(h);
out:
   if (!in_place)
   {
      free(out_l);
      free(out_r);
   }
   free(in_l);
   free(in_r);
   free(lr);
   free(got);
}

static void test_ladspa(void *lib, const char *dir)
{
   lad_get_t get = (lad_get_t)(uintptr_t)dlsym(lib, "ladspa_descriptor");
   const lad_desc *d;
   int ci;
   CHECK(get != NULL, "ladspa_descriptor is not exported");
   if (!get)
      return;
   d = get(0);
   CHECK(d != NULL, "ladspa_descriptor(0) is NULL");
   CHECK(get(1) == NULL, "ladspa_descriptor(1) is not NULL");
   if (!d)
      return;
   CHECK(d->label && strcmp(d->label, "jukahub_spatial") == 0, "label %s", d->label ? d->label : "(null)");
   CHECK(d->port_count == 7, "port count %lu", d->port_count);
   CHECK(d->port_descriptors[0] == 0x9 && d->port_descriptors[1] == 0x9, "audio inputs are not INPUT|AUDIO");
   CHECK(d->port_descriptors[2] == 0xA && d->port_descriptors[3] == 0xA, "audio outputs are not OUTPUT|AUDIO");
   CHECK(d->port_descriptors[4] == 0x5 && d->port_descriptors[5] == 0x5 && d->port_descriptors[6] == 0x5,
         "controls are not INPUT|CONTROL");
   CHECK(d->instantiate && d->connect_port && d->run && d->cleanup, "missing callbacks");
   CHECK(d->run_adding == NULL && d->set_run_adding_gain == NULL, "run_adding is set without support");
   for (ci = 0; ci < NCASES; ci++)
   {
      const test_case *c = &CASES[ci];
      double *want = load_golden(dir, c);
      if (!want)
      {
         CHECK(0, "%s: no golden vector", c->name);
         continue;
      }
      run_ladspa_case(d, c, want, 0);
      run_ladspa_case(d, c, want, 1);
      free(want);
   }
   /* Runtime control changes: room, then mode, then strength 0 (copy). */
   {
      void *h = d->instantiate(d, 48000);
      float in_l[512], in_r[512], out_l[512], out_r[512];
      float mode = 1.0f, strength = 0.75f, room = 1.0f;
      int i, pass, finite = 1, exact = 1;
      for (i = 0; i < 512; i++)
      {
         in_l[i] = 0.5f * (float)sin(i * 0.05);
         in_r[i] = 0.5f * (float)cos(i * 0.031);
      }
      d->connect_port(h, 0, in_l);
      d->connect_port(h, 1, in_r);
      d->connect_port(h, 2, out_l);
      d->connect_port(h, 3, out_r);
      d->connect_port(h, 4, &mode);
      d->connect_port(h, 5, &strength);
      d->connect_port(h, 6, &room);
      d->activate(h);
      for (pass = 0; pass < 12; pass++)
      {
         room = (float)(pass % 3);
         mode = pass < 8 ? (float)(1 + pass % 2) : 9.0f; /* 9 clamps to speakers */
         d->run(h, 512);
         for (i = 0; i < 512; i++)
            finite = finite && isfinite(out_l[i]) && isfinite(out_r[i]) && fabsf(out_l[i]) < 1.0f && fabsf(out_r[i]) < 1.0f;
      }
      CHECK(finite, "LADSPA runtime control changes produced a non-finite or clipped sample");
      strength = 0.0f;
      d->run(h, 512);
      for (i = 0; i < 512; i++)
         exact = exact && out_l[i] == in_l[i] && out_r[i] == in_r[i];
      CHECK(exact, "LADSPA strength 0 is not a bit-exact copy");
      /* A NaN on the strength port is 0 too (the reference's rule). */
      strength = NAN;
      d->run(h, 512);
      exact = 1;
      for (i = 0; i < 512; i++)
         exact = exact && out_l[i] == in_l[i] && out_r[i] == in_r[i];
      CHECK(exact, "LADSPA NaN strength is not a bit-exact copy");
      d->cleanup(h);
      CHECK(d->instantiate(d, 4000) == NULL, "LADSPA accepted a 4 kHz rate");
   }
}

/* ── 5. Robustness ── */

static void test_robustness(void)
{
   static float buf[2 * 9600];
   jh_spatial *s;
   int i, ok;
   double rates[] = { 8000.0, 11025.0, 22050.0, 32040.5, 44100.0, 48000.0, 96000.0, 192000.0 };
   int mode, ri;

   /* Full-scale square through the loudest setting stays inside (-1, 1). */
   for (mode = JH_MODE_HEADPHONES; mode <= JH_MODE_SPEAKERS; mode++)
   {
      s = jh_spatial_new(48000, mode, 1.0f, JH_ROOM_LARGE);
      for (i = 0; i < 9600; i++)
      {
         float v = ((i / 120) % 2) ? 0.999f : -0.999f;
         buf[2 * i] = v;
         buf[2 * i + 1] = (i % 333 < 160) ? v : -v;
      }
      jh_spatial_process_interleaved(s, buf, 9600);
      ok = 1;
      for (i = 0; i < 2 * 9600; i++)
         ok = ok && isfinite(buf[i]) && fabsf(buf[i]) < 1.0f;
      CHECK(ok, "mode %d: full-scale input left (-1, 1)", mode);
      jh_spatial_free(s);
   }

   /* A sample that is not a finite number is processed as 0: the block is
    * exactly what the same block with zeros there gives (both modes, both
    * entry points), and the next block is clean too. */
   for (mode = JH_MODE_HEADPHONES; mode <= JH_MODE_SPEAKERS; mode++)
   {
      static float bad[2 * 512], zero[2 * 512], pl[512], pr[512];
      jh_spatial *x = jh_spatial_new(48000, mode, 0.75f, JH_ROOM_MEDIUM);
      jh_spatial *y = jh_spatial_new(48000, mode, 0.75f, JH_ROOM_MEDIUM);
      jh_spatial *z = jh_spatial_new(48000, mode, 0.75f, JH_ROOM_MEDIUM);
      for (i = 0; i < 512; i++)
      {
         zero[2 * i] = bad[2 * i] = (float)sin(i * 0.1);
         zero[2 * i + 1] = bad[2 * i + 1] = (float)sin(i * 0.13);
      }
      bad[20] = NAN;
      bad[41] = INFINITY;
      bad[300] = -INFINITY;
      zero[20] = zero[41] = zero[300] = 0.0f;
      for (i = 0; i < 512; i++)
      {
         pl[i] = bad[2 * i];
         pr[i] = bad[2 * i + 1];
      }
      jh_spatial_process_interleaved(x, bad, 512);
      jh_spatial_process_interleaved(y, zero, 512);
      jh_spatial_process_planar(z, pl, pr, pl, pr, 512);
      ok = memcmp(bad, zero, sizeof(bad)) == 0;
      CHECK(ok, "mode %d: a NaN/inf sample was not processed as 0 (interleaved)", mode);
      ok = 1;
      for (i = 0; i < 512; i++)
         ok = ok && pl[i] == zero[2 * i] && pr[i] == zero[2 * i + 1];
      CHECK(ok, "mode %d: a NaN/inf sample was not processed as 0 (planar)", mode);
      ok = 1;
      for (i = 0; i < 2 * 512; i++)
         ok = ok && isfinite(bad[i]) && fabsf(bad[i]) < 1.0f;
      CHECK(ok, "mode %d: a NaN/inf input gave a non-finite or full-scale output", mode);
      for (i = 0; i < 4800; i++)
      {
         buf[2 * i] = 0.3f * (float)sin(i * 0.1);
         buf[2 * i + 1] = 0.3f * (float)sin(i * 0.13);
      }
      jh_spatial_process_interleaved(x, buf, 4800);
      ok = 1;
      for (i = 0; i < 2 * 4800; i++)
         ok = ok && isfinite(buf[i]);
      CHECK(ok, "mode %d: a NaN input poisoned the following block", mode);
      jh_spatial_free(x);
      jh_spatial_free(y);
      jh_spatial_free(z);
   }

   /* The limiter's ceiling: far over full scale the output is exactly the
    * largest float below 1, never 1.0. */
   s = jh_spatial_new(48000, JH_MODE_SPEAKERS, 1.0f, JH_ROOM_MEDIUM);
   for (i = 0; i < 64; i++)
   {
      buf[2 * i] = (i % 2) ? 1e6f : -1e6f;
      buf[2 * i + 1] = buf[2 * i];
   }
   jh_spatial_process_interleaved(s, buf, 64);
   ok = 1;
   for (i = 0; i < 2 * 64; i++)
      ok = ok && fabsf(buf[i]) == 0.99999994f;
   CHECK(ok, "the limiter's ceiling is not 0x3F7FFFFF at 1e6 input (got %.9g)", (double)buf[0]);
   jh_spatial_free(s);

   /* Every rate starts, runs finite and bounded; out-of-range rates refuse. */
   for (ri = 0; ri < (int)(sizeof(rates) / sizeof(rates[0])); ri++)
   {
      test_case c = { "rate", rates[ri], JH_MODE_HEADPHONES, 1.0f, JH_ROOM_LARGE, "noise", 4000 };
      s = jh_spatial_new(rates[ri], JH_MODE_HEADPHONES, 1.0f, JH_ROOM_LARGE);
      CHECK(s != NULL, "rate %.1f refused", rates[ri]);
      if (!s)
         continue;
      make_input(&c, buf);
      jh_spatial_process_interleaved(s, buf, 4000);
      ok = 1;
      for (i = 0; i < 8000; i++)
         ok = ok && isfinite(buf[i]) && fabsf(buf[i]) < 1.0f;
      CHECK(ok, "rate %.1f: non-finite or clipped output", rates[ri]);
      CHECK(jh_spatial_makeup(s) > 0.3 && jh_spatial_makeup(s) < 3.0, "rate %.1f: makeup %.3f", rates[ri],
            jh_spatial_makeup(s));
      jh_spatial_free(s);
   }
   CHECK(jh_spatial_new(7999.0, 1, 0.75f, 1) == NULL, "7999 Hz accepted");
   CHECK(jh_spatial_new(NAN, 1, 0.75f, 1) == NULL, "NaN rate accepted");
   CHECK(jh_spatial_new(500000.0, 1, 0.75f, 1) == NULL, "500 kHz accepted");

   /* Clamping. */
   s = jh_spatial_new(48000, 7, 3.0f, -4);
   CHECK(s && jh_spatial_mode(s) == JH_MODE_SPEAKERS && jh_spatial_strength(s) == 1.0f && jh_spatial_room(s) == JH_ROOM_SMALL,
         "out-of-range settings were not clamped");
   jh_spatial_set_strength(s, NAN);
   CHECK(jh_spatial_strength(s) == 0.0f, "NaN strength did not read as 0 (the reference's rule)");
   jh_spatial_free(s);
   /* ... and a NaN strength at creation is off: bit-exact passthrough. */
   {
      static float a[2 * 256], b[2 * 256];
      for (i = 0; i < 2 * 256; i++)
         a[i] = b[i] = 0.4f * (float)sin(i * 0.07);
      s = jh_spatial_new(48000, JH_MODE_HEADPHONES, NAN, JH_ROOM_MEDIUM);
      jh_spatial_process_interleaved(s, a, 256);
      CHECK(memcmp(a, b, sizeof(a)) == 0, "a NaN strength at creation did not pass the input through");
      jh_spatial_free(s);
   }

   /* Reset gives exactly a fresh instance. */
   {
      test_case c = CASES[4];
      static float a[2 * 4800], b[2 * 4800];
      jh_spatial *x = jh_spatial_new(48000, JH_MODE_HEADPHONES, 1.0f, JH_ROOM_MEDIUM);
      jh_spatial *y = jh_spatial_new(48000, JH_MODE_HEADPHONES, 1.0f, JH_ROOM_MEDIUM);
      make_input(&c, a);
      jh_spatial_process_interleaved(x, a, 4800); /* dirty x */
      jh_spatial_reset(x);
      make_input(&c, a);
      make_input(&c, b);
      jh_spatial_process_interleaved(x, a, 4800);
      jh_spatial_process_interleaved(y, b, 4800);
      CHECK(memcmp(a, b, sizeof(a)) == 0, "reset does not equal a fresh instance");
      /* A room change and back is the same as a reset. */
      jh_spatial_set_room(x, JH_ROOM_LARGE);
      jh_spatial_set_room(x, JH_ROOM_MEDIUM);
      jh_spatial_reset(y);
      make_input(&c, a);
      make_input(&c, b);
      jh_spatial_process_interleaved(x, a, 4800);
      jh_spatial_process_interleaved(y, b, 4800);
      CHECK(memcmp(a, b, sizeof(a)) == 0, "room large -> medium differs from a fresh medium room");
      jh_spatial_free(x);
      jh_spatial_free(y);
   }
}

/* ── 6. Speed (informational) ── */

static void bench(void)
{
   enum { FRAMES = 48000 * 10 };
   float *buf = (float *)malloc(sizeof(float) * 2 * FRAMES);
   jh_spatial *s = jh_spatial_new(48000, JH_MODE_HEADPHONES, 0.75f, JH_ROOM_LARGE);
   test_case c = { "bench", 48000, JH_MODE_HEADPHONES, 0.75f, JH_ROOM_LARGE, "noise", FRAMES };
   struct timespec t0, t1;
   double secs;
   int i;
   make_input(&c, buf);
   clock_gettime(CLOCK_MONOTONIC, &t0);
   for (i = 0; i < FRAMES; i += 512)
      jh_spatial_process_interleaved(s, buf + 2 * i, (unsigned long)(FRAMES - i < 512 ? FRAMES - i : 512));
   clock_gettime(CLOCK_MONOTONIC, &t1);
   secs = (double)(t1.tv_sec - t0.tv_sec) + (double)(t1.tv_nsec - t0.tv_nsec) / 1e9;
   printf("  speed: 10 s of 48 kHz stereo (headphones, large room) in %.3f s = %.0fx real time, %.1f ns/frame\n", secs,
         10.0 / secs, secs / FRAMES * 1e9);
   jh_spatial_free(s);
   free(buf);
}

int main(int argc, char **argv)
{
   void *lib;
   if (argc != 3)
   {
      fprintf(stderr, "usage: %s <golden-dir> <jukahub-spatial.so>\n", argv[0]);
      return 2;
   }
   setvbuf(stdout, NULL, _IOLBF, 0);
   printf("JukaHub Spatial host tests\n");
   test_api(argv[1]);
   lib = dlopen(argv[2], RTLD_NOW | RTLD_LOCAL);
   CHECK(lib != NULL, "dlopen %s: %s", argv[2], dlerror());
   if (lib)
   {
      test_retroarch(lib, argv[1]);
      test_ladspa(lib, argv[1]);
   }
   test_robustness();
   bench();
   if (lib)
      dlclose(lib);
   if (failures)
   {
      printf("FAILED: %d of %d checks\n", failures, checks);
      return 1;
   }
   printf("PASS: %d checks\n", checks);
   return 0;
}
