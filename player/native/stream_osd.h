/* stream_osd.h - chiaki-stream's on-screen display.
 *
 * Everything is drawn with the SDL renderer the video already uses: rounded
 * panels from filled rects, text from one glyph atlas (osd_font.h, Inter
 * Regular). Sizes are given for a 720-line screen and scaled to the real
 * output, so the Brick's 1024x768 panel gets the same layout.
 *
 * The screens are pure drawing: chiaki-stream.c owns what is open and when,
 * builds a view struct and calls the matching osd_draw_* after copying the
 * last video frame. */
#ifndef JUKAHUB_STREAM_OSD_H
#define JUKAHUB_STREAM_OSD_H

#include <SDL2/SDL.h>
#include <stdbool.h>
#include <stdint.h>

#define OSD_MENU_MAX_ROWS 10

typedef struct {
    SDL_Renderer *renderer;
    SDL_Texture *atlas;
    int out_w;
    int out_h;
    float ui; /* out_h / 720 */
} Osd;

bool osd_init(Osd *osd, SDL_Renderer *renderer);
void osd_fini(Osd *osd);
/* Re-reads the renderer's output size. */
void osd_resize(Osd *osd);

/* Primitives; px is the font size for a 720-line screen. */
int osd_text_width(const Osd *osd, const char *text, float px);
int osd_line_height(const Osd *osd, float px);
void osd_text(Osd *osd, const char *text, int x, int y, float px, SDL_Color color);
void osd_fill(Osd *osd, int x, int y, int w, int h, int radius, SDL_Color color);
/* Copies text into out, cut with "..." so it fits max_w. */
void osd_fit(const Osd *osd, const char *text, float px, int max_w, char *out, size_t out_size);

typedef struct {
    const char *title;    /* "Connecting to Juka's PS5" */
    const char *detail;   /* the address being used */
    const char *status;   /* "Starting Remote Play" */
    unsigned int elapsed; /* seconds */
    uint32_t ticks;       /* drives the dots */
    const char *footer;   /* "Hold START + SELECT to cancel" */
} OsdConnectView;
void osd_draw_connect(Osd *osd, const OsdConnectView *view);

typedef struct {
    const char *name;
    char digits[4];
    int cursor;
    bool wrong;
    bool checking; /* sent, waiting for the console */
} OsdPinView;
void osd_draw_pin(Osd *osd, const OsdPinView *view);

/* The two-line hint shown when the video starts; alpha 0..1. */
void osd_draw_hint(Osd *osd, bool select_create, float alpha);
/* The full table, inside the stream menu's panel. */
void osd_draw_controls(Osd *osd, bool select_create, bool dpad_stick);

typedef enum {
    OSD_TOAST_INFO = 0,
    OSD_TOAST_GOOD,
    OSD_TOAST_WARN
} OsdToastKind;
void osd_draw_toast(Osd *osd, const char *text, OsdToastKind kind, float alpha);
void osd_draw_quit(Osd *osd, float progress);

typedef struct {
    char label[48];
    char value[40]; /* "" for an action row */
} OsdMenuRow;

typedef struct {
    const char *title;    /* console name */
    const char *line1;    /* "Connected 12:04 - Battery 64%" */
    const char *line2;    /* "H.265 - 1280x720 - 30 fps" */
    OsdMenuRow rows[OSD_MENU_MAX_ROWS];
    int count;
    int cursor;
    bool show_controls;
    bool select_create;
    bool dpad_stick;
} OsdMenuView;
void osd_draw_menu(Osd *osd, const OsdMenuView *view);

void osd_draw_stats(Osd *osd, const char *line);

typedef struct {
    const char *title;
    const char *message;
    const char *footer;
} OsdErrorView;
void osd_draw_error(Osd *osd, const OsdErrorView *view);

/* A small touchpad with the synthetic finger on it. */
void osd_draw_touch(Osd *osd, uint16_t x, uint16_t y, uint16_t max_x, uint16_t max_y);

#endif
