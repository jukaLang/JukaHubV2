/*
 * winetest.c — JukaHub's built-in Wine test (Windows Apps → "Wine Test").
 *
 * One full-screen window that says Wine works, and shows what it runs on:
 * the Wine version, the host system, the Windows version Wine reports, the
 * processor, the screen and the memory. A ball moves and a clock ticks so a
 * frozen screen is obvious; every button, key and mouse event is named on
 * screen, so the pad layout JukaHub gives Windows programs can be checked; a
 * short chime plays through Windows' own sound API at start (R1 / Space plays
 * it again). Y / Esc, the Close button or Select + Start closes it.
 *
 * It is a plain 64-bit Win32 GDI program with no C runtime: user32, gdi32,
 * kernel32, ntdll and winmm only. There is no Windows SDK here, so the few
 * types and functions it needs are declared below with the Windows layouts
 * (checked by _Static_assert), and mkwinetest.py builds it with the host gcc
 * in the Microsoft x64 calling convention (-mabi=ms) and writes the PE file
 * itself. The code is position independent (-fpie, no absolute addresses), so
 * the image carries an empty relocation block and loads at any base.
 *
 * Lines it prints on standard output (they land in the app's log in JukaHub):
 *   jukahub-winetest: started · ...      the program runs
 *   jukahub-winetest: first frame ...    Windows drew the window
 *   jukahub-winetest: closed ...         it ended normally
 */

typedef unsigned char BYTE;
typedef unsigned short WORD;
typedef unsigned short WCHAR;
typedef unsigned int DWORD;
typedef unsigned int UINT;
typedef int BOOL;
typedef int LONG;
typedef unsigned long long ULONGLONG;
typedef unsigned long long UINT_PTR;
typedef long long LONG_PTR;
typedef unsigned long long DWORD_PTR;
typedef UINT_PTR WPARAM;
typedef LONG_PTR LPARAM;
typedef LONG_PTR LRESULT;
typedef void *HANDLE;
typedef HANDLE HWND;
typedef HANDLE HDC;
typedef HANDLE HINSTANCE;
typedef HANDLE HMODULE;
typedef HANDLE HICON;
typedef HANDLE HCURSOR;
typedef HANDLE HBRUSH;
typedef HANDLE HFONT;
typedef HANDLE HBITMAP;
typedef HANDLE HPEN;
typedef HANDLE HGDIOBJ;
typedef HANDLE HMENU;
typedef DWORD COLORREF;
typedef unsigned short ATOM;

#define WINAPI __attribute__((ms_abi))
#define NULL ((void *)0)

typedef LRESULT(WINAPI *WNDPROC)(HWND, UINT, WPARAM, LPARAM);
typedef void(WINAPI *TIMERPROC)(HWND, UINT, UINT_PTR, DWORD);
/* void (*)(void) is the type gcc lets any function pointer be cast from. */
typedef void(WINAPI *FARPROC)(void);

typedef struct {
	LONG left, top, right, bottom;
} RECT;
typedef struct {
	LONG x, y;
} POINT;
typedef struct {
	LONG cx, cy;
} SIZE;
typedef struct {
	HWND hwnd;
	UINT message;
	WPARAM wParam;
	LPARAM lParam;
	DWORD time;
	POINT pt;
	DWORD lPrivate;
} MSG;
typedef struct {
	HDC hdc;
	BOOL fErase;
	RECT rcPaint;
	BOOL fRestore;
	BOOL fIncUpdate;
	BYTE rgbReserved[32];
} PAINTSTRUCT;
typedef struct {
	UINT cbSize;
	UINT style;
	WNDPROC lpfnWndProc;
	int cbClsExtra;
	int cbWndExtra;
	HINSTANCE hInstance;
	HICON hIcon;
	HCURSOR hCursor;
	HBRUSH hbrBackground;
	const WCHAR *lpszMenuName;
	const WCHAR *lpszClassName;
	HICON hIconSm;
} WNDCLASSEXW;
typedef struct {
	WORD wProcessorArchitecture;
	WORD wReserved;
	DWORD dwPageSize;
	void *lpMinimumApplicationAddress;
	void *lpMaximumApplicationAddress;
	DWORD_PTR dwActiveProcessorMask;
	DWORD dwNumberOfProcessors;
	DWORD dwProcessorType;
	DWORD dwAllocationGranularity;
	WORD wProcessorLevel;
	WORD wProcessorRevision;
} SYSTEM_INFO;
typedef struct {
	DWORD dwLength;
	DWORD dwMemoryLoad;
	ULONGLONG ullTotalPhys, ullAvailPhys, ullTotalPageFile, ullAvailPageFile;
	ULONGLONG ullTotalVirtual, ullAvailVirtual, ullAvailExtendedVirtual;
} MEMORYSTATUSEX;
typedef struct {
	DWORD dwOSVersionInfoSize, dwMajorVersion, dwMinorVersion, dwBuildNumber, dwPlatformId;
	WCHAR szCSDVersion[128];
} RTL_OSVERSIONINFOW;
typedef struct {
	WORD wYear, wMonth, wDayOfWeek, wDay, wHour, wMinute, wSecond, wMilliseconds;
} SYSTEMTIME;

_Static_assert(sizeof(MSG) == 48, "MSG layout");
_Static_assert(sizeof(PAINTSTRUCT) == 72, "PAINTSTRUCT layout");
_Static_assert(__builtin_offsetof(PAINTSTRUCT, rcPaint) == 12, "PAINTSTRUCT.rcPaint");
_Static_assert(sizeof(WNDCLASSEXW) == 80, "WNDCLASSEXW layout");
_Static_assert(sizeof(SYSTEM_INFO) == 48, "SYSTEM_INFO layout");
_Static_assert(__builtin_offsetof(SYSTEM_INFO, dwNumberOfProcessors) == 32, "SYSTEM_INFO.dwNumberOfProcessors");
_Static_assert(sizeof(MEMORYSTATUSEX) == 64, "MEMORYSTATUSEX layout");
_Static_assert(sizeof(RTL_OSVERSIONINFOW) == 276, "RTL_OSVERSIONINFOW layout");
_Static_assert(sizeof(SYSTEMTIME) == 16, "SYSTEMTIME layout");
_Static_assert(sizeof(WCHAR) == 2 && sizeof(LONG) == 4 && sizeof(DWORD) == 4, "Windows integer sizes");

