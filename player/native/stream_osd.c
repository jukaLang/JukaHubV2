/* stream_osd.c - the on-screen display (see stream_osd.h). */
#include "stream_osd.h"

#include "osd_font.h"

#include <math.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static const SDL_Color OSD_PANEL = {16, 20, 30, 230};
static const SDL_Color OSD_INSET = {30, 36, 52, 255};
static const SDL_Color OSD_TRACK = {255, 255, 255, 28};
static const SDL_Color OSD_TEXT = {242, 245, 250, 255};
static const SDL_Color OSD_TEXT2 = {178, 188, 204, 255};
static const SDL_Color OSD_TEXT3 = {130, 140, 158, 255};
static const SDL_Color OSD_ACCENT = {72, 132, 255, 255};
static const SDL_Color OSD_GOOD = {82, 204, 128, 255};
static const SDL_Color OSD_WARN = {255, 190, 72, 255};
static const SDL_Color OSD_DANGER = {255, 108, 100, 255};

static int iround(float v)
{
    return (int)(v < 0.0f ? v - 0.5f : v + 0.5f);
}

static int imax(int a, int b)
{
    return a > b ? a : b;
}

static int imin(int a, int b)
{
    return a < b ? a : b;
}

/* A length given for a 720-line screen, in output pixels. */
static int S(const Osd *osd, float v)
{
    return iround(v * osd->ui);
}

/* snprintf where cutting the text short is fine (display strings): through a
 * va_list, so -Wformat-truncation does not second-guess every call. */
__attribute__((format(printf, 3, 4)))
static void textf(char *out, size_t size, const char *format, ...)
{
    va_list args;
    va_start(args, format);
    vsnprintf(out, size, format, args);
    va_end(args);
}

/* A bounded copy that, unlike snprintf("%s"), gives -Wformat-truncation nothing to
 * guess about. */
static void copy_text(char *dst, size_t size, const char *src)
{
    if(!size) return;
    size_t n = strlen(src ? src : "");
    if(n >= size) n = size - 1;
    if(n) memcpy(dst, src, n);
    dst[n] = '\0';
}

static SDL_Color fade(SDL_Color color, float alpha)
{
    if(alpha < 0.0f) alpha = 0.0f;
    if(alpha > 1.0f) alpha = 1.0f;
    color.a = (Uint8)iround((float)color.a * alpha);
    return color;
}

void osd_resize(Osd *osd)
{
    int w = 0, h = 0;
    if(!osd->renderer || SDL_GetRendererOutputSize(osd->renderer, &w, &h) != 0 || w <= 0 || h <= 0) {
        w = 1280;
        h = 720;
    }
    osd->out_w = w;
    osd->out_h = h;
    osd->ui = (float)h / 720.0f;
    if((float)w / osd->ui < 900.0f) osd->ui = (float)w / 900.0f; /* narrow screens */
    if(osd->ui < 0.4f) osd->ui = 0.4f;
}

bool osd_init(Osd *osd, SDL_Renderer *renderer)
{
    memset(osd, 0, sizeof(*osd));
    osd->renderer = renderer;
    osd_resize(osd);
    size_t count = (size_t)OSD_FONT_ATLAS_W * (size_t)OSD_FONT_ATLAS_H;
    Uint32 *pixels = calloc(count, sizeof(Uint32));
    if(!pixels) return false;
    size_t pos = 0;
    for(int chunk = 0; chunk < OSD_FONT_RLE_CHUNKS; chunk++) {
        for(const char *p = osd_font_rle[chunk]; *p && pos < count; p++) {
            unsigned int run, level;
            if(*p >= 'a' && *p <= 'z') {
                run = (unsigned int)(*p - 'a') + 1;
                level = 0;
            } else if(*p >= '0' && *p <= '9') {
                run = (unsigned int)(*p - '0') + 1;
                level = 15;
            } else if(*p >= 'A' && *p <= 'N') {
                run = 1;
                level = (unsigned int)(*p - 'A') + 1;
            } else {
                continue;
            }
            Uint32 pixel = ((Uint32)(level * 17u) << 24) | 0x00FFFFFFu;
            while(run-- && pos < count) pixels[pos++] = pixel;
        }
    }
    osd->atlas = SDL_CreateTexture(renderer, SDL_PIXELFORMAT_ARGB8888, SDL_TEXTUREACCESS_STATIC,
                                   OSD_FONT_ATLAS_W, OSD_FONT_ATLAS_H);
    if(osd->atlas) {
        SDL_UpdateTexture(osd->atlas, NULL, pixels, OSD_FONT_ATLAS_W * (int)sizeof(Uint32));
        SDL_SetTextureBlendMode(osd->atlas, SDL_BLENDMODE_BLEND);
    }
    free(pixels);
    return osd->atlas != NULL;
}

