#include "usb_device.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "tinyusb.h"
#include "tinyusb_default_config.h"
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
                                               .idVendor = CONFIG_HID_TEST_USB_VID,
                                               .idProduct = CONFIG_HID_TEST_USB_PID,
                                               .bcdDevice = 0x0100,
                                               .iManufacturer = 1,
                                               .iProduct = 2,
                                               .bNumConfigurations = 1};
static const char *strings[] = {(const char[]){0x09, 0x04}, "EspKVM", "EspKVM HID test"};
static bool compatible;
static atomic_bool boot_protocol, mounted, in_flight, pending_release, rebuild_keyboard;
static atomic_uint completed, epoch, released_epoch;
static atomic_uchar leds, idle_rates[INTERFACE_COUNT];
static int64_t last_submission[INTERFACE_COUNT];
static portMUX_TYPE state_lock = portMUX_INITIALIZER_UNLOCKED;
static keyboard_state current_keyboard;
static uint8_t current_buttons;
static uint16_t current_consumer;

const uint8_t *tud_hid_descriptor_report_cb(uint8_t instance) {
    switch (instance) {
    case KEYBOARD:
        return compatible ? boot_descriptor : nkro_descriptor;
    case MOUSE:
        return mouse_descriptor;
    case CONSUMER:
        return consumer_descriptor;
    default:
        return NULL;
    }
}

static size_t snapshot(uint8_t instance, uint8_t report[33]) {
    size_t length = 0;
    portENTER_CRITICAL(&state_lock);
    switch (instance) {
    case KEYBOARD:
        length =
            keyboard_report(&current_keyboard, compatible || atomic_load(&boot_protocol), report);
        break;
    case MOUSE:
        mouse_report(current_buttons, 0, 0, 0, 0, report);
        length = 7;
        break;
    case CONSUMER:
        consumer_report(current_consumer, report);
        length = 2;
        break;
    }
    portEXIT_CRITICAL(&state_lock);
    return length;
}

uint16_t tud_hid_get_report_cb(uint8_t instance, uint8_t report_id, hid_report_type_t type,
                               uint8_t *buffer, uint16_t requested) {
    if (report_id != 0)
        return 0;
    if (instance == KEYBOARD && type == HID_REPORT_TYPE_OUTPUT && requested) {
        buffer[0] = atomic_load(&leds);
        return 1;
    }
    if (type != HID_REPORT_TYPE_INPUT)
        return 0;
    uint8_t report[33];
    size_t length = snapshot(instance, report);
    if (length > requested)
        length = requested;
    memcpy(buffer, report, length);
    return (uint16_t)length;
}

void tud_hid_set_report_cb(uint8_t instance, uint8_t report_id, hid_report_type_t type,
                           const uint8_t *buffer, uint16_t size) {
    if (instance == KEYBOARD && report_id == 0 && type == HID_REPORT_TYPE_OUTPUT && size == 1)
        atomic_store(&leds, buffer[0] & 7);
}
void tud_hid_set_protocol_cb(uint8_t instance, uint8_t protocol) {
    if (instance == KEYBOARD) {
        atomic_store(&boot_protocol, protocol == HID_PROTOCOL_BOOT);
        atomic_store(&rebuild_keyboard, true);
    }
}
bool tud_hid_set_idle_cb(uint8_t instance, uint8_t rate) {
    if (instance >= INTERFACE_COUNT)
        return false;
    atomic_store(&idle_rates[instance], rate);
    return true;
}
void tud_hid_report_complete_cb(uint8_t instance, const uint8_t *report, uint16_t size) {
    (void)instance;
    (void)report;
    (void)size;
    atomic_fetch_add(&completed, 1);
    atomic_store(&in_flight, false);
}
static void on_mount(void) {
    atomic_store(&boot_protocol, false);
    atomic_store(&rebuild_keyboard, false);
    atomic_store(&leds, 0);
    for (unsigned i = 0; i < INTERFACE_COUNT; i++)
        atomic_store(&idle_rates[i], 0);
    atomic_fetch_add(&epoch, 1);
    atomic_store(&pending_release, true);
    atomic_store(&mounted, true);
}
static void on_unmount(void) {
    atomic_store(&mounted, false);
    atomic_fetch_add(&epoch, 1);
    atomic_store(&in_flight, false);
    atomic_store(&pending_release, true);
    portENTER_CRITICAL(&state_lock);
    current_keyboard = (keyboard_state){0};
    current_buttons = 0;
    current_consumer = 0;
    portEXIT_CRITICAL(&state_lock);
}
static void device_event(tinyusb_event_t *event, void *argument) {
    (void)argument;
    if (event->id == TINYUSB_EVENT_ATTACHED)
        on_mount();
    if (event->id == TINYUSB_EVENT_DETACHED)
        on_unmount();
    if (event->id == TINYUSB_EVENT_SUSPENDED)
        on_unmount();
    if (event->id == TINYUSB_EVENT_RESUMED) {
        atomic_fetch_add(&epoch, 1);
        atomic_store(&pending_release, true);
        atomic_store(&mounted, true);
    }
}

