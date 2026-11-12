/* Runs chiaki-stream.c's whole main() against scripted fakes of SDL, chiaki and
 * FFmpeg (fake_runtime.c): every scenario writes a real session file, streams on
 * a fake clock and checks the status file and what reached the "console".
 * Built and run by run-host-tests.sh. */
#include "fake_runtime.h"
#include "stream_input.h"

#include <chiaki/controller.h>
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>

int stream_main(int argc, char **argv);

/* chiaki-stream.c's test seams: where pad identification reads sysfs, and the
 * SDL_JoystickPath stand-in. */
extern const char *stream_sysfs_root;
extern const char *(*stream_joystick_path_hook)(SDL_Joystick *joystick);

static int failures, checks;
#define CHECK(cond) do { checks++; if(!(cond)) { failures++; \
    fprintf(stderr, "%s:%d [%s]: CHECK(%s) failed\n", __FILE__, __LINE__, scenario, #cond); } } while(0)
static const char *scenario = "";

static char SESSION[512], STATUS[512];
#define BASE "host=192.168.1.40\nregist_key=0a1b2c3d\nrp_key=AAECAwQFBgcICQoLDA0ODw==\nwidth=1280\nheight=720\n" \
             "fps=30\nbitrate=8000\nvolume=80\nname=Test PS5\n"
#define PS5 "ps5=1\ntarget=1000100\n"

static char status[4096];

static int run(const char *session_text)
{
    FILE *f = fopen(SESSION, "w");
    fputs(session_text, f);
    fprintf(f, "status_file=%s\n", STATUS);
    fclose(f);
    remove(STATUS);
    fake_sort();
    char *argv[] = {"chiaki-stream", SESSION, NULL};
    int rc = stream_main(2, argv);
    status[0] = '\0';
    f = fopen(STATUS, "r");
    if(f) {
        size_t n = fread(status, 1, sizeof(status) - 1, f);
        status[n] = '\0';
        fclose(f);
    }
    return rc;
}

static bool has(const char *line)
{
    return strstr(status, line) != NULL;
}

static bool had_bit(uint32_t bit, uint32_t from, uint32_t to)
{
    for(int i = 0; i < fake_record.states; i++)
        if(fake_record.state_at[i] >= from && fake_record.state_at[i] <= to && (fake_record.state[i].buttons & bit))
            return true;
    return false;
}

/* The buttons the console holds at time t: the last state sent at or before t. */
static uint32_t buttons_at(uint32_t t)
{
    uint32_t b = 0;
    for(int i = 0; i < fake_record.states && fake_record.state_at[i] <= t; i++) b = fake_record.state[i].buttons;
    return b;
}

static void tap(uint32_t at, SDL_JoystickID id, int button, uint32_t len)
{
    fake_button(at, id, button, true);
    fake_button(at + len, id, button, false);
}

/* The event node each fake device reports through SDL_JoystickPath (NULL: the
 * SDL is too old to say). */
static const char *fake_paths[8];

static const char *fake_path(SDL_Joystick *joystick)
{
    int index = fake_device_index(joystick);
    return index >= 0 && index < 8 ? fake_paths[index] : NULL;
}

/* The handheld's own pad as SDL reports it: named after its USB id, which is
 * a wired Xbox 360 pad's. Only sysfs (SYSFS, built in main) says TRIMUI. */
static int add_builtin_pad(SDL_JoystickID id)
{
    int index = fake_add_device(true, "Xbox 360 Controller", id);
    fake_device_ids(index, 0x045e, 0x028e);
    return index;
}

/* A DualSense: SDL_CONTROLLER_TYPE_PS5 is 7 (SDL 2.0.14 on). */
static int add_dualsense(SDL_JoystickID id)
{
    int index = fake_add_device(true, "DualSense Wireless Controller", id);
    fake_device_ids(index, 0x054c, 0x0ce6);
    fake_device_type(index, 7);
    return index;
}

static char SYSFS[512], MOTOR[512];

static void put_file(const char *path, const char *text)
{
    char dir[600];
    snprintf(dir, sizeof(dir), "%s", path);
    for(char *p = dir + 1; *p; p++) {
        if(*p == '/') { *p = '\0'; mkdir(dir, 0755); *p = '/'; }
    }
    FILE *f = fopen(path, "w");
    if(!f) { perror(path); exit(2); }
    fputs(text, f);
    fclose(f);
}

static void read_small(const char *path, char *out, size_t size)
{
    out[0] = '\0';
    FILE *f = fopen(path, "r");
    if(!f) return;
    size_t n = fread(out, 1, size - 1, f);
    out[n] = '\0';
    fclose(f);
}

static void fake_input_device(const char *event, const char *name)
{
    char path[700];
    snprintf(path, sizeof(path), "%s/sys/class/input/%s/device/name", SYSFS, event); put_file(path, name);
    snprintf(path, sizeof(path), "%s/sys/class/input/%s/device/id/vendor", SYSFS, event); put_file(path, "045e\n");
    snprintf(path, sizeof(path), "%s/sys/class/input/%s/device/id/product", SYSFS, event); put_file(path, "028e\n");
}

static bool stick_x_at(int16_t want, uint32_t from, uint32_t to)
{
    for(int i = 0; i < fake_record.states; i++)
        if(fake_record.state_at[i] >= from && fake_record.state_at[i] <= to && fake_record.state[i].left_x == want)
            return true;
    return false;
}

static void scenario_happy(void)
{
    scenario = "happy path";
    fake_reset(60000);
    add_builtin_pad(0);
    fake_device_added(1001, 0); /* SDL's own ADDED event for the pad opened at start */
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 12000, 33);
    tap(3000, 0, SDL_CONTROLLER_BUTTON_GUIDE, 100);          /* PS */
    tap(4000, 0, SDL_CONTROLLER_BUTTON_BACK, 100);           /* touchpad */
    fake_button(5000, 0, SDL_CONTROLLER_BUTTON_GUIDE, true);  /* Menu + Select = Create */
    tap(5050, 0, SDL_CONTROLLER_BUTTON_BACK, 100);
    fake_button(5200, 0, SDL_CONTROLLER_BUTTON_GUIDE, false);
    fake_button(6000, 0, SDL_CONTROLLER_BUTTON_GUIDE, true);  /* Menu + Up = swipe */
    tap(6020, 0, SDL_CONTROLLER_BUTTON_DPAD_UP, 80);
    fake_button(6300, 0, SDL_CONTROLLER_BUTTON_GUIDE, false);
    fake_chiaki(6500, CHIAKI_EVENT_RUMBLE, 200);
    tap(6600, 0, SDL_CONTROLLER_BUTTON_A, 100);               /* Cross */
    fake_button(7000, 0, SDL_CONTROLLER_BUTTON_GUIDE, true);  /* hold Menu: stream menu */
    fake_button(8100, 0, SDL_CONTROLLER_BUTTON_GUIDE, false);
    tap(8200, 0, SDL_CONTROLLER_BUTTON_DPAD_DOWN, 50);        /* cursor -> Quit */
    tap(8400, 0, SDL_CONTROLLER_BUTTON_B, 50);                /* TrimUI "A" */
    int rc = run(BASE PS5);
    CHECK(rc == 0);
    CHECK(!fake_record.timed_out);
    CHECK(has("result=ok\n") && has("rc=0\n") && has("connected=1\n") && has("codec=h265\n") && has("saved_pin_rejected=0\n"));
    CHECK(has("host=192.168.1.40\n"));
    CHECK(fake_record.codec == CHIAKI_CODEC_H265 && fake_record.ps5);
    CHECK(fake_device_refs(0) == 0);   /* opened once, the duplicate ADDED was given back, closed at exit */
    CHECK(fake_record.opens == 2);
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_PS, 2900, 3099));
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_PS, 3100, 3120));
    CHECK(!(buttons_at(3400) & CHIAKI_CONTROLLER_BUTTON_PS));
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_TOUCHPAD, 4000, 4100));
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_SHARE, 3900, 4200));
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_SHARE, 5050, 5150));
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_TOUCHPAD, 5000, 5300));
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_PS, 5000, 5600));
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_DPAD_UP, 6000, 6300));
    CHECK(fake_record.touch_starts >= 1 && fake_record.touch_moves >= 2 && fake_record.touch_stops >= 1);
    CHECK(fake_record.rumble_calls >= 1 && fake_record.rumble_low == 200u * 257u);
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_CROSS, 6600, 6700));
    CHECK(buttons_at(8300) == 0);        /* menu open: idle pad */
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_PS, 7000, 9000)); /* holding Menu opened the menu, no PS */
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_MOON, 8350, 9000)); /* the confirm press never reached the game */
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_DPAD_DOWN, 8150, 9000));
    CHECK(fake_record.stopped == 1 && fake_record.fini == 1);
    CHECK(fake_now < 9500);
}

