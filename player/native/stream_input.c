/* stream_input.c - the TrimUI-to-DualSense mapping (see stream_input.h). */
#include "stream_input.h"

#include <string.h>

#define BIT(input) (1u << (unsigned)(input))

static bool time_reached(uint32_t now, uint32_t deadline)
{
    return (int32_t)(now - deadline) >= 0;
}

static uint32_t elapsed(uint32_t now, uint32_t since)
{
    return now - since;
}

static void push_event(SiState *s, SiEventType type, int value)
{
    if(s->event_count == SI_EVENT_QUEUE) {
        /* Full: the oldest event goes, a newer one says more about now. */
        s->event_head = (s->event_head + 1) % SI_EVENT_QUEUE;
        s->event_count--;
    }
    unsigned slot = (s->event_head + s->event_count) % SI_EVENT_QUEUE;
    s->events[slot].type = type;
    s->events[slot].value = value;
    s->event_count++;
}

bool si_poll_event(SiState *s, SiEvent *event)
{
    if(!s->event_count) return false;
    *event = s->events[s->event_head];
    s->event_head = (s->event_head + 1) % SI_EVENT_QUEUE;
    s->event_count--;
    return true;
}

void si_init(SiState *s, SiSelectMode mode, bool dpad_stick, uint16_t touch_max_x, uint16_t touch_max_y)
{
    memset(s, 0, sizeof(*s));
    s->select_mode = mode;
    s->dpad_stick = dpad_stick;
    s->touch_max_x = touch_max_x ? touch_max_x : 1919;
    s->touch_max_y = touch_max_y ? touch_max_y : 1079;
}

void si_set_select_mode(SiState *s, SiSelectMode mode)
{
    s->select_mode = mode;
}

void si_set_dpad_stick(SiState *s, bool on)
{
    s->dpad_stick = on;
}

uint32_t si_select_output(SiSelectMode mode, bool alt)
{
    bool touchpad = (mode == SI_SELECT_TOUCHPAD) != alt;
    return touchpad ? SI_OUT_TOUCHPAD : SI_OUT_SHARE;
}

bool si_held(const SiState *s, SiInput input)
{
    return (s->held & BIT(input)) != 0;
}

static SiPad *find_pad(SiState *s, int32_t id, bool create)
{
    for(int i = 0; i < SI_MAX_PADS; i++)
        if(s->pads[i].used && s->pads[i].id == id) return &s->pads[i];
    if(!create) return NULL;
    for(int i = 0; i < SI_MAX_PADS; i++) {
        if(!s->pads[i].used) {
            memset(&s->pads[i], 0, sizeof(s->pads[i]));
            s->pads[i].used = true;
            s->pads[i].id = id;
            return &s->pads[i];
        }
    }
    return NULL;
}

bool si_pad_attach(SiState *s, int32_t id, uint32_t flags)
{
    SiPad *pad = find_pad(s, id, true);
    if(!pad) return false;
    pad->flags = flags;
    return true;
}

/* True when a pad other than except is the handheld's own. */
static bool builtin_present(const SiState *s, const SiPad *except)
{
    for(int i = 0; i < SI_MAX_PADS; i++) {
        const SiPad *pad = &s->pads[i];
        if(pad->used && pad != except && (pad->flags & SI_PAD_BUILTIN)) return true;
    }
    return false;
}

/* What a press means on this pad (stream_input.h): Select on a pad with its
 * own touchpad is Create, and an external pad's Guide is the console's PS
 * button, held straight through, while the handheld's own pad is there. */
static SiInput pad_input(const SiState *s, const SiPad *pad, SiInput input)
{
    if(input == SI_IN_BACK && (pad->flags & SI_PAD_TOUCHPAD)) return SI_IN_CREATE;
    if(input == SI_IN_GUIDE && !(pad->flags & SI_PAD_BUILTIN) && builtin_present(s, pad)) return SI_IN_PS;
    return input;
}

/* The largest deflection of one axis across every pad, so a resting pad never
 * cancels the one being played with. */
static int16_t merged_axis(const SiState *s, SiAxis axis)
{
    int16_t best = 0;
    int best_mag = 0;
    for(int i = 0; i < SI_MAX_PADS; i++) {
        if(!s->pads[i].used) continue;
        int v = s->pads[i].axis[axis];
        int mag = v < 0 ? -v : v;
        if(mag > best_mag) {
            best_mag = mag;
            best = (int16_t)v;
        }
    }
    return best;
}

