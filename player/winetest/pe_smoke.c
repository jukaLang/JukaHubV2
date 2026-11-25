/*
 * pe_smoke.c — run winetest.exe without Windows or Wine (mkwinetest.py --check).
 *
 * It does what a loader does — maps the headers and sections, applies the
 * relocation blocks for a base other than the program's own, resolves every
 * import by DLL and name — and then calls the entry point. The imports are
 * stand-ins for user32, gdi32, kernel32, ntdll and winmm that check what they
 * are given against the Windows structure layouts (raw offsets, written here
 * independently of winetest.c's declarations), keep track of every GDI object
 * and which device context holds it, and drive a scripted session: the
 * window's creation and size, paints, timer ticks, pointer moves, clicks, the
 * wheel, keys, and one of three ways out (Escape, the Close button, Alt+F4 on
 * a 1024 x 768 screen).
 *
 * x86-64 Linux only (the program's machine code runs natively; the stand-ins
 * are compiled with the Microsoft calling convention). Exit status 0 means
 * every session ended cleanly with every check met.
 */
#define _GNU_SOURCE
#include <setjmp.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>

#define MS __attribute__((ms_abi))

typedef uint16_t wchar16;

static int failures;
#define FAIL(...)                                                                                                      \
	do {                                                                                                           \
		fprintf(stderr, "pe_smoke: FAIL: " __VA_ARGS__);                                                       \
		fputc('\n', stderr);                                                                                   \
		failures++;                                                                                            \
	} while (0)

/* ── Session state ──────────────────────────────────────────────────────── */

enum { MODE_ESC, MODE_CLICK, MODE_ALTF4 };
static const char *mode_names[] = {"Escape", "Close button", "Alt+F4 at 1024x768"};

static int mode, screen_w, screen_h;
static jmp_buf exit_jump;
static unsigned exit_code;
static uint32_t clock_ms;
static uint8_t *image;
static size_t image_len;

typedef int64_t(MS *wndproc_t)(void *, uint32_t, uint64_t, int64_t);
static wndproc_t wndproc;
static const wchar16 *class_name;
static void *const hwnd_main = (void *)0x4242;
static int window_alive, destroyed, quit_posted, quit_code, timer_on;
static int inv, inv_l, inv_t, inv_r, inv_b;
static int paints, timers_sent, step;
static int saw_title, saw_close_label, chime_calls;
static char out_lines[4096];
static size_t out_len;

/* ── GDI objects ────────────────────────────────────────────────────────── */

enum { K_NONE, K_DC, K_BITMAP, K_BRUSH, K_PEN, K_FONT };
#define MAX_OBJ 4096
static struct {
	int kind, alive, stock;
	uintptr_t sel[6]; /* for a DC: what it holds, by kind */
} objs[MAX_OBJ];
static int nobj, live, max_live;
static const uintptr_t SCREEN_DC = 0x9999;

static uintptr_t new_obj(int kind, int stock)
{
	if (nobj >= MAX_OBJ) {
		FAIL("more than %d GDI objects were made in one session", MAX_OBJ);
		exit(1);
	}
	int i = nobj++;
	objs[i].kind = kind;
	objs[i].alive = 1;
	objs[i].stock = stock;
	for (int k = 0; k < 6; k++)
		objs[i].sel[k] = 0;
	if (!stock) {
		live++;
		if (live > max_live)
			max_live = live;
	}
	return 0x10000 + (uintptr_t)i;
}

static int obj_index(uintptr_t h)
{
	if (h < 0x10000 || h >= 0x10000 + (uintptr_t)nobj)
		return -1;
	return (int)(h - 0x10000);
}

static int dc_ok(uintptr_t h, const char *who)
{
	if (h == SCREEN_DC)
		return 1;
	int i = obj_index(h);
	if (i < 0 || objs[i].kind != K_DC || !objs[i].alive) {
		FAIL("%s got %#lx, which is not a live device context", who, (unsigned long)h);
		return 0;
	}
	return 1;
}

static uintptr_t screen_sel[6];
static uintptr_t *sel_of(uintptr_t dc)
{
	if (dc == SCREEN_DC)
		return screen_sel;
	return objs[obj_index(dc)].sel;
}

static uintptr_t stock_default[6];

static void reset_gdi(void)
{
	nobj = live = max_live = 0;
	memset(screen_sel, 0, sizeof(screen_sel));
	for (int k = K_BITMAP; k <= K_FONT; k++)
		stock_default[k] = new_obj(k, 1);
}

/* ── Text the program writes ───────────────────────────────────────────── */

static void check_text(const wchar16 *s, int n, const char *who)
{
	if (!s) {
		FAIL("%s got no string", who);
		return;
	}
	if (n < 0 || n > 400) {
		FAIL("%s got a length of %d", who, n);
		return;
	}
	char ascii[401];
	for (int i = 0; i < n; i++) {
		if (s[i] == 0)
			FAIL("%s: a NUL inside the counted string", who);
		if (s[i] >= 0xD800 && s[i] < 0xE000)
			FAIL("%s: a lone surrogate", who);
		ascii[i] = s[i] < 128 ? (char)s[i] : '~';
	}
	ascii[n] = 0;
	if (strstr(ascii, "Wine is working!"))
		saw_title++;
	if (!strcmp(ascii, "Close"))
		saw_close_label++;
}

