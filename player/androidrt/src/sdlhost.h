/* sdlhost.h — the slice of the SDL2 ABI the SDL-hosted present backend
 * (eglpresent.c, ANDROIDPAK_PRESENT=sdl) calls, declared header-free like
 * eglhost.h: the TSP SDK sysroot and the host smoke builds have no SDL
 * headers, and the firmware's libSDL2-2.0.so.0 is dlopen'ed at run time —
 * never linked, so a runtime without SDL2 still starts.
 *
 * The values are SDL2's (SDL.h / SDL_video.h, unchanged since 2.0.0); get
 * them wrong and the firmware's SDL builds a different window than asked:
 *   SDL_INIT_VIDEO 0x20; SDL_WINDOW_FULLSCREEN 0x1, _OPENGL 0x2, _SHOWN 0x4,
 *   _FULLSCREEN_DESKTOP 0x1001; SDL_WINDOWPOS_UNDEFINED 0x1FFF0000;
 *   SDL_GLattr counts up from SDL_GL_RED_SIZE = 0 (MAJOR_VERSION is 17,
 *   PROFILE_MASK 21); SDL_GL_CONTEXT_PROFILE_ES 0x4.
 * On SDL's EGL video drivers (mali-fbdev, KMSDRM, ...) an SDL_GLContext is
 * the EGLContext itself; the backend still reads every EGL object back with
 * eglGetCurrent* instead of relying on that.
 */
#ifndef ANDROIDRT_SDLHOST_H
#define ANDROIDRT_SDLHOST_H

#include <stdint.h>

typedef struct ApakSdlWindow ApakSdlWindow; /* SDL_Window, opaque */
typedef void *ApakSdlGLContext;             /* SDL_GLContext       */

typedef struct {
    uint8_t major, minor, patch;
} ApakSdlVersion; /* SDL_version */

#define APAK_SDL_INIT_VIDEO             0x00000020u
#define APAK_SDL_WINDOW_FULLSCREEN      0x00000001u
#define APAK_SDL_WINDOW_OPENGL          0x00000002u
#define APAK_SDL_WINDOW_SHOWN           0x00000004u
#define APAK_SDL_WINDOW_FULLSCREEN_DESKTOP (APAK_SDL_WINDOW_FULLSCREEN | 0x00001000u)
#define APAK_SDL_WINDOWPOS_UNDEFINED    0x1FFF0000

/* SDL_GLattr */
enum {
    APAK_SDL_GL_RED_SIZE = 0,
    APAK_SDL_GL_GREEN_SIZE = 1,
    APAK_SDL_GL_BLUE_SIZE = 2,
    APAK_SDL_GL_ALPHA_SIZE = 3,
    APAK_SDL_GL_BUFFER_SIZE = 4,
    APAK_SDL_GL_DOUBLEBUFFER = 5,
    APAK_SDL_GL_DEPTH_SIZE = 6,
    APAK_SDL_GL_STENCIL_SIZE = 7,
    APAK_SDL_GL_MULTISAMPLEBUFFERS = 13,
    APAK_SDL_GL_MULTISAMPLESAMPLES = 14,
    APAK_SDL_GL_CONTEXT_MAJOR_VERSION = 17,
    APAK_SDL_GL_CONTEXT_MINOR_VERSION = 18,
    APAK_SDL_GL_CONTEXT_PROFILE_MASK = 21,
    APAK_SDL_GL_ATTR_COUNT = 28 /* SDL_GL_FLOATBUFFERS + 1, for the mock */
};
#define APAK_SDL_GL_CONTEXT_PROFILE_ES 0x0004

typedef int (*PFN_SDL_InitSubSystem)(uint32_t flags);
typedef void (*PFN_SDL_QuitSubSystem)(uint32_t flags);
typedef const char *(*PFN_SDL_GetError)(void);
typedef void (*PFN_SDL_GetVersion)(ApakSdlVersion *ver);
typedef const char *(*PFN_SDL_GetCurrentVideoDriver)(void);
typedef int (*PFN_SDL_GL_LoadLibrary)(const char *path);
typedef int (*PFN_SDL_GL_SetAttribute)(int attr, int value);
typedef ApakSdlWindow *(*PFN_SDL_CreateWindow)(const char *title, int x, int y, int w, int h, uint32_t flags);
typedef void (*PFN_SDL_DestroyWindow)(ApakSdlWindow *window);
typedef ApakSdlGLContext (*PFN_SDL_GL_CreateContext)(ApakSdlWindow *window);
typedef int (*PFN_SDL_GL_MakeCurrent)(ApakSdlWindow *window, ApakSdlGLContext context);
typedef void (*PFN_SDL_GL_SwapWindow)(ApakSdlWindow *window); /* void in SDL2 */
typedef void (*PFN_SDL_GL_DeleteContext)(ApakSdlGLContext context);
typedef ApakSdlWindow *(*PFN_SDL_GL_GetCurrentWindow)(void);

/* The resolved library. The first block is required; the rest is optional
 * (diagnostics and safety checks). */
typedef struct ApakSdl {
    void *lib;
    char name[256];
    PFN_SDL_InitSubSystem SDL_InitSubSystem;
    PFN_SDL_GetError SDL_GetError;
    PFN_SDL_GL_SetAttribute SDL_GL_SetAttribute;
    PFN_SDL_CreateWindow SDL_CreateWindow;
    PFN_SDL_GL_CreateContext SDL_GL_CreateContext;
    PFN_SDL_GL_MakeCurrent SDL_GL_MakeCurrent;
    PFN_SDL_GL_SwapWindow SDL_GL_SwapWindow;
    PFN_SDL_GL_DeleteContext SDL_GL_DeleteContext;
    /* optional */
    PFN_SDL_QuitSubSystem SDL_QuitSubSystem;
    PFN_SDL_GetVersion SDL_GetVersion;
    PFN_SDL_GetCurrentVideoDriver SDL_GetCurrentVideoDriver;
    PFN_SDL_GL_LoadLibrary SDL_GL_LoadLibrary;
    PFN_SDL_DestroyWindow SDL_DestroyWindow;
    PFN_SDL_GL_GetCurrentWindow SDL_GL_GetCurrentWindow;
} ApakSdl;

#endif /* ANDROIDRT_SDLHOST_H */
