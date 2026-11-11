/* Host tests for stream_sys.c:
 *   cc -std=c11 -Wall -Wextra -I.. test_stream_sys.c ../stream_sys.c -o t && ./t */
#define _POSIX_C_SOURCE 200809L
#include "../stream_sys.h"

#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <unistd.h>

static int failures, checks;
static char tmpdir[256];

#define CHECK(cond) do { checks++; if(!(cond)) { failures++; \
    fprintf(stderr, "%s:%d: CHECK(%s) failed\n", __FILE__, __LINE__, #cond); } } while(0)

static void write_file(const char *path, const char *text)
{
    FILE *f = fopen(path, "w");
    if(!f) { perror(path); exit(2); }
    fputs(text, f);
    fclose(f);
}

static void read_file(const char *path, char *out, size_t size)
{
    out[0] = '\0';
    FILE *f = fopen(path, "r");
    if(!f) return;
    size_t n = fread(out, 1, size - 1, f);
    out[n] = '\0';
    fclose(f);
}

static void mkdirs(const char *path)
{
    char buf[512];
    snprintf(buf, sizeof(buf), "%s", path);
    for(char *p = buf + 1; *p; p++) {
        if(*p == '/') { *p = '\0'; mkdir(buf, 0755); *p = '/'; }
    }
    mkdir(buf, 0755);
}

static const char *session_path(void)
{
    static char path[300];
    snprintf(path, sizeof(path), "%s/session.conf", tmpdir);
    return path;
}

/* 16 bytes 00..0f */
#define RP_KEY "AAECAwQFBgcICQoLDA0ODw=="

static void test_base64(void)
{
    uint8_t out[32];
    size_t size = sizeof(out);
    CHECK(ss_base64_decode(RP_KEY, out, &size) && size == 16 && out[0] == 0 && out[15] == 15);
    size = sizeof(out);
    CHECK(ss_base64_decode("TWFu", out, &size) && size == 3 && !memcmp(out, "Man", 3));
    size = sizeof(out);
    CHECK(ss_base64_decode("TWE=", out, &size) && size == 2);
    size = sizeof(out);
    CHECK(!ss_base64_decode("TWE", out, &size));      /* not a multiple of 4 */
    size = sizeof(out);
    CHECK(!ss_base64_decode("T=E=", out, &size));     /* padding in the middle */
    size = sizeof(out);
    CHECK(!ss_base64_decode("TW!u", out, &size));     /* bad character */
    size = 2;
    CHECK(!ss_base64_decode("TWFu", out, &size));     /* does not fit */
    size = sizeof(out);
    CHECK(!ss_base64_decode("", out, &size));
}

static void test_session_old_format(void)
{
    SsConfig c;
    char err[128] = {0};
    write_file(session_path(),
               "host=192.168.1.40\nregist_key=0a1b2c3d\nrp_key=" RP_KEY "\nps5=1\ntarget=1000100\n"
               "rp_version=1.0\nverbose=0\nwidth=960\nheight=540\nfps=30\nbitrate=4000\nvolume=70\n");
    CHECK(ss_load_config(session_path(), &c, err, sizeof(err)));
    CHECK(access(session_path(), F_OK) != 0); /* deleted after reading */
    CHECK(!strcmp(c.host, "192.168.1.40") && !strcmp(c.regist_key, "0a1b2c3d"));
    CHECK(c.morning[0] == 0 && c.morning[15] == 15);
    CHECK(c.ps5 && c.target == 1000100 && c.width == 960 && c.height == 540 && c.bitrate == 4000 && c.volume == 70);
    CHECK(c.codec == SS_CODEC_H265);         /* a PS5 without codec= keeps HEVC */
    CHECK(!strcmp(c.name, "PlayStation"));
    CHECK(c.rumble && c.hints && !c.dpad_stick && !c.remote && c.select_create == 0);
    CHECK(c.login_pin[0] == '\0' && c.status_file[0] == '\0' && c.motor_gpio[0] == '\0');
}

static void test_session_new_keys(void)
{
    SsConfig c;
    char err[128] = {0};
    write_file(session_path(),
               "host = example.org \r\nregist_key=1234\r\nrp_key=" RP_KEY "\r\nps5=1\r\ntarget=1000100\r\n"
               "name=Juka\xe2\x80\x99s PS5\r\ncodec=h264\r\nlogin_pin=0420\r\nselect_button=create\r\n"
               "dpad_stick=1\r\nrumble=0\r\nhints=0\r\nremote=1\r\n"
               "motor_gpio=/sys/class/gpio/gpio227/value\r\nmotor_level=/sys/class/motor/voltage\r\n"
               "motor_level_min=500000\r\nmotor_level_max=2500000\r\nstatus_file=/mnt/SDCARD/Apps/JukaHub/psremote-last.txt\r\n"
               "unknown_key=whatever\r\n");
    CHECK(ss_load_config(session_path(), &c, err, sizeof(err)));
    CHECK(!strcmp(c.host, "example.org"));
    CHECK(!strcmp(c.name, "Juka?s PS5"));
    CHECK(c.codec == SS_CODEC_H264);
    CHECK(!strcmp(c.login_pin, "0420"));
    CHECK(c.select_create == 1 && c.dpad_stick && !c.rumble && !c.hints && c.remote);
    CHECK(!strcmp(c.motor_gpio, "/sys/class/gpio/gpio227/value"));
    CHECK(c.motor_range_set && c.motor_level_min == 500000 && c.motor_level_max == 2500000);
    CHECK(!strcmp(c.status_file, "/mnt/SDCARD/Apps/JukaHub/psremote-last.txt"));
}

static void test_session_rejects(void)
{
    SsConfig c;
    char err[128];
    const char *bad[] = {
        "regist_key=0a1b\nrp_key=" RP_KEY "\n",                              /* no host */
        "host=h\nrp_key=" RP_KEY "\n",                                       /* no regist key */
        "host=h\nregist_key=0a1b2c3d4\nrp_key=" RP_KEY "\n",                 /* key dump, 9 chars */
        "host=h\nregist_key=0a1b\nrp_key=AAECAwQFBgcICQoLDA0O\n",            /* 15 bytes */
        "host=h\nregist_key=0a1b\nrp_key=not base64 at all\n",
        "host=h\nregist_key=0a1b\n",                                          /* no rp_key */
    };
    for(size_t i = 0; i < sizeof(bad) / sizeof(bad[0]); i++) {
        err[0] = '\0';
        write_file(session_path(), bad[i]);
        CHECK(!ss_load_config(session_path(), &c, err, sizeof(err)));
        CHECK(!strcmp(err, "invalid or incomplete session file"));
        CHECK(c.regist_key[0] == '\0' && c.morning[0] == 0 && c.morning[15] == 0); /* wiped */
    }
    err[0] = '\0';
    CHECK(!ss_load_config("/nonexistent/session.conf", &c, err, sizeof(err)));
    CHECK(!strncmp(err, "cannot open session file", 24));
}

static void test_session_value_checks(void)
{
    SsConfig c;
    char err[128];
    write_file(session_path(),
               "host=h\nregist_key=ab\nrp_key=" RP_KEY "\nps5=0\ncodec=h265\nlogin_pin=12345\n"
               "width=99999\nfps=0\nvolume=-5\nbitrate=7000x\nmotor_gpio=relative/path\n"
               "motor_level=/a/../b\nstatus_file=psremote-last.txt\nmotor_level_min=9\nmotor_level_max=3\n"
               "select_button=weird\ndpad_stick=maybe\n");
    CHECK(ss_load_config(session_path(), &c, err, sizeof(err)));
    CHECK(c.codec == SS_CODEC_H264);                  /* PS4: always H.264 */
    CHECK(c.login_pin[0] == '\0');                    /* 5 digits ignored */
    CHECK(c.width == 1280 && c.fps == 30 && c.volume == 80 && c.bitrate == 8000); /* defaults kept */
    CHECK(c.motor_gpio[0] == '\0' && c.motor_level[0] == '\0' && c.status_file[0] == '\0');
    CHECK(!c.motor_range_set);                        /* max <= min dropped */
    CHECK(c.select_create == 0 && !c.dpad_stick);
    write_file(session_path(), "host=h\nregist_key=ab\nrp_key=" RP_KEY "\nlogin_pin=12a4\nname=\x01\x02\n");
    CHECK(ss_load_config(session_path(), &c, err, sizeof(err)));
    CHECK(c.login_pin[0] == '\0' && !strcmp(c.name, "PlayStation"));
}

/* A line longer than SS_LINE_MAX is skipped whole: before, its tail was read as
 * the next line, so "name=<800 x>host=..." could change the host. */
static void test_session_long_lines(void)
{
    SsConfig c;
    char err[128] = {0};
    static char text[4096];
    /* "name=" + 762 bytes is where the old 768-byte fgets() buffer split the line,
     * so its tail "host=10.6.6.6" used to replace the host. */
    char filler[763];
    memset(filler, 'x', sizeof(filler) - 1);
    filler[sizeof(filler) - 1] = '\0';
    snprintf(text, sizeof(text),
             "host=192.168.1.40\nregist_key=0a1b2c3d\nrp_key=" RP_KEY "\nname=%shost=10.6.6.6\n"
             "volume=55\n", filler);
    write_file(session_path(), text);
    CHECK(ss_load_config(session_path(), &c, err, sizeof(err)));
    CHECK(!strcmp(c.host, "192.168.1.40"));
    CHECK(!strcmp(c.name, "PlayStation")); /* the long name line went as a whole */
    CHECK(c.volume == 55);                  /* the line after it still counts */
    /* A line of exactly SS_LINE_MAX bytes is still read. */
    char name[SS_LINE_MAX + 1];
    memset(name, 'n', SS_LINE_MAX - 5);
    name[SS_LINE_MAX - 5] = '\0';
    snprintf(text, sizeof(text), "host=h\nregist_key=ab\nrp_key=" RP_KEY "\nname=%s", name); /* no final newline */
    write_file(session_path(), text);
    CHECK(ss_load_config(session_path(), &c, err, sizeof(err)));
    CHECK(strlen(c.name) == sizeof(c.name) - 1 && c.name[0] == 'n');
    /* A file bigger than SS_SESSION_MAX is refused, and deleted all the same. */
    FILE *f = fopen(session_path(), "w");
    fputs("host=h\nregist_key=ab\nrp_key=" RP_KEY "\n", f);
    for(int i = 0; i < SS_SESSION_MAX / 10 + 10; i++) fputs("# padding\n", f);
    fclose(f);
    CHECK(!ss_load_config(session_path(), &c, err, sizeof(err)));
    CHECK(access(session_path(), F_OK) != 0);
    CHECK(c.regist_key[0] == '\0');
}

static void test_wipe(void)
{
    char secret[16];
    memcpy(secret, "1234567890abcdef", sizeof(secret));
    ss_wipe(secret, sizeof(secret));
    int zero = 1;
    for(size_t i = 0; i < sizeof(secret); i++) zero &= secret[i] == 0;
    CHECK(zero);
    ss_wipe(NULL, 4); /* harmless */
}

static void test_status_file(void)
{
    char path[300], text[2048], tmp[400];
    snprintf(path, sizeof(path), "%s/psremote-last.txt", tmpdir);
    SsStatus st = {0};
    st.rc = 11;
    st.result = "connect_failed";
    st.message = "Could not reach the console.\nCheck Wi-Fi.";
    st.connected = false;
    st.seconds = 0;
    st.frames = 0;
    st.quit_reason = "Connection refused";
    st.host = "192.168.1.40";
    st.codec = "h265";
    CHECK(ss_write_status(path, &st));
    read_file(path, text, sizeof(text));
    CHECK(strstr(text, "version=1\nrc=11\nresult=connect_failed\n") == text);
    CHECK(strstr(text, "message=Could not reach the console. Check Wi-Fi.\n") != NULL);
    CHECK(strstr(text, "connected=0\nseconds=0\nframes=0\nquit_reason=Connection refused\nhost=192.168.1.40\ncodec=h265\nsaved_pin_rejected=0\n") != NULL);
    struct stat sb;
    CHECK(stat(path, &sb) == 0 && (sb.st_mode & 0777) == 0600);
    snprintf(tmp, sizeof(tmp), "%s.tmp-%ld", path, (long)getpid());
    CHECK(access(tmp, F_OK) != 0);
    st.rc = 0; st.result = "ok"; st.connected = true; st.seconds = 125; st.frames = 3750; st.message = NULL; st.quit_reason = NULL;
    st.saved_pin_rejected = true;
    CHECK(ss_write_status(path, &st)); /* replaces the old one */
    read_file(path, text, sizeof(text));
    CHECK(strstr(text, "result=ok\nmessage=\nconnected=1\nseconds=125\nframes=3750\nquit_reason=\n") != NULL);
    CHECK(strstr(text, "saved_pin_rejected=1\n") != NULL);
    CHECK(!ss_write_status("/nonexistent-dir/x/status", &st));
    CHECK(!ss_write_status("", &st));

    /* A stale temp file of this pid is replaced; a link planted at the temp name
     * is removed, not written through. */
    write_file(tmp, "stale");
    st.result = "cancelled";
    CHECK(ss_write_status(path, &st));
    read_file(path, text, sizeof(text));
    CHECK(strstr(text, "result=cancelled\n") != NULL);
    char victim[400];
    snprintf(victim, sizeof(victim), "%s/victim.txt", tmpdir);
    write_file(victim, "precious");
    CHECK(symlink(victim, tmp) == 0);
    st.result = "ok";
    CHECK(ss_write_status(path, &st));
    read_file(victim, text, sizeof(text));
    CHECK(!strcmp(text, "precious"));
    read_file(path, text, sizeof(text));
    CHECK(strstr(text, "result=ok\n") != NULL);
    CHECK(access(tmp, F_OK) != 0);
}

static void test_battery(void)
{
    char root[300], dir[400], path[500];
    snprintf(root, sizeof(root), "%s/bat", tmpdir);
    snprintf(dir, sizeof(dir), "%s/sys/devices/axp/axp2202-battery", root);
    mkdirs(dir);
    snprintf(path, sizeof(path), "%s/capacity", dir); write_file(path, "57\n");
    snprintf(path, sizeof(path), "%s/status", dir); write_file(path, "Discharging\n");
    snprintf(path, sizeof(path), "%s/sys/class/power_supply", root); mkdirs(path);
    /* the class directory holds symlinks, like the kernel's */
    char link[500];
    snprintf(link, sizeof(link), "%s/sys/class/power_supply/axp2202-battery", root);
    CHECK(symlink(dir, link) == 0);
    SsBattery b;
    CHECK(ss_battery_read(root, &b) && b.valid && b.percent == 57 && !b.on_power);
    /* Status Full with no charger entry counts as on power. */
    snprintf(path, sizeof(path), "%s/status", dir); write_file(path, "Full\n");
    CHECK(ss_battery_read(root, &b) && b.on_power);
    /* A charger entry decides when the battery does not say "Charging". */
    snprintf(path, sizeof(path), "%s/sys/class/power_supply/axp2202-usb", root); mkdirs(path);
    snprintf(path, sizeof(path), "%s/sys/class/power_supply/axp2202-usb/online", root); write_file(path, "0\n");
    CHECK(ss_battery_read(root, &b) && !b.on_power);
    write_file(path, "1\n");
    CHECK(ss_battery_read(root, &b) && b.on_power);
    write_file(path, "0\n");
    snprintf(path, sizeof(path), "%s/status", dir); write_file(path, "Charging\n");
    CHECK(ss_battery_read(root, &b) && b.on_power);
    snprintf(path, sizeof(path), "%s/capacity", dir); write_file(path, "140\n");
    CHECK(ss_battery_read(root, &b) && b.percent == 100);
    CHECK(!ss_battery_read("/nonexistent-root", &b) && !b.valid && b.percent == -1);
}

static void input_device(const char *root, const char *event, const char *name, const char *vendor, const char *product)
{
    char path[700];
    snprintf(path, sizeof(path), "%s/sys/class/input/%s/device/id", root, event); mkdirs(path);
    snprintf(path, sizeof(path), "%s/sys/class/input/%s/device/name", root, event); write_file(path, name);
    snprintf(path, sizeof(path), "%s/sys/class/input/%s/device/id/vendor", root, event); write_file(path, vendor);
    snprintf(path, sizeof(path), "%s/sys/class/input/%s/device/id/product", root, event); write_file(path, product);
}

static void test_builtin_pad(void)
{
    char root[400];
    char other[400];
    snprintf(root, sizeof(root), "%s/pads", tmpdir);
    snprintf(other, sizeof(other), "%s/pads-desktop", tmpdir);
    input_device(root, "event3", "TRIMUI Player1\n", "045e\n", "028e\n");
    input_device(root, "event7", "Microsoft X-Box 360 pad\n", "045e\n", "028e\n");
    input_device(root, "event9", "Sony Interactive Entertainment Wireless Controller\n", "054c\n", "0ce6\n");
    input_device(other, "event2", "Microsoft X-Box 360 pad\n", "045e\n", "028e\n");
    /* SDL's name says so (a firmware mapping that keeps the device name). */
    CHECK(ss_builtin_pad(root, "TRIMUI Player1", 0, 0, NULL));
    /* SDL calls it "Xbox 360 Controller"; the event node's kernel name decides. */
    CHECK(ss_builtin_pad(root, "Xbox 360 Controller", 0x045e, 0x028e, "/dev/input/event3"));
    CHECK(!ss_builtin_pad(root, "Xbox 360 Controller", 0x045e, 0x028e, "/dev/input/event7"));
    /* No node (SDL older than 2.24), or one that is not an event node: the id
     * counts while a TRIMUI device with that id exists. */
    CHECK(ss_builtin_pad(root, "Xbox 360 Controller", 0x045e, 0x028e, NULL));
    CHECK(ss_builtin_pad(root, "Xbox 360 Controller", 0x045e, 0x028e, "/dev/hidraw2"));
    CHECK(ss_builtin_pad(root, "Xbox 360 Controller", 0x045e, 0x028e, "/dev/input/event99"));
    CHECK(!ss_builtin_pad(other, "Xbox 360 Controller", 0x045e, 0x028e, NULL));
    CHECK(!ss_builtin_pad("/nonexistent-root", "Xbox 360 Controller", 0x045e, 0x028e, NULL));
    /* Any other pad is external. */
    CHECK(!ss_builtin_pad(root, "DualSense Wireless Controller", 0x054c, 0x0ce6, "/dev/input/event9"));
    CHECK(!ss_builtin_pad(root, "DualSense Wireless Controller", 0x054c, 0x0ce6, NULL));
    CHECK(!ss_builtin_pad(root, NULL, 0, 0, NULL));
    /* A node name that is not "eventN" is never read; the scan decides. */
    CHECK(ss_builtin_pad(root, "Xbox 360 Controller", 0x045e, 0x028e, "/dev/input/event7x"));
}

static void test_motor(void)
{
    char root[300], path[500], text[64];
    SsMotor m;
    snprintf(root, sizeof(root), "%s/motor5040", tmpdir);
    snprintf(path, sizeof(path), "%s/sys/class/motor", root); mkdirs(path);
    snprintf(path, sizeof(path), "%s/sys/class/gpio/gpio227", root); mkdirs(path);
    snprintf(path, sizeof(path), "%s/sys/class/motor/voltage", root); write_file(path, "3300000\n");
    snprintf(path, sizeof(path), "%s/sys/class/gpio/gpio227/value", root); write_file(path, "1\n");
    ss_motor_open(&m, root, NULL, NULL, 0, 0, false);
    CHECK(m.available && m.level_min == 500000 && m.level_max == 3300000);
    read_file(path, text, sizeof(text));
    CHECK(!strcmp(text, "0") && !m.on); /* left on by a run that died: switched off at open */
    ss_motor_set(&m, 1.0f, 100);
    read_file(path, text, sizeof(text));
    CHECK(!strcmp(text, "1") && m.on);
    char vpath[500];
    snprintf(vpath, sizeof(vpath), "%s/sys/class/motor/voltage", root);
    read_file(vpath, text, sizeof(text));
    CHECK(!strcmp(text, "3300000"));
    ss_motor_set(&m, 0.5f, 200);
    read_file(vpath, text, sizeof(text));
    CHECK(!strcmp(text, "2320000"));
    write_file(path, "untouched");
    ss_motor_set(&m, 0.505f, 250); /* already on, level within a 2% step: no writes */
    read_file(path, text, sizeof(text));
    CHECK(!strcmp(text, "untouched"));
    ss_motor_set(&m, 0.01f, 300);
    read_file(path, text, sizeof(text));
    CHECK(!strcmp(text, "0") && !m.on);
    ss_motor_set(&m, 0.8f, 400);
    ss_motor_tick(&m, 400 + SS_MOTOR_SAFETY_MS - 1);
    CHECK(m.on);
    ss_motor_tick(&m, 400 + SS_MOTOR_SAFETY_MS);
    read_file(path, text, sizeof(text));
    CHECK(!m.on && !strcmp(text, "0"));
    ss_motor_set(&m, 0.8f, 20000);
    ss_motor_off(&m);
    CHECK(!m.on);

    /* TG5050: level node 0..65535. */
    snprintf(root, sizeof(root), "%s/motor5050", tmpdir);
    snprintf(path, sizeof(path), "%s/sys/class/motor", root); mkdirs(path);
    snprintf(path, sizeof(path), "%s/sys/class/gpio/gpio236", root); mkdirs(path);
    snprintf(path, sizeof(path), "%s/sys/class/motor/level", root); write_file(path, "0\n");
    snprintf(path, sizeof(path), "%s/sys/class/gpio/gpio236/value", root); write_file(path, "0\n");
    ss_motor_open(&m, root, NULL, NULL, 0, 0, false);
    CHECK(m.available && m.level_max == 65535);
    ss_motor_set(&m, 1.0f, 1);
    snprintf(path, sizeof(path), "%s/sys/class/motor/level", root);
    read_file(path, text, sizeof(text));
    CHECK(!strcmp(text, "65535"));

    /* No vendor strength node: never touches gpio export. */
    snprintf(root, sizeof(root), "%s/nomotor", tmpdir);
    snprintf(path, sizeof(path), "%s/sys/class/gpio", root); mkdirs(path);
    snprintf(path, sizeof(path), "%s/sys/class/gpio/export", root); write_file(path, "");
    ss_motor_open(&m, root, NULL, NULL, 0, 0, false);
    CHECK(!m.available);
    read_file(path, text, sizeof(text));
    CHECK(text[0] == '\0');
    ss_motor_set(&m, 1.0f, 1); /* harmless */

    /* Vendor node present, GPIO not exported: the pin is exported, and a GPIO
     * that never appears leaves the motor off. */
    snprintf(path, sizeof(path), "%s/sys/class/motor", root); mkdirs(path);
    snprintf(path, sizeof(path), "%s/sys/class/motor/voltage", root); write_file(path, "0\n");
    ss_motor_open(&m, root, NULL, NULL, 0, 0, false);
    snprintf(path, sizeof(path), "%s/sys/class/gpio/export", root);
    read_file(path, text, sizeof(text));
    CHECK(!strcmp(text, "227") && !m.available);

    /* Explicit nodes from JukaHub, with the Brick Pro's 2.5 V cap. */
    char gpio[500], level[500];
    snprintf(gpio, sizeof(gpio), "%s/motor5040/sys/class/gpio/gpio227/value", tmpdir);
    snprintf(level, sizeof(level), "%s/motor5040/sys/class/motor/voltage", tmpdir);
    ss_motor_open(&m, "", gpio, level, 500000, 2500000, true);
    CHECK(m.available);
    ss_motor_set(&m, 1.0f, 5);
    read_file(level, text, sizeof(text));
    CHECK(!strcmp(text, "2500000"));
    /* Explicit GPIO without a range: on/off only, the strength node is left alone. */
    write_file(level, "unchanged");
    ss_motor_off(&m);
    write_file(gpio, "1");
    ss_motor_open(&m, "", gpio, level, 0, 0, false);
    read_file(gpio, text, sizeof(text));
    CHECK(!strcmp(text, "0")); /* the explicit node is switched off at open too */
    ss_motor_set(&m, 1.0f, 6);
    read_file(level, text, sizeof(text));
    CHECK(m.on && !strcmp(text, "unchanged"));
}

/* A crash with the motor running: the guard writes "0" and the process still
 * dies of the signal. Runs in a child so the abort does not end the tests. */
static void test_motor_crash_guard(void)
{
    char root[300], gpio[500], level[500], text[64];
    snprintf(root, sizeof(root), "%s/motorcrash", tmpdir);
    snprintf(gpio, sizeof(gpio), "%s/value", root);
    snprintf(level, sizeof(level), "%s/level", root);
    mkdirs(root);
    write_file(gpio, "0");
    write_file(level, "0");
    fflush(NULL);
    pid_t child = fork();
    if(child == 0) {
        SsMotor m;
        ss_motor_open(&m, "", gpio, level, 0, 65535, true);
        ss_motor_set(&m, 1.0f, 1);
        ss_motor_crash_guard(&m);
        abort();
    }
    int status = 0;
    CHECK(child > 0 && waitpid(child, &status, 0) == child);
    CHECK(WIFSIGNALED(status) && WTERMSIG(status) == SIGABRT);
    read_file(gpio, text, sizeof(text));
    CHECK(!strcmp(text, "0"));
    read_file(level, text, sizeof(text));
    CHECK(!strcmp(text, "65535")); /* the motor really was running when it died */

    /* No motor: the guard installs nothing and the crash is untouched. */
    fflush(NULL);
    child = fork();
    if(child == 0) {
        SsMotor none;
        memset(&none, 0, sizeof(none));
        ss_motor_crash_guard(&none);
        abort();
    }
    CHECK(child > 0 && waitpid(child, &status, 0) == child);
    CHECK(WIFSIGNALED(status) && WTERMSIG(status) == SIGABRT);
}

int main(int argc, char **argv)
{
    const char *base = argc > 1 ? argv[1] : "/tmp";
    snprintf(tmpdir, sizeof(tmpdir), "%s/t-sys-%ld", base, (long)getpid());
    mkdirs(tmpdir);
    test_base64();
    test_session_old_format();
    test_session_new_keys();
    test_session_rejects();
    test_session_value_checks();
    test_session_long_lines();
    test_wipe();
    test_status_file();
    test_battery();
    test_builtin_pad();
    test_motor();
    test_motor_crash_guard();
    printf("stream_sys: %d checks, %d failures\n", checks, failures);
    char cmd[400];
    snprintf(cmd, sizeof(cmd), "rm -rf '%s'", tmpdir);
    if(system(cmd) != 0) fprintf(stderr, "cleanup failed\n");
    return failures ? 1 : 0;
}