static void scenario_pin_entry(void)
{
    scenario = "pin entry";
    fake_reset(60000);
    add_builtin_pad(0);
    fake_chiaki(1500, CHIAKI_EVENT_LOGIN_PIN_REQUEST, 0);
    for(int i = 0; i < 4; i++) tap(1800 + (uint32_t)i * 100, 0, SDL_CONTROLLER_BUTTON_DPAD_UP, 40);
    tap(2300, 0, SDL_CONTROLLER_BUTTON_DPAD_RIGHT, 40);
    tap(2400, 0, SDL_CONTROLLER_BUTTON_DPAD_UP, 40);
    tap(2500, 0, SDL_CONTROLLER_BUTTON_DPAD_UP, 40);
    tap(2700, 0, SDL_CONTROLLER_BUTTON_B, 40);   /* "A": enter */
    fake_chiaki(3000, CHIAKI_EVENT_LOGIN_PIN_REQUEST, 1);
    tap(3500, 0, SDL_CONTROLLER_BUTTON_A, 40);   /* "B": cancel */
    int rc = run(BASE PS5);
    CHECK(rc == 12);
    CHECK(fake_record.pin_count == 1 && !strcmp(fake_record.pins[0], "4200"));
    CHECK(has("result=login_pin_cancelled\n") && has("rc=12\n"));
    CHECK(buttons_at(3400) == 0 && !had_bit(CHIAKI_CONTROLLER_BUTTON_DPAD_UP, 1500, 4000));
}

static void scenario_saved_pin_and_quit_combo(void)
{
    scenario = "saved pin + quit combo";
    fake_reset(60000);
    add_builtin_pad(0);
    fake_chiaki(1500, CHIAKI_EVENT_LOGIN_PIN_REQUEST, 0);
    fake_chiaki(1800, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(2000, 9000, 33);
    fake_button(4000, 0, SDL_CONTROLLER_BUTTON_START, true);
    fake_button(4100, 0, SDL_CONTROLLER_BUTTON_BACK, true);
    fake_button(5600, 0, SDL_CONTROLLER_BUTTON_BACK, false);
    fake_button(5600, 0, SDL_CONTROLLER_BUTTON_START, false);
    int rc = run(BASE PS5 "login_pin=1234\nselect_button=create\n");
    CHECK(rc == 0);
    CHECK(fake_record.pin_count == 1 && !strcmp(fake_record.pins[0], "1234"));
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_OPTIONS, 4000, 4099));
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_OPTIONS, 4150, 6000));
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_SHARE, 4100, 6000)); /* Select = Create here, held in the combo */
    CHECK(has("result=ok\n"));
    CHECK(fake_now >= 4100 + 1200 && fake_now < 5600);
}

static void scenario_connect_failed(void)
{
    scenario = "connect failed";
    fake_reset(60000);
    add_builtin_pad(0);
    fake_chiaki(2000, CHIAKI_EVENT_QUIT, 5);
    tap(2300, 0, SDL_CONTROLLER_BUTTON_A, 40);   /* inside the 400 ms grace: ignored */
    tap(2800, 0, SDL_CONTROLLER_BUTTON_A, 40);   /* closes the card */
    int rc = run(BASE PS5);
    CHECK(rc == 11);
    CHECK(has("result=connect_failed\n") && has("connected=0\n"));
    CHECK(strstr(status, "did not answer at 192.168.1.40 (Connection refused - fake detail)") != NULL);
    CHECK(strstr(status, "quit_reason=Connection refused - fake detail\n") != NULL);
    CHECK(fake_record.audio_paused == 1);
    CHECK(fake_now >= 2800 && fake_now < 3200);
}

static void scenario_console_quit(void)
{
    scenario = "console quit";
    fake_reset(60000);
    add_builtin_pad(0);
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 4000, 33);
    fake_chiaki(3000, CHIAKI_EVENT_QUIT, 5);
    int rc = run(BASE PS5 "remote=1\n");
    CHECK(rc == 11);
    CHECK(has("result=console_quit\n") && has("connected=1\n"));
    CHECK(strstr(status, "message=Test PS5 ended the stream: Connection refused - fake detail.\n") != NULL);
    CHECK(fake_now >= 3000 + 8000); /* the card closed itself */
}