/*
 * The imports. mkwinetest.py reads these lines to build the import table, and
 * points each __imp_ symbol at its slot in the import address table, the way
 * a Windows linker does.
 */
#define IMPORT(dll, ret, name, args) extern ret(WINAPI *__imp_##name) args __attribute__((visibility("hidden")));
#define W32(name) (*__imp_##name)

IMPORT(kernel32, HMODULE, GetModuleHandleW, (const WCHAR *))
IMPORT(kernel32, FARPROC, GetProcAddress, (HMODULE, const char *))
IMPORT(kernel32, void, ExitProcess, (UINT))
IMPORT(kernel32, DWORD, GetTickCount, (void))
IMPORT(kernel32, HANDLE, GetStdHandle, (DWORD))
IMPORT(kernel32, BOOL, WriteFile, (HANDLE, const void *, DWORD, DWORD *, void *))
IMPORT(kernel32, void, GetNativeSystemInfo, (SYSTEM_INFO *))
IMPORT(kernel32, BOOL, GlobalMemoryStatusEx, (MEMORYSTATUSEX *))
IMPORT(kernel32, void, GetLocalTime, (SYSTEMTIME *))
IMPORT(kernel32, DWORD, GetEnvironmentVariableW, (const WCHAR *, WCHAR *, DWORD))
IMPORT(ntdll, LONG, RtlGetVersion, (RTL_OSVERSIONINFOW *))
IMPORT(user32, ATOM, RegisterClassExW, (const WNDCLASSEXW *))
IMPORT(user32, HWND, CreateWindowExW, (DWORD, const WCHAR *, const WCHAR *, DWORD, int, int, int, int, HWND, HMENU, HINSTANCE, void *))
IMPORT(user32, LRESULT, DefWindowProcW, (HWND, UINT, WPARAM, LPARAM))
IMPORT(user32, BOOL, ShowWindow, (HWND, int))
IMPORT(user32, BOOL, UpdateWindow, (HWND))
IMPORT(user32, BOOL, SetForegroundWindow, (HWND))
IMPORT(user32, HWND, SetFocus, (HWND))
IMPORT(user32, BOOL, GetMessageW, (MSG *, HWND, UINT, UINT))
IMPORT(user32, BOOL, TranslateMessage, (const MSG *))
IMPORT(user32, LRESULT, DispatchMessageW, (const MSG *))
IMPORT(user32, void, PostQuitMessage, (int))
IMPORT(user32, BOOL, DestroyWindow, (HWND))
IMPORT(user32, HDC, BeginPaint, (HWND, PAINTSTRUCT *))
IMPORT(user32, BOOL, EndPaint, (HWND, const PAINTSTRUCT *))
IMPORT(user32, BOOL, InvalidateRect, (HWND, const RECT *, BOOL))
IMPORT(user32, UINT_PTR, SetTimer, (HWND, UINT_PTR, UINT, TIMERPROC))
IMPORT(user32, BOOL, KillTimer, (HWND, UINT_PTR))
IMPORT(user32, int, GetSystemMetrics, (int))
IMPORT(user32, HCURSOR, LoadCursorW, (HINSTANCE, const WCHAR *))
IMPORT(user32, BOOL, GetClientRect, (HWND, RECT *))
IMPORT(user32, int, FillRect, (HDC, const RECT *, HBRUSH))
IMPORT(gdi32, HDC, CreateCompatibleDC, (HDC))
IMPORT(gdi32, HBITMAP, CreateCompatibleBitmap, (HDC, int, int))
IMPORT(gdi32, HGDIOBJ, SelectObject, (HDC, HGDIOBJ))
IMPORT(gdi32, BOOL, DeleteObject, (HGDIOBJ))
IMPORT(gdi32, BOOL, DeleteDC, (HDC))
IMPORT(gdi32, BOOL, BitBlt, (HDC, int, int, int, int, HDC, int, int, DWORD))
IMPORT(gdi32, HBRUSH, CreateSolidBrush, (COLORREF))
IMPORT(gdi32, HPEN, CreatePen, (int, int, COLORREF))
IMPORT(gdi32, HGDIOBJ, GetStockObject, (int))
IMPORT(gdi32, HFONT, CreateFontW, (int, int, int, int, int, DWORD, DWORD, DWORD, DWORD, DWORD, DWORD, DWORD, DWORD, const WCHAR *))
IMPORT(gdi32, int, SetBkMode, (HDC, int))
IMPORT(gdi32, COLORREF, SetTextColor, (HDC, COLORREF))
IMPORT(gdi32, BOOL, TextOutW, (HDC, int, int, const WCHAR *, int))
IMPORT(gdi32, BOOL, GetTextExtentPoint32W, (HDC, const WCHAR *, int, SIZE *))
IMPORT(gdi32, BOOL, Ellipse, (HDC, int, int, int, int))
IMPORT(gdi32, BOOL, RoundRect, (HDC, int, int, int, int, int, int))
IMPORT(gdi32, BOOL, MoveToEx, (HDC, int, int, POINT *))
IMPORT(gdi32, BOOL, LineTo, (HDC, int, int))
IMPORT(winmm, BOOL, PlaySoundW, (const void *, HMODULE, DWORD))

