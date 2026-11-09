/* device.c — see device.h. */
#define _GNU_SOURCE
#include <ctype.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "device.h"

/* Panels per TrimUI's specifications: the Smart Pro family is 4.96"
 * 1280x720 (1468.6 px diagonal: 296 dpi), the Brick family (Brick, Hammer)
 * 3.2" 1024x768 (1280 px diagonal: 400 dpi). */
static const ApakDevice kDevices[] = {
    { "trimui-smart-pro", "TrimUI", "Smart Pro", "tg5040", 1280, 720, 296.1f },
    { "trimui-smart-pro-s", "TrimUI", "Smart Pro S", "tg5050", 1280, 720, 296.1f },
    { "trimui-brick", "TrimUI", "Brick", "tg3040", 1024, 768, 400.0f },
    { "trimui-hammer", "TrimUI", "Hammer", "tg3040", 1024, 768, 400.0f },
    { "trimui-brick-pro", "TrimUI", "Brick Pro", "tg4040", 1024, 768, 400.0f },
};

static int g_measuredW, g_measuredH;

static int containsWord(const char *hay, const char *needle) {
    char low[512];
    size_t i;
    for (i = 0; hay[i] && i + 1 < sizeof low; i++) low[i] = (char)tolower((unsigned char)hay[i]);
    low[i] = '\0';
    return strstr(low, needle) != NULL;
}

static void readText(const char *path, char *out, size_t cap) {
    FILE *f = fopen(path, "rb");
    size_t n = 0, i;
    out[0] = '\0';
    if (!f) return;
    n = fread(out, 1, cap - 1, f);
    fclose(f);
    for (i = 0; i < n; i++)
        if (out[i] == '\0') out[i] = ' '; /* device-tree strings are NUL separated */
    out[n] = '\0';
}

static const ApakDevice *detect(void) {
    const char *env = getenv("ANDROIDPAK_DEVICE");
    char model[256], compat[256], both[520];
    size_t i;
    if (env && *env) {
        for (i = 0; i < sizeof kDevices / sizeof kDevices[0]; i++)
            if (strcmp(kDevices[i].id, env) == 0) return &kDevices[i];
        if (containsWord(env, "tg3040") || containsWord(env, "brick")) return &kDevices[2];
        if (containsWord(env, "tg5050")) return &kDevices[1];
    }
    readText("/proc/device-tree/model", model, sizeof model);
    readText("/proc/device-tree/compatible", compat, sizeof compat);
    snprintf(both, sizeof both, "%s %s", model, compat);
    if (containsWord(both, "hammer")) return &kDevices[3];
    if (containsWord(both, "tg3040") || containsWord(both, "brick")) return &kDevices[2];
    if (containsWord(both, "tg4040")) return &kDevices[4];
    if (containsWord(both, "tg5050")) return &kDevices[1];
    return &kDevices[0];
}

const ApakDevice *apak_device(void) {
    static const ApakDevice *cached;
    if (!cached) cached = detect();
    return cached;
}

static int parseSize(const char *s, int *w, int *h) {
    int pw = 0, ph = 0;
    if (!s || sscanf(s, "%dx%d", &pw, &ph) != 2 || pw <= 0 || ph <= 0 || pw > 8192 || ph > 8192) return 0;
    *w = pw;
    *h = ph;
    return 1;
}

void apak_devicePanel(int *w, int *h) {
    const ApakDevice *d = apak_device();
    if (parseSize(getenv("ANDROIDRT_WINDOW"), w, h)) return;
    if (parseSize(getenv("ANDROIDPAK_PANEL"), w, h)) return;
    if (g_measuredW > 0 && g_measuredH > 0) {
        *w = g_measuredW;
        *h = g_measuredH;
        return;
    }
    *w = d->panelW;
    *h = d->panelH;
}

void apak_deviceSetMeasuredPanel(int w, int h) {
    if (w > 0 && h > 0 && w <= 8192 && h <= 8192) {
        g_measuredW = w;
        g_measuredH = h;
    }
}

float apak_deviceDpi(void) {
    const ApakDevice *d = apak_device();
    const char *env = getenv("ANDROIDPAK_DPI");
    float v = env ? (float)atof(env) : 0.0f;
    if (v > 50.0f && v < 2000.0f) return v;
    return d->dpi;
}

int apak_deviceDensity(void) { return 320; /* xhdpi: what both panels' sizes call for at handheld distance */ }
