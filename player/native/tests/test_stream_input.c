/* Host tests for stream_input.c:
 *   cc -std=c11 -Wall -Wextra -I.. test_stream_input.c ../stream_input.c -o t && ./t */
#include "../stream_input.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static int failures, checks;

#define CHECK(cond) do { checks++; if(!(cond)) { failures++; \
    fprintf(stderr, "%s:%d: CHECK(%s) failed\n", __FILE__, __LINE__, #cond); } } while(0)

static SiOutput out_at(const SiState *s, uint32_t now)
{
    SiOutput out;
    si_output(s, now, &out);
    return out;
}

static int count_events(SiState *s, SiEventType type, int *last_value)
{
    SiEvent ev;
    int n = 0;
    while(si_poll_event(s, &ev)) {
        if(ev.type == type) {
            n++;
            if(last_value) *last_value = ev.value;
        }
    }
    return n;
}

static void run_ticks(SiState *s, uint32_t from, uint32_t to)
{
    for(uint32_t t = from; t <= to; t += 5) si_tick(s, t);
}

static void test_face_buttons_positional(void)
{
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_pad_button(&s, 1, SI_IN_SOUTH, true, 0);
    si_pad_button(&s, 1, SI_IN_EAST, true, 0);
    SiOutput o = out_at(&s, 1);
    CHECK(o.buttons == (SI_OUT_CROSS | SI_OUT_MOON));
    si_pad_button(&s, 1, SI_IN_SOUTH, false, 2);
    si_pad_button(&s, 1, SI_IN_EAST, false, 2);
    si_pad_button(&s, 1, SI_IN_WEST, true, 3);
    si_pad_button(&s, 1, SI_IN_NORTH, true, 3);
    o = out_at(&s, 4);
    CHECK(o.buttons == (SI_OUT_BOX | SI_OUT_PYRAMID));
    si_pad_button(&s, 1, SI_IN_WEST, false, 5);
    si_pad_button(&s, 1, SI_IN_NORTH, false, 5);
    si_pad_button(&s, 1, SI_IN_L1, true, 6);
    si_pad_button(&s, 1, SI_IN_R1, true, 6);
    si_pad_button(&s, 1, SI_IN_L3, true, 6);
    si_pad_button(&s, 1, SI_IN_R3, true, 6);
    si_pad_button(&s, 1, SI_IN_START, true, 6);
    o = out_at(&s, 7);
    CHECK(o.buttons == (SI_OUT_L1 | SI_OUT_R1 | SI_OUT_L3 | SI_OUT_R3 | SI_OUT_OPTIONS));
}

static void test_select_modes(void)
{
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_pad_button(&s, 1, SI_IN_BACK, true, 0);
    CHECK(out_at(&s, 1).buttons == SI_OUT_TOUCHPAD);
    si_pad_button(&s, 1, SI_IN_BACK, false, 2);
    CHECK(out_at(&s, 3).buttons == 0);
    si_set_select_mode(&s, SI_SELECT_CREATE);
    si_pad_button(&s, 1, SI_IN_BACK, true, 4);
    CHECK(out_at(&s, 5).buttons == SI_OUT_SHARE);
    CHECK(si_select_output(SI_SELECT_TOUCHPAD, false) == SI_OUT_TOUCHPAD);
    CHECK(si_select_output(SI_SELECT_TOUCHPAD, true) == SI_OUT_SHARE);
    CHECK(si_select_output(SI_SELECT_CREATE, true) == SI_OUT_TOUCHPAD);
}

static void test_menu_tap_is_ps(void)
{
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_pad_button(&s, 1, SI_IN_GUIDE, true, 1000);
    run_ticks(&s, 1000, 1100);
    CHECK(out_at(&s, 1100).buttons == 0); /* nothing while Menu is down */
    si_pad_button(&s, 1, SI_IN_GUIDE, false, 1120);
    CHECK(out_at(&s, 1121).buttons == SI_OUT_PS);
    si_tick(&s, 1121 + SI_PS_PULSE_MS - 10);
    CHECK(out_at(&s, 1121 + SI_PS_PULSE_MS - 10).buttons == SI_OUT_PS);
    si_tick(&s, 1120 + SI_PS_PULSE_MS);
    CHECK(out_at(&s, 1120 + SI_PS_PULSE_MS).buttons == 0);
    CHECK(count_events(&s, SI_EV_PS, NULL) == 1);
    CHECK(count_events(&s, SI_EV_OPEN_MENU, NULL) == 0);
}

static void test_menu_hold_opens_menu_without_ps(void)
{
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_pad_button(&s, 1, SI_IN_GUIDE, true, 0);
    run_ticks(&s, 0, SI_GUIDE_MENU_MS - 5);
    CHECK(count_events(&s, SI_EV_OPEN_MENU, NULL) == 0);
    run_ticks(&s, SI_GUIDE_MENU_MS, SI_GUIDE_MENU_MS + 50);
    CHECK(count_events(&s, SI_EV_OPEN_MENU, NULL) == 1);
    si_set_modal(&s, true, SI_GUIDE_MENU_MS + 60);
    CHECK(out_at(&s, SI_GUIDE_MENU_MS + 61).buttons == 0);
    si_pad_button(&s, 1, SI_IN_GUIDE, false, SI_GUIDE_MENU_MS + 300);
    si_tick(&s, SI_GUIDE_MENU_MS + 301);
    CHECK(count_events(&s, SI_EV_PS, NULL) == 0);
    CHECK(out_at(&s, SI_GUIDE_MENU_MS + 302).buttons == 0);
}

static void test_menu_combo_cancels_tap(void)
{
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_pad_button(&s, 1, SI_IN_GUIDE, true, 0);
    si_pad_button(&s, 1, SI_IN_SOUTH, true, 50); /* unassigned: passes through */
    CHECK(out_at(&s, 51).buttons == SI_OUT_CROSS);
    si_pad_button(&s, 1, SI_IN_SOUTH, false, 80);
    si_pad_button(&s, 1, SI_IN_GUIDE, false, 100);
    run_ticks(&s, 100, 2000);
    CHECK(count_events(&s, SI_EV_PS, NULL) == 0);
    CHECK(count_events(&s, SI_EV_OPEN_MENU, NULL) == 0);
}

static void test_menu_select_is_the_other_button(void)
{
    SiState s;
    int value = 0;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_pad_button(&s, 1, SI_IN_GUIDE, true, 0);
    si_pad_button(&s, 1, SI_IN_BACK, true, 40);
    CHECK(out_at(&s, 41).buttons == SI_OUT_SHARE);
    CHECK(count_events(&s, SI_EV_ALT_BUTTON, &value) == 1);
    CHECK(value == (int)SI_OUT_SHARE);
    /* Menu let go first: this Select press stays Create until it is released. */
    si_pad_button(&s, 1, SI_IN_GUIDE, false, 60);
    CHECK(out_at(&s, 61).buttons == SI_OUT_SHARE);
    si_pad_button(&s, 1, SI_IN_BACK, false, 80);
    CHECK(out_at(&s, 81).buttons == 0);
    run_ticks(&s, 80, 400);
    CHECK(count_events(&s, SI_EV_PS, NULL) == 0);
    /* The next Select alone is the touchpad again. */
    si_pad_button(&s, 1, SI_IN_BACK, true, 500);
    CHECK(out_at(&s, 501).buttons == SI_OUT_TOUCHPAD);
    /* Create mode flips both. */
    si_pad_button(&s, 1, SI_IN_BACK, false, 510);
    si_set_select_mode(&s, SI_SELECT_CREATE);
    si_pad_button(&s, 1, SI_IN_GUIDE, true, 600);
    si_pad_button(&s, 1, SI_IN_BACK, true, 620);
    CHECK(out_at(&s, 621).buttons == SI_OUT_TOUCHPAD);
}

static void test_menu_dpad_swipes(void)
{
    SiState s;
    int dir = -1;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_pad_button(&s, 1, SI_IN_GUIDE, true, 0);
    si_pad_button(&s, 1, SI_IN_UP, true, 30);
    CHECK(count_events(&s, SI_EV_SWIPE, &dir) == 1);
    CHECK(dir == SI_SWIPE_UP);
    SiOutput first = out_at(&s, 30);
    CHECK((first.buttons & SI_OUT_DPAD_UP) == 0); /* latched: no D-pad press */
    CHECK(first.touch[1].active);
    uint16_t y0 = first.touch[1].y;
    SiOutput mid = out_at(&s, 30 + SI_SWIPE_MS / 2);
    SiOutput end = out_at(&s, 30 + SI_SWIPE_MS);
    CHECK(mid.touch[1].active && end.touch[1].active);
    CHECK(mid.touch[1].y < y0 && end.touch[1].y < mid.touch[1].y); /* up = smaller y */
    CHECK(mid.touch[1].x == 1919 / 2);
    CHECK(first.touch[1].serial == end.touch[1].serial);
    si_tick(&s, 30 + SI_SWIPE_MS + SI_SWIPE_HOLD_MS);
    CHECK(!out_at(&s, 30 + SI_SWIPE_MS + SI_SWIPE_HOLD_MS).touch[1].active);
    /* Still holding Up after Menu goes: no D-pad press appears. */
    si_pad_button(&s, 1, SI_IN_GUIDE, false, 300);
    CHECK((out_at(&s, 301).buttons & SI_OUT_DPAD_UP) == 0);
    si_pad_button(&s, 1, SI_IN_UP, false, 320);
    si_pad_button(&s, 1, SI_IN_UP, true, 400);
    CHECK(out_at(&s, 401).buttons == SI_OUT_DPAD_UP);
    CHECK(count_events(&s, SI_EV_PS, NULL) == 0);
    /* A second swipe gets a new serial. */
    si_pad_button(&s, 1, SI_IN_UP, false, 410);
    si_pad_button(&s, 1, SI_IN_GUIDE, true, 500);
    si_pad_button(&s, 1, SI_IN_RIGHT, true, 510);
    SiOutput right = out_at(&s, 510 + SI_SWIPE_MS);
    CHECK(right.touch[1].active && right.touch[1].serial != first.touch[1].serial);
    CHECK(right.touch[1].x > 1919 / 2 && right.touch[1].y == 1079 / 2);
}

static void test_menu_right_stick_finger(void)
{
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_pad_axis(&s, 1, SI_AXIS_RX, 20000, 0);
    CHECK(out_at(&s, 1).rx == 20000); /* no Menu: plain stick */
    CHECK(!out_at(&s, 1).touch[0].active);
    si_pad_axis(&s, 1, SI_AXIS_RX, 0, 2);
    si_pad_button(&s, 1, SI_IN_GUIDE, true, 10);
    si_pad_axis(&s, 1, SI_AXIS_RX, 5000, 20); /* inside the dead zone */
    SiOutput o = out_at(&s, 21);
    CHECK(!o.touch[0].active && o.rx == 0);
    si_pad_axis(&s, 1, SI_AXIS_RX, 32767, 30);
    o = out_at(&s, 31);
    CHECK(o.touch[0].active && o.rx == 0);
    CHECK(o.touch[0].x == 1918 && o.touch[0].y == 539);
    uint32_t serial = o.touch[0].serial;
    si_pad_axis(&s, 1, SI_AXIS_RX, -32767, 40);
    si_pad_axis(&s, 1, SI_AXIS_RY, 0, 40);
    o = out_at(&s, 41);
    CHECK(o.touch[0].active && o.touch[0].x == 0 && o.touch[0].serial == serial);
    si_pad_axis(&s, 1, SI_AXIS_RX, -9000, 50); /* between off and on: stays down */
    CHECK(out_at(&s, 51).touch[0].active);
    si_pad_axis(&s, 1, SI_AXIS_RX, -1000, 60);
    CHECK(!out_at(&s, 61).touch[0].active);
    si_pad_axis(&s, 1, SI_AXIS_RX, 30000, 70);
    o = out_at(&s, 71);
    CHECK(o.touch[0].active && o.touch[0].serial != serial);
    /* Menu up lifts the finger, and the finger made it a combo: no PS. */
    si_pad_button(&s, 1, SI_IN_GUIDE, false, 100);
    o = out_at(&s, 101);
    CHECK(!o.touch[0].active && o.rx == 30000);
    CHECK((o.buttons & SI_OUT_PS) == 0);
    CHECK(count_events(&s, SI_EV_PS, NULL) == 0);
}

static void test_menu_r3_and_l3(void)
{
    SiState s;
    int mode = -1;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_pad_button(&s, 1, SI_IN_GUIDE, true, 0);
    si_pad_button(&s, 1, SI_IN_R3, true, 20);
    CHECK(out_at(&s, 21).buttons == SI_OUT_TOUCHPAD);
    CHECK(count_events(&s, SI_EV_TOUCH_CLICK, NULL) == 1);
    si_pad_button(&s, 1, SI_IN_R3, false, 40);
    si_pad_button(&s, 1, SI_IN_L3, true, 50);
    CHECK(out_at(&s, 51).buttons == 0); /* the toggle press is not L3 */
    CHECK(count_events(&s, SI_EV_DPAD_STICK, &mode) == 1 && mode == 1);
    CHECK(s.dpad_stick);
    si_pad_button(&s, 1, SI_IN_L3, false, 60);
    si_pad_button(&s, 1, SI_IN_GUIDE, false, 70);
    si_pad_button(&s, 1, SI_IN_LEFT, true, 80);
    SiOutput o = out_at(&s, 81);
    CHECK(o.buttons == 0 && o.lx == -32767 && o.ly == 0);
    si_pad_button(&s, 1, SI_IN_UP, true, 82);
    o = out_at(&s, 83);
    CHECK(o.lx == -32767 && o.ly == -32767);
    /* A real stick pushed further than nothing still wins per axis. */
    si_pad_button(&s, 1, SI_IN_LEFT, false, 90);
    si_pad_button(&s, 1, SI_IN_UP, false, 90);
    si_pad_axis(&s, 2, SI_AXIS_LX, 12000, 91);
    CHECK(out_at(&s, 92).lx == 12000);
    si_set_dpad_stick(&s, false);
    si_pad_button(&s, 1, SI_IN_LEFT, true, 100);
    o = out_at(&s, 101);
    CHECK(o.buttons == SI_OUT_DPAD_LEFT && o.lx == 12000);
}

static void test_quit_hold(void)
{
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_pad_button(&s, 1, SI_IN_START, true, 0);
    CHECK(out_at(&s, 1).buttons == SI_OUT_OPTIONS);
    si_pad_button(&s, 1, SI_IN_BACK, true, 100);
    CHECK(out_at(&s, 101).buttons == 0); /* the combo releases both */
    CHECK(si_quit_progress(&s, 100 + SI_QUIT_HOLD_MS / 2) > 0.4f);
    run_ticks(&s, 100, 100 + SI_QUIT_HOLD_MS - 10);
    CHECK(count_events(&s, SI_EV_QUIT, NULL) == 0);
    run_ticks(&s, 100 + SI_QUIT_HOLD_MS, 100 + SI_QUIT_HOLD_MS + 20);
    CHECK(count_events(&s, SI_EV_QUIT, NULL) == 1);
    run_ticks(&s, 100 + SI_QUIT_HOLD_MS + 25, 100 + SI_QUIT_HOLD_MS + 500);
    CHECK(count_events(&s, SI_EV_QUIT, NULL) == 0); /* once per hold */
    si_pad_button(&s, 1, SI_IN_BACK, false, 2000);
    CHECK(si_quit_progress(&s, 2001) == 0.0f);
    /* Start stays off the console until it is let go (it was half of the combo). */
    CHECK(out_at(&s, 2001).buttons == 0);
    si_pad_button(&s, 1, SI_IN_START, false, 2100);
    si_pad_button(&s, 1, SI_IN_START, true, 2200);
    CHECK(out_at(&s, 2201).buttons == SI_OUT_OPTIONS);
    /* A short overlap never quits. */
    si_pad_button(&s, 1, SI_IN_BACK, true, 3000);
    si_pad_button(&s, 1, SI_IN_BACK, false, 3500);
    run_ticks(&s, 3000, 6000);
    CHECK(count_events(&s, SI_EV_QUIT, NULL) == 0);
    /* Quit works while a modal screen is up (the connect card's cancel). */
    si_set_modal(&s, true, 6000);
    si_pad_button(&s, 1, SI_IN_BACK, true, 6100);
    run_ticks(&s, 6100, 6100 + SI_QUIT_HOLD_MS + 10);
    CHECK(count_events(&s, SI_EV_QUIT, NULL) == 1);
}

/* Letting go of one half of Start + Select early sends nothing: before, the half
 * still held became a real Options or touchpad (or Create) press. */
static void test_quit_combo_released_early(void)
{
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_pad_button(&s, 1, SI_IN_START, true, 0);
    si_pad_button(&s, 1, SI_IN_BACK, true, 50);
    CHECK(out_at(&s, 51).buttons == 0);
    si_pad_button(&s, 1, SI_IN_BACK, false, 300);   /* Select first */
    run_ticks(&s, 300, 2000);
    CHECK(out_at(&s, 2000).buttons == 0);           /* no Options from the Start still held */
    CHECK(count_events(&s, SI_EV_QUIT, NULL) == 0);
    si_pad_button(&s, 1, SI_IN_START, false, 2100);
    CHECK(out_at(&s, 2101).buttons == 0);

    si_pad_button(&s, 1, SI_IN_BACK, true, 3000);   /* Select = touchpad on its own */
    CHECK(out_at(&s, 3001).buttons == SI_OUT_TOUCHPAD);
    si_pad_button(&s, 1, SI_IN_START, true, 3050);
    CHECK(out_at(&s, 3051).buttons == 0);
    si_pad_button(&s, 1, SI_IN_START, false, 3300); /* Start first */
    run_ticks(&s, 3300, 5000);
    CHECK(out_at(&s, 5000).buttons == 0);           /* no touchpad from the Select still held */
    si_pad_button(&s, 1, SI_IN_BACK, false, 5100);
    si_pad_button(&s, 1, SI_IN_BACK, true, 5200);
    CHECK(out_at(&s, 5201).buttons == SI_OUT_TOUCHPAD);
    si_pad_button(&s, 1, SI_IN_BACK, false, 5300);

    /* A DualSense's Options + Create, let go one at a time. */
    si_pad_attach(&s, 2, SI_PAD_TOUCHPAD);
    si_pad_button(&s, 2, SI_IN_START, true, 6000);
    si_pad_button(&s, 2, SI_IN_BACK, true, 6010);
    CHECK(out_at(&s, 6011).buttons == 0);
    si_pad_button(&s, 2, SI_IN_START, false, 6200);
    CHECK((out_at(&s, 6201).buttons & (SI_OUT_SHARE | SI_OUT_OPTIONS)) == 0);
    si_pad_button(&s, 2, SI_IN_BACK, false, 6300);
    si_pad_button(&s, 2, SI_IN_BACK, true, 6400);   /* the latch went with the release */
    CHECK(out_at(&s, 6401).buttons == SI_OUT_SHARE);
    si_pad_button(&s, 2, SI_IN_BACK, false, 6500);
    /* Create was never held in the first part: no latch was left on it. */
    CHECK(s.latched == 0);
}

static void test_hold_ps(void)
{
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_set_modal(&s, true, 0);
    si_hold_ps(&s, 10, SI_PS_HOLD_MS);              /* ignored while the menu is up */
    si_set_modal(&s, false, 20);
    CHECK((out_at(&s, 21).buttons & SI_OUT_PS) == 0);
    si_hold_ps(&s, 100, SI_PS_HOLD_MS);
    CHECK(out_at(&s, 101).buttons == SI_OUT_PS);
    si_tick(&s, 100 + SI_PS_HOLD_MS - 1);
    CHECK(out_at(&s, 100 + SI_PS_HOLD_MS - 1).buttons == SI_OUT_PS);
    si_tick(&s, 100 + SI_PS_HOLD_MS);
    CHECK(out_at(&s, 100 + SI_PS_HOLD_MS).buttons == 0);
    CHECK(count_events(&s, SI_EV_PS, NULL) == 0);   /* not a tap */
}

static void test_modal_idle_and_swallow(void)
{
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_pad_axis(&s, 1, SI_AXIS_L2, 32767, 0);
    si_pad_button(&s, 1, SI_IN_SOUTH, true, 0);
    si_set_modal(&s, true, 10);
    SiOutput o = out_at(&s, 11);
    CHECK(o.buttons == 0 && o.l2 == 0 && o.lx == 0);
    si_pad_button(&s, 1, SI_IN_EAST, true, 20); /* "A" confirms in the menu */
    si_set_modal(&s, false, 30);
    o = out_at(&s, 31);
    CHECK(o.buttons == 0);   /* South and East were held across the screen */
    CHECK(o.l2 == 255);      /* axes pass straight through */
    si_pad_button(&s, 1, SI_IN_EAST, false, 40);
    si_pad_button(&s, 1, SI_IN_EAST, true, 50);
    CHECK(out_at(&s, 51).buttons == SI_OUT_MOON);
    si_pad_button(&s, 1, SI_IN_SOUTH, false, 60);
    si_pad_button(&s, 1, SI_IN_SOUTH, true, 70);
    CHECK(out_at(&s, 71).buttons == (SI_OUT_MOON | SI_OUT_CROSS));
    /* Menu pressed while modal and released after: no PS. */
    si_set_modal(&s, true, 100);
    si_pad_button(&s, 1, SI_IN_GUIDE, true, 110);
    si_set_modal(&s, false, 120);
    si_pad_button(&s, 1, SI_IN_GUIDE, false, 130);
    run_ticks(&s, 130, 400);
    CHECK(count_events(&s, SI_EV_PS, NULL) == 0);
    CHECK((out_at(&s, 401).buttons & SI_OUT_PS) == 0);
}

static void test_multi_pad_merge(void)
{
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_pad_axis(&s, 1, SI_AXIS_LX, 900, 0);    /* built-in pad drifting */
    si_pad_axis(&s, 7, SI_AXIS_LX, -25000, 0); /* Bluetooth pad in use */
    si_pad_axis(&s, 7, SI_AXIS_R2, 16384, 0);
    si_pad_button(&s, 1, SI_IN_SOUTH, true, 0);
    si_pad_button(&s, 7, SI_IN_NORTH, true, 0);
    SiOutput o = out_at(&s, 1);
    CHECK(o.lx == -25000);
    CHECK(o.r2 == 128);
    CHECK(o.buttons == (SI_OUT_CROSS | SI_OUT_PYRAMID));
    /* The same button on both pads stays held until both let go. */
    si_pad_button(&s, 7, SI_IN_SOUTH, true, 5);
    si_pad_button(&s, 1, SI_IN_SOUTH, false, 6);
    CHECK(out_at(&s, 7).buttons & SI_OUT_CROSS);
    /* Detaching the BT pad drops what it held, and a Menu it held is no tap. */
    si_pad_button(&s, 7, SI_IN_GUIDE, true, 10);
    si_pad_detach(&s, 7, 20);
    o = out_at(&s, 21);
    CHECK(o.buttons == 0 && o.lx == 900 && o.r2 == 0);
    run_ticks(&s, 20, 300);
    CHECK(count_events(&s, SI_EV_PS, NULL) == 0);
    /* Pads are created on demand and the table has a limit. */
    for(int id = 100; id < 100 + SI_MAX_PADS; id++) si_pad_attach(&s, id, 0);
    CHECK(!si_pad_attach(&s, 999, 0));
    si_pad_button(&s, 999, SI_IN_SOUTH, true, 400); /* dropped, no crash */
}

static void test_real_touchpad(void)
{
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 941);
    si_pad_touch(&s, 3, 0, true, 0.25f, 0.5f, 0);
    SiOutput o = out_at(&s, 1);
    CHECK(o.touch[0].active && o.touch[0].x == 480 && o.touch[0].y == 471);
    uint32_t serial = o.touch[0].serial;
    si_pad_touch(&s, 3, 0, true, 2.0f, -1.0f, 2); /* clamped */
    o = out_at(&s, 3);
    CHECK(o.touch[0].x == 1919 && o.touch[0].y == 0 && o.touch[0].serial == serial);
    si_pad_button(&s, 3, SI_IN_TOUCHPAD, true, 4);
    CHECK(out_at(&s, 5).buttons == SI_OUT_TOUCHPAD);
    si_pad_touch(&s, 3, 0, false, 0.0f, 0.0f, 6);
    CHECK(!out_at(&s, 7).touch[0].active);
    si_pad_touch(&s, 3, 1, true, 0.5f, 0.5f, 8);
    o = out_at(&s, 9);
    CHECK(o.touch[0].active && o.touch[0].serial != serial); /* first free slot */
    si_pad_touch(&s, 3, 5, true, 0.5f, 0.5f, 10); /* bad finger index ignored */
}

