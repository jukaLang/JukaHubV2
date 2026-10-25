/* openslessim.c — libOpenSLES.so: real audio playback for NDK games on the
 * A133P, sized to the path games actually drive:
 *
 *   slCreateEngine → Realize → GetInterface(SL_IID_ENGINE) →
 *   CreateAudioPlayer → Realize → RegisterCallback / Enqueue →
 *   SetPlayState(SL_PLAYSTATE_PLAYING) → ... → SetPlayState(STOPPED) → Destroy
 *
 * PCM flows one way: S16 interleaved little-endian, 1 or 2 channels, any
 * sample rate → ALSA "default" through dlopen("libasound.so.2") — the same
 * library stock TrimUI firmware loads for its own audio stack. Writes are
 * blocking and paced by the shim's own worker thread, so the game's Enqueue
 * returns immediately and the SLBufferQueue callback fires on real buffer
 * completion, from the worker (exactly how native Android delivers it).
 *
 * When libasound is missing or the device can't be opened, playback does not
 * fail: buffers are "played" against a wall-clock pacer at the game's sample
 * rate — callback order and timing stay faithful, output is silence, since
 * no speakers to feed and a graceful downgrade, and the log says so once.
 *
 * Interfaces that need decoders or DSP the shim does not ship (EQ, reverb,
 * 3D, pitch, record, Android effects) fail with SL_RESULT_FEATURE_UNSUPPORTED
 * and log which interface ID asked, so a game degrades gracefully or the log
 * names the next shim target.
 *
 * ABI notes (all cross-checked against the Khronos 1.0.1 header AOSP ships
 * and wilhelm's OpenSLES_IID.c):
 *  · interface handles are `const struct XItf_ *const *` — *handle is the
 *    vtable, methods receive the handle as self. We model a handle as
 *    struct SLIface { vt, obj } so methods can reach their owning object.
 *  · SL_RESULT_* values are verbatim; the old skeleton's values were off by
 *    one for everything past RESOURCE_ERROR, which would have broken any
 *    game that ordinal-checks results.
 *  · interface UUIDs are compared by full 16-byte value, matching the
 *    constants games embed from OpenSLES_IID.h.
 */

#define _GNU_SOURCE
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "actbridge.h" /* ANDROIDRT_EXPORT */
#include "dlport.h"

/* ── SLresult / lifecycle constants, byte-for-byte OpenSL ES 1.0.1 ───── */
typedef uint32_t SLuint32;
typedef int32_t  SLint32;
typedef int16_t  SLint16;
typedef uint32_t SLboolean;
typedef SLuint32 SLresult;
typedef SLuint32 SLmillisecond;

#define SL_RESULT_SUCCESS                     0x00000000
#define SL_RESULT_PRECONDITIONS_VIOLATED      0x00000001
#define SL_RESULT_PARAMETER_INVALID           0x00000002
#define SL_RESULT_MEMORY_FAILURE              0x00000003
#define SL_RESULT_RESOURCE_ERROR              0x00000004
#define SL_RESULT_RESOURCE_LOST               0x00000005
#define SL_RESULT_IO_ERROR                    0x00000006
#define SL_RESULT_BUFFER_INSUFFICIENT         0x00000007
#define SL_RESULT_CONTENT_CORRUPTED           0x00000008
#define SL_RESULT_CONTENT_UNSUPPORTED         0x00000009
#define SL_RESULT_CONTENT_NOT_FOUND           0x0000000A
#define SL_RESULT_PERMISSION_DENIED           0x0000000B
#define SL_RESULT_FEATURE_UNSUPPORTED         0x0000000C
#define SL_RESULT_INTERNAL_ERROR              0x0000000D
#define SL_RESULT_UNKNOWN_ERROR               0x0000000E
#define SL_RESULT_OPERATION_ABORTED           0x0000000F
#define SL_RESULT_CONTROL_LOST                0x00000010

#define SL_OBJECT_STATE_UNREALIZED 1
#define SL_OBJECT_STATE_REALIZED   2
#define SL_OBJECT_STATE_SUSPENDED  3

#define SL_PLAYSTATE_STOPPED 1
#define SL_PLAYSTATE_PAUSED  2
#define SL_PLAYSTATE_PLAYING 3

#define SL_TIME_UNKNOWN 0xFFFFFFFFu

#define SL_TRUE  1u
#define SL_FALSE 0u

/* Layout per `struct SLInterfaceID_` — 16 bytes exactly. */
struct SLInterfaceID_ {
    uint32_t time_low;
    uint16_t time_mid;
    uint16_t time_hi_and_version;
    uint16_t clock_seq;
    uint8_t  node[6];
};
typedef const struct SLInterfaceID_ *SLInterfaceID;

/* Interface UUIDs, quoted from wilhelm libopensles/OpenSLES_IID.c — the
 * same constants games embed and pass by address through SL_IID_*. */
static const struct SLInterfaceID_ kIidObject = {
    0x79216360, 0xddd7, 0x11db, 0xac16, { 0x00, 0x02, 0xa5, 0xd5, 0xc5, 0x1b } };
static const struct SLInterfaceID_ kIidEngine = {
    0x8d97c260, 0xddd4, 0x11db, 0x958f, { 0x00, 0x02, 0xa5, 0xd5, 0xc5, 0x1b } };
static const struct SLInterfaceID_ kIidPlay = {
    0xef0bd9c0, 0xddd7, 0x11db, 0xbf49, { 0x00, 0x02, 0xa5, 0xd5, 0xc5, 0x1b } };
static const struct SLInterfaceID_ kIidBufferQueue = {
    0x2bc99cc0, 0xddd4, 0x11db, 0x8d99, { 0x00, 0x02, 0xa5, 0xd5, 0xc5, 0x1b } };
static const struct SLInterfaceID_ kIidOutputMix = {
    0x97750f60, 0xddd7, 0x11db, 0x92b1, { 0x00, 0x02, 0xa5, 0xd5, 0xc5, 0x1b } };
static const struct SLInterfaceID_ kIidVolume = {
    0x09e8ede0, 0xddde, 0x11db, 0xb4f6, { 0x00, 0x02, 0xa5, 0xd5, 0xc5, 0x1b } };
/* Android extension (OpenSLES_Android.h), API level 9. */
static const struct SLInterfaceID_ kIidAndroidSimpleBufferQueue = {
    0x198e4940, 0xc5d7, 0x11df, 0xa2a6, { 0x00, 0x02, 0xa5, 0xd5, 0xc5, 0x1b } };

/* Data source/sink structs, member order per SLES/OpenSLES.h. */
struct SLDataSource {
    void *pLocator;
    void *pFormat;
};
struct SLDataSink {
    void *pLocator;
    void *pFormat;
};
struct SLDataLocator_BufferQueue {
    SLuint32 locatorType;
    SLuint32 numBuffers;
};
struct SLDataLocator_OutputMix {
    SLuint32 locatorType;
    void    *outputMix;
};
struct SLDataFormat_PCM {
    SLuint32 formatType;
    SLuint32 numChannels;
    SLuint32 samplesPerSec; /* milliHertz */
    SLuint32 bitsPerSample;
    SLuint32 containerSize;
    SLuint32 channelMask;
    SLuint32 endianness;
};
struct SLBufferQueueState {
    SLuint32 count;
    SLuint32 playIndex;
};

#define SL_DATALOCATOR_BUFFERQUEUE 0x00000006u
#define SL_DATALOCATOR_OUTPUTMIX   0x00000004u
#define SL_DATAFORMAT_PCM          0x00000002u
#define SL_BYTEORDER_LITTLEENDIAN  0x00000002u
#define SL_SPEAKER_FRONT_LEFT      0x00000001u
#define SL_SPEAKER_FRONT_RIGHT     0x00000002u
#define SL_SPEAKER_FRONT_CENTER    0x00000004u

#define SLogF(...) fprintf(stderr, "[androidrt][audio] " __VA_ARGS__)

/* ── dynamic ALSA (snd_pcm_* through libasound.so.2) ─────────────────── */

