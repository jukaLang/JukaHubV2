/* device.h — which TrimUI handheld the runtime is on, and its panel.
 *
 * One answer for every layer that describes the device to a game: the
 * AConfiguration and system properties, the JNI layer's DisplayMetrics and
 * class packs, and the window the lifecycle issues. JukaHub passes its own
 * detection as ANDROIDPAK_DEVICE (trimui-smart-pro, trimui-brick, ...);
 * without it the device tree is read; the Smart Pro is the default.
 */
#ifndef ANDROIDRT_DEVICE_H
#define ANDROIDRT_DEVICE_H

typedef struct ApakDevice {
    const char *id;           /* JukaHub's DeviceModel string             */
    const char *manufacturer; /* "TrimUI"                                 */
    const char *model;        /* "Smart Pro"                              */
    const char *board;        /* "tg5040"                                 */
    int panelW, panelH;       /* native landscape panel                   */
    float dpi;                /* physical dots per inch                   */
} ApakDevice;

const ApakDevice *apak_device(void);

/* The window games draw into: ANDROIDRT_WINDOW, else ANDROIDPAK_PANEL, else
 * what the present layer measured (framebuffer / KMS mode), else the
 * device's panel. */
void apak_devicePanel(int *w, int *h);
/* The present layer reports the real scanout size it found. */
void apak_deviceSetMeasuredPanel(int w, int h);

/* Physical dots per inch (ANDROIDPAK_DPI overrides). */
float apak_deviceDpi(void);
/* The logical density Android would configure (DisplayMetrics.densityDpi). */
int apak_deviceDensity(void);

#endif /* ANDROIDRT_DEVICE_H */