static int wlen(const wchar16 *s)
{
	int n = 0;
	while (s[n])
		n++;
	return n;
}

static int weq(const wchar16 *a, const char *b)
{
	while (*a && *b && *a == (wchar16)(unsigned char)*b)
		a++, b++;
	return *a == 0 && *b == 0;
}

/* ── kernel32 / ntdll ───────────────────────────────────────────────────── */

static MS const char *st_wine_get_version(void) { return "10.0 (pe_smoke)"; }
static MS void st_wine_get_host_version(const char **sys, const char **rel)
{
	if (sys)
		*sys = "Linux";
	if (rel)
		*rel = "4.9.191";
}

static MS void *st_GetModuleHandleW(const wchar16 *name)
{
	if (!name)
		return image;
	if (weq(name, "ntdll.dll"))
		return (void *)0x7000;
	FAIL("GetModuleHandleW for an unexpected module");
	return 0;
}

static MS void *st_GetProcAddress(void *mod, const char *name)
{
	if (mod != (void *)0x7000) {
		FAIL("GetProcAddress on an unexpected module");
		return 0;
	}
	if (!strcmp(name, "wine_get_version"))
		return (void *)st_wine_get_version;
	if (!strcmp(name, "wine_get_host_version"))
		return (void *)st_wine_get_host_version;
	return 0;
}

static MS void st_ExitProcess(unsigned code)
{
	exit_code = code;
	longjmp(exit_jump, 1);
}

static MS uint32_t st_GetTickCount(void) { return clock_ms; }
static MS void *st_GetStdHandle(uint32_t which) { return which == (uint32_t)-11 ? (void *)7 : 0; }

static MS int st_WriteFile(void *h, const void *buf, uint32_t n, uint32_t *wrote, void *ov)
{
	if (h != (void *)7 || !buf || ov) {
		FAIL("WriteFile with unexpected arguments");
		return 0;
	}
	if (out_len + n < sizeof(out_lines) - 1) {
		memcpy(out_lines + out_len, buf, n);
		out_len += n;
		out_lines[out_len] = 0;
	}
	printf("    [stdout] %.*s", (int)n, (const char *)buf);
	if (wrote)
		*wrote = n;
	return 1;
}

static MS void st_GetNativeSystemInfo(void *p)
{
	memset(p, 0, 48);
	*(uint16_t *)((uint8_t *)p + 0) = 9; /* PROCESSOR_ARCHITECTURE_AMD64 */
	*(uint32_t *)((uint8_t *)p + 4) = 4096;
	*(uint32_t *)((uint8_t *)p + 32) = 4; /* dwNumberOfProcessors */
}

static MS int st_GlobalMemoryStatusEx(void *p)
{
	if (*(uint32_t *)p != 64) {
		FAIL("MEMORYSTATUSEX.dwLength is %u, not 64", *(uint32_t *)p);
		return 0;
	}
	*(uint32_t *)((uint8_t *)p + 4) = 37;
	*(uint64_t *)((uint8_t *)p + 8) = 1006ull << 20;
	return 1;
}

static MS void st_GetLocalTime(void *p)
{
	uint16_t *t = p;
	t[0] = 2026, t[1] = 11, t[2] = 2, t[3] = 25, t[4] = 9, t[5] = 5, t[6] = (uint16_t)(clock_ms / 1000 % 60), t[7] = 0;
}

static MS uint32_t st_GetEnvironmentVariableW(const wchar16 *name, wchar16 *buf, uint32_t size)
{
	if (weq(name, "BOX64_LD_LIBRARY_PATH")) {
		if (size >= 2) {
			buf[0] = 'x';
			buf[1] = 0;
			return 1;
		}
		return 2;
	}
	return 0;
}

static MS int32_t st_RtlGetVersion(void *p)
{
	if (*(uint32_t *)p != 276) {
		FAIL("RTL_OSVERSIONINFOW size is %u, not 276", *(uint32_t *)p);
		return -1;
	}
	uint32_t *v = p;
	v[1] = 10, v[2] = 0, v[3] = 19045, v[4] = 2;
	return 0;
}

/* ── user32 ─────────────────────────────────────────────────────────────── */

#define WM_CREATE 0x0001
#define WM_DESTROY 0x0002
#define WM_SIZE 0x0005
#define WM_PAINT 0x000F
#define WM_CLOSE 0x0010
#define WM_QUIT 0x0012
#define WM_ERASEBKGND 0x0014
#define WM_KEYDOWN 0x0100
#define WM_SYSKEYDOWN 0x0104
#define WM_TIMER 0x0113
#define WM_MOUSEMOVE 0x0200
#define WM_LBUTTONDOWN 0x0201
#define WM_RBUTTONDOWN 0x0204
#define WM_MBUTTONDOWN 0x0207
#define WM_MOUSEWHEEL 0x020A

static int64_t send(uint32_t msg, uint64_t wp, int64_t lp)
{
	if (!wndproc) {
		FAIL("a message before the class was registered");
		return 0;
	}
	return wndproc(hwnd_main, msg, wp, lp);
}

