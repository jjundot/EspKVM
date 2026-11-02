#include "hid_service.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "hid_engine.h"
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
    TUD_CONFIG_DESCRIPTOR(1, INTERFACE_COUNT, 0, CONFIGURATION_LENGTH, 0, 100),                    \
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
static bool compatible;
static atomic_uchar leds;
static portMUX_TYPE lock = portMUX_INITIALIZER_UNLOCKED;
static uint8_t last_report[3][33], last_size[3], idle[3];
static uint64_t last_at[3];
const uint8_t *tud_hid_descriptor_report_cb(uint8_t i) {
    return i == KEYBOARD   ? (compatible ? boot_descriptor : nkro_descriptor)
           : i == MOUSE    ? mouse_descriptor
           : i == CONSUMER ? consumer_descriptor
                           : NULL;
}
uint16_t tud_hid_get_report_cb(uint8_t i, uint8_t id, hid_report_type_t type, uint8_t *out,
                               uint16_t size) {
    if (i >= 3 || id)
        return 0;
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
    if (i == KEYBOARD && !id && type == HID_REPORT_TYPE_OUTPUT && size == 1)
        atomic_store(&leds, data[0] & 7);
}
void tud_hid_set_protocol_cb(uint8_t i, uint8_t protocol) {
    if (i != KEYBOARD)
        return;
    portENTER_CRITICAL(&lock);
    hid_engine_protocol(&engine, compatible || protocol == HID_PROTOCOL_BOOT,
                        esp_timer_get_time() / 1000);
    portEXIT_CRITICAL(&lock);
}
bool tud_hid_set_idle_cb(uint8_t i, uint8_t rate) {
    if (i >= 3)
        return false;
    portENTER_CRITICAL(&lock);
    idle[i] = rate;
    portEXIT_CRITICAL(&lock);
    return true;
}
void tud_hid_report_complete_cb(uint8_t i, const uint8_t *report, uint16_t size) {
    portENTER_CRITICAL(&lock);
    if (engine.in_flight && engine.interface == i) {
        if (size <= 33) {
            memcpy(last_report[i], report, size);
            last_size[i] = (uint8_t)size;
            last_at[i] = esp_timer_get_time() / 1000;
        }
        hid_engine_complete(&engine, true);
    }
    portEXIT_CRITICAL(&lock);
}
static void device_event(tinyusb_event_t *event, void *arg) {
    (void)arg;
    portENTER_CRITICAL(&lock);
    if (event->id == TINYUSB_EVENT_ATTACHED || event->id == TINYUSB_EVENT_RESUMED) {
        hid_engine_mount(&engine, true);
        engine.six_kro = compatible;
        memset(last_report, 0, sizeof(last_report));
        memset(idle, 0, sizeof(idle));
        atomic_store(&leds, 0);
        last_size[0] = compatible ? 8 : 33;
        last_size[1] = 7;
        last_size[2] = 2;
    } else if (event->id == TINYUSB_EVENT_DETACHED || event->id == TINYUSB_EVENT_SUSPENDED)
        hid_engine_mount(&engine, false);
    portEXIT_CRITICAL(&lock);
}
uint32_t hid_release(void) {
    portENTER_CRITICAL(&lock);
    uint32_t gen = engine.generation + 1;
    hid_engine_release(&engine, gen);
    portEXIT_CRITICAL(&lock);
    return gen;
}
bool hid_ready(uint32_t generation) {
    portENTER_CRITICAL(&lock);
    bool ready = hid_engine_ready(&engine) && engine.generation == generation;
    portEXIT_CRITICAL(&lock);
    return ready;
}
bool hid_mounted(void) {
    portENTER_CRITICAL(&lock);
    bool value = engine.mounted;
    portEXIT_CRITICAL(&lock);
    return value;
}
bool hid_healthy(uint32_t generation) {
    portENTER_CRITICAL(&lock);
    bool value = !engine.failed && engine.generation == generation;
    portEXIT_CRITICAL(&lock);
    return value;
}
uint8_t hid_leds(void) { return atomic_load(&leds); }
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
    portENTER_CRITICAL(&lock);
    bool ok = hid_engine_offer(&engine, &event, generation, esp_timer_get_time() / 1000);
    portEXIT_CRITICAL(&lock);
    return ok;
}
static void task(void *arg) {
    (void)arg;
    uint8_t report[33];
    for (;;) {
        uint64_t now = esp_timer_get_time() / 1000;
        unsigned i = 0;
        size_t n = 0;
        bool send = false;
        portENTER_CRITICAL(&lock);
        if (!engine.in_flight && engine.mounted) {
            // Core chooses the next interface before endpoint readiness is tested. A failed
            // submission retains the FIFO item and every unconsumed movement remainder.
            send = hid_engine_next(&engine, now);
            if (!send && hid_engine_ready(&engine)) {
                for (unsigned j = 0; j < 3; j++)
                    if (idle[j] && now - last_at[j] >= idle[j] * 4u) {
                        engine.interface = j;
                        engine.report_size = last_size[j];
                        memcpy(engine.report, last_report[j], last_size[j]);
                        if (j == MOUSE)
                            memset(engine.report + 1, 0, 6); // Never repeat relative movement.
                        engine.barrier_report = false;
                        engine.submitted_generation = engine.generation;
                        engine.submitted_epoch = engine.epoch;
                        engine.submitted_at = now;
                        engine.in_flight = true;
                        send = true;
                        break;
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
