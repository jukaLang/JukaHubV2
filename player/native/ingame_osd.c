/*
 * ingame_osd.c — JukaHub's In-game indicators hook (libjukaosd.so).
 *
 * JukaHub puts this library in LD_PRELOAD for a RetroArch or PPSSPP game when
 * Settings → Games & emulators → In-game indicators is on (ingamehud.go). It
 * wraps the three calls that put a frame on the screen:
 *
 *     eglSwapBuffers       RetroArch's own EGL contexts
 *     SDL_GL_SwapWindow    SDL's GL contexts (PPSSPP, RetroArch's sdl_gl)
 *     SDL_RenderPresent    SDL's 2D renderer (RetroArch's sdl2 driver)
 *
 * and, just before the real call, draws one picture over the finished frame:
 * a card JukaHub drew (volume, brightness, low battery, a notification) and
 * wrote to the file named by JUKAHUB_OSD. Nothing else is hooked, so a game
 * whose renderer uses none of these runs exactly as it would without us.
 *
 * Per frame the hook reads two 32-bit numbers from the mapped file; only when
 * they say a new picture is complete does it copy and check the pixels (an
 * FNV-1a sum written with them), and only while a card is showing does it
 * draw: one textured quad in GLES2, or one SDL_RenderCopy. Every piece of GL
 * state it touches is read first and put back after (program, buffers,
 * vertex arrays 0 and 1, texture unit 0, blending, scissor, depth, stencil,
 * cull, viewport, framebuffer, colour mask, unpack state; on GLES3 also the
 * vertex array object, the unpack buffer and the sampler). It never calls
 * glGetError, so the game's own error state is untouched. Any missing
 * function, a shader that does not compile or a file that is not there turns
 * the drawing off for good; the frame always goes out.
 *
 * The file (little-endian):
 *     0   "JKOSD01\0"
 *     8   seq        odd while JukaHub writes, even when complete
 *     12  width      pixels, <= 640
 *     16  height     pixels, <= 160
 *     20  show_ms    how long the card stays (0: until the next one)
 *     24  anchor     0 = top centre
 *     28  design_h   the screen height the card was drawn for (720)
 *     32  sum        FNV-1a of the pixels
 *     36  seq2       equal to seq once complete
 *     64  pixels     RGBA, straight alpha, top row first
 *
 * Build: cc -shared -fPIC -O2 -fvisibility=hidden -o libjukaosd.so ingame_osd.c -ldl
 * (build-helpers.sh does, in native mode). It links nothing but libc and
 * libdl: EGL, GLES and SDL are looked up at run time in the game's process.
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <fcntl.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#define OSD_EXPORT __attribute__((visibility("default")))

#define OSD_HEADER 64
#define OSD_MAX_W 640
#define OSD_MAX_H 160
#define OSD_FILE_SIZE (OSD_HEADER + OSD_MAX_W * OSD_MAX_H * 4)
#define OSD_FADE_IN_MS 120u
#define OSD_FADE_OUT_MS 250u

/* ── The shared file ───────────────────────────────────────────────────── */

static char *osd_path;
static int osd_off;
static const unsigned char *osd_map;
static unsigned osd_tries;
static uint32_t osd_seen;
static uint32_t osd_w, osd_h, osd_show_ms, osd_design_h;
static uint64_t osd_shown_at;
static int osd_have;
static int osd_dirty_gl, osd_dirty_sdl;
static unsigned char osd_pix[OSD_MAX_W * OSD_MAX_H * 4];
static __thread int osd_in_swap;

__attribute__((constructor)) static void osd_init(void)
{
    const char *p = getenv("JUKAHUB_OSD");
    if (p == NULL || *p == '\0') {
        osd_off = 1;
        return;
    }
    osd_path = strdup(p);
    if (osd_path == NULL)
        osd_off = 1;
}

static uint64_t osd_now_ms(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000u + (uint64_t)ts.tv_nsec / 1000000u;
}

static uint32_t osd_rd32(const unsigned char *p)
{
    return (uint32_t)p[0] | (uint32_t)p[1] << 8 | (uint32_t)p[2] << 16 | (uint32_t)p[3] << 24;
}

static uint32_t osd_fnv1a(const unsigned char *p, size_t n)
{
    uint32_t h = 2166136261u;
    for (size_t i = 0; i < n; i++) {
        h ^= p[i];
        h *= 16777619u;
    }
    return h;
}

/* osd_mapped maps the file the first time it is there, looking again about
 * every two seconds (at 60 frames a second) until then. */
static int osd_mapped(void)
{
    if (osd_map != NULL)
        return 1;
    if (osd_off || (osd_tries++ % 120u) != 0)
        return 0;
    int fd = open(osd_path, O_RDONLY | O_CLOEXEC);
    if (fd < 0)
        return 0;
    struct stat st;
    if (fstat(fd, &st) != 0 || st.st_size < (off_t)OSD_FILE_SIZE) {
        close(fd);
        return 0;
    }
    void *m = mmap(NULL, OSD_FILE_SIZE, PROT_READ, MAP_SHARED, fd, 0);
    close(fd);
    if (m == MAP_FAILED)
        return 0;
    if (memcmp(m, "JKOSD01", 8) != 0) {
        munmap(m, OSD_FILE_SIZE);
        osd_off = 1;
        return 0;
    }
    osd_map = (const unsigned char *)m;
    return 1;
}