void osd_fini(Osd *osd)
{
    if(osd->atlas) SDL_DestroyTexture(osd->atlas);
    osd->atlas = NULL;
}

static float font_scale(const Osd *osd, float px)
{
    return px / (float)OSD_FONT_PIXEL_SIZE * osd->ui;
}

static const OsdGlyph *glyph_for(unsigned char c)
{
    if(c < OSD_FONT_FIRST || c >= OSD_FONT_FIRST + OSD_FONT_COUNT) c = '?';
    return &osd_font_glyphs[c - OSD_FONT_FIRST];
}

int osd_text_width(const Osd *osd, const char *text, float px)
{
    long advance = 0;
    for(const unsigned char *p = (const unsigned char *)(text ? text : ""); *p; p++)
        advance += glyph_for(*p)->advance64;
    return iround((float)advance * font_scale(osd, px) / 64.0f);
}

int osd_line_height(const Osd *osd, float px)
{
    return iround((float)OSD_FONT_LINE_HEIGHT * font_scale(osd, px));
}

void osd_text(Osd *osd, const char *text, int x, int y, float px, SDL_Color color)
{
    if(!osd->atlas || !text || !color.a) return;
    float scale = font_scale(osd, px);
    SDL_SetTextureColorMod(osd->atlas, color.r, color.g, color.b);
    SDL_SetTextureAlphaMod(osd->atlas, color.a);
    float pen = (float)x;
    for(const unsigned char *p = (const unsigned char *)text; *p; p++) {
        const OsdGlyph *g = glyph_for(*p);
        if(g->w > 0 && g->h > 0) {
            SDL_Rect src = {g->x, g->y, g->w, g->h};
            SDL_Rect dst = {iround(pen + (float)g->bearing_x * scale), y + iround((float)g->bearing_y * scale),
                            imax(1, iround((float)g->w * scale)), imax(1, iround((float)g->h * scale))};
            SDL_RenderCopy(osd->renderer, osd->atlas, &src, &dst);
        }
        pen += (float)g->advance64 * scale / 64.0f;
    }
}

void osd_fill(Osd *osd, int x, int y, int w, int h, int radius, SDL_Color color)
{
    if(w <= 0 || h <= 0 || !color.a) return;
    SDL_SetRenderDrawBlendMode(osd->renderer, SDL_BLENDMODE_BLEND);
    SDL_SetRenderDrawColor(osd->renderer, color.r, color.g, color.b, color.a);
    if(radius * 2 > h) radius = h / 2;
    if(radius * 2 > w) radius = w / 2;
    if(radius > 64) radius = 64;
    if(radius <= 0) {
        SDL_Rect rect = {x, y, w, h};
        SDL_RenderFillRect(osd->renderer, &rect);
        return;
    }
    SDL_Rect rects[2 * 64 + 1];
    int n = 0;
    for(int i = 0; i < radius; i++) {
        float dy = (float)radius - (float)i - 0.5f;
        float dx = sqrtf((float)(radius * radius) - dy * dy);
        int inset = radius - iround(dx);
        rects[n++] = (SDL_Rect){x + inset, y + i, w - 2 * inset, 1};
        rects[n++] = (SDL_Rect){x + inset, y + h - 1 - i, w - 2 * inset, 1};
    }
    rects[n++] = (SDL_Rect){x, y + radius, w, h - 2 * radius};
    SDL_RenderFillRects(osd->renderer, rects, n);
}