static void test_external_pad_buttons(void)
{
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    CHECK(si_pad_attach(&s, 1, SI_PAD_BUILTIN));
    CHECK(si_pad_attach(&s, 2, SI_PAD_TOUCHPAD)); /* a DualSense */
    CHECK(si_pad_attach(&s, 3, 0));               /* an Xbox pad */
    /* Select is Create on the pad with its own touchpad, the touchpad click on
     * the others (select mode), and the real touchpad is the touchpad. */
    si_pad_button(&s, 2, SI_IN_BACK, true, 0);
    CHECK(out_at(&s, 1).buttons == SI_OUT_SHARE);
    si_pad_button(&s, 2, SI_IN_BACK, false, 2);
    si_pad_button(&s, 1, SI_IN_BACK, true, 3);
    CHECK(out_at(&s, 4).buttons == SI_OUT_TOUCHPAD);
    si_pad_button(&s, 1, SI_IN_BACK, false, 5);
    si_pad_button(&s, 3, SI_IN_BACK, true, 6);
    CHECK(out_at(&s, 7).buttons == SI_OUT_TOUCHPAD);
    si_pad_button(&s, 3, SI_IN_BACK, false, 8);
    si_pad_button(&s, 2, SI_IN_TOUCHPAD, true, 9);
    CHECK(out_at(&s, 10).buttons == SI_OUT_TOUCHPAD);
    si_pad_button(&s, 2, SI_IN_TOUCHPAD, false, 11);
    CHECK(out_at(&s, 12).buttons == 0);
    /* An external PS button is the console's, held straight through: no stream
     * menu however long, and no extra pulse when it lets go. */
    si_pad_button(&s, 2, SI_IN_GUIDE, true, 100);
    CHECK(out_at(&s, 101).buttons == SI_OUT_PS);
    run_ticks(&s, 100, 100 + SI_GUIDE_MENU_MS + 500);
    CHECK(out_at(&s, 100 + SI_GUIDE_MENU_MS + 500).buttons == SI_OUT_PS);
    CHECK(count_events(&s, SI_EV_OPEN_MENU, NULL) == 0);
    si_pad_button(&s, 2, SI_IN_GUIDE, false, 2000);
    CHECK(out_at(&s, 2001).buttons == 0);
    run_ticks(&s, 2000, 2400);
    CHECK(count_events(&s, SI_EV_PS, NULL) == 0);
    CHECK(out_at(&s, 2401).buttons == 0);
    si_pad_button(&s, 3, SI_IN_GUIDE, true, 2500); /* the Xbox pad's Guide too */
    CHECK(out_at(&s, 2501).buttons == SI_OUT_PS);
    si_pad_button(&s, 3, SI_IN_GUIDE, false, 2600);
    CHECK(out_at(&s, 2601).buttons == 0);
    /* The handheld's own Menu keeps its tap and its hold. */
    si_pad_button(&s, 1, SI_IN_GUIDE, true, 3000);
    si_pad_button(&s, 1, SI_IN_GUIDE, false, 3050);
    CHECK(out_at(&s, 3051).buttons == SI_OUT_PS);
    CHECK(count_events(&s, SI_EV_PS, NULL) == 1);
    run_ticks(&s, 3050, 3050 + SI_PS_PULSE_MS + 10);
    si_pad_button(&s, 1, SI_IN_GUIDE, true, 4000);
    run_ticks(&s, 4000, 4000 + SI_GUIDE_MENU_MS + 20);
    CHECK(count_events(&s, SI_EV_OPEN_MENU, NULL) == 1);
}