static void scenario_invalid(void)
{
    scenario = "invalid session";
    fake_reset(60000);
    int rc = run("host=192.168.1.40\nregist_key=0a1b2c3d\nps5=1\ntarget=1000100\n");
    CHECK(rc == 3);
    CHECK(has("result=invalid_session\n") && has("rc=3\n"));
    fake_reset(60000);
    rc = run(BASE "ps5=1\ntarget=900\n");
    CHECK(rc == 3 && has("result=invalid_session\n"));
    fake_reset(60000);
    add_builtin_pad(0);
    fake_frames(1500, 3000, 33);
    tap(2000, 0, SDL_CONTROLLER_BUTTON_START, 1300);
    tap(2000, 0, SDL_CONTROLLER_BUTTON_BACK, 1300);
    rc = run(BASE "ps5=1\ntarget=1000000\n"); /* PS5_UNKNOWN is accepted now */
    CHECK(rc == 0);
}

static void scenario_hotplug(void)
{
    scenario = "hotplug";
    fake_reset(60000);
    add_builtin_pad(0);
    fake_add_device(true, "DualSense Wireless Controller", 5);
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 8000, 33);
    fake_device_added(4000, 1);
    fake_button(4500, 5, SDL_CONTROLLER_BUTTON_A, true);
    fake_axis(4500, 5, SDL_CONTROLLER_AXIS_LEFTX, -20000);
    fake_device_removed(4600, 5);
    tap(5000, 0, SDL_CONTROLLER_BUTTON_START, 1300);
    tap(5000, 0, SDL_CONTROLLER_BUTTON_BACK, 1300);
    int rc = run(BASE PS5);
    CHECK(rc == 0);
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_CROSS, 4500, 4599));
    CHECK(!(buttons_at(4700) & CHIAKI_CONTROLLER_BUTTON_CROSS));
    bool moved = false, recentred = false;
    for(int i = 0; i < fake_record.states; i++) {
        if(fake_record.state_at[i] >= 4500 && fake_record.state_at[i] < 4600 && fake_record.state[i].left_x == -20000) moved = true;
        if(fake_record.state_at[i] >= 4600 && fake_record.state[i].left_x == 0) recentred = true;
    }
    CHECK(moved && recentred);
    CHECK(fake_device_refs(1) == 0);
}

static void scenario_ps4_codec_and_decoder_failure(void)
{
    scenario = "ps4 codec";
    fake_reset(60000);
    add_builtin_pad(0);
    fake_frames(1500, 3000, 33);
    tap(2000, 0, SDL_CONTROLLER_BUTTON_START, 1300);
    tap(2000, 0, SDL_CONTROLLER_BUTTON_BACK, 1300);
    int rc = run(BASE "ps5=0\ntarget=1000\ncodec=h265\n");
    CHECK(rc == 0 && fake_record.codec == CHIAKI_CODEC_H264 && !fake_record.ps5);
    CHECK(has("codec=h264\n"));

    scenario = "decoder failure";
    fake_reset(60000);
    fake_record.fail_decoder = true;
    add_builtin_pad(0);
    rc = run(BASE PS5);
    CHECK(rc == 6 && has("result=decoder_failed\n"));
    CHECK(fake_record.started == 0);
}


static void joy_button(uint32_t at, SDL_JoystickID id, int button, bool down)
{
    SDL_Event e;
    memset(&e, 0, sizeof(e));
    e.type = down ? SDL_JOYBUTTONDOWN : SDL_JOYBUTTONUP;
    e.jbutton.which = id;
    e.jbutton.button = (Uint8)button;
    fake_sdl_event(at, e);
}

static void scenario_fallback_joystick(void)
{
    scenario = "joystick fallback";
    fake_reset(60000);
    fake_add_device(false, "Unmapped pad", 9);
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 9000, 33);
    joy_button(2000, 9, 1, true);   /* raw 1 = Cross */
    joy_button(2100, 9, 1, false);
    joy_button(2500, 9, 10, true);  /* raw 10 = Menu: tap = PS */
    joy_button(2600, 9, 10, false);
    joy_button(3000, 9, 7, true);   /* raw 7 = R2, digital */
    joy_button(3100, 9, 7, false);
    joy_button(4000, 9, 9, true);   /* Start + Select (raw 9 + 8) held = quit */
    joy_button(4000, 9, 8, true);
    joy_button(5500, 9, 9, false);
    joy_button(5500, 9, 8, false);
    int rc = run(BASE PS5);
    CHECK(rc == 0 && has("result=ok\n"));
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_CROSS, 2000, 2099));
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_PS, 2600, 2700));
    bool r2 = false;
    for(int i = 0; i < fake_record.states; i++)
        if(fake_record.state_at[i] >= 3000 && fake_record.state_at[i] < 3100 && fake_record.state[i].r2_state == 255) r2 = true;
    CHECK(r2);
    CHECK(fake_now >= 4000 + 1200 && fake_now < 5500);
}

static void scenario_dpad_stick_and_rumble_off(void)
{
    scenario = "dpad stick + rumble off";
    fake_reset(60000);
    add_builtin_pad(0);
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 9000, 33);
    fake_chiaki(1600, CHIAKI_EVENT_RUMBLE, 255);
    tap(2000, 0, SDL_CONTROLLER_BUTTON_DPAD_LEFT, 100);      /* stick mode from the session file */
    fake_button(3000, 0, SDL_CONTROLLER_BUTTON_GUIDE, true); /* Menu + L3: back to D-pad */
    tap(3050, 0, SDL_CONTROLLER_BUTTON_LEFTSTICK, 50);
    fake_button(3200, 0, SDL_CONTROLLER_BUTTON_GUIDE, false);
    tap(3500, 0, SDL_CONTROLLER_BUTTON_DPAD_LEFT, 100);
    tap(5000, 0, SDL_CONTROLLER_BUTTON_START, 1300);
    tap(5000, 0, SDL_CONTROLLER_BUTTON_BACK, 1300);
    int rc = run(BASE PS5 "dpad_stick=1\nrumble=0\n");
    CHECK(rc == 0);
    bool stick = false;
    for(int i = 0; i < fake_record.states; i++)
        if(fake_record.state_at[i] >= 2000 && fake_record.state_at[i] < 2100 && fake_record.state[i].left_x == -32767) stick = true;
    CHECK(stick);
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_DPAD_LEFT, 2000, 2100));
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_L3, 3000, 3200));
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_DPAD_LEFT, 3500, 3600));
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_PS, 3000, 3600));
    CHECK(fake_record.rumble_low == 0 && fake_record.rumble_high == 0);
}

