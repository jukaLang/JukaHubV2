/* fake_audio.c — the smoke-test probe for AndroidRT's OpenSL ES shim.
 *
 * It drives the ABI exactly the way an NDK game does, no ALSA required:
 * on a host without libasound.so.2 the shim's worker "plays" against a
 * wall-clock pacer, which keeps callback order and GetPosition truthful —
 * so every assertion here holds both with a device and in silent mode:
 *
 *   slCreateEngine → Realize → GetInterface(SL_IID_ENGINE)
 *   GetInterface refuses before Realize (PRECONDITIONS_VIOLATED)
 *   unknown interfaces come back FEATURE_UNSUPPORTED (UUID equality)
 *   CreateOutputMix → Realize; CreateAudioPlayer (BufferQueue → OutputMix)
 *   SLVolumeItf Set/Get round-trips the millibel level
 *   buffer-queue callback fires once per completed Enqueue, with the
 *     buffer-queue interface as the callback's caller handle
 *   GetPosition advances with frames consumed and keeps pacing through
 *     underrun (silence stays truthful)
 *   SetPlayState(STOPPED) drops queued content mid-flight, pins playIndex
 *     and never fires a late callback for a dropped buffer
 *   Destroy from inside the buffer-queue callback survives (no deadlock,
 *     no use-after-free) and ends the callback stream where it stood
 *
 * PASS means every assertion held; any mismatch prints and fails the run.
 */

#include <stdio.h>
#include <stdint.h>
#include <string.h>
#include <time.h>

#include "actbridge.h" /* ANDROIDRT_EXPORT exposes the shim's sl* entry */

/* ── a minimal OpenSL ES client surface, embedded like a game would ── */
typedef uint32_t SLuint32;
typedef int32_t  SLint32;
typedef int16_t  SLint16;
typedef SLuint32 SLboolean;
typedef SLuint32 SLresult;
typedef SLuint32 SLmillisecond;

#define SL_RESULT_PRECONDITIONS_VIOLATED 0x00000001
#define SL_RESULT_FEATURE_UNSUPPORTED    0x0000000C
#define SL_RESULT_SUCCESS                0x00000000

#define SL_FALSE 0u
#define SL_TRUE  1u

#define SL_PLAYSTATE_PLAYING 3
#define SL_PLAYSTATE_STOPPED 1

#define SL_DATALOCATOR_BUFFERQUEUE 0x00000006u
#define SL_DATALOCATOR_OUTPUTMIX   0x00000004u
#define SL_DATAFORMAT_PCM          0x00000002u
#define SL_BYTEORDER_LITTLEENDIAN  0x00000002u
#define SL_SPEAKER_FRONT_LEFT      0x00000001u
#define SL_SPEAKER_FRONT_RIGHT     0x00000002u

struct SLInterfaceID_ {
    uint32_t time_low;
    uint16_t time_mid;
    uint16_t time_hi_and_version;
    uint16_t clock_seq;
    uint8_t  node[6];
};
typedef const struct SLInterfaceID_ *SLInterfaceID;

/* Bytes quoted from wilhelm libopensles/OpenSLES_IID.c — identical to the
 * shim's kIid* constants, so full 16-byte comparison routes correctly. */
static const struct SLInterfaceID_ SL_IID_ENGINE = {
    0x8d97c260, 0xddd4, 0x11db, 0x958f, { 0x00, 0x02, 0xa5, 0xd5, 0xc5, 0x1b } };
static const struct SLInterfaceID_ SL_IID_OUTPUTMIX = {
    0x97750f60, 0xddd7, 0x11db, 0x92b1, { 0x00, 0x02, 0xa5, 0xd5, 0xc5, 0x1b } };
static const struct SLInterfaceID_ SL_IID_PLAY = {
    0xef0bd9c0, 0xddd7, 0x11db, 0xbf49, { 0x00, 0x02, 0xa5, 0xd5, 0xc5, 0x1b } };
static const struct SLInterfaceID_ SL_IID_BUFFERQUEUE = {
    0x2bc99cc0, 0xddd4, 0x11db, 0x8d99, { 0x00, 0x02, 0xa5, 0xd5, 0xc5, 0x1b } };