static void test_no_builtin_pad_keeps_the_handheld_layout(void)
{
    /* With no pad marked built-in (a desktop, or a pad SDL could not identify)
     * every pad keeps the handheld layout, so the stream menu and the D-pad
     * mode are still there. */
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, true, 1919, 1079);
    CHECK(si_pad_attach(&s, 5, 0));
    si_pad_button(&s, 5, SI_IN_GUIDE, true, 0);
    run_ticks(&s, 0, SI_GUIDE_MENU_MS + 20);
    CHECK(count_events(&s, SI_EV_OPEN_MENU, NULL) == 1);
    si_pad_button(&s, 5, SI_IN_GUIDE, false, SI_GUIDE_MENU_MS + 100);
    si_pad_button(&s, 5, SI_IN_LEFT, true, 2000);
    SiOutput o = out_at(&s, 2001);
    CHECK(o.lx == -32767 && !(o.buttons & SI_OUT_DPAD_LEFT));
}

static void test_dpad_mode_only_on_the_builtin_pad(void)
{
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, true, 1919, 1079);
    si_pad_attach(&s, 1, SI_PAD_BUILTIN);
    si_pad_attach(&s, 2, SI_PAD_TOUCHPAD);
    si_pad_button(&s, 1, SI_IN_LEFT, true, 0);
    SiOutput o = out_at(&s, 1);
    CHECK(o.lx == -32767 && o.buttons == 0);
    si_pad_button(&s, 2, SI_IN_UP, true, 2); /* the external D-pad is a D-pad */
    o = out_at(&s, 3);
    CHECK(o.buttons == SI_OUT_DPAD_UP && o.lx == -32767 && o.ly == 0);
    si_pad_button(&s, 1, SI_IN_LEFT, false, 4);
    o = out_at(&s, 5);
    CHECK(o.buttons == SI_OUT_DPAD_UP && o.lx == 0);
    /* Menu + L3 still switches the handheld's D-pad back to a D-pad. */
    si_pad_button(&s, 1, SI_IN_GUIDE, true, 10);
    si_pad_button(&s, 1, SI_IN_L3, true, 20);
    si_pad_button(&s, 1, SI_IN_L3, false, 30);
    si_pad_button(&s, 1, SI_IN_GUIDE, false, 40);
    si_pad_button(&s, 1, SI_IN_DOWN, true, 50);
    o = out_at(&s, 51);
    CHECK((o.buttons & SI_OUT_DPAD_DOWN) && o.ly == 0);
}