struct Alsa {
    void *lib;
    const char *(*pcmStrError)(void *, int);
    int (*pcmOpen)(void **, const char *, int, int, void **);
    int (*pcmSetParams)(void *, int, int, unsigned, unsigned, int, unsigned);
    int (*pcmClose)(void *);
    long (*pcmWritei)(void *, const void *, unsigned long);
};
static struct Alsa g_alsa;
static int g_alsaTried;

#define ALSA_STREAM_PLAYBACK   0
#define ALSA_PCM_FORMAT_S16_LE 2
#define ALSA_RW_INTERLEAVED    3
#define ALSA_LATENCY_US        200000

static void alsa_init(void) {
    if (g_alsaTried) return;
    g_alsaTried = 1;
    memset(&g_alsa, 0, sizeof g_alsa);
    g_alsa.lib = rt_dlopen("libasound.so.2", 0);
    if (!g_alsa.lib) {
        SLogF("libasound.so.2 not present (%s) — playback paces silent at the game's rate\n",
              rt_dlerror());
        return;
    }
    g_alsa.pcmStrError  = rt_dlsym(g_alsa.lib, "snd_strerror");
    g_alsa.pcmOpen      = rt_dlsym(g_alsa.lib, "snd_pcm_open");
    g_alsa.pcmSetParams = rt_dlsym(g_alsa.lib, "snd_pcm_set_params");
    g_alsa.pcmClose     = rt_dlsym(g_alsa.lib, "snd_pcm_close");
    g_alsa.pcmWritei    = rt_dlsym(g_alsa.lib, "snd_pcm_writei");
    if (!g_alsa.pcmOpen || !g_alsa.pcmSetParams || !g_alsa.pcmClose || !g_alsa.pcmWritei) {
        SLogF("libasound.so.2 lacks the PCM symbols the writer needs — silent pacing\n");
        rt_dlclose(g_alsa.lib);
        g_alsa.lib = NULL;
    } else {
        SLogF("ALSA backend ready (snd_pcm_* via libasound.so.2)\n");
    }
}

/* ── objects, handles, per-spec vtables ──────────────────────────────── */

struct SLObject;
struct SLDataFormat_PCM;
struct SLBufferQueueState;

/* Every interface handle points at its interface vtable; *handle is the
 * vtable, self carries the owning object back. */
struct SLIface {
    const void      *vt;
    struct SLObject *obj;
};

typedef void (*slBufferQueueCallback)(struct SLIface *caller, void *pContext);
typedef void (*slPlayCallback)(struct SLIface *caller, void *pContext, SLuint32 event);
typedef void (*slObjectCallback)(void *caller, const void *pContext, SLuint32 event,
                                 SLresult result, SLuint32 param, void *pInterface);

/* SLObjectItf — 10 methods, order verbatim from the 1.0.1 header. */
struct SLObjectItfVT {
    SLresult (*Realize)(struct SLIface *self, SLboolean async);
    SLresult (*Resume)(struct SLIface *self, SLboolean async);
    SLresult (*GetState)(struct SLIface *self, SLuint32 *pState);
    SLresult (*GetInterface)(struct SLIface *self, SLInterfaceID iid, void **pItf);
    SLresult (*RegisterCallback)(struct SLIface *self, slObjectCallback callback, void *pContext);
    void     (*AbortAsyncOperation)(struct SLIface *self);
    void     (*Destroy)(struct SLIface *self);
    SLresult (*SetPriority)(struct SLIface *self, SLint32 priority, SLboolean preemptable);
    SLresult (*GetPriority)(struct SLIface *self, SLint32 *pPriority, SLboolean *pPreemptable);
    SLresult (*SetLossOfControlInterfaces)(struct SLIface *self, SLint16 n,
                                           const SLInterfaceID *pInterfaceIDs,
                                           SLboolean enabled);
};

/* SLEngineItf — 15 methods, slot order verbatim from the 1.0.1 header.
 * CreateAudioPlayer is slot 2, CreateOutputMix slot 7; games reach both
 * through this table. */
struct SLEngineItfVT {
    SLresult (*CreateLEDDevice)(struct SLIface *self, void **pDevice, SLuint32 deviceID,
                                SLuint32 n, const SLInterfaceID *iids, const SLboolean *req);
    SLresult (*CreateVibraDevice)(struct SLIface *self, void **pDevice, SLuint32 deviceID,
                                  SLuint32 n, const SLInterfaceID *iids, const SLboolean *req);
    SLresult (*CreateAudioPlayer)(struct SLIface *self, void **pPlayer,
                                  const struct SLDataSource *pAudioSrc,
                                  const struct SLDataSink *pAudioSnk,
                                  SLuint32 n, const SLInterfaceID *iids, const SLboolean *req);
    SLresult (*CreateAudioRecorder)(struct SLIface *self, void **pRecorder,
                                    const struct SLDataSource *pAudioSrc,
                                    const struct SLDataSink *pAudioSnk,
                                    SLuint32 n, const SLInterfaceID *iids, const SLboolean *req);
    SLresult (*CreateMidiPlayer)(struct SLIface *self, void **pPlayer,
                                 const struct SLDataSource *pMIDISrc,
                                 const struct SLDataSource *pBankSrc,
                                 const struct SLDataSink *pAudioOutput,
                                 const struct SLDataSink *pVibra,
                                 const struct SLDataSink *pLEDArray,
                                 SLuint32 n, const SLInterfaceID *iids, const SLboolean *req);
    SLresult (*CreateListener)(struct SLIface *self, void **pListener, SLuint32 n,
                               const SLInterfaceID *iids, const SLboolean *req);
    SLresult (*Create3DGroup)(struct SLIface *self, void **pGroup, SLuint32 n,
                              const SLInterfaceID *iids, const SLboolean *req);
    SLresult (*CreateOutputMix)(struct SLIface *self, void **pMix, SLuint32 n,
                                const SLInterfaceID *iids, const SLboolean *req);
    SLresult (*CreateMetadataExtractor)(struct SLIface *self, void **pObject,
                                        const struct SLDataSource *pSrc,
                                        SLuint32 n, const SLInterfaceID *iids,
                                        const SLboolean *req);
    SLresult (*CreateExtensionObject)(struct SLIface *self, void **pObject, void *pParams,
                                      SLuint32 objectID, SLuint32 n,
                                      const SLInterfaceID *iids, const SLboolean *req);
    SLresult (*QueryNumSupportedInterfaces)(struct SLIface *self, SLuint32 objectID,
                                            SLuint32 *pNum);
    SLresult (*QuerySupportedInterfaces)(struct SLIface *self, SLuint32 objectID,
                                         SLuint32 index, SLInterfaceID *pIid);
    SLresult (*QueryNumSupportedExtensions)(struct SLIface *self, SLuint32 *pNum);
    SLresult (*QuerySupportedExtension)(struct SLIface *self, SLuint32 index,
                                        const void *pExtensionName, SLint16 *pLength);
    SLresult (*IsExtensionSupported)(struct SLIface *self, const void *pExtensionName,
                                     SLboolean *pSupported);
};

/* SLPlayItf — 12 methods, order verbatim from the header. */
struct SLPlayItfVT {
    SLresult (*SetPlayState)(struct SLIface *self, SLuint32 state);
    SLresult (*GetPlayState)(struct SLIface *self, SLuint32 *pState);
    SLresult (*GetDuration)(struct SLIface *self, SLmillisecond *pMsec);
    SLresult (*GetPosition)(struct SLIface *self, SLmillisecond *pMsec);
    SLresult (*RegisterCallback)(struct SLIface *self, slPlayCallback callback, void *pContext);
    SLresult (*SetCallbackEventsMask)(struct SLIface *self, SLuint32 eventFlags);
    SLresult (*GetCallbackEventsMask)(struct SLIface *self, SLuint32 *pEventFlags);
    SLresult (*SetMarkerPosition)(struct SLIface *self, SLmillisecond mSec);
    SLresult (*ClearMarkerPosition)(struct SLIface *self);
    SLresult (*GetMarkerPosition)(struct SLIface *self, SLmillisecond *pMsec);
    SLresult (*SetPositionUpdatePeriod)(struct SLIface *self, SLmillisecond mSec);
    SLresult (*GetPositionUpdatePeriod)(struct SLIface *self, SLmillisecond *pMsec);
};

