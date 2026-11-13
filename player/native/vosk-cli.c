// A small WAV-to-text front end for Vosk on TrimUI Smart Pro. The library and
// model live beside this binary. Recorded audio stays on the device.
#include "vosk_api.h"

#include <dlfcn.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

static uint32_t le32(const unsigned char *p) {
    return (uint32_t)p[0] | ((uint32_t)p[1] << 8) |
           ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24);
}

int main(int argc, char **argv) {
    if (argc == 2 && strcmp(argv[1], "--help") == 0) {
        puts("usage: vosk-cli -m MODEL -f WAV|-");
        return 0;
    }
    if (argc != 5 || strcmp(argv[1], "-m") || strcmp(argv[3], "-f")) {
        fputs("usage: vosk-cli -m MODEL -f WAV|-\n", stderr);
        return 2;
    }

    FILE *input = strcmp(argv[4], "-") == 0 ? stdin : fopen(argv[4], "rb");
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

    // Loading by the binary's own path avoids a global library install and
    // keeps Vosk optional for people who use Groq or Whisper instead.
    char library[4096];
    ssize_t n = readlink("/proc/self/exe", library, sizeof(library) - 1);
    if (n <= 0 || n >= (ssize_t)sizeof(library)) {
        fputs("cannot locate speech library\n", stderr);
        if (input != stdin) fclose(input);
        return 2;
    }
    library[n] = '\0';
    char *slash = strrchr(library, '/');
    if (!slash || (size_t)(slash - library) + sizeof("/libvosk.so") > sizeof(library)) {
        fputs("cannot locate speech library\n", stderr);
        if (input != stdin) fclose(input);
        return 2;
    }
    strcpy(slash, "/libvosk.so");
    void *lib = dlopen(library, RTLD_NOW | RTLD_LOCAL);
    if (!lib) {
        fprintf(stderr, "speech library: %s\n", dlerror());
        if (input != stdin) fclose(input);
        return 2;
    }

#define SYMBOL(name) dlsym(lib, "vosk_" #name)
    void (*set_log_level)(int) = SYMBOL(set_log_level);
    VoskModel *(*model_new)(const char *) = SYMBOL(model_new);
    void (*model_free)(VoskModel *) = SYMBOL(model_free);
    VoskRecognizer *(*recognizer_new)(VoskModel *, float) = SYMBOL(recognizer_new);
    void (*recognizer_free)(VoskRecognizer *) = SYMBOL(recognizer_free);
    int (*accept_waveform)(VoskRecognizer *, const char *, int) = SYMBOL(recognizer_accept_waveform);
    const char *(*final_result)(VoskRecognizer *) = SYMBOL(recognizer_final_result);
#undef SYMBOL
    if (!set_log_level || !model_new || !model_free || !recognizer_new ||
        !recognizer_free || !accept_waveform || !final_result) {
        fputs("speech library is incomplete\n", stderr);
        dlclose(lib);
        if (input != stdin) fclose(input);
        return 2;
    }

    set_log_level(-1);
    VoskModel *model = model_new(argv[2]);
    if (!model) {
        fputs("speech model could not load\n", stderr);
        dlclose(lib);
        if (input != stdin) fclose(input);
        return 2;
    }
    VoskRecognizer *rec = recognizer_new(model, 16000.0f);
    if (!rec) {
        fputs("speech recognizer could not start\n", stderr);
        model_free(model);
        dlclose(lib);
        if (input != stdin) fclose(input);
        return 2;
    }

    char pcm[4096];
    size_t got;
    while ((got = fread(pcm, 1, sizeof(pcm), input)) > 0)
        accept_waveform(rec, pcm, (int)got);
    int failed = ferror(input);
    if (!failed) {
        const char *result = final_result(rec);
        if (result) puts(result);
        else failed = 1;
    }
    recognizer_free(rec);
    model_free(model);
    dlclose(lib);
    if (input != stdin) fclose(input);
    return failed ? 2 : 0;
}