static MS uint16_t st_RegisterClassExW(const void *wc)
{
	const uint8_t *p = wc;
	if (*(const uint32_t *)p != 80) {
		FAIL("WNDCLASSEXW.cbSize is %u, not 80", *(const uint32_t *)p);
		return 0;
	}
	wndproc = *(wndproc_t const *)(p + 8);
	class_name = *(const wchar16 *const *)(p + 64);
	if (!wndproc || !class_name) {
		FAIL("the window class has no procedure or no name");
		return 0;
	}
	if (*(void *const *)(p + 24) != image)
		FAIL("the window class's hInstance is not the program");
	return 0xC123;
}

static MS void *st_CreateWindowExW(uint32_t ex, const wchar16 *cls, const wchar16 *title, uint32_t style, int x, int y, int w, int h,
				   void *parent, void *menu, void *inst, void *param)
{
	if (!cls || !class_name || wlen(cls) != wlen(class_name) || memcmp(cls, class_name, 2 * wlen(cls))) {
		FAIL("CreateWindowExW names another class");
		return 0;
	}
	if (w != screen_w || h != screen_h)
		FAIL("the window is %dx%d on a %dx%d screen", w, h, screen_w, screen_h);
	window_alive = 1;
	send(WM_CREATE, 0, 0);
	send(WM_SIZE, 0, (int64_t)((uint32_t)w | ((uint32_t)h << 16)));
	return hwnd_main;
}

static MS int64_t st_DefWindowProcW(void *hwnd, uint32_t msg, uint64_t wp, int64_t lp)
{
	if (msg == WM_SYSKEYDOWN && wp == 0x73) /* Alt+F4 */
		send(WM_CLOSE, 0, 0);
	return 0;
}

static MS int st_ShowWindow(void *hwnd, int cmd) { return hwnd == hwnd_main; }
static MS int st_SetForegroundWindow(void *hwnd) { return hwnd == hwnd_main; }
static MS void *st_SetFocus(void *hwnd) { return hwnd; }

static MS int st_InvalidateRect(void *hwnd, const int32_t *rc, int erase)
{
	if (hwnd != hwnd_main) {
		FAIL("InvalidateRect on another window");
		return 0;
	}
	int l = 0, t = 0, r = screen_w, b = screen_h;
	if (rc) {
		l = rc[0], t = rc[1], r = rc[2], b = rc[3];
		if (r < l || b < t)
			FAIL("InvalidateRect with an inverted rectangle");
	}
	if (!inv) {
		inv = 1, inv_l = l, inv_t = t, inv_r = r, inv_b = b;
	} else {
		inv_l = l < inv_l ? l : inv_l;
		inv_t = t < inv_t ? t : inv_t;
		inv_r = r > inv_r ? r : inv_r;
		inv_b = b > inv_b ? b : inv_b;
	}
	return 1;
}

static MS int st_UpdateWindow(void *hwnd)
{
	if (inv)
		send(WM_PAINT, 0, 0);
	return 1;
}

static MS void *st_BeginPaint(void *hwnd, void *ps)
{
	uint8_t *p = ps;
	memset(p, 0, 72);
	*(uintptr_t *)p = SCREEN_DC;
	int32_t *rc = (int32_t *)(p + 12);
	if (inv)
		rc[0] = inv_l, rc[1] = inv_t, rc[2] = inv_r, rc[3] = inv_b;
	inv = 0;
	paints++;
	return (void *)SCREEN_DC;
}

static MS int st_EndPaint(void *hwnd, const void *ps)
{
	if (*(const uintptr_t *)ps != SCREEN_DC)
		FAIL("EndPaint got another PAINTSTRUCT");
	return 1;
}

static MS uint64_t st_SetTimer(void *hwnd, uint64_t id, uint32_t ms, void *fn)
{
	if (fn || ms < 10)
		FAIL("SetTimer with a callback or a %u ms period", ms);
	timer_on = 1;
	return id;
}

static MS int st_KillTimer(void *hwnd, uint64_t id)
{
	timer_on = 0;
	return 1;
}

static MS int st_GetSystemMetrics(int i) { return i == 0 ? screen_w : i == 1 ? screen_h : 0; }
static MS void *st_LoadCursorW(void *inst, const wchar16 *name) { return (void *)0x5555; }

static MS int st_GetClientRect(void *hwnd, int32_t *rc)
{
	rc[0] = rc[1] = 0, rc[2] = screen_w, rc[3] = screen_h;
	return 1;
}

static MS int st_TranslateMessage(const void *m) { return 0; }

static MS int st_DestroyWindow(void *hwnd)
{
	if (!window_alive) {
		FAIL("DestroyWindow twice");
		return 0;
	}
	window_alive = 0;
	destroyed = 1;
	send(WM_DESTROY, 0, 0);
	return 1;
}

static MS void st_PostQuitMessage(int code)
{
	quit_posted = 1;
	quit_code = code;
}

/* The scripted session: one message per call, a paint whenever part of the
 * window waits to be drawn (as Windows does when the queue is empty). */
struct scripted {
	uint32_t msg;
	uint64_t wp;
	int64_t lp;
	int repeat;
};

static int64_t xy(int x, int y) { return (int64_t)(((uint32_t)(uint16_t)x) | ((uint32_t)(uint16_t)y << 16)); }

static struct scripted script[64];
static int script_len;

