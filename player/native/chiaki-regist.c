#include <chiaki/base64.h>
#include <chiaki/common.h>
#include <chiaki/log.h>
#include <chiaki/regist.h>

#include <errno.h>
#include <fcntl.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

#define PS5_TARGET 1000100

typedef struct {
    char host[256];
    uint8_t account_id[CHIAKI_PSN_ACCOUNT_ID_SIZE];
    uint32_t pin;
} RegistConfig;

typedef struct {
    bool finished;
    bool success;
    const char *failure_stage;
    ChiakiRegisteredHost host;
} RegistResult;

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

static bool parse_pin(const char *text, uint32_t *pin)
{
    if(strlen(text) != 8) return false;
    for(const char *cursor = text; *cursor; cursor++)
        if(*cursor < '0' || *cursor > '9') return false;
    *pin = (uint32_t)strtoul(text, NULL, 10);
    return true;
}

static bool load_config(const char *path, RegistConfig *config)
{
    memset(config, 0, sizeof(*config));
    FILE *file = fopen(path, "r");
    if(!file) {
        fprintf(stderr, "[ps5-regist] stage=input error=open_failed detail=%s\n", strerror(errno));
        return false;
    }

    char account_id[64] = {0};
    char pin[16] = {0};
    char line[512];
    while(fgets(line, sizeof(line), file)) {
        char *separator = strchr(line, '=');
        if(!separator) continue;
        *separator = '\0';
        char *value = separator + 1;
        trim(line);
        trim(value);
        if(!strcmp(line, "host")) snprintf(config->host, sizeof(config->host), "%s", value);
        else if(!strcmp(line, "pin")) snprintf(pin, sizeof(pin), "%s", value);
        else if(!strcmp(line, "account_id")) snprintf(account_id, sizeof(account_id), "%s", value);
    }
    fclose(file);
    unlink(path);

    size_t account_id_size = sizeof(config->account_id);
    bool valid = config->host[0] && parse_pin(pin, &config->pin) &&
        chiaki_base64_decode(account_id, strlen(account_id), config->account_id,
                             &account_id_size) == CHIAKI_ERR_SUCCESS &&
        account_id_size == sizeof(config->account_id);
    memset(pin, 0, sizeof(pin));
    memset(account_id, 0, sizeof(account_id));
    if(!valid)
        fprintf(stderr, "[ps5-regist] stage=input error=invalid_config\n");
    return valid;
}

static void regist_log(ChiakiLogLevel level, const char *message, void *user)
{
    RegistResult *result = user;
    if(strstr(message, "getaddrinfo") || strstr(message, "search") ||
            strstr(message, "connect") || strstr(message, "send") ||
            strstr(message, "receive") || strstr(message, "timed out") ||
            strstr(message, "network"))
        result->failure_stage = "network";
    else if(strstr(message, "response") || strstr(message, "payload") ||
            strstr(message, "HTTP"))
        result->failure_stage = "protocol";
    fprintf(stderr, "[ps5-regist] stage=%s upstream=%c %s\n",
            result->failure_stage, chiaki_log_level_char(level), message);
    fflush(stderr);
}

static void regist_cb(ChiakiRegistEvent *event, void *user)
{
    RegistResult *result = user;
    result->finished = true;
    if(event->type == CHIAKI_REGIST_EVENT_TYPE_FINISHED_SUCCESS && event->registered_host) {
        result->success = true;
        memcpy(&result->host, event->registered_host, sizeof(result->host));
    }
}

static bool write_hex(FILE *file, const char *key, const uint8_t *value, size_t size)
{
    if(fprintf(file, "%s=", key) < 0) return false;
    for(size_t index = 0; index < size; index++)
        if(fprintf(file, "%02x", value[index]) < 0) return false;
    return fputc('\n', file) != EOF;
}

static bool write_result(const char *path, const ChiakiRegisteredHost *host)
{
    int descriptor = open(path, O_WRONLY | O_CREAT | O_TRUNC, 0600);
    if(descriptor < 0) return false;
    if(fchmod(descriptor, 0600) != 0) {
        close(descriptor);
        return false;
    }
    FILE *file = fdopen(descriptor, "w");
    if(!file) {
        close(descriptor);
        return false;
    }

    size_t regist_size = sizeof(host->rp_regist_key);
    while(regist_size && !host->rp_regist_key[regist_size - 1]) regist_size--;
    bool ok = fprintf(file, "target=%u\n", (unsigned int)host->target) >= 0 &&
        fprintf(file, "rp_key_type=%u\n", host->rp_key_type) >= 0 &&
        write_hex(file, "regist_key", (const uint8_t *)host->rp_regist_key, regist_size) &&
        write_hex(file, "rp_key", host->rp_key, sizeof(host->rp_key)) &&
        write_hex(file, "server_mac", host->server_mac, sizeof(host->server_mac));
    if(fflush(file) != 0 || fsync(descriptor) != 0) ok = false;
    if(fclose(file) != 0) ok = false;
    return ok;
}

int main(int argc, char **argv)
{
    if(argc != 3) {
        fprintf(stderr, "usage: chiaki-regist INPUT_FILE RESULT_FILE\n");
        return 2;
    }

    RegistConfig config;
    if(!load_config(argv[1], &config)) return 3;
    ChiakiErrorCode error = chiaki_lib_init();
    if(error != CHIAKI_ERR_SUCCESS) {
        fprintf(stderr, "[ps5-regist] stage=start error=chiaki_init detail=%s\n",
                chiaki_error_string(error));
        return 4;
    }

    RegistResult result;
    memset(&result, 0, sizeof(result));
    result.failure_stage = "protocol";
    ChiakiLog log;
    chiaki_log_init(&log, CHIAKI_LOG_INFO | CHIAKI_LOG_WARNING | CHIAKI_LOG_ERROR,
                    regist_log, &result);
    ChiakiRegistInfo info;
    memset(&info, 0, sizeof(info));
    info.target = (ChiakiTarget)PS5_TARGET;
    info.host = config.host;
    info.broadcast = false;
    memcpy(info.psn_account_id, config.account_id, sizeof(info.psn_account_id));
    info.pin = config.pin;

    ChiakiRegist regist;
    memset(&regist, 0, sizeof(regist));
    fprintf(stderr, "[ps5-regist] stage=start target=%u broadcast=off\n", PS5_TARGET);
    error = chiaki_regist_start(&regist, &log, &info, regist_cb, &result);
    memset(config.account_id, 0, sizeof(config.account_id));
    config.pin = 0;
    if(error != CHIAKI_ERR_SUCCESS) {
        fprintf(stderr, "[ps5-regist] stage=start error=%s\n", chiaki_error_string(error));
        return 5;
    }
    chiaki_regist_fini(&regist);
    if(!result.finished || !result.success) {
        fprintf(stderr, "[ps5-regist] stage=%s error=registration_failed\n",
                result.failure_stage);
        return 6;
    }
    if(!write_result(argv[2], &result.host)) {
        fprintf(stderr, "[ps5-regist] stage=result error=write_failed detail=%s\n", strerror(errno));
        memset(&result.host, 0, sizeof(result.host));
        return 7;
    }
    memset(&result.host, 0, sizeof(result.host));
    fprintf(stderr, "[ps5-regist] stage=result status=success target=%u\n", PS5_TARGET);
    return 0;
}
