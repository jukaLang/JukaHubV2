/* Test stub, NOT the real header: only the declarations chiaki-stream uses, for tests/run-host-tests.sh. */
/* Minimal SDL2 declarations for syntax checks of chiaki-stream (NOT the real header).
 * Signatures copied from SDL 2.0.x for exactly the API the helper uses. */
#ifndef STUB_SDL_H
#define STUB_SDL_H
#include <stdint.h>
#include <stddef.h>

typedef uint8_t Uint8; typedef int8_t Sint8; typedef uint16_t Uint16; typedef int16_t Sint16;
typedef uint32_t Uint32; typedef int32_t Sint32; typedef uint64_t Uint64;
typedef enum { SDL_FALSE = 0, SDL_TRUE = 1 } SDL_bool;

#define SDL_MAJOR_VERSION 2
#define SDL_MINOR_VERSION 0
#ifndef STUB_SDL_PATCH
#define STUB_SDL_PATCH 14
#endif
#define SDL_PATCHLEVEL STUB_SDL_PATCH
typedef struct SDL_version { Uint8 major, minor, patch; } SDL_version;
#define SDL_VERSIONNUM(X, Y, Z) ((X)*1000 + (Y)*100 + (Z))
#define SDL_COMPILEDVERSION SDL_VERSIONNUM(SDL_MAJOR_VERSION, SDL_MINOR_VERSION, SDL_PATCHLEVEL)
#define SDL_VERSION_ATLEAST(X, Y, Z) (SDL_COMPILEDVERSION >= SDL_VERSIONNUM(X, Y, Z))
void SDL_GetVersion(SDL_version *ver);

#define SDL_INIT_TIMER 0x00000001u
#define SDL_INIT_AUDIO 0x00000010u
#define SDL_INIT_VIDEO 0x00000020u
#define SDL_INIT_JOYSTICK 0x00000200u
#define SDL_INIT_HAPTIC 0x00001000u
#define SDL_INIT_GAMECONTROLLER 0x00002000u
int SDL_Init(Uint32 flags);
void SDL_Quit(void);
const char *SDL_GetError(void);
SDL_bool SDL_SetHint(const char *name, const char *value);
#define SDL_HINT_RENDER_SCALE_QUALITY "SDL_RENDER_SCALE_QUALITY"
#define SDL_HINT_JOYSTICK_ALLOW_BACKGROUND_EVENTS "SDL_JOYSTICK_ALLOW_BACKGROUND_EVENTS"
Uint32 SDL_GetTicks(void);
void SDL_Delay(Uint32 ms);
int SDL_ShowCursor(int toggle);
#define SDL_DISABLE 0

typedef struct SDL_Window SDL_Window;
typedef struct SDL_Renderer SDL_Renderer;
typedef struct SDL_Texture SDL_Texture;
typedef struct SDL_Rect { int x, y, w, h; } SDL_Rect;
typedef struct SDL_Color { Uint8 r, g, b, a; } SDL_Color;
#define SDL_WINDOWPOS_UNDEFINED 0x1FFF0000u
#define SDL_WINDOW_FULLSCREEN_DESKTOP 0x00001001u
#define SDL_WINDOW_SHOWN 0x00000004u
SDL_Window *SDL_CreateWindow(const char *title, int x, int y, int w, int h, Uint32 flags);
void SDL_DestroyWindow(SDL_Window *window);
#define SDL_RENDERER_SOFTWARE 0x00000001u
#define SDL_RENDERER_ACCELERATED 0x00000002u
#define SDL_RENDERER_PRESENTVSYNC 0x00000004u
typedef struct SDL_RendererInfo {
    const char *name; Uint32 flags; Uint32 num_texture_formats; Uint32 texture_formats[16];
    int max_texture_width; int max_texture_height;
} SDL_RendererInfo;
SDL_Renderer *SDL_CreateRenderer(SDL_Window *window, int index, Uint32 flags);
void SDL_DestroyRenderer(SDL_Renderer *renderer);
int SDL_GetRendererInfo(SDL_Renderer *renderer, SDL_RendererInfo *info);
int SDL_GetRendererOutputSize(SDL_Renderer *renderer, int *w, int *h);
int SDL_SetRenderDrawColor(SDL_Renderer *renderer, Uint8 r, Uint8 g, Uint8 b, Uint8 a);
typedef enum { SDL_BLENDMODE_NONE = 0, SDL_BLENDMODE_BLEND = 1 } SDL_BlendMode;
int SDL_SetRenderDrawBlendMode(SDL_Renderer *renderer, SDL_BlendMode blendMode);
int SDL_RenderClear(SDL_Renderer *renderer);
int SDL_RenderFillRect(SDL_Renderer *renderer, const SDL_Rect *rect);
int SDL_RenderFillRects(SDL_Renderer *renderer, const SDL_Rect *rects, int count);
int SDL_RenderCopy(SDL_Renderer *renderer, SDL_Texture *texture, const SDL_Rect *srcrect, const SDL_Rect *dstrect);
void SDL_RenderPresent(SDL_Renderer *renderer);
#define SDL_PIXELFORMAT_IYUV 0x56555949u
#define SDL_PIXELFORMAT_ARGB8888 0x16362004u
#define SDL_TEXTUREACCESS_STATIC 0
#define SDL_TEXTUREACCESS_STREAMING 1
SDL_Texture *SDL_CreateTexture(SDL_Renderer *renderer, Uint32 format, int access, int w, int h);
void SDL_DestroyTexture(SDL_Texture *texture);
int SDL_UpdateTexture(SDL_Texture *texture, const SDL_Rect *rect, const void *pixels, int pitch);
int SDL_UpdateYUVTexture(SDL_Texture *texture, const SDL_Rect *rect, const Uint8 *Yplane, int Ypitch,
                         const Uint8 *Uplane, int Upitch, const Uint8 *Vplane, int Vpitch);
