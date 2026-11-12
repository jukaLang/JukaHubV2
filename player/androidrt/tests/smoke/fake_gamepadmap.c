#include "gamepadmap.h"

#include <math.h>
#include <stdio.h>
#include <stdlib.h>

static int keys[64][2], keyCount;
static int axes[64];
static float axisValues[64];
static int axisCount;

static void keyOut(void *ctx, int code, int action) {
    (void)ctx;
    if (keyCount >= 64) abort();
    keys[keyCount][0] = code;
    keys[keyCount++][1] = action;
}

static void axisOut(void *ctx, int axis, float value) {
    (void)ctx;
    if (axisCount >= 64) abort();
    axes[axisCount] = axis;
    axisValues[axisCount++] = value;
}

static void expectKey(int count, int code, int action) {
    if (keyCount != count || keys[count - 1][0] != code || keys[count - 1][1] != action) {
        fprintf(stderr, "key event %d: wanted %d/%d, got count %d last %d/%d\n",
                count, code, action, keyCount,
                keyCount ? keys[keyCount - 1][0] : -1,
                keyCount ? keys[keyCount - 1][1] : -1);
        exit(1);
    }
}

int main(void) {
    ApakGamepadMap map;
    if (apak_gamepadMode("com.anathemasit.penguindash", NULL) != APAK_GAMEPAD_PENGUIN_DASH ||
        apak_gamepadMode("other.game", NULL) != APAK_GAMEPAD_NATIVE ||
        apak_gamepadMode("com.anathemasit.penguindash", "native") != APAK_GAMEPAD_NATIVE ||
        apak_gamepadMode("other.game", "keyboard") != APAK_GAMEPAD_KEYBOARD) return 1;
    if (fabsf(apak_gamepadNormalize(2, 255, 0, 255) - 1.f) > 0.001f ||
        fabsf(apak_gamepadNormalize(5, 128, 0, 255) - 0.502f) > 0.003f ||
        apak_gamepadNormalize(2, -32768, -32768, 32767) != 0.f ||
        apak_gamepadNormalize(5, 32767, -32768, 32767) != 1.f ||
        apak_gamepadNormalize(0, -32767, -32767, 32767) != -1.f) return 2;

    apak_gamepadInit(&map, APAK_GAMEPAD_NATIVE);
    apak_gamepadFeedAxis(&map, 0x10, -1.f, keyOut, axisOut, NULL);
    expectKey(1, 21, 0); /* Smart Pro hat left -> Android DPAD_LEFT */
    if (axisCount != 1 || axes[0] != 15 || axisValues[0] != -1.f) return 3;
    apak_gamepadFeedKey(&map, 0x222, 1, keyOut, NULL);
    if (keyCount != 1) return 4; /* hard D-pad and hat share one logical key */
    apak_gamepadFeedAxis(&map, 0x10, 0.f, keyOut, axisOut, NULL);
    if (keyCount != 1) return 5; /* still held by the hard D-pad */
    apak_gamepadFeedKey(&map, 0x222, 0, keyOut, NULL);
    expectKey(2, 21, 1);
    apak_gamepadFeedKey(&map, 0x130, 1, keyOut, NULL);
    expectKey(3, 96, 0);

    keyCount = axisCount = 0;
    apak_gamepadInit(&map, APAK_GAMEPAD_PENGUIN_DASH);
    apak_gamepadFeedAxis(&map, 0, -0.7f, keyOut, axisOut, NULL);
    expectKey(1, 21, 0); /* left stick steers via the game's arrow-key path */
    if (axisCount != 0) return 6; /* Penguin treats joystick motion as touch */
    apak_gamepadFeedAxis(&map, 0, -0.32f, keyOut, axisOut, NULL);
    if (keyCount != 1) return 7; /* stick hysteresis */
    apak_gamepadFeedAxis(&map, 0, 0.f, keyOut, axisOut, NULL);
    expectKey(2, 21, 1);
    apak_gamepadFeedKey(&map, 0x130, 1, keyOut, NULL);
    expectKey(3, 96, 0); /* A selects */
    apak_gamepadFeedKey(&map, 0x134, 1, keyOut, NULL);
    expectKey(4, 62, 0); /* X jumps */
    apak_gamepadFeedKey(&map, 0x131, 1, keyOut, NULL);
    expectKey(5, 4, 0); /* B backs out */
    apak_gamepadFeedKey(&map, 0x13b, 1, keyOut, NULL);
    expectKey(6, 44, 0); /* Start pauses */
    apak_gamepadFeedAxis(&map, 0x02, 1.f, keyOut, axisOut, NULL);
    expectKey(7, 20, 0); /* left trigger brakes */
    apak_gamepadFeedAxis(&map, 0x05, 1.f, keyOut, axisOut, NULL);
    expectKey(8, 19, 0); /* right trigger paddles */

    keyCount = axisCount = 0;
    apak_gamepadInit(&map, APAK_GAMEPAD_KEYBOARD);
    apak_gamepadFeedKey(&map, 0x130, 1, keyOut, NULL);
    expectKey(1, 66, 0); /* generic keyboard-mode A -> Enter */
    apak_gamepadFeedKey(&map, 0x133, 1, keyOut, NULL);
    expectKey(2, 61, 0); /* Y -> Tab */
    puts("PASS: controller maps, hat keys, stick keys and calibrated triggers");
    return 0;
}
