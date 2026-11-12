/* stream_sys.c - session file, status file, battery and motor (stream_sys.h). */
#define _POSIX_C_SOURCE 200809L
#include "stream_sys.h"

#include <ctype.h>
#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <math.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

static void set_error(char *error, size_t size, const char *text)
{
    if(error && size) snprintf(error, size, "%s", text);
}

/* memset through a volatile pointer: the compiler cannot prove the call has no
 * effect, so a wipe of a buffer that is never read again stays in. */
static void *(*const volatile wipe_memset)(void *, int, size_t) = memset;

void ss_wipe(void *p, size_t n)
{
    if(p && n) wipe_memset(p, 0, n);
}

static void trim(char *text)
{
    char *start = text;
    while(*start == ' ' || *start == '\t' || *start == '\r' || *start == '\n') start++;
    if(start != text) memmove(text, start, strlen(start) + 1);
    size_t length = strlen(text);
    while(length && (text[length - 1] == ' ' || text[length - 1] == '\t' ||
                     text[length - 1] == '\r' || text[length - 1] == '\n'))
        text[--length] = '\0';
}

static bool parse_uint(const char *text, unsigned int minimum, unsigned int maximum, unsigned int *value)
{
    char *end = NULL;
    errno = 0;
    if(!*text || *text == '-' || *text == '+') return false;
    unsigned long parsed = strtoul(text, &end, 10);
    if(errno || !end || *end || parsed < minimum || parsed > maximum) return false;
    *value = (unsigned int)parsed;
    return true;
}

static bool parse_long(const char *text, long minimum, long maximum, long *value)
{
    char *end = NULL;
    errno = 0;
    if(!*text) return false;
    long parsed = strtol(text, &end, 10);
    if(errno || !end || *end || parsed < minimum || parsed > maximum) return false;
    *value = parsed;
    return true;
}

static bool parse_bool(const char *text, bool *value)
{
    if(!strcmp(text, "1") || !strcasecmp(text, "true") || !strcasecmp(text, "on") || !strcasecmp(text, "yes")) {
        *value = true;
        return true;
    }
    if(!strcmp(text, "0") || !strcasecmp(text, "false") || !strcasecmp(text, "off") || !strcasecmp(text, "no")) {
        *value = false;
        return true;
    }
    return false;
}

static int base64_value(int c)
{
    if(c >= 'A' && c <= 'Z') return c - 'A';
    if(c >= 'a' && c <= 'z') return c - 'a' + 26;
    if(c >= '0' && c <= '9') return c - '0' + 52;
    if(c == '+') return 62;
    if(c == '/') return 63;
    return -1;
}

bool ss_base64_decode(const char *text, uint8_t *out, size_t *out_size)
{
    size_t length = strlen(text);
    size_t capacity = *out_size;
    size_t written = 0;
    if(length == 0 || length % 4) return false;
    for(size_t i = 0; i < length; i += 4) {
        int v[4];
        int pad = 0;
        for(int j = 0; j < 4; j++) {
            int c = (unsigned char)text[i + (size_t)j];
            if(c == '=') {
                /* Padding only in the last group, only at its end. */
                if(i + 4 != length || j < 2) return false;
                v[j] = 0;
                pad++;
            } else {
                if(pad) return false;
                v[j] = base64_value(c);
                if(v[j] < 0) return false;
            }
        }
        uint32_t triple = ((uint32_t)v[0] << 18) | ((uint32_t)v[1] << 12) | ((uint32_t)v[2] << 6) | (uint32_t)v[3];
        size_t bytes = 3 - (size_t)pad;
        if(written + bytes > capacity) return false;
        out[written++] = (uint8_t)(triple >> 16);
        if(bytes > 1) out[written++] = (uint8_t)(triple >> 8);
        if(bytes > 2) out[written++] = (uint8_t)triple;
    }
    *out_size = written;
    return true;
}

void ss_ascii_name(const char *in, char *out, size_t out_size)
{
    size_t n = 0;
    if(!out_size) return;
    for(const unsigned char *p = (const unsigned char *)in; *p && n + 1 < out_size; p++) {
        if(*p >= 0x80 && *p < 0xC0) continue; /* UTF-8 continuation byte */
        if(*p >= 0xC0) out[n++] = '?';
        else if(*p >= 0x20 && *p < 0x7F) out[n++] = (char)*p;
        else if(*p == '\t') out[n++] = ' ';
    }
    out[n] = '\0';
    trim(out);
}

static bool absolute_path(const char *value, size_t limit)
{
    return value[0] == '/' && strlen(value) < limit && !strstr(value, "/../");
}