static void build_script(void)
{
	int n = 0;
	script[n++] = (struct scripted){WM_TIMER, 1, 0, 70};
	script[n++] = (struct scripted){WM_MOUSEMOVE, 0, xy(300, 200), 1};
	script[n++] = (struct scripted){WM_MOUSEMOVE, 0, xy(screen_w / 2, screen_h / 2), 1};
	script[n++] = (struct scripted){WM_LBUTTONDOWN, 1, xy(screen_w / 2, screen_h / 2), 1};
	script[n++] = (struct scripted){WM_TIMER, 1, 0, 25};
	script[n++] = (struct scripted){WM_RBUTTONDOWN, 2, xy(40, 40), 1};
	script[n++] = (struct scripted){WM_MBUTTONDOWN, 16, xy(screen_w - 3, screen_h - 3), 1};
	script[n++] = (struct scripted){WM_MOUSEWHEEL, (uint64_t)120 << 16, xy(10, 10), 1};
	script[n++] = (struct scripted){WM_MOUSEWHEEL, (uint64_t)(uint16_t)-120 << 16, xy(10, 10), 1};
	script[n++] = (struct scripted){WM_KEYDOWN, 0x0D, 0, 1};
	script[n++] = (struct scripted){WM_KEYDOWN, 0x09, 0, 1};
	script[n++] = (struct scripted){WM_KEYDOWN, 0x20, 0, 1};
	script[n++] = (struct scripted){WM_KEYDOWN, 0x25, 0, 1};
	script[n++] = (struct scripted){WM_KEYDOWN, 'A', 0, 1};
	script[n++] = (struct scripted){WM_KEYDOWN, '7', 0, 1};
	script[n++] = (struct scripted){WM_KEYDOWN, 0x74, 0, 1}; /* F5: "a key" */
	script[n++] = (struct scripted){WM_SYSKEYDOWN, 0x12, 0, 1};
	script[n++] = (struct scripted){WM_TIMER, 1, 0, 40};
	switch (mode) {
	case MODE_ESC:
		script[n++] = (struct scripted){WM_KEYDOWN, 0x1B, 0, 1};
		break;
	case MODE_CLICK: {
		/* The middle of the Close button: design (1180, 50) on 1280 x 720. */
		script[n++] = (struct scripted){WM_LBUTTONDOWN, 1, xy(1180, 50), 1};
		break;
	}
	case MODE_ALTF4:
		script[n++] = (struct scripted){WM_SYSKEYDOWN, 0x73, 1 << 29, 1};
		break;
	}
	script_len = n;
}

static MS int st_GetMessageW(void *m, void *hwnd, uint32_t lo, uint32_t hi)
{
	uint8_t *p = m;
	memset(p, 0, 48);
	if (quit_posted) {
		*(uint32_t *)(p + 8) = WM_QUIT;
		*(uint64_t *)(p + 16) = (uint64_t)quit_code;
		return 0;
	}
	*(void **)p = hwnd_main;
	static int paints_in_a_row;
	if (inv) {
		if (++paints_in_a_row > 3) {
			FAIL("WM_PAINT keeps coming back: the window procedure does not validate the window");
			longjmp(exit_jump, 2);
		}
		*(uint32_t *)(p + 8) = WM_PAINT;
		return 1;
	}
	paints_in_a_row = 0;
	while (step < script_len && script[step].repeat <= 0)
		step++;
	if (step >= script_len) {
		FAIL("the session's script ran out and the program did not close");
		longjmp(exit_jump, 2);
	}
	struct scripted *s = &script[step];
	s->repeat--;
	*(uint32_t *)(p + 8) = s->msg;
	*(uint64_t *)(p + 16) = s->wp;
	*(int64_t *)(p + 24) = s->lp;
	*(uint32_t *)(p + 32) = clock_ms;
	clock_ms += s->msg == WM_TIMER ? 33 : 7;
	if (s->msg == WM_TIMER) {
		if (!timer_on)
			FAIL("a timer tick with no timer running");
		timers_sent++;
	}
	return 1;
}

static MS int64_t st_DispatchMessageW(const void *m)
{
	const uint8_t *p = m;
	if (*(void *const *)p != hwnd_main) {
		FAIL("DispatchMessageW for another window");
		return 0;
	}
	return send(*(const uint32_t *)(p + 8), *(const uint64_t *)(p + 16), *(const int64_t *)(p + 24));
}

static MS int st_FillRect(void *dc, const int32_t *rc, void *brush)
{
	if (!dc_ok((uintptr_t)dc, "FillRect"))
		return 0;
	int i = obj_index((uintptr_t)brush);
	if (i < 0 || objs[i].kind != K_BRUSH || !objs[i].alive)
		FAIL("FillRect with a brush that is not live");
	if (!rc || rc[2] < rc[0] || rc[3] < rc[1])
		FAIL("FillRect with a bad rectangle");
	return 1;
}

/* ── gdi32 ──────────────────────────────────────────────────────────────── */

static MS void *st_CreateCompatibleDC(void *dc)
{
	dc_ok((uintptr_t)dc, "CreateCompatibleDC");
	return (void *)new_obj(K_DC, 0);
}

static MS void *st_CreateCompatibleBitmap(void *dc, int w, int h)
{
	dc_ok((uintptr_t)dc, "CreateCompatibleBitmap");
	if (w <= 0 || h <= 0 || w > 8192 || h > 8192)
		FAIL("a %dx%d bitmap", w, h);
	return (void *)new_obj(K_BITMAP, 0);
}