int SDL_SetTextureBlendMode(SDL_Texture *texture, SDL_BlendMode blendMode);
int SDL_SetTextureColorMod(SDL_Texture *texture, Uint8 r, Uint8 g, Uint8 b);
int SDL_SetTextureAlphaMod(SDL_Texture *texture, Uint8 alpha);

/* audio */
typedef Uint32 SDL_AudioDeviceID;
typedef Uint16 SDL_AudioFormat;
#define AUDIO_S16SYS 0x8010
typedef void (*SDL_AudioCallback)(void *userdata, Uint8 *stream, int len);
typedef struct SDL_AudioSpec {
    int freq; SDL_AudioFormat format; Uint8 channels; Uint8 silence; Uint16 samples; Uint16 padding;
    Uint32 size; SDL_AudioCallback callback; void *userdata;
} SDL_AudioSpec;
#define SDL_zero(x) memset(&(x), 0, sizeof((x)))
SDL_AudioDeviceID SDL_OpenAudioDevice(const char *device, int iscapture, const SDL_AudioSpec *desired,
                                      SDL_AudioSpec *obtained, int allowed_changes);
void SDL_CloseAudioDevice(SDL_AudioDeviceID dev);
void SDL_PauseAudioDevice(SDL_AudioDeviceID dev, int pause_on);
Uint32 SDL_GetQueuedAudioSize(SDL_AudioDeviceID dev);
void SDL_ClearQueuedAudio(SDL_AudioDeviceID dev);
int SDL_QueueAudio(SDL_AudioDeviceID dev, const void *data, Uint32 len);

