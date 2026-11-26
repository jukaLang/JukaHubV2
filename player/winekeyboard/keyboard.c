/* JukaHub's floating Virtual Keyboard for Wine's Xwayland windows.
 * The Wine program named by JUKAHUB_VK_TARGET runs as this program's child.
 * JUKAHUB_VK_CMDLINE is its quoted Windows command line, when it has args.
 * This no-activate topmost window remains a small tab until Start/F12 toggles
 * its panel; clicking a key sends Unicode input to the focused program.
 * It has no CRT.
 */
typedef unsigned short WCHAR;
typedef unsigned short WORD;
typedef unsigned int DWORD;
typedef unsigned int UINT;
typedef int BOOL;
typedef int LONG;
typedef long long LONG_PTR;
typedef unsigned long long UINT_PTR;
typedef unsigned long long DWORD_PTR;
typedef UINT_PTR WPARAM;
typedef LONG_PTR LPARAM;
typedef LONG_PTR LRESULT;
typedef void *HANDLE;
typedef HANDLE HWND;
typedef HANDLE HDC;
typedef HANDLE HINSTANCE;
typedef HANDLE HMODULE;
typedef HANDLE HBRUSH;
typedef HANDLE HCURSOR;
typedef HANDLE HMENU;
typedef HANDLE HICON;
typedef unsigned short ATOM;
typedef struct { LONG x, y; } POINT;
typedef struct { LONG left, top, right, bottom; } RECT;
typedef struct { HWND hwnd; UINT message; WPARAM wParam; LPARAM lParam; DWORD time; POINT pt; DWORD lPrivate; } MSG;
typedef struct { HDC hdc; BOOL fErase; RECT rcPaint; BOOL fRestore; BOOL fIncUpdate; unsigned char rgbReserved[32]; } PAINTSTRUCT;
typedef LRESULT (__attribute__((ms_abi)) *WNDPROC)(HWND, UINT, WPARAM, LPARAM);
typedef struct {
    UINT cbSize, style; WNDPROC lpfnWndProc; int cbClsExtra, cbWndExtra;
    HINSTANCE hInstance; HICON hIcon; HCURSOR hCursor; HBRUSH hbrBackground;
    const WCHAR *lpszMenuName, *lpszClassName; HICON hIconSm;
} WNDCLASSEXW;
typedef struct {
    DWORD cb; WCHAR *lpReserved, *lpDesktop, *lpTitle;
    DWORD dwX,dwY,dwXSize,dwYSize,dwXCountChars,dwYCountChars,dwFillAttribute,dwFlags;
    WORD wShowWindow, cbReserved2; unsigned char *lpReserved2;
    HANDLE hStdInput,hStdOutput,hStdError;
} STARTUPINFOW;
typedef struct { HANDLE hProcess,hThread; DWORD dwProcessId,dwThreadId; } PROCESS_INFORMATION;
typedef struct { WORD wVk,wScan; DWORD dwFlags,time; DWORD_PTR dwExtraInfo; } KEYBDINPUT;
typedef struct { DWORD type; union { KEYBDINPUT ki; unsigned char pad[32]; } data; } INPUT;
_Static_assert(sizeof(MSG)==48, "MSG");
_Static_assert(sizeof(WNDCLASSEXW)==80, "WNDCLASSEXW");
_Static_assert(sizeof(STARTUPINFOW)==104, "STARTUPINFOW");
_Static_assert(sizeof(PROCESS_INFORMATION)==24, "PROCESS_INFORMATION");
_Static_assert(sizeof(INPUT)==40, "INPUT");
#define W(s) L##s
#define NULL ((void *)0)
#define WINAPI __attribute__((ms_abi))
#define IMPORT(dll,ret,name,args) extern ret (WINAPI *__imp_##name) args __attribute__((visibility("hidden")));
#define API(name) (*__imp_##name)
IMPORT(kernel32,HMODULE,GetModuleHandleW,(const WCHAR *))
IMPORT(kernel32,void,ExitProcess,(UINT))
IMPORT(kernel32,DWORD,GetEnvironmentVariableW,(const WCHAR *,WCHAR *,DWORD))
IMPORT(kernel32,BOOL,CreateProcessW,(const WCHAR *,WCHAR *,void *,void *,BOOL,DWORD,void *,const WCHAR *,STARTUPINFOW *,PROCESS_INFORMATION *))
IMPORT(kernel32,DWORD,WaitForSingleObject,(HANDLE,DWORD))
IMPORT(kernel32,BOOL,GetExitCodeProcess,(HANDLE,DWORD *))
IMPORT(kernel32,BOOL,CloseHandle,(HANDLE))
IMPORT(kernel32,HANDLE,GetStdHandle,(DWORD))
IMPORT(kernel32,BOOL,WriteFile,(HANDLE,const void *,DWORD,DWORD *,void *))
IMPORT(user32,ATOM,RegisterClassExW,(const WNDCLASSEXW *))
IMPORT(user32,HWND,CreateWindowExW,(DWORD,const WCHAR *,const WCHAR *,DWORD,int,int,int,int,HWND,HMENU,HINSTANCE,void *))
IMPORT(user32,LRESULT,DefWindowProcW,(HWND,UINT,WPARAM,LPARAM))
IMPORT(user32,BOOL,ShowWindow,(HWND,int))
IMPORT(user32,BOOL,UpdateWindow,(HWND))
IMPORT(user32,BOOL,GetMessageW,(MSG *,HWND,UINT,UINT))
IMPORT(user32,BOOL,TranslateMessage,(const MSG *))
IMPORT(user32,LRESULT,DispatchMessageW,(const MSG *))
IMPORT(user32,void,PostQuitMessage,(int))
IMPORT(user32,HDC,BeginPaint,(HWND,PAINTSTRUCT *))
IMPORT(user32,BOOL,EndPaint,(HWND,const PAINTSTRUCT *))
IMPORT(user32,BOOL,InvalidateRect,(HWND,const RECT *,BOOL))
IMPORT(user32,UINT_PTR,SetTimer,(HWND,UINT_PTR,UINT,void *))
IMPORT(user32,BOOL,GetClientRect,(HWND,RECT *))
IMPORT(user32,int,FillRect,(HDC,const RECT *,HBRUSH))
IMPORT(user32,HWND,GetForegroundWindow,(void))
IMPORT(user32,BOOL,SetForegroundWindow,(HWND))
IMPORT(user32,UINT,SendInput,(UINT,INPUT *,int))
IMPORT(user32,int,GetSystemMetrics,(int))
IMPORT(user32,BOOL,RegisterHotKey,(HWND,int,UINT,UINT))
IMPORT(user32,BOOL,UnregisterHotKey,(HWND,int))
IMPORT(user32,short,GetAsyncKeyState,(int))
IMPORT(gdi32,HBRUSH,CreateSolidBrush,(DWORD))
IMPORT(gdi32,BOOL,DeleteObject,(void *))
IMPORT(gdi32,int,SetBkMode,(HDC,int))
IMPORT(gdi32,DWORD,SetTextColor,(HDC,DWORD))
IMPORT(gdi32,BOOL,TextOutW,(HDC,int,int,const WCHAR *,int))
#define WM_DESTROY 0x0002
#define WM_PAINT 0x000F
#define WM_MOUSEACTIVATE 0x0021
#define WM_TIMER 0x0113
#define WM_LBUTTONUP 0x0202
#define WM_HOTKEY 0x0312
#define MA_NOACTIVATE 3
#define VK_F12 0x7b
#define MOD_NOREPEAT 0x4000
#define VK_HOTKEY_ID 1
#define WS_POPUP 0x80000000u
#define WS_VISIBLE 0x10000000u
#define WS_EX_TOPMOST 0x00000008u
#define WS_EX_TOOLWINDOW 0x00000080u
#define WS_EX_NOACTIVATE 0x08000000u
#define SW_HIDE 0
#define SW_SHOWNOACTIVATE 4
#define COLOR_BG 0x00282628
#define COLOR_KEY 0x00434242
#define COLOR_ACCENT 0x009c6231
#define COLOR_TEXT 0x00ffffff
#define KEYEVENTF_KEYUP 0x0002
#define KEYEVENTF_UNICODE 0x0004
#define WAIT_OBJECT_0 0
#define STD_OUTPUT_HANDLE ((DWORD)-11)
static PROCESS_INFORMATION child;
static DWORD child_exit_code;
static HWND keyboard_window, panel_window, target_window;
static int panel_w, expanded, shifted;
static BOOL hotkey_registered, hotkey_down;
static HBRUSH bg_brush, key_brush, accent_brush;
static WCHAR child_target[4096];
/* CreateProcessW may modify its command line, so keep a private buffer. */
static WCHAR child_command_line[4096];
typedef struct { RECT r; int kind; WCHAR ch; } vk_key;
enum { VK_CHAR, VK_SHIFT, VK_BACKSPACE, VK_ENTER, VK_SPACE, VK_HIDE };
static vk_key keys[64];
static int key_count;
static int wlen(const WCHAR *s) { int n=0; while(s && s[n]) n++; return n; }
static int screen_metric(const WCHAR *name,int fallback) {
    WCHAR digits[16]={0};
    DWORD n=API(GetEnvironmentVariableW)(name,digits,16);
    if(n==0 || n>=16) return fallback;
    int value=0;
    for(DWORD i=0;i<n;i++) {
        if(digits[i]<'0' || digits[i]>'9') return fallback;
        value=value*10+(digits[i]-'0');
        if(value>8192) return fallback;
    }
    return value>=320?value:fallback;
}
static void logline(const char *s, DWORD len) {
    HANDLE h=API(GetStdHandle)(STD_OUTPUT_HANDLE); DWORD n=0;
    if(h) API(WriteFile)(h,s,len,&n,NULL);
}
static WCHAR shifted_char(WCHAR c) {
    if(c>='a' && c<='z') return c-'a'+'A';
    switch(c) {
    case '1':return '!'; case '2':return '@'; case '3':return '#'; case '4':return '$';
    case '5':return '%'; case '6':return '^'; case '7':return '&'; case '8':return '*';
    case '9':return '('; case '0':return ')'; case '-':return '_'; case '=':return '+';
    case '[':return '{'; case ']':return '}'; case ';':return ':'; case 39:return '"';
    case 92:return '|'; case ',':return '<'; case '.':return '>'; case '/':return '?';
    }
    return c;
}
static void send_key(WCHAR ch, BOOL unicode) {
    INPUT ev[2]={{0},{0}};
    HWND active=API(GetForegroundWindow)();
    if(active && active!=keyboard_window && active!=panel_window) target_window=active;
    if(target_window && target_window!=keyboard_window && target_window!=panel_window) API(SetForegroundWindow)(target_window);
    ev[0].type=ev[1].type=1;
    if(unicode) {
        ev[0].data.ki.wScan=ev[1].data.ki.wScan=ch;
        ev[0].data.ki.dwFlags=KEYEVENTF_UNICODE;
        ev[1].data.ki.dwFlags=KEYEVENTF_UNICODE|KEYEVENTF_KEYUP;
    } else {
        ev[0].data.ki.wVk=ev[1].data.ki.wVk=ch;
        ev[1].data.ki.dwFlags=KEYEVENTF_KEYUP;
    }
    if(API(SendInput)(2,ev,sizeof(INPUT))==2) logline("vk: key sent\n",13);
    else logline("vk: send failed\n",16);
}
static void add_key(int x,int y,int w,int h,int kind,WCHAR ch) {
    if(key_count>=64) return;
    keys[key_count].r=(RECT){x,y,x+w,y+h};
    keys[key_count].kind=kind;
    keys[key_count].ch=ch;
    key_count++;
}
static void add_row(const WCHAR *chars,int n,int row,int pad,int gap,int panel_w,int key_h) {
    int w=(panel_w-2*pad-(n-1)*gap)/n;
    int y=44+row*(key_h+gap);
    for(int i=0;i<n;i++) add_key(pad+i*(w+gap),y,w,key_h,VK_CHAR,chars[i]);
}
static void layout_keys(int panel_w) {
    const int pad=14,gap=7,kh=43;
    const WCHAR r1[]=W("1234567890-=");
    const WCHAR r2[]=W("qwertyuiop[]");
    const WCHAR r3[]=W("asdfghjkl;'\\");
    const WCHAR r4[]=W("zxcvbnm,./");
    key_count=0;
    add_row(r1,12,0,pad,gap,panel_w,kh);
    add_row(r2,12,1,pad,gap,panel_w,kh);
    add_row(r3,12,2,pad,gap,panel_w,kh);
    int w=(panel_w-2*pad-11*gap)/12, y=44+3*(kh+gap);
    add_key(pad,y,w,kh,VK_SHIFT,0);
    for(int i=0;i<10;i++) add_key(pad+(i+1)*(w+gap),y,w,kh,VK_CHAR,r4[i]);
    add_key(pad+11*(w+gap),y,w,kh,VK_BACKSPACE,0);
    y=44+4*(kh+gap);
    int side=(panel_w-2*pad)/6;
    add_key(pad,y,side,kh,VK_HIDE,0);
    add_key(pad+side+gap,y,panel_w-2*pad-2*side-2*gap,kh,VK_SPACE,0);
    add_key(panel_w-pad-side,y,side,kh,VK_ENTER,0);
}
static void show_panel(int show) {
    expanded=show;
    if(expanded) {
        API(ShowWindow)(panel_window,SW_SHOWNOACTIVATE);
        API(ShowWindow)(keyboard_window,SW_HIDE);
        API(InvalidateRect)(panel_window,NULL,1);
    } else {
        API(ShowWindow)(keyboard_window,SW_SHOWNOACTIVATE);
        API(ShowWindow)(panel_window,SW_HIDE);
        API(InvalidateRect)(keyboard_window,NULL,1);
    }
}
static void toggle_panel(void) {
    show_panel(!expanded);
    logline("vk: toggled\n",12);
}
static void draw_key(HDC dc,const vk_key *k) {
    int accent=k->kind==VK_ENTER || (k->kind==VK_SHIFT && shifted);
    API(FillRect)(dc,&k->r,accent?accent_brush:key_brush);
    WCHAR label[3]={0,0,0}; const WCHAR *words=NULL;
    switch(k->kind) {
    case VK_CHAR: label[0]=shifted?shifted_char(k->ch):k->ch; break;
    case VK_SHIFT: words=W("Shift"); break;
    case VK_BACKSPACE: words=W("Bksp"); break;
    case VK_ENTER: words=W("Enter"); break;
    case VK_SPACE: words=W("Space"); break;
    case VK_HIDE: words=W("Hide"); break;
    }
    if(!words) words=label;
    API(TextOutW)(dc,k->r.left+11,k->r.top+13,words,wlen(words));
}
static void activate_key(const vk_key *k) {
    switch(k->kind) {
    case VK_HIDE: show_panel(0); break;
    case VK_SHIFT: shifted=!shifted; API(InvalidateRect)(panel_window,NULL,0); break;
    case VK_BACKSPACE: send_key(8,0); break;
    case VK_ENTER: send_key(13,0); break;
    case VK_SPACE: send_key(' ',1); break;
    case VK_CHAR:
        send_key(shifted?shifted_char(k->ch):k->ch,1);
        if(shifted) { shifted=0; API(InvalidateRect)(panel_window,NULL,0); }
        break;
    }
}
static LRESULT WINAPI window_proc(HWND hwnd,UINT msg,WPARAM wp,LPARAM lp) {
    if(msg==WM_MOUSEACTIVATE) return MA_NOACTIVATE;
    if(msg==WM_HOTKEY && wp==VK_HOTKEY_ID) { toggle_panel(); return 0; }
    if(msg==WM_PAINT) {
        PAINTSTRUCT ps; RECT r; HDC dc=API(BeginPaint)(hwnd,&ps);
        API(GetClientRect)(hwnd,&r); API(FillRect)(dc,&r,bg_brush);
        API(SetBkMode)(dc,1); API(SetTextColor)(dc,COLOR_TEXT);
        API(TextOutW)(dc,14,hwnd==panel_window?14:13,W("Virtual Keyboard"),16);
        if(hwnd==panel_window) {
            const WCHAR *hint=W("Start hides; click the editor, then keys");
            API(TextOutW)(dc,230,14,hint,wlen(hint));
            for(int i=0;i<key_count;i++) draw_key(dc,&keys[i]);
        } else {
            API(TextOutW)(dc,155,13,W("Start"),5);
        }
        API(EndPaint)(hwnd,&ps); return 0;
    }
    if(msg==WM_LBUTTONUP) {
        if(hwnd==keyboard_window) { toggle_panel(); return 0; }
        int x=(int)(unsigned short)(lp&0xffff), y=(int)(unsigned short)((lp>>16)&0xffff);
        for(int i=0;i<key_count;i++) {
            RECT *r=&keys[i].r;
            if(x>=r->left && x<r->right && y>=r->top && y<r->bottom) {
                activate_key(&keys[i]); break;
            }
        }
        return 0;
    }
    if(msg==WM_TIMER) {
        if(!hotkey_registered) {
            BOOL down=(API(GetAsyncKeyState)(VK_F12)&0x8000)!=0;
            if(down && !hotkey_down) toggle_panel();
            hotkey_down=down;
        }
        HWND active=API(GetForegroundWindow)();
        if(active && active!=keyboard_window && active!=panel_window) target_window=active;
        if(child.hProcess && API(WaitForSingleObject)(child.hProcess,0)==WAIT_OBJECT_0) {
            DWORD code=1;
            if(!API(GetExitCodeProcess)(child.hProcess,&code)) logline("vk: exit code failed\n",21);
            child_exit_code=code;
            logline("vk: child exited\n",17); API(PostQuitMessage)((int)code);
        }
        return 0;
    }
    if(msg==WM_DESTROY) { API(PostQuitMessage)(0); return 0; }
    return API(DefWindowProcW)(hwnd,msg,wp,lp);
}
/* The shared PE writer's linker script uses this entry symbol. */
__attribute__((used)) void WINAPI winetest_entry(void) {
    DWORD n=API(GetEnvironmentVariableW)(W("JUKAHUB_VK_TARGET"),child_target,4096);
    const WCHAR *app=child_target;
    if(n>=4096) { logline("vk: target too long\n",20); API(ExitProcess)(3); }
#ifdef VK_PROOF
    if(n==0) app=W("Z:\\mnt\\SDCARD\\Apps\\JukaHub-Trimui\\data\\windows-apps\\Notepad++\\64-bit\\notepad++.exe");
#else
    if(n==0) { logline("vk: no target\n",14); API(ExitProcess)(3); }
#endif
    DWORD command_size=API(GetEnvironmentVariableW)(W("JUKAHUB_VK_CMDLINE"),child_command_line,4096);
    if(command_size>=4096) { logline("vk: command too long\n",21); API(ExitProcess)(7); }
    STARTUPINFOW si={0}; si.cb=sizeof(si);
    if(!API(CreateProcessW)(app,command_size?child_command_line:NULL,NULL,NULL,0,0,NULL,NULL,&si,&child)) {
        logline("vk: child failed\n",17); API(ExitProcess)(4);
    }
    API(CloseHandle)(child.hThread);
    logline("vk: child started\n",18);
    HINSTANCE module=API(GetModuleHandleW)(NULL);
    bg_brush=API(CreateSolidBrush)(COLOR_BG);
    key_brush=API(CreateSolidBrush)(COLOR_KEY);
    accent_brush=API(CreateSolidBrush)(COLOR_ACCENT);
    WNDCLASSEXW wc={0}; wc.cbSize=sizeof(wc); wc.lpfnWndProc=window_proc; wc.hInstance=module;
    wc.lpszClassName=W("JukaHubWineKeyboard"); wc.hbrBackground=bg_brush;
    if(!API(RegisterClassExW)(&wc)) { API(ExitProcess)(5); }
    /* WestonPack's Xwayland may report a desktop 32 px larger than the
       1280x720 framebuffer; its DISPLAY_* values name the visible surface. */
    int sw=screen_metric(W("DISPLAY_WIDTH"),API(GetSystemMetrics)(0));
    int sh=screen_metric(W("DISPLAY_HEIGHT"),API(GetSystemMetrics)(1));
    /* Wine's X11 popup has a 32 px outer frame on each side. Keep that
       frame inside the display so the client tab and keys can be clicked. */
    const int frame=32, tab_w=200, tab_h=46, panel_h=300;
    panel_w=sw-2*frame;
    layout_keys(panel_w);
    keyboard_window=API(CreateWindowExW)(WS_EX_TOPMOST|WS_EX_TOOLWINDOW|WS_EX_NOACTIVATE,wc.lpszClassName,
        W("Virtual Keyboard"),WS_POPUP|WS_VISIBLE,sw-tab_w-2*frame,sh-tab_h-2*frame,
        tab_w,tab_h,NULL,NULL,module,NULL);
    if(!keyboard_window) { API(ExitProcess)(6); }
    panel_window=API(CreateWindowExW)(WS_EX_TOPMOST|WS_EX_TOOLWINDOW|WS_EX_NOACTIVATE,wc.lpszClassName,
        W("Virtual Keyboard"),WS_POPUP,0,sh-panel_h-2*frame,panel_w,panel_h,NULL,NULL,module,NULL);
    if(!panel_window) { logline("vk: panel failed\n",17); API(ExitProcess)(6); }
    API(ShowWindow)(keyboard_window,SW_SHOWNOACTIVATE); API(UpdateWindow)(keyboard_window);
    hotkey_registered=API(RegisterHotKey)(keyboard_window,VK_HOTKEY_ID,MOD_NOREPEAT,VK_F12);
    if(!hotkey_registered) logline("vk: hotkey polling\n",19);
    API(SetTimer)(keyboard_window,1,50,NULL);
    logline("vk: window shown\n",17);
    MSG msg;
    while(API(GetMessageW)(&msg,NULL,0,0)>0) { API(TranslateMessage)(&msg); API(DispatchMessageW)(&msg); }
    if(hotkey_registered) API(UnregisterHotKey)(keyboard_window,VK_HOTKEY_ID);
    if(child.hProcess) API(CloseHandle)(child.hProcess);
    API(DeleteObject)(bg_brush); API(DeleteObject)(key_brush); API(DeleteObject)(accent_brush);
    API(ExitProcess)(child_exit_code);
}
