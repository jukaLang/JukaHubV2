/* No-op drawing for the end-to-end run (the previews rasterise; this only counts). */
#include <SDL2/SDL.h>
#include <stdlib.h>
struct SDL_Renderer { int w, h; };
struct SDL_Texture { int w, h; };
static long draw_calls;
static int video_textures, video_textures_alive, video_w, video_h;
long fake_draw_calls(void) { return draw_calls; }
/* Video textures (IYUV): how many were made, how many are alive, the last size. */
int fake_video_textures(void) { return video_textures; }
int fake_video_textures_alive(void) { return video_textures_alive; }
int fake_video_w(void) { return video_w; }
int fake_video_h(void) { return video_h; }
void fake_draw_reset(void) { draw_calls = 0; video_textures = video_textures_alive = video_w = video_h = 0; }
SDL_Renderer *fake_renderer(int w, int h) { static struct SDL_Renderer r; r.w = w; r.h = h; return &r; }
int SDL_GetRendererOutputSize(SDL_Renderer *r, int *w, int *h) { *w = r->w; *h = r->h; return 0; }
SDL_Texture *SDL_CreateTexture(SDL_Renderer *r, Uint32 f, int a, int w, int h)
{
    (void)r; (void)a;
    SDL_Texture *t = calloc(1, sizeof(*t));
    t->w = w;
    t->h = h;
    if(f == SDL_PIXELFORMAT_IYUV) {
        video_textures++;
        video_textures_alive++;
        video_w = w;
        video_h = h;
        t->w = -w; /* marks it as a video texture for the destroy count */
    }
    return t;
}
int SDL_UpdateTexture(SDL_Texture *t, const SDL_Rect *r, const void *p, int pitch) { (void)t; (void)r; (void)p; (void)pitch; return 0; }
int SDL_SetTextureBlendMode(SDL_Texture *t, SDL_BlendMode m) { (void)t; (void)m; return 0; }
void SDL_DestroyTexture(SDL_Texture *t) { if(t && t->w < 0) video_textures_alive--; free(t); }
int SDL_SetTextureColorMod(SDL_Texture *t, Uint8 r, Uint8 g, Uint8 b) { (void)t; (void)r; (void)g; (void)b; return 0; }
int SDL_SetTextureAlphaMod(SDL_Texture *t, Uint8 a) { (void)t; (void)a; return 0; }
int SDL_SetRenderDrawBlendMode(SDL_Renderer *r, SDL_BlendMode m) { (void)r; (void)m; return 0; }
int SDL_SetRenderDrawColor(SDL_Renderer *r, Uint8 a, Uint8 b, Uint8 c, Uint8 d) { (void)r; (void)a; (void)b; (void)c; (void)d; return 0; }
int SDL_RenderFillRect(SDL_Renderer *r, const SDL_Rect *rect) { (void)r; (void)rect; draw_calls++; return 0; }
int SDL_RenderFillRects(SDL_Renderer *r, const SDL_Rect *rects, int n) { (void)r; (void)rects; draw_calls += n; return 0; }
int SDL_RenderCopy(SDL_Renderer *r, SDL_Texture *t, const SDL_Rect *s, const SDL_Rect *d) { (void)r; (void)t; (void)s; (void)d; draw_calls++; return 0; }