static void scenario_rumble_follows_the_pad(void)
{
    scenario = "rumble target";
    fake_reset(60000);
    add_builtin_pad(0);
    fake_add_device(true, "DualSense Wireless Controller", 5);
    fake_add_device(false, "accelerometer", 7);
    fake_device_buttons(2, 0);
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 9000, 33);
    tap(2000, 0, SDL_CONTROLLER_BUTTON_A, 50);   /* built-in pad in use */
    fake_chiaki(2200, CHIAKI_EVENT_RUMBLE, 100);
    fake_chiaki(2400, CHIAKI_EVENT_RUMBLE, 0);
    tap(3000, 5, SDL_CONTROLLER_BUTTON_A, 50);   /* then the DualSense */
    fake_chiaki(3200, CHIAKI_EVENT_RUMBLE, 220);
    tap(5000, 0, SDL_CONTROLLER_BUTTON_START, 1300);
    tap(5000, 0, SDL_CONTROLLER_BUTTON_BACK, 1300);
    int rc = run(BASE PS5);
    CHECK(rc == 0);
    CHECK(fake_record.rumble_low_by[0] == 100u * 257u); /* the 220 went to the DualSense only */
    CHECK(fake_record.rumble_low_by[1] == 220u * 257u);
    CHECK(fake_device_refs(2) == 0); /* the 0-button sensor was opened and given back */
}

static void scenario_saved_pin_rejected_then_entered(void)
{
    scenario = "saved pin rejected";
    fake_reset(60000);
    add_builtin_pad(0);
    fake_chiaki(1500, CHIAKI_EVENT_LOGIN_PIN_REQUEST, 0);  /* saved 1111 goes in */
    fake_chiaki(1800, CHIAKI_EVENT_LOGIN_PIN_REQUEST, 1);  /* refused: the screen opens */
    tap(2200, 0, SDL_CONTROLLER_BUTTON_DPAD_DOWN, 40);     /* first digit 0 -> 9 (never pre-filled) */
    tap(2400, 0, SDL_CONTROLLER_BUTTON_B, 40);             /* "A": enter 9000 */
    fake_chiaki(2700, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(2900, 9000, 33);
    tap(4000, 0, SDL_CONTROLLER_BUTTON_START, 1300);
    tap(4000, 0, SDL_CONTROLLER_BUTTON_BACK, 1300);
    int rc = run(BASE PS5 "login_pin=1111\n");
    CHECK(rc == 0 && has("result=ok\n") && has("saved_pin_rejected=1\n"));
    CHECK(fake_record.pin_count == 2 && !strcmp(fake_record.pins[0], "1111") && !strcmp(fake_record.pins[1], "9000"));
    CHECK(buttons_at(2500) == 0); /* the passcode screen kept the pad idle */
}

static void scenario_builtin_pad_by_its_id(void)
{
    scenario = "built-in pad by its id";
    fake_reset(60000);
    add_builtin_pad(0); /* "Xbox 360 Controller" to SDL */
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 6000, 33);
    tap(2000, 0, SDL_CONTROLLER_BUTTON_A, 50);          /* the built-in pad is in use */
    fake_chiaki(2200, CHIAKI_EVENT_RUMBLE, 200);          /* so its motor rumbles */
    tap(2500, 0, SDL_CONTROLLER_BUTTON_DPAD_LEFT, 100);   /* and its D-pad mode applies */
    tap(4000, 0, SDL_CONTROLLER_BUTTON_START, 1300);
    tap(4000, 0, SDL_CONTROLLER_BUTTON_BACK, 1300);
    put_file(MOTOR, "");
    char session[1400];
    snprintf(session, sizeof(session), BASE PS5 "dpad_stick=1\nmotor_gpio=%s\n", MOTOR);
    int rc = run(session);
    CHECK(rc == 0);
    char motor[16];
    read_small(MOTOR, motor, sizeof(motor));
    CHECK(!strcmp(motor, "0")); /* switched on for the rumble, off at the end */
    CHECK(stick_x_at(-32767, 2500, 2599) && !had_bit(CHIAKI_CONTROLLER_BUTTON_DPAD_LEFT, 2500, 2600));
}

static void scenario_usb_xbox_pad_is_external(void)
{
    scenario = "usb xbox 360 pad";
    fake_reset(60000);
    add_builtin_pad(0);
    int pad = fake_add_device(true, "Xbox 360 Controller", 4);
    fake_device_ids(pad, 0x045e, 0x028e);
    fake_paths[0] = "/dev/input/event3"; /* TRIMUI Player1 */
    fake_paths[pad] = "/dev/input/event7"; /* Microsoft X-Box 360 pad */
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 8000, 33);
    tap(2000, 4, SDL_CONTROLLER_BUTTON_DPAD_LEFT, 100); /* its D-pad stays a D-pad */
    tap(2500, 0, SDL_CONTROLLER_BUTTON_DPAD_LEFT, 100); /* the handheld's moves the stick */
    tap(3000, 4, SDL_CONTROLLER_BUTTON_GUIDE, 1300);    /* its Guide is PS, held */
    tap(5000, 0, SDL_CONTROLLER_BUTTON_START, 1300);
    tap(5000, 0, SDL_CONTROLLER_BUTTON_BACK, 1300);
    int rc = run(BASE PS5 "dpad_stick=1\n");
    CHECK(rc == 0);
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_DPAD_LEFT, 2000, 2099) && !stick_x_at(-32767, 2000, 2099));
    CHECK(stick_x_at(-32767, 2500, 2599) && !had_bit(CHIAKI_CONTROLLER_BUTTON_DPAD_LEFT, 2500, 2599));
    CHECK((buttons_at(3100) & CHIAKI_CONTROLLER_BUTTON_PS) && (buttons_at(4200) & CHIAKI_CONTROLLER_BUTTON_PS));
    CHECK(!(buttons_at(4400) & CHIAKI_CONTROLLER_BUTTON_PS));
    CHECK(fake_now >= 5000 + 1200 && fake_now < 6300);
    memset(fake_paths, 0, sizeof(fake_paths));
}