static const struct SLInterfaceID_ SL_IID_VOLUME = {
    0x09e8ede0, 0xddde, 0x11db, 0xb4f6, { 0x00, 0x02, 0xa5, 0xd5, 0xc5, 0x1b } };
static const struct SLInterfaceID_ SL_IID_NOTHING = { /* unknown */
    0xdeadbeef, 0xbeef, 0xdead, 0xbeef, { 0x01, 0x02, 0x03, 0x04, 0x05, 0x06 } };

typedef void *SLObjectItf; /* handle: *handle is the object vtable */

struct SLDataSource_; struct SLDataSink_; struct SLBufferQueueState_;

/* vtable slices the probe dereferences — the rest of each table is unused */
/* Member order verbatim from SLES/OpenSLES.h — omitting members is how
 * games write minimal subsets too, but the slots must stay in place. */
struct SLObjectVt {
    SLresult (*Realize)(void *self, SLboolean async);
    SLresult (*Resume)(void *self, SLboolean async);       /* slot 1 */
    SLresult (*GetState)(void *self, SLuint32 *pState);    /* slot 2 */
    SLresult (*GetInterface)(void *self, SLInterfaceID iid, void **pItf);
    SLresult (*RegisterCallback)(void *self, void *cb, void *ctx);
    void     (*AbortAsyncOperation)(void *self);
    void     (*Destroy)(void *self);                       /* slot 6 */
};
struct SLEngineVt {
    SLresult (*pad0)(void); SLresult (*pad1)(void);
    SLresult (*CreateAudioPlayer)(void *self, SLObjectItf *pPlayer,
                                  const struct SLDataSource_ *pSrc,
                                  const struct SLDataSink_ *pSnk,
                                  SLuint32 numIfs, const SLInterfaceID *iids,
                                  const SLboolean *required);   /* slot 2 */
    SLresult (*pad3)(void); SLresult (*pad4)(void);
    SLresult (*pad5)(void); SLresult (*pad6)(void);
    SLresult (*CreateOutputMix)(void *self, SLObjectItf *pMix,
                                SLuint32 numIfs, const SLInterfaceID *iids,
                                const SLboolean *required);     /* slot 7 */
};
struct SLPlayVt {
    SLresult (*SetPlayState)(void *self, SLuint32 state);
    SLresult (*GetPlayState)(void *self, SLuint32 *pState);
    SLresult (*pad0)(void);                                  /* Duration */
    SLresult (*GetPosition)(void *self, SLmillisecond *pMsec); /* slot 3 */
};
struct SLBufferQueueVt {
    SLresult (*Enqueue)(void *self, const void *pBuffer, SLuint32 size);
    SLresult (*Clear)(void *self);
    SLresult (*GetState)(void *self, struct SLBufferQueueState_ *pState);
    SLresult (*RegisterCallback)(void *self,
                                 void (*callback)(void *caller, void *pContext),
                                 void *pContext);
};
struct SLVolumeVt {
    SLresult (*SetVolumeLevel)(void *self, SLint16 level);
    SLresult (*GetVolumeLevel)(void *self, SLint16 *pLevel);
};

struct SLDataSource_ { void *pLocator; void *pFormat; };
struct SLDataSink_   { void *pLocator; void *pFormat; };
struct SLDataLocator_BufferQueue { SLuint32 locatorType; SLuint32 numBuffers; };
struct SLDataLocator_OutputMix   { SLuint32 locatorType; void *outputMix; };
struct SLDataFormat_PCM {
    SLuint32 formatType; SLuint32 numChannels; SLuint32 samplesPerSec;
    SLuint32 bitsPerSample; SLuint32 containerSize; SLuint32 channelMask;
    SLuint32 endianness;
};
struct SLBufferQueueState_ { SLuint32 count; SLuint32 playIndex; };

extern ANDROIDRT_EXPORT SLresult
slCreateEngine(void **engineObjectOut, uint32_t numOptions, const void *engineOptions,
               uint32_t numInterfaces, const SLInterfaceID *interfaceIds,
               const SLboolean *interfaceRequired);

