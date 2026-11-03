#include "hid_service.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "hid_engine.h"
#include "keep_awake.h"
#include "esp_random.h"
#include "message_validate.h"
#include "tinyusb.h"
#include "tinyusb_default_config.h"
#include "esp_log.h"
#include <stdatomic.h>
#include <string.h>

enum { KEYBOARD, MOUSE, CONSUMER, INTERFACE_COUNT };
static const uint8_t nkro_descriptor[] = {
    0x05, 0x01, 0x09, 0x06, 0xa1, 0x01, 0x05, 0x07, 0x19, 0xe0, 0x29, 0xe7, 0x15, 0x00,
    0x25, 0x01, 0x75, 0x01, 0x95, 0x08, 0x81, 0x02, 0x19, 0x00, 0x2a, 0xff, 0x00, 0x75,
    0x01, 0x96, 0x00, 0x01, 0x81, 0x02, 0x05, 0x08, 0x19, 0x01, 0x29, 0x03, 0x75, 0x01,
    0x95, 0x03, 0x91, 0x02, 0x75, 0x05, 0x95, 0x01, 0x91, 0x03, 0xc0};
static const uint8_t boot_descriptor[] = {TUD_HID_REPORT_DESC_KEYBOARD()};
static const uint8_t mouse_descriptor[] = {
    0x05, 0x01, 0x09, 0x02, 0xa1, 0x01, 0x09, 0x01, 0xa1, 0x00, 0x05, 0x09, 0x19, 0x01, 0x29, 0x05,
    0x15, 0x00, 0x25, 0x01, 0x75, 0x01, 0x95, 0x05, 0x81, 0x02, 0x75, 0x03, 0x95, 0x01, 0x81, 0x03,
    0x05, 0x01, 0x09, 0x30, 0x09, 0x31, 0x16, 0x01, 0x80, 0x26, 0xff, 0x7f, 0x75, 0x10, 0x95, 0x02,
    0x81, 0x06, 0x09, 0x38, 0x15, 0x81, 0x25, 0x7f, 0x75, 0x08, 0x95, 0x01, 0x81, 0x06, 0x05, 0x0c,
    0x0a, 0x38, 0x02, 0x15, 0x81, 0x25, 0x7f, 0x75, 0x08, 0x95, 0x01, 0x81, 0x06, 0xc0, 0xc0};
static const uint8_t consumer_descriptor[] = {TUD_HID_REPORT_DESC_CONSUMER()};
#define CONFIGURATION_LENGTH (TUD_CONFIG_DESC_LEN + INTERFACE_COUNT * TUD_HID_DESC_LEN)
#define CONFIGURATION(keyboard_size)                                                               \
    TUD_CONFIG_DESCRIPTOR(1, INTERFACE_COUNT, 0, CONFIGURATION_LENGTH,                             \
                          TUSB_DESC_CONFIG_ATT_REMOTE_WAKEUP, 100),                                \
        TUD_HID_DESCRIPTOR(KEYBOARD, 0, HID_ITF_PROTOCOL_KEYBOARD, keyboard_size, 0x81, 64, 1),    \
        TUD_HID_DESCRIPTOR(MOUSE, 0, HID_ITF_PROTOCOL_NONE, sizeof(mouse_descriptor), 0x82, 16,    \
                           1),                                                                     \
        TUD_HID_DESCRIPTOR(CONSUMER, 0, HID_ITF_PROTOCOL_NONE, sizeof(consumer_descriptor), 0x83,  \
                           8, 1)
static const uint8_t normal_configuration[] = {CONFIGURATION(sizeof(nkro_descriptor))};
static const uint8_t compatible_configuration[] = {CONFIGURATION(sizeof(boot_descriptor))};
static tusb_desc_device_t device_descriptor = {.bLength = sizeof(tusb_desc_device_t),
                                               .bDescriptorType = TUSB_DESC_DEVICE,
                                               .bcdUSB = 0x0200,
                                               .bMaxPacketSize0 = 64,
                                               .idVendor = 0,
                                               .idProduct = 0,
                                               .bcdDevice = 0x0100,
                                               .iManufacturer = 1,
                                               .iProduct = 2,
                                               .bNumConfigurations = 1};