static void scenario_dualsense_layout(void)
{
    scenario = "dualsense layout";
    fake_reset(60000);
    add_builtin_pad(0);
    add_dualsense(5);
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 9000, 33);
    tap(2000, 5, SDL_CONTROLLER_BUTTON_BACK, 100);    /* Create */
    tap(2300, 0, SDL_CONTROLLER_BUTTON_BACK, 100);    /* the handheld's Select: the touchpad */
    tap(2600, 5, SDL_CONTROLLER_BUTTON_DPAD_UP, 100); /* a D-pad, D-pad mode or not */
    tap(3000, 5, SDL_CONTROLLER_BUTTON_GUIDE, 1500);  /* PS held, no stream menu */
    tap(5000, 0, SDL_CONTROLLER_BUTTON_GUIDE, 60);    /* the handheld's Menu tap is still PS */
    fake_button(6000, 5, SDL_CONTROLLER_BUTTON_START, true); /* Options + Create quits */
    fake_button(6000, 5, SDL_CONTROLLER_BUTTON_BACK, true);
    fake_button(7400, 5, SDL_CONTROLLER_BUTTON_BACK, false);
    fake_button(7400, 5, SDL_CONTROLLER_BUTTON_START, false);
    int rc = run(BASE PS5 "dpad_stick=1\n");
    CHECK(rc == 0 && has("result=ok\n"));
#if SDL_VERSION_ATLEAST(2, 0, 12)
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_SHARE, 2000, 2099) && !had_bit(CHIAKI_CONTROLLER_BUTTON_TOUCHPAD, 1900, 2200));
#endif
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_TOUCHPAD, 2300, 2399));
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_DPAD_UP, 2600, 2699) && !stick_x_at(-32767, 2600, 2699));
    CHECK((buttons_at(3100) & CHIAKI_CONTROLLER_BUTTON_PS) && (buttons_at(4400) & CHIAKI_CONTROLLER_BUTTON_PS));
    CHECK(!(buttons_at(4600) & CHIAKI_CONTROLLER_BUTTON_PS));
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_PS, 5060, 5100));
    CHECK(fake_now >= 6000 + 1200 && fake_now < 7400);
}

/* PlayStation pads that reach SDL through the kernel's event device with no
 * mapping in SDL's database: a DualShock 4 on the TrimUI's 4.9 kernel (the HID
 * report's order, touchpad click included) and a DualSense through
 * hid-playstation (evdev gamepad order, touchpad on a node of its own). The
 * helper maps both by GUID, so they open as game controllers. */
static void scenario_sony_raw_pads_get_mappings(void)
{
    scenario = "sony raw pads get mappings";
    fake_reset(60000);
    add_builtin_pad(0);
    int ds4 = fake_add_device(false, "Wireless Controller", 5);
    fake_device_ids(ds4, 0x054c, 0x09cc);
    fake_device_type(ds4, 4);
    fake_device_sony(ds4, 14, 6, 1);
    int ds = fake_add_device(false, "DualSense Wireless Controller", 6);
    fake_device_ids(ds, 0x054c, 0x0ce6);
#if SDL_VERSION_ATLEAST(2, 0, 14)
    fake_device_type(ds, 7);
#endif
    fake_device_no_touchpad(ds);
    fake_device_sony(ds, 13, 6, 1);
    int other = fake_add_device(false, "Generic USB Joystick", 7);
    fake_device_sony(other, 14, 6, 1); /* the same counts, but not Sony's */
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 6000, 33);
    tap(2000, 5, SDL_CONTROLLER_BUTTON_A, 100);    /* Cross, through the mapping */
    tap(2300, 5, SDL_CONTROLLER_BUTTON_BACK, 100); /* DualShock 4 Share */
    tap(2600, 6, SDL_CONTROLLER_BUTTON_BACK, 100); /* DualSense Create, no touchpad here */
    tap(2900, 6, SDL_CONTROLLER_BUTTON_B, 100);    /* Circle */
    int rc = run(BASE PS5);
    CHECK(rc == 0);
    CHECK(fake_device_is_gc(ds4) && fake_device_is_gc(ds) && !fake_device_is_gc(other));
    CHECK(fake_record.mappings == 3); /* the TrimUI pad's + one per Sony pad */
    CHECK(strncmp(fake_record.mapping, fake_device_guid(ds), 32) == 0);
    CHECK(strstr(fake_record.mapping, ",PS5 Controller,a:b0,b:b1,x:b3,y:b2,") != NULL);
    CHECK(fake_device_refs(ds4) == 0 && fake_device_refs(ds) == 0 && fake_device_refs(other) == 0);
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_CROSS, 2000, 2099));
#if SDL_VERSION_ATLEAST(2, 0, 14)
    /* The DualShock 4's touchpad click is in its mapping: Share is Create. */
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_SHARE, 2300, 2399) && !had_bit(CHIAKI_CONTROLLER_BUTTON_TOUCHPAD, 2300, 2399));
#endif
    /* The DualSense cannot click its touchpad here: its Create button does. */
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_TOUCHPAD, 2600, 2699) && !had_bit(CHIAKI_CONTROLLER_BUTTON_SHARE, 2600, 2699));
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_MOON, 2900, 2999));
}

#if SDL_VERSION_ATLEAST(2, 0, 14)
static const ChiakiControllerState *state_at(uint32_t t); /* below */

/* A DualSense through SDL's own driver: its gyro and accelerometer reach the
 * console, with an orientation; when it leaves, the console's pad lies still. */
static void scenario_motion(void)
{
    scenario = "motion";
    fake_reset(60000);
    add_builtin_pad(0);
    int ds = add_dualsense(5);
    fake_device_sensors(ds);
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 6000, 33);
    tap(1800, 5, SDL_CONTROLLER_BUTTON_A, 50);
    for(int i = 0; i < 250; i++) { /* 1 s at 250 Hz: turning about "up" at 1 rad/s */
        fake_sensor(2000 + (uint32_t)i * 4, 5, SDL_SENSOR_ACCEL, 0.0f, SDL_STANDARD_GRAVITY, 0.0f);
        fake_sensor(2000 + (uint32_t)i * 4, 5, SDL_SENSOR_GYRO, 0.0f, 1.0f, 0.0f);
    }
    tap(2500, 0, SDL_CONTROLLER_BUTTON_A, 50); /* the handheld has no motion: still the DualSense's */
    fake_device_removed(4000, 5);
    int rc = run(BASE PS5);
    CHECK(rc == 0);
    CHECK(fake_device_sensors_on(ds) == 2);
    const ChiakiControllerState *s = state_at(3500);
    CHECK(s && fabsf(s->gyro_y - 1.0f) < 1e-6f && fabsf(s->accel_y - 1.0f) < 1e-4f);
    CHECK(s && fabsf(fabsf(s->orient_y) - sinf(0.5f)) < 0.02f && fabsf(s->orient_w - cosf(0.5f)) < 0.02f);
    s = state_at(4500);
    CHECK(s && s->gyro_y == 0.0f && s->accel_y == 1.0f && s->orient_w == 1.0f && s->orient_y == 0.0f);
}
#endif