static void finger_lift(SiState *s)
{
    if(!s->finger.active) return;
    s->finger.active = false;
    push_event(s, SI_EV_FINGER, 0);
}

static uint16_t clamp_u16(long value, uint16_t max)
{
    if(value < 0) return 0;
    if(value > (long)max) return max;
    return (uint16_t)value;
}

/* The Menu finger: down past SI_FINGER_ON, lifted under SI_FINGER_OFF or when
 * Menu goes up. It sits at the touchpad centre plus the stick's deflection. */
static void update_finger(SiState *s)
{
    if(!s->guide_down || s->guide_menu_fired || s->modal) {
        finger_lift(s);
        return;
    }
    long rx = merged_axis(s, SI_AXIS_RX);
    long ry = merged_axis(s, SI_AXIS_RY);
    long mag2 = rx * rx + ry * ry;
    if(!s->finger.active) {
        if(mag2 <= (long)SI_FINGER_ON * SI_FINGER_ON) return;
        s->finger.active = true;
        s->finger.serial = ++s->serial_next;
        s->guide_combo = true;
        push_event(s, SI_EV_FINGER, 1);
    } else if(mag2 < (long)SI_FINGER_OFF * SI_FINGER_OFF) {
        finger_lift(s);
        return;
    }
    long cx = s->touch_max_x / 2;
    long cy = s->touch_max_y / 2;
    s->finger.x = clamp_u16(cx + rx * cx / 32767, s->touch_max_x);
    s->finger.y = clamp_u16(cy + ry * cy / 32767, s->touch_max_y);
}

static void start_swipe(SiState *s, SiSwipeDir dir, uint32_t now)
{
    s->swipe_active = true;
    s->swipe_start = now;
    s->swipe_dir = dir;
    s->swipe_serial = ++s->serial_next;
    push_event(s, SI_EV_SWIPE, (int)dir);
}

static bool swipe_position(const SiState *s, uint32_t now, uint16_t *x, uint16_t *y)
{
    if(!s->swipe_active) return false;
    uint32_t t = elapsed(now, s->swipe_start);
    if(t >= SI_SWIPE_MS + SI_SWIPE_HOLD_MS) return false;
    if(t > SI_SWIPE_MS) t = SI_SWIPE_MS;
    long cx = s->touch_max_x / 2;
    long cy = s->touch_max_y / 2;
    long dx = 0, dy = 0;
    long span_x = (long)s->touch_max_x * 2 / 5;
    long span_y = (long)s->touch_max_y * 2 / 5;
    switch(s->swipe_dir) {
        case SI_SWIPE_UP: dy = -span_y; break;
        case SI_SWIPE_DOWN: dy = span_y; break;
        case SI_SWIPE_LEFT: dx = -span_x; break;
        case SI_SWIPE_RIGHT: dx = span_x; break;
    }
    /* From half a span before the centre to half a span past it. */
    long px = cx - dx / 2 + dx * (long)t / (long)SI_SWIPE_MS;
    long py = cy - dy / 2 + dy * (long)t / (long)SI_SWIPE_MS;
    *x = clamp_u16(px, s->touch_max_x);
    *y = clamp_u16(py, s->touch_max_y);
    return true;
}

bool si_synthetic_touch(const SiState *s, uint32_t now, uint16_t *x, uint16_t *y)
{
    if(s->modal) return false;
    if(s->finger.active) {
        *x = s->finger.x;
        *y = s->finger.y;
        return true;
    }
    return swipe_position(s, now, x, y);
}

static SiSwipeDir swipe_for(SiInput input)
{
    switch(input) {
        case SI_IN_UP: return SI_SWIPE_UP;
        case SI_IN_DOWN: return SI_SWIPE_DOWN;
        case SI_IN_LEFT: return SI_SWIPE_LEFT;
        default: return SI_SWIPE_RIGHT;
    }
}