/* SLBufferQueueItf and SLAndroidSimpleBufferQueueItf are member-compatible
 * (Enqueue / Clear / GetState / RegisterCallback), so one table serves both. */
struct SLBufferQueueItfVT {
    SLresult (*Enqueue)(struct SLIface *self, const void *pBuffer, SLuint32 size);
    SLresult (*Clear)(struct SLIface *self);
    SLresult (*GetState)(struct SLIface *self, struct SLBufferQueueState *pState);
    SLresult (*RegisterCallback)(struct SLIface *self, slBufferQueueCallback callback,
                                 void *pContext);
};

/* SLOutputMixItf — 3 methods, order verbatim from the header. */
struct SLOutputMixItfVT {
    SLresult (*GetDestinationOutputDeviceIDs)(struct SLIface *self, SLint32 *pNumDevices,
                                              SLuint32 *pDeviceIDs);
    SLresult (*RegisterDeviceChangeCallback)(struct SLIface *self, void *callback, void *pContext);
    SLresult (*ReRoute)(struct SLIface *self, SLint32 numOutputDevices,
                        SLuint32 *pOutputDeviceIDs);
};

/* SLVolumeItf — 9 methods, order verbatim from the header. */
struct SLVolumeItfVT {
    SLresult (*SetVolumeLevel)(struct SLIface *self, SLint16 level);
    SLresult (*GetVolumeLevel)(struct SLIface *self, SLint16 *pLevel);
    SLresult (*GetMaxVolumeLevel)(struct SLIface *self, SLint16 *pMaxLevel);
    SLresult (*SetMute)(struct SLIface *self, SLboolean mute);
    SLresult (*GetMute)(struct SLIface *self, SLboolean *pMute);
    SLresult (*EnableStereoPosition)(struct SLIface *self, SLboolean enable);
    SLresult (*IsEnabledStereoPosition)(struct SLIface *self, SLboolean *pEnable);
    SLresult (*SetStereoPosition)(struct SLIface *self, SLint16 stereoPosition);
    SLresult (*GetStereoPosition)(struct SLIface *self, SLint16 *pStereoPosition);
};

#define OBJ_KIND_ENGINE  0
#define OBJ_KIND_PLAYER  1
#define OBJ_KIND_MIX     2

/* One queued Enqueue buffer. Bytes are copied so the game may reuse the
 * memory immediately after Enqueue returns (over-compliant: the ABI forbids
 * touching the buffer only until the callback, so copying is safe for
 * compliant games and forgiving for careless ones). */
struct SLBuf {
    uint8_t *data;
    size_t   size;
};

#define QUEUE_CAP        64
#define MIN_RATE_MILLIHZ  8000000u
#define MAX_RATE_MILLIHZ 48000000u
#define UNDERRUN_FRAMES  256  /* silence block fed to ALSA between Enqueues */

struct SLObject {
    SLuint32 state;
    int      kind;

    /* pthread prims, initialized in every kind (Destroy is uniform) */
    pthread_mutex_t lock;
    pthread_cond_t  cvRoom;  /* queue has room for the next Enqueue */
    pthread_cond_t  cvWake;  /* worker kick                          */

    /* cached interface handles — identity is stable for the object's life */
    struct SLIface ifObject;
    struct SLIface ifEngine;
    struct SLIface ifPlay;
    struct SLIface ifBq;
    struct SLIface ifMix;
    struct SLIface ifVolume;
    uint32_t unsupportedLogged; /* one log line per distinct unknown IID */

    /* player ── */
    void    *alsaPcm;
    int      alsaDead;            /* write failed: keep the handle so    */
                                  /* objFree still closes it, but stream  */
                                  /* silent instead of erroring forever   */
    uint8_t *zeroPcm;            /* zero block feeding ALSA on underrun  */

    pthread_t workerThread;
    int       workerStarted;
    int       quit;
    int       dying;

    SLuint32 rateMilliHz;
    SLuint32 channels;

    SLuint32 playState;
    uint64_t framesDone;         /* consumed by the device while PLAYING */
    uint32_t bufsDone;           /* buffers handed back (GetState.playIndex) */

    /* producer-consumer queue */
    struct SLBuf queue[QUEUE_CAP];
    size_t       qHead, qTail, qCount;

    /* buffer currently being streamed, owned by the worker alone */
    uint8_t *pendBuf;            /* malloc'd copy, freed when consumed    */
    const uint8_t *pendPos;      /* next bytes to write                   */
    size_t   pendFramesLeft;
    int      dropPending;        /* stop/pause asked mid-buffer: discard  */

    slBufferQueueCallback bqCallback;
    void                 *bqCtx;

    SLint16  volumeMB;           /* 0 = full volume (SLVolumeItf level)  */
};

static struct SLObject *ifaceObj(struct SLIface *self) { return self->obj; }

static const char *playStateName(SLuint32 st) {
    switch (st) {
        case SL_PLAYSTATE_STOPPED: return "STOPPED";
        case SL_PLAYSTATE_PAUSED:  return "PAUSED";
        case SL_PLAYSTATE_PLAYING: return "PLAYING";
        default:                   return "?";
    }
}

static int iidEq(SLInterfaceID a, SLInterfaceID b) {
    return a && b && memcmp(a, b, sizeof(struct SLInterfaceID_)) == 0;
}

/* vtables live below; objNew wires them into every object's if* suffixes */
static const struct SLObjectItfVT      g_objectVt;
static const struct SLEngineItfVT      g_engineVt;
static const struct SLPlayItfVT        g_playVt;
static const struct SLBufferQueueItfVT g_bqVt;
static const struct SLOutputMixItfVT   g_outputMixVt;
static const struct SLVolumeItfVT      g_volumeVt;

/* ── allocator: one construction path for every object kind ─────────── */

static struct SLObject *objNew(int kind) {
    struct SLObject *o = calloc(1, sizeof *o);
    if (!o) return NULL;
    o->state = SL_OBJECT_STATE_UNREALIZED;
    o->kind  = kind;
    pthread_mutex_init(&o->lock, NULL);
    pthread_cond_init(&o->cvRoom, NULL);
    pthread_cond_init(&o->cvWake, NULL);
    o->ifObject.vt = &g_objectVt; o->ifObject.obj = o;
    o->ifVolume.vt = &g_volumeVt; o->ifVolume.obj = o;
    if (kind == OBJ_KIND_ENGINE) {
        o->ifEngine.vt = &g_engineVt; o->ifEngine.obj = o;
    } else if (kind == OBJ_KIND_PLAYER) {
        o->ifPlay.vt = &g_playVt; o->ifPlay.obj = o;
        o->ifBq.vt   = &g_bqVt;   o->ifBq.obj   = o;
    } else {
        o->ifMix.vt = &g_outputMixVt; o->ifMix.obj = o;
    }
    return o;
}

/* Frees everything an object holds. Only the worker (players) or the
 * destroying thread (engine/mix) may run this, never both. */
static void objFree(struct SLObject *o) {
    if (g_alsa.lib && o->alsaPcm) g_alsa.pcmClose(o->alsaPcm);
    free(o->pendBuf);
    free(o->zeroPcm);
    for (size_t i = 0; i < QUEUE_CAP; i++) free(o->queue[i].data);
    pthread_cond_destroy(&o->cvWake);
    pthread_cond_destroy(&o->cvRoom);
    pthread_mutex_destroy(&o->lock);
    free(o);
}

/* ── worker: drains the queue into the device ───────────────────────── */

/* Writes `frames` S16 frames, blocking; returns frames consumed (== frames
 * on success, negative on hard failure). With no ALSA the write becomes a
 * wall-clock pacer: the call costs exactly the duration the frame block
 * represents, so callback timing stays truthful in silent mode too. */
