#ifndef ANDROIDRT_GAMEPADMAP_H
#define ANDROIDRT_GAMEPADMAP_H

/* Controller translation is separate from evdev so it can be tested on a
 * host. Inputs use Linux button and axis codes; outputs use Android key and
 * motion axis codes. */

typedef enum {
    APAK_GAMEPAD_NATIVE,
    APAK_GAMEPAD_KEYBOARD,
    APAK_GAMEPAD_PENGUIN_DASH
} ApakGamepadMode;

typedef struct {
    ApakGamepadMode mode;
    unsigned char swapAB; /* TrimUI Player1 labels A on east, B on south. */
    unsigned char buttons[20];
    unsigned char held[128];
    unsigned char stickDirs[4]; /* left, right, up, down */
    float axes[8];              /* X, Y, Z, RX, RY, RZ, HAT0X, HAT0Y */
} ApakGamepadMap;

typedef void (*ApakGamepadEmitKey)(void *ctx, int keyCode, int action);
typedef void (*ApakGamepadEmitAxis)(void *ctx, int axis, float value);

ApakGamepadMode apak_gamepadMode(const char *packageName, const char *option);
const char *apak_gamepadModeName(ApakGamepadMode mode);
void apak_gamepadInit(ApakGamepadMap *map, ApakGamepadMode mode);
void apak_gamepadSetSwapAB(ApakGamepadMap *map, int enabled);
float apak_gamepadNormalize(unsigned short code, int value, int min, int max);
void apak_gamepadFeedKey(ApakGamepadMap *map, unsigned short code, int value,
                         ApakGamepadEmitKey emit, void *ctx);
void apak_gamepadFeedAxis(ApakGamepadMap *map, unsigned short code, float value,
                          ApakGamepadEmitKey emitKey, ApakGamepadEmitAxis emitAxis,
                          void *ctx);

#endif