static MS void *st_SelectObject(void *dc, void *obj)
{
	if (!dc_ok((uintptr_t)dc, "SelectObject"))
		return 0;
	int i = obj_index((uintptr_t)obj);
	if (i < 0 || !objs[i].alive || objs[i].kind == K_DC) {
		FAIL("SelectObject with %#lx, not a live object", (unsigned long)(uintptr_t)obj);
		return 0;
	}
	if (objs[i].kind == K_BITMAP && (uintptr_t)dc == SCREEN_DC)
		FAIL("a bitmap selected into the window's own DC");
	uintptr_t *sel = sel_of((uintptr_t)dc);
	uintptr_t prev = sel[objs[i].kind] ? sel[objs[i].kind] : stock_default[objs[i].kind];
	sel[objs[i].kind] = (uintptr_t)obj;
	return (void *)prev;
}

static int selected_anywhere(uintptr_t h)
{
	for (int k = 0; k < 6; k++)
		if (screen_sel[k] == h)
			return 1;
	for (int i = 0; i < nobj; i++)
		if (objs[i].kind == K_DC && objs[i].alive)
			for (int k = 0; k < 6; k++)
				if (objs[i].sel[k] == h)
					return 1;
	return 0;
}

static MS int st_DeleteObject(void *obj)
{
	int i = obj_index((uintptr_t)obj);
	if (i < 0 || !objs[i].alive || objs[i].kind == K_DC) {
		FAIL("DeleteObject on %#lx, not a live object", (unsigned long)(uintptr_t)obj);
		return 0;
	}
	if (objs[i].stock)
		return 1; /* Windows ignores deleting a stock object */
	if (selected_anywhere((uintptr_t)obj)) {
		FAIL("DeleteObject on an object still selected into a DC (Windows keeps it: a leak)");
		return 0;
	}
	objs[i].alive = 0;
	live--;
	return 1;
}

static MS int st_DeleteDC(void *dc)
{
	int i = obj_index((uintptr_t)dc);
	if (i < 0 || objs[i].kind != K_DC || !objs[i].alive) {
		FAIL("DeleteDC on %#lx, not a live DC", (unsigned long)(uintptr_t)dc);
		return 0;
	}
	for (int k = K_BITMAP; k <= K_FONT; k++)
		if (objs[i].sel[k] && objs[i].sel[k] != stock_default[k]) {
			int j = obj_index(objs[i].sel[k]);
			if (j >= 0 && !objs[j].stock)
				FAIL("DeleteDC while it still holds the program's own object (kind %d)", k);
		}
	objs[i].alive = 0;
	live--;
	return 1;
}

static MS int st_BitBlt(void *dst, int x, int y, int w, int h, void *src, int sx, int sy, uint32_t rop)
{
	if (!dc_ok((uintptr_t)dst, "BitBlt") || !dc_ok((uintptr_t)src, "BitBlt"))
		return 0;
	if (w <= 0 || h <= 0 || x < 0 || y < 0 || x + w > screen_w || y + h > screen_h || sx != x || sy != y)
		FAIL("BitBlt %dx%d at %d,%d from %d,%d", w, h, x, y, sx, sy);
	if (rop != 0x00CC0020)
		FAIL("BitBlt with raster operation %#x", rop);
	if ((uintptr_t)src != SCREEN_DC) {
		uintptr_t bmp = sel_of((uintptr_t)src)[K_BITMAP];
		if (!bmp || bmp == stock_default[K_BITMAP])
			FAIL("BitBlt from a memory DC with no bitmap in it");
	}
	return 1;
}

static MS void *st_CreateSolidBrush(uint32_t c) { return (void *)new_obj(K_BRUSH, 0); }

static MS void *st_CreatePen(int style, int width, uint32_t c)
{
	if (width < 0 || width > 64)
		FAIL("a pen %d px wide", width);
	return (void *)new_obj(K_PEN, 0);
}

static uintptr_t stock_null_brush, stock_null_pen;
static MS void *st_GetStockObject(int i)
{
	if (i == 5)
		return (void *)stock_null_brush;
	if (i == 8)
		return (void *)stock_null_pen;
	FAIL("GetStockObject(%d)", i);
	return 0;
}

static MS void *st_CreateFontW(int h, int w, int esc, int orient, int weight, uint32_t italic, uint32_t underline, uint32_t strike,
			      uint32_t charset, uint32_t outprec, uint32_t clipprec, uint32_t quality, uint32_t pitch, const wchar16 *face)
{
	if (h >= 0 || h < -200)
		FAIL("a font %d px high (a negative height is the character height)", h);
	if (weight < 100 || weight > 900)
		FAIL("a font of weight %d", weight);
	if (!face || !weq(face, "Tahoma"))
		FAIL("a font face other than Tahoma");
	if (quality != 4 || charset != 1)
		FAIL("font quality %u / charset %u", quality, charset);
	return (void *)new_obj(K_FONT, 0);
}

static MS int st_SetBkMode(void *dc, int m)
{
	dc_ok((uintptr_t)dc, "SetBkMode");
	return 2;
}

static MS uint32_t st_SetTextColor(void *dc, uint32_t c)
{
	dc_ok((uintptr_t)dc, "SetTextColor");
	if (c >> 24)
		FAIL("a text colour with high bits set: %#x", c);
	return 0;
}

