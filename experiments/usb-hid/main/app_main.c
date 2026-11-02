#include "driver/gpio.h"
#include "esp_log.h"
#include "esp_system.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "nvs.h"
#include "nvs_flash.h"
#include "usb_device.h"
#ifndef CONFIG_HID_TEST_FORCE_6KRO
#define CONFIG_HID_TEST_FORCE_6KRO 0
#endif

static const char *TAG = "hid-test";
static nvs_handle_t preferences;

static bool type_test(void) {
    const uint8_t usages[] = {8, 22, 19, 14, 25, 16}; // "espkvm", US layout
    if (!usb_test_release())
        return false;
    for (unsigned i = 0; i < sizeof(usages); i++) {
        keyboard_state state = {0};
        state.keys[usages[i] / 8] = 1u << (usages[i] % 8);
        if (!usb_test_keyboard(&state))
            return false;
        vTaskDelay(pdMS_TO_TICKS(25));
        state = (keyboard_state){0};
        if (!usb_test_keyboard(&state))
            return false;
        vTaskDelay(pdMS_TO_TICKS(25));
    }
    if (!usb_test_mouse(0, 20, 0, 0, 0) || !usb_test_mouse(0, -20, 0, 0, 0))
        return false;
    return usb_test_release();
}

void app_main(void) {
    // Never erase NVS automatically: preserve compatibility mode across failures.
    ESP_ERROR_CHECK(nvs_flash_init());
    ESP_ERROR_CHECK(nvs_open("hid_test", NVS_READWRITE, &preferences));
    uint8_t stored = 0;
    esp_err_t result = nvs_get_u8(preferences, "six_kro", &stored);
    bool compatible = result == ESP_ERR_NVS_NOT_FOUND ? CONFIG_HID_TEST_FORCE_6KRO : stored != 0;
    if (result != ESP_OK && result != ESP_ERR_NVS_NOT_FOUND)
        ESP_ERROR_CHECK(result);
    result = usb_device_start(compatible, CONFIG_HID_TEST_USB_VID, CONFIG_HID_TEST_USB_PID);
    if (result != ESP_OK) {
        ESP_LOGE(TAG, "Set authorized HID_TEST_USB_VID/PID in menuconfig before hardware use");
        return;
    }
    gpio_config_t button = {.pin_bit_mask = 1ULL << CONFIG_HID_TEST_BUTTON_GPIO,
                            .mode = GPIO_MODE_INPUT,
                            .pull_up_en = GPIO_PULLUP_ENABLE};
    ESP_ERROR_CHECK(gpio_config(&button));
    ESP_LOGI(TAG, "Mode: %s; after mount hold button 1-3s and release to type; >=3s toggles mode",
             compatible ? "fixed 6KRO" : "NKRO / Boot");
    int64_t pressed_at = 0;
    bool armed = false;
    uint8_t last_leds = 0xff;
    for (;;) {
        bool available = usb_device_poll();
        uint8_t current_leds = usb_device_leds();
        if (available && current_leds != last_leds) {
            ESP_LOGI(TAG, "Keyboard LED bits: %u", (unsigned)current_leds);
            last_leds = current_leds;
        }
        bool down = gpio_get_level(CONFIG_HID_TEST_BUTTON_GPIO) == 0;
        // Require a released button after enumeration; holding at reset never types.
        if (!available) {
            pressed_at = 0;
            armed = false;
        } else if (!down && !armed)
            armed = true;
        else if (armed && down && !pressed_at)
            pressed_at = esp_timer_get_time();
        else if (armed && !down && pressed_at) {
            int64_t held = esp_timer_get_time() - pressed_at;
            pressed_at = 0;
            if (held >= 3000000) {
                if (usb_test_release()) {
                    ESP_ERROR_CHECK(nvs_set_u8(preferences, "six_kro", !compatible));
                    ESP_ERROR_CHECK(nvs_commit(preferences));
                    ESP_LOGI(TAG, "Saved keyboard descriptor mode; rebooting");
                    esp_restart();
                }
            } else if (held >= 1000000) {
                bool success = type_test();
                ESP_LOGI(TAG, "Test %s (USB completion, not proof of application receipt)",
                         success ? "completed" : "failed");
                if (!success)
                    usb_test_release();
            }
        }
        vTaskDelay(1);
    }
}