/* The whole session file through read(): no stdio buffer is left holding the
 * keys after it is closed. Returns -1 (errno set) when it cannot be opened, 0
 * for a file that is bigger than the buffer, 1 when *length bytes were read. */
static int read_session(const char *path, char *buffer, size_t capacity, size_t *length)
{
    int descriptor = open(path, O_RDONLY | O_CLOEXEC);
    if(descriptor < 0) return -1;
    size_t total = 0;
    int result = 1;
    for(;;) {
        if(total == capacity) {
            char extra;
            ssize_t more = read(descriptor, &extra, 1);
            if(more > 0) result = 0;
            break;
        }
        ssize_t got = read(descriptor, buffer + total, capacity - total);
        if(got < 0 && errno == EINTR) continue;
        if(got <= 0) break; /* end of file, or an error: what was read is all there is */
        total += (size_t)got;
    }
    close(descriptor);
    *length = total;
    return result;
}

bool ss_load_config(const char *path, SsConfig *config, char *error, size_t error_size)
{
    memset(config, 0, sizeof(*config));
    config->width = 1280;
    config->height = 720;
    config->fps = 30;
    config->bitrate = 8000;
    config->volume = 80;
    config->target = 1000;
    config->rumble = true;
    config->hints = true;
    snprintf(config->name, sizeof(config->name), "PlayStation");

    static char buffer[SS_SESSION_MAX];
    size_t length = 0;
    int read_result = read_session(path, buffer, sizeof(buffer), &length);
    if(read_result < 0) {
        char text[160];
        snprintf(text, sizeof(text), "cannot open session file: %s", strerror(errno));
        set_error(error, error_size, text);
        return false;
    }
    unlink(path); /* the keys never stay on disk, whatever the file held */
    if(read_result == 0) {
        ss_wipe(buffer, sizeof(buffer));
        set_error(error, error_size, "invalid or incomplete session file");
        return false;
    }
    char line[SS_LINE_MAX + 1];
    char rp_key[128] = {0};
    char codec[16] = {0};
    char name[96] = {0};
    size_t start = 0;
    while(start < length) {
        const char *newline = memchr(buffer + start, '\n', length - start);
        size_t end = newline ? (size_t)(newline - buffer) : length;
        size_t line_length = end - start;
        size_t next = newline ? end + 1 : length;
        if(line_length > SS_LINE_MAX) {
            start = next; /* too long for any key: skipped whole */
            continue;
        }
        memcpy(line, buffer + start, line_length);
        line[line_length] = '\0';
        start = next;
        char *separator = strchr(line, '=');
        if(!separator) continue;
        *separator = '\0';
        char *value = separator + 1;
        trim(line);
        trim(value);
        bool flag = false;
        if(!strcmp(line, "host")) snprintf(config->host, sizeof(config->host), "%s", value);
        else if(!strcmp(line, "regist_key")) snprintf(config->regist_key, sizeof(config->regist_key), "%s", value);
        else if(!strcmp(line, "rp_key")) snprintf(rp_key, sizeof(rp_key), "%s", value);
        else if(!strcmp(line, "ps5")) config->ps5 = !strcmp(value, "1") || !strcasecmp(value, "true");
        else if(!strcmp(line, "verbose")) config->verbose = !strcmp(value, "1") || !strcasecmp(value, "true");
        else if(!strcmp(line, "target")) parse_uint(value, 0, 1000100, &config->target);
        else if(!strcmp(line, "width")) parse_uint(value, 320, 3840, &config->width);
        else if(!strcmp(line, "height")) parse_uint(value, 180, 2160, &config->height);
        else if(!strcmp(line, "fps")) parse_uint(value, 1, 120, &config->fps);
        else if(!strcmp(line, "bitrate")) parse_uint(value, 1000, 50000, &config->bitrate);
        else if(!strcmp(line, "volume")) parse_uint(value, 0, 100, &config->volume);
        else if(!strcmp(line, "name")) snprintf(name, sizeof(name), "%s", value);
        else if(!strcmp(line, "codec")) snprintf(codec, sizeof(codec), "%s", value);
        else if(!strcmp(line, "login_pin")) {
            if(strlen(value) == 4 && isdigit((unsigned char)value[0]) && isdigit((unsigned char)value[1]) &&
               isdigit((unsigned char)value[2]) && isdigit((unsigned char)value[3]))
                memcpy(config->login_pin, value, 5);
        } else if(!strcmp(line, "select_button")) {
            if(!strcasecmp(value, "create") || !strcasecmp(value, "share")) config->select_create = 1;
            else if(!strcasecmp(value, "touchpad")) config->select_create = 0;
        } else if(!strcmp(line, "dpad_stick")) {
            if(parse_bool(value, &flag)) config->dpad_stick = flag;
        } else if(!strcmp(line, "rumble")) {
            if(parse_bool(value, &flag)) config->rumble = flag;
        } else if(!strcmp(line, "hints")) {
            if(parse_bool(value, &flag)) config->hints = flag;
        } else if(!strcmp(line, "remote")) {
            if(parse_bool(value, &flag)) config->remote = flag;
        } else if(!strcmp(line, "motor_gpio")) {
            if(absolute_path(value, sizeof(config->motor_gpio)))
                snprintf(config->motor_gpio, sizeof(config->motor_gpio), "%s", value);
        } else if(!strcmp(line, "motor_level")) {
            if(absolute_path(value, sizeof(config->motor_level)))
                snprintf(config->motor_level, sizeof(config->motor_level), "%s", value);
        } else if(!strcmp(line, "motor_level_min")) {
            if(parse_long(value, 0, 100000000L, &config->motor_level_min)) config->motor_range_set = true;
        } else if(!strcmp(line, "motor_level_max")) {
            if(parse_long(value, 1, 100000000L, &config->motor_level_max)) config->motor_range_set = true;
        } else if(!strcmp(line, "status_file")) {
            if(absolute_path(value, sizeof(config->status_file)))
                snprintf(config->status_file, sizeof(config->status_file), "%s", value);
        }
    }
    ss_wipe(line, sizeof(line));
    ss_wipe(buffer, sizeof(buffer));

    if(name[0]) {
        char ascii[96];
        ss_ascii_name(name, ascii, sizeof(ascii));
        if(ascii[0]) snprintf(config->name, sizeof(config->name), "%s", ascii);
    }
    config->codec = config->ps5 ? SS_CODEC_H265 : SS_CODEC_H264;
    if(!strcasecmp(codec, "h264") || !strcasecmp(codec, "avc")) config->codec = SS_CODEC_H264;
    else if(!strcasecmp(codec, "h265") || !strcasecmp(codec, "hevc")) config->codec = SS_CODEC_H265;
    if(!config->ps5) config->codec = SS_CODEC_H264; /* a PS4 streams H.264 only */
    if(config->motor_range_set && config->motor_level_max <= config->motor_level_min)
        config->motor_range_set = false;

    size_t morning_size = sizeof(config->morning);
    bool valid = config->host[0] && config->regist_key[0] && strlen(config->regist_key) <= 8 &&
                 ss_base64_decode(rp_key, config->morning, &morning_size) && morning_size == 16;
    ss_wipe(rp_key, sizeof(rp_key));
    if(!valid) {
        ss_wipe(config->regist_key, sizeof(config->regist_key));
        ss_wipe(config->morning, sizeof(config->morning));
        ss_wipe(config->login_pin, sizeof(config->login_pin));
        set_error(error, error_size, "invalid or incomplete session file");
        return false;
    }
    return true;
}