/* osd_poll takes a newly completed picture and returns the card's opacity
 * now, 0..255 (0: nothing to draw). */
static int osd_poll(void)
{
    if (!osd_mapped())
        return 0;
    uint32_t seq = osd_rd32(osd_map + 8);
    if (seq != osd_seen && seq != 0 && (seq & 1u) == 0 && seq == osd_rd32(osd_map + 36)) {
        uint32_t w = osd_rd32(osd_map + 12), h = osd_rd32(osd_map + 16);
        if (w == 0 || h == 0 || w > OSD_MAX_W || h > OSD_MAX_H) {
            osd_seen = seq;
        } else {
            size_t n = (size_t)w * h * 4u;
            memcpy(osd_pix, osd_map + OSD_HEADER, n);
            __sync_synchronize();
            if (osd_rd32(osd_map + 8) == seq && osd_fnv1a(osd_pix, n) == osd_rd32(osd_map + 32)) {
                osd_w = w;
                osd_h = h;
                osd_show_ms = osd_rd32(osd_map + 20);
                osd_design_h = osd_rd32(osd_map + 28);
                if (osd_design_h < 100u)
                    osd_design_h = 720u;
                osd_seen = seq;
                osd_shown_at = osd_now_ms();
                osd_have = 1;
                osd_dirty_gl = osd_dirty_sdl = 1;
            }
            /* A sum that does not match is a picture still being written:
             * it is read again on the next frame. */
        }
    }
    if (!osd_have)
        return 0;
    uint64_t t = osd_now_ms() - osd_shown_at;
    if (osd_show_ms != 0 && t >= osd_show_ms) {
        osd_have = 0;
        return 0;
    }
    uint64_t a = 255;
    if (t < OSD_FADE_IN_MS)
        a = 255u * t / OSD_FADE_IN_MS;
    if (osd_show_ms != 0 && osd_show_ms > OSD_FADE_OUT_MS && t > osd_show_ms - OSD_FADE_OUT_MS) {
        uint64_t left = 255u * (osd_show_ms - t) / OSD_FADE_OUT_MS;
        if (left < a)
            a = left;
    }
    return a == 0 ? 1 : (int)a;
}

/* osd_rect places the card on a w×h screen: scaled from its design height,
 * kept inside 92 % of the width, near the top, centred. */
static void osd_rect(int w, int h, float *x, float *y, float *dw, float *dh)
{
    float scale = (float)h / (float)osd_design_h;
    if ((float)osd_w * scale > (float)w * 0.92f)
        scale = (float)w * 0.92f / (float)osd_w;
    *dw = (float)osd_w * scale;
    *dh = (float)osd_h * scale;
    *x = ((float)w - *dw) / 2.0f;
    *y = (float)h * 24.0f / 720.0f;
}

/* ── GLES2 ────────────────────────────────────────────────────────────── */

typedef unsigned int GLenum;
typedef unsigned int GLuint;
typedef int GLint;
typedef int GLsizei;
typedef unsigned char GLboolean;
typedef float GLfloat;
typedef char GLchar;