static long dev_write(struct SLObject *p, const uint8_t *bytes, size_t frames) {
    if (p->alsaPcm && !p->alsaDead) {
        long r = g_alsa.pcmWritei(p->alsaPcm, bytes, (unsigned long)frames);
        if (r < 0) {
            SLogF("snd_pcm_writei failed (%s) — switching to silent pacing\n",
                  g_alsa.pcmStrError ? g_alsa.pcmStrError(p->alsaPcm, (int)r) : "?");
            return r;
        }
        return r; /* usually == frames; a short write just re-loops */
    }
    uint64_t hz = (uint64_t)p->rateMilliHz / 1000u;
    if (hz == 0) hz = 1;
    struct timespec ts;
    ts.tv_sec  = (time_t)(frames / hz);
    ts.tv_nsec = (long)(((uint64_t)(frames % hz) * 1000000000ull) / hz);
    nanosleep(&ts, NULL);
    return (long)frames;
}

static void *playerWriterThread(void *arg) {
    struct SLObject *p = arg;
    size_t chunkFrames = UNDERRUN_FRAMES;
    SLogF("player thread up: %u mHz, %u ch — ALSA %s\n",
          (unsigned)p->rateMilliHz, (unsigned)p->channels,
          p->alsaPcm ? "device open" : "missing → silent pacing");

    pthread_mutex_lock(&p->lock);
    for (;;) {
        while (!p->quit && p->pendFramesLeft == 0 && p->qCount == 0 &&
               p->playState != SL_PLAYSTATE_PLAYING)
            pthread_cond_wait(&p->cvWake, &p->lock);
        if (p->quit) break;

        /* Take the next buffer under the lock, then stream it without the
         * lock so Enqueue never queues behind a blocking write. */
        if (p->pendFramesLeft == 0 && p->qCount > 0) {
            struct SLBuf *b = &p->queue[p->qHead];
            p->pendBuf        = b->data;      /* ownership moves to worker */
            p->pendPos        = b->data;
            p->pendFramesLeft = b->size / (2u * p->channels);
            b->data = NULL;
            b->size = 0;
            p->qHead = (p->qHead + 1) % QUEUE_CAP;
            p->qCount--;
            pthread_cond_signal(&p->cvRoom);
        }

        if (p->pendFramesLeft > 0) {
            size_t want = p->pendFramesLeft < chunkFrames ? p->pendFramesLeft : chunkFrames;
            pthread_mutex_unlock(&p->lock);
            long wrote = dev_write(p, p->pendPos, want);
            pthread_mutex_lock(&p->lock);
            if (p->dropPending) {
                /* stop/pause while streaming: throw the rest away */
                p->dropPending = 0;
                p->pendFramesLeft = 0;
            } else if (wrote > 0) {
                p->pendPos += (size_t)wrote * 2u * p->channels;
                p->pendFramesLeft -= (size_t)wrote;
                if (p->playState == SL_PLAYSTATE_PLAYING) p->framesDone += (uint64_t)wrote;
                if (p->pendFramesLeft == 0) {
                    p->bufsDone++;
                    slBufferQueueCallback cb = p->bqCallback;
                    void *ctx = p->bqCtx;
                    struct SLIface *caller = &p->ifBq;
                    free(p->pendBuf);
                    p->pendBuf = NULL;
                    /* callback with the mutex parked: the game's callback
                     * may Enqueue immediately, so do not hold it */
                    pthread_mutex_unlock(&p->lock);
                    if (cb) cb(caller, ctx);
                    pthread_mutex_lock(&p->lock);
                }
            } else if (wrote < 0) {
                p->alsaDead = 1;    /* died mid-write: silent from here on,
                                     * handle kept so objFree still closes it */
                p->dropPending = 0;
                p->pendFramesLeft = 0;
            }
        } else if (p->playState == SL_PLAYSTATE_PLAYING) {
            /* underrun feed: ALSA gets silence while the game is between
             * Enqueues so the device stays hot and wakeups stay cheap */
            pthread_mutex_unlock(&p->lock);
            long wrote = dev_write(p, p->zeroPcm, chunkFrames);
            pthread_mutex_lock(&p->lock);
            if (wrote > 0 && p->playState == SL_PLAYSTATE_PLAYING)
                p->framesDone += (uint64_t)wrote;
            if (wrote < 0) p->alsaDead = 1;
        }
    }
    /* destroy bookkeeping: whoever destroys joins-free; the worker (if the
     * dying mark was seen) or the destroying thread runs objFree exactly
     * once. objFree must run with no other thread touching `p`. */
    int dying = p->dying;
    pthread_mutex_unlock(&p->lock);
    if (dying) objFree(p);
    return NULL;
}

/* ── object interface ────────────────────────────────────────────────── */

static SLresult objectRealize(struct SLIface *self, SLboolean async) {
    (void)async;
    struct SLObject *o = ifaceObj(self);
    if (o->state != SL_OBJECT_STATE_UNREALIZED) return SL_RESULT_PRECONDITIONS_VIOLATED;
    o->state = SL_OBJECT_STATE_REALIZED;
    return SL_RESULT_SUCCESS;
}

static SLresult objectResume(struct SLIface *self, SLboolean async) {
    (void)async;
    struct SLObject *o = ifaceObj(self);
    if (o->state == SL_OBJECT_STATE_UNREALIZED) return SL_RESULT_PRECONDITIONS_VIOLATED;
    return SL_RESULT_SUCCESS; /* nothing suspends this shim's objects */
}

static SLresult objectGetState(struct SLIface *self, SLuint32 *pState) {
    struct SLObject *o = ifaceObj(self);
    if (!pState) return SL_RESULT_PARAMETER_INVALID;
    if (o->state == SL_OBJECT_STATE_UNREALIZED) return SL_RESULT_PRECONDITIONS_VIOLATED;
    *pState = o->state;
    return SL_RESULT_SUCCESS;
}

static SLresult objectRegisterCallback(struct SLIface *self, slObjectCallback callback,
                                       void *pContext) {
    (void)self; (void)callback; (void)pContext;
    return SL_RESULT_SUCCESS; /* async-realize events never fire here */
}

static void objectAbortAsync(struct SLIface *self) { (void)self; }

static SLresult objectSetPriority(struct SLIface *self, SLint32 priority, SLboolean preemptable) {
    (void)self; (void)priority; (void)preemptable;
    return SL_RESULT_SUCCESS;
}

static SLresult objectGetPriority(struct SLIface *self, SLint32 *pPriority,
                                  SLboolean *pPreemptable) {
    (void)self;
    if (pPriority)    *pPriority = 0;
    if (pPreemptable) *pPreemptable = SL_FALSE;
    return SL_RESULT_SUCCESS;
}

static SLresult objectSetLossOfControlInterfaces(struct SLIface *self, SLint16 n,
                                                 const SLInterfaceID *pInterfaceIDs,
                                                 SLboolean enabled) {
    (void)self; (void)n; (void)pInterfaceIDs; (void)enabled;
    return SL_RESULT_SUCCESS;
}

static void objectDestroy(struct SLIface *self) {
    struct SLObject *o = ifaceObj(self);
    if (o->kind == OBJ_KIND_PLAYER && o->workerStarted) {
        /* Hand the allocation to the worker and let it free: a game may
         * legally Destroy from inside the buffer-queue callback, and a
         * self-join would deadlock while the callback frame is live. */
        pthread_mutex_lock(&o->lock);
        o->dying = 1;
        o->quit  = 1;
        pthread_cond_broadcast(&o->cvWake);
        pthread_mutex_unlock(&o->lock);
        return;
    }
    objFree(o);
}