/* The few constants used. */
#define WM_CREATE 0x0001
#define WM_DESTROY 0x0002
#define WM_SIZE 0x0005
#define WM_PAINT 0x000F
#define WM_CLOSE 0x0010
#define WM_ERASEBKGND 0x0014
#define WM_KEYDOWN 0x0100
#define WM_SYSKEYDOWN 0x0104
#define WM_TIMER 0x0113
#define WM_MOUSEMOVE 0x0200
#define WM_LBUTTONDOWN 0x0201
#define WM_RBUTTONDOWN 0x0204
#define WM_MBUTTONDOWN 0x0207
#define WM_MOUSEWHEEL 0x020A
#define WS_POPUP 0x80000000u
#define WS_VISIBLE 0x10000000u
#define WS_EX_APPWINDOW 0x00040000u
#define SW_SHOW 5
#define SM_CXSCREEN 0
#define SM_CYSCREEN 1
#define CS_HREDRAW 0x0002
#define CS_VREDRAW 0x0001
#define SRCCOPY 0x00CC0020u
#define TRANSPARENT 1
#define PS_SOLID 0
#define NULL_BRUSH 5
#define NULL_PEN 8
#define FW_NORMAL 400
#define FW_SEMIBOLD 600
#define FW_BOLD 700
#define DEFAULT_CHARSET 1
#define ANTIALIASED_QUALITY 4
#define SND_ASYNC 0x0001
#define SND_NODEFAULT 0x0002
#define SND_MEMORY 0x0004
#define STD_OUTPUT_HANDLE ((DWORD)-11)
#define VK_TAB 0x09
#define VK_RETURN 0x0D
#define VK_ESCAPE 0x1B
#define VK_SPACE 0x20
#define VK_PRIOR 0x21
#define VK_NEXT 0x22
#define VK_END 0x23
#define VK_HOME 0x24
#define VK_LEFT 0x25
#define VK_UP 0x26
#define VK_RIGHT 0x27
#define VK_DOWN 0x28
#define VK_BACK 0x08
#define VK_MENU 0x12
#define VK_F4 0x73
#define IDC_ARROW ((const WCHAR *)(UINT_PTR)32512)

#define RGB(r, g, b) ((COLORREF)((r) | ((g) << 8) | ((b) << 16)))
#define W(s) ((const WCHAR *)u##s)

/* The chime: a short WAV file, made by mkwinetest.py. */
#include "chime.h"

/* ── No C runtime: the two routines the compiler may call ───────────────── */

__attribute__((used, optimize("no-tree-loop-distribute-patterns"))) void *memset(void *dst, int c, __SIZE_TYPE__ n)
{
	BYTE *d = dst;
	while (n--)
		*d++ = (BYTE)c;
	return dst;
}

__attribute__((used, optimize("no-tree-loop-distribute-patterns"))) void *memcpy(void *dst, const void *src, __SIZE_TYPE__ n)
{
	BYTE *d = dst;
	const BYTE *s = src;
	while (n--)
		*d++ = *s++;
	return dst;
}

/* ── Text buffers (UTF-16) ──────────────────────────────────────────────── */

typedef struct {
	WCHAR s[256];
	int n;
} line;

static void l_clear(line *l) { l->n = 0, l->s[0] = 0; }

static void l_w(line *l, const WCHAR *s)
{
	while (*s && l->n < (int)(sizeof(l->s) / 2) - 1)
		l->s[l->n++] = *s++;
	l->s[l->n] = 0;
}

static void l_a(line *l, const char *s)
{
	while (*s && l->n < (int)(sizeof(l->s) / 2) - 1)
		l->s[l->n++] = (WCHAR)(BYTE)*s++;
	l->s[l->n] = 0;
}

static void l_u(line *l, ULONGLONG v)
{
	WCHAR tmp[24];
	int i = 0;
	do {
		tmp[i++] = (WCHAR)('0' + v % 10);
		v /= 10;
	} while (v && i < 24);
	while (i > 0 && l->n < (int)(sizeof(l->s) / 2) - 1)
		l->s[l->n++] = tmp[--i];
	l->s[l->n] = 0;
}

static void l_u2(line *l, unsigned v)
{
	if (v < 10)
		l_w(l, W("0"));
	l_u(l, v);
}

static void a_copy(char *dst, int cap, const char *src)
{
	int i = 0;
	if (src)
		for (; src[i] && i < cap - 1; i++)
			dst[i] = src[i];
	dst[i] = 0;
}

/* ── The log ────────────────────────────────────────────────────────────── */

/* say writes one line to standard output, in ASCII; a GUI program under Wine
 * inherits the Unix stdout, which JukaHub keeps as the app's log. */
static void say(const line *l)
{
	static char buf[300];
	HANDLE out = W32(GetStdHandle)(STD_OUTPUT_HANDLE);
	if (out == NULL || out == (HANDLE)(LONG_PTR)-1)
		return;
	int n = 0;
	for (int i = 0; i < l->n && n < (int)sizeof(buf) - 2; i++) {
		WCHAR c = l->s[i];
		switch (c) {
		case 0x00B7: /* · */
			buf[n++] = '|';
			break;
		case 0x00D7: /* × */
			buf[n++] = 'x';
			break;
		case 0x2013: /* – */
		case 0x2014: /* — */
			buf[n++] = '-';
			break;
		case 0x2026: /* … */
			buf[n++] = '.';
			break;
		default:
			buf[n++] = c < 0x80 ? (char)c : '?';
		}
	}
	buf[n++] = '\n';
	DWORD wrote = 0;
	W32(WriteFile)(out, buf, (DWORD)n, &wrote, NULL);
}

/* ── What the program finds out once ────────────────────────────────────── */

static char wine_ver[48], host_sys[32], host_rel[48];
static line info_wine, info_host, info_win, info_cpu, info_prog, info_screen, info_mem;
static int screen_w, screen_h;