#define GL_TRIANGLE_STRIP 0x0005
#define GL_CULL_FACE 0x0B44
#define GL_DEPTH_TEST 0x0B71
#define GL_STENCIL_TEST 0x0B90
#define GL_VIEWPORT 0x0BA2
#define GL_BLEND 0x0BE2
#define GL_SCISSOR_TEST 0x0C11
#define GL_COLOR_WRITEMASK 0x0C23
#define GL_UNPACK_ROW_LENGTH 0x0CF2
#define GL_UNPACK_SKIP_ROWS 0x0CF3
#define GL_UNPACK_SKIP_PIXELS 0x0CF4
#define GL_UNPACK_ALIGNMENT 0x0CF5
#define GL_TEXTURE_2D 0x0DE1
#define GL_UNSIGNED_BYTE 0x1401
#define GL_FLOAT 0x1406
#define GL_RGBA 0x1908
#define GL_VERSION 0x1F02
#define GL_LINEAR 0x2601
#define GL_TEXTURE_MAG_FILTER 0x2800
#define GL_TEXTURE_MIN_FILTER 0x2801
#define GL_TEXTURE_WRAP_S 0x2802
#define GL_TEXTURE_WRAP_T 0x2803
#define GL_SRC_ALPHA 0x0302
#define GL_ONE_MINUS_SRC_ALPHA 0x0303
#define GL_ONE 1
#define GL_FUNC_ADD 0x8006
#define GL_BLEND_EQUATION_RGB 0x8009
#define GL_TEXTURE_BINDING_2D 0x8069
#define GL_BLEND_DST_RGB 0x80C8
#define GL_BLEND_SRC_RGB 0x80C9
#define GL_BLEND_DST_ALPHA 0x80CA
#define GL_BLEND_SRC_ALPHA 0x80CB
#define GL_CLAMP_TO_EDGE 0x812F
#define GL_TEXTURE0 0x84C0
#define GL_ACTIVE_TEXTURE 0x84E0
#define GL_VERTEX_ARRAY_BINDING 0x85B5
#define GL_VERTEX_ATTRIB_ARRAY_ENABLED 0x8622
#define GL_VERTEX_ATTRIB_ARRAY_SIZE 0x8623
#define GL_VERTEX_ATTRIB_ARRAY_STRIDE 0x8624
#define GL_VERTEX_ATTRIB_ARRAY_TYPE 0x8625
#define GL_VERTEX_ATTRIB_ARRAY_POINTER 0x8645
#define GL_BLEND_EQUATION_ALPHA 0x883D
#define GL_VERTEX_ATTRIB_ARRAY_NORMALIZED 0x886A
#define GL_ARRAY_BUFFER 0x8892
#define GL_ARRAY_BUFFER_BINDING 0x8894
#define GL_VERTEX_ATTRIB_ARRAY_BUFFER_BINDING 0x889F
#define GL_PIXEL_UNPACK_BUFFER 0x88EC
#define GL_PIXEL_UNPACK_BUFFER_BINDING 0x88EF
#define GL_SAMPLER_BINDING 0x8919
#define GL_FRAGMENT_SHADER 0x8B30
#define GL_VERTEX_SHADER 0x8B31
#define GL_COMPILE_STATUS 0x8B81
#define GL_LINK_STATUS 0x8B82
#define GL_CURRENT_PROGRAM 0x8B8D
#define GL_READ_FRAMEBUFFER 0x8CA8
#define GL_DRAW_FRAMEBUFFER 0x8CA9
#define GL_FRAMEBUFFER_BINDING 0x8CA6
#define GL_READ_FRAMEBUFFER_BINDING 0x8CAA
#define GL_FRAMEBUFFER 0x8D40

static struct {
    int broken;
    void *ctx; /* the context prog and tex belong to */
    int es3;
    GLuint prog, tex;
    GLint u_alpha, u_tex;
    void (*GetIntegerv)(GLenum, GLint *);
    void (*GetBooleanv)(GLenum, GLboolean *);
    GLboolean (*IsEnabled)(GLenum);
    void (*Enable)(GLenum);
    void (*Disable)(GLenum);
    void (*BlendFuncSeparate)(GLenum, GLenum, GLenum, GLenum);
    void (*BlendEquationSeparate)(GLenum, GLenum);
    void (*ColorMask)(GLboolean, GLboolean, GLboolean, GLboolean);
    void (*Viewport)(GLint, GLint, GLsizei, GLsizei);
    void (*UseProgram)(GLuint);
    void (*BindBuffer)(GLenum, GLuint);
    void (*ActiveTexture)(GLenum);
    void (*BindTexture)(GLenum, GLuint);
    void (*BindFramebuffer)(GLenum, GLuint);
    void (*GetVertexAttribiv)(GLuint, GLenum, GLint *);
    void (*GetVertexAttribPointerv)(GLuint, GLenum, void **);
    void (*VertexAttribPointer)(GLuint, GLint, GLenum, GLboolean, GLsizei, const void *);
    void (*EnableVertexAttribArray)(GLuint);
    void (*DisableVertexAttribArray)(GLuint);
    void (*DrawArrays)(GLenum, GLint, GLsizei);
    void (*Uniform1f)(GLint, GLfloat);
    void (*Uniform1i)(GLint, GLint);
    void (*TexImage2D)(GLenum, GLint, GLint, GLsizei, GLsizei, GLint, GLenum, GLenum, const void *);
    void (*TexParameteri)(GLenum, GLenum, GLint);
    void (*PixelStorei)(GLenum, GLint);
    void (*GenTextures)(GLsizei, GLuint *);
    GLuint (*CreateShader)(GLenum);
    void (*ShaderSource)(GLuint, GLsizei, const GLchar *const *, const GLint *);
    void (*CompileShader)(GLuint);
    void (*GetShaderiv)(GLuint, GLenum, GLint *);
    GLuint (*CreateProgram)(void);
    void (*AttachShader)(GLuint, GLuint);
    void (*BindAttribLocation)(GLuint, GLuint, const GLchar *);
    void (*LinkProgram)(GLuint);
    void (*GetProgramiv)(GLuint, GLenum, GLint *);
    GLint (*GetUniformLocation)(GLuint, const GLchar *);
    void (*DeleteShader)(GLuint);
    const unsigned char *(*GetString)(GLenum);
    void (*BindVertexArray)(GLuint);  /* GLES3 */
    void (*BindSampler)(GLuint, GLuint); /* GLES3 */
} gl;

/* osd_proc finds a GL function: in the process, then through the context's
 * loader (EGL's, then SDL's). */