void osd_fit(const Osd *osd, const char *text, float px, int max_w, char *out, size_t out_size)
{
    char buffer[256];
    if(!out_size) return;
    snprintf(out, out_size, "%s", text ? text : "");
    if(osd_text_width(osd, out, px) <= max_w) return;
    snprintf(buffer, sizeof(buffer), "%s", out);
    size_t n = strlen(buffer);
    while(n > 0) {
        buffer[--n] = '\0';
        while(n > 0 && buffer[n - 1] == ' ') buffer[--n] = '\0';
        char candidate[260];
        textf(candidate, sizeof(candidate), "%s...", buffer);
        if(osd_text_width(osd, candidate, px) <= max_w) {
            copy_text(out, out_size, candidate);
            return;
        }
    }
    snprintf(out, out_size, "...");
}

/* Greedy word wrap into at most max_lines; the last line is cut with "...". */
static int wrap(const Osd *osd, const char *text, float px, int max_w, char lines[][160], int max_lines)
{
    int count = 0;
    char line[330] = {0};
    const char *p = text ? text : "";
    while(*p && count < max_lines) {
        while(*p == ' ') p++;
        if(!*p) break;
        const char *end = p;
        while(*end && *end != ' ') end++;
        char word[160];
        size_t length = (size_t)(end - p);
        if(length >= sizeof(word)) length = sizeof(word) - 1;
        memcpy(word, p, length);
        word[length] = '\0';
        char candidate[330];
        textf(candidate, sizeof(candidate), "%s%s%s", line, line[0] ? " " : "", word);
        if(!line[0] || osd_text_width(osd, candidate, px) <= max_w) {
            copy_text(line, sizeof(line), candidate);
            p = end;
            continue;
        }
        if(count == max_lines - 1) break;
        osd_fit(osd, line, px, max_w, lines[count++], 160);
        line[0] = '\0';
    }
    if(count < max_lines && line[0]) {
        if(*p) { /* text left over: show that it was cut */
            char cut[340];
            copy_text(cut, sizeof(cut) - 4, line);
            strcat(cut, " ...");
            osd_fit(osd, cut, px, max_w, lines[count++], 160);
        } else {
            osd_fit(osd, line, px, max_w, lines[count++], 160);
        }
    }
    return count;
}

static void panel(Osd *osd, int x, int y, int w, int h, SDL_Color accent, float alpha)
{
    osd_fill(osd, x, y, w, h, S(osd, 18), fade(OSD_PANEL, alpha));
    osd_fill(osd, x + S(osd, 28), y, w - S(osd, 56), imax(2, S(osd, 3)), 1, fade(accent, alpha));
}

static void dim(Osd *osd, Uint8 amount)
{
    SDL_Color shade = {0, 0, 0, amount};
    osd_fill(osd, 0, 0, osd->out_w, osd->out_h, 0, shade);
}

static void text_right(Osd *osd, const char *text, int right, int y, float px, SDL_Color color)
{
    osd_text(osd, text, right - osd_text_width(osd, text, px), y, px, color);
}

static void text_center(Osd *osd, const char *text, int cx, int y, float px, SDL_Color color)
{
    osd_text(osd, text, cx - osd_text_width(osd, text, px) / 2, y, px, color);
}