static void scenario_split_quit_combo(void)
{
    scenario = "split quit combo";
    fake_reset(60000);
    add_builtin_pad(0);
    add_dualsense(5);
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 9000, 33);
    tap(2000, 0, SDL_CONTROLLER_BUTTON_START, 1500); /* Start on the handheld... */
    tap(2000, 5, SDL_CONTROLLER_BUTTON_BACK, 1500);  /* ...Select on the DualSense: two players */
    tap(5000, 0, SDL_CONTROLLER_BUTTON_START, 1300); /* both on one pad quits */
    tap(5000, 0, SDL_CONTROLLER_BUTTON_BACK, 1300);
    int rc = run(BASE PS5);
    CHECK(rc == 0);
    CHECK(buttons_at(3300) & CHIAKI_CONTROLLER_BUTTON_OPTIONS); /* still Options on the console */
    CHECK(fake_now >= 5000 + 1200 && fake_now < 6300);
}

#if SDL_VERSION_ATLEAST(2, 0, 14)
static void touchpad(uint32_t at, SDL_JoystickID which, int finger, Uint32 type, float x, float y)
{
    SDL_Event e;
    memset(&e, 0, sizeof(e));
    e.type = type;
    e.ctouchpad.which = which;
    e.ctouchpad.touchpad = 0;
    e.ctouchpad.finger = finger;
    e.ctouchpad.x = x;
    e.ctouchpad.y = y;
    fake_sdl_event(at, e);
}

static void scenario_two_fingers(void)
{
    scenario = "two fingers";
    fake_reset(60000);
    add_builtin_pad(0);
    add_dualsense(5);
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 6000, 33);
    touchpad(2000, 5, 0, SDL_CONTROLLERTOUCHPADDOWN, 0.2f, 0.5f);
    touchpad(2100, 5, 1, SDL_CONTROLLERTOUCHPADDOWN, 0.8f, 0.5f);
    touchpad(2300, 5, 0, SDL_CONTROLLERTOUCHPADUP, 0.2f, 0.5f); /* finger 1 keeps its touch */
    touchpad(2400, 5, 1, SDL_CONTROLLERTOUCHPADMOTION, 0.7f, 0.4f);
    touchpad(2500, 5, 1, SDL_CONTROLLERTOUCHPADUP, 0.7f, 0.4f);
    tap(4000, 0, SDL_CONTROLLER_BUTTON_START, 1300);
    tap(4000, 0, SDL_CONTROLLER_BUTTON_BACK, 1300);
    int rc = run(BASE PS5);
    CHECK(rc == 0);
    CHECK(fake_record.touch_starts == 2 && fake_record.touch_stops == 2);
    CHECK(fake_record.touch_moves >= 1);
}
#endif

static void scenario_seventh_pad_is_refused(void)
{
    scenario = "seventh pad";
    fake_reset(60000);
    add_builtin_pad(0);
    for(int i = 1; i <= SI_MAX_PADS; i++) fake_add_device(true, "Wireless Controller", (SDL_JoystickID)(10 + i));
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 6000, 33);
    tap(2000, 10 + SI_MAX_PADS, SDL_CONTROLLER_BUTTON_A, 50); /* the pad that found no room */
    fake_chiaki(2200, CHIAKI_EVENT_RUMBLE, 180);
    tap(4000, 0, SDL_CONTROLLER_BUTTON_START, 1300);
    tap(4000, 0, SDL_CONTROLLER_BUTTON_BACK, 1300);
    int rc = run(BASE PS5);
    CHECK(rc == 0);
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_CROSS, 2000, 2100));
    CHECK(fake_record.rumble_low_by[SI_MAX_PADS] == 0); /* nor the rumble */
    CHECK(fake_record.rumble_low_by[0] == 180u * 257u);
    CHECK(fake_device_refs(SI_MAX_PADS) == 0);
}

static void scenario_steady_rumble_is_renewed(void)
{
    scenario = "steady rumble";
    fake_reset(60000);
    add_builtin_pad(0);
    int ds = add_dualsense(5);
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 16000, 33);
    tap(1800, 5, SDL_CONTROLLER_BUTTON_A, 50);   /* the DualSense is in use */
    fake_chiaki(2000, CHIAKI_EVENT_RUMBLE, 150); /* one event, then a steady rumble */
    tap(14000, 0, SDL_CONTROLLER_BUTTON_START, 1300);
    tap(14000, 0, SDL_CONTROLLER_BUTTON_BACK, 1300);
    int rc = run(BASE PS5);
    CHECK(rc == 0);
    CHECK(fake_record.rumble_low_by[ds] == 150u * 257u);
    CHECK(fake_record.rumble_last_at[ds] >= 13000); /* still going long after SDL's 10 s */
}

/* ---- scenarios for the C helper review fixes (session 4874af7d) ---- */

/* fake_draw.c */
void fake_draw_reset(void);
int fake_video_textures(void);
int fake_video_textures_alive(void);
int fake_video_w(void);
int fake_video_h(void);

static char WORKDIR[400];

/* The state the console holds at time t (the last one sent at or before t). */
static const ChiakiControllerState *state_at(uint32_t t)
{
    const ChiakiControllerState *s = NULL;
    for(int i = 0; i < fake_record.states && fake_record.state_at[i] <= t; i++) s = &fake_record.state[i];
    return s;
}

static void quit_combo(uint32_t at)
{
    tap(at, 0, SDL_CONTROLLER_BUTTON_START, 1300);
    tap(at, 0, SDL_CONTROLLER_BUTTON_BACK, 1300);
}

