#ifndef FAKE_RUNTIME_H
#define FAKE_RUNTIME_H
#include <SDL2/SDL.h>
#include <chiaki/session.h>
#define FAKE_MAX_STATES 20000
typedef enum { FAKE_SDL_EVENT, FAKE_CHIAKI_EVENT, FAKE_FRAME } FakeKind;
typedef struct { uint32_t at; int seq; FakeKind kind; SDL_Event sdl; ChiakiEvent chiaki; int w, h; } FakeAction;
typedef struct {
    char host[256]; int codec; bool ps5; char regist_key[16];
    int started, stopped, fini, presents, opens, frames_pulled;
    ChiakiControllerState state[FAKE_MAX_STATES]; uint32_t state_at[FAKE_MAX_STATES]; int states;
    char pins[8][8]; int pin_count;
    int touch_starts, touch_stops, touch_moves;
    int rumble_calls; unsigned rumble_low, rumble_high, rumble_ms; unsigned rumble_low_by[8];
    uint32_t rumble_last_at[8]; /* per device: the clock at its last non-zero rumble call */
    int audio_paused; bool timed_out; bool fail_decoder;
    unsigned rumble_last_low_by[8]; uint32_t rumble_zero_at; /* last call's strength; the clock at the last (0, 0) */
    uint32_t stop_at, pause_at;           /* chiaki_session_stop, the card's SDL_PauseAudioDevice(1) */
    int fail_update_after;                /* SDL_UpdateYUVTexture fails after this many (0 = never) */
    int updates;
    char watch_path[512]; char watch_at_pause[32]; /* a file read when the card starts */
    int mappings; bool mapping_before_open; char mapping[512];
} FakeRecord;
extern FakeRecord fake_record;
extern uint32_t fake_now;
void fake_reset(uint32_t limit);
int fake_add_device(bool gc, const char *name, SDL_JoystickID id);
int fake_device_refs(int index);
void fake_device_buttons(int index, int buttons);
void fake_device_axes(int index, int axes);
void fake_frames_sized(uint32_t from, uint32_t to, uint32_t every, int w, int h);
void fake_joy_axis(uint32_t at, SDL_JoystickID which, int axis, int value);
/* The USB id SDL reports (SDL_JoystickGetVendor/Product) and SDL's controller
 * type (SDL_GameControllerGetType, a SDL_CONTROLLER_TYPE_ value). */
void fake_device_ids(int index, unsigned vendor, unsigned product);
void fake_device_type(int index, int type);
/* The device a joystick handle belongs to (for a SDL_JoystickPath stand-in). */
int fake_device_index(SDL_Joystick *joystick);
void fake_sdl_event(uint32_t at, SDL_Event event);
void fake_button(uint32_t at, SDL_JoystickID which, int button, bool down);
void fake_axis(uint32_t at, SDL_JoystickID which, int axis, int value);
void fake_device_added(uint32_t at, int index);
void fake_device_removed(uint32_t at, SDL_JoystickID id);
void fake_chiaki(uint32_t at, ChiakiEventType type, int value);
void fake_frames(uint32_t from, uint32_t to, uint32_t every);
void fake_sort(void);
#endif
