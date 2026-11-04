#include "config_store.h"
#include "frame.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#ifdef ESP_PLATFORM
#include "esp_log.h"
#else
#define ESP_LOGE(...) ((void)0)
#define ESP_LOGI(...) ((void)0)
#define ESP_LOGW(...) ((void)0)
#endif
#include "nvs.h"
#include "nvs_flash.h"
#include "pb_decode.h"
#include "pb_encode.h"
#include <ctype.h>
#include <inttypes.h>
#include <string.h>
static const char *TAG = "ek_config";
static StaticSemaphore_t mutex_storage;
static SemaphoreHandle_t mutex;
static ek_Config active;
static bool paired;
static uint32_t generation;
static nvs_handle_t handle;
static uint8_t storage[528];
static void keep_awake_defaults(ek_Config *c) {
    if (c->has_keep_awake) return;
    c->has_keep_awake = true;
    c->keep_awake = (ek_KeepAwake){.interval_min_s = 30, .interval_max_s = 60,
        .stop_after_s = 1800, .host_active_within_s = 120};
}
static uint32_t get32(const uint8_t *p) {
    return (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24);
}
static void put32(uint8_t *p, uint32_t v) {
    for (unsigned i = 0; i < 4; i++)
        p[i] = (uint8_t)(v >> (8 * i));
}
static bool utf8(const char *str, size_t max) {
    size_t n = strnlen(str, max + 1);
    if (n > max)
        return false;
    for (size_t i = 0; i < n;) {
        uint8_t c = (uint8_t)str[i++];
        if (c < 0x80)
            continue;
        unsigned count;
        uint32_t code, min;
        if (c >= 0xc2 && c <= 0xdf) {
            count = 1;
            code = c & 31;
            min = 0x80;
        } else if (c >= 0xe0 && c <= 0xef) {
            count = 2;
            code = c & 15;
            min = 0x800;
        } else if (c >= 0xf0 && c <= 0xf4) {
            count = 3;
            code = c & 7;
            min = 0x10000;
        } else
            return false;
        if (i + count > n)
            return false;
        while (count--) {
            c = (uint8_t)str[i++];
            if ((c & 0xc0) != 0x80)
                return false;
            code = (code << 6) | (c & 63);
        }
        if (code < min || code > 0x10ffff || (code >= 0xd800 && code <= 0xdfff))
            return false;
    }
    return true;
}
static bool host(uint32_t ip, uint32_t inv) {
    uint32_t h = ip & inv;
    return (ip >> 24) != 0 && (ip >> 24) != 127 && (ip >> 24) < 224 && h && h != inv;
}
uint32_t config_validate(const ek_Config *c, bool has_pair) {
    if (!utf8(c->name, 31))
        return 1;
    if (!utf8(c->ssid, 32) || !c->ssid[0])
        return 2;
    if (!has_pair && !c->has_password)
        return 3;
    size_t n = c->has_password ? strnlen(c->password, sizeof(c->password)) : 0;
    if (n && (n < 8 || n > 63)) {
        if (n != 64)
            return 3;
        for (size_t i = 0; i < 64; i++)
            if (!isxdigit((unsigned char)c->password[i]))
                return 3;
    }
    if ((c->has_psk && c->psk.size != 16) || (!has_pair && !c->has_psk))
        return 4;
    if (!c->usb_vid || c->usb_vid > 65535)
        return 5;
    if (!c->usb_pid || c->usb_pid > 65535)
        return 6;
    uint32_t inv = ~c->mask;
    if (!c->mask || inv < 3 || (inv & (inv + 1)))
        return 9;
    if (!host(c->ip, inv))
        return 7;
    if (!host(c->gw, inv) || (c->ip & c->mask) != (c->gw & c->mask) || c->gw == c->ip)
        return 8;
    if (!c->tcp_port || c->tcp_port > 65535)
        return 10;
    if (c->has_keep_awake && (!c->keep_awake.interval_min_s ||
        c->keep_awake.interval_max_s < c->keep_awake.interval_min_s ||
        c->keep_awake.interval_max_s > 86400 || c->keep_awake.mode > 1))
        return 12;
    return 0;
}
static bool load_slot(const char *key, ek_Config *cfg, bool *is_paired, uint32_t *version) {
    size_t n = sizeof(storage);
    if (nvs_get_blob(handle, key, storage, &n) != ESP_OK || n < 20)
        return false;
    if (memcmp(storage, "EKCF", 4) || (get32(storage + 4) != 1 && get32(storage + 4) != 2) ||
        get32(storage + 12) > 1 || get32(storage + n - 4) != frame_crc32(storage, n - 4))
        return false;
    *version = get32(storage + 8);
    *is_paired = get32(storage + 12) != 0;
    if (!*version)
        return false;
    *cfg = (ek_Config)ek_Config_init_zero;
    if (!*is_paired)
        return n == 20;
    pb_istream_t stream = pb_istream_from_buffer(storage + 16, n - 20);
    if (!pb_decode(&stream, ek_Config_fields, cfg))
        return false;
    if (get32(storage + 4) == 1) {
        cfg->has_password = true;
        cfg->has_psk = true;
    }
    return cfg->psk.size == 16 && !config_validate(cfg, false);
}
esp_err_t config_init(void) {
    active=(ek_Config)ek_Config_init_zero;paired=false;generation=0;
    mutex = xSemaphoreCreateMutexStatic(&mutex_storage);
    esp_err_t e = nvs_flash_init();
    if (e != ESP_OK) {
        ESP_LOGE(TAG, "NVS initialization failed: %s", esp_err_to_name(e));
        return e;
    }
    if ((e = nvs_open("ek", NVS_READWRITE, &handle)) != ESP_OK) {
        ESP_LOGE(TAG, "NVS namespace open failed: %s", esp_err_to_name(e));
        return e;
    }
    ek_Config c;
    bool pair;
    uint32_t gen;
    const char *keys[] = {"slot0", "slot1"};
    for (unsigned i = 0; i < 2; i++)
        if (load_slot(keys[i], &c, &pair, &gen) && gen > generation) {
            active = c;
            paired = pair;
            generation = gen;
        }
    keep_awake_defaults(&active);
    ESP_LOGI(TAG, "configuration loaded: paired=%u generation=%" PRIu32, (unsigned)paired, generation);
    return ESP_OK;
}
bool config_get(ek_Config *out, bool redact) {
    xSemaphoreTake(mutex, portMAX_DELAY);
    *out = active;
    bool result = paired;
    if (!paired) {
        *out = (ek_Config)ek_Config_init_zero;
        strcpy(out->name, "EspKVM");
        out->tcp_port = 24800;
        out->ip = 0xc0a801c8;
        out->gw = 0xc0a80101;
        out->mask = 0xffffff00;
        // Bench build identity requested for the connected test device.
        out->usb_vid = 0x046d;
        out->usb_pid = 0xc52b;
    }
    keep_awake_defaults(out);
    if (redact) {
        memset(out->psk.bytes, 0, 16);
        out->psk.size = 0;
        out->has_psk = false;
        out->has_password = false;
        memset(out->password, 0, sizeof(out->password));
    }
    xSemaphoreGive(mutex);
    return result;
}
static esp_err_t commit(const ek_Config *c, bool pair) {
    if (generation == UINT32_MAX)
        return ESP_ERR_INVALID_STATE;
    uint32_t next = generation + 1;
    memcpy(storage, "EKCF", 4);
    put32(storage + 4, 2);
    put32(storage + 8, next);
    put32(storage + 12, pair ? 1 : 0);
    size_t n = 16;
    if (pair) {
        pb_ostream_t stream = pb_ostream_from_buffer(storage + 16, 512 - 16);
        if (!pb_encode(&stream, ek_Config_fields, c))
            return ESP_FAIL;
        n += stream.bytes_written;
    }
    put32(storage + n, frame_crc32(storage, n));
    n += 4;
    esp_err_t e = nvs_set_blob(handle, (next & 1) ? "slot1" : "slot0", storage, n);
    if (e == ESP_OK)
        e = nvs_commit(handle);
    if (e == ESP_OK) {
        active = *c;
        paired = pair;
        generation = next;
        ESP_LOGI(TAG, "configuration committed to NVS: paired=%u generation=%" PRIu32, (unsigned)pair, next);
    } else {
        ESP_LOGE(TAG, "NVS configuration commit failed: %s", esp_err_to_name(e));
    }
    return e;
}
ek_ConfigResult config_save(const ek_Config *candidate) {
    ek_ConfigResult result = ek_ConfigResult_init_zero;
    if (xSemaphoreTake(mutex, 0) != pdTRUE) {
        result.code = 3;
        ESP_LOGW(TAG, "configuration write rejected: store busy");
        return result;
    }
    ek_Config next = *candidate;
    if (!next.has_keep_awake && paired) {
        next.has_keep_awake = active.has_keep_awake;
        next.keep_awake = active.keep_awake;
    }
    keep_awake_defaults(&next);
    if (paired && !next.has_password) {
        memcpy(next.password, active.password, sizeof(next.password));
        next.has_password = true;
    }
    if (paired && !next.has_psk) {
        next.psk = active.psk;
        next.has_psk = true;
    }
    result.field = config_validate(&next, false);
    if (result.field) {
        result.code = 1;
        ESP_LOGW(TAG, "configuration rejected by validation: field=%" PRIu32, result.field);
    } else {
        if (commit(&next, true) != ESP_OK)
            result.code = 2;
    }
    volatile unsigned char *wipe = (volatile unsigned char *)&next;
    for (size_t i = 0; i < sizeof(next); i++)
        wipe[i] = 0;
    xSemaphoreGive(mutex);
    return result;
}
esp_err_t config_reset(void) {
    if (xSemaphoreTake(mutex, 0) != pdTRUE)
        return ESP_ERR_TIMEOUT;
    ek_Config empty = ek_Config_init_zero;
    esp_err_t e = commit(&empty, false);
    xSemaphoreGive(mutex);
    return e;
}