static void put_line(FILE *file, const char *key, const char *value)
{
    fprintf(file, "%s=", key);
    for(const char *p = value ? value : ""; *p; p++) {
        unsigned char c = (unsigned char)*p;
        fputc(c == '\n' || c == '\r' || c == '\t' ? ' ' : c, file);
    }
    fputc('\n', file);
}

bool ss_write_status(const char *path, const SsStatus *status)
{
    if(!path || !path[0]) return false;
    char tmp[600];
    int n = snprintf(tmp, sizeof(tmp), "%s.tmp-%ld", path, (long)getpid());
    if(n < 0 || (size_t)n >= sizeof(tmp)) return false;
    /* A fresh file of our own: a leftover temp name (an earlier run with the same
     * pid) is removed first, and a link planted there is neither followed nor
     * written through. */
    unlink(tmp);
    int descriptor = open(tmp, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0600);
    if(descriptor < 0) return false;
    /* open() already made it 0600; a card filesystem that refuses chmod (vfat
     * can) must not cost JukaHub the report. */
    (void)fchmod(descriptor, 0600);
    FILE *file = fdopen(descriptor, "w");
    if(!file) {
        close(descriptor);
        unlink(tmp);
        return false;
    }
    char number[32];
    put_line(file, "version", "1");
    snprintf(number, sizeof(number), "%d", status->rc);
    put_line(file, "rc", number);
    put_line(file, "result", status->result ? status->result : "init_failed");
    put_line(file, "message", status->message);
    put_line(file, "connected", status->connected ? "1" : "0");
    snprintf(number, sizeof(number), "%lu", status->seconds);
    put_line(file, "seconds", number);
    snprintf(number, sizeof(number), "%llu", status->frames);
    put_line(file, "frames", number);
    put_line(file, "quit_reason", status->quit_reason);
    put_line(file, "host", status->host);
    put_line(file, "codec", status->codec);
    put_line(file, "saved_pin_rejected", status->saved_pin_rejected ? "1" : "0");
    bool ok = fflush(file) == 0 && fsync(descriptor) == 0;
    if(fclose(file) != 0) ok = false;
    if(!ok || rename(tmp, path) != 0) {
        unlink(tmp);
        return false;
    }
    return true;
}

