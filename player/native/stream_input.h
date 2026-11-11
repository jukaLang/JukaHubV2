/* stream_input.h - how a TrimUI handheld's buttons become a DualSense.
 *
 * The mapping lives here, apart from SDL and chiaki, so it can be unit tested
 * on any machine (tests/test_stream_input.c). chiaki-stream.c feeds it pad
 * events (any number of pads, identified by the caller's id), calls
 * si_tick() every loop and copies si_output() into chiaki's controller state.
 *
 * Inputs are SDL GameController POSITIONS: the firmware pad "TRIMUI Player1"
 * reports the Xbox way, so SOUTH is the bottom face button (printed "B" on a
 * TrimUI), EAST the right one ("A"), WEST the left ("Y"), NORTH the top ("X").
 * The PlayStation buttons are positional as well: bottom = Cross.
 *
 *   Menu tap                 PS button (sent on release, held ~150 ms)
 *   Select                   touchpad click, or Create (select mode)
 *   Menu + Select            the other one of the two
 *   Menu + D-pad             touchpad swipe in that direction
 *   Menu + right stick       a finger on the touchpad that follows the stick
 *   Menu + R3                touchpad click
 *   Menu + L3                D-pad drives the left stick (toggle)
 *   hold Menu alone          SI_EV_OPEN_MENU (the stream menu)
 *   hold Start + Select      SI_EV_QUIT (both on one pad); neither half reaches
 *                            the console again until that button is let go
 *
 * The stream menu's "Hold PS (Home)" holds PS for SI_PS_HOLD_MS (si_hold_ps):
 * the long press (PS5 home screen, PS4 quick menu) a Menu tap cannot give.
 *
 * That layout is the handheld's own pad's (SI_PAD_BUILTIN). A Bluetooth or USB
 * pad keeps its own buttons while the built-in pad is attached: its PS / Guide
 * button is the console's PS button for as long as it is held (the stream menu
 * stays on the handheld's Menu key), its D-pad is always a D-pad (the D-pad
 * mode is for the stickless handhelds' own D-pad), and a pad with its own
 * touchpad (SI_PAD_TOUCHPAD: DualShock 4, DualSense) sends Create from its
 * Create/Share button and the touchpad click from its touchpad. When no pad is
 * marked built-in (a desktop, or a pad SDL could not identify) every pad gets
 * the handheld layout, as before.
 *
 * While a modal screen is up (si_set_modal) the console gets an idle pad; the
 * buttons still held when it closes are ignored until they are released, so the
 * press that closed a menu never reaches the game.
 */
#ifndef JUKAHUB_STREAM_INPUT_H
#define JUKAHUB_STREAM_INPUT_H

#include <stdbool.h>
#include <stdint.h>

#define SI_MAX_PADS 6
#define SI_MAX_TOUCHES 2
#define SI_EVENT_QUEUE 16

/* Timings, in milliseconds. */
#define SI_GUIDE_MENU_MS 900u   /* hold Menu this long, alone, for the stream menu */
#define SI_PS_PULSE_MS 150u     /* how long a Menu tap holds the PS button */
#define SI_PS_HOLD_MS 1200u     /* the stream menu's "Hold PS": a long press */
#define SI_QUIT_HOLD_MS 1200u   /* Start + Select */
#define SI_SWIPE_MS 160u        /* a synthetic swipe's travel time */
#define SI_SWIPE_HOLD_MS 40u    /* the finger rests at the end before it lifts */

/* Right-stick deflection that puts the Menu finger down / lifts it again. */
#define SI_FINGER_ON 11469      /* 35% of 32767 */
#define SI_FINGER_OFF 7209      /* 22% */

/* Output button bits. They mirror chiaki's ChiakiControllerButton values, but
 * chiaki-stream.c's to_chiaki_buttons() maps them bit by bit, so a differing
 * chiaki header cannot scramble the buttons. */
#define SI_OUT_CROSS (1u << 0)
#define SI_OUT_MOON (1u << 1)
#define SI_OUT_BOX (1u << 2)
#define SI_OUT_PYRAMID (1u << 3)
#define SI_OUT_DPAD_LEFT (1u << 4)
#define SI_OUT_DPAD_RIGHT (1u << 5)
#define SI_OUT_DPAD_UP (1u << 6)
#define SI_OUT_DPAD_DOWN (1u << 7)
#define SI_OUT_L1 (1u << 8)
#define SI_OUT_R1 (1u << 9)
#define SI_OUT_L3 (1u << 10)
#define SI_OUT_R3 (1u << 11)
#define SI_OUT_OPTIONS (1u << 12)
#define SI_OUT_SHARE (1u << 13)
#define SI_OUT_TOUCHPAD (1u << 14)
#define SI_OUT_PS (1u << 15)

typedef enum {
    SI_IN_SOUTH = 0,
    SI_IN_EAST,
    SI_IN_WEST,
    SI_IN_NORTH,
    SI_IN_L1,
    SI_IN_R1,
    SI_IN_L3,
    SI_IN_R3,
    SI_IN_START,
    SI_IN_BACK,
    SI_IN_GUIDE,
    SI_IN_UP,
    SI_IN_DOWN,
    SI_IN_LEFT,
    SI_IN_RIGHT,
    SI_IN_TOUCHPAD, /* a real touchpad click (DualSense / DualShock 4) */
    /* What si_pad_button turns an external pad's buttons into (see the top):
     * callers keep sending SI_IN_GUIDE and SI_IN_BACK. */
    SI_IN_PS,     /* a PS / Guide button held straight through */
    SI_IN_CREATE, /* Create / Share of a pad with its own touchpad */
    SI_IN_COUNT
} SiInput;