static void on_press(SiState *s, SiInput input, uint32_t now)
{
    if(s->modal) return; /* tracked as held; nothing else while a screen is up */
    if(input == SI_IN_GUIDE) {
        s->guide_down = true;
        s->guide_since = now;
        s->guide_combo = false;
        s->guide_menu_fired = false;
        return;
    }
    if(!s->guide_down || s->guide_menu_fired) return;
    switch(input) {
        case SI_IN_BACK:
            s->select_alt = true;
            s->guide_combo = true;
            push_event(s, SI_EV_ALT_BUTTON, (int)si_select_output(s->select_mode, true));
            break;
        case SI_IN_R3:
            s->r3_alt = true;
            s->guide_combo = true;
            push_event(s, SI_EV_TOUCH_CLICK, 0);
            break;
        case SI_IN_L3:
            s->latched |= BIT(input);
            s->guide_combo = true;
            s->dpad_stick = !s->dpad_stick;
            push_event(s, SI_EV_DPAD_STICK, s->dpad_stick ? 1 : 0);
            break;
        case SI_IN_UP:
        case SI_IN_DOWN:
        case SI_IN_LEFT:
        case SI_IN_RIGHT:
            s->latched |= BIT(input);
            s->guide_combo = true;
            start_swipe(s, swipe_for(input), now);
            break;
        default:
            /* Unassigned with Menu: the button still plays, and Menu is no
             * longer a tap. */
            s->guide_combo = true;
            break;
    }
}

static void on_release(SiState *s, SiInput input, uint32_t now)
{
    uint32_t bit = BIT(input);
    if(input == SI_IN_GUIDE) {
        bool tap = s->guide_down && !s->guide_combo && !s->guide_menu_fired &&
                   !s->modal && !s->suppress_tap && !(s->swallow & bit);
        if(tap) {
            s->ps_pending = true;
            s->ps_until = now + SI_PS_PULSE_MS;
            push_event(s, SI_EV_PS, 0);
        }
        s->guide_down = false;
        finger_lift(s);
    } else if(input == SI_IN_BACK) {
        s->select_alt = false;
    } else if(input == SI_IN_R3) {
        s->r3_alt = false;
    }
    s->latched &= ~bit;
    s->swallow &= ~bit;
}

/* Start + Select held on one pad. A Start on one pad and a Select on another
 * are two players' buttons, not the quit combo. */
static bool quit_combo_held(const SiState *s)
{
    for(int i = 0; i < SI_MAX_PADS; i++) {
        const SiPad *pad = &s->pads[i];
        if(pad->used && (pad->held & BIT(SI_IN_START)) && (pad->held & (BIT(SI_IN_BACK) | BIT(SI_IN_CREATE))))
            return true;
    }
    return false;
}

static void update_quit_hold(SiState *s, uint32_t now)
{
    bool both = quit_combo_held(s);
    if(both && !s->quit_hold) {
        s->quit_hold = true;
        s->quit_fired = false;
        s->quit_since = now;
        /* Both halves of the combo stay off the console until each is let go:
         * with only the hold muting them, letting go of Select first sent
         * Options, and letting go of Start first the touchpad (or Create). Only
         * what is held is latched: a latch on a button that is up would never
         * be cleared (a release clears it). */
        s->latched |= s->held & (BIT(SI_IN_START) | BIT(SI_IN_BACK) | BIT(SI_IN_CREATE));
    } else if(!both) {
        s->quit_hold = false;
        s->quit_fired = false;
    }
}

static void recompute(SiState *s, uint32_t now)
{
    uint32_t held = 0;
    for(int i = 0; i < SI_MAX_PADS; i++)
        if(s->pads[i].used) held |= s->pads[i].held;
    uint32_t pressed = held & ~s->held;
    uint32_t released = s->held & ~held;
    s->held = held;
    for(int input = 0; input < SI_IN_COUNT; input++)
        if(released & BIT(input)) on_release(s, (SiInput)input, now);
    for(int input = 0; input < SI_IN_COUNT; input++)
        if(pressed & BIT(input)) on_press(s, (SiInput)input, now);
    update_quit_hold(s, now);
    update_finger(s);
}

void si_pad_detach(SiState *s, int32_t id, uint32_t now)
{
    SiPad *pad = find_pad(s, id, false);
    if(!pad) return;
    memset(pad, 0, sizeof(*pad));
    s->suppress_tap = true;
    recompute(s, now);
    s->suppress_tap = false;
}

void si_pad_button(SiState *s, int32_t id, SiInput input, bool down, uint32_t now)
{
    if((int)input < 0 || input >= SI_IN_COUNT) return;
    SiPad *pad = find_pad(s, id, true);
    if(!pad) return;
    SiInput mapped = pad_input(s, pad, input);
    if(down) {
        pad->held |= BIT(mapped);
    } else {
        pad->held &= ~BIT(mapped);
        /* The built-in pad can come or go while Guide is down, which changes
         * what Guide means: its release lets go of either meaning. */
        if(input == SI_IN_GUIDE) pad->held &= ~(BIT(SI_IN_GUIDE) | BIT(SI_IN_PS));
    }
    recompute(s, now);
}