static void join_path(char *out, size_t size, const char *root, const char *rest)
{
    const char *base = root && root[0] ? root : "/";
    size_t length = strlen(base);
    bool slash = length && base[length - 1] == '/';
    snprintf(out, size, "%s%s%s", base, slash ? "" : "/", rest[0] == '/' ? rest + 1 : rest);
}

static bool read_text(const char *path, char *out, size_t size)
{
    FILE *file = fopen(path, "r");
    if(!file) return false;
    size_t n = fread(out, 1, size - 1, file);
    fclose(file);
    out[n] = '\0';
    trim(out);
    return true;
}

static bool read_long(const char *path, long *value)
{
    char text[64];
    if(!read_text(path, text, sizeof(text))) return false;
    char *end = NULL;
    errno = 0;
    long parsed = strtol(text, &end, 10);
    if(errno || end == text) return false;
    *value = parsed;
    return true;
}

static bool is_dir(const char *path)
{
    struct stat st;
    return stat(path, &st) == 0 && S_ISDIR(st.st_mode); /* follows the class symlinks */
}

static bool exists(const char *path)
{
    struct stat st;
    return stat(path, &st) == 0;
}

static void lower(char *text)
{
    for(; *text; text++) *text = (char)tolower((unsigned char)*text);
}

static bool name_word(const char *name, const char *word)
{
    size_t length = strlen(word);
    for(const char *p = strstr(name, word); p; p = strstr(p + 1, word)) {
        bool left = p == name || !isalpha((unsigned char)p[-1]);
        bool right = !isalpha((unsigned char)p[length]);
        if(left && right) return true;
    }
    return false;
}

static bool charger_entry(const char *dir, const char *name, bool *online)
{
    char path[640];
    char type[32] = {0};
    long value = 0;
    join_path(path, sizeof(path), dir, "type");
    if(read_text(path, type, sizeof(type))) lower(type);
    if(!strcmp(type, "battery")) return false;
    join_path(path, sizeof(path), dir, "online");
    if(!read_long(path, &value)) return false;
    char lowered[256];
    snprintf(lowered, sizeof(lowered), "%s", name);
    lower(lowered);
    bool kind = !strcmp(type, "mains") || !strcmp(type, "usb") || !strcmp(type, "wireless") ||
                !strncmp(type, "usb_", 4) || strstr(lowered, "charger") || strstr(lowered, "adapter") ||
                strstr(lowered, "mains") || name_word(lowered, "ac") || name_word(lowered, "usb") ||
                name_word(lowered, "dc") || name_word(lowered, "vbus") || name_word(lowered, "pd");
    if(!kind) return false;
    *online = value > 0;
    return true;
}

bool ss_battery_read(const char *root, SsBattery *battery)
{
    memset(battery, 0, sizeof(*battery));
    battery->percent = -1;
    char base[512];
    join_path(base, sizeof(base), root, "sys/class/power_supply");
    DIR *dir = opendir(base);
    if(!dir) return false;
    char named[640] = {0};
    char any[640] = {0};
    int chargers = 0;
    bool charger_online = false;
    struct dirent *entry;
    while((entry = readdir(dir))) {
        if(entry->d_name[0] == '.') continue;
        char path[640];
        char capacity[700];
        join_path(path, sizeof(path), base, entry->d_name);
        if(!is_dir(path)) continue;
        bool online = false;
        if(charger_entry(path, entry->d_name, &online)) {
            chargers++;
            if(online) charger_online = true;
            continue;
        }
        join_path(capacity, sizeof(capacity), path, "capacity");
        if(!exists(capacity)) continue;
        char lowered[256];
        snprintf(lowered, sizeof(lowered), "%s", entry->d_name);
        lower(lowered);
        if(strstr(lowered, "bat") && !named[0]) snprintf(named, sizeof(named), "%s", path);
        else if(!any[0]) snprintf(any, sizeof(any), "%s", path);
    }
    closedir(dir);
    const char *chosen = named[0] ? named : any;
    if(!chosen[0]) return false;
    char path[700];
    long percent = -1;
    join_path(path, sizeof(path), chosen, "capacity");
    if(!read_long(path, &percent)) return false;
    if(percent < 0) percent = 0;
    if(percent > 100) percent = 100;
    char status[32] = {0};
    join_path(path, sizeof(path), chosen, "status");
    if(read_text(path, status, sizeof(status))) lower(status);
    battery->valid = true;
    battery->percent = (int)percent;
    if(!strcmp(status, "charging")) battery->on_power = true;
    else if(chargers > 0) battery->on_power = charger_online;
    else battery->on_power = !strcmp(status, "full") || !strcmp(status, "not charging");
    return true;
}