static void gather(void)
{
	HMODULE nt = W32(GetModuleHandleW)(W("ntdll.dll"));
	if (nt) {
		/* Present only in Wine's ntdll: a real Windows says it is not Wine. */
		const char *(WINAPI * wgv)(void) = (const char *(WINAPI *)(void))W32(GetProcAddress)(nt, "wine_get_version");
		void(WINAPI * wghv)(const char **, const char **) =
		    (void(WINAPI *)(const char **, const char **))W32(GetProcAddress)(nt, "wine_get_host_version");
		if (wgv)
			a_copy(wine_ver, sizeof(wine_ver), wgv());
		if (wghv) {
			const char *sys = NULL, *rel = NULL;
			wghv(&sys, &rel);
			a_copy(host_sys, sizeof(host_sys), sys);
			a_copy(host_rel, sizeof(host_rel), rel);
		}
	}
	l_clear(&info_wine);
	if (wine_ver[0]) {
		l_w(&info_wine, W("Wine "));
		l_a(&info_wine, wine_ver);
	} else {
		l_w(&info_wine, W("not Wine: real Windows runs this"));
	}
	l_clear(&info_host);
	if (host_sys[0]) {
		l_a(&info_host, host_sys);
		if (host_rel[0]) {
			l_w(&info_host, W(" "));
			l_a(&info_host, host_rel);
		}
	} else {
		l_w(&info_host, W("Windows"));
	}

	RTL_OSVERSIONINFOW v;
	memset(&v, 0, sizeof(v));
	v.dwOSVersionInfoSize = sizeof(v);
	l_clear(&info_win);
	if (W32(RtlGetVersion)(&v) == 0) {
		l_w(&info_win, W("Windows "));
		if (v.dwMajorVersion == 10 && v.dwBuildNumber >= 22000)
			l_w(&info_win, W("11"));
		else if (v.dwMajorVersion == 10)
			l_w(&info_win, W("10"));
		else if (v.dwMajorVersion == 6 && v.dwMinorVersion == 1)
			l_w(&info_win, W("7"));
		else if (v.dwMajorVersion == 6 && v.dwMinorVersion >= 2)
			l_w(&info_win, W("8"));
		else
			l_u(&info_win, v.dwMajorVersion);
		l_w(&info_win, W(" (version "));
		l_u(&info_win, v.dwMajorVersion);
		l_w(&info_win, W("."));
		l_u(&info_win, v.dwMinorVersion);
		l_w(&info_win, W(", build "));
		l_u(&info_win, v.dwBuildNumber);
		l_w(&info_win, W(")"));
	} else {
		l_w(&info_win, W("unknown"));
	}

	SYSTEM_INFO si;
	memset(&si, 0, sizeof(si));
	W32(GetNativeSystemInfo)(&si);
	l_clear(&info_cpu);
	switch (si.wProcessorArchitecture) {
	case 9:
		l_w(&info_cpu, W("x64"));
		break;
	case 0:
		l_w(&info_cpu, W("x86 (32-bit)"));
		break;
	case 12:
		l_w(&info_cpu, W("ARM64"));
		break;
	case 5:
		l_w(&info_cpu, W("ARM"));
		break;
	default:
		l_w(&info_cpu, W("unknown"));
	}
	if (si.dwNumberOfProcessors) {
		l_w(&info_cpu, W(" · "));
		l_u(&info_cpu, si.dwNumberOfProcessors);
		l_w(&info_cpu, si.dwNumberOfProcessors == 1 ? W(" core") : W(" cores"));
	}
	/* JukaHub starts Wine through Box64 on an ARM handheld: Wine passes the
	 * Unix environment on, so its variables are visible here. */
	WCHAR tmp[8];
	if (W32(GetEnvironmentVariableW)(W("BOX64_LD_LIBRARY_PATH"), tmp, 8) ||
	    W32(GetEnvironmentVariableW)(W("BOX64_PATH"), tmp, 8))
		l_w(&info_cpu, W(" · translated by Box64"));

	l_clear(&info_prog);
	l_w(&info_prog, W("64-bit · drawn with GDI · sound through winmm"));

	screen_w = W32(GetSystemMetrics)(SM_CXSCREEN);
	screen_h = W32(GetSystemMetrics)(SM_CYSCREEN);
	l_clear(&info_screen);
	l_u(&info_screen, (ULONGLONG)(screen_w > 0 ? screen_w : 0));
	l_w(&info_screen, W(" × "));
	l_u(&info_screen, (ULONGLONG)(screen_h > 0 ? screen_h : 0));

	MEMORYSTATUSEX ms;
	memset(&ms, 0, sizeof(ms));
	ms.dwLength = sizeof(ms);
	l_clear(&info_mem);
	if (W32(GlobalMemoryStatusEx)(&ms) && ms.ullTotalPhys) {
		ULONGLONG mb = ms.ullTotalPhys >> 20;
		if (mb >= 1024) {
			ULONGLONG tenths = (mb * 10 + 512) / 1024;
			l_u(&info_mem, tenths / 10);
			l_w(&info_mem, W("."));
			l_u(&info_mem, tenths % 10);
			l_w(&info_mem, W(" GB"));
		} else {
			l_u(&info_mem, mb);
			l_w(&info_mem, W(" MB"));
		}
		l_w(&info_mem, W(" · "));
		l_u(&info_mem, ms.dwMemoryLoad);
		l_w(&info_mem, W("% in use"));
	} else {
		l_w(&info_mem, W("unknown"));
	}
}

/* ── Drawing ────────────────────────────────────────────────────────────── */

/* The design is 1280 x 720; other screens scale it to fit and centre it. */
#define DESIGN_W 1280
#define DESIGN_H 720

static int cw, ch;      /* client size */
static int sc_num = 1, sc_den = 1, ox, oy;
static HDC bg_dc, fr_dc;
static HBITMAP bg_bmp, fr_bmp, bg_old, fr_old;
static HFONT f_title, f_sub, f_label, f_value, f_small, f_button;

static int X(int v) { return ox + v * sc_num / sc_den; }
static int Y(int v) { return oy + v * sc_num / sc_den; }
static int S(int v) { return v * sc_num / sc_den; }

static COLORREF mix(COLORREF a, COLORREF b, int t, int of)
{
	int ar = a & 0xFF, ag = (a >> 8) & 0xFF, ab = (a >> 16) & 0xFF;
	int br = b & 0xFF, bg = (b >> 8) & 0xFF, bb = (b >> 16) & 0xFF;
	return RGB(ar + (br - ar) * t / of, ag + (bg - ag) * t / of, ab + (bb - ab) * t / of);
}

static void fill(HDC dc, int l, int t, int r, int b, COLORREF c)
{
	RECT rc = {l, t, r, b};
	HBRUSH br = W32(CreateSolidBrush)(c);
	W32(FillRect)(dc, &rc, br);
	W32(DeleteObject)(br);
}

/* shape draws an ellipse (round = 0) or a rounded rectangle, filled with fillc
 * (fillc == -1: no fill) and outlined with a pen of width pw (0: none). */
static void shape(HDC dc, int l, int t, int r, int b, int round, long long fillc, COLORREF pen, int pw)
{
	HGDIOBJ brush = fillc < 0 ? W32(GetStockObject)(NULL_BRUSH) : (HGDIOBJ)W32(CreateSolidBrush)((COLORREF)fillc);
	HGDIOBJ p = pw > 0 ? (HGDIOBJ)W32(CreatePen)(PS_SOLID, pw, pen) : W32(GetStockObject)(NULL_PEN);
	HGDIOBJ ob = W32(SelectObject)(dc, brush);
	HGDIOBJ op = W32(SelectObject)(dc, p);
	if (round > 0)
		W32(RoundRect)(dc, l, t, r, b, round, round);
	else
		W32(Ellipse)(dc, l, t, r, b);
	W32(SelectObject)(dc, ob);
	W32(SelectObject)(dc, op);
	if (fillc >= 0)
		W32(DeleteObject)(brush);
	if (pw > 0)
		W32(DeleteObject)(p);
}