void osd_draw_connect(Osd *osd, const OsdConnectView *view)
{
    int w = imin(S(osd, 700), osd->out_w - S(osd, 40));
    int h = S(osd, 244);
    int x = (osd->out_w - w) / 2;
    int y = (osd->out_h - h) / 2;
    int pad = S(osd, 34);
    char line[160];
    panel(osd, x, y, w, h, OSD_ACCENT, 1.0f);
    osd_fit(osd, view->title, 32, w - 2 * pad, line, sizeof(line));
    osd_text(osd, line, x + pad, y + S(osd, 26), 32, OSD_TEXT);
    if(view->detail && view->detail[0]) {
        osd_fit(osd, view->detail, 21, w - 2 * pad, line, sizeof(line));
        osd_text(osd, line, x + pad, y + S(osd, 72), 21, OSD_TEXT2);
    }
    char elapsed[32];
    snprintf(elapsed, sizeof(elapsed), "%u s", view->elapsed);
    int elapsed_w = osd_text_width(osd, elapsed, 21);
    unsigned int dots = (view->ticks / 400u) % 4u;
    char status[200];
    textf(status, sizeof(status), "%s%.*s", view->status ? view->status : "", (int)dots, "...");
    osd_fit(osd, status, 25, w - 3 * pad - elapsed_w, line, sizeof(line));
    osd_text(osd, line, x + pad, y + S(osd, 116), 25, OSD_TEXT);
    text_right(osd, elapsed, x + w - pad, y + S(osd, 119), 21, OSD_TEXT3);

    /* A sliding segment says "working" even when nothing else changes. */
    int bar_x = x + pad, bar_w = w - 2 * pad, bar_y = y + S(osd, 168), bar_h = imax(3, S(osd, 6));
    osd_fill(osd, bar_x, bar_y, bar_w, bar_h, bar_h / 2, OSD_TRACK);
    int segment = bar_w / 4;
    int travel = bar_w + segment;
    int head = (int)((view->ticks / 6u) % (unsigned int)imax(1, travel)) - segment;
    int start = imax(0, head), end = imin(bar_w, head + segment);
    if(end > start) osd_fill(osd, bar_x + start, bar_y, end - start, bar_h, bar_h / 2, OSD_ACCENT);

    if(view->footer && view->footer[0]) {
        osd_fit(osd, view->footer, 20, w - 2 * pad, line, sizeof(line));
        osd_text(osd, line, x + pad, y + h - S(osd, 50), 20, OSD_TEXT3);
    }
}

void osd_draw_pin(Osd *osd, const OsdPinView *view)
{
    dim(osd, 150);
    int w = imin(S(osd, 640), osd->out_w - S(osd, 40));
    int h = S(osd, 370);
    int x = (osd->out_w - w) / 2;
    int y = (osd->out_h - h) / 2;
    int pad = S(osd, 34);
    char line[200];
    panel(osd, x, y, w, h, view->wrong ? OSD_DANGER : OSD_ACCENT, 1.0f);
    osd_text(osd, "Login passcode", x + pad, y + S(osd, 24), 32, OSD_TEXT);
    char subtitle[200];
    textf(subtitle, sizeof(subtitle), "%s asks for its 4-digit login passcode", view->name ? view->name : "The console");
    osd_fit(osd, subtitle, 21, w - 2 * pad, line, sizeof(line));
    osd_text(osd, line, x + pad, y + S(osd, 70), 21, OSD_TEXT2);

    int bw = S(osd, 80), bh = S(osd, 100), gap = S(osd, 20);
    int total = 4 * bw + 3 * gap;
    int bx = x + (w - total) / 2;
    int by = y + S(osd, 134);
    int border = imax(2, S(osd, 3));
    for(int i = 0; i < 4; i++) {
        int cx = bx + i * (bw + gap);
        bool focused = i == view->cursor && !view->checking;
        if(focused) osd_fill(osd, cx - border, by - border, bw + 2 * border, bh + 2 * border, S(osd, 16), OSD_ACCENT);
        osd_fill(osd, cx, by, bw, bh, S(osd, 14), OSD_INSET);
        char digit[2] = {view->digits[i] ? view->digits[i] : '0', '\0'};
        int lh = osd_line_height(osd, 54);
        text_center(osd, digit, cx + bw / 2, by + (bh - lh) / 2 + S(osd, 2), 54, OSD_TEXT);
        if(focused) {
            text_center(osd, "^", cx + bw / 2, by - S(osd, 34), 22, OSD_TEXT3);
            text_center(osd, "v", cx + bw / 2, by + bh + S(osd, 2), 22, OSD_TEXT3);
        }
    }
    const char *status = "Up/Down change a digit, Left/Right move";
    SDL_Color status_color = OSD_TEXT3;
    if(view->checking) {
        status = "Checking the passcode...";
        status_color = OSD_TEXT2;
    } else if(view->wrong) {
        status = "Wrong passcode - try again";
        status_color = OSD_DANGER;
    }
    osd_fit(osd, status, 21, w - 2 * pad, line, sizeof(line));
    text_center(osd, line, x + w / 2, by + bh + S(osd, 40), 21, status_color);
    osd_text(osd, "A  enter      B  cancel", x + pad, y + h - S(osd, 48), 20, OSD_TEXT3);
}