/* ---- the built-in pad ---- */

static bool contains_ci(const char *text, const char *word)
{
    size_t length = strlen(word);
    for(const char *at = text; *at; at++)
        if(!strncasecmp(at, word, length)) return true;
    return false;
}

/* "event" and a number: the only names read under sys/class/input. */
static bool event_node_name(const char *name)
{
    if(strncmp(name, "event", 5) != 0) return false;
    size_t digits = strspn(name + 5, "0123456789");
    return digits > 0 && digits < 6 && name[5 + digits] == '\0';
}

static bool input_attr(const char *root, const char *event, const char *attr, char *out, size_t size)
{
    char rest[160];
    char path[700];
    /* event is "eventN" (event_node_name) and attr a short literal: the bound
     * only tells -Wformat-truncation what the callers already guarantee. */
    snprintf(rest, sizeof(rest), "sys/class/input/%.32s/device/%.32s", event, attr);
    join_path(path, sizeof(path), root, rest);
    return read_text(path, out, size);
}

static bool input_id(const char *root, const char *event, const char *attr, unsigned *value)
{
    char text[16];
    if(!input_attr(root, event, attr, text, sizeof(text))) return false;
    char *end = NULL;
    errno = 0;
    unsigned long parsed = strtoul(text, &end, 16);
    if(errno || end == text || *end || parsed > 0xffffu) return false;
    *value = (unsigned)parsed;
    return true;
}

bool ss_builtin_pad(const char *root, const char *name, unsigned vendor, unsigned product, const char *devnode)
{
    if(name && contains_ci(name, "trimui")) return true;
    if(vendor != SS_BUILTIN_PAD_VENDOR || product != SS_BUILTIN_PAD_PRODUCT) return false;
    char kernel_name[128];
    if(devnode && devnode[0]) {
        const char *event = strrchr(devnode, '/');
        event = event ? event + 1 : devnode;
        if(event_node_name(event) && input_attr(root, event, "name", kernel_name, sizeof(kernel_name)))
            return contains_ci(kernel_name, "trimui");
    }
    char base[512];
    join_path(base, sizeof(base), root, "sys/class/input");
    DIR *dir = opendir(base);
    if(!dir) return false;
    bool found = false;
    struct dirent *entry;
    while(!found && (entry = readdir(dir))) {
        unsigned v = 0, p = 0;
        if(!event_node_name(entry->d_name)) continue;
        if(!input_attr(root, entry->d_name, "name", kernel_name, sizeof(kernel_name)) || !contains_ci(kernel_name, "trimui"))
            continue;
        found = input_id(root, entry->d_name, "id/vendor", &v) && input_id(root, entry->d_name, "id/product", &p) &&
                v == SS_BUILTIN_PAD_VENDOR && p == SS_BUILTIN_PAD_PRODUCT;
    }
    closedir(dir);
    return found;
}

/* ---- PlayStation pads without an SDL mapping ---- */

int ss_sony_model(unsigned vendor, unsigned product)
{
    if(vendor != SS_SONY_VENDOR) return 0;
    switch(product) {
        case 0x05c4u: /* DualShock 4 (CUH-ZCT1) */
        case 0x09ccu: /* DualShock 4 (CUH-ZCT2) */
        case 0x0ba0u: /* the DualShock 4 USB wireless adaptor */
            return 4;
        case 0x0ce6u: /* DualSense */
        case 0x0df2u: /* DualSense Edge */
            return 5;
        default:
            return 0;
    }
}

