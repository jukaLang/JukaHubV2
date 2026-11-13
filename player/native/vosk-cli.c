// A small speech-to-text front end for Vosk on TrimUI Smart Pro. The library
// and model live beside this binary. Recorded audio stays on the device.
//
// Two modes:
//
//   vosk-cli -m MODEL -f WAV|-     one clip: read a WAV, print the final JSON.
//   vosk-cli --serve -m MODEL      a warm recognizer for JukaHub (stt_stream.go):
//                                  the model loads once and audio is decoded
//                                  while it is being recorded.
//
// The server reads and writes frames on stdin/stdout: one type byte, a 32-bit
// little-endian payload length, the payload. Once the model is loaded it sends
// K "ready" (X and a message, then exit, when it cannot load). Requests:
//
//   B  begin an utterance. Payload: one flag byte (1: word times and
//      confidences in results, 2: run at background priority), then an
//      optional JSON grammar (["hey juka", "[unk]"]). Reply K.
//   A  16 kHz mono S16LE audio. Reply R + result JSON when an utterance just
//      ended (Vosk's endpoint), else K.
//   P  like A, but replies P + partial JSON when no utterance ended.
//   E  end: reply F + the final JSON; the recognizer is freed.
//   W  newline-separated words: reply K + the ones the model knows.
//   Q  quit.
//
// Errors reply X + a message. Single-threaded: one utterance at a time. The
// server exits when stdin closes (JukaHub quit or died).
#include "vosk_api.h"

#include <dlfcn.h>
#include <errno.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/resource.h>
#include <unistd.h>

#define USAGE "usage: vosk-cli -m MODEL -f WAV|-\n       vosk-cli --serve -m MODEL\n"
#define SERVE_MAX_PAYLOAD (1u << 20)
#define SERVE_BACKGROUND_NICE 10

static uint32_t le32(const unsigned char *p) {
    return (uint32_t)p[0] | ((uint32_t)p[1] << 8) |
           ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24);
}

struct vosk {
    void *lib;
    void (*set_log_level)(int);
    VoskModel *(*model_new)(const char *);
    void (*model_free)(VoskModel *);
    int (*model_find_word)(VoskModel *, const char *);
    VoskRecognizer *(*recognizer_new)(VoskModel *, float);
    VoskRecognizer *(*recognizer_new_grm)(VoskModel *, float, const char *);
    void (*recognizer_set_words)(VoskRecognizer *, int);
    void (*recognizer_free)(VoskRecognizer *);
    int (*accept_waveform)(VoskRecognizer *, const char *, int);
    const char *(*result)(VoskRecognizer *);
    const char *(*partial_result)(VoskRecognizer *);
    const char *(*final_result)(VoskRecognizer *);
};

// Loading by the binary's own path avoids a global library install and keeps
// Vosk optional for people who use Groq or Whisper instead.
static int vosk_load(struct vosk *v) {
    char library[4096];
    ssize_t n = readlink("/proc/self/exe", library, sizeof(library) - 1);
    if (n <= 0 || n >= (ssize_t)sizeof(library)) {
        fputs("cannot locate speech library\n", stderr);
        return -1;
    }
    library[n] = '\0';
    char *slash = strrchr(library, '/');
    if (!slash || (size_t)(slash - library) + sizeof("/libvosk.so") > sizeof(library)) {
        fputs("cannot locate speech library\n", stderr);
        return -1;
    }
    strcpy(slash, "/libvosk.so");
    v->lib = dlopen(library, RTLD_NOW | RTLD_LOCAL);
    if (!v->lib) {
        fprintf(stderr, "speech library: %s\n", dlerror());
        return -1;
    }
#define SYMBOL(name) dlsym(v->lib, "vosk_" #name)
    v->set_log_level = SYMBOL(set_log_level);
    v->model_new = SYMBOL(model_new);
    v->model_free = SYMBOL(model_free);
    v->model_find_word = SYMBOL(model_find_word);
    v->recognizer_new = SYMBOL(recognizer_new);
    v->recognizer_new_grm = SYMBOL(recognizer_new_grm);
    v->recognizer_set_words = SYMBOL(recognizer_set_words);
    v->recognizer_free = SYMBOL(recognizer_free);
    v->accept_waveform = SYMBOL(recognizer_accept_waveform);
    v->result = SYMBOL(recognizer_result);
    v->partial_result = SYMBOL(recognizer_partial_result);
    v->final_result = SYMBOL(recognizer_final_result);
#undef SYMBOL
    if (!v->set_log_level || !v->model_new || !v->model_free || !v->recognizer_new ||
        !v->recognizer_free || !v->accept_waveform || !v->final_result) {
        fputs("speech library is incomplete\n", stderr);
        dlclose(v->lib);
        v->lib = NULL;
        return -1;
    }
    return 0;
}