static void *osd_proc(const char *name)
{
    static void *(*egl_gpa)(const char *);
    static void *(*sdl_gpa)(const char *);
    static int looked;
    void *p = dlsym(RTLD_DEFAULT, name);
    if (p != NULL)
        return p;
    if (!looked) {
        looked = 1;
        *(void **)&egl_gpa = dlsym(RTLD_DEFAULT, "eglGetProcAddress");
        *(void **)&sdl_gpa = dlsym(RTLD_DEFAULT, "SDL_GL_GetProcAddress");
    }
    if (egl_gpa != NULL && (p = egl_gpa(name)) != NULL)
        return p;
    if (sdl_gpa != NULL)
        p = sdl_gpa(name);
    return p;
}

#define OSD_GL(fn) (*(void **)&gl.fn = osd_proc("gl" #fn)) == NULL

static int osd_gl_load(void)
{
    if (OSD_GL(GetIntegerv) || OSD_GL(GetBooleanv) || OSD_GL(IsEnabled) || OSD_GL(Enable) || OSD_GL(Disable) ||
        OSD_GL(BlendFuncSeparate) || OSD_GL(BlendEquationSeparate) || OSD_GL(ColorMask) || OSD_GL(Viewport) ||
        OSD_GL(UseProgram) || OSD_GL(BindBuffer) || OSD_GL(ActiveTexture) || OSD_GL(BindTexture) ||
        OSD_GL(BindFramebuffer) || OSD_GL(GetVertexAttribiv) || OSD_GL(GetVertexAttribPointerv) ||
        OSD_GL(VertexAttribPointer) || OSD_GL(EnableVertexAttribArray) || OSD_GL(DisableVertexAttribArray) ||
        OSD_GL(DrawArrays) || OSD_GL(Uniform1f) || OSD_GL(Uniform1i) || OSD_GL(TexImage2D) ||
        OSD_GL(TexParameteri) || OSD_GL(PixelStorei) || OSD_GL(GenTextures) || OSD_GL(CreateShader) ||
        OSD_GL(ShaderSource) || OSD_GL(CompileShader) || OSD_GL(GetShaderiv) || OSD_GL(CreateProgram) ||
        OSD_GL(AttachShader) || OSD_GL(BindAttribLocation) || OSD_GL(LinkProgram) || OSD_GL(GetProgramiv) ||
        OSD_GL(GetUniformLocation) || OSD_GL(DeleteShader) || OSD_GL(GetString))
        return 0;
    const char *v = (const char *)gl.GetString(GL_VERSION);
    gl.es3 = v != NULL && strstr(v, "OpenGL ES 3") != NULL;
    if (gl.es3) {
        *(void **)&gl.BindVertexArray = osd_proc("glBindVertexArray");
        *(void **)&gl.BindSampler = osd_proc("glBindSampler");
        if (gl.BindVertexArray == NULL || gl.BindSampler == NULL)
            gl.es3 = 0;
    }
    return 1;
}

static GLuint osd_gl_shader(GLenum kind, const char *src)
{
    GLuint s = gl.CreateShader(kind);
    GLint ok = 0;
    if (s == 0)
        return 0;
    gl.ShaderSource(s, 1, &src, NULL);
    gl.CompileShader(s);
    gl.GetShaderiv(s, GL_COMPILE_STATUS, &ok);
    if (!ok) {
        gl.DeleteShader(s);
        return 0;
    }
    return s;
}

static const char osd_vs[] =
    "attribute vec2 a_pos;\n"
    "attribute vec2 a_uv;\n"
    "varying vec2 v_uv;\n"
    "void main() { v_uv = a_uv; gl_Position = vec4(a_pos, 0.0, 1.0); }\n";

static const char osd_fs[] =
    "#ifdef GL_ES\nprecision mediump float;\n#endif\n"
    "varying vec2 v_uv;\n"
    "uniform sampler2D u_tex;\n"
    "uniform float u_alpha;\n"
    "void main() { vec4 c = texture2D(u_tex, v_uv); gl_FragColor = vec4(c.rgb, c.a * u_alpha); }\n";

/* osd_gl_ready makes the program and the texture in the current context.
 * Called with the game's state saved and our bindings free to change. */
static int osd_gl_ready(void *ctx)
{
    if (gl.ctx == ctx && gl.prog != 0)
        return 1;
    gl.ctx = ctx;
    gl.prog = gl.tex = 0;
    GLuint vs = osd_gl_shader(GL_VERTEX_SHADER, osd_vs);
    GLuint fs = osd_gl_shader(GL_FRAGMENT_SHADER, osd_fs);
    if (vs == 0 || fs == 0) {
        gl.broken = 1;
        return 0;
    }
    GLuint p = gl.CreateProgram();
    GLint ok = 0;
    gl.AttachShader(p, vs);
    gl.AttachShader(p, fs);
    gl.BindAttribLocation(p, 0, "a_pos");
    gl.BindAttribLocation(p, 1, "a_uv");
    gl.LinkProgram(p);
    gl.DeleteShader(vs);
    gl.DeleteShader(fs);
    gl.GetProgramiv(p, GL_LINK_STATUS, &ok);
    if (!ok) {
        gl.broken = 1;
        return 0;
    }
    gl.prog = p;
    gl.u_alpha = gl.GetUniformLocation(p, "u_alpha");
    gl.u_tex = gl.GetUniformLocation(p, "u_tex");
    gl.GenTextures(1, &gl.tex);
    osd_dirty_gl = 1;
    return gl.tex != 0;
}