static void test_quit_combo_from_one_pad(void)
{
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_pad_attach(&s, 1, SI_PAD_BUILTIN);
    si_pad_attach(&s, 2, SI_PAD_TOUCHPAD);
    /* Start on one pad and Select on another are two players: no quit. */
    si_pad_button(&s, 1, SI_IN_START, true, 0);
    si_pad_button(&s, 2, SI_IN_BACK, true, 0);
    run_ticks(&s, 0, SI_QUIT_HOLD_MS + 300);
    CHECK(count_events(&s, SI_EV_QUIT, NULL) == 0);
    CHECK(si_quit_progress(&s, SI_QUIT_HOLD_MS) == 0.0f);
    CHECK(out_at(&s, SI_QUIT_HOLD_MS + 301).buttons == (SI_OUT_OPTIONS | SI_OUT_SHARE));
    si_pad_button(&s, 1, SI_IN_START, false, 2000);
    si_pad_button(&s, 2, SI_IN_BACK, false, 2000);
    /* Options + Create on the DualSense is the combo, held off the console. */
    si_pad_button(&s, 2, SI_IN_START, true, 3000);
    si_pad_button(&s, 2, SI_IN_BACK, true, 3000);
    CHECK(out_at(&s, 3001).buttons == 0);
    run_ticks(&s, 3000, 3000 + SI_QUIT_HOLD_MS + 20);
    CHECK(count_events(&s, SI_EV_QUIT, NULL) == 1);
}