/* GetInterface compares the full 16-byte interface ID. */
static SLresult objectGetInterface(struct SLIface *self, SLInterfaceID iid, void **pItf) {
    struct SLObject *o = ifaceObj(self);
    if (!iid || !pItf) return SL_RESULT_PARAMETER_INVALID;
    if (o->state != SL_OBJECT_STATE_REALIZED) return SL_RESULT_PRECONDITIONS_VIOLATED;

    if (iidEq(iid, &kIidObject)) { *pItf = &o->ifObject; return SL_RESULT_SUCCESS; }
    if (o->kind == OBJ_KIND_ENGINE && iidEq(iid, &kIidEngine)) {
        *pItf = &o->ifEngine;
        return SL_RESULT_SUCCESS;
    }
    if (o->kind == OBJ_KIND_PLAYER) {
        if (iidEq(iid, &kIidPlay))  { *pItf = &o->ifPlay;  return SL_RESULT_SUCCESS; }
        if (iidEq(iid, &kIidBufferQueue) || iidEq(iid, &kIidAndroidSimpleBufferQueue)) {
            *pItf = &o->ifBq;
            return SL_RESULT_SUCCESS;
        }
    }
    if (o->kind == OBJ_KIND_MIX && iidEq(iid, &kIidOutputMix)) {
        *pItf = &o->ifMix;
        return SL_RESULT_SUCCESS;
    }
    if ((o->kind == OBJ_KIND_PLAYER || o->kind == OBJ_KIND_MIX) && iidEq(iid, &kIidVolume)) {
        *pItf = &o->ifVolume;
        return SL_RESULT_SUCCESS;
    }
    /* Every IID with a decoder or DSP behind it (equalizer, effects, 3D,
     * pitch, record, Android custom effects): unsupported, logged once per
     * distinct value so the launch log names the missing shim target. */
    if (iid && !(o->unsupportedLogged & 1u)) {
        o->unsupportedLogged |= 1u;
        SLogF("GetInterface: interface %08x-%04x-%04x not supported by this shim\n",
              (unsigned)iid->time_low, (unsigned)iid->time_mid,
              (unsigned)iid->time_hi_and_version);
    }
    return SL_RESULT_FEATURE_UNSUPPORTED;
}

/* ── play interface ──────────────────────────────────────────────────── */

static SLresult playSetPlayState(struct SLIface *self, SLuint32 state) {
    struct SLObject *p = ifaceObj(self);
    if (state != SL_PLAYSTATE_STOPPED && state != SL_PLAYSTATE_PAUSED &&
        state != SL_PLAYSTATE_PLAYING)
        return SL_RESULT_PARAMETER_INVALID;
    if (p->state != SL_OBJECT_STATE_REALIZED) return SL_RESULT_PRECONDITIONS_VIOLATED;

    pthread_mutex_lock(&p->lock);
    SLuint32 old = p->playState;
    if (state == SL_PLAYSTATE_PLAYING && !p->workerStarted) {
        if (pthread_create(&p->workerThread, NULL, playerWriterThread, p) != 0) {
            pthread_mutex_unlock(&p->lock);
            SLogF("pthread_create for the player thread failed\n");
            return SL_RESULT_RESOURCE_ERROR;
        }
        p->workerStarted = 1;
    }
    if (old == SL_PLAYSTATE_STOPPED && state != SL_PLAYSTATE_STOPPED)
        p->framesDone = 0;   /* stop → play/pause restarts from position 0 */
    p->playState = state;
    if (state != SL_PLAYSTATE_PLAYING) {
        /* drop queued content; the in-flight buffer discards async */
        for (size_t i = 0; i < QUEUE_CAP; i++) {
            free(p->queue[i].data);
            p->queue[i].data = NULL;
            p->queue[i].size = 0;
        }
        p->qHead = p->qTail = p->qCount = 0;
        if (p->pendFramesLeft > 0) p->dropPending = 1;
        pthread_cond_broadcast(&p->cvRoom);
    }
    pthread_cond_signal(&p->cvWake);
    pthread_mutex_unlock(&p->lock);
    if (old != state)
        SLogF("play state %s → %s\n", playStateName(old), playStateName(state));
    return SL_RESULT_SUCCESS;
}

static SLresult playGetPlayState(struct SLIface *self, SLuint32 *pState) {
    struct SLObject *p = ifaceObj(self);
    if (!pState) return SL_RESULT_PARAMETER_INVALID;
    if (p->state != SL_OBJECT_STATE_REALIZED) return SL_RESULT_PRECONDITIONS_VIOLATED;
    *pState = p->playState;
    return SL_RESULT_SUCCESS;
}

static SLresult playGetDuration(struct SLIface *self, SLmillisecond *pMsec) {
    (void)self;
    if (!pMsec) return SL_RESULT_PARAMETER_INVALID;
    *pMsec = SL_TIME_UNKNOWN; /* buffer-queue sources carry no duration */
    return SL_RESULT_SUCCESS;
}

static SLresult playGetPosition(struct SLIface *self, SLmillisecond *pMsec) {
    struct SLObject *p = ifaceObj(self);
    if (!pMsec) return SL_RESULT_PARAMETER_INVALID;
    uint64_t ms = (uint64_t)p->framesDone * 1000u / ((uint64_t)p->rateMilliHz / 1000u);
    *pMsec = (SLmillisecond)ms;
    return SL_RESULT_SUCCESS;
}

static SLresult playRegisterCallback(struct SLIface *self, slPlayCallback callback,
                                     void *pContext) {
    (void)self; (void)callback; (void)pContext;
    return SL_RESULT_SUCCESS; /* play-callback events are not generated yet */
}

static SLresult playSetCallbackEventsMask(struct SLIface *self, SLuint32 eventFlags) {
    (void)self; (void)eventFlags;
    return SL_RESULT_SUCCESS;
}

static SLresult playGetCallbackEventsMask(struct SLIface *self, SLuint32 *pEventFlags) {
    (void)self;
    if (pEventFlags) *pEventFlags = 0;
    return SL_RESULT_SUCCESS;
}

static SLresult playSetMarkerPosition(struct SLIface *self, SLmillisecond mSec) {
    (void)self; (void)mSec;
    return SL_RESULT_FEATURE_UNSUPPORTED;
}

static SLresult playClearMarkerPosition(struct SLIface *self) {
    (void)self;
    return SL_RESULT_SUCCESS;
}

static SLresult playGetMarkerPosition(struct SLIface *self, SLmillisecond *pMsec) {
    (void)self;
    if (pMsec) *pMsec = SL_TIME_UNKNOWN;
    return SL_RESULT_SUCCESS;
}

static SLresult playSetPositionUpdatePeriod(struct SLIface *self, SLmillisecond mSec) {
    (void)self; (void)mSec;
    return SL_RESULT_SUCCESS;
}

static SLresult playGetPositionUpdatePeriod(struct SLIface *self, SLmillisecond *pMsec) {
    (void)self;
    if (pMsec) *pMsec = 1000;
    return SL_RESULT_SUCCESS;
}

/* ── buffer queue interface ──────────────────────────────────────────── */

static SLresult bqEnqueue(struct SLIface *self, const void *pBuffer, SLuint32 size) {
    struct SLObject *p = ifaceObj(self);
    if (!pBuffer || size == 0) return SL_RESULT_PARAMETER_INVALID;
    if (p->state != SL_OBJECT_STATE_REALIZED) return SL_RESULT_PRECONDITIONS_VIOLATED;
    if ((size % (2u * p->channels)) != 0) return SL_RESULT_PARAMETER_INVALID;

    uint8_t *copy = malloc(size);
    if (!copy) return SL_RESULT_MEMORY_FAILURE;
    memcpy(copy, pBuffer, size);

    pthread_mutex_lock(&p->lock);
    if (p->qCount == QUEUE_CAP) {
        pthread_mutex_unlock(&p->lock);
        free(copy);
        return SL_RESULT_BUFFER_INSUFFICIENT;
    }
    struct SLBuf *slot = &p->queue[p->qTail];
    slot->data = copy;
    slot->size = size;
    p->qTail = (p->qTail + 1) % QUEUE_CAP;
    p->qCount++;
    pthread_cond_signal(&p->cvWake);
    pthread_mutex_unlock(&p->lock);
    return SL_RESULT_SUCCESS;
}

static SLresult bqClear(struct SLIface *self) {
    struct SLObject *p = ifaceObj(self);
    if (p->state != SL_OBJECT_STATE_REALIZED) return SL_RESULT_PRECONDITIONS_VIOLATED;
    pthread_mutex_lock(&p->lock);
    for (size_t i = 0; i < QUEUE_CAP; i++) {
        free(p->queue[i].data);
        p->queue[i].data = NULL;
        p->queue[i].size = 0;
    }
    p->qHead = p->qTail = p->qCount = 0;
    if (p->pendFramesLeft > 0) p->dropPending = 1;
    pthread_mutex_unlock(&p->lock);
    return SL_RESULT_SUCCESS;
}