/* M1: letting go of one half of Start + Select early sends nothing. */
static void scenario_quit_combo_released_early(void)
{
    scenario = "quit combo released early";
    fake_reset(60000);
    fake_add_device(true, "TRIMUI Player1", 0);
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 9000, 33);
    fake_button(3000, 0, SDL_CONTROLLER_BUTTON_START, true);
    fake_button(3050, 0, SDL_CONTROLLER_BUTTON_BACK, true);
    fake_button(3300, 0, SDL_CONTROLLER_BUTTON_BACK, false);  /* Select first */
    fake_button(3600, 0, SDL_CONTROLLER_BUTTON_START, false);
    fake_button(4000, 0, SDL_CONTROLLER_BUTTON_BACK, true);
    fake_button(4050, 0, SDL_CONTROLLER_BUTTON_START, true);
    fake_button(4300, 0, SDL_CONTROLLER_BUTTON_START, false); /* Start first */
    fake_button(4600, 0, SDL_CONTROLLER_BUTTON_BACK, false);
    quit_combo(6000);
    int rc = run(BASE PS5);
    CHECK(rc == 0 && has("result=ok\n"));
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_OPTIONS, 3000, 3049));
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_OPTIONS, 3060, 3700));
    CHECK(!(buttons_at(3450) & CHIAKI_CONTROLLER_BUTTON_OPTIONS));
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_TOUCHPAD, 3050, 3700));
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_TOUCHPAD, 4000, 4049));
    CHECK(!(buttons_at(4450) & CHIAKI_CONTROLLER_BUTTON_TOUCHPAD));
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_TOUCHPAD, 4060, 4700) && !had_bit(CHIAKI_CONTROLLER_BUTTON_OPTIONS, 4050, 4700));
}

/* M2: Start is not "enter" on the passcode screen; L12: B gives up while the
 * console is still checking. */
static void scenario_pin_start_and_cancel_while_checking(void)
{
    scenario = "pin: start, cancel while checking";
    fake_reset(10000);
    fake_add_device(true, "TRIMUI Player1", 0);
    fake_chiaki(1500, CHIAKI_EVENT_LOGIN_PIN_REQUEST, 0);
    tap(1800, 0, SDL_CONTROLLER_BUTTON_START, 40); /* not a submit */
    tap(2000, 0, SDL_CONTROLLER_BUTTON_B, 40);     /* "A": enter 0000 */
    tap(2300, 0, SDL_CONTROLLER_BUTTON_A, 40);     /* "B" while checking: give up */
    int rc = run(BASE PS5);
    CHECK(rc == 12 && has("result=login_pin_cancelled\n"));
    CHECK(fake_record.pin_count == 1 && !strcmp(fake_record.pins[0], "0000"));
    CHECK(!fake_record.timed_out && fake_now < 2600);
}

/* M3 / M4 / L5: when the video fails mid-game the console gets an idle pad, the
 * session is stopped and the rumble is off before the error card shows. */
static void scenario_video_failure_lets_go(void)
{
    scenario = "video failure lets go";
    char gpio[500], level[500], text[64], session[1400];
    snprintf(gpio, sizeof(gpio), "%s/e2e-vf-gpio", WORKDIR);
    snprintf(level, sizeof(level), "%s/e2e-vf-level", WORKDIR);
    put_file(gpio, "1");
    put_file(level, "0");
    fake_reset(60000);
    snprintf(fake_record.watch_path, sizeof(fake_record.watch_path), "%s", gpio);
    fake_record.fail_update_after = 60;
    fake_add_device(true, "TRIMUI Player1", 0);
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 9000, 33);
    fake_button(2500, 0, SDL_CONTROLLER_BUTTON_A, true);           /* Cross held through it */
    fake_axis(2500, 0, SDL_CONTROLLER_AXIS_TRIGGERRIGHT, 32767);   /* and R2 */
    fake_chiaki(2600, CHIAKI_EVENT_RUMBLE, 200);
    snprintf(session, sizeof(session), BASE PS5 "motor_gpio=%s\nmotor_level=%s\nmotor_level_min=0\nmotor_level_max=65535\n",
             gpio, level);
    int rc = run(session);
    CHECK(rc == 10 && has("result=video_failed\n"));
    CHECK(fake_record.stop_at && fake_record.pause_at && fake_record.stop_at <= fake_record.pause_at);
    const ChiakiControllerState *last = state_at(fake_record.stop_at);
    CHECK(last && last->buttons == 0 && last->r2_state == 0);
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_CROSS, 2500, 2600));
    CHECK(fake_record.rumble_low_by[0] == 200u * 257u);              /* it did rumble */
    CHECK(fake_record.rumble_last_low_by[0] == 0 && fake_record.rumble_zero_at &&
          fake_record.rumble_zero_at <= fake_record.pause_at);      /* and stopped before the card */
    CHECK(!strcmp(fake_record.watch_at_pause, "0"));                 /* the motor too */
    read_small(level, text, sizeof(text));
    CHECK(strcmp(text, "0") != 0);                                   /* the motor had been driven */
}

/* L8: a resolution change mid-stream gets a texture of the new size. */
static void scenario_resolution_change(void)
{
    scenario = "resolution change";
    fake_reset(60000);
    fake_draw_reset();
    fake_add_device(true, "TRIMUI Player1", 0);
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames_sized(1500, 3000, 33, 1280, 720);
    fake_frames_sized(3033, 4500, 33, 960, 540);
    quit_combo(5000);
    int rc = run(BASE PS5);
    CHECK(rc == 0 && has("result=ok\n"));
    CHECK(fake_video_textures() == 2);
    CHECK(fake_video_w() == 960 && fake_video_h() == 540);
    CHECK(fake_video_textures_alive() == 0);
}

/* L13: the stream menu's "Hold PS (Home)" holds PS about 1.2 s after it closes. */
static void scenario_hold_ps_row(void)
{
    scenario = "hold PS row";
    fake_reset(60000);
    fake_add_device(true, "TRIMUI Player1", 0);
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 12000, 33);
    fake_button(3000, 0, SDL_CONTROLLER_BUTTON_GUIDE, true);  /* the menu opens at 3900 */
    fake_button(4100, 0, SDL_CONTROLLER_BUTTON_GUIDE, false);
    tap(4200, 0, SDL_CONTROLLER_BUTTON_DPAD_DOWN, 50);        /* Quit */
    tap(4300, 0, SDL_CONTROLLER_BUTTON_DPAD_DOWN, 50);        /* Hold PS (Home) */
    tap(4400, 0, SDL_CONTROLLER_BUTTON_B, 50);                /* "A" */
    quit_combo(7000);
    int rc = run(BASE PS5);
    CHECK(rc == 0 && has("result=ok\n"));
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_PS, 3000, 4399));
    CHECK(buttons_at(4450) & CHIAKI_CONTROLLER_BUTTON_PS);
    CHECK(buttons_at(5500) & CHIAKI_CONTROLLER_BUTTON_PS);
    CHECK(!(buttons_at(5700) & CHIAKI_CONTROLLER_BUTTON_PS));
    CHECK(!had_bit(CHIAKI_CONTROLLER_BUTTON_MOON, 4400, 6000)); /* the confirm press stayed in the menu */
}