static void stroke(HDC dc, int x0, int y0, int x1, int y1, int w, COLORREF c)
{
	HPEN p = W32(CreatePen)(PS_SOLID, w, c);
	HGDIOBJ op = W32(SelectObject)(dc, p);
	W32(MoveToEx)(dc, x0, y0, NULL);
	W32(LineTo)(dc, x1, y1);
	W32(SelectObject)(dc, op);
	W32(DeleteObject)(p);
}

static int text_w(HDC dc, HFONT f, const WCHAR *s, int n)
{
	SIZE sz = {0, 0};
	HGDIOBJ of = W32(SelectObject)(dc, f);
	W32(GetTextExtentPoint32W)(dc, s, n, &sz);
	W32(SelectObject)(dc, of);
	return sz.cx;
}

/* text draws s with its left edge at x; align 1 centres it on x. */
static void text(HDC dc, HFONT f, const WCHAR *s, int n, int x, int y, COLORREF c, int align)
{
	if (n < 0)
		for (n = 0; s[n]; n++)
			;
	if (align == 1)
		x -= text_w(dc, f, s, n) / 2;
	HGDIOBJ of = W32(SelectObject)(dc, f);
	W32(SetBkMode)(dc, TRANSPARENT);
	W32(SetTextColor)(dc, c);
	W32(TextOutW)(dc, x, y, s, n);
	W32(SelectObject)(dc, of);
}

static HFONT font(int px, int weight)
{
	return W32(CreateFontW)(-S(px), 0, 0, 0, weight, 0, 0, 0, DEFAULT_CHARSET, 0, 0, ANTIALIASED_QUALITY, 0, W("Tahoma"));
}

static void free_fonts(void)
{
	HFONT *all[] = {&f_title, &f_sub, &f_label, &f_value, &f_small, &f_button};
	for (unsigned i = 0; i < sizeof(all) / sizeof(all[0]); i++)
		if (*all[i]) {
			W32(DeleteObject)(*all[i]);
			*all[i] = NULL;
		}
}

static void free_buffers(void)
{
	if (bg_dc) {
		W32(SelectObject)(bg_dc, bg_old);
		W32(DeleteObject)(bg_bmp);
		W32(DeleteDC)(bg_dc);
		bg_dc = NULL;
	}
	if (fr_dc) {
		W32(SelectObject)(fr_dc, fr_old);
		W32(DeleteObject)(fr_bmp);
		W32(DeleteDC)(fr_dc);
		fr_dc = NULL;
	}
}

static const COLORREF c_top = RGB(13, 18, 38), c_bottom = RGB(31, 15, 64);
static const COLORREF c_ink = RGB(246, 247, 252), c_dim = RGB(143, 155, 199), c_soft = RGB(200, 207, 232);
static const COLORREF c_accent = RGB(127, 209, 255), c_ok = RGB(52, 199, 123), c_card = RGB(21, 26, 51), c_line = RGB(44, 53, 102);

/* glow paints a soft round light: rings from the backdrop colour to c. */
static void glow(HDC dc, int cx, int cy, int r, COLORREF base, COLORREF c)
{
	for (int i = 0; i < 24; i++) {
		int rr = r - r * i / 24;
		shape(dc, cx - rr, cy - rr, cx + rr, cy + rr, 0, mix(base, c, i + 1, 30), 0, 0);
	}
}

/* Rows of the information card. */
static const WCHAR *const *row_labels(void)
{
	static const WCHAR *labels[7];
	labels[0] = W("Running in");
	labels[1] = W("Host system");
	labels[2] = W("Windows reports");
	labels[3] = W("Processor");
	labels[4] = W("This program");
	labels[5] = W("Screen");
	labels[6] = W("Memory");
	return labels;
}

static const line *row_values(int i)
{
	switch (i) {
	case 0:
		return &info_wine;
	case 1:
		return &info_host;
	case 2:
		return &info_win;
	case 3:
		return &info_cpu;
	case 4:
		return &info_prog;
	case 5:
		return &info_screen;
	}
	return &info_mem;
}

/* The Close button, in design pixels. */
#define CLOSE_L 1110
#define CLOSE_T 28
#define CLOSE_R 1250
#define CLOSE_B 72

/* draw_static paints everything that does not move into the backdrop bitmap. */
static void draw_static(HDC dc)
{
	for (int i = 0; i < 90; i++) {
		int t = ch * i / 90, b = ch * (i + 1) / 90;
		fill(dc, 0, t, cw, b + 1, mix(c_top, c_bottom, i, 89));
	}
	glow(dc, X(170), Y(110), S(330), mix(c_top, c_bottom, 15, 100), RGB(59, 42, 138));
	glow(dc, X(1130), Y(660), S(300), mix(c_top, c_bottom, 90, 100), RGB(18, 80, 122));

	/* The tick badge. */
	int bx = X(640), by = Y(104), br = S(42);
	shape(dc, bx - br - S(8), by - br - S(8), bx + br + S(8), by + br + S(8), 0, mix(c_top, c_ok, 8, 30), 0, 0);
	shape(dc, bx - br, by - br, bx + br, by + br, 0, c_ok, 0, 0);
	stroke(dc, bx - S(19), by + S(1), bx - S(5), by + S(16), S(9), c_ink);
	stroke(dc, bx - S(5), by + S(16), bx + S(21), by - S(14), S(9), c_ink);

	text(dc, f_title, W("Wine is working!"), -1, X(640), Y(160), c_ink, 1);
	text(dc, f_sub, W("This Windows program is running on your handheld through Wine."), -1, X(640), Y(236), c_soft, 1);

	/* The information card. */
	shape(dc, X(220), Y(286), X(1060), Y(522), S(28), c_card, c_line, S(2) > 0 ? S(2) : 1);
	const WCHAR *const *labels = row_labels();
	for (int i = 0; i < 7; i++) {
		int y = Y(304 + i * 30);
		text(dc, f_label, labels[i], -1, X(262), y, c_dim, 0);
		const line *v = row_values(i);
		text(dc, f_value, v->s, v->n, X(480), y, i == 0 && wine_ver[0] ? c_ok : c_ink, 0);
	}

	/* The Close button. */
	shape(dc, X(CLOSE_L), Y(CLOSE_T), X(CLOSE_R), Y(CLOSE_B), S(44), RGB(36, 42, 78), c_line, 1);
	text(dc, f_button, W("Close"), -1, X((CLOSE_L + CLOSE_R) / 2), Y(CLOSE_T + 10), c_ink, 1);

	text(dc, f_small,
	     W("Y / Esc  close     ·     A  click     ·     R1 / Space  chime     ·     Select + Start  close"),
	     -1, X(640), Y(688), c_dim, 1);
}