SsSonyLayout ss_sony_layout(int buttons, int axes, int hats)
{
    /* Both layouts have the D-pad as a hat and six stick / trigger axes; a
     * motion sensor or touchpad node has neither. */
    if(hats < 1 || axes < 6) return SS_SONY_NONE;
    if(buttons >= 14) return SS_SONY_HID_GENERIC;
    if(buttons == 13) return SS_SONY_EVDEV_GAMEPAD;
    return SS_SONY_NONE;
}

const char *ss_sony_layout_name(SsSonyLayout layout)
{
    switch(layout) {
        case SS_SONY_HID_GENERIC: return "HID report order";
        case SS_SONY_EVDEV_GAMEPAD: return "evdev gamepad order";
        default: return "unknown";
    }
}

bool ss_sony_mapping(char *out, size_t size, const char *guid, int model, SsSonyLayout layout, bool touchpad)
{
    if(!out || !size) return false;
    out[0] = '\0';
    if(!guid || strlen(guid) != 32 || (model != 4 && model != 5)) return false;
    for(int i = 0; i < 32; i++)
        if(!isxdigit((unsigned char)guid[i])) return false;
    const char *buttons;
    switch(layout) {
        case SS_SONY_HID_GENERIC:
            buttons = touchpad
                ? "a:b1,b:b2,x:b0,y:b3,leftshoulder:b4,rightshoulder:b5,lefttrigger:a3,righttrigger:a4,"
                  "back:b8,start:b9,leftstick:b10,rightstick:b11,guide:b12,touchpad:b13,"
                  "leftx:a0,lefty:a1,rightx:a2,righty:a5,"
                : "a:b1,b:b2,x:b0,y:b3,leftshoulder:b4,rightshoulder:b5,lefttrigger:a3,righttrigger:a4,"
                  "back:b8,start:b9,leftstick:b10,rightstick:b11,guide:b12,"
                  "leftx:a0,lefty:a1,rightx:a2,righty:a5,";
            break;
        case SS_SONY_EVDEV_GAMEPAD:
            buttons = "a:b0,b:b1,x:b3,y:b2,leftshoulder:b4,rightshoulder:b5,lefttrigger:a2,righttrigger:a5,"
                      "back:b8,start:b9,guide:b10,leftstick:b11,rightstick:b12,"
                      "leftx:a0,lefty:a1,rightx:a3,righty:a4,";
            break;
        default:
            return false;
    }
    int n = snprintf(out, size, "%s,%s,%sdpup:h0.1,dpright:h0.2,dpdown:h0.4,dpleft:h0.8,", guid,
                     model == 5 ? "PS5 Controller" : "PS4 Controller", buttons);
    if(n < 0 || (size_t)n >= size) {
        out[0] = '\0';
        return false;
    }
    return true;
}

/* ---- motion ---- */

void ss_motion_reset(SsMotion *motion)
{
    memset(motion, 0, sizeof(*motion));
    motion->accel[1] = 1.0f;
    motion->orient[3] = 1.0f;
    motion->q[0] = 1.0f;
    motion->dirty = true;
}

static bool finite3(float x, float y, float z)
{
    return isfinite(x) && isfinite(y) && isfinite(z);
}

void ss_motion_accel(SsMotion *motion, float x, float y, float z)
{
    if(!finite3(x, y, z)) return;
    motion->accel[0] = x;
    motion->accel[1] = y;
    motion->accel[2] = z;
    motion->have_accel = true;
    motion->dirty = true;
}

/* The filter runs with gravity on +z (Madgwick's frame); SDL's sensors have it
 * on +y. (x, y, z) -> (x, -z, y) is a proper rotation taking one to the other,
 * and the same map moves the quaternion's vector part back and forth. */