static SLresult bqGetState(struct SLIface *self, struct SLBufferQueueState *pState) {
    struct SLObject *p = ifaceObj(self);
    if (!pState) return SL_RESULT_PARAMETER_INVALID;
    if (p->state != SL_OBJECT_STATE_REALIZED) return SL_RESULT_PRECONDITIONS_VIOLATED;
    pthread_mutex_lock(&p->lock);
    pState->count     = (SLuint32)p->qCount;
    pState->playIndex = p->bufsDone;
    pthread_mutex_unlock(&p->lock);
    return SL_RESULT_SUCCESS;
}

static SLresult bqRegisterCallback(struct SLIface *self, slBufferQueueCallback callback,
                                   void *pContext) {
    struct SLObject *p = ifaceObj(self);
    if (!callback) return SL_RESULT_PARAMETER_INVALID;
    if (p->state != SL_OBJECT_STATE_REALIZED) return SL_RESULT_PRECONDITIONS_VIOLATED;
    pthread_mutex_lock(&p->lock);
    p->bqCallback = callback;
    p->bqCtx      = pContext;
    pthread_mutex_unlock(&p->lock);
    return SL_RESULT_SUCCESS;
}

/* ── output mix / volume (passthrough stubs) ─────────────────────────── */

static SLresult mixGetDestOutDevs(struct SLIface *self, SLint32 *pNumDevices,
                                  SLuint32 *pDeviceIDs) {
    (void)self;
    if (!pNumDevices) return SL_RESULT_PARAMETER_INVALID;
    *pNumDevices = 0;
    (void)pDeviceIDs;
    return SL_RESULT_SUCCESS;
}

static SLresult mixRegisterDeviceChange(struct SLIface *self, void *callback, void *pContext) {
    (void)self; (void)callback; (void)pContext;
    return SL_RESULT_SUCCESS;
}

static SLresult mixReRoute(struct SLIface *self, SLint32 numOutputDevices,
                           SLuint32 *pOutputDeviceIDs) {
    (void)self; (void)numOutputDevices; (void)pOutputDeviceIDs;
    return SL_RESULT_SUCCESS; /* routing is ALSA's business, not the shim's */
}

static SLresult volSetVolumeLevel(struct SLIface *self, SLint16 level) {
    struct SLObject *o = ifaceObj(self);
    o->volumeMB = level;
    return SL_RESULT_SUCCESS;
}

static SLresult volGetVolumeLevel(struct SLIface *self, SLint16 *pLevel) {
    struct SLObject *o = ifaceObj(self);
    if (!pLevel) return SL_RESULT_PARAMETER_INVALID;
    *pLevel = o->volumeMB;
    return SL_RESULT_SUCCESS;
}

static SLresult volGetMaxVolumeLevel(struct SLIface *self, SLint16 *pMaxLevel) {
    (void)self;
    if (pMaxLevel) *pMaxLevel = 0;
    return SL_RESULT_SUCCESS;
}

static SLresult volSetMute(struct SLIface *self, SLboolean mute) {
    (void)self; (void)mute;
    return SL_RESULT_SUCCESS; /* mute is the launcher's business */
}

static SLresult volGetMute(struct SLIface *self, SLboolean *pMute) {
    (void)self;
    if (pMute) *pMute = SL_FALSE;
    return SL_RESULT_SUCCESS;
}

static SLresult volEnableStereoPosition(struct SLIface *self, SLboolean enable) {
    (void)self; (void)enable;
    return SL_RESULT_FEATURE_UNSUPPORTED;
}

static SLresult volIsEnabledStereoPosition(struct SLIface *self, SLboolean *pEnable) {
    (void)self;
    if (pEnable) *pEnable = SL_FALSE;
    return SL_RESULT_SUCCESS;
}

static SLresult volSetStereoPosition(struct SLIface *self, SLint16 stereoPosition) {
    (void)self; (void)stereoPosition;
    return SL_RESULT_FEATURE_UNSUPPORTED;
}

static SLresult volGetStereoPosition(struct SLIface *self, SLint16 *pStereoPosition) {
    (void)self;
    if (pStereoPosition) *pStereoPosition = 0;
    return SL_RESULT_SUCCESS;
}

/* ── vtable constants ────────────────────────────────────────────────── */

/* engine create_* methods are defined below the table that names them. */
static SLresult engineCreateLEDDevice(struct SLIface *self, void **pDevice, SLuint32 deviceID,
                                      SLuint32 n, const SLInterfaceID *iids, const SLboolean *req);
static SLresult engineCreateVibraDevice(struct SLIface *self, void **pDevice, SLuint32 deviceID,
                                        SLuint32 n, const SLInterfaceID *iids, const SLboolean *req);
static SLresult engineCreateAudioPlayer(struct SLIface *self, void **pPlayer,
                                        const struct SLDataSource *pAudioSrc,
                                        const struct SLDataSink *pAudioSnk,
                                        SLuint32 n, const SLInterfaceID *iids, const SLboolean *req);
static SLresult engineCreateAudioRecorder(struct SLIface *self, void **pRecorder,
                                          const struct SLDataSource *pAudioSrc,
                                          const struct SLDataSink *pAudioSnk,
                                          SLuint32 n, const SLInterfaceID *iids, const SLboolean *req);
static SLresult engineCreateMidiPlayer(struct SLIface *self, void **pPlayer,
                                       const struct SLDataSource *pMIDISrc,
                                       const struct SLDataSource *pBankSrc,
                                       const struct SLDataSink *pAudioOutput,
                                       const struct SLDataSink *pVibra,
                                       const struct SLDataSink *pLEDArray,
                                       SLuint32 n, const SLInterfaceID *iids, const SLboolean *req);
static SLresult engineCreateListener(struct SLIface *self, void **pListener, SLuint32 n,
                                     const SLInterfaceID *iids, const SLboolean *req);
static SLresult engineCreate3DGroup(struct SLIface *self, void **pGroup, SLuint32 n,
                                    const SLInterfaceID *iids, const SLboolean *req);
static SLresult engineCreateOutputMix(struct SLIface *self, void **pMix, SLuint32 n,
                                      const SLInterfaceID *iids, const SLboolean *req);
static SLresult engineCreateMetadataExtractor(struct SLIface *self, void **pObject,
                                              const struct SLDataSource *pSrc,
                                              SLuint32 n, const SLInterfaceID *iids,
                                              const SLboolean *req);
static SLresult engineCreateExtensionObject(struct SLIface *self, void **pObject, void *pParams,
                                            SLuint32 objectID, SLuint32 n,
                                            const SLInterfaceID *iids, const SLboolean *req);
static SLresult engineQueryNumSupportedInterfaces(struct SLIface *self, SLuint32 objectID,
                                                  SLuint32 *pNum);
static SLresult engineQuerySupportedInterfaces(struct SLIface *self, SLuint32 objectID,
                                               SLuint32 index, SLInterfaceID *pIid);
static SLresult engineQueryNumSupportedExtensions(struct SLIface *self, SLuint32 *pNum);
static SLresult engineQuerySupportedExtension(struct SLIface *self, SLuint32 index,
                                              const void *pExtensionName, SLint16 *pLength);
static SLresult engineIsExtensionSupported(struct SLIface *self, const void *pExtensionName,
                                           SLboolean *pSupported);

static const struct SLObjectItfVT g_objectVt = {
    .Realize                    = objectRealize,
    .Resume                     = objectResume,
    .GetState                   = objectGetState,
    .GetInterface               = objectGetInterface,
    .RegisterCallback           = objectRegisterCallback,
    .AbortAsyncOperation        = objectAbortAsync,
    .Destroy                    = objectDestroy,
    .SetPriority                = objectSetPriority,
    .GetPriority                = objectGetPriority,
    .SetLossOfControlInterfaces = objectSetLossOfControlInterfaces,
};