/* The game's state the quad changes. */
struct osd_attrib {
    GLint enabled, size, type, normalized, stride, buffer;
    void *pointer;
};

struct osd_gl_state {
    GLint program, array_buffer, active_texture, texture, fbo, read_fbo, viewport[4];
    GLint blend_src_rgb, blend_dst_rgb, blend_src_a, blend_dst_a, blend_eq_rgb, blend_eq_a;
    GLboolean blend, scissor, depth, stencil, cull, mask[4];
    GLint unpack_alignment, unpack_row_length, unpack_skip_rows, unpack_skip_pixels, unpack_buffer;
    GLint vao, sampler;
    struct osd_attrib attrib[2];
};

static void osd_gl_save(struct osd_gl_state *s)
{
    gl.GetIntegerv(GL_CURRENT_PROGRAM, &s->program);
    gl.GetIntegerv(GL_ARRAY_BUFFER_BINDING, &s->array_buffer);
    gl.GetIntegerv(GL_ACTIVE_TEXTURE, &s->active_texture);
    gl.ActiveTexture(GL_TEXTURE0);
    gl.GetIntegerv(GL_TEXTURE_BINDING_2D, &s->texture);
    gl.GetIntegerv(GL_FRAMEBUFFER_BINDING, &s->fbo);
    gl.GetIntegerv(GL_VIEWPORT, s->viewport);
    gl.GetIntegerv(GL_BLEND_SRC_RGB, &s->blend_src_rgb);
    gl.GetIntegerv(GL_BLEND_DST_RGB, &s->blend_dst_rgb);
    gl.GetIntegerv(GL_BLEND_SRC_ALPHA, &s->blend_src_a);
    gl.GetIntegerv(GL_BLEND_DST_ALPHA, &s->blend_dst_a);
    gl.GetIntegerv(GL_BLEND_EQUATION_RGB, &s->blend_eq_rgb);
    gl.GetIntegerv(GL_BLEND_EQUATION_ALPHA, &s->blend_eq_a);
    s->blend = gl.IsEnabled(GL_BLEND);
    s->scissor = gl.IsEnabled(GL_SCISSOR_TEST);
    s->depth = gl.IsEnabled(GL_DEPTH_TEST);
    s->stencil = gl.IsEnabled(GL_STENCIL_TEST);
    s->cull = gl.IsEnabled(GL_CULL_FACE);
    gl.GetBooleanv(GL_COLOR_WRITEMASK, s->mask);
    gl.GetIntegerv(GL_UNPACK_ALIGNMENT, &s->unpack_alignment);
    if (gl.es3) {
        gl.GetIntegerv(GL_VERTEX_ARRAY_BINDING, &s->vao);
        gl.BindVertexArray(0);
        gl.GetIntegerv(GL_READ_FRAMEBUFFER_BINDING, &s->read_fbo);
        gl.GetIntegerv(GL_PIXEL_UNPACK_BUFFER_BINDING, &s->unpack_buffer);
        gl.GetIntegerv(GL_UNPACK_ROW_LENGTH, &s->unpack_row_length);
        gl.GetIntegerv(GL_UNPACK_SKIP_ROWS, &s->unpack_skip_rows);
        gl.GetIntegerv(GL_UNPACK_SKIP_PIXELS, &s->unpack_skip_pixels);
        gl.GetIntegerv(GL_SAMPLER_BINDING, &s->sampler);
    }
    /* After the vertex array object is unbound: these are the default
     * array's, the ones the quad uses. */
    for (GLuint i = 0; i < 2; i++) {
        struct osd_attrib *a = &s->attrib[i];
        gl.GetVertexAttribiv(i, GL_VERTEX_ATTRIB_ARRAY_ENABLED, &a->enabled);
        gl.GetVertexAttribiv(i, GL_VERTEX_ATTRIB_ARRAY_SIZE, &a->size);
        gl.GetVertexAttribiv(i, GL_VERTEX_ATTRIB_ARRAY_TYPE, &a->type);
        gl.GetVertexAttribiv(i, GL_VERTEX_ATTRIB_ARRAY_NORMALIZED, &a->normalized);
        gl.GetVertexAttribiv(i, GL_VERTEX_ATTRIB_ARRAY_STRIDE, &a->stride);
        gl.GetVertexAttribiv(i, GL_VERTEX_ATTRIB_ARRAY_BUFFER_BINDING, &a->buffer);
        a->pointer = NULL;
        gl.GetVertexAttribPointerv(i, GL_VERTEX_ATTRIB_ARRAY_POINTER, &a->pointer);
    }
}

static void osd_gl_toggle(GLenum cap, GLboolean on)
{
    if (on)
        gl.Enable(cap);
    else
        gl.Disable(cap);
}