/* ── probe plumbing ─────────────────────────────────────────────────── */

static int g_failures;

static void check(int ok, const char *what) {
    if (ok) fprintf(stderr, "[audio-probe] ok: %s\n", what);
    else { fprintf(stderr, "[audio-probe] FAIL: %s\n", what); g_failures++; }
}

#define CHECK(cond, what) check(!!(cond), (what))

static SLresult objRealize(SLObjectItf o) {
    return (*(struct SLObjectVt **)o)->Realize(o, SL_FALSE);
}
static void objDestroy(SLObjectItf o) {
    (*(struct SLObjectVt **)o)->Destroy(o);
}
static SLresult objGetIf(SLObjectItf o, SLInterfaceID iid, void **out) {
    return (*(struct SLObjectVt **)o)->GetInterface(o, iid, out);
}

static double nowMs(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return ts.tv_sec * 1000.0 + ts.tv_nsec / 1e6;
}

static void zsleep(int ms) {
    struct timespec ts = { 0, (long)ms * 1000 * 1000 };
    nanosleep(&ts, NULL);
}

/* ── stage 2 counters (registered on the real player) ───────────────── */
static int    g_cbCount;
static void  *g_cbLastSelf;
static int    g_cbMaxCount = 2; /* how many callbacks stage 2 waits for */

static void bqCallback(void *caller, void *ctx) {
    (void)ctx;
    g_cbLastSelf = caller;
    g_cbCount++;
}

/* ── stage 3 (destroy-from-callback) state ──────────────────────────── */
struct DestroyStage {
    SLObjectItf              player;    /* the object to Destroy */
    struct SLBufferQueueVt  *bqVt;      /* refill from inside the callback */
    void                    *bqItf;     /* the buffer-queue *handle* to call */
    int                      count;     /* callbacks seen (counts to 3)     */
    uint32_t                 frameBytes;
    int                      armedAt;   /* which callback fires Destroy     */
};
static struct DestroyStage g_ds;

static void dsCallback(void *caller, void *ctx) {
    struct DestroyStage *ds = ctx;
    (void)caller;
    ds->count++;
    if (ds->count < ds->armedAt) {
        /* refill: a game keeps the queue fed from inside the callback */
        static uint8_t refill[8000];
        ds->bqVt->Enqueue(ds->bqItf, refill, ds->frameBytes);
        return;
    }
    objDestroy(ds->player); /* the landmine: Destroy inside the callback */
}

#define PCM_FRAMES 9600   /* 200 ms at 48 kHz stereo */
#define PCM_BYTES  (PCM_FRAMES * 4)
#define PCM_FRAMES2 400   /* 50 ms at 8 kHz mono     */
#define PCM_BYTES2 (PCM_FRAMES2 * 2)