// ── One clip ────────────────────────────────────────────────────────────────

static int run_once(const char *model_path, const char *wav_path) {
    FILE *input = strcmp(wav_path, "-") == 0 ? stdin : fopen(wav_path, "rb");
    if (!input) {
        perror("opening WAV");
        return 2;
    }
    unsigned char wav[44];
    if (fread(wav, 1, sizeof(wav), input) != sizeof(wav) ||
        memcmp(wav, "RIFF", 4) || memcmp(wav + 8, "WAVE", 4) ||
        wav[20] != 1 || wav[21] != 0 || wav[22] != 1 || wav[23] != 0 ||
        le32(wav + 24) != 16000 || wav[34] != 16 || wav[35] != 0 ||
        memcmp(wav + 36, "data", 4)) {
        fputs("expected mono 16-bit 16 kHz WAV\n", stderr);
        if (input != stdin) fclose(input);
        return 2;
    }

    struct vosk v;
    memset(&v, 0, sizeof(v));
    if (vosk_load(&v) != 0) {
        if (input != stdin) fclose(input);
        return 2;
    }
    v.set_log_level(-1);
    VoskModel *model = v.model_new(model_path);
    if (!model) {
        fputs("speech model could not load\n", stderr);
        dlclose(v.lib);
        if (input != stdin) fclose(input);
        return 2;
    }
    VoskRecognizer *rec = v.recognizer_new(model, 16000.0f);
    if (!rec) {
        fputs("speech recognizer could not start\n", stderr);
        v.model_free(model);
        dlclose(v.lib);
        if (input != stdin) fclose(input);
        return 2;
    }

    char pcm[4096];
    size_t got;
    while ((got = fread(pcm, 1, sizeof(pcm), input)) > 0)
        v.accept_waveform(rec, pcm, (int)got);
    int failed = ferror(input);
    if (!failed) {
        const char *result = v.final_result(rec);
        if (result) puts(result);
        else failed = 1;
    }
    v.recognizer_free(rec);
    v.model_free(model);
    dlclose(v.lib);
    if (input != stdin) fclose(input);
    return failed ? 2 : 0;
}

// ── Server ──────────────────────────────────────────────────────────────────