static void osd_gl_restore(const struct osd_gl_state *s)
{
    for (GLuint i = 0; i < 2; i++) {
        const struct osd_attrib *a = &s->attrib[i];
        gl.BindBuffer(GL_ARRAY_BUFFER, (GLuint)a->buffer);
        gl.VertexAttribPointer(i, a->size, (GLenum)a->type, (GLboolean)a->normalized, a->stride, a->pointer);
        if (a->enabled)
            gl.EnableVertexAttribArray(i);
        else
            gl.DisableVertexAttribArray(i);
    }
    gl.BindBuffer(GL_ARRAY_BUFFER, (GLuint)s->array_buffer);
    gl.UseProgram((GLuint)s->program);
    gl.ActiveTexture(GL_TEXTURE0);
    gl.BindTexture(GL_TEXTURE_2D, (GLuint)s->texture);
    gl.PixelStorei(GL_UNPACK_ALIGNMENT, s->unpack_alignment);
    if (gl.es3) {
        gl.BindSampler(0, (GLuint)s->sampler);
        gl.PixelStorei(GL_UNPACK_ROW_LENGTH, s->unpack_row_length);
        gl.PixelStorei(GL_UNPACK_SKIP_ROWS, s->unpack_skip_rows);
        gl.PixelStorei(GL_UNPACK_SKIP_PIXELS, s->unpack_skip_pixels);
        gl.BindBuffer(GL_PIXEL_UNPACK_BUFFER, (GLuint)s->unpack_buffer);
        gl.BindFramebuffer(GL_DRAW_FRAMEBUFFER, (GLuint)s->fbo);
        gl.BindFramebuffer(GL_READ_FRAMEBUFFER, (GLuint)s->read_fbo);
        gl.BindVertexArray((GLuint)s->vao);
    } else {
        gl.BindFramebuffer(GL_FRAMEBUFFER, (GLuint)s->fbo);
    }
    gl.ActiveTexture((GLenum)s->active_texture);
    gl.Viewport(s->viewport[0], s->viewport[1], s->viewport[2], s->viewport[3]);
    gl.BlendFuncSeparate((GLenum)s->blend_src_rgb, (GLenum)s->blend_dst_rgb, (GLenum)s->blend_src_a, (GLenum)s->blend_dst_a);
    gl.BlendEquationSeparate((GLenum)s->blend_eq_rgb, (GLenum)s->blend_eq_a);
    osd_gl_toggle(GL_BLEND, s->blend);
    osd_gl_toggle(GL_SCISSOR_TEST, s->scissor);
    osd_gl_toggle(GL_DEPTH_TEST, s->depth);
    osd_gl_toggle(GL_STENCIL_TEST, s->stencil);
    osd_gl_toggle(GL_CULL_FACE, s->cull);
    gl.ColorMask(s->mask[0], s->mask[1], s->mask[2], s->mask[3]);
}

/* osd_gl_frame draws the card over the default framebuffer, w×h pixels. */
static void osd_gl_frame(void *ctx, int w, int h, int alpha)
{
    static int loaded;
    if (gl.broken || ctx == NULL || w <= 0 || h <= 0)
        return;
    if (!loaded) {
        loaded = 1;
        if (!osd_gl_load()) {
            gl.broken = 1;
            return;
        }
    }
    struct osd_gl_state s;
    memset(&s, 0, sizeof s);
    osd_gl_save(&s);
    if (osd_gl_ready(ctx)) {
        if (gl.es3) {
            gl.BindSampler(0, 0);
            gl.BindBuffer(GL_PIXEL_UNPACK_BUFFER, 0);
            gl.PixelStorei(GL_UNPACK_ROW_LENGTH, 0);
            gl.PixelStorei(GL_UNPACK_SKIP_ROWS, 0);
            gl.PixelStorei(GL_UNPACK_SKIP_PIXELS, 0);
        }
        gl.BindFramebuffer(GL_FRAMEBUFFER, 0);
        gl.BindTexture(GL_TEXTURE_2D, gl.tex);
        if (osd_dirty_gl) {
            gl.PixelStorei(GL_UNPACK_ALIGNMENT, 4);
            gl.TexImage2D(GL_TEXTURE_2D, 0, GL_RGBA, (GLsizei)osd_w, (GLsizei)osd_h, 0, GL_RGBA, GL_UNSIGNED_BYTE, osd_pix);
            gl.TexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_LINEAR);
            gl.TexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_LINEAR);
            gl.TexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_S, GL_CLAMP_TO_EDGE);
            gl.TexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_T, GL_CLAMP_TO_EDGE);
            osd_dirty_gl = 0;
        }
        float x, y, dw, dh;
        osd_rect(w, h, &x, &y, &dw, &dh);
        float x0 = 2.0f * x / (float)w - 1.0f, x1 = 2.0f * (x + dw) / (float)w - 1.0f;
        float y0 = 1.0f - 2.0f * y / (float)h, y1 = 1.0f - 2.0f * (y + dh) / (float)h;
        const GLfloat quad[16] = {
            x0, y0, 0.0f, 0.0f,
            x1, y0, 1.0f, 0.0f,
            x0, y1, 0.0f, 1.0f,
            x1, y1, 1.0f, 1.0f,
        };
        gl.Viewport(0, 0, w, h);
        gl.Disable(GL_SCISSOR_TEST);
        gl.Disable(GL_DEPTH_TEST);
        gl.Disable(GL_STENCIL_TEST);
        gl.Disable(GL_CULL_FACE);
        gl.ColorMask(1, 1, 1, 1);
        gl.Enable(GL_BLEND);
        gl.BlendFuncSeparate(GL_SRC_ALPHA, GL_ONE_MINUS_SRC_ALPHA, GL_ONE, GL_ONE_MINUS_SRC_ALPHA);
        gl.BlendEquationSeparate(GL_FUNC_ADD, GL_FUNC_ADD);
        gl.UseProgram(gl.prog);
        gl.Uniform1i(gl.u_tex, 0);
        gl.Uniform1f(gl.u_alpha, (GLfloat)alpha / 255.0f);
        gl.BindBuffer(GL_ARRAY_BUFFER, 0);
        gl.VertexAttribPointer(0, 2, GL_FLOAT, 0, 4 * sizeof(GLfloat), quad);
        gl.VertexAttribPointer(1, 2, GL_FLOAT, 0, 4 * sizeof(GLfloat), quad + 2);
        gl.EnableVertexAttribArray(0);
        gl.EnableVertexAttribArray(1);
        gl.DrawArrays(GL_TRIANGLE_STRIP, 0, 4);
    }
    osd_gl_restore(&s);
}