void si_pad_axis(SiState *s, int32_t id, SiAxis axis, int16_t value, uint32_t now)
{
    (void)now;
    if((int)axis < 0 || axis >= SI_AXIS_COUNT) return;
    SiPad *pad = find_pad(s, id, true);
    if(!pad) return;
    if((axis == SI_AXIS_L2 || axis == SI_AXIS_R2) && value < 0) value = 0;
    pad->axis[axis] = value;
    update_finger(s);
}

void si_pad_touch(SiState *s, int32_t id, int finger, bool down, float x, float y, uint32_t now)
{
    (void)now;
    if(finger < 0 || finger >= SI_MAX_TOUCHES) return;
    SiPad *pad = find_pad(s, id, true);
    if(!pad) return;
    if(x < 0.0f) x = 0.0f;
    if(x > 1.0f) x = 1.0f;
    if(y < 0.0f) y = 0.0f;
    if(y > 1.0f) y = 1.0f;
    if(down && !pad->touch_down[finger]) pad->touch_serial[finger] = ++s->serial_next;
    pad->touch_down[finger] = down;
    pad->touch_x[finger] = (uint16_t)(x * (float)s->touch_max_x + 0.5f);
    pad->touch_y[finger] = (uint16_t)(y * (float)s->touch_max_y + 0.5f);
}

void si_set_modal(SiState *s, bool modal, uint32_t now)
{
    if(modal == s->modal) return;
    s->modal = modal;
    if(modal) {
        finger_lift(s);
        s->swipe_active = false;
        s->ps_pending = false;
        s->guide_down = false;
        s->select_alt = false;
        s->r3_alt = false;
        s->latched = 0;
    } else {
        /* Whatever closed the screen is still held: it must not reach the
         * console when the stream resumes. */
        s->swallow = s->held;
    }
    update_quit_hold(s, now);
}

void si_hold_ps(SiState *s, uint32_t now, uint32_t ms)
{
    if(s->modal || !ms) return;
    s->ps_pending = true;
    s->ps_until = now + ms;
}

void si_tick(SiState *s, uint32_t now)
{
    if(s->guide_down && !s->guide_combo && !s->guide_menu_fired && !s->modal &&
       elapsed(now, s->guide_since) >= SI_GUIDE_MENU_MS) {
        s->guide_menu_fired = true;
        finger_lift(s);
        push_event(s, SI_EV_OPEN_MENU, 0);
    }
    if(s->quit_hold && !s->quit_fired && elapsed(now, s->quit_since) >= SI_QUIT_HOLD_MS) {
        s->quit_fired = true;
        push_event(s, SI_EV_QUIT, 0);
    }
    if(s->ps_pending && time_reached(now, s->ps_until)) s->ps_pending = false;
    if(s->swipe_active && elapsed(now, s->swipe_start) >= SI_SWIPE_MS + SI_SWIPE_HOLD_MS)
        s->swipe_active = false;
    update_finger(s);
}

float si_quit_progress(const SiState *s, uint32_t now)
{
    if(!s->quit_hold) return 0.0f;
    float p = (float)elapsed(now, s->quit_since) / (float)SI_QUIT_HOLD_MS;
    return p > 1.0f ? 1.0f : p;
}

static int16_t stronger(int16_t a, int b)
{
    int ma = a < 0 ? -a : a;
    int mb = b < 0 ? -b : b;
    return mb > ma ? (int16_t)b : a;
}