void ss_motion_gyro(SsMotion *motion, float x, float y, float z, float dt)
{
    if(!finite3(x, y, z)) return;
    motion->gyro[0] = x;
    motion->gyro[1] = y;
    motion->gyro[2] = z;
    motion->dirty = true;
    if(!(dt > 0.0f)) return;
    if(dt > SS_MOTION_MAX_DT) dt = SS_MOTION_MAX_DT;
    float gx = x, gy = -z, gz = y;
    float ax = motion->accel[0], ay = -motion->accel[2], az = motion->accel[1];
    float q0 = motion->q[0], q1 = motion->q[1], q2 = motion->q[2], q3 = motion->q[3];
    float qd0 = 0.5f * (-q1 * gx - q2 * gy - q3 * gz);
    float qd1 = 0.5f * (q0 * gx + q2 * gz - q3 * gy);
    float qd2 = 0.5f * (q0 * gy - q1 * gz + q3 * gx);
    float qd3 = 0.5f * (q0 * gz + q1 * gy - q2 * gx);
    float a_norm = sqrtf(ax * ax + ay * ay + az * az);
    /* The accelerometer corrects the tilt only while it reads about 1 g: a pad
     * being swung measures its own push as well. */
    if(motion->have_accel && a_norm > 0.5f && a_norm < 1.5f) {
        ax /= a_norm;
        ay /= a_norm;
        az /= a_norm;
        float _2q0 = 2.0f * q0, _2q1 = 2.0f * q1, _2q2 = 2.0f * q2, _2q3 = 2.0f * q3;
        float _4q0 = 4.0f * q0, _4q1 = 4.0f * q1, _4q2 = 4.0f * q2;
        float _8q1 = 8.0f * q1, _8q2 = 8.0f * q2;
        float q0q0 = q0 * q0, q1q1 = q1 * q1, q2q2 = q2 * q2, q3q3 = q3 * q3;
        float s0 = _4q0 * q2q2 + _2q2 * ax + _4q0 * q1q1 - _2q1 * ay;
        float s1 = _4q1 * q3q3 - _2q3 * ax + 4.0f * q0q0 * q1 - _2q0 * ay - _4q1 + _8q1 * q1q1 + _8q1 * q2q2 + _4q1 * az;
        float s2 = 4.0f * q0q0 * q2 + _2q0 * ax + _4q2 * q3q3 - _2q3 * ay - _4q2 + _8q2 * q1q1 + _8q2 * q2q2 + _4q2 * az;
        float s3 = 4.0f * q1q1 * q3 - _2q1 * ax + 4.0f * q2q2 * q3 - _2q2 * ay;
        float s_norm = sqrtf(s0 * s0 + s1 * s1 + s2 * s2 + s3 * s3);
        if(s_norm > 1e-9f) {
            float beta = motion->settled < 0.5f ? 2.5f : 0.1f;
            qd0 -= beta * s0 / s_norm;
            qd1 -= beta * s1 / s_norm;
            qd2 -= beta * s2 / s_norm;
            qd3 -= beta * s3 / s_norm;
        }
    }
    q0 += qd0 * dt;
    q1 += qd1 * dt;
    q2 += qd2 * dt;
    q3 += qd3 * dt;
    float q_norm = sqrtf(q0 * q0 + q1 * q1 + q2 * q2 + q3 * q3);
    if(!isfinite(q_norm) || q_norm < 1e-6f) {
        q0 = 1.0f;
        q1 = q2 = q3 = 0.0f;
        q_norm = 1.0f;
    }
    motion->q[0] = q0 / q_norm;
    motion->q[1] = q1 / q_norm;
    motion->q[2] = q2 / q_norm;
    motion->q[3] = q3 / q_norm;
    motion->settled += dt;
    /* Back to SDL's frame: (x', y', z') -> (x', z', -y'). */
    motion->orient[0] = motion->q[1];
    motion->orient[1] = motion->q[3];
    motion->orient[2] = -motion->q[2];
    motion->orient[3] = motion->q[0];
}

bool ss_motion_take(SsMotion *motion)
{
    bool dirty = motion->dirty;
    motion->dirty = false;
    return dirty;
}

/* Like `echo text > path`: sysfs attributes take O_TRUNC, and the node is never
 * created when it does not exist. */
static bool write_text(const char *path, const char *text)
{
    int descriptor = open(path, O_WRONLY | O_TRUNC | O_CLOEXEC);
    if(descriptor < 0) return false;
    size_t length = strlen(text);
    bool ok = write(descriptor, text, length) == (ssize_t)length;
    if(close(descriptor) != 0) ok = false;
    return ok;
}

static void sleep_ms(long ms)
{
    struct timespec ts = {ms / 1000, (ms % 1000) * 1000000L};
    nanosleep(&ts, NULL);
}