void osd_draw_hint(Osd *osd, bool select_create, float alpha)
{
    const char *first = select_create
        ? "Menu: PS button    Select: Create    Menu + Select: Touchpad"
        : "Menu: PS button    Select: Touchpad    Menu + Select: Create";
    const char *second = "Menu + D-pad: swipe    Hold Menu: stream menu    Hold Start + Select: quit";
    float px = 20;
    int lh = osd_line_height(osd, px);
    int pad = S(osd, 24);
    int w = imax(osd_text_width(osd, first, px), osd_text_width(osd, second, px)) + 2 * pad;
    w = imin(w, osd->out_w - S(osd, 24));
    int h = 2 * lh + 2 * S(osd, 14);
    int x = (osd->out_w - w) / 2;
    int y = osd->out_h - h - S(osd, 34);
    char line[200];
    osd_fill(osd, x, y, w, h, S(osd, 16), fade(OSD_PANEL, alpha));
    osd_fit(osd, first, px, w - 2 * pad, line, sizeof(line));
    text_center(osd, line, x + w / 2, y + S(osd, 12), px, fade(OSD_TEXT, alpha));
    osd_fit(osd, second, px, w - 2 * pad, line, sizeof(line));
    text_center(osd, line, x + w / 2, y + S(osd, 12) + lh, px, fade(OSD_TEXT2, alpha));
}

static const char *const controls_rows[][2] = {
    {"B / A / Y / X", "Cross / Circle / Square / Triangle"},
    {"Start", "Options"},
    {"Select", NULL}, /* filled from the select mode */
    {"Menu", "PS button"},
    {"Menu + Select", NULL},
    {"Menu + D-pad", "Swipe on the touchpad"},
    {"Menu + right stick", "Finger on the touchpad"},
    {"Menu + R3", "Touchpad click"},
    {"Menu + L3", NULL},
    {"Hold Menu", "This menu"},
    {"Hold Start + Select", "Quit Remote Play"},
};
#define CONTROLS_ROW_COUNT ((int)(sizeof(controls_rows) / sizeof(controls_rows[0])))

static int controls_height(const Osd *osd)
{
    return CONTROLS_ROW_COUNT * osd_line_height(osd, 20) + S(osd, 16);
}

static void draw_controls_at(Osd *osd, int x, int y, int w, bool select_create, bool dpad_stick)
{
    int lh = osd_line_height(osd, 20);
    int split = x + S(osd, 210);
    char line[160];
    for(int i = 0; i < CONTROLS_ROW_COUNT; i++) {
        const char *what = controls_rows[i][1];
        if(i == 2) what = select_create ? "Create" : "Touchpad click";
        if(i == 4) what = select_create ? "Touchpad click" : "Create";
        if(i == 8) what = dpad_stick ? "D-pad back to D-pad (now: left stick)" : "D-pad as left stick (now: D-pad)";
        osd_text(osd, controls_rows[i][0], x, y + i * lh, 20, OSD_TEXT2);
        osd_fit(osd, what, 20, x + w - split, line, sizeof(line));
        osd_text(osd, line, split, y + i * lh, 20, OSD_TEXT);
    }
}

void osd_draw_controls(Osd *osd, bool select_create, bool dpad_stick)
{
    int w = imin(S(osd, 680), osd->out_w - S(osd, 40));
    int pad = S(osd, 34);
    int h = controls_height(osd) + S(osd, 96);
    int x = (osd->out_w - w) / 2;
    int y = (osd->out_h - h) / 2;
    dim(osd, 150);
    panel(osd, x, y, w, h, OSD_ACCENT, 1.0f);
    osd_text(osd, "Controls", x + pad, y + S(osd, 20), 28, OSD_TEXT);
    draw_controls_at(osd, x + pad, y + S(osd, 70), w - 2 * pad, select_create, dpad_stick);
}