void si_output(const SiState *s, uint32_t now, SiOutput *out)
{
    memset(out, 0, sizeof(*out));
    if(s->modal) return;
    uint32_t held = s->held & ~s->swallow & ~s->latched;
    uint32_t b = 0;
    if(held & BIT(SI_IN_SOUTH)) b |= SI_OUT_CROSS;
    if(held & BIT(SI_IN_EAST)) b |= SI_OUT_MOON;
    if(held & BIT(SI_IN_WEST)) b |= SI_OUT_BOX;
    if(held & BIT(SI_IN_NORTH)) b |= SI_OUT_PYRAMID;
    if(held & BIT(SI_IN_L1)) b |= SI_OUT_L1;
    if(held & BIT(SI_IN_R1)) b |= SI_OUT_R1;
    if(held & BIT(SI_IN_L3)) b |= SI_OUT_L3;
    if(held & BIT(SI_IN_R3)) b |= s->r3_alt ? SI_OUT_TOUCHPAD : SI_OUT_R3;
    if(held & BIT(SI_IN_TOUCHPAD)) b |= SI_OUT_TOUCHPAD;
    if(held & BIT(SI_IN_PS)) b |= SI_OUT_PS;
    if(!s->quit_hold) {
        if(held & BIT(SI_IN_START)) b |= SI_OUT_OPTIONS;
        if(held & BIT(SI_IN_BACK)) b |= si_select_output(s->select_mode, s->select_alt);
        if(held & BIT(SI_IN_CREATE)) b |= SI_OUT_SHARE;
    }
    /* The D-pad mode is for the stickless handhelds' own D-pad: an external
     * pad's D-pad stays a D-pad while the built-in pad is there. */
    const uint32_t dpad_bits = BIT(SI_IN_UP) | BIT(SI_IN_DOWN) | BIT(SI_IN_LEFT) | BIT(SI_IN_RIGHT);
    bool any_builtin = builtin_present(s, NULL);
    uint32_t stick_dpad = 0, plain_dpad = 0;
    for(int i = 0; i < SI_MAX_PADS; i++) {
        const SiPad *pad = &s->pads[i];
        if(!pad->used) continue;
        uint32_t dpad = pad->held & dpad_bits & ~s->swallow & ~s->latched;
        if(s->dpad_stick && (!any_builtin || (pad->flags & SI_PAD_BUILTIN))) stick_dpad |= dpad;
        else plain_dpad |= dpad;
    }
    if(plain_dpad & BIT(SI_IN_UP)) b |= SI_OUT_DPAD_UP;
    if(plain_dpad & BIT(SI_IN_DOWN)) b |= SI_OUT_DPAD_DOWN;
    if(plain_dpad & BIT(SI_IN_LEFT)) b |= SI_OUT_DPAD_LEFT;
    if(plain_dpad & BIT(SI_IN_RIGHT)) b |= SI_OUT_DPAD_RIGHT;
    if(s->ps_pending && !time_reached(now, s->ps_until)) b |= SI_OUT_PS;
    out->buttons = b;

    out->lx = merged_axis(s, SI_AXIS_LX);
    out->ly = merged_axis(s, SI_AXIS_LY);
    if(stick_dpad) {
        int dx = ((stick_dpad & BIT(SI_IN_RIGHT)) ? 32767 : 0) - ((stick_dpad & BIT(SI_IN_LEFT)) ? 32767 : 0);
        int dy = ((stick_dpad & BIT(SI_IN_DOWN)) ? 32767 : 0) - ((stick_dpad & BIT(SI_IN_UP)) ? 32767 : 0);
        out->lx = stronger(out->lx, dx);
        out->ly = stronger(out->ly, dy);
    }
    if(!s->guide_down || s->guide_menu_fired) {
        out->rx = merged_axis(s, SI_AXIS_RX);
        out->ry = merged_axis(s, SI_AXIS_RY);
    }
    int16_t l2 = merged_axis(s, SI_AXIS_L2);
    int16_t r2 = merged_axis(s, SI_AXIS_R2);
    out->l2 = l2 > 0 ? (uint8_t)(l2 >> 7) : 0;
    out->r2 = r2 > 0 ? (uint8_t)(r2 >> 7) : 0;

    /* A real touchpad wins; otherwise slot 0 is the Menu finger, slot 1 a swipe. */
    int slot = 0;
    for(int i = 0; i < SI_MAX_PADS && slot < SI_MAX_TOUCHES; i++) {
        const SiPad *pad = &s->pads[i];
        if(!pad->used) continue;
        for(int f = 0; f < SI_MAX_TOUCHES && slot < SI_MAX_TOUCHES; f++) {
            if(!pad->touch_down[f]) continue;
            out->touch[slot].active = true;
            out->touch[slot].x = pad->touch_x[f];
            out->touch[slot].y = pad->touch_y[f];
            out->touch[slot].serial = pad->touch_serial[f];
            slot++;
        }
    }
    if(slot == 0) {
        if(s->finger.active) out->touch[0] = s->finger;
        uint16_t x, y;
        if(swipe_position(s, now, &x, &y)) {
            out->touch[1].active = true;
            out->touch[1].x = x;
            out->touch[1].y = y;
            out->touch[1].serial = s->swipe_serial;
        }
    }
}