void ss_motor_open(SsMotor *motor, const char *root, const char *gpio, const char *level,
                   long level_min, long level_max, bool range_set)
{
    memset(motor, 0, sizeof(*motor));
    motor->last_level = -1;
    if(gpio && gpio[0]) {
        snprintf(motor->gpio, sizeof(motor->gpio), "%s", gpio);
        if(level && level[0]) snprintf(motor->level, sizeof(motor->level), "%s", level);
        motor->level_min = range_set ? level_min : 0;
        motor->level_max = range_set ? level_max : 0;
        if(!range_set) motor->level[0] = '\0'; /* no range: never guess a voltage */
        motor->available = exists(motor->gpio);
        if(motor->available) write_text(motor->gpio, "0"); /* a run that died mid-rumble */
        return;
    }
    static const struct {
        const char *gpio, *level, *pin;
        long min, max;
    } layouts[] = {
        {"sys/class/gpio/gpio227/value", "sys/class/motor/voltage", "227", 500000L, 3300000L},
        {"sys/class/gpio/gpio236/value", "sys/class/motor/level", "236", 0L, 65535L},
    };
    for(size_t i = 0; i < sizeof(layouts) / sizeof(layouts[0]); i++) {
        char gpio_path[512], level_path[512], export_path[512];
        join_path(level_path, sizeof(level_path), root, layouts[i].level);
        if(!exists(level_path)) continue;
        join_path(gpio_path, sizeof(gpio_path), root, layouts[i].gpio);
        if(!exists(gpio_path)) {
            join_path(export_path, sizeof(export_path), root, "sys/class/gpio/export");
            if(!write_text(export_path, layouts[i].pin)) continue;
            for(int wait = 0; wait < 15 && !exists(gpio_path); wait++) sleep_ms(10);
            if(!exists(gpio_path)) continue;
        }
        snprintf(motor->gpio, sizeof(motor->gpio), "%s", gpio_path);
        snprintf(motor->level, sizeof(motor->level), "%s", level_path);
        motor->level_min = range_set ? level_min : layouts[i].min;
        motor->level_max = range_set ? level_max : layouts[i].max;
        motor->available = true;
        write_text(motor->gpio, "0"); /* a run that died mid-rumble */
        return;
    }
}

/* The crash guard's copy of the GPIO path: a signal handler may only touch
 * memory nothing else is writing, so it gets its own. */
static char crash_gpio[512];
static volatile sig_atomic_t crash_armed;

static void crash_motor_off(int signal_number)
{
    if(crash_armed) {
        int descriptor = open(crash_gpio, O_WRONLY | O_TRUNC | O_CLOEXEC);
        if(descriptor >= 0) {
            ssize_t ignored = write(descriptor, "0", 1);
            (void)ignored;
            close(descriptor);
        }
    }
    /* SA_RESETHAND has put the default action back: the signal is delivered
     * again as this handler returns and ends the process the way it would have. */
    raise(signal_number);
}

void ss_motor_crash_guard(const SsMotor *motor)
{
    crash_armed = 0;
    if(!motor || !motor->available) return;
    snprintf(crash_gpio, sizeof(crash_gpio), "%s", motor->gpio);
    crash_armed = 1;
    static const int signals[] = {SIGSEGV, SIGBUS, SIGABRT, SIGFPE, SIGILL};
    for(size_t i = 0; i < sizeof(signals) / sizeof(signals[0]); i++) {
        struct sigaction action;
        memset(&action, 0, sizeof(action));
        action.sa_handler = crash_motor_off;
        sigemptyset(&action.sa_mask);
        action.sa_flags = SA_RESETHAND;
        sigaction(signals[i], &action, NULL);
    }
}

void ss_motor_off(SsMotor *motor)
{
    if(!motor->available || !motor->on) return;
    write_text(motor->gpio, "0");
    motor->on = false;
}

void ss_motor_set(SsMotor *motor, float strength, uint32_t now)
{
    if(!motor->available) return;
    motor->last_event = now;
    if(!(strength >= 0.05f)) {
        ss_motor_off(motor);
        return;
    }
    if(strength > 1.0f) strength = 1.0f;
    if(motor->level[0] && !motor->level_blocked && motor->level_max > motor->level_min) {
        /* A coin motor does not start at a few hundred millivolts: the weakest
         * rumble still gets 30% so it is felt at all. */
        double span = (double)(motor->level_max - motor->level_min);
        long level = motor->level_min + (long)(span * (0.3 + 0.7 * (double)strength) + 0.5);
        long step = (motor->level_max - motor->level_min) / 50;
        if(motor->last_level < 0 || labs(level - motor->last_level) > step) {
            char text[32];
            snprintf(text, sizeof(text), "%ld", level);
            if(write_text(motor->level, text)) motor->last_level = level;
            else motor->level_blocked = true; /* the GPIO still works at its default level */
        }
    }
    if(!motor->on) {
        if(write_text(motor->gpio, "1")) motor->on = true;
    }
}

void ss_motor_tick(SsMotor *motor, uint32_t now)
{
    if(motor->on && now - motor->last_event >= SS_MOTOR_SAFETY_MS) ss_motor_off(motor);
}