static MS int st_TextOutW(void *dc, int x, int y, const wchar16 *s, int n)
{
	if (!dc_ok((uintptr_t)dc, "TextOutW"))
		return 0;
	uintptr_t f = sel_of((uintptr_t)dc)[K_FONT];
	if (!f || f == stock_default[K_FONT])
		FAIL("TextOutW with the default font still selected");
	check_text(s, n, "TextOutW");
	return 1;
}

static MS int st_GetTextExtentPoint32W(void *dc, const wchar16 *s, int n, int32_t *size)
{
	if (!dc_ok((uintptr_t)dc, "GetTextExtentPoint32W"))
		return 0;
	check_text(s, n, "GetTextExtentPoint32W");
	size[0] = n * 11;
	size[1] = 24;
	return 1;
}

static MS int st_Ellipse(void *dc, int l, int t, int r, int b)
{
	dc_ok((uintptr_t)dc, "Ellipse");
	if (r < l || b < t)
		FAIL("Ellipse with an inverted box");
	return 1;
}

static MS int st_RoundRect(void *dc, int l, int t, int r, int b, int w, int h)
{
	dc_ok((uintptr_t)dc, "RoundRect");
	if (r < l || b < t || w < 0 || h < 0)
		FAIL("RoundRect with a bad box");
	return 1;
}

static MS int st_MoveToEx(void *dc, int x, int y, void *old)
{
	dc_ok((uintptr_t)dc, "MoveToEx");
	return 1;
}

static MS int st_LineTo(void *dc, int x, int y)
{
	dc_ok((uintptr_t)dc, "LineTo");
	return 1;
}

/* ── winmm ──────────────────────────────────────────────────────────────── */

static MS int st_PlaySoundW(const void *snd, void *mod, uint32_t flags)
{
	const uint8_t *p = snd;
	if (!(flags & 0x0004) || !(flags & 0x0001) || mod) {
		FAIL("PlaySoundW without SND_MEMORY | SND_ASYNC (flags %#x)", flags);
		return 0;
	}
	if (memcmp(p, "RIFF", 4) || memcmp(p + 8, "WAVEfmt ", 8)) {
		FAIL("PlaySoundW got something that is not a WAV file");
		return 0;
	}
	uint16_t fmt = *(const uint16_t *)(p + 20), ch = *(const uint16_t *)(p + 22), bits = *(const uint16_t *)(p + 34);
	uint32_t rate = *(const uint32_t *)(p + 24), riff = *(const uint32_t *)(p + 4), data = *(const uint32_t *)(p + 40);
	if (fmt != 1 || ch != 1 || bits != 16 || rate < 8000 || memcmp(p + 36, "data", 4) || data + 36 != riff)
		FAIL("the WAV header is off: format %u, %u channel(s), %u bits, %u Hz", fmt, ch, bits, rate);
	chime_calls++;
	return 1;
}

/* ── The import table ───────────────────────────────────────────────────── */

struct stub {
	const char *dll, *name;
	void *fn;
};

static const struct stub stubs[] = {
    {"kernel32.dll", "GetModuleHandleW", (void *)st_GetModuleHandleW},
    {"kernel32.dll", "GetProcAddress", (void *)st_GetProcAddress},
    {"kernel32.dll", "ExitProcess", (void *)st_ExitProcess},
    {"kernel32.dll", "GetTickCount", (void *)st_GetTickCount},
    {"kernel32.dll", "GetStdHandle", (void *)st_GetStdHandle},
    {"kernel32.dll", "WriteFile", (void *)st_WriteFile},
    {"kernel32.dll", "GetNativeSystemInfo", (void *)st_GetNativeSystemInfo},
    {"kernel32.dll", "GlobalMemoryStatusEx", (void *)st_GlobalMemoryStatusEx},
    {"kernel32.dll", "GetLocalTime", (void *)st_GetLocalTime},
    {"kernel32.dll", "GetEnvironmentVariableW", (void *)st_GetEnvironmentVariableW},
    {"ntdll.dll", "RtlGetVersion", (void *)st_RtlGetVersion},
    {"user32.dll", "RegisterClassExW", (void *)st_RegisterClassExW},
    {"user32.dll", "CreateWindowExW", (void *)st_CreateWindowExW},
    {"user32.dll", "DefWindowProcW", (void *)st_DefWindowProcW},
    {"user32.dll", "ShowWindow", (void *)st_ShowWindow},
    {"user32.dll", "UpdateWindow", (void *)st_UpdateWindow},
    {"user32.dll", "SetForegroundWindow", (void *)st_SetForegroundWindow},
    {"user32.dll", "SetFocus", (void *)st_SetFocus},
    {"user32.dll", "GetMessageW", (void *)st_GetMessageW},
    {"user32.dll", "TranslateMessage", (void *)st_TranslateMessage},
    {"user32.dll", "DispatchMessageW", (void *)st_DispatchMessageW},
    {"user32.dll", "PostQuitMessage", (void *)st_PostQuitMessage},
    {"user32.dll", "DestroyWindow", (void *)st_DestroyWindow},
    {"user32.dll", "BeginPaint", (void *)st_BeginPaint},
    {"user32.dll", "EndPaint", (void *)st_EndPaint},
    {"user32.dll", "InvalidateRect", (void *)st_InvalidateRect},
    {"user32.dll", "SetTimer", (void *)st_SetTimer},
    {"user32.dll", "KillTimer", (void *)st_KillTimer},
    {"user32.dll", "GetSystemMetrics", (void *)st_GetSystemMetrics},
    {"user32.dll", "LoadCursorW", (void *)st_LoadCursorW},
    {"user32.dll", "GetClientRect", (void *)st_GetClientRect},
    {"user32.dll", "FillRect", (void *)st_FillRect},
    {"gdi32.dll", "CreateCompatibleDC", (void *)st_CreateCompatibleDC},
    {"gdi32.dll", "CreateCompatibleBitmap", (void *)st_CreateCompatibleBitmap},
    {"gdi32.dll", "SelectObject", (void *)st_SelectObject},
    {"gdi32.dll", "DeleteObject", (void *)st_DeleteObject},
    {"gdi32.dll", "DeleteDC", (void *)st_DeleteDC},
    {"gdi32.dll", "BitBlt", (void *)st_BitBlt},
    {"gdi32.dll", "CreateSolidBrush", (void *)st_CreateSolidBrush},
    {"gdi32.dll", "CreatePen", (void *)st_CreatePen},
    {"gdi32.dll", "GetStockObject", (void *)st_GetStockObject},
    {"gdi32.dll", "CreateFontW", (void *)st_CreateFontW},
    {"gdi32.dll", "SetBkMode", (void *)st_SetBkMode},
    {"gdi32.dll", "SetTextColor", (void *)st_SetTextColor},
    {"gdi32.dll", "TextOutW", (void *)st_TextOutW},
    {"gdi32.dll", "GetTextExtentPoint32W", (void *)st_GetTextExtentPoint32W},
    {"gdi32.dll", "Ellipse", (void *)st_Ellipse},
    {"gdi32.dll", "RoundRect", (void *)st_RoundRect},
    {"gdi32.dll", "MoveToEx", (void *)st_MoveToEx},
    {"gdi32.dll", "LineTo", (void *)st_LineTo},
    {"winmm.dll", "PlaySoundW", (void *)st_PlaySoundW},
};