/* ── The hooks ────────────────────────────────────────────────────────── */

/* osd_next finds the real function behind ours. */
static void *osd_next(const char *name, const char *lib)
{
    void *p = dlsym(RTLD_NEXT, name);
    if (p == NULL && lib != NULL) {
        void *h = dlopen(lib, RTLD_NOW | RTLD_NOLOAD);
        if (h != NULL)
            p = dlsym(h, name);
    }
    return p;
}

typedef void *EGLDisplay;
typedef void *EGLSurface;
typedef unsigned int EGLBoolean;
typedef int EGLint;
#define EGL_HEIGHT 0x3056
#define EGL_WIDTH 0x3057

OSD_EXPORT EGLBoolean eglSwapBuffers(EGLDisplay dpy, EGLSurface surface)
{
    static EGLBoolean (*real)(EGLDisplay, EGLSurface);
    static EGLBoolean (*query)(EGLDisplay, EGLSurface, EGLint, EGLint *);
    static void *(*current)(void);
    if (real == NULL) {
        *(void **)&real = osd_next("eglSwapBuffers", "libEGL.so.1");
        *(void **)&query = osd_next("eglQuerySurface", "libEGL.so.1");
        *(void **)&current = osd_next("eglGetCurrentContext", "libEGL.so.1");
        if (real == NULL)
            return 0;
    }
    if (!osd_in_swap && query != NULL && current != NULL) {
        int a = osd_poll();
        if (a > 0) {
            EGLint w = 0, h = 0;
            if (query(dpy, surface, EGL_WIDTH, &w) && query(dpy, surface, EGL_HEIGHT, &h))
                osd_gl_frame(current(), w, h, a);
        }
    }
    osd_in_swap++;
    EGLBoolean r = real(dpy, surface);
    osd_in_swap--;
    return r;
}

OSD_EXPORT void SDL_GL_SwapWindow(void *window)
{
    static void (*real)(void *);
    static void (*drawable)(void *, int *, int *);
    static void *(*current)(void);
    if (real == NULL) {
        *(void **)&real = osd_next("SDL_GL_SwapWindow", "libSDL2-2.0.so.0");
        *(void **)&drawable = osd_next("SDL_GL_GetDrawableSize", "libSDL2-2.0.so.0");
        *(void **)&current = osd_next("SDL_GL_GetCurrentContext", "libSDL2-2.0.so.0");
        if (real == NULL)
            return;
    }
    if (!osd_in_swap && drawable != NULL && current != NULL) {
        int a = osd_poll();
        if (a > 0) {
            int w = 0, h = 0;
            drawable(window, &w, &h);
            osd_gl_frame(current(), w, h, a);
        }
    }
    osd_in_swap++;
    real(window);
    osd_in_swap--;
}

/* SDL's 2D renderer: one streaming texture per renderer. */
typedef struct {
    int x, y, w, h;
} osd_sdl_rect;

#define OSD_SDL_ABGR8888 0x16762004u /* RGBA bytes on a little-endian CPU */
#define OSD_SDL_TEXTUREACCESS_STREAMING 1
#define OSD_SDL_BLENDMODE_BLEND 1

