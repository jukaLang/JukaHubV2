#include "gamepadmap.h"

#include <string.h>

enum {
    KEY_BACK = 4, KEY_DPAD_UP = 19, KEY_DPAD_DOWN = 20,
    KEY_DPAD_LEFT = 21, KEY_DPAD_RIGHT = 22, KEY_P = 44, KEY_T = 48,
    KEY_TAB = 61, KEY_SPACE = 62, KEY_ENTER = 66,
    KEY_BUTTON_A = 96, KEY_BUTTON_B = 97, KEY_BUTTON_X = 99,
    KEY_BUTTON_Y = 100, KEY_BUTTON_L1 = 102, KEY_BUTTON_R1 = 103,
    KEY_BUTTON_L2 = 104, KEY_BUTTON_R2 = 105,
    KEY_BUTTON_THUMBL = 106, KEY_BUTTON_THUMBR = 107,
    KEY_BUTTON_START = 108, KEY_BUTTON_SELECT = 109
};

/* BTN_SOUTH .. BTN_THUMBR and BTN_DPAD_UP .. BTN_DPAD_RIGHT. */
static int buttonIndex(unsigned short code) {
    if (code >= 0x130 && code <= 0x13e) return code - 0x130;
    if (code >= 0x220 && code <= 0x223) return 16 + code - 0x220;
    return -1;
}

static int axisIndex(unsigned short code) {
    if (code <= 5) return code;
    if (code == 0x10 || code == 0x11) return 6 + code - 0x10;
    return -1;
}

static float clamp(float value, float lo, float hi) {
    return value < lo ? lo : value > hi ? hi : value;
}

ApakGamepadMode apak_gamepadMode(const char *packageName, const char *option) {
    if (option && *option) {
        if (strcmp(option, "keyboard") == 0) return APAK_GAMEPAD_KEYBOARD;
        if (strcmp(option, "penguin-dash") == 0) return APAK_GAMEPAD_PENGUIN_DASH;
        /* "native" also lets a profile override the built-in game preset. */
        return APAK_GAMEPAD_NATIVE;
    }
    if (packageName && strcmp(packageName, "com.anathemasit.penguindash") == 0)
        return APAK_GAMEPAD_PENGUIN_DASH;
    return APAK_GAMEPAD_NATIVE;
}

const char *apak_gamepadModeName(ApakGamepadMode mode) {
    switch (mode) {
        case APAK_GAMEPAD_KEYBOARD: return "keyboard";
        case APAK_GAMEPAD_PENGUIN_DASH: return "penguin-dash";
        default: return "native";
    }
}

void apak_gamepadInit(ApakGamepadMap *map, ApakGamepadMode mode) {
    memset(map, 0, sizeof *map);
    map->mode = mode;
}

void apak_gamepadSetSwapAB(ApakGamepadMap *map, int enabled) {
    map->swapAB = enabled != 0;
}

/* Linux triggers may report 0..255 or -32768..32767. Android's Z/RZ
 * triggers are 0..1; sticks and hats are -1..1. The actual EVIOCGABS limits
 * come from the opened controller, not a guessed fixed divisor. */
float apak_gamepadNormalize(unsigned short code, int value, int min, int max) {
    float center;
    if (max <= min) return 0.f;
    if (code == 0x02 || code == 0x05)
        return clamp((value - min) / (float)(max - min), 0.f, 1.f);
    center = ((float)min + max) * 0.5f;
    if (value >= center) return clamp((value - center) / (max - center), -1.f, 1.f);
    return clamp((value - center) / (center - min), -1.f, 1.f);
}

static int pressed(const ApakGamepadMap *map, unsigned short code) {
    int i = buttonIndex(code);
    return i >= 0 && map->buttons[i];
}

static unsigned char axisDirection(unsigned char was, float value, int positive) {
    float directed = positive ? value : -value;
    return directed >= (was ? 0.28f : 0.45f);
}

