/* Test stub, NOT the real header: only the declarations chiaki-stream uses, for tests/run-host-tests.sh. */
#ifndef STUB_CHIAKI_CONTROLLER_H
#define STUB_CHIAKI_CONTROLLER_H
#include "common.h"
typedef enum chiaki_controller_button_t {
    CHIAKI_CONTROLLER_BUTTON_CROSS = (1 << 0), CHIAKI_CONTROLLER_BUTTON_MOON = (1 << 1),
    CHIAKI_CONTROLLER_BUTTON_BOX = (1 << 2), CHIAKI_CONTROLLER_BUTTON_PYRAMID = (1 << 3),
    CHIAKI_CONTROLLER_BUTTON_DPAD_LEFT = (1 << 4), CHIAKI_CONTROLLER_BUTTON_DPAD_RIGHT = (1 << 5),
    CHIAKI_CONTROLLER_BUTTON_DPAD_UP = (1 << 6), CHIAKI_CONTROLLER_BUTTON_DPAD_DOWN = (1 << 7),
    CHIAKI_CONTROLLER_BUTTON_L1 = (1 << 8), CHIAKI_CONTROLLER_BUTTON_R1 = (1 << 9),
    CHIAKI_CONTROLLER_BUTTON_L3 = (1 << 10), CHIAKI_CONTROLLER_BUTTON_R3 = (1 << 11),
    CHIAKI_CONTROLLER_BUTTON_OPTIONS = (1 << 12), CHIAKI_CONTROLLER_BUTTON_SHARE = (1 << 13),
    CHIAKI_CONTROLLER_BUTTON_TOUCHPAD = (1 << 14), CHIAKI_CONTROLLER_BUTTON_PS = (1 << 15)
} ChiakiControllerButton;
typedef struct chiaki_controller_touch_t { uint16_t x, y; int8_t id; } ChiakiControllerTouch;
#define CHIAKI_CONTROLLER_TOUCHES_MAX 2
typedef struct chiaki_controller_state_t {
    uint32_t buttons; uint8_t l2_state; uint8_t r2_state;
    int16_t left_x, left_y, right_x, right_y;
    uint8_t touch_id_next; ChiakiControllerTouch touches[CHIAKI_CONTROLLER_TOUCHES_MAX];
    float gyro_x, gyro_y, gyro_z, accel_x, accel_y, accel_z, orient_x, orient_y, orient_z, orient_w;
} ChiakiControllerState;
CHIAKI_EXPORT void chiaki_controller_state_set_idle(ChiakiControllerState *state);
CHIAKI_EXPORT int8_t chiaki_controller_state_start_touch(ChiakiControllerState *state, uint16_t x, uint16_t y);
CHIAKI_EXPORT void chiaki_controller_state_stop_touch(ChiakiControllerState *state, uint8_t id);
CHIAKI_EXPORT void chiaki_controller_state_set_touch_pos(ChiakiControllerState *state, uint8_t id, uint16_t x, uint16_t y);
#endif