static struct {
    int broken, loaded;
    void *renderer, *tex;
    uint32_t tex_w, tex_h;
    void *(*CreateTexture)(void *, uint32_t, int, int, int);
    void (*DestroyTexture)(void *);
    int (*UpdateTexture)(void *, const osd_sdl_rect *, const void *, int);
    int (*SetTextureBlendMode)(void *, int);
    int (*SetTextureAlphaMod)(void *, uint8_t);
    int (*RenderCopy)(void *, void *, const osd_sdl_rect *, const osd_sdl_rect *);
    void *(*GetRenderTarget)(void *);
    int (*SetRenderTarget)(void *, void *);
    int (*GetRendererOutputSize)(void *, int *, int *);
    void (*RenderGetLogicalSize)(void *, int *, int *);
    void (*RenderGetViewport)(void *, osd_sdl_rect *);
    int (*RenderSetViewport)(void *, const osd_sdl_rect *);
    void (*RenderGetClipRect)(void *, osd_sdl_rect *);
    int (*RenderSetClipRect)(void *, const osd_sdl_rect *);
    int (*RenderIsClipEnabled)(void *);
} sdl;

#define OSD_SDL(fn) (*(void **)&sdl.fn = osd_next("SDL_" #fn, "libSDL2-2.0.so.0")) == NULL

static void osd_sdl_frame(void *r, int alpha)
{
    if (sdl.broken || r == NULL)
        return;
    if (!sdl.loaded) {
        sdl.loaded = 1;
        if (OSD_SDL(CreateTexture) || OSD_SDL(DestroyTexture) || OSD_SDL(UpdateTexture) ||
            OSD_SDL(SetTextureBlendMode) || OSD_SDL(SetTextureAlphaMod) || OSD_SDL(RenderCopy) ||
            OSD_SDL(GetRenderTarget) || OSD_SDL(SetRenderTarget) || OSD_SDL(GetRendererOutputSize) ||
            OSD_SDL(RenderGetLogicalSize) || OSD_SDL(RenderGetViewport) || OSD_SDL(RenderSetViewport) ||
            OSD_SDL(RenderGetClipRect) || OSD_SDL(RenderSetClipRect) || OSD_SDL(RenderIsClipEnabled)) {
            sdl.broken = 1;
            return;
        }
    }
    if (sdl.renderer != r || sdl.tex_w != osd_w || sdl.tex_h != osd_h) {
        if (sdl.tex != NULL && sdl.renderer == r)
            sdl.DestroyTexture(sdl.tex);
        sdl.tex = sdl.CreateTexture(r, OSD_SDL_ABGR8888, OSD_SDL_TEXTUREACCESS_STREAMING, (int)osd_w, (int)osd_h);
        sdl.renderer = r;
        sdl.tex_w = osd_w;
        sdl.tex_h = osd_h;
        osd_dirty_sdl = 1;
        if (sdl.tex == NULL)
            return;
        sdl.SetTextureBlendMode(sdl.tex, OSD_SDL_BLENDMODE_BLEND);
    }
    if (osd_dirty_sdl) {
        if (sdl.UpdateTexture(sdl.tex, NULL, osd_pix, (int)osd_w * 4) != 0)
            return;
        osd_dirty_sdl = 0;
    }
    void *target = sdl.GetRenderTarget(r);
    if (target != NULL)
        sdl.SetRenderTarget(r, NULL);
    osd_sdl_rect vp, clip;
    sdl.RenderGetViewport(r, &vp);
    int clipped = sdl.RenderIsClipEnabled(r);
    sdl.RenderGetClipRect(r, &clip);
    sdl.RenderSetViewport(r, NULL);
    sdl.RenderSetClipRect(r, NULL);
    int w = 0, h = 0;
    sdl.RenderGetLogicalSize(r, &w, &h);
    if (w <= 0 || h <= 0)
        sdl.GetRendererOutputSize(r, &w, &h);
    if (w > 0 && h > 0) {
        float x, y, dw, dh;
        osd_rect(w, h, &x, &y, &dw, &dh);
        osd_sdl_rect dst = {(int)x, (int)y, (int)dw, (int)dh};
        sdl.SetTextureAlphaMod(sdl.tex, (uint8_t)alpha);
        sdl.RenderCopy(r, sdl.tex, NULL, &dst);
    }
    sdl.RenderSetViewport(r, &vp);
    sdl.RenderSetClipRect(r, clipped ? &clip : NULL);
    if (target != NULL)
        sdl.SetRenderTarget(r, target);
}

OSD_EXPORT void SDL_RenderPresent(void *renderer)
{
    static void (*real)(void *);
    if (real == NULL) {
        *(void **)&real = osd_next("SDL_RenderPresent", "libSDL2-2.0.so.0");
        if (real == NULL)
            return;
    }
    if (!osd_in_swap) {
        int a = osd_poll();
        if (a > 0)
            osd_sdl_frame(renderer, a);
    }
    osd_in_swap++;
    real(renderer);
    osd_in_swap--;
}

/* A renderer going away takes its textures with it: ours is forgotten, so a
 * new renderer at the same address never gets a stale pointer. */
OSD_EXPORT void SDL_DestroyRenderer(void *renderer)
{
    static void (*real)(void *);
    if (real == NULL) {
        *(void **)&real = osd_next("SDL_DestroyRenderer", "libSDL2-2.0.so.0");
        if (real == NULL)
            return;
    }
    if (renderer != NULL && renderer == sdl.renderer) {
        sdl.renderer = NULL;
        sdl.tex = NULL;
    }
    real(renderer);
}