/* ── Live state ─────────────────────────────────────────────────────────── */

static HWND win;
static DWORD started_at, first_frame_ms, fps_mark;
static ULONGLONG frames, fps_frames;
static unsigned fps;
static int painted;
static line input_line, sound_line;
static int ptr_x = -1, ptr_y = -1;
static DWORD last_move_note;

typedef struct {
	int x, y;
	DWORD at;
	COLORREF c;
} ripple;
static ripple ripples[8];

#define RIPPLE_MS 650
#define TIMER_ID 1

static void note_input(const WCHAR *what, int x, int y)
{
	l_clear(&input_line);
	l_w(&input_line, W("Last input: "));
	l_w(&input_line, what);
	if (x >= 0) {
		l_w(&input_line, W(" at "));
		l_u(&input_line, (ULONGLONG)x);
		l_w(&input_line, W(", "));
		l_u(&input_line, (ULONGLONG)y);
	}
	RECT rc = {0, Y(604), cw, Y(632)};
	W32(InvalidateRect)(win, &rc, 0);
}

static void add_ripple(int x, int y, COLORREF c)
{
	int oldest = 0;
	for (int i = 0; i < 8; i++) {
		if (ripples[i].at == 0) {
			oldest = i;
			break;
		}
		if (ripples[i].at < ripples[oldest].at)
			oldest = i;
	}
	ripples[oldest].x = x;
	ripples[oldest].y = y;
	ripples[oldest].at = W32(GetTickCount)() | 1;
	ripples[oldest].c = c;
}

static void play_chime(int by_hand)
{
	BOOL ok = W32(PlaySoundW)(chime_wav, NULL, SND_MEMORY | SND_ASYNC | SND_NODEFAULT);
	l_clear(&sound_line);
	if (ok) {
		l_w(&sound_line, by_hand ? W("Sound: the chime played again") : W("Sound: a chime played through Windows sound"));
		l_w(&sound_line, W("  (R1 / Space plays it again)"));
	} else {
		l_w(&sound_line, W("Sound: Windows found no sound device"));
	}
	line log;
	l_clear(&log);
	l_w(&log, ok ? W("jukahub-winetest: sound played") : W("jukahub-winetest: sound not available"));
	say(&log);
	RECT rc = {0, Y(634), cw, Y(662)};
	W32(InvalidateRect)(win, &rc, 0);
}

/* The moving parts' band, in design pixels. */
#define BAND_T 534
#define BAND_B 600

static void invalidate_band(void)
{
	RECT rc = {0, Y(BAND_T), cw, Y(BAND_B)};
	W32(InvalidateRect)(win, &rc, 0);
}

static void invalidate_around(int x, int y, int r)
{
	RECT rc = {x - r - 4, y - r - 4, x + r + 4, y + r + 4};
	W32(InvalidateRect)(win, &rc, 0);
}

static void draw_live(HDC dc)
{
	DWORD now = W32(GetTickCount)();

	/* The track and the ball: it goes back and forth once every 2.4 s. */
	int l = X(240), r = X(1040), ty = Y(556);
	shape(dc, l, ty - S(7), r, ty + S(7), S(14), RGB(30, 36, 70), 0, 0);
	DWORD period = 2400, ph = (now - started_at) % period;
	int half = period / 2, pos = ph < (DWORD)half ? (int)ph : (int)(period - ph);
	/* ease in and out: 3t^2 - 2t^3, in thousandths */
	long long t = (long long)pos * 1000 / half;
	long long e = (3 * t * t * 1000 - 2 * t * t * t) / 1000000;
	int bx = l + S(14) + (int)((r - l - S(28)) * e / 1000);
	COLORREF bc = mix(c_accent, c_ok, (int)e, 1000);
	shape(dc, l, ty - S(3), bx, ty + S(3), S(6), mix(c_line, bc, 2, 3), 0, 0);
	shape(dc, bx - S(13), ty - S(13), bx + S(13), ty + S(13), 0, bc, c_ink, S(2) > 0 ? S(2) : 1);

	/* Frames, frame rate and the clock. */
	SYSTEMTIME st;
	W32(GetLocalTime)(&st);
	line l2;
	l_clear(&l2);
	l_w(&l2, W("Frames drawn "));
	l_u(&l2, frames);
	l_w(&l2, W("   ·   "));
	l_u(&l2, fps);
	l_w(&l2, W(" per second   ·   Windows clock "));
	l_u2(&l2, st.wHour);
	l_w(&l2, W(":"));
	l_u2(&l2, st.wMinute);
	l_w(&l2, W(":"));
	l_u2(&l2, st.wSecond);
	text(dc, f_small, l2.s, l2.n, X(640), Y(574), c_soft, 1);

	text(dc, f_small, input_line.s, input_line.n, X(640), Y(606), c_accent, 1);
	text(dc, f_small, sound_line.s, sound_line.n, X(640), Y(636), c_dim, 1);

	/* Clicks ripple out from where they landed. */
	for (int i = 0; i < 8; i++) {
		ripple *rp = &ripples[i];
		if (!rp->at)
			continue;
		DWORD age = now - rp->at;
		if (age >= RIPPLE_MS)
			continue;
		int rr = S(10) + S(60) * (int)age / RIPPLE_MS;
		COLORREF c = mix(rp->c, c_bottom, (int)age, RIPPLE_MS);
		shape(dc, rp->x - rr, rp->y - rr, rp->x + rr, rp->y + rr, 0, -1, c, S(4) > 0 ? S(4) : 1);
	}

	/* Where the pointer is, for a compositor that draws none. */
	if (ptr_x >= 0) {
		int pr = S(9) > 3 ? S(9) : 3;
		shape(dc, ptr_x - pr, ptr_y - pr, ptr_x + pr, ptr_y + pr, 0, -1, c_accent, 2);
		stroke(dc, ptr_x - pr - S(6), ptr_y, ptr_x - pr + S(2), ptr_y, 2, c_accent);
		stroke(dc, ptr_x + pr - S(2), ptr_y, ptr_x + pr + S(6), ptr_y, 2, c_accent);
	}
}