int main(void) {
    setvbuf(stderr, NULL, _IONBF, 0); /* crash diagnostics: print as they open */
    fprintf(stderr, "[audio-probe] start\n");

    /* ── stage 1: engine up, wrong routes refused ────────────────────── */
    SLObjectItf engine = NULL;
    SLresult rc = slCreateEngine((void **)&engine, 0, NULL, 0, NULL, NULL);
    CHECK(rc == SL_RESULT_SUCCESS && engine != NULL, "slCreateEngine returns a handle");

    void *early = (void *)0x1;
    rc = objGetIf(engine, &SL_IID_ENGINE, &early);
    CHECK(rc == SL_RESULT_PRECONDITIONS_VIOLATED && early == (void *)0x1,
          "GetInterface before Realize: PRECONDITIONS_VIOLATED, output untouched");

    CHECK(objRealize(engine) == SL_RESULT_SUCCESS, "engine Realizes");

    struct SLEngineVt *engVt;
    void *eitf = NULL;
    CHECK(objGetIf(engine, &SL_IID_ENGINE, &eitf) == SL_RESULT_SUCCESS && eitf != NULL,
          "engine routes SL_IID_ENGINE");
    engVt = *(struct SLEngineVt **)eitf; /* *handle is the vtable */

    void *unknownIf = (void *)0x2;
    rc = objGetIf(engine, &SL_IID_NOTHING, &unknownIf);
    CHECK(rc == SL_RESULT_FEATURE_UNSUPPORTED && unknownIf == (void *)0x2,
          "unknown IID: FEATURE_UNSUPPORTED, output untouched");

    /* ── stage 2: output mix and audio player ────────────────────────── */
    SLObjectItf mix = NULL;
    rc = engVt->CreateOutputMix(eitf, &mix, 0, NULL, NULL);
    CHECK(rc == SL_RESULT_SUCCESS && mix != NULL, "CreateOutputMix succeeds");
    CHECK(objRealize(mix) == SL_RESULT_SUCCESS, "mix Realizes");
    void *mixItf = NULL;
    CHECK(objGetIf(mix, &SL_IID_OUTPUTMIX, &mixItf) == SL_RESULT_SUCCESS && mixItf != NULL,
          "mix routes SL_IID_OUTPUTMIX");

    struct SLDataLocator_BufferQueue srcLoc = { SL_DATALOCATOR_BUFFERQUEUE, 4 };
    struct SLDataFormat_PCM fmt = {
        SL_DATAFORMAT_PCM, 2 /*stereo*/, 48000 * 1000 /*milliHz*/,
        16, 16, SL_SPEAKER_FRONT_LEFT | SL_SPEAKER_FRONT_RIGHT,
        SL_BYTEORDER_LITTLEENDIAN };
    struct SLDataLocator_OutputMix snkLoc = { SL_DATALOCATOR_OUTPUTMIX, mix };
    struct SLDataSource_ src = { &srcLoc, &fmt };
    struct SLDataSink_   snk = { &snkLoc, NULL };

    static const SLInterfaceID playerIfs[] = { &SL_IID_PLAY, &SL_IID_BUFFERQUEUE, &SL_IID_VOLUME };
    static const SLboolean      wanted[]    = { SL_TRUE, SL_TRUE, SL_TRUE };
    SLObjectItf player = NULL;
    rc = engVt->CreateAudioPlayer(eitf, &player, &src, &snk, 3, playerIfs, wanted);
    CHECK(rc == SL_RESULT_SUCCESS && player != NULL, "CreateAudioPlayer (48k stereo) succeeds");
    CHECK(objRealize(player) == SL_RESULT_SUCCESS, "player Realizes");

    void *playItf = NULL, *bqItf = NULL, *volItf = NULL;
    CHECK(objGetIf(player, &SL_IID_PLAY, &playItf) == SL_RESULT_SUCCESS,
          "player routes SL_IID_PLAY");
    CHECK(objGetIf(player, &SL_IID_BUFFERQUEUE, &bqItf) == SL_RESULT_SUCCESS,
          "player routes SL_IID_BUFFERQUEUE");
    CHECK(objGetIf(player, &SL_IID_VOLUME, &volItf) == SL_RESULT_SUCCESS,
          "player routes SL_IID_VOLUME");
    struct SLPlayVt         *playVt = *(struct SLPlayVt **)playItf;
    struct SLBufferQueueVt  *bqVt   = *(struct SLBufferQueueVt **)bqItf;
    struct SLVolumeVt       *volVt  = *(struct SLVolumeVt **)volItf;

    /* volume round-trip through the shim's stored millibel */
    CHECK(volVt->SetVolumeLevel(volItf, -600) == SL_RESULT_SUCCESS, "SetVolumeLevel accepted");
    SLint16 lvl = 1;
    CHECK(volVt->GetVolumeLevel(volItf, &lvl) == SL_RESULT_SUCCESS && lvl == -600,
          "GetVolumeLevel echoes the stored millibel");

    /* ── stage 2 continued: enqueue → play → callback stream ─────────── */
    static uint8_t pcmBuf[PCM_BYTES];
    memset(pcmBuf, 0x7f, sizeof pcmBuf); /* not silence — would pass even if
                                          * the shim never touched the data  */

    CHECK(bqVt->RegisterCallback(bqItf, bqCallback, NULL) == SL_RESULT_SUCCESS,
          "RegisterCallback accepted on the realized player");
    struct SLBufferQueueState_ st;
    CHECK(bqVt->Enqueue(bqItf, pcmBuf, sizeof pcmBuf) == SL_RESULT_SUCCESS, "Enqueue #1");
    CHECK(bqVt->Enqueue(bqItf, pcmBuf, sizeof pcmBuf) == SL_RESULT_SUCCESS, "Enqueue #2");
    CHECK(bqVt->GetState(bqItf, &st) == SL_RESULT_SUCCESS && st.count == 2 && st.playIndex == 0,
          "GetState: two queued, none played");

    CHECK(playVt->SetPlayState(playItf, SL_PLAYSTATE_PLAYING) == SL_RESULT_SUCCESS,
          "SetPlayState(PLAYING)");

    double t0 = nowMs();
    int timedOut = 1;
    while (nowMs() - t0 < 5000.0) {
        if (g_cbCount >= g_cbMaxCount) { timedOut = 0; break; }
        zsleep(10);
    }
    CHECK(!timedOut, "both buffers completed and fired the registered callback");
    CHECK(g_cbLastSelf == bqItf, "callback's caller handle is the buffer-queue interface");
    CHECK(bqVt->GetState(bqItf, &st) == SL_RESULT_SUCCESS && st.count == 0 && st.playIndex == 2,
          "GetState: queue drained, playIndex 2");

    SLuint32 pstate = 0;
    CHECK(playVt->GetPlayState(playItf, &pstate) == SL_RESULT_SUCCESS &&
          pstate == SL_PLAYSTATE_PLAYING,
          "GetPlayState still reports PLAYING after the drain");

    SLmillisecond pos = 0;
    CHECK(playVt->GetPosition(playItf, &pos) == SL_RESULT_SUCCESS && pos >= 400,
          "GetPosition advanced with the consumed frames (>=400 ms)");
    fprintf(stderr, "[audio-probe]    (position = %u ms, wall = %.0f ms)\n",
            (unsigned)pos, nowMs() - t0);

    /* underrun pacing keeps the clock flowing between Enqueues. The gap
     * must cover Windows' coarse sleep granularity: at 48 kHz a 256-frame
     * chunk is 5.3 ms of sleep but lands on ~15 ms timers there, so give
     * the underrun 400 ms of headroom. */
    playVt->GetPosition(playItf, &pos);
    double p0 = pos;
    zsleep(400);
    playVt->GetPosition(playItf, &pos);
    CHECK((double)pos >= p0 + (SLmillisecond)100,
          "silence keeps GetPosition advancing (underrun paces truthfully)");

    /* stop discards everything queued or mid-flight; playIndex pins */
    CHECK(bqVt->Enqueue(bqItf, pcmBuf, sizeof pcmBuf) == SL_RESULT_SUCCESS, "Enqueue #3");
    int cbBeforeStop = g_cbCount;
    CHECK(playVt->SetPlayState(playItf, SL_PLAYSTATE_STOPPED) == SL_RESULT_SUCCESS,
          "SetPlayState(STOPPED)");
    zsleep(300); /* let the in-flight buffer's drop land */
    CHECK(bqVt->GetState(bqItf, &st) == SL_RESULT_SUCCESS && st.count == 0,
          "stop dropped the queued buffer");
    CHECK(g_cbCount == cbBeforeStop, "dropped buffer fires no late callback");
    CHECK(playVt->GetPlayState(playItf, &pstate) == SL_RESULT_SUCCESS &&
          pstate == SL_PLAYSTATE_STOPPED, "GetPlayState reports STOPPED");

    objDestroy(player);
    objDestroy(mix);
    objDestroy(engine);
    fprintf(stderr, "[audio-probe] stage teardown survived\n");

    /* ── stage 3: Destroy from inside the buffer-queue callback ──────── */
    memset(&g_ds, 0, sizeof g_ds);
    g_ds.armedAt = 3;

    SLObjectItf engine2 = NULL, player2 = NULL;
    rc = slCreateEngine((void **)&engine2, 0, NULL, 0, NULL, NULL);
    CHECK(rc == SL_RESULT_SUCCESS && objRealize(engine2) == SL_RESULT_SUCCESS,
          "second engine created and realized");
    void *eitf2 = NULL;
    CHECK(objGetIf(engine2, &SL_IID_ENGINE, &eitf2) == SL_RESULT_SUCCESS,
          "second engine routes SL_IID_ENGINE");

    engVt = *(struct SLEngineVt **)eitf2; /* *handle is the vtable */

    struct SLDataLocator_BufferQueue srcLoc2 = { SL_DATALOCATOR_BUFFERQUEUE, 8 };
    struct SLDataFormat_PCM fmt2 = {
        SL_DATAFORMAT_PCM, 1 /*mono*/, 8000 * 1000, 16, 16, 0,
        SL_BYTEORDER_LITTLEENDIAN };
    struct SLDataLocator_OutputMix snkLoc2 = { 0 /* no sink: NULL is legal */ };
    struct SLDataSource_ src2 = { &srcLoc2, &fmt2 };
    struct SLDataSink_   snk2 = { &snkLoc2, NULL };

    static const SLInterfaceID playerIfs2[] = { &SL_IID_PLAY, &SL_IID_BUFFERQUEUE };
    static const SLboolean      wanted2[]   = { SL_TRUE, SL_TRUE };
    rc = engVt->CreateAudioPlayer(eitf2, &player2, &src2, &snk2, 2, playerIfs2, wanted2);
    CHECK(rc == SL_RESULT_SUCCESS && player2 != NULL && objRealize(player2) == SL_RESULT_SUCCESS,
          "second player (8k mono, NULL sink) created and realized");

    void *bqItf2 = NULL;
    CHECK(objGetIf(player2, &SL_IID_BUFFERQUEUE, &bqItf2) == SL_RESULT_SUCCESS,
          "second player routes SL_IID_BUFFERQUEUE");
    g_ds.player      = player2;
    g_ds.bqVt        = *(struct SLBufferQueueVt **)bqItf2;
    g_ds.bqItf       = bqItf2;
    g_ds.frameBytes  = PCM_BYTES2;

    static uint8_t pcmBuf2[PCM_BYTES2];
    memset(pcmBuf2, 0x33, sizeof pcmBuf2);
    CHECK(g_ds.bqVt->RegisterCallback(bqItf2, dsCallback, &g_ds) == SL_RESULT_SUCCESS,
          "destroy-stage callback registered");
    CHECK(g_ds.bqVt->Enqueue(bqItf2, pcmBuf2, sizeof pcmBuf2) == SL_RESULT_SUCCESS,
          "destroy-stage Enqueue #1");

    void *playItf2 = NULL;
    CHECK(objGetIf(player2, &SL_IID_PLAY, &playItf2) == SL_RESULT_SUCCESS,
          "second player routes SL_IID_PLAY");
    CHECK((*(struct SLPlayVt **)playItf2)->SetPlayState(playItf2, SL_PLAYSTATE_PLAYING) ==
          SL_RESULT_SUCCESS, "second player starts PLAYING");

    /* three 50 ms buffers: callbacks 1 and 2 refill, 3 destroys. The
     * worker hands the free to itself, the game's thread never joins. */
    t0 = nowMs();
    while (nowMs() - t0 < 5000.0) {
        if (g_ds.count >= g_ds.armedAt) break;
        zsleep(10);
    }
    CHECK(g_ds.count == g_ds.armedAt, "destroy fired on the third completed buffer");
    zsleep(300); /* worker-side objFree (dying handoff) must land */
    fprintf(stderr, "[audio-probe] Destroy from inside the callback: survived (%d callbacks)\n",
            g_ds.count);
    objDestroy(engine2); /* player2 died from the callback; mix was freed above */

    fprintf(stderr, "[audio-probe] %s\n", g_failures ? "FAILED" : "all assertions held");
    return g_failures ? 1 : 0;
}
