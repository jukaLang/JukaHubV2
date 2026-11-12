/* logshim.c — liblog: __android_log_* to stderr.
 *
 * Every game calls these; every launch log becomes readable in
 * data/game-profiles/logs/<id>.log because the profile launcher already
 * captures the child's stderr. Priorities are rendered with the same
 * letters logcat prints so grepping stays familiar:
 *
 *   [game][<tag>][<letter>] <message>
 *
 * ANDROID_LOG_FATAL and __android_log_assert abort, as on Android.
 * ANDROIDPAK_LOG_LEVEL=V|D|I|W|E|F|S sets the minimum priority (default V).
 */

#define _GNU_SOURCE
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#if !defined(_WIN32)
#include <unistd.h>
#endif

#include "actbridge.h" /* ANDROIDRT_EXPORT */

/* android/log.h priorities */
enum {
    ANDROID_LOG_UNKNOWN = 0,
    ANDROID_LOG_DEFAULT,
    ANDROID_LOG_VERBOSE,
    ANDROID_LOG_DEBUG,
    ANDROID_LOG_INFO,
    ANDROID_LOG_WARN,
    ANDROID_LOG_ERROR,
    ANDROID_LOG_FATAL,
    ANDROID_LOG_SILENT,
};

static int g_minPriority = -1;
static const char *g_defaultTag = "game";
#if !defined(_WIN32)
static int g_originalStderr = -1;
#endif

/* Some native apps capture fd 2 and forward every line to liblog. Keep the
 * launcher's original log destination before their onCreate can redirect it,
 * or writing a liblog message back to fd 2 creates an endless feedback loop. */
void apak_logInit(void) {
#if !defined(_WIN32)
    if (g_originalStderr < 0) g_originalStderr = dup(STDERR_FILENO);
#endif
}

static const char *letterFor(int prio) {
    switch (prio) {
        case ANDROID_LOG_VERBOSE: return "V";
        case ANDROID_LOG_DEBUG:   return "D";
        case ANDROID_LOG_INFO:    return "I";
        case ANDROID_LOG_WARN:    return "W";
        case ANDROID_LOG_ERROR:   return "E";
        case ANDROID_LOG_FATAL:   return "F";
        case ANDROID_LOG_SILENT:  return "S";
        default:                  return "I";
    }
}

static int minPriority(void) {
    if (g_minPriority < 0) {
        const char *v = getenv("ANDROIDPAK_LOG_LEVEL");
        static const char letters[] = "VDIWEFS";
        const char *hit = v && *v ? strchr(letters, v[0]) : NULL;
        g_minPriority = hit ? ANDROID_LOG_VERBOSE + (int)(hit - letters) : ANDROID_LOG_VERBOSE;
    }
    return g_minPriority;
}

static int loggable(int prio) { return prio >= minPriority() || prio == ANDROID_LOG_FATAL; }

/* One line per message: the text is formatted first, then written with its
 * prefix in a single call so lines from game threads never interleave. */
static void emit(int prio, const char *tag, const char *text) {
    size_t len = text ? strlen(text) : 0;
    while (len && (text[len - 1] == '\n' || text[len - 1] == '\r')) len--;
#if !defined(_WIN32)
    if (g_originalStderr >= 0) {
        dprintf(g_originalStderr, "[game][%s][%s] %.*s\n", tag ? tag : g_defaultTag,
                letterFor(prio), (int)len, text ? text : "");
        return;
    }
#endif
    fprintf(stderr, "[game][%s][%s] %.*s\n", tag ? tag : g_defaultTag, letterFor(prio), (int)len, text ? text : "");
    if (prio == ANDROID_LOG_FATAL) fflush(stderr);
}