static const struct SLEngineItfVT g_engineVt = {
    .CreateLEDDevice             = engineCreateLEDDevice,
    .CreateVibraDevice           = engineCreateVibraDevice,
    .CreateAudioPlayer           = engineCreateAudioPlayer,
    .CreateAudioRecorder         = engineCreateAudioRecorder,
    .CreateMidiPlayer            = engineCreateMidiPlayer,
    .CreateListener              = engineCreateListener,
    .Create3DGroup               = engineCreate3DGroup,
    .CreateOutputMix             = engineCreateOutputMix,
    .CreateMetadataExtractor     = engineCreateMetadataExtractor,
    .CreateExtensionObject       = engineCreateExtensionObject,
    .QueryNumSupportedInterfaces = engineQueryNumSupportedInterfaces,
    .QuerySupportedInterfaces    = engineQuerySupportedInterfaces,
    .QueryNumSupportedExtensions = engineQueryNumSupportedExtensions,
    .QuerySupportedExtension     = engineQuerySupportedExtension,
    .IsExtensionSupported        = engineIsExtensionSupported,
};

static const struct SLPlayItfVT g_playVt = {
    .SetPlayState            = playSetPlayState,
    .GetPlayState            = playGetPlayState,
    .GetDuration             = playGetDuration,
    .GetPosition             = playGetPosition,
    .RegisterCallback        = playRegisterCallback,
    .SetCallbackEventsMask   = playSetCallbackEventsMask,
    .GetCallbackEventsMask   = playGetCallbackEventsMask,
    .SetMarkerPosition       = playSetMarkerPosition,
    .ClearMarkerPosition     = playClearMarkerPosition,
    .GetMarkerPosition       = playGetMarkerPosition,
    .SetPositionUpdatePeriod = playSetPositionUpdatePeriod,
    .GetPositionUpdatePeriod = playGetPositionUpdatePeriod,
};

static const struct SLBufferQueueItfVT g_bqVt = {
    .Enqueue          = bqEnqueue,
    .Clear            = bqClear,
    .GetState         = bqGetState,
    .RegisterCallback = bqRegisterCallback,
};

static const struct SLOutputMixItfVT g_outputMixVt = {
    .GetDestinationOutputDeviceIDs = mixGetDestOutDevs,
    .RegisterDeviceChangeCallback  = mixRegisterDeviceChange,
    .ReRoute                       = mixReRoute,
};

static const struct SLVolumeItfVT g_volumeVt = {
    .SetVolumeLevel          = volSetVolumeLevel,
    .GetVolumeLevel          = volGetVolumeLevel,
    .GetMaxVolumeLevel       = volGetMaxVolumeLevel,
    .SetMute                 = volSetMute,
    .GetMute                 = volGetMute,
    .EnableStereoPosition    = volEnableStereoPosition,
    .IsEnabledStereoPosition = volIsEnabledStereoPosition,
    .SetStereoPosition       = volSetStereoPosition,
    .GetStereoPosition       = volGetStereoPosition,
};

/* ── engine CreateXxx: only player + mix are real, everything that needs a
 *    decoder logs its name so the next shim target is readable ─────────── */

static SLresult engineCreateLEDDevice(struct SLIface *self, void **pDevice, SLuint32 deviceID,
                                      SLuint32 n, const SLInterfaceID *iids,
                                      const SLboolean *req) {
    (void)self; (void)pDevice; (void)deviceID; (void)n; (void)iids; (void)req;
    SLogF("CreateLEDDevice: LED arrays are not part of this shim\n");
    return SL_RESULT_FEATURE_UNSUPPORTED;
}

static SLresult engineCreateVibraDevice(struct SLIface *self, void **pDevice, SLuint32 deviceID,
                                        SLuint32 n, const SLInterfaceID *iids,
                                        const SLboolean *req) {
    (void)self; (void)pDevice; (void)deviceID; (void)n; (void)iids; (void)req;
    SLogF("CreateVibraDevice: vibra is not part of this shim\n");
    return SL_RESULT_FEATURE_UNSUPPORTED;
}

/* Validates the game's source/sink, opens the ALSA "default" device for
 * this player, and returns the object unrealized (the game Realizes next). */
static SLresult engineCreateAudioPlayer(struct SLIface *self, void **pPlayer,
                                        const struct SLDataSource *pAudioSrc,
                                        const struct SLDataSink *pAudioSnk,
                                        SLuint32 n, const SLInterfaceID *iids,
                                        const SLboolean *req) {
    (void)n; (void)iids; (void)req;
    struct SLObject *engine = ifaceObj(self);
    if (!pPlayer || !pAudioSrc) return SL_RESULT_PARAMETER_INVALID;
    if (engine->state != SL_OBJECT_STATE_REALIZED) return SL_RESULT_PRECONDITIONS_VIOLATED;

    const struct SLDataLocator_BufferQueue *loc =
        (const struct SLDataLocator_BufferQueue *)pAudioSrc->pLocator;
    const struct SLDataFormat_PCM *fmt = (const struct SLDataFormat_PCM *)pAudioSrc->pFormat;
    if (!loc || loc->locatorType != SL_DATALOCATOR_BUFFERQUEUE) {
        SLogF("CreateAudioPlayer: source must be SL_DATALOCATOR_BUFFERQUEUE\n");
        return SL_RESULT_CONTENT_UNSUPPORTED;
    }
    if (!fmt || fmt->formatType != SL_DATAFORMAT_PCM ||
        fmt->bitsPerSample != 16 || fmt->containerSize != 16) {
        SLogF("CreateAudioPlayer: need 16/16-bit PCM, got formatType %u bits %u container %u\n",
              fmt ? (unsigned)fmt->formatType : 0u,
              fmt ? (unsigned)fmt->bitsPerSample : 0u,
              fmt ? (unsigned)fmt->containerSize : 0u);
        return SL_RESULT_CONTENT_UNSUPPORTED;
    }
    if (fmt->numChannels != 1 && fmt->numChannels != 2) {
        SLogF("CreateAudioPlayer: %u-channel streams are not wired (mono/stereo only)\n",
              (unsigned)fmt->numChannels);
        return SL_RESULT_CONTENT_UNSUPPORTED;
    }
    if (fmt->channelMask != 0 && fmt->channelMask != (SL_SPEAKER_FRONT_LEFT | SL_SPEAKER_FRONT_RIGHT) &&
        fmt->channelMask != SL_SPEAKER_FRONT_LEFT && fmt->channelMask != SL_SPEAKER_FRONT_CENTER) {
        SLogF("CreateAudioPlayer: channel mask 0x%x unsupported (uses L/R/LFE/…)\n",
              (unsigned)fmt->channelMask);
        return SL_RESULT_CONTENT_UNSUPPORTED;
    }
    if (fmt->samplesPerSec < MIN_RATE_MILLIHZ || fmt->samplesPerSec > MAX_RATE_MILLIHZ) {
        SLogF("CreateAudioPlayer: sample rate %u milliHz out of range\n",
              (unsigned)fmt->samplesPerSec);
        return SL_RESULT_CONTENT_UNSUPPORTED;
    }
    if (fmt->endianness != 0 && fmt->endianness != SL_BYTEORDER_LITTLEENDIAN) {
        SLogF("CreateAudioPlayer: big-endian PCM is not native on this board\n");
        return SL_RESULT_CONTENT_UNSUPPORTED;
    }
    const struct SLDataLocator_OutputMix *sink =
        (const struct SLDataLocator_OutputMix *)(pAudioSnk ? pAudioSnk->pLocator : NULL);
    if (sink && sink->locatorType != 0 && sink->locatorType != SL_DATALOCATOR_OUTPUTMIX) {
        SLogF("CreateAudioPlayer: sink must be an output mix (or NULL)\n");
        return SL_RESULT_CONTENT_UNSUPPORTED;
    }

    struct SLObject *p = objNew(OBJ_KIND_PLAYER);
    if (!p) return SL_RESULT_MEMORY_FAILURE;
    p->rateMilliHz = fmt->samplesPerSec;
    p->channels    = fmt->numChannels;
    p->playState   = SL_PLAYSTATE_STOPPED;
    p->volumeMB    = 0; /* full volume */
    p->zeroPcm     = calloc(UNDERRUN_FRAMES * 2u * p->channels, 1);
    if (!p->zeroPcm) {
        objFree(p);
        return SL_RESULT_MEMORY_FAILURE;
    }

    /* the ABI hands back the SLObjectItf interface handle */

    alsa_init();
    if (g_alsa.lib) {
        void *pcm = NULL;
        int rc = g_alsa.pcmOpen(&pcm, "default", ALSA_STREAM_PLAYBACK, 0, NULL);
        if (rc == 0 && pcm) {
            rc = g_alsa.pcmSetParams(pcm, ALSA_PCM_FORMAT_S16_LE, ALSA_RW_INTERLEAVED,
                                     (unsigned)fmt->numChannels,
                                     (unsigned)(fmt->samplesPerSec / 1000u), 1, ALSA_LATENCY_US);
            if (rc == 0) {
                p->alsaPcm = pcm;
                SLogF("ALSA \"default\" open: %u Hz, %u ch, S16LE\n",
                      (unsigned)(fmt->samplesPerSec / 1000u), (unsigned)fmt->numChannels);
            } else {
                SLogF("snd_pcm_set_params failed (%s) — silent pacing for this player\n",
                      g_alsa.pcmStrError(pcm, rc));
                g_alsa.pcmClose(pcm);
            }
        } else {
            SLogF("snd_pcm_open(\"default\") failed (%s) — silent pacing for this player\n",
                  g_alsa.pcmStrError ? g_alsa.pcmStrError(NULL, rc) : "?");
        }
    }

    /* the ABI hands back the SLObjectItf interface handle */
    *pPlayer = &p->ifObject;
    return SL_RESULT_SUCCESS;
}

