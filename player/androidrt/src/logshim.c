/* logshim.c — liblog.so: __android_log_* to stderr.
 *
 * Every game calls these; every launch log becomes readable in
 * data/game-profiles/logs/<id>.log because the profile launcher already
 * captures the child's stderr. Priorities are rendered with the same
 * letters logcat prints so grepping stays familiar.
 */

#define _GNU_SOURCE
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

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

static void emit(int prio, const char *tag, const char *fmt, va_list args) {
    const char *letter = letterFor(prio);
    if (fmt) {
        vfprintf(stderr, fmt, args);
        /* __android_log_print messages end with their own newline usually;
         * keep one so log lines never glue together. */
        fputc('\n', stderr);
        (void)tag;
        (void)letter;
    }
}

ANDROIDRT_EXPORT int __android_log_write(int prio, const char *tag, const char *text) {
    if (!text) return 1;
    fprintf(stderr, "[game][%s][%s] %s\n", tag ? tag : "game", letterFor(prio), text);
    return 1;
}

ANDROIDRT_EXPORT int __android_log_print(int prio, const char *tag, const char *fmt, ...) {
    va_list args;
    va_start(args, fmt);
    fprintf(stderr, "[game][%s][%s] ", tag ? tag : "game", letterFor(prio));
    emit(prio, tag, fmt, args);
    va_end(args);
    return 1;
}

ANDROIDRT_EXPORT int __android_log_vprint(int prio, const char *tag, const char *fmt, va_list args) {
    fprintf(stderr, "[game][%s][%s] ", tag ? tag : "game", letterFor(prio));
    emit(prio, tag, fmt, args);
    return 1;
}

ANDROIDRT_EXPORT int __android_log_buf_print(int bufID, int prio, const char *tag, const char *fmt, ...) {
    va_list args;
    va_start(args, fmt);
    int rc = __android_log_vprint(prio, tag, fmt, args);
    va_end(args);
    return rc;
}

ANDROIDRT_EXPORT int __android_log_buf_write(int bufID, int prio, const char *tag, const char *text) {
    (void)bufID;
    return __android_log_write(prio, tag, text);
}

ANDROIDRT_EXPORT void __android_log_assert(const char *cond, const char *tag, const char *fmt, ...) {
    if (cond) return;
    va_list args;
    va_start(args, fmt);
    __android_log_vprint(ANDROID_LOG_FATAL, tag ? tag : "assert", fmt ? fmt : "assertion failed", args);
    va_end(args);
    abort();
}