static void test_guide_released_after_the_builtin_pad_left(void)
{
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_pad_attach(&s, 1, SI_PAD_BUILTIN);
    si_pad_attach(&s, 2, 0);
    si_pad_button(&s, 2, SI_IN_GUIDE, true, 0);   /* PS, straight through */
    CHECK(out_at(&s, 1).buttons == SI_OUT_PS);
    si_pad_detach(&s, 1, 10);                     /* the built-in pad goes */
    si_pad_button(&s, 2, SI_IN_GUIDE, false, 20); /* Guide would be Menu now */
    CHECK(out_at(&s, 21).buttons == 0);
    run_ticks(&s, 20, 2000);
    CHECK(count_events(&s, SI_EV_OPEN_MENU, NULL) == 0);
    CHECK(out_at(&s, 2001).buttons == 0);
    /* Attaching an existing pad updates its flags. */
    CHECK(si_pad_attach(&s, 2, SI_PAD_TOUCHPAD));
    si_pad_button(&s, 2, SI_IN_BACK, true, 2100);
    CHECK(out_at(&s, 2101).buttons == SI_OUT_SHARE);
}

static void test_event_queue_overflow(void)
{
    SiState s;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_pad_button(&s, 1, SI_IN_GUIDE, true, 0);
    for(int i = 0; i < SI_EVENT_QUEUE + 5; i++) {
        si_pad_button(&s, 1, SI_IN_UP, true, (uint32_t)(10 + i * 2));
        si_pad_button(&s, 1, SI_IN_UP, false, (uint32_t)(11 + i * 2));
    }
    CHECK(s.event_count == SI_EVENT_QUEUE);
    CHECK(count_events(&s, SI_EV_SWIPE, NULL) == SI_EVENT_QUEUE);
}