static const char *strings[] = {(const char[]){0x09, 0x04}, "Logitech", "USB ReceiverX"};
static const char *TAG = "ek_hid";

static hid_engine engine;
static keep_awake awake;
static bool compatible;
static bool usb_present, usb_suspended;
// A sleeping host may reset the bus while resuming; within this grace period a
// detach is reported as "waking" so the selected target survives the wake.
#define WAKE_GRACE_MS 20000u
static uint64_t waking_until, wake_at;
static atomic_uchar leds;
static portMUX_TYPE lock = portMUX_INITIALIZER_UNLOCKED;
static uint8_t last_report[3][33], last_size[3], idle[3];
static uint64_t last_at[3];
static uint64_t now_ms(void) { return esp_timer_get_time() / 1000; }
// Caller holds lock. Suspended, or between a wake-time bus reset and re-enumeration:
// the host cannot see stale input from us, and pending-release runs before input.
static bool sleeping(uint64_t now) {
    return usb_suspended || (!usb_present && now < waking_until);
}
static void use(uint8_t i) {
    portENTER_CRITICAL(&lock);
    hid_engine_use(&engine, i);
    portEXIT_CRITICAL(&lock);
}
const uint8_t *tud_hid_descriptor_report_cb(uint8_t i) {
    use(i);
    return i == KEYBOARD   ? (compatible ? boot_descriptor : nkro_descriptor)
           : i == MOUSE    ? mouse_descriptor
           : i == CONSUMER ? consumer_descriptor
                           : NULL;
}
uint16_t tud_hid_get_report_cb(uint8_t i, uint8_t id, hid_report_type_t type, uint8_t *out,
                               uint16_t size) {
    if (i >= 3 || id)
        return 0;
    use(i);
    if (i == KEYBOARD && type == HID_REPORT_TYPE_OUTPUT && size) {
        out[0] = atomic_load(&leds);
        return 1;
    }
    if (type != HID_REPORT_TYPE_INPUT)
        return 0;
    portENTER_CRITICAL(&lock);
    uint8_t report[33];
    uint16_t n;
    if (i == KEYBOARD)
        n = keyboard_report(&engine.keyboard, engine.six_kro, report);
    else if (i == MOUSE) {
        mouse_report(engine.buttons, 0, 0, 0, 0, report);
        n = 7;
    } else {
        consumer_report(engine.consumer, report);
        n = 2;
    }
    if (n > size)
        n = size;
    memcpy(out, report, n);
    portEXIT_CRITICAL(&lock);
    return n;
}
void tud_hid_set_report_cb(uint8_t i, uint8_t id, hid_report_type_t type, const uint8_t *data,
                           uint16_t size) {
    if (i < 3)
        use(i);
    if (i == KEYBOARD && !id && type == HID_REPORT_TYPE_OUTPUT && size == 1)
        atomic_store(&leds, data[0] & 7);
}
void tud_hid_set_protocol_cb(uint8_t i, uint8_t protocol) {
    if (i != KEYBOARD)
        return;
    portENTER_CRITICAL(&lock);
    hid_engine_use(&engine, i);
    hid_engine_protocol(&engine, compatible || protocol == HID_PROTOCOL_BOOT,
                        esp_timer_get_time() / 1000);
    portEXIT_CRITICAL(&lock);
}
bool tud_hid_set_idle_cb(uint8_t i, uint8_t rate) {
    if (i >= 3)
        return false;
    portENTER_CRITICAL(&lock);
    hid_engine_use(&engine, i);
    idle[i] = rate;
    portEXIT_CRITICAL(&lock);
    return true;
}
void tud_hid_report_complete_cb(uint8_t i, const uint8_t *report, uint16_t size) {
    if (i >= 3)
        return;
    portENTER_CRITICAL(&lock);
    // Whatever completed is what the host now holds, including late reports.
    if (size <= 33) {
        memcpy(last_report[i], report, size);
        last_size[i] = (uint8_t)size;
        last_at[i] = now_ms();
    }
    hid_engine_report_done(&engine, i);
    portEXIT_CRITICAL(&lock);
}
static void device_event(tinyusb_event_t *event, void *arg) {
    (void)arg;
    uint64_t now = now_ms();
    bool waking = false;
    portENTER_CRITICAL(&lock);
    if (event->id == TINYUSB_EVENT_ATTACHED) {
        usb_present = true;
        usb_suspended = false;
        waking_until = 0;
        hid_engine_attach(&engine);
        engine.six_kro = compatible;
        memset(last_report, 0, sizeof(last_report));
        memset(idle, 0, sizeof(idle));
        atomic_store(&leds, 0);
        last_size[0] = compatible ? 8 : 33;
        last_size[1] = 7;
        last_size[2] = 2;
    } else if (event->id == TINYUSB_EVENT_SUSPENDED) {
        usb_present = true;
        usb_suspended = true;
        wake_at = 0;
        hid_engine_suspend(&engine);
    } else if (event->id == TINYUSB_EVENT_RESUMED) {
        usb_present = true;
        usb_suspended = false;
        waking_until = now + WAKE_GRACE_MS; // The host may still reset the bus.
        hid_engine_resume(&engine);
    } else if (event->id == TINYUSB_EVENT_DETACHED) {
        if (usb_suspended || now < waking_until) {
            waking_until = now + WAKE_GRACE_MS;
            waking = true;
        }
        usb_present = false;
        usb_suspended = false;
        hid_engine_detach(&engine);
    }
    portEXIT_CRITICAL(&lock);
    if (event->id == TINYUSB_EVENT_SUSPENDED)
        ESP_LOGI(TAG, "USB host suspended; HID remains enumerated and remote wake is available");
    else if (event->id == TINYUSB_EVENT_RESUMED)
        ESP_LOGI(TAG, "USB host resumed; restoring neutral HID state");
    else if (event->id == TINYUSB_EVENT_DETACHED)
        ESP_LOGI(TAG, "%s",
                 waking ? "USB bus reset while host wakes; keeping session for re-enumeration"
                        : "USB HID host detached");
}
uint32_t hid_release(void) {
    portENTER_CRITICAL(&lock);
    uint32_t gen = engine.generation + 1;
    hid_engine_release(&engine, gen);
    portEXIT_CRITICAL(&lock);
    return gen;
}
bool hid_ready(uint32_t generation) {
    uint64_t now = now_ms();
    portENTER_CRITICAL(&lock);
    // A sleeping host is released by suspend itself; pending-release still runs first
    // after it resumes, so the switch can complete and input can then wake it.
    bool ready = engine.generation == generation && (hid_engine_ready(&engine) || sleeping(now));
    portEXIT_CRITICAL(&lock);
    return ready;
}
bool hid_mounted(void) {
    uint64_t now = now_ms();
    portENTER_CRITICAL(&lock);
    bool value = usb_present || sleeping(now);
    portEXIT_CRITICAL(&lock);
    return value;
}
bool hid_sleeping(void) {
    uint64_t now = now_ms();
    portENTER_CRITICAL(&lock);
    bool value = sleeping(now);
    portEXIT_CRITICAL(&lock);
    return value;
}
void hid_wake_host(void) {
    bool request = false;
    uint64_t now = now_ms();
    portENTER_CRITICAL(&lock);
    // Retry on later input: the first attempt may precede the host's wake readiness.
    if (usb_present && usb_suspended && (!wake_at || now - wake_at >= 1000)) {
        wake_at = now;
        request = true;
    }
    portEXIT_CRITICAL(&lock);
    if (request) {
        esp_err_t err = tinyusb_remote_wakeup();
        if (err != ESP_OK)
            ESP_LOGW(TAG, "USB remote wake was not accepted by host: %s", esp_err_to_name(err));
    }
}
bool hid_healthy(uint32_t generation) {
    portENTER_CRITICAL(&lock);
    bool value = !engine.failed && engine.generation == generation;
    portEXIT_CRITICAL(&lock);
    return value;
}
uint8_t hid_leds(void) { return atomic_load(&leds); }
void hid_host_activity(uint32_t idle_s, bool known) {
    portENTER_CRITICAL(&lock);
    keep_awake_host(&awake, esp_timer_get_time() / 1000, idle_s, known);
    portEXIT_CRITICAL(&lock);
}
void hid_host_disconnected(void) {
    portENTER_CRITICAL(&lock);
    awake.heartbeat_seen = false;
    portEXIT_CRITICAL(&lock);
}
uint32_t hid_keep_awake_status(uint32_t *moves) {
    portENTER_CRITICAL(&lock);
    uint32_t state = keep_awake_state(&awake, esp_timer_get_time() / 1000);
    *moves = awake.moves;
    portEXIT_CRITICAL(&lock);
    return state;
}
bool hid_submit(const ek_Msg *m, uint32_t generation) {
    if (!message_validate(m, EK_TO_DEVICE))
        return false;
    hid_event event = {0};
    switch (m->which_body) {
    case ek_Msg_kb_tag: {
        event.kind = HID_KEYBOARD;
        event.value.keyboard.modifiers = (uint8_t)m->body.kb.mods;
        uint64_t words[] = {m->body.kb.keys_lo, m->body.kb.keys_hi, m->body.kb.keys_128_191,
                            m->body.kb.keys_192_255};
        for (unsigned i = 0; i < 32; i++)
            event.value.keyboard.keys[i] = (uint8_t)(words[i / 8] >> (8 * (i % 8)));
        break;
    }
    case ek_Msg_mouse_tag:
        event.kind = HID_MOUSE;
        event.value.mouse.buttons = m->body.mouse.buttons;
        event.value.mouse.x = m->body.mouse.dx;
        event.value.mouse.y = m->body.mouse.dy;
        event.value.mouse.wheel = m->body.mouse.wheel;
        event.value.mouse.pan = m->body.mouse.hwheel;
        break;
    case ek_Msg_consumer_tag:
        event.kind = HID_CONSUMER;
        event.value.consumer = m->body.consumer.usage;
        break;
    default:
        return false;
    }
    uint64_t now = esp_timer_get_time() / 1000;
    uint32_t random = esp_random();
    portENTER_CRITICAL(&lock);
    if (sleeping(now)) {
        if (engine.generation != generation) {
            portEXIT_CRITICAL(&lock);
            return false;
        }
        bool changed = event.kind == HID_KEYBOARD
            ? memcmp(&event.value.keyboard, &engine.keyboard, sizeof(engine.keyboard)) != 0
            : event.kind == HID_MOUSE
            ? event.value.mouse.buttons != engine.buttons || event.value.mouse.x || event.value.mouse.y ||
              event.value.mouse.wheel || event.value.mouse.pan
            : event.value.consumer != engine.consumer;
        if (changed) keep_awake_user(&awake, now, random);
        portEXIT_CRITICAL(&lock);
        hid_wake_host();
        return true;
    }
    bool changed = event.kind == HID_KEYBOARD
        ? memcmp(&event.value.keyboard, &engine.keyboard, sizeof(engine.keyboard)) != 0
        : event.kind == HID_MOUSE
        ? event.value.mouse.buttons != engine.buttons || event.value.mouse.x || event.value.mouse.y ||
          event.value.mouse.wheel || event.value.mouse.pan
        : event.value.consumer != engine.consumer;
    bool ok = hid_engine_offer(&engine, &event, generation, now);
    if (ok && changed) keep_awake_user(&awake, now, random);
    portEXIT_CRITICAL(&lock);
    return ok;
}
static void task(void *arg) {
    (void)arg;
    uint8_t report[33];
    for (;;) {
        uint64_t now = esp_timer_get_time() / 1000;
        uint32_t random = esp_random();
        unsigned i = 0;
        size_t n = 0;
        bool send = false;
        portENTER_CRITICAL(&lock);
        if (keep_awake_poll(&awake, now) && hid_engine_ready(&engine) &&
            !engine.in_flight && !engine.count && !engine.buttons && !engine.consumer) {
            keyboard_state empty = {0};
            if (!memcmp(&engine.keyboard, &empty, sizeof(empty))) {
                // Keep synthetic movement out of hid_submit: it must never renew C or A/B.
                hid_event move = {.kind = HID_MOUSE};
                move.value.mouse.x = (int)((random >> 8) % 7) - 3;
                move.value.mouse.y = (int)((random >> 16) % 7) - 3;
                if (!move.value.mouse.x && !move.value.mouse.y) move.value.mouse.x = 1;
                if (hid_engine_offer(&engine, &move, engine.generation, now))
                    keep_awake_moved(&awake, now, random);
            }
        }
        if (!engine.in_flight && engine.mounted) {
            // Core chooses the next interface before endpoint readiness is tested. A failed
            // submission retains the FIFO item and every unconsumed movement remainder.
            send = hid_engine_next(&engine, now);
            if (!send && hid_engine_ready(&engine)) {
                for (unsigned j = 0; j < 3 && !send; j++)
                    if (idle[j] && now - last_at[j] >= idle[j] * 4u) {
                        uint8_t repeat[33];
                        memcpy(repeat, last_report[j], last_size[j]);
                        if (j == MOUSE)
                            memset(repeat + 1, 0, 6); // Never repeat relative movement.
                        send = hid_engine_repeat(&engine, j, repeat, last_size[j], now);
                    }
            }
            if (send) {
                i = engine.interface;
                n = engine.report_size;
                memcpy(report, engine.report, n);
            }
        } else
            (void)hid_engine_next(&engine, now);
        portEXIT_CRITICAL(&lock);
        if (send && (!tud_hid_n_ready(i) || !tud_hid_n_report(i, 0, report, (uint16_t)n))) {
            portENTER_CRITICAL(&lock);
            hid_engine_complete(&engine, false);
            portEXIT_CRITICAL(&lock);
        }
        vTaskDelay(pdMS_TO_TICKS(1));
    }
}
esp_err_t hid_service_start(const ek_Config *config) {
    if (!config->usb_vid || !config->usb_pid)
        return ESP_ERR_INVALID_ARG;
    compatible = config->six_kro;
    ek_KeepAwake policy = config->has_keep_awake ? config->keep_awake :
        (ek_KeepAwake){.interval_min_s = 30, .interval_max_s = 60,
            .stop_after_s = 1800, .host_active_within_s = 120};
    keep_awake_init(&awake, (keep_awake_config){policy.enabled, policy.interval_min_s,
        policy.interval_max_s, policy.stop_after_s, policy.host_active_within_s},
        esp_timer_get_time() / 1000, esp_random());
    engine.six_kro = compatible;
    engine.pending_release = true;
    device_descriptor.idVendor = config->usb_vid;
    device_descriptor.idProduct = config->usb_pid;
    tinyusb_config_t usb = TINYUSB_DEFAULT_CONFIG(device_event);
    usb.descriptor.device = &device_descriptor;
    usb.descriptor.string = strings;
    usb.descriptor.string_count = 3;
    usb.descriptor.full_speed_config = compatible ? compatible_configuration : normal_configuration;
    esp_err_t e = tinyusb_driver_install(&usb);
    if (e != ESP_OK) {
        ESP_LOGE(TAG, "TinyUSB install failed: %s", esp_err_to_name(e));
        return e;
    }
    ESP_LOGI(TAG, "TinyUSB installed; waiting for host enumeration");
    BaseType_t created = xTaskCreate(task, "hid", 4096, NULL, configMAX_PRIORITIES - 3, NULL);
    ESP_LOGI(TAG, "HID worker start: %s", created == pdPASS ? "ok" : "failed");
    return created == pdPASS
               ? ESP_OK
               : ESP_ERR_NO_MEM;
}