void osd_draw_toast(Osd *osd, const char *text, OsdToastKind kind, float alpha)
{
    if(!text || !text[0] || alpha <= 0.0f) return;
    float px = 22;
    char line[200];
    int max_w = osd->out_w - S(osd, 120);
    osd_fit(osd, text, px, max_w, line, sizeof(line));
    int h = S(osd, 48);
    int w = osd_text_width(osd, line, px) + S(osd, 64);
    int x = (osd->out_w - w) / 2;
    int y = S(osd, 24);
    SDL_Color dot = kind == OSD_TOAST_GOOD ? OSD_GOOD : kind == OSD_TOAST_WARN ? OSD_WARN : OSD_ACCENT;
    int d = S(osd, 10);
    osd_fill(osd, x, y, w, h, h / 2, fade(OSD_PANEL, alpha));
    osd_fill(osd, x + S(osd, 20), y + (h - d) / 2, d, d, d / 2, fade(dot, alpha));
    osd_text(osd, line, x + S(osd, 42), y + (h - osd_line_height(osd, px)) / 2, px, fade(OSD_TEXT, alpha));
}

void osd_draw_quit(Osd *osd, float progress)
{
    const char *text = "Keep holding START + SELECT to quit";
    float px = 22;
    int w = osd_text_width(osd, text, px) + S(osd, 72);
    int h = S(osd, 84);
    int x = (osd->out_w - w) / 2;
    int y = osd->out_h - h - S(osd, 40);
    osd_fill(osd, x, y, w, h, S(osd, 18), OSD_PANEL);
    text_center(osd, text, x + w / 2, y + S(osd, 12), px, OSD_TEXT);
    int bar_x = x + S(osd, 28), bar_w = w - S(osd, 56), bar_h = imax(4, S(osd, 8));
    int bar_y = y + h - S(osd, 26);
    if(progress < 0.0f) progress = 0.0f;
    if(progress > 1.0f) progress = 1.0f;
    osd_fill(osd, bar_x, bar_y, bar_w, bar_h, bar_h / 2, OSD_TRACK);
    osd_fill(osd, bar_x, bar_y, imax(bar_h, iround((float)bar_w * progress)), bar_h, bar_h / 2, OSD_DANGER);
}

void osd_draw_menu(Osd *osd, const OsdMenuView *view)
{
    dim(osd, 150);
    int w = imin(S(osd, 660), osd->out_w - S(osd, 40));
    int pad = S(osd, 32);
    int header = S(osd, 120);
    int footer = S(osd, 56);
    int row_h = S(osd, 50);
    int count = view->count < 0 ? 0 : view->count > OSD_MENU_MAX_ROWS ? OSD_MENU_MAX_ROWS : view->count;
    int body = view->show_controls ? controls_height(osd) : count * row_h;
    int h = header + body + footer;
    int x = (osd->out_w - w) / 2;
    int y = imax(S(osd, 12), (osd->out_h - h) / 2);
    char line[200];
    panel(osd, x, y, w, h, OSD_ACCENT, 1.0f);
    osd_fit(osd, view->title, 30, w - 2 * pad, line, sizeof(line));
    osd_text(osd, line, x + pad, y + S(osd, 20), 30, OSD_TEXT);
    if(view->line1) {
        osd_fit(osd, view->line1, 20, w - 2 * pad, line, sizeof(line));
        osd_text(osd, line, x + pad, y + S(osd, 62), 20, OSD_TEXT2);
    }
    if(view->line2) {
        osd_fit(osd, view->line2, 18, w - 2 * pad, line, sizeof(line));
        osd_text(osd, line, x + pad, y + S(osd, 89), 18, OSD_TEXT3);
    }
    if(view->show_controls) {
        draw_controls_at(osd, x + pad, y + header, w - 2 * pad, view->select_create, view->dpad_stick);
        osd_text(osd, "B  back", x + pad, y + h - S(osd, 42), 18, OSD_TEXT3);
        return;
    }
    for(int i = 0; i < count; i++) {
        const OsdMenuRow *row = &view->rows[i];
        int ry = y + header + i * row_h;
        bool focused = i == view->cursor;
        if(focused) {
            SDL_Color glow = OSD_ACCENT;
            glow.a = 64;
            osd_fill(osd, x + S(osd, 14), ry + S(osd, 3), w - S(osd, 28), row_h - S(osd, 6), S(osd, 12), glow);
            osd_fill(osd, x + S(osd, 14), ry + S(osd, 12), imax(3, S(osd, 4)), row_h - S(osd, 24), 1, OSD_ACCENT);
        }
        int text_y = ry + (row_h - osd_line_height(osd, 23)) / 2;
        int value_w = 0;
        if(row->value[0]) {
            char value[64];
            if(focused) textf(value, sizeof(value), "<  %s  >", row->value);
            else copy_text(value, sizeof(value), row->value);
            value_w = osd_text_width(osd, value, 21);
            text_right(osd, value, x + w - pad, ry + (row_h - osd_line_height(osd, 21)) / 2, 21,
                       focused ? OSD_TEXT : OSD_TEXT2);
        }
        osd_fit(osd, row->label, 23, w - 2 * pad - value_w - S(osd, 16), line, sizeof(line));
        osd_text(osd, line, x + pad, text_y, 23, OSD_TEXT);
    }
    osd_text(osd, "A  select      Left/Right  change      B  resume", x + pad, y + h - S(osd, 42), 18, OSD_TEXT3);
}