static void test_clock_wrap(void)
{
    SiState s;
    uint32_t base = 0xFFFFFF00u;
    si_init(&s, SI_SELECT_TOUCHPAD, false, 1919, 1079);
    si_pad_button(&s, 1, SI_IN_GUIDE, true, base);
    si_pad_button(&s, 1, SI_IN_GUIDE, false, base + 100);
    CHECK(out_at(&s, base + 150).buttons == SI_OUT_PS); /* across the wrap */
    si_tick(&s, base + 100 + SI_PS_PULSE_MS);
    CHECK(out_at(&s, base + 100 + SI_PS_PULSE_MS).buttons == 0);
    si_pad_button(&s, 1, SI_IN_GUIDE, true, base + 400);
    run_ticks(&s, base + 400, base + 400 + SI_GUIDE_MENU_MS + 10);
    CHECK(count_events(&s, SI_EV_OPEN_MENU, NULL) == 1);
}

int main(void)
{
    test_face_buttons_positional();
    test_select_modes();
    test_menu_tap_is_ps();
    test_menu_hold_opens_menu_without_ps();
    test_menu_combo_cancels_tap();
    test_menu_select_is_the_other_button();
    test_menu_dpad_swipes();
    test_menu_right_stick_finger();
    test_menu_r3_and_l3();
    test_quit_hold();
    test_quit_combo_released_early();
    test_hold_ps();
    test_modal_idle_and_swallow();
    test_multi_pad_merge();
    test_real_touchpad();
    test_external_pad_buttons();
    test_no_builtin_pad_keeps_the_handheld_layout();
    test_dpad_mode_only_on_the_builtin_pad();
    test_quit_combo_from_one_pad();
    test_guide_released_after_the_builtin_pad_left();
    test_event_queue_overflow();
    test_clock_wrap();
    printf("stream_input: %d checks, %d failures\n", checks, failures);
    return failures ? 1 : 0;
}
