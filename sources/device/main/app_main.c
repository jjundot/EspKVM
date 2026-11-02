#include "config_store.h"
#include "ek_crypto.h"
#include "esp_err.h"
#include "hid_service.h"
#include "network_service.h"
#include "serial_service.h"
#include "esp_log.h"
#include "esp_err.h"
static const char *TAG = "espkvm";
void app_main(void) {
    ESP_LOGI(TAG, "boot: config init");
    ESP_ERROR_CHECK(config_init());
    static ek_Config config;
    bool paired = config_get(&config, false);
    // Keep the USB keyboard/mouse available with safe defaults before network
    // provisioning; TCP configuration is exposed only after pairing.
    esp_err_t hid_err = hid_service_start(&config);
    ESP_LOGI(TAG, "HID start: %s (VID=%04x PID=%04x paired=%u)", esp_err_to_name(hid_err),
             config.usb_vid, config.usb_pid, paired);
    ESP_ERROR_CHECK(hid_err);
    if (paired) {
        ESP_ERROR_CHECK(network_service_start(&config));
    }
    ek_zero(&config, sizeof(config));
    ESP_ERROR_CHECK(serial_service_start());
}