static void emitv(int prio, const char *tag, const char *fmt, va_list args) {
    char stackBuf[1024];
    char *buf = stackBuf;
    va_list copy;
    int n;
    va_copy(copy, args);
    n = vsnprintf(stackBuf, sizeof stackBuf, fmt ? fmt : "", copy);
    va_end(copy);
    if (n >= (int)sizeof stackBuf) {
        buf = (char *)malloc((size_t)n + 1);
        if (buf) vsnprintf(buf, (size_t)n + 1, fmt, args);
        else buf = stackBuf;
    }
    emit(prio, tag, buf);
    if (buf != stackBuf) free(buf);
}

ANDROIDRT_EXPORT int __android_log_write(int prio, const char *tag, const char *text) {
    if (!loggable(prio)) return 0;
    emit(prio, tag, text);
    return 1;
}

ANDROIDRT_EXPORT int __android_log_print(int prio, const char *tag, const char *fmt, ...) {
    va_list args;
    if (!loggable(prio)) return 0;
    va_start(args, fmt);
    emitv(prio, tag, fmt, args);
    va_end(args);
    return 1;
}

ANDROIDRT_EXPORT int __android_log_vprint(int prio, const char *tag, const char *fmt, va_list args) {
    if (!loggable(prio)) return 0;
    emitv(prio, tag, fmt, args);
    return 1;
}

ANDROIDRT_EXPORT int __android_log_buf_print(int bufID, int prio, const char *tag, const char *fmt, ...) {
    va_list args;
    int rc;
    (void)bufID;
    va_start(args, fmt);
    rc = __android_log_vprint(prio, tag, fmt, args);
    va_end(args);
    return rc;
}

ANDROIDRT_EXPORT int __android_log_buf_write(int bufID, int prio, const char *tag, const char *text) {
    (void)bufID;
    return __android_log_write(prio, tag, text);
}

/* __android_log_assert is __noreturn: `cond` is the stringified condition
 * of an assertion that has ALREADY failed (or NULL for LOG_ALWAYS_FATAL). */
ANDROIDRT_EXPORT void __android_log_assert(const char *cond, const char *tag, const char *fmt, ...) {
    char msg[1024];
    if (fmt) {
        va_list args;
        va_start(args, fmt);
        vsnprintf(msg, sizeof msg, fmt, args);
        va_end(args);
    } else if (cond) {
        snprintf(msg, sizeof msg, "Assertion failed: %s", cond);
    } else {
        snprintf(msg, sizeof msg, "Unspecified assertion failed");
    }
    emit(ANDROID_LOG_FATAL, tag ? tag : "assert", msg);
    fflush(stderr);
    abort();
}

ANDROIDRT_EXPORT int __android_log_is_loggable(int prio, const char *tag, int defaultPrio) {
    (void)tag;
    (void)defaultPrio;
    return loggable(prio);
}

ANDROIDRT_EXPORT int __android_log_is_loggable_len(int prio, const char *tag, size_t len, int defaultPrio) {
    (void)tag;
    (void)len;
    (void)defaultPrio;
    return loggable(prio);
}

ANDROIDRT_EXPORT int32_t __android_log_set_minimum_priority(int32_t priority) {
    int32_t old = minPriority();
    g_minPriority = priority;
    return old;
}

ANDROIDRT_EXPORT int32_t __android_log_get_minimum_priority(void) { return minPriority(); }

ANDROIDRT_EXPORT void __android_log_set_default_tag(const char *tag) {
    if (tag) g_defaultTag = tag;
}

/* API 30 logger structs are passed by pointer; the message is what matters. */
struct ApakLogMessage {
    size_t structSize;
    int32_t bufferId;
    int32_t priority;
    const char *tag;
    const char *file;
    uint32_t line;
    const char *message;
};

ANDROIDRT_EXPORT void __android_log_write_log_message(struct ApakLogMessage *m) {
    if (m && loggable(m->priority)) emit(m->priority, m->tag, m->message);
}

ANDROIDRT_EXPORT void __android_log_call_aborter(const char *abortMessage) {
    fprintf(stderr, "[androidrt] abort: %s\n", abortMessage ? abortMessage : "(no message)");
    fflush(stderr);
    abort();
}