/* M6: the TrimUI mapping is added before any pad opens, and a six-axis raw pad
 * is read in the X360 order (triggers on axes 2 and 5, not on the right stick). */
static void scenario_mapping_and_xpad_raw(void)
{
    scenario = "mapping + xpad raw";
    fake_reset(60000);
    fake_add_device(true, "TRIMUI Player1", 0);
    int raw = fake_add_device(false, "Generic X-Box pad", 9);
    fake_device_axes(raw, 6);
    fake_chiaki(1300, CHIAKI_EVENT_CONNECTED, 0);
    fake_frames(1500, 9000, 33);
    fake_joy_axis(2000, 9, 2, -32768); /* left trigger at rest */
    fake_joy_axis(2000, 9, 5, 32767);  /* right trigger pressed */
    fake_joy_axis(2100, 9, 3, 20000);  /* right stick X */
    fake_joy_axis(2150, 9, 3, 0);
    joy_button(2200, 9, 0, true);      /* A = Cross */
    joy_button(2300, 9, 0, false);
    joy_button(2500, 9, 8, true);      /* Guide: an external pad's PS, held through */
    joy_button(2600, 9, 8, false);
    joy_button(4000, 9, 7, true);      /* Start + Back on this pad = quit */
    joy_button(4000, 9, 6, true);
    joy_button(5500, 9, 7, false);
    joy_button(5500, 9, 6, false);
    int rc = run(BASE PS5);
    CHECK(rc == 0 && has("result=ok\n"));
    CHECK(fake_record.mappings == 1 && fake_record.mapping_before_open);
    CHECK(strstr(fake_record.mapping, "030000005e0400008e02000014010000,X360 Controller,a:b0,b:b1,back:b6,") == fake_record.mapping);
    CHECK(strstr(fake_record.mapping, "guide:b8") != NULL && strstr(fake_record.mapping, "platform:Linux,") != NULL);
    bool r2 = false, stick_still = true, rx = false;
    for(int i = 0; i < fake_record.states; i++) {
        uint32_t at = fake_record.state_at[i];
        if(at >= 2000 && at < 2100) {
            if(fake_record.state[i].r2_state == 255) r2 = true;
            if(fake_record.state[i].right_x != 0 || fake_record.state[i].l2_state != 0) stick_still = false;
        }
        if(at >= 2100 && at < 2150 && fake_record.state[i].right_x == 20000) rx = true;
    }
    CHECK(r2 && stick_still && rx);
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_CROSS, 2200, 2299));
    CHECK(had_bit(CHIAKI_CONTROLLER_BUTTON_PS, 2500, 2599));
    CHECK(!(buttons_at(2700) & CHIAKI_CONTROLLER_BUTTON_PS));
    CHECK(fake_now >= 4000 + 1200 && fake_now < 5500);
}

/* L4: SIGTERM ends the error card at once instead of after its 8 s. */
static void scenario_sigterm_ends_card(void)
{
    scenario = "sigterm ends the card";
    fake_reset(2500); /* the fake raises SIGTERM once the clock passes this */
    fake_add_device(true, "TRIMUI Player1", 0);
    fake_chiaki(2000, CHIAKI_EVENT_QUIT, 5);
    int rc = run(BASE PS5);
    CHECK(rc == 11 && has("result=connect_failed\n"));
    CHECK(fake_record.timed_out && fake_now < 2700);
}

/* L11: a console that asked for the passcode and then hung up did answer. */
static void scenario_login_pin_failed(void)
{
    scenario = "login pin failed";
    fake_reset(60000);
    fake_add_device(true, "TRIMUI Player1", 0);
    fake_chiaki(1500, CHIAKI_EVENT_LOGIN_PIN_REQUEST, 0);
    tap(1800, 0, SDL_CONTROLLER_BUTTON_B, 40); /* "A": enter 0000 */
    fake_chiaki(2200, CHIAKI_EVENT_QUIT, 5);
    tap(2900, 0, SDL_CONTROLLER_BUTTON_A, 40); /* closes the card */
    int rc = run(BASE PS5);
    CHECK(rc == 11 && has("result=login_pin_failed\n"));
    CHECK(strstr(status, "ended the connection at the login passcode step (Connection refused - fake detail)") != NULL);
    CHECK(strstr(status, "did not answer") == NULL);
}

int main(int argc, char **argv)
{
    const char *base = argc > 1 ? argv[1] : "/tmp";
    snprintf(WORKDIR, sizeof(WORKDIR), "%s", base);
    snprintf(SESSION, sizeof(SESSION), "%s/e2e-session.conf", base);
    snprintf(STATUS, sizeof(STATUS), "%s/e2e-status.txt", base);
    snprintf(SYSFS, sizeof(SYSFS), "%s/e2e-sysfs", base);
    snprintf(MOTOR, sizeof(MOTOR), "%s/e2e-motor-gpio", base);
    fake_input_device("event3", "TRIMUI Player1\n");
    fake_input_device("event7", "Microsoft X-Box 360 pad\n");
    stream_sysfs_root = SYSFS;
    stream_joystick_path_hook = fake_path;
    scenario_happy();
    scenario_pin_entry();
    scenario_saved_pin_and_quit_combo();
    scenario_connect_failed();
    scenario_console_quit();
    scenario_invalid();
    scenario_hotplug();
    scenario_ps4_codec_and_decoder_failure();
    scenario_fallback_joystick();
    scenario_dpad_stick_and_rumble_off();
    scenario_rumble_follows_the_pad();
    scenario_saved_pin_rejected_then_entered();
    scenario_builtin_pad_by_its_id();
    scenario_usb_xbox_pad_is_external();
    scenario_dualsense_layout();
    scenario_sony_raw_pads_get_mappings();
#if SDL_VERSION_ATLEAST(2, 0, 14)
    scenario_motion();
#endif
    scenario_split_quit_combo();
#if SDL_VERSION_ATLEAST(2, 0, 14)
    scenario_two_fingers();
#endif
    scenario_seventh_pad_is_refused();
    scenario_steady_rumble_is_renewed();
    scenario_quit_combo_released_early();
    scenario_pin_start_and_cancel_while_checking();
    scenario_video_failure_lets_go();
    scenario_resolution_change();
    scenario_hold_ps_row();
    scenario_mapping_and_xpad_raw();
    scenario_sigterm_ends_card();
    scenario_login_pin_failed();
    printf("chiaki-stream end-to-end: %d checks, %d failures\n", checks, failures);
    return failures ? 1 : 0;
}