static void refreshKeys(ApakGamepadMap *map, ApakGamepadEmitKey emit, void *ctx) {
    unsigned char want[sizeof map->held] = {0};
    int keyboard = map->mode != APAK_GAMEPAD_NATIVE;
    int penguin = map->mode == APAK_GAMEPAD_PENGUIN_DASH;

    /* Hats are Android DPAD keys as well as joystick axes. This is the
     * missing Smart Pro path: its D-pad reports ABS_HAT0X/Y, not BTN_DPAD. */
    want[KEY_DPAD_LEFT] = pressed(map, 0x222) || map->axes[6] < -0.5f;
    want[KEY_DPAD_RIGHT] = pressed(map, 0x223) || map->axes[6] > 0.5f;
    want[KEY_DPAD_UP] = pressed(map, 0x220) || map->axes[7] < -0.5f;
    want[KEY_DPAD_DOWN] = pressed(map, 0x221) || map->axes[7] > 0.5f;

    if (keyboard) {
        map->stickDirs[0] = axisDirection(map->stickDirs[0], map->axes[0], 0);
        map->stickDirs[1] = axisDirection(map->stickDirs[1], map->axes[0], 1);
        map->stickDirs[2] = axisDirection(map->stickDirs[2], map->axes[1], 0);
        map->stickDirs[3] = axisDirection(map->stickDirs[3], map->axes[1], 1);
        want[KEY_DPAD_LEFT] |= map->stickDirs[0] || pressed(map, 0x136);
        want[KEY_DPAD_RIGHT] |= map->stickDirs[1] || pressed(map, 0x137);
        want[KEY_DPAD_UP] |= map->stickDirs[2] || map->axes[5] > 0.5f;
        want[KEY_DPAD_DOWN] |= map->stickDirs[3] || map->axes[2] > 0.5f;
        want[KEY_BACK] = pressed(map, 0x131) || pressed(map, 0x13a);
        want[KEY_SPACE] = pressed(map, 0x134) || pressed(map, 0x132);
        if (penguin) {
            want[KEY_BUTTON_A] = pressed(map, 0x130);
            want[KEY_T] = pressed(map, 0x133);
            want[KEY_P] = pressed(map, 0x13b);
        } else {
            want[KEY_ENTER] = pressed(map, 0x130) || pressed(map, 0x13b);
            want[KEY_TAB] = pressed(map, 0x133);
        }
    } else {
        want[KEY_BUTTON_A] = pressed(map, 0x130);
        want[KEY_BUTTON_B] = pressed(map, 0x131);
        want[KEY_BUTTON_X] = pressed(map, 0x132) || pressed(map, 0x134);
        want[KEY_BUTTON_Y] = pressed(map, 0x133);
        want[KEY_BUTTON_L1] = pressed(map, 0x136);
        want[KEY_BUTTON_R1] = pressed(map, 0x137);
        want[KEY_BUTTON_L2] = pressed(map, 0x138);
        want[KEY_BUTTON_R2] = pressed(map, 0x139);
        want[KEY_BUTTON_SELECT] = pressed(map, 0x13a);
        want[KEY_BUTTON_START] = pressed(map, 0x13b);
        want[KEY_BUTTON_THUMBL] = pressed(map, 0x13d);
        want[KEY_BUTTON_THUMBR] = pressed(map, 0x13e);
    }

    for (int key = 0; key < (int)sizeof map->held; key++) {
        if (want[key] == map->held[key]) continue;
        /* Release before press when one axis changes direction. */
        if (map->held[key]) { map->held[key] = 0; emit(ctx, key, 1); }
    }
    for (int key = 0; key < (int)sizeof map->held; key++) {
        if (!want[key] || map->held[key]) continue;
        map->held[key] = 1;
        emit(ctx, key, 0);
    }
}

void apak_gamepadFeedKey(ApakGamepadMap *map, unsigned short code, int value,
                         ApakGamepadEmitKey emit, void *ctx) {
    /* The Smart Pro's printed A/B positions follow Nintendo's layout while
     * its evdev node uses Xbox button codes. Translate those two buttons
     * before either the native Android or keyboard map sees them. */
    if (map->swapAB && (code == 0x130 || code == 0x131))
        code = code == 0x130 ? 0x131 : 0x130;
    int i = buttonIndex(code);
    if (i < 0 || value == 2) return; /* ignore unrelated and auto-repeat */
    map->buttons[i] = value != 0;
    refreshKeys(map, emit, ctx);
}

void apak_gamepadFeedAxis(ApakGamepadMap *map, unsigned short code, float value,
                          ApakGamepadEmitKey emitKey, ApakGamepadEmitAxis emitAxis,
                          void *ctx) {
    static const int androidAxis[8] = { 0, 1, 11, 12, 13, 14, 15, 16 };
    int i = axisIndex(code);
    if (i < 0) return;
    map->axes[i] = clamp(value, code == 0x02 || code == 0x05 ? 0.f : -1.f, 1.f);
    refreshKeys(map, emitKey, ctx);
    if (map->mode == APAK_GAMEPAD_NATIVE) emitAxis(ctx, androidAxis[i], map->axes[i]);
}