/* joystick / game controller */
typedef struct _SDL_Joystick SDL_Joystick;
typedef struct _SDL_GameController SDL_GameController;
typedef Sint32 SDL_JoystickID;
int SDL_NumJoysticks(void);
SDL_bool SDL_IsGameController(int joystick_index);
int SDL_GameControllerAddMapping(const char *mappingString);
SDL_GameController *SDL_GameControllerOpen(int joystick_index);
void SDL_GameControllerClose(SDL_GameController *gamecontroller);
const char *SDL_GameControllerName(SDL_GameController *gamecontroller);
SDL_Joystick *SDL_GameControllerGetJoystick(SDL_GameController *gamecontroller);
int SDL_GameControllerRumble(SDL_GameController *gamecontroller, Uint16 low, Uint16 high, Uint32 duration_ms);
SDL_Joystick *SDL_JoystickOpen(int device_index);
void SDL_JoystickClose(SDL_Joystick *joystick);
const char *SDL_JoystickName(SDL_Joystick *joystick);
SDL_JoystickID SDL_JoystickInstanceID(SDL_Joystick *joystick);
int SDL_JoystickNumButtons(SDL_Joystick *joystick);
int SDL_JoystickNumAxes(SDL_Joystick *joystick);
int SDL_JoystickRumble(SDL_Joystick *joystick, Uint16 low, Uint16 high, Uint32 duration_ms);
Uint16 SDL_JoystickGetVendor(SDL_Joystick *joystick);  /* 2.0.6 */
Uint16 SDL_JoystickGetProduct(SDL_Joystick *joystick); /* 2.0.6 */
int SDL_JoystickNumHats(SDL_Joystick *joystick);
typedef struct { Uint8 data[16]; } SDL_JoystickGUID;
SDL_JoystickGUID SDL_JoystickGetGUID(SDL_Joystick *joystick);
void SDL_JoystickGetGUIDString(SDL_JoystickGUID guid, char *pszGUID, int cbGUID);
#if SDL_VERSION_ATLEAST(2, 0, 12)
typedef enum {
    SDL_CONTROLLER_TYPE_UNKNOWN = 0, SDL_CONTROLLER_TYPE_XBOX360, SDL_CONTROLLER_TYPE_XBOXONE, SDL_CONTROLLER_TYPE_PS3,
    SDL_CONTROLLER_TYPE_PS4, SDL_CONTROLLER_TYPE_NINTENDO_SWITCH_PRO,
#if SDL_VERSION_ATLEAST(2, 0, 14)
    SDL_CONTROLLER_TYPE_VIRTUAL, SDL_CONTROLLER_TYPE_PS5,
#endif
} SDL_GameControllerType;
SDL_GameControllerType SDL_GameControllerGetType(SDL_GameController *gamecontroller);
#endif
#define SDL_HAT_UP 0x01
#define SDL_HAT_RIGHT 0x02
#define SDL_HAT_DOWN 0x04
#define SDL_HAT_LEFT 0x08
typedef enum {
    SDL_CONTROLLER_BUTTON_INVALID = -1,
    SDL_CONTROLLER_BUTTON_A, SDL_CONTROLLER_BUTTON_B, SDL_CONTROLLER_BUTTON_X, SDL_CONTROLLER_BUTTON_Y,
    SDL_CONTROLLER_BUTTON_BACK, SDL_CONTROLLER_BUTTON_GUIDE, SDL_CONTROLLER_BUTTON_START,
    SDL_CONTROLLER_BUTTON_LEFTSTICK, SDL_CONTROLLER_BUTTON_RIGHTSTICK,
    SDL_CONTROLLER_BUTTON_LEFTSHOULDER, SDL_CONTROLLER_BUTTON_RIGHTSHOULDER,
    SDL_CONTROLLER_BUTTON_DPAD_UP, SDL_CONTROLLER_BUTTON_DPAD_DOWN,
    SDL_CONTROLLER_BUTTON_DPAD_LEFT, SDL_CONTROLLER_BUTTON_DPAD_RIGHT,
#if SDL_VERSION_ATLEAST(2, 0, 14)
    SDL_CONTROLLER_BUTTON_MISC1, SDL_CONTROLLER_BUTTON_PADDLE1, SDL_CONTROLLER_BUTTON_PADDLE2,
    SDL_CONTROLLER_BUTTON_PADDLE3, SDL_CONTROLLER_BUTTON_PADDLE4, SDL_CONTROLLER_BUTTON_TOUCHPAD,
#endif
    SDL_CONTROLLER_BUTTON_MAX
} SDL_GameControllerButton;
typedef enum {
    SDL_CONTROLLER_AXIS_INVALID = -1,
    SDL_CONTROLLER_AXIS_LEFTX, SDL_CONTROLLER_AXIS_LEFTY, SDL_CONTROLLER_AXIS_RIGHTX, SDL_CONTROLLER_AXIS_RIGHTY,
    SDL_CONTROLLER_AXIS_TRIGGERLEFT, SDL_CONTROLLER_AXIS_TRIGGERRIGHT, SDL_CONTROLLER_AXIS_MAX
} SDL_GameControllerAxis;
#if SDL_VERSION_ATLEAST(2, 0, 14)
typedef enum { SDL_SENSOR_INVALID = -1, SDL_SENSOR_UNKNOWN, SDL_SENSOR_ACCEL, SDL_SENSOR_GYRO } SDL_SensorType;
#define SDL_STANDARD_GRAVITY 9.80665f
SDL_bool SDL_GameControllerHasButton(SDL_GameController *gamecontroller, SDL_GameControllerButton button);
int SDL_GameControllerGetNumTouchpads(SDL_GameController *gamecontroller);
SDL_bool SDL_GameControllerHasSensor(SDL_GameController *gamecontroller, SDL_SensorType type);
int SDL_GameControllerSetSensorEnabled(SDL_GameController *gamecontroller, SDL_SensorType type, SDL_bool enabled);
#endif