// read_full reads exactly len bytes: 1 on success, 0 at a clean end of input
// before the first byte, -1 on an error or an input cut off mid-frame.
static int read_full(int fd, void *buf, size_t len) {
    unsigned char *p = buf;
    size_t done = 0;
    while (done < len) {
        ssize_t n = read(fd, p + done, len - done);
        if (n < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        if (n == 0) return done == 0 ? 0 : -1;
        done += (size_t)n;
    }
    return 1;
}

static int write_full(int fd, const void *buf, size_t len) {
    const unsigned char *p = buf;
    size_t done = 0;
    while (done < len) {
        ssize_t n = write(fd, p + done, len - done);
        if (n < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        done += (size_t)n;
    }
    return 0;
}

static int send_frame(char type, const char *payload, size_t len) {
    unsigned char head[5];
    head[0] = (unsigned char)type;
    head[1] = (unsigned char)(len & 0xff);
    head[2] = (unsigned char)((len >> 8) & 0xff);
    head[3] = (unsigned char)((len >> 16) & 0xff);
    head[4] = (unsigned char)((len >> 24) & 0xff);
    if (write_full(STDOUT_FILENO, head, sizeof(head)) != 0) return -1;
    if (len > 0 && write_full(STDOUT_FILENO, payload, len) != 0) return -1;
    return 0;
}

static int send_text(char type, const char *text) {
    return send_frame(type, text ? text : "", text ? strlen(text) : 0);
}

// known_words answers W: each newline-separated word the model can recognize,
// newline-separated. Without vosk_model_find_word every word is returned, and
// Vosk itself skips the unknown ones of a grammar.
static int known_words(struct vosk *v, VoskModel *model, char *list, size_t len) {
    char *out = malloc(len + 1);
    if (!out) return send_text('X', "out of memory");
    size_t o = 0;
    size_t start = 0;
    for (size_t i = 0; i <= len; i++) {
        if (i < len && list[i] != '\n') continue;
        list[i < len ? i : len] = '\0';
        const char *word = list + start;
        size_t wlen = i - start;
        start = i + 1;
        if (wlen == 0) continue;
        if (v->model_find_word && v->model_find_word(model, word) < 0) continue;
        if (o > 0) out[o++] = '\n';
        memcpy(out + o, word, wlen);
        o += wlen;
    }
    int rc = send_frame('K', out, o);
    free(out);
    return rc;
}

static int serve(const char *model_path) {
    // JukaHub going away (even killed) closes the pipe: the next read ends
    // the loop, and a write after that must fail, not kill the process with
    // SIGPIPE before it can clean up. (No parent-death signal: it follows the
    // thread that started the process, which in Go is any thread.)
    signal(SIGPIPE, SIG_IGN);

    struct vosk v;
    memset(&v, 0, sizeof(v));
    if (vosk_load(&v) != 0) {
        send_text('X', "speech library could not load");
        return 2;
    }
    v.set_log_level(-1);
    VoskModel *model = v.model_new(model_path);
    if (!model) {
        send_text('X', "speech model could not load");
        dlclose(v.lib);
        return 2;
    }
    if (send_text('K', "ready") != 0) {
        v.model_free(model);
        dlclose(v.lib);
        return 0;
    }

    VoskRecognizer *rec = NULL;
    char *buf = malloc(SERVE_MAX_PAYLOAD + 1);
    int rc = 0;
    if (!buf) {
        send_text('X', "out of memory");
        rc = 2;
        goto out;
    }
    for (;;) {
        unsigned char head[5];
        int r = read_full(STDIN_FILENO, head, sizeof(head));
        if (r == 0) break;
        if (r < 0) {
            rc = 2;
            break;
        }
        uint32_t len = le32(head + 1);
        if (len > SERVE_MAX_PAYLOAD) {
            // The stream is out of step: nothing after this can be trusted.
            send_text('X', "frame too large");
            rc = 2;
            break;
        }
        if (len > 0 && read_full(STDIN_FILENO, buf, len) != 1) {
            rc = 2;
            break;
        }
        buf[len] = '\0';
        int sent = 0;
        switch (head[0]) {
        case 'B': {
            if (rec) {
                v.recognizer_free(rec);
                rec = NULL;
            }
            unsigned flags = len > 0 ? (unsigned char)buf[0] : 0;
            const char *grammar = len > 1 ? buf + 1 : NULL;
            setpriority(PRIO_PROCESS, 0, (flags & 2) ? SERVE_BACKGROUND_NICE : 0);
            if (grammar && grammar[0] && v.recognizer_new_grm)
                rec = v.recognizer_new_grm(model, 16000.0f, grammar);
            else
                rec = v.recognizer_new(model, 16000.0f);
            if (!rec) {
                sent = send_text('X', "speech recognizer could not start");
                break;
            }
            if (v.recognizer_set_words) v.recognizer_set_words(rec, (flags & 1) ? 1 : 0);
            sent = send_text('K', "");
            break;
        }
        case 'A':
        case 'P': {
            if (!rec) {
                sent = send_text('X', "no utterance");
                break;
            }
            int ended = v.accept_waveform(rec, buf, (int)(len & ~1u));
            if (ended > 0 && v.result)
                sent = send_text('R', v.result(rec));
            else if (ended < 0)
                sent = send_text('X', "speech decoding failed");
            else if (head[0] == 'P' && v.partial_result)
                sent = send_text('P', v.partial_result(rec));
            else
                sent = send_text('K', "");
            break;
        }
        case 'E':
            if (!rec) {
                sent = send_text('F', "{\"text\" : \"\"}");
                break;
            }
            sent = send_text('F', v.final_result(rec));
            v.recognizer_free(rec);
            rec = NULL;
            break;
        case 'W':
            sent = known_words(&v, model, buf, len);
            break;
        case 'Q':
            goto out;
        default:
            sent = send_text('X', "unknown request");
            break;
        }
        if (sent != 0) {
            // JukaHub is gone.
            break;
        }
    }
out:
    if (rec) v.recognizer_free(rec);
    free(buf);
    v.model_free(model);
    dlclose(v.lib);
    return rc;
}

int main(int argc, char **argv) {
    if (argc == 2 && strcmp(argv[1], "--help") == 0) {
        // stt_stream.go looks for "--serve" here to know this build streams.
        fputs(USAGE, stdout);
        return 0;
    }
    if (argc == 4 && strcmp(argv[1], "--serve") == 0 && strcmp(argv[2], "-m") == 0)
        return serve(argv[3]);
    if (argc != 5 || strcmp(argv[1], "-m") || strcmp(argv[3], "-f")) {
        fputs(USAGE, stderr);
        return 2;
    }
    return run_once(argv[2], argv[4]);
}