static SLresult engineCreateAudioRecorder(struct SLIface *self, void **pRecorder,
                                          const struct SLDataSource *pAudioSrc,
                                          const struct SLDataSink *pAudioSnk,
                                          SLuint32 n, const SLInterfaceID *iids,
                                          const SLboolean *req) {
    (void)self; (void)pRecorder; (void)pAudioSrc; (void)pAudioSnk; (void)n; (void)iids; (void)req;
    SLogF("CreateAudioRecorder: recording is not part of this shim\n");
    return SL_RESULT_FEATURE_UNSUPPORTED;
}

static SLresult engineCreateMidiPlayer(struct SLIface *self, void **pPlayer,
                                       const struct SLDataSource *pMIDISrc,
                                       const struct SLDataSource *pBankSrc,
                                       const struct SLDataSink *pAudioOutput,
                                       const struct SLDataSink *pVibra,
                                       const struct SLDataSink *pLEDArray,
                                       SLuint32 n, const SLInterfaceID *iids,
                                       const SLboolean *req) {
    (void)self; (void)pPlayer; (void)pMIDISrc; (void)pBankSrc; (void)pAudioOutput;
    (void)pVibra; (void)pLEDArray; (void)n; (void)iids; (void)req;
    SLogF("CreateMidiPlayer: MIDI is not part of this shim\n");
    return SL_RESULT_FEATURE_UNSUPPORTED;
}

static SLresult engineCreateListener(struct SLIface *self, void **pListener, SLuint32 n,
                                     const SLInterfaceID *iids, const SLboolean *req) {
    (void)self; (void)pListener; (void)n; (void)iids; (void)req;
    SLogF("CreateListener: 3D audio is not part of this shim\n");
    return SL_RESULT_FEATURE_UNSUPPORTED;
}

static SLresult engineCreate3DGroup(struct SLIface *self, void **pGroup, SLuint32 n,
                                    const SLInterfaceID *iids, const SLboolean *req) {
    (void)self; (void)pGroup; (void)n; (void)iids; (void)req;
    SLogF("Create3DGroup: 3D audio is not part of this shim\n");
    return SL_RESULT_FEATURE_UNSUPPORTED;
}

static SLresult engineCreateOutputMix(struct SLIface *self, void **pMix, SLuint32 n,
                                      const SLInterfaceID *iids, const SLboolean *req) {
    (void)n; (void)iids; (void)req;
    struct SLObject *engine = ifaceObj(self);
    if (!pMix) return SL_RESULT_PARAMETER_INVALID;
    if (engine->state != SL_OBJECT_STATE_REALIZED) return SL_RESULT_PRECONDITIONS_VIOLATED;
    struct SLObject *m = objNew(OBJ_KIND_MIX);
    if (!m) return SL_RESULT_MEMORY_FAILURE;
    /* the ABI hands back the SLObjectItf interface handle */
    *pMix = &m->ifObject;
    return SL_RESULT_SUCCESS;
}

static SLresult engineCreateMetadataExtractor(struct SLIface *self, void **pObject,
                                              const struct SLDataSource *pSrc,
                                              SLuint32 n, const SLInterfaceID *iids,
                                              const SLboolean *req) {
    (void)self; (void)pObject; (void)pSrc; (void)n; (void)iids; (void)req;
    SLogF("CreateMetadataExtractor: not part of this shim\n");
    return SL_RESULT_FEATURE_UNSUPPORTED;
}

static SLresult engineCreateExtensionObject(struct SLIface *self, void **pObject, void *pParams,
                                            SLuint32 objectID, SLuint32 n,
                                            const SLInterfaceID *iids, const SLboolean *req) {
    (void)self; (void)pObject; (void)pParams; (void)objectID; (void)n; (void)iids; (void)req;
    SLogF("CreateExtensionObject: not part of this shim\n");
    return SL_RESULT_FEATURE_UNSUPPORTED;
}

static SLresult engineQueryNumSupportedInterfaces(struct SLIface *self, SLuint32 objectID,
                                                  SLuint32 *pNum) {
    (void)self; (void)objectID;
    if (!pNum) return SL_RESULT_PARAMETER_INVALID;
    *pNum = 0;
    return SL_RESULT_SUCCESS;
}

static SLresult engineQuerySupportedInterfaces(struct SLIface *self, SLuint32 objectID,
                                               SLuint32 index, SLInterfaceID *pIid) {
    (void)self; (void)objectID; (void)index; (void)pIid;
    return SL_RESULT_PARAMETER_INVALID; /* 0 listed above → any index is out of range */
}

static SLresult engineQueryNumSupportedExtensions(struct SLIface *self, SLuint32 *pNum) {
    (void)self;
    if (!pNum) return SL_RESULT_PARAMETER_INVALID;
    *pNum = 0;
    return SL_RESULT_SUCCESS;
}

static SLresult engineQuerySupportedExtension(struct SLIface *self, SLuint32 index,
                                              const void *pExtensionName, SLint16 *pLength) {
    (void)self; (void)index; (void)pExtensionName; (void)pLength;
    return SL_RESULT_PARAMETER_INVALID;
}

static SLresult engineIsExtensionSupported(struct SLIface *self, const void *pExtensionName,
                                           SLboolean *pSupported) {
    (void)self; (void)pExtensionName;
    if (pSupported) *pSupported = SL_FALSE;
    return SL_RESULT_SUCCESS;
}

/* ── entry points ────────────────────────────────────────────────────── */

/* slCreateEngine(SLObjectItf*, numOptions, pEngineOptions, numInterfaces, *,
 * pInterfaceRequired) — engine options (SL_ENGINEOPTION_THREADSAFE and
 * friends) are accepted and ignored: this shim is always thread-safe and
 * never drops control of anything. */
ANDROIDRT_EXPORT SLresult slCreateEngine(void **engineObjectOut, uint32_t numOptions,
                                         const void *engineOptions, uint32_t numInterfaces,
                                         const SLInterfaceID *interfaceIds,
                                         const SLboolean *interfaceRequired) {
    (void)numOptions; (void)engineOptions; (void)numInterfaces;
    (void)interfaceIds; (void)interfaceRequired;
    if (!engineObjectOut) return SL_RESULT_PARAMETER_INVALID;
    struct SLObject *o = objNew(OBJ_KIND_ENGINE);
    if (!o) return SL_RESULT_MEMORY_FAILURE;
    SLogF("engine created\n");
    /* the ABI hands back the SLObjectItf interface handle, not the object */
    *engineObjectOut = &o->ifObject;
    return SL_RESULT_SUCCESS;
}