/* keyboard */
typedef Sint32 SDL_Keycode;
#define SDLK_ESCAPE 27
typedef struct SDL_Keysym { int scancode; SDL_Keycode sym; Uint16 mod; Uint32 unused; } SDL_Keysym;

/* events */
typedef enum {
    SDL_QUIT = 0x100,
    SDL_KEYDOWN = 0x300, SDL_KEYUP,
    SDL_JOYAXISMOTION = 0x600, SDL_JOYBALLMOTION, SDL_JOYHATMOTION, SDL_JOYBUTTONDOWN, SDL_JOYBUTTONUP,
    SDL_JOYDEVICEADDED, SDL_JOYDEVICEREMOVED,
    SDL_CONTROLLERAXISMOTION = 0x650, SDL_CONTROLLERBUTTONDOWN, SDL_CONTROLLERBUTTONUP,
    SDL_CONTROLLERDEVICEADDED, SDL_CONTROLLERDEVICEREMOVED, SDL_CONTROLLERDEVICEREMAPPED,
#if SDL_VERSION_ATLEAST(2, 0, 14)
    SDL_CONTROLLERTOUCHPADDOWN, SDL_CONTROLLERTOUCHPADMOTION, SDL_CONTROLLERTOUCHPADUP,
    SDL_CONTROLLERSENSORUPDATE,
#endif
    SDL_LASTEVENT = 0xFFFF
} SDL_EventType;
typedef struct { Uint32 type; Uint32 timestamp; Uint32 windowID; Uint8 state; Uint8 repeat; Uint8 padding2, padding3; SDL_Keysym keysym; } SDL_KeyboardEvent;
typedef struct { Uint32 type; Uint32 timestamp; SDL_JoystickID which; Uint8 axis, padding1, padding2, padding3; Sint16 value; Uint16 padding4; } SDL_JoyAxisEvent;
typedef struct { Uint32 type; Uint32 timestamp; SDL_JoystickID which; Uint8 hat; Uint8 value; Uint8 padding1, padding2; } SDL_JoyHatEvent;
typedef struct { Uint32 type; Uint32 timestamp; SDL_JoystickID which; Uint8 button; Uint8 state; Uint8 padding1, padding2; } SDL_JoyButtonEvent;
typedef struct { Uint32 type; Uint32 timestamp; Sint32 which; } SDL_JoyDeviceEvent;
typedef struct { Uint32 type; Uint32 timestamp; SDL_JoystickID which; Uint8 axis, padding1, padding2, padding3; Sint16 value; Uint16 padding4; } SDL_ControllerAxisEvent;
typedef struct { Uint32 type; Uint32 timestamp; SDL_JoystickID which; Uint8 button; Uint8 state; Uint8 padding1, padding2; } SDL_ControllerButtonEvent;
typedef struct { Uint32 type; Uint32 timestamp; Sint32 which; } SDL_ControllerDeviceEvent;
#if SDL_VERSION_ATLEAST(2, 0, 14)
typedef struct { Uint32 type; Uint32 timestamp; SDL_JoystickID which; Sint32 touchpad; Sint32 finger; float x; float y; float pressure; } SDL_ControllerTouchpadEvent;
typedef struct { Uint32 type; Uint32 timestamp; SDL_JoystickID which; Sint32 sensor; float data[3]; } SDL_ControllerSensorEvent;
#endif
typedef union SDL_Event {
    Uint32 type;
    SDL_KeyboardEvent key;
    SDL_JoyAxisEvent jaxis;
    SDL_JoyHatEvent jhat;
    SDL_JoyButtonEvent jbutton;
    SDL_JoyDeviceEvent jdevice;
    SDL_ControllerAxisEvent caxis;
    SDL_ControllerButtonEvent cbutton;
    SDL_ControllerDeviceEvent cdevice;
#if SDL_VERSION_ATLEAST(2, 0, 14)
    SDL_ControllerTouchpadEvent ctouchpad;
    SDL_ControllerSensorEvent csensor;
#endif
    Uint8 padding[56];
} SDL_Event;
int SDL_PollEvent(SDL_Event *event);
#endif