void osd_draw_stats(Osd *osd, const char *text)
{
    if(!text || !text[0]) return;
    float px = 18;
    int w = osd_text_width(osd, text, px) + S(osd, 28);
    int h = osd_line_height(osd, px) + S(osd, 10);
    SDL_Color shade = {0, 0, 0, 150};
    osd_fill(osd, S(osd, 12), S(osd, 12), w, h, S(osd, 10), shade);
    osd_text(osd, text, S(osd, 26), S(osd, 17), px, OSD_TEXT2);
}

void osd_draw_error(Osd *osd, const OsdErrorView *view)
{
    dim(osd, 170);
    int w = imin(S(osd, 720), osd->out_w - S(osd, 40));
    int pad = S(osd, 34);
    char lines[5][160];
    int count = wrap(osd, view->message, 22, w - 2 * pad, lines, 5);
    int lh = osd_line_height(osd, 22);
    int h = S(osd, 86) + count * lh + S(osd, 78);
    int x = (osd->out_w - w) / 2;
    int y = (osd->out_h - h) / 2;
    char title[160];
    panel(osd, x, y, w, h, OSD_DANGER, 1.0f);
    osd_fit(osd, view->title, 30, w - 2 * pad, title, sizeof(title));
    osd_text(osd, title, x + pad, y + S(osd, 24), 30, OSD_TEXT);
    for(int i = 0; i < count; i++) osd_text(osd, lines[i], x + pad, y + S(osd, 78) + i * lh, 22, OSD_TEXT2);
    if(view->footer) osd_text(osd, view->footer, x + pad, y + h - S(osd, 48), 20, OSD_TEXT3);
}

void osd_draw_touch(Osd *osd, uint16_t x, uint16_t y, uint16_t max_x, uint16_t max_y)
{
    if(!max_x || !max_y) return;
    int w = S(osd, 180);
    int h = imax(1, w * (int)max_y / (int)max_x);
    /* Top right: the bottom belongs to the hint card and the quit bar, the top
     * centre to toasts, the top left to the stats line. */
    int px = osd->out_w - w - S(osd, 30);
    int py = S(osd, 30);
    int r = S(osd, 9);
    osd_fill(osd, px - S(osd, 8), py - S(osd, 8), w + S(osd, 16), h + S(osd, 16), S(osd, 16), OSD_PANEL);
    osd_fill(osd, px, py, w, h, S(osd, 12), OSD_TRACK);
    int cx = px + (int)((long)x * w / max_x);
    int cy = py + (int)((long)y * h / max_y);
    osd_fill(osd, cx - r, cy - r, 2 * r, 2 * r, r, OSD_ACCENT);
}