static void layout(int w, int h)
{
	if (w <= 0 || h <= 0)
		return;
	cw = w, ch = h;
	/* scale = min(w / 1280, h / 720) */
	if ((long long)w * DESIGN_H <= (long long)h * DESIGN_W)
		sc_num = w, sc_den = DESIGN_W;
	else
		sc_num = h, sc_den = DESIGN_H;
	ox = (w - DESIGN_W * sc_num / sc_den) / 2;
	oy = (h - DESIGN_H * sc_num / sc_den) / 2;
	free_buffers();
	free_fonts();
	f_title = font(62, FW_BOLD);
	f_sub = font(24, FW_NORMAL);
	f_label = font(21, FW_NORMAL);
	f_value = font(21, FW_SEMIBOLD);
	f_small = font(20, FW_NORMAL);
	f_button = font(21, FW_SEMIBOLD);
}

static void ensure_buffers(HDC screen)
{
	if (bg_dc)
		return;
	bg_dc = W32(CreateCompatibleDC)(screen);
	bg_bmp = W32(CreateCompatibleBitmap)(screen, cw, ch);
	bg_old = W32(SelectObject)(bg_dc, bg_bmp);
	fr_dc = W32(CreateCompatibleDC)(screen);
	fr_bmp = W32(CreateCompatibleBitmap)(screen, cw, ch);
	fr_old = W32(SelectObject)(fr_dc, fr_bmp);
	draw_static(bg_dc);
}

static void paint(HWND hwnd)
{
	PAINTSTRUCT ps;
	HDC dc = W32(BeginPaint)(hwnd, &ps);
	if (!dc)
		return;
	if (cw <= 0 || ch <= 0) {
		RECT rc;
		if (W32(GetClientRect)(hwnd, &rc))
			layout(rc.right - rc.left, rc.bottom - rc.top);
	}
	if (cw > 0 && ch > 0) {
		ensure_buffers(dc);
		RECT r = ps.rcPaint;
		if (r.left < 0)
			r.left = 0;
		if (r.top < 0)
			r.top = 0;
		if (r.right > cw)
			r.right = cw;
		if (r.bottom > ch)
			r.bottom = ch;
		if (r.right > r.left && r.bottom > r.top) {
			int w = r.right - r.left, h = r.bottom - r.top;
			W32(BitBlt)(fr_dc, r.left, r.top, w, h, bg_dc, r.left, r.top, SRCCOPY);
			draw_live(fr_dc);
			W32(BitBlt)(dc, r.left, r.top, w, h, fr_dc, r.left, r.top, SRCCOPY);
		}
		frames++;
		fps_frames++;
	}
	W32(EndPaint)(hwnd, &ps);
	if (!painted && frames) {
		painted = 1;
		first_frame_ms = W32(GetTickCount)() - started_at;
		line log;
		l_clear(&log);
		l_w(&log, W("jukahub-winetest: first frame drawn after "));
		l_u(&log, first_frame_ms);
		l_w(&log, W(" ms at "));
		l_u(&log, (ULONGLONG)cw);
		l_w(&log, W("x"));
		l_u(&log, (ULONGLONG)ch);
		say(&log);
	}
}

static const WCHAR *key_name(WPARAM vk)
{
	switch (vk) {
	case VK_RETURN:
		return W("Enter key (Start)");
	case VK_TAB:
		return W("Tab key (L1)");
	case VK_SPACE:
		return W("Space key (R1)");
	case VK_LEFT:
		return W("Left arrow (D-pad)");
	case VK_RIGHT:
		return W("Right arrow (D-pad)");
	case VK_UP:
		return W("Up arrow (D-pad)");
	case VK_DOWN:
		return W("Down arrow (D-pad)");
	case VK_BACK:
		return W("Backspace key");
	case VK_HOME:
		return W("Home key");
	case VK_END:
		return W("End key");
	case VK_PRIOR:
		return W("Page Up key");
	case VK_NEXT:
		return W("Page Down key");
	case VK_MENU:
		return W("Alt key");
	}
	if ((vk >= 'A' && vk <= 'Z') || (vk >= '0' && vk <= '9')) {
		static WCHAR one[] = {'K', 'e', 'y', ' ', 'X', 0};
		one[4] = (WCHAR)vk;
		return one;
	}
	return W("a key");
}