// This experiment has exactly one application producer. It deliberately waits
// for completion, making quick down/up transitions observable on the USB bus.
static bool submit(uint8_t instance, const uint8_t *report, size_t length) {
    int64_t deadline = esp_timer_get_time() + 100000;
    unsigned generation = atomic_load(&epoch);
    while (atomic_load(&mounted) && (atomic_load(&in_flight) || !tud_hid_n_ready(instance))) {
        if (esp_timer_get_time() >= deadline)
            return false;
        vTaskDelay(1);
    }
    if (!atomic_load(&mounted))
        return false;
    unsigned before = atomic_load(&completed);
    atomic_store(&in_flight, true);
    if (!tud_hid_n_report(instance, 0, report, (uint16_t)length)) {
        atomic_store(&in_flight, false);
        return false;
    }
    while (atomic_load(&completed) == before) {
        if (atomic_load(&epoch) != generation || !atomic_load(&mounted) ||
            esp_timer_get_time() >= deadline)
            return false;
        vTaskDelay(1);
    }
    last_submission[instance] = esp_timer_get_time();
    return atomic_load(&epoch) == generation;
}

bool usb_device_mounted(void) { return atomic_load(&mounted); }
uint8_t usb_device_leds(void) { return atomic_load(&leds); }
bool usb_test_release(void) {
    unsigned generation = atomic_load(&epoch);
    atomic_store(&pending_release, true);
    portENTER_CRITICAL(&state_lock);
    current_keyboard = (keyboard_state){0};
    current_buttons = 0;
    current_consumer = 0;
    portEXIT_CRITICAL(&state_lock);
    for (unsigned i = 0; i < INTERFACE_COUNT; i++) {
        uint8_t report[33];
        size_t length = snapshot(i, report);
        if (!submit(i, report, length))
            return false;
    }
    if (atomic_load(&epoch) != generation)
        return false;
    atomic_store(&released_epoch, generation);
    atomic_store(&pending_release, false);
    return true;
}
bool usb_device_poll(void) {
    if (!usb_device_mounted())
        return false;
    if ((atomic_load(&pending_release) || atomic_load(&released_epoch) != atomic_load(&epoch)) &&
        !usb_test_release())
        return false;
    bool rebuild = atomic_exchange(&rebuild_keyboard, false);
    for (unsigned i = 0; i < INTERFACE_COUNT; i++) {
        unsigned rate = atomic_load(&idle_rates[i]);
        if ((i == KEYBOARD && rebuild) ||
            (rate && esp_timer_get_time() - last_submission[i] >= rate * 4000)) {
            uint8_t report[33];
            size_t length = snapshot(i, report);
            if (!submit(i, report, length)) {
                atomic_store(&pending_release, true);
                return false;
            }
        }
    }
    return true;
}
bool usb_test_keyboard(const keyboard_state *state) {
    if (!usb_device_poll())
        return false;
    portENTER_CRITICAL(&state_lock);
    current_keyboard = *state;
    portEXIT_CRITICAL(&state_lock);
    uint8_t report[33];
    size_t length = snapshot(KEYBOARD, report);
    bool result = submit(KEYBOARD, report, length);
    if (!result)
        atomic_store(&pending_release, true);
    return result;
}
bool usb_test_mouse(uint8_t buttons, int16_t dx, int16_t dy, int8_t wheel, int8_t pan) {
    if (!usb_device_poll())
        return false;
    portENTER_CRITICAL(&state_lock);
    current_buttons = buttons;
    portEXIT_CRITICAL(&state_lock);
    uint8_t report[7];
    mouse_report(buttons, dx, dy, wheel, pan, report);
    bool result = submit(MOUSE, report, sizeof(report));
    if (!result)
        atomic_store(&pending_release, true);
    return result;
}
esp_err_t usb_device_start(bool six_kro, uint16_t vid, uint16_t pid) {
    if (!vid || !pid)
        return ESP_ERR_INVALID_ARG;
    device_descriptor.idVendor = vid;
    device_descriptor.idProduct = pid;
    compatible = six_kro;
    tinyusb_config_t config = TINYUSB_DEFAULT_CONFIG(device_event);
    config.descriptor.device = &device_descriptor;
    config.descriptor.string = strings;
    config.descriptor.string_count = sizeof(strings) / sizeof(strings[0]);
    config.descriptor.full_speed_config =
        compatible ? compatible_configuration : normal_configuration;
    return tinyusb_driver_install(&config);
}
