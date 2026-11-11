#include "stream_osd.h"
#include <stdio.h>
#include <string.h>

SDL_Renderer *fake_renderer(int w, int h);
void fake_background(SDL_Renderer *r, int kind);
void fake_save_ppm(SDL_Renderer *r, const char *path);

static const char *out_dir = ".";

static void shot(int w, int h, const char *name, int screen)
{
    SDL_Renderer *r = fake_renderer(w, h);
    Osd osd;
    fake_background(r, screen == 0 || screen == 1 ? 0 : 1);
    osd_init(&osd, r);
    char path[256];
    switch(screen) {
        case 0: {
            OsdConnectView v = {"Connecting to Juka's PS5", "192.168.1.40 - H.265 - 1280x720", "Starting Remote Play", 7, 1200, "Hold START + SELECT to cancel"};
            osd_draw_connect(&osd, &v);
            break;
        }
        case 1: {
            OsdPinView v; memset(&v, 0, sizeof(v)); v.name = "Juka's PS5"; memcpy(v.digits, "0420", 4); v.cursor = 2; v.wrong = true;
            osd_draw_pin(&osd, &v);
            break;
        }
        case 2:
            osd_draw_hint(&osd, false, 1.0f);
            osd_draw_toast(&osd, "Battery 15% - charge soon", OSD_TOAST_WARN, 1.0f);
            osd_draw_touch(&osd, 1400, 300, 1919, 1079);
            osd_draw_stats(&osd, "29.8 fps - lost 0 - fec 1 - 1280x720 H.265");
            break;
        case 3: {
            OsdMenuView v; memset(&v, 0, sizeof(v));
            v.title = "Juka's PS5"; v.line1 = "Streaming 12:04 - Battery 64%"; v.line2 = "H.265 - 1280x720 - 30 fps - 8000 kb/s";
            const char *labels[] = {"Resume", "Quit Remote Play", "Hold PS (Home)", "Select button", "D-pad works as", "Rumble", "Stream volume", "Stream stats", "Controls"};
            const char *values[] = {"", "", "", "Touchpad", "D-pad", "On", "80%", "Off", ""};
            for(int i = 0; i < 9; i++) { snprintf(v.rows[i].label, 48, "%s", labels[i]); snprintf(v.rows[i].value, 40, "%s", values[i]); }
            v.count = 9; v.cursor = 3;
            osd_draw_menu(&osd, &v);
            break;
        }
        case 4: {
            OsdMenuView v; memset(&v, 0, sizeof(v));
            v.title = "Juka's PS5"; v.line1 = "Streaming 12:04 - Battery 64%"; v.line2 = "H.265 - 1280x720 - 30 fps";
            v.show_controls = true; v.count = 0;
            osd_draw_menu(&osd, &v);
            break;
        }
        case 5: {
            OsdErrorView v = {"Couldn't connect to Juka's PS5",
                "Juka's PS5 did not answer at 192.168.1.40 (Connection refused). Check that it is on, Remote Play is enabled on it, and this handheld is on the same network.",
                "Press any button to go back to JukaHub (7 s)"};
            osd_draw_error(&osd, &v);
            break;
        }
        case 6:
            osd_draw_quit(&osd, 0.6f);
            break;
    }
    snprintf(path, sizeof(path), "%s/%s.ppm", out_dir, name);
    fake_save_ppm(r, path);
}

/* Rasterises every OSD screen with a software stand-in for the SDL renderer
 * (fake_sdl.c) into PPM images, at 1280x720 (Smart Pro) and 1024x768 (Brick):
 *   run-host-tests.sh preview   (writes them to $OUT/preview) */
int main(int argc, char **argv)
{
    if(argc > 1) out_dir = argv[1];
    const char *names[] = {"connect", "pin", "overlay", "menu", "controls", "error", "quit"};
    for(int i = 0; i < 7; i++) {
        char n[64];
        snprintf(n, sizeof(n), "%s-720", names[i]);
        shot(1280, 720, n, i);
    }
    shot(1024, 768, "menu-brick", 3);
    shot(1024, 768, "overlay-brick", 2);
    return 0;
}