static void *find_stub(const char *dll, const char *name)
{
	for (size_t i = 0; i < sizeof(stubs) / sizeof(stubs[0]); i++)
		if (!strcasecmp(stubs[i].dll, dll) && !strcmp(stubs[i].name, name))
			return stubs[i].fn;
	return 0;
}

/* ── The loader ─────────────────────────────────────────────────────────── */

static uint8_t *file;
static size_t file_len;

#define RD16(p) (*(const uint16_t *)(p))
#define RD32(p) (*(const uint32_t *)(p))
#define RD64(p) (*(const uint64_t *)(p))

static void *load(uint32_t *entry_rva)
{
	if (file_len < 0x200 || memcmp(file, "MZ", 2)) {
		FAIL("not an MZ file");
		exit(1);
	}
	uint32_t pe = RD32(file + 0x3C);
	if (memcmp(file + pe, "PE\0\0", 4) || RD16(file + pe + 4) != 0x8664) {
		FAIL("not an x64 PE file");
		exit(1);
	}
	uint16_t nsec = RD16(file + pe + 6), optsz = RD16(file + pe + 20), chars = RD16(file + pe + 22);
	const uint8_t *o = file + pe + 24;
	if (RD16(o) != 0x20B || optsz != 240)
		FAIL("not a PE32+ optional header");
	if (chars & 0x0001)
		FAIL("the image says its relocations were stripped: it can only load at its own base");
	*entry_rva = RD32(o + 16);
	uint64_t base = RD64(o + 24);
	uint32_t size_image = RD32(o + 56), size_headers = RD32(o + 60);
	uint16_t subsystem = RD16(o + 68), dllchars = RD16(o + 70);
	if (subsystem != 2)
		FAIL("subsystem %u, not a Windows GUI program", subsystem);
	if (!(dllchars & 0x0040) || !(dllchars & 0x0100))
		FAIL("not marked dynamic-base and NX-compatible");
	image_len = size_image;
	/* Somewhere else than the program's own base, to prove it moves. */
	image = mmap((void *)0x7e0000000000ull, image_len, PROT_READ | PROT_WRITE | PROT_EXEC, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
	if (image == MAP_FAILED) {
		perror("mmap");
		exit(1);
	}
	if ((uint64_t)(uintptr_t)image == base)
		FAIL("the image landed on its own base; the relocation path went untested");
	memcpy(image, file, size_headers);
	const uint8_t *sec = o + optsz;
	for (int i = 0; i < nsec; i++, sec += 40) {
		uint32_t vsize = RD32(sec + 8), rva = RD32(sec + 12), rawsize = RD32(sec + 16), rawptr = RD32(sec + 20);
		if ((uint64_t)rva + vsize > size_image || (uint64_t)rawptr + rawsize > file_len) {
			FAIL("section %.8s is outside the image or the file", (const char *)sec);
			exit(1);
		}
		memcpy(image + rva, file + rawptr, rawsize < vsize ? rawsize : vsize);
	}
	/* Base relocations. */
	uint32_t rel_rva = RD32(o + 112 + 8 * 5), rel_size = RD32(o + 112 + 8 * 5 + 4);
	int64_t delta = (int64_t)((uintptr_t)image - base);
	int fixups = 0;
	for (uint32_t off = 0; off < rel_size;) {
		uint32_t page = RD32(image + rel_rva + off), block = RD32(image + rel_rva + off + 4);
		if (block < 8 || off + block > rel_size) {
			FAIL("a broken relocation block");
			break;
		}
		for (uint32_t e = 8; e + 2 <= block; e += 2) {
			uint16_t v = RD16(image + rel_rva + off + e);
			switch (v >> 12) {
			case 0:
				break;
			case 10:
				*(uint64_t *)(image + page + (v & 0xFFF)) += (uint64_t)delta;
				fixups++;
				break;
			default:
				FAIL("relocation type %u", v >> 12);
			}
		}
		off += block;
	}
	if (rel_size == 0)
		FAIL("no relocation data: a loader could not move the image");
	if (fixups)
		FAIL("%d absolute fixups: the code is not position independent", fixups);
	/* Imports. */
	uint32_t imp = RD32(o + 112 + 8);
	int resolved = 0;
	for (const uint8_t *d = image + imp;; d += 20) {
		uint32_t ilt = RD32(d), name = RD32(d + 12), iat = RD32(d + 16);
		if (!ilt && !name && !iat)
			break;
		const char *dll = (const char *)image + name;
		uint64_t *lookup = (uint64_t *)(image + ilt), *slot = (uint64_t *)(image + iat);
		for (; *lookup; lookup++, slot++) {
			if (*lookup >> 63) {
				FAIL("%s: an import by ordinal", dll);
				continue;
			}
			const char *fn = (const char *)image + (uint32_t)*lookup + 2;
			void *stub = find_stub(dll, fn);
			if (!stub) {
				FAIL("no stand-in for %s!%s", dll, fn);
				continue;
			}
			*slot = (uint64_t)(uintptr_t)stub;
			resolved++;
		}
	}
	printf("  loaded at %p (its own base %#llx), %d imports resolved\n", (void *)image, (unsigned long long)base, resolved);
	return image + *entry_rva;
}

static int session(void)
{
	int before = failures;
	window_alive = destroyed = quit_posted = quit_code = timer_on = 0;
	inv = paints = timers_sent = step = 0;
	saw_title = saw_close_label = chime_calls = 0;
	out_len = 0;
	out_lines[0] = 0;
	wndproc = 0;
	class_name = 0;
	clock_ms = 5000000;
	screen_w = mode == MODE_ALTF4 ? 1024 : 1280;
	screen_h = mode == MODE_ALTF4 ? 768 : 720;
	reset_gdi();
	stock_null_brush = new_obj(K_BRUSH, 1);
	stock_null_pen = new_obj(K_PEN, 1);
	build_script();
	printf("session: %s\n", mode_names[mode]);
	uint32_t entry_rva;
	void (MS *entry)(void) = (void (MS *)(void))load(&entry_rva);
	int how = setjmp(exit_jump);
	if (how == 0) {
		entry();
		FAIL("the entry point returned instead of calling ExitProcess");
	}
	if (how == 1 && exit_code != 0)
		FAIL("exit code %u", exit_code);
	if (!destroyed || window_alive)
		FAIL("the window was not destroyed");
	if (timer_on)
		FAIL("the timer was left running");
	if (paints < 50)
		FAIL("only %d paints", paints);
	if (!saw_title)
		FAIL("the title was never drawn");
	if (!saw_close_label)
		FAIL("the Close button was never drawn");
	if (chime_calls != 2)
		FAIL("the chime played %d times, expected 2 (start and Space)", chime_calls);
	if (live != 0)
		FAIL("%d GDI objects still alive at exit (a leak)", live);
	if (max_live > 24)
		FAIL("%d GDI objects alive at once", max_live);
	const char *want[] = {"jukahub-winetest: started | Wine 10.0 (pe_smoke) | Linux 4.9.191 | Windows 10 (version 10.0, build 19045) | x64 | 4 cores | translated by Box64",
			      "jukahub-winetest: window shown", "jukahub-winetest: first frame drawn after", "jukahub-winetest: sound played",
			      "jukahub-winetest: closed after", "Last input: "};
	for (size_t i = 0; i < sizeof(want) / sizeof(want[0]); i++)
		if (!strstr(out_lines, want[i]))
			FAIL("the log has no \"%s\"", want[i]);
	printf("  %d paints, %d timer ticks, %d GDI objects made, at most %d alive, %d at exit, exit code %u\n", paints, timers_sent,
	       nobj, max_live, live, exit_code);
	munmap(image, image_len);
	return failures == before;
}

int main(int argc, char **argv)
{
	if (argc != 2) {
		fprintf(stderr, "usage: pe_smoke winetest.exe\n");
		return 2;
	}
	FILE *f = fopen(argv[1], "rb");
	if (!f) {
		perror(argv[1]);
		return 2;
	}
	fseek(f, 0, SEEK_END);
	file_len = (size_t)ftell(f);
	fseek(f, 0, SEEK_SET);
	file = malloc(file_len);
	if (fread(file, 1, file_len, f) != file_len) {
		perror("read");
		return 2;
	}
	fclose(f);
	int ok = 1;
	for (mode = MODE_ESC; mode <= MODE_ALTF4; mode++)
		ok &= session();
	printf(ok && !failures ? "pe_smoke: all sessions passed\n" : "pe_smoke: %d failure(s)\n", failures);
	return ok && !failures ? 0 : 1;
}