/* si_pad_attach flags. */
#define SI_PAD_BUILTIN (1u << 0)  /* the handheld's own pad: Menu key, D-pad mode */
#define SI_PAD_TOUCHPAD (1u << 1) /* has its own touchpad: Select is Create */

typedef enum {
    SI_AXIS_LX = 0,
    SI_AXIS_LY,
    SI_AXIS_RX,
    SI_AXIS_RY,
    SI_AXIS_L2, /* 0..32767 */
    SI_AXIS_R2,
    SI_AXIS_COUNT
} SiAxis;

typedef enum {
    SI_SELECT_TOUCHPAD = 0,
    SI_SELECT_CREATE = 1
} SiSelectMode;

typedef enum {
    SI_SWIPE_UP = 0,
    SI_SWIPE_DOWN,
    SI_SWIPE_LEFT,
    SI_SWIPE_RIGHT
} SiSwipeDir;

typedef enum {
    SI_EV_NONE = 0,
    SI_EV_OPEN_MENU,   /* Menu held alone */
    SI_EV_QUIT,        /* Start + Select held */
    SI_EV_DPAD_STICK,  /* value: the new D-pad mode, 1 = left stick */
    SI_EV_SWIPE,       /* value: SiSwipeDir */
    SI_EV_ALT_BUTTON,  /* value: the SI_OUT_ bit Menu + Select sent */
    SI_EV_TOUCH_CLICK, /* Menu + R3 */
    SI_EV_FINGER,      /* value: 1 the Menu finger went down, 0 it lifted */
    SI_EV_PS           /* a Menu tap sent the PS button */
} SiEventType;

typedef struct {
    SiEventType type;
    int value;
} SiEvent;

typedef struct {
    bool active;
    uint16_t x, y;
    uint32_t serial; /* changes whenever a new touch begins in this slot */
} SiTouch;

typedef struct {
    uint32_t buttons; /* SI_OUT_ bits */
    uint8_t l2, r2;
    int16_t lx, ly, rx, ry;
    SiTouch touch[SI_MAX_TOUCHES];
} SiOutput;

typedef struct {
    bool used;
    int32_t id;
    uint32_t flags; /* SI_PAD_ bits */
    uint32_t held;  /* bit (1 << SiInput) per held input */
    int16_t axis[SI_AXIS_COUNT];
    bool touch_down[SI_MAX_TOUCHES];
    uint16_t touch_x[SI_MAX_TOUCHES], touch_y[SI_MAX_TOUCHES];
    uint32_t touch_serial[SI_MAX_TOUCHES];
} SiPad;

typedef struct {
    SiPad pads[SI_MAX_PADS];

    SiSelectMode select_mode;
    bool dpad_stick;
    uint16_t touch_max_x, touch_max_y;
    bool modal;

    uint32_t held;    /* every pad's held inputs, OR'd */
    uint32_t swallow; /* held when a modal screen closed; ignored until released */
    uint32_t latched; /* presses that became a Menu combo; their own output stays off */
    bool suppress_tap;

    bool guide_down;
    bool guide_combo;
    bool guide_menu_fired;
    uint32_t guide_since;
    bool select_alt;
    bool r3_alt;

    bool ps_pending;
    uint32_t ps_until;

    bool quit_hold;
    bool quit_fired;
    uint32_t quit_since;

    SiTouch finger;
    bool swipe_active;
    uint32_t swipe_start;
    uint32_t swipe_serial;
    SiSwipeDir swipe_dir;
    uint32_t serial_next;

    SiEvent events[SI_EVENT_QUEUE];
    unsigned event_head;
    unsigned event_count;
} SiState;

void si_init(SiState *s, SiSelectMode mode, bool dpad_stick, uint16_t touch_max_x, uint16_t touch_max_y);
void si_set_select_mode(SiState *s, SiSelectMode mode);
void si_set_dpad_stick(SiState *s, bool on);
void si_set_modal(SiState *s, bool modal, uint32_t now);
/* Holds the PS button for ms from now (the stream menu's "Hold PS"). Ignored
 * while a modal screen is up: call it after the menu has closed. */
void si_hold_ps(SiState *s, uint32_t now, uint32_t ms);

/* Pads are created on first use (with no flags); attaching sets a pad's SI_PAD_
 * flags and fails only when the table is full. Detaching releases everything
 * a pad held without that release counting as a Menu tap. */
bool si_pad_attach(SiState *s, int32_t id, uint32_t flags);
void si_pad_detach(SiState *s, int32_t id, uint32_t now);
void si_pad_button(SiState *s, int32_t id, SiInput input, bool down, uint32_t now);
void si_pad_axis(SiState *s, int32_t id, SiAxis axis, int16_t value, uint32_t now);
/* A real touchpad finger, x and y in 0..1 of the pad's surface. */
void si_pad_touch(SiState *s, int32_t id, int finger, bool down, float x, float y, uint32_t now);

void si_tick(SiState *s, uint32_t now);
void si_output(const SiState *s, uint32_t now, SiOutput *out);
bool si_poll_event(SiState *s, SiEvent *event);

/* 0 when Start + Select are not held, else 0..1 of the quit hold. */
float si_quit_progress(const SiState *s, uint32_t now);
bool si_held(const SiState *s, SiInput input);
/* The touchpad / Create bit Select sends in a mode, or Menu + Select (alt). */
uint32_t si_select_output(SiSelectMode mode, bool alt);
/* True while a synthetic touch (Menu finger or swipe) is on the touchpad. */
bool si_synthetic_touch(const SiState *s, uint32_t now, uint16_t *x, uint16_t *y);

#endif
