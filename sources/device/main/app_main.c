#include "config_store.h"
#include "esp_err.h"
#include "serial_service.h"
// Provisioning bring-up: no input endpoint is exposed until HID/session integration.
void app_main(void) {
    ESP_ERROR_CHECK(config_init());
    ESP_ERROR_CHECK(serial_service_start());
}
