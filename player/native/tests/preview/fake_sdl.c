/* A software stand-in for the SDL renderer calls stream_osd.c makes, so the
 * OSD can be rasterised to an image on a host without SDL. */
#include <SDL2/SDL.h>
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

struct SDL_Renderer { int w, h; float *rgb; Uint8 r, g, b, a; };
struct SDL_Texture { int w, h; Uint32 *px; Uint8 mr, mg, mb, ma; Uint32 format; };

SDL_Renderer *fake_renderer(int w, int h)
{
    SDL_Renderer *r = calloc(1, sizeof(*r));
    r->w = w; r->h = h;
    r->rgb = calloc((size_t)w * h * 3, sizeof(float));
    return r;
}

void fake_background(SDL_Renderer *r, int kind)
{
    for(int y = 0; y < r->h; y++) for(int x = 0; x < r->w; x++) {
        float *p = &r->rgb[((size_t)y * r->w + x) * 3];
        if(kind == 0) { p[0] = p[1] = p[2] = 0; continue; }
        /* a fake game frame: a sky gradient with a few blocks */
        p[0] = 40 + 80.0f * y / r->h; p[1] = 90 + 60.0f * x / r->w; p[2] = 160 - 60.0f * y / r->h;
        if(((x / 160) + (y / 120)) % 3 == 0) { p[0] = 200; p[1] = 170; p[2] = 90; }
    }
}

void fake_save_ppm(SDL_Renderer *r, const char *path)
{
    FILE *f = fopen(path, "wb");
    fprintf(f, "P6\n%d %d\n255\n", r->w, r->h);
    for(size_t i = 0; i < (size_t)r->w * r->h * 3; i++) {
        float v = r->rgb[i]; if(v < 0) v = 0; if(v > 255) v = 255;
        fputc((int)(v + 0.5f), f);
    }
    fclose(f);
}

int SDL_GetRendererOutputSize(SDL_Renderer *renderer, int *w, int *h) { *w = renderer->w; *h = renderer->h; return 0; }
SDL_Texture *SDL_CreateTexture(SDL_Renderer *renderer, Uint32 format, int access, int w, int h)
{
    (void)renderer; (void)format; (void)access;
    SDL_Texture *t = calloc(1, sizeof(*t));
    t->w = w; t->h = h; t->px = calloc((size_t)w * h, 4); t->mr = t->mg = t->mb = t->ma = 255; t->format = format;
    return t;
}
int SDL_UpdateTexture(SDL_Texture *t, const SDL_Rect *rect, const void *pixels, int pitch)
{
    (void)rect; for(int y = 0; y < t->h; y++) memcpy(&t->px[(size_t)y * t->w], (const char *)pixels + (size_t)y * pitch, (size_t)t->w * 4);
    return 0;
}
int SDL_SetTextureBlendMode(SDL_Texture *t, SDL_BlendMode m) { (void)t; (void)m; return 0; }
void SDL_DestroyTexture(SDL_Texture *t) { if(t) { free(t->px); free(t); } }
int SDL_SetTextureColorMod(SDL_Texture *t, Uint8 r, Uint8 g, Uint8 b) { t->mr = r; t->mg = g; t->mb = b; return 0; }
int SDL_SetTextureAlphaMod(SDL_Texture *t, Uint8 a) { t->ma = a; return 0; }
int SDL_SetRenderDrawBlendMode(SDL_Renderer *r, SDL_BlendMode m) { (void)r; (void)m; return 0; }
int SDL_SetRenderDrawColor(SDL_Renderer *r, Uint8 cr, Uint8 cg, Uint8 cb, Uint8 ca) { r->r = cr; r->g = cg; r->b = cb; r->a = ca; return 0; }

static void blend(SDL_Renderer *r, int x, int y, float cr, float cg, float cb, float a)
{
    if(x < 0 || y < 0 || x >= r->w || y >= r->h || a <= 0) return;
    float *p = &r->rgb[((size_t)y * r->w + x) * 3];
    p[0] = p[0] * (1 - a) + cr * a; p[1] = p[1] * (1 - a) + cg * a; p[2] = p[2] * (1 - a) + cb * a;
}

int SDL_RenderFillRect(SDL_Renderer *r, const SDL_Rect *rect)
{
    SDL_Rect full = {0, 0, r->w, r->h};
    if(!rect) rect = &full;
    for(int y = rect->y; y < rect->y + rect->h; y++)
        for(int x = rect->x; x < rect->x + rect->w; x++) blend(r, x, y, r->r, r->g, r->b, r->a / 255.0f);
    return 0;
}
int SDL_RenderFillRects(SDL_Renderer *r, const SDL_Rect *rects, int count)
{
    for(int i = 0; i < count; i++) SDL_RenderFillRect(r, &rects[i]);
    return 0;
}
static float texel_alpha(const SDL_Texture *t, int x, int y)
{
    if(x < 0 || y < 0 || x >= t->w || y >= t->h) return 0;
    return (float)(t->px[(size_t)y * t->w + x] >> 24) / 255.0f;
}
int SDL_RenderCopy(SDL_Renderer *r, SDL_Texture *t, const SDL_Rect *src, const SDL_Rect *dst)
{
    SDL_Rect whole_src = {0, 0, t->w, t->h}, whole_dst = {0, 0, r->w, r->h};
    if(t->format == SDL_PIXELFORMAT_IYUV) return 0; /* video frames: nothing to draw in the fake */
    if(!src) src = &whole_src;
    if(!dst) dst = &whole_dst;
    for(int y = 0; y < dst->h; y++) for(int x = 0; x < dst->w; x++) {
        /* bilinear, like SDL_HINT_RENDER_SCALE_QUALITY=linear */
        float sx = src->x + (x + 0.5f) * src->w / (float)dst->w - 0.5f;
        float sy = src->y + (y + 0.5f) * src->h / (float)dst->h - 0.5f;
        int x0 = (int)floorf(sx), y0 = (int)floorf(sy);
        float fx = sx - x0, fy = sy - y0;
        float a = texel_alpha(t, x0, y0) * (1 - fx) * (1 - fy) + texel_alpha(t, x0 + 1, y0) * fx * (1 - fy) +
                  texel_alpha(t, x0, y0 + 1) * (1 - fx) * fy + texel_alpha(t, x0 + 1, y0 + 1) * fx * fy;
        blend(r, dst->x + x, dst->y + y, t->mr, t->mg, t->mb, a * t->ma / 255.0f);
    }
    return 0;
}
