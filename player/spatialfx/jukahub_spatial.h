/*
 * jukahub_spatial.h — JukaHub Spatial, the virtual-surround DSP for games.
 *
 * The C port of player/spatialfx/jh_spatial_ref.py (the specification; the
 * golden vectors in player/testdata/spatial check this port). The same .so is
 * a RetroArch audio DSP filter (dspfilter_get_implementation) and a LADSPA
 * plugin (ladspa_descriptor); this header is the plain C API the two wrappers
 * and the host tests use. Nothing here is exported from the shared library.
 *
 * One instance per stream. Not thread-safe per instance (one audio thread);
 * instances share nothing, so any number may run at once.
 */
#ifndef JUKAHUB_SPATIAL_H
#define JUKAHUB_SPATIAL_H

#ifdef __cplusplus
extern "C" {
#endif

enum {
   JH_MODE_OFF = 0,
   JH_MODE_HEADPHONES = 1,
   JH_MODE_SPEAKERS = 2
};

enum {
   JH_ROOM_SMALL = 0,
   JH_ROOM_MEDIUM = 1,
   JH_ROOM_LARGE = 2
};

/* Sample rates outside this range are refused (jh_spatial_new returns NULL). */
#define JH_RATE_MIN 8000.0
#define JH_RATE_MAX 384000.0

typedef struct jh_spatial jh_spatial;

/* A new instance at fs Hz. mode and room are clamped to their ranges and
 * strength to [0, 1]; a NaN strength is 0, i.e. off (the reference's rule).
 * NULL when fs is out of range or memory runs out. */
jh_spatial *jh_spatial_new(double fs, int mode, float strength, int room);
void jh_spatial_free(jh_spatial *s);

/* Silence every delay line and filter state; the settings stay. */
void jh_spatial_reset(jh_spatial *s);

/* Setters. A mode or room that differs from the current one resets the
 * state (a room change re-derives every delay); a strength change only
 * re-derives the gains and the loudness makeup. None of them allocates. */
void jh_spatial_set_mode(jh_spatial *s, int mode);
void jh_spatial_set_room(jh_spatial *s, int room);
void jh_spatial_set_strength(jh_spatial *s, float strength);

int jh_spatial_mode(const jh_spatial *s);
int jh_spatial_room(const jh_spatial *s);
float jh_spatial_strength(const jh_spatial *s);
double jh_spatial_rate(const jh_spatial *s);
double jh_spatial_makeup(const jh_spatial *s);

/* Process interleaved stereo (L R L R ...) in place. Off, or strength 0,
 * leaves the buffer untouched (bit-exact). A sample that is not a finite
 * number is processed as 0; every output is finite and inside (-1, 1). */
void jh_spatial_process_interleaved(jh_spatial *s, float *lr, unsigned long frames);

/* Process planar stereo. The outputs may be the inputs (in place); off or
 * strength 0 copies the input unchanged. */
void jh_spatial_process_planar(jh_spatial *s, const float *in_l, const float *in_r,
      float *out_l, float *out_r, unsigned long frames);

#ifdef __cplusplus
}
#endif

#endif /* JUKAHUB_SPATIAL_H */