static LRESULT WINAPI wndproc(HWND hwnd, UINT msg, WPARAM wp, LPARAM lp)
{
	switch (msg) {
	case WM_CREATE:
		return 0;
	case WM_SIZE:
		layout((int)(short)(lp & 0xFFFF), (int)(short)((lp >> 16) & 0xFFFF));
		W32(InvalidateRect)(hwnd, NULL, 0);
		return 0;
	case WM_ERASEBKGND:
		return 1; /* paint covers every pixel */
	case WM_PAINT:
		paint(hwnd);
		return 0;
	case WM_TIMER: {
		DWORD now = W32(GetTickCount)();
		if (now - fps_mark >= 1000) {
			fps = (unsigned)(fps_frames * 1000 / (now - fps_mark));
			fps_frames = 0;
			fps_mark = now;
		}
		invalidate_band();
		for (int i = 0; i < 8; i++) {
			ripple *rp = &ripples[i];
			if (!rp->at)
				continue;
			if (now - rp->at >= RIPPLE_MS + 100) {
				rp->at = 0;
			}
			/* The last frame of a ripple is its erase. */
			invalidate_around(rp->x, rp->y, S(74));
		}
		if (painted == 1) {
			/* After the first frame: the chime, once. */
			painted = 2;
			play_chime(0);
		}
		return 0;
	}
	case WM_MOUSEMOVE: {
		int x = (int)(short)(lp & 0xFFFF), y = (int)(short)((lp >> 16) & 0xFFFF);
		if (ptr_x >= 0)
			invalidate_around(ptr_x, ptr_y, S(18) + 4);
		ptr_x = x, ptr_y = y;
		invalidate_around(x, y, S(18) + 4);
		DWORD now = W32(GetTickCount)();
		if (now - last_move_note > 120) {
			last_move_note = now;
			note_input(W("pointer moved (stick)"), x, y);
		}
		return 0;
	}
	case WM_LBUTTONDOWN: {
		int x = (int)(short)(lp & 0xFFFF), y = (int)(short)((lp >> 16) & 0xFFFF);
		if (x >= X(CLOSE_L) && x < X(CLOSE_R) && y >= Y(CLOSE_T) && y < Y(CLOSE_B)) {
			W32(DestroyWindow)(hwnd);
			return 0;
		}
		add_ripple(x, y, c_accent);
		note_input(W("left click (A)"), x, y);
		return 0;
	}
	case WM_RBUTTONDOWN: {
		int x = (int)(short)(lp & 0xFFFF), y = (int)(short)((lp >> 16) & 0xFFFF);
		add_ripple(x, y, c_ok);
		note_input(W("right click (B)"), x, y);
		return 0;
	}
	case WM_MBUTTONDOWN: {
		int x = (int)(short)(lp & 0xFFFF), y = (int)(short)((lp >> 16) & 0xFFFF);
		add_ripple(x, y, RGB(255, 196, 92));
		note_input(W("middle click (X)"), x, y);
		return 0;
	}
	case WM_MOUSEWHEEL:
		note_input((short)((wp >> 16) & 0xFFFF) > 0 ? W("wheel up (L2)") : W("wheel down (R2)"), -1, -1);
		return 0;
	case WM_KEYDOWN:
	case WM_SYSKEYDOWN:
		if (wp == VK_ESCAPE) {
			W32(DestroyWindow)(hwnd);
			return 0;
		}
		if (wp == VK_SPACE)
			play_chime(1);
		if (msg == WM_SYSKEYDOWN && wp == VK_F4)
			break; /* Alt+F4: DefWindowProc sends WM_CLOSE */
		note_input(key_name(wp), -1, -1);
		if (msg == WM_SYSKEYDOWN)
			break;
		return 0;
	case WM_CLOSE:
		W32(DestroyWindow)(hwnd);
		return 0;
	case WM_DESTROY:
		W32(KillTimer)(hwnd, TIMER_ID);
		W32(PostQuitMessage)(0);
		return 0;
	}
	return W32(DefWindowProcW)(hwnd, msg, wp, lp);
}

static int run(void)
{
	started_at = W32(GetTickCount)();
	fps_mark = started_at;
	gather();

	line log;
	l_clear(&log);
	l_w(&log, W("jukahub-winetest: started · "));
	l_w(&log, info_wine.s);
	l_w(&log, W(" · "));
	l_w(&log, info_host.s);
	l_w(&log, W(" · "));
	l_w(&log, info_win.s);
	l_w(&log, W(" · "));
	l_w(&log, info_cpu.s);
	say(&log);

	l_clear(&input_line);
	l_w(&input_line, W("Last input: none yet — press a button or move the stick"));
	l_clear(&sound_line);
	l_w(&sound_line, W("Sound: starting…"));

	HINSTANCE self = W32(GetModuleHandleW)(NULL);
	WNDCLASSEXW wc;
	memset(&wc, 0, sizeof(wc));
	wc.cbSize = sizeof(wc);
	wc.style = CS_HREDRAW | CS_VREDRAW;
	wc.lpfnWndProc = wndproc;
	wc.hInstance = self;
	wc.hCursor = W32(LoadCursorW)(NULL, IDC_ARROW);
	wc.lpszClassName = W("JukaHubWineTest");
	if (!W32(RegisterClassExW)(&wc)) {
		l_clear(&log);
		l_w(&log, W("jukahub-winetest: could not register the window class"));
		say(&log);
		return 2;
	}
	int w = screen_w > 0 ? screen_w : DESIGN_W, h = screen_h > 0 ? screen_h : DESIGN_H;
	win = W32(CreateWindowExW)(WS_EX_APPWINDOW, W("JukaHubWineTest"), W("JukaHub Wine Test"), WS_POPUP | WS_VISIBLE, 0, 0, w, h,
				   NULL, NULL, self, NULL);
	if (!win) {
		l_clear(&log);
		l_w(&log, W("jukahub-winetest: could not create the window"));
		say(&log);
		return 3;
	}
	if (cw <= 0)
		layout(w, h);
	W32(ShowWindow)(win, SW_SHOW);
	l_clear(&log);
	l_w(&log, W("jukahub-winetest: window shown "));
	l_u(&log, (ULONGLONG)w);
	l_w(&log, W("x"));
	l_u(&log, (ULONGLONG)h);
	say(&log);
	W32(SetForegroundWindow)(win);
	W32(SetFocus)(win);
	W32(UpdateWindow)(win);
	W32(SetTimer)(win, TIMER_ID, 33, NULL);

	MSG m;
	int code = 0;
	for (;;) {
		BOOL got = W32(GetMessageW)(&m, NULL, 0, 0);
		if (got == 0) {
			code = (int)m.wParam;
			break;
		}
		if (got == -1) {
			code = 4;
			break;
		}
		W32(TranslateMessage)(&m);
		W32(DispatchMessageW)(&m);
	}
	free_buffers();
	free_fonts();

	l_clear(&log);
	l_w(&log, W("jukahub-winetest: closed after "));
	l_u(&log, (W32(GetTickCount)() - started_at) / 1000);
	l_w(&log, W(" s, "));
	l_u(&log, frames);
	l_w(&log, W(" frames, "));
	l_w(&log, input_line.s);
	say(&log);
	return code;
}

/* The entry point: Windows calls it with nothing set up, and nothing after it
 * returns would end the process, so it ends it. */
__attribute__((used)) void WINAPI winetest_entry(void)
{
	W32(ExitProcess)((UINT)run());
}
